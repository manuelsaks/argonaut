package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/config"
	apperrors "github.com/darksworm/argonaut/pkg/errors"
	"github.com/darksworm/argonaut/pkg/model"
)

type contextReloginMsg struct {
	ContextName string
	SwitchEpoch int
	RequestID   int
	Server      *model.Server
	Interactive bool
	Error       error
}

func requiresRelogin(err error) bool {
	if err == nil {
		return false
	}
	if argErr, ok := errors.AsType[*apperrors.ArgonautError](err); ok {
		return argErr != nil && !hasHTTPStatusCtx(argErr, 403) && argErr.Category == apperrors.ErrorAuth
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "403") || strings.Contains(text, "forbidden") || strings.Contains(text, "permission denied") {
		return false
	}
	return strings.Contains(text, "401") || strings.Contains(text, "unauthenticated") || strings.Contains(text, "unauthorized") || strings.Contains(text, "token expired") || strings.Contains(text, "token is expired") || strings.Contains(text, "invalid session")
}

func (m *Model) selectedArgoContext() string {
	if m.state.Navigation.View == model.ViewContexts {
		items := m.getVisibleItems()
		i := m.state.Navigation.SelectedIdx
		if i >= 0 && i < len(items) {
			return items[i].(string)
		}
		return ""
	}
	return m.currentContextName
}

func (m *Model) toggleContextAutoRelogin() (tea.Model, tea.Cmd) {
	name := m.selectedArgoContext()
	if name == "" {
		return m, m.showFooterNotice("Select an Argo CD context first")
	}
	next := *m.config
	next.Auth.AutoRelogin = maps.Clone(m.config.Auth.AutoRelogin)
	if next.Auth.AutoRelogin == nil {
		next.Auth.AutoRelogin = make(map[string]bool)
	}
	enabled := !next.IsAutoReloginEnabled(name)
	next.Auth.AutoRelogin[name] = enabled
	if err := config.SaveArgonautConfig(&next); err != nil {
		return m, m.showFooterNotice("Cannot save auto-login preference: " + err.Error())
	}
	m.config = &next
	state := "OFF"
	if enabled {
		state = "ON"
	}
	notice := m.showFooterNotice("Auto-login " + state + ": " + name)
	if enabled && name == m.currentContextName {
		return m, tea.Batch(notice, m.validateAuthentication())
	}
	return m, notice
}

// Bind every CLI invocation to a named Argo CD context. Never invoke a shell
// or pass a password/token in arguments. CLI errors/output are not logged.
func argoCDCommand(ctx context.Context, executable, configPath, contextName string, args ...string) (*exec.Cmd, error) {
	if contextName == "" {
		return nil, fmt.Errorf("no Argo CD context selected")
	}
	if _, err := exec.LookPath(executable); err != nil {
		return nil, fmt.Errorf("Argo CD CLI unavailable; install argocd or set auth.command")
	}
	cfg, err := config.ReadCLIConfigFromPath(configPath)
	if err != nil {
		return nil, err
	}
	if core, _ := cfg.IsContextCore(contextName); core {
		return nil, fmt.Errorf("core contexts do not support API login")
	}
	if pf, _ := cfg.IsContextPortForward(contextName); pf {
		return nil, fmt.Errorf("port-forward contexts do not support live login")
	}
	if _, err := cfg.ToServerConfigForContext(contextName); err != nil {
		return nil, err
	}
	args = append(args, "--config", configPath, "--argocd-context", contextName)
	for _, c := range cfg.Contexts {
		if c.Name != contextName {
			continue
		}
		for _, s := range cfg.Servers {
			if s.Server == c.Server {
				if s.GrpcWeb {
					args = append(args, "--grpc-web")
				}
				if s.GrpcWebRootPath != "" {
					args = append(args, "--grpc-web-root-path", s.GrpcWebRootPath)
				}
			}
		}
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	// Environment overrides must not send the selected context's credentials
	// to a different server. Keep proxy and browser environment variables.
	cmd.Env = make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), "ARGOCD_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	return cmd, nil
}

func (m *Model) maybeAutoRelogin() (tea.Cmd, bool) {
	name := m.currentContextName
	if name == "" || !m.config.IsAutoReloginEnabled(name) {
		return nil, false
	}
	if m.reloginInProgress {
		return nil, true
	}
	// An expired refresh token, cancelled SSO, or invalid credentials must not
	// open a browser or ask for a password in an endless retry loop.
	if last := m.reloginAttempts[name]; !last.IsZero() && m.now().Sub(last) < time.Minute {
		return nil, false
	}
	return m.beginContextRelogin(name, true), true
}

