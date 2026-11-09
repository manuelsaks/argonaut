package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	cblog "github.com/charmbracelet/log"
	"github.com/darksworm/argonaut/pkg/model"
)

// pagerDoneMsg signals that an external pager has closed
type pagerDoneMsg struct{ Err error }
type pauseRenderingMsg struct{}
type resumeRenderingMsg struct{}

// textPagerMsg transfers content to the UI loop without requiring an external
// pager. Capture the epoch before starting the operation that produces it.
type textPagerMsg struct {
	Title, Text string
	SwitchEpoch int
}

// SetProgram stores the Bubble Tea program pointer for terminal hand-off
func (m *Model) SetProgram(p *tea.Program) { m.program = p }

// openTextPager uses the built-in pager on Windows or when less is unavailable.
func (m *Model) openTextPager(title, text string) tea.Cmd {
	return m.openTextPagerForEpoch(title, text, m.switchEpoch)
}

func (m *Model) openTextPagerForEpoch(title, text string, epoch int) tea.Cmd {
	if runtime.GOOS == "windows" || !inPath("less") {
		return func() tea.Msg { return textPagerMsg{Title: title, Text: text, SwitchEpoch: epoch} }
	}
	return func() tea.Msg {
		if m.program != nil {
			m.program.Send(pauseRenderingMsg{})
			_ = m.program.ReleaseTerminal()
		}
		defer func() {
			// Clear screen and restore terminal to Bubble Tea
			fmt.Print("\x1b[2J\x1b[H")
			time.Sleep(150 * time.Millisecond)
			if m.program != nil {
				_ = m.program.RestoreTerminal()
				m.program.Send(resumeRenderingMsg{})
			}
		}()

		// Use less -R for everything (handles ANSI codes properly)
		c := exec.Command("less", "-R")
		c.Stdin = strings.NewReader(text)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			cblog.With("component", "pager").Error("Failed to run less", "err", err)
			return pagerDoneMsg{Err: err}
		}
		return pagerDoneMsg{Err: nil}
	}
}

// openInteractiveDiffViewer replaces the terminal with an interactive diff tool
// configured via ARGONAUT_DIFF_VIEWER. The command may include {left} and {right}
// placeholders for file paths.
func (m *Model) openInteractiveDiffViewer(leftFile, rightFile, cmdStr string) tea.Msg {
	if m.program != nil {
		m.program.Send(pauseRenderingMsg{})
		_ = m.program.ReleaseTerminal()
	}
	defer func() {
		fmt.Print("\x1b[2J\x1b[H")
		time.Sleep(150 * time.Millisecond)
		if m.program != nil {
			_ = m.program.RestoreTerminal()
			m.program.Send(resumeRenderingMsg{})
		}
	}()

	c := diffViewerCommand(cmdStr, leftFile, rightFile)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		cblog.With("component", "diff").Error("interactive diff viewer failed", "err", err)
		return pagerDoneMsg{Err: err}
	}
	return pagerDoneMsg{Err: nil}
}

// runDiffFormatterWithTitle runs a diff formatter with optional resource name for better headers
func (m *Model) runDiffFormatterWithTitle(diffText string, resourceName string) (string, error) {
	cmdStr := m.config.GetDiffFormatter()
	cols := 0
	if m.state != nil {
		cols = m.state.Terminal.Cols
	}

	usingDelta := false
	if cmdStr == "" && inPath("delta") {
		usingDelta = true
		if cols > 0 {
			cmdStr = fmt.Sprintf("delta --side-by-side --line-numbers --navigate --paging=never --width=%d", cols)
		} else {
			cmdStr = "delta --side-by-side --line-numbers --navigate --paging=never"
		}
	}

	if cmdStr == "" {
		return diffText, nil
	}
	var c *exec.Cmd
	if usingDelta {
		// Auto-detected native executables do not need a shell.
		args := []string{"--side-by-side", "--line-numbers", "--navigate", "--paging=never"}
		if cols > 0 {
			args = append(args, fmt.Sprintf("--width=%d", cols))
		}
		c = exec.Command("delta", args...)
	} else {
		c = configuredShellCommand(cmdStr, true)
	}
	if cols > 0 {
		c.Env = append(os.Environ(), fmt.Sprintf("COLUMNS=%d", cols))
	} else {
		c.Env = os.Environ()
	}
	c.Stdin = strings.NewReader(diffText)
	out, err := c.Output()
	if err != nil {
		return diffText, err
	}

	// Post-process delta's output to clean up the header if we have a resource name
	if usingDelta && resourceName != "" {
		return rewriteDeltaHeader(string(out), resourceName), nil
	}

	return string(out), nil
}

