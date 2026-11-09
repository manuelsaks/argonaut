package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Executed in a separate native Windows process, under the actual PowerShell
// command path. It receives filenames as arguments and formatter data on stdin.
func TestWindowsExternalToolHelper(t *testing.T) {
	mode := os.Getenv("ARGONAUT_TEST_HELPER")
	if mode == "" {
		return
	}
	if mode == "fail" {
		os.Exit(7)
	}
	if mode == "formatter" {
		_, _ = io.Copy(os.Stdout, os.Stdin)
		os.Exit(0)
	}
	for i, arg := range os.Args {
		if arg == "--" && len(os.Args) == i+3 {
			for _, path := range os.Args[i+1:] {
				if _, err := os.ReadFile(path); err != nil {
					os.Exit(9)
				}
			}
			_ = json.NewEncoder(os.Stdout).Encode(os.Args[i+1:])
			os.Exit(0)
		}
	}
	os.Exit(8)
}

func windowsHelperCommand(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The executable itself and both argument paths may contain spaces, quotes,
	// Unicode and metacharacters; no POSIX shell is available in the test PATH.
	dir := filepath.Join(t.TempDir(), "helper's & folder")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "native helper.exe")
	input, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.Create(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	return "'" + strings.ReplaceAll(target, "'", "''") + "' '-test.run=^TestWindowsExternalToolHelper$' '--'"
}

func TestWindowsViewerPassesPathsLiterallyWithoutSh(t *testing.T) {
	command := windowsHelperCommand(t)
	t.Setenv("PATH", t.TempDir())
	left := filepath.Join(t.TempDir(), "left żółć ' & ; $(whoami) %x%!.yaml")
	right := filepath.Join(t.TempDir(), "right [1] $HOME & '.yaml")
	for _, path := range []string{left, right} {
		if err := os.WriteFile(path, []byte("dummy"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, placeholders := range []string{` {left} {right}`, ` "{left}" '{right}'`} {
		cmd := diffViewerCommand(command+placeholders, left, right)
		cmd.Env = append(cmd.Env, "ARGONAUT_TEST_HELPER=viewer")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("native viewer: %v: %s", err, out)
		}
		var args []string
		if err := json.Unmarshal(out, &args); err != nil {
			t.Fatalf("viewer output: %v: %q", err, out)
		}
		if len(args) != 2 || args[0] != left || args[1] != right {
			t.Fatalf("filename was interpreted as shell code: %q", args)
		}
	}
}

func TestWindowsFormatterPreservesUnicodeAndExitCodeWithoutSh(t *testing.T) {
	command := windowsHelperCommand(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("ARGONAUT_TEST_HELPER", "formatter")
	m := buildSyncTestModel(80, 24)
	m.config.Diff.Formatter = command
	want := "-zażółć gęślą jaźń\n+changed 世界\n"
	out, err := m.runDiffFormatterWithTitle(want, "demo")
	if err != nil {
		t.Fatalf("formatter: %v: %s", err, out)
	}
	if strings.ReplaceAll(out, "\r\n", "\n") != want {
		t.Fatalf("formatter corrupted stdin/stdout: %q", out)
	}
	t.Setenv("ARGONAUT_TEST_HELPER", "fail")
	if _, err := m.runDiffFormatterWithTitle(want, "demo"); err == nil {
		t.Fatal("formatter exit code was lost")
	}
}