func (m *Model) beginContextRelogin(name string, automatic bool) tea.Cmd {
	if m.reloginInProgress {
		return nil
	}
	if name == "" {
		return m.showFooterNotice("Select an Argo CD context first")
	}
	m.reloginSeq++
	epoch, seq := m.switchEpoch, m.reloginSeq
	path := m.argoConfigPath
	if path == "" {
		path = config.GetConfigPath()
	}
	executable := m.config.GetArgoCDCommand()
	m.reloginInProgress = true
	m.reloginContext = name
	if name == m.currentContextName {
		m.cleanupAppWatcher()
		if m.state.Navigation.View == model.ViewTree {
			m.cleanupTreeWatchers()
			m.treeLoading = false
		}
	}
	if m.reloginAttempts == nil {
		m.reloginAttempts = make(map[string]time.Time)
	}
	m.reloginAttempts[name] = m.now()
	if !automatic {
		return m.interactiveContextRelogin(name, epoch, seq, path, executable)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	m.reloginCancel = cancel
	// A harmless CLI read first picks up a concurrent login or uses the CLI's
	// existing SSO refresh token, without interrupting the terminal.
	return func() tea.Msg {
		msg := contextReloginMsg{ContextName: name, SwitchEpoch: epoch, RequestID: seq}
		defer cancel()
		cmd, err := argoCDCommand(ctx, executable, path, name, "account", "get-user-info")
		if err != nil {
			msg.Error = err
			return msg
		}
		if err := cmd.Run(); err != nil {
			msg.Interactive = true
			return msg
		}
		msg.Server, msg.Error = readReloggedServer(path, name)
		return msg
	}
}

func readReloggedServer(path, name string) (*model.Server, error) {
	cfg, err := config.ReadCLIConfigFromPath(path)
	if err != nil {
		return nil, err
	}
	return cfg.ToServerConfigForContext(name)
}

func (m *Model) interactiveContextRelogin(name string, epoch, seq int, path, executable string) tea.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	m.reloginCancel = cancel
	cmd, err := argoCDCommand(ctx, executable, path, name, "relogin")
	if err != nil {
		cancel()
		return func() tea.Msg {
			return contextReloginMsg{ContextName: name, SwitchEpoch: epoch, RequestID: seq, Error: err}
		}
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer cancel()
		msg := contextReloginMsg{ContextName: name, SwitchEpoch: epoch, RequestID: seq}
		if err != nil {
			msg.Error = fmt.Errorf("Argo CD CLI login did not complete; press L to retry")
			return msg
		}
		msg.Server, msg.Error = readReloggedServer(path, name)
		return msg
	})
}

func (m *Model) cancelContextRelogin() {
	if m.reloginCancel != nil {
		m.reloginCancel()
		m.reloginCancel = nil
	}
	m.reloginInProgress = false
	m.reloginContext = ""
	m.reloginSeq++
}

func (m *Model) handleContextRelogin(msg contextReloginMsg) (tea.Model, tea.Cmd) {
	if msg.SwitchEpoch != m.switchEpoch || msg.RequestID != m.reloginSeq || msg.ContextName != m.reloginContext || !m.reloginInProgress {
		return m, nil
	}
	if msg.Interactive {
		path := m.argoConfigPath
		if path == "" {
			path = config.GetConfigPath()
		}
		return m, m.interactiveContextRelogin(msg.ContextName, msg.SwitchEpoch, msg.RequestID, path, m.config.GetArgoCDCommand())
	}
	m.reloginInProgress = false
	m.reloginContext = ""
	if m.reloginCancel != nil {
		m.reloginCancel()
		m.reloginCancel = nil
	}
	if msg.Error == nil && msg.Server == nil {
		msg.Error = fmt.Errorf("CLI did not return context credentials")
	}
	if msg.Error != nil {
		if m.state.Navigation.View != model.ViewContexts {
			m.state.Mode = model.ModeAuthRequired
		}
		return m, m.showFooterNotice("Login failed: " + msg.Error.Error())
	}
	// Logging into an inactive row refreshes its CLI credentials without
	// changing the currently selected server in this TUI.
	if msg.ContextName != m.currentContextName {
		return m, m.showFooterNotice("Logged in: " + msg.ContextName)
	}
	if m.state.Server != nil && msg.Server.BaseURL != m.state.Server.BaseURL {
		return m, m.showFooterNotice("Context server changed during login; select the context again")
	}
	m.contextSwitchSeq++
	return m.handleContextSwitchResult(model.ContextSwitchResultMsg{
		Server: msg.Server, ContextName: msg.ContextName, ContextNames: m.state.ContextNames,
		SwitchEpoch: m.switchEpoch, RequestID: m.contextSwitchSeq, Reload: true,
	})
}