// rewriteDeltaHeader replaces delta's temp-file-path header line with a clean
// "<resource>: Live ⟶ Desired" header, preserving surrounding ANSI codes.
func rewriteDeltaHeader(output, resourceName string) string {
	lines := strings.Split(output, "\n")

	// Look for the delta header line (contains the file paths)
	for i, line := range lines {
		// Delta's header typically contains "Δ" and the file paths
		if strings.Contains(line, "Δ") && strings.Contains(line, os.TempDir()) {
			// Extract ANSI codes to preserve colors
			// Delta typically uses color codes at the start and resets at the end
			ansiPrefix := ""
			if idx := strings.Index(line, "Δ"); idx > 0 {
				ansiPrefix = line[:idx]
			}

			// Check if line ends with ANSI reset code
			ansiSuffix := ""
			if strings.HasSuffix(line, "\x1b[0m") || strings.HasSuffix(line, "[0m") {
				// Line already has reset code, we'll add it back
				ansiSuffix = "\x1b[0m"
			}

			// Replace with clean header
			lines[i] = fmt.Sprintf("%sΔ %s: Live ⟶ Desired%s", ansiPrefix, resourceName, ansiSuffix)
			break
		}
	}

	return strings.Join(lines, "\n")
}

func inPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// Paths stay in environment values rather than being interpolated into shell
// code. A filename containing quotes or shell metacharacters remains one arg.
func diffViewerCommand(command, left, right string) *exec.Cmd {
	for _, binding := range []struct{ placeholder, variable string }{
		{"{left}", "ARGONAUT_DIFF_LEFT"}, {"{right}", "ARGONAUT_DIFF_RIGHT"},
	} {
		argument := shellEnvironmentArgument(binding.variable)
		command = strings.NewReplacer(`"`+binding.placeholder+`"`, argument,
			"'"+binding.placeholder+"'", argument, binding.placeholder, argument).Replace(command)
	}
	c := configuredShellCommand(command, false)
	c.Env = append(os.Environ(), "ARGONAUT_DIFF_LEFT="+left, "ARGONAUT_DIFF_RIGHT="+right)
	return c
}

func (m *Model) showTextPager(msg textPagerMsg) {
	m.state.Diff = &model.DiffState{Title: msg.Title, Content: strings.Split(strings.ReplaceAll(msg.Text, "\r\n", "\n"), "\n")}
	m.state.Mode = model.ModeDiff
}

// k9sResourceMap maps Kubernetes resource kinds to k9s resource aliases
var k9sResourceMap = map[string]string{
	"Pod":                     "pod",
	"Deployment":              "deploy",
	"Service":                 "svc",
	"Ingress":                 "ing",
	"ConfigMap":               "cm",
	"Secret":                  "secret",
	"ReplicaSet":              "rs",
	"StatefulSet":             "sts",
	"DaemonSet":               "ds",
	"Job":                     "job",
	"CronJob":                 "cj",
	"PersistentVolumeClaim":   "pvc",
	"PersistentVolume":        "pv",
	"ServiceAccount":          "sa",
	"Namespace":               "ns",
	"Node":                    "node",
	"Event":                   "event",
	"Endpoints":               "ep",
	"HorizontalPodAutoscaler": "hpa",
	"NetworkPolicy":           "netpol",
	"Role":                    "role",
	"RoleBinding":             "rolebinding",
	"ClusterRole":             "clusterrole",
	"ClusterRoleBinding":      "clusterrolebinding",
	"Application":             "applications.argoproj.io",
}

// k9sDoneMsg signals that k9s has exited
type k9sDoneMsg struct{ Err error }
