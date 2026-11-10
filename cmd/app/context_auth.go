package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
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
	Probed      bool
	Error       error
}

func requiresRelogin(err error) bool {
	if err == nil {
		return false
	}
	if argErr, ok := errors.AsType[*apperrors.ArgonautError](err); ok {
		if argErr == nil || hasHTTPStatusCtx(argErr, 403) || argErr.Category == apperrors.ErrorPermission {
			return false
		}
		if argErr.Category == apperrors.ErrorAuth || hasHTTPStatusCtx(argErr, 401) {
			return true
		}
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "403") || strings.Contains(text, "forbidden") || strings.Contains(text, "permission denied") {
		return false
	}
	return strings.Contains(text, "401") || strings.Contains(text, "unauthenticated") || strings.Contains(text, "unauthorized") || strings.Contains(text, "token expired") || strings.Contains(text, "token is expired") || strings.Contains(text, "invalid session") || strings.Contains(text, "no auth token") || strings.Contains(text, "not logged in") || strings.Contains(text, "not authenticated") || strings.Contains(text, "invalid token")
}

func (m *Model) selectedArgoContext() string {
	if m.state.Navigation.View == model.ViewContexts {
		items := m.getVisibleItems()
		i := m.state.Navigation.SelectedIdx
		if i >= 0 && i < len(items) {
			name := items[i].(string)
			if name == allContextsChoice {
				return ""
			}
			return name
		}
		return ""
	}
	if m.allContexts != nil {
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
	if enabled {
		login, _ := m.maybeAutoReloginForContext(name)
		return m, tea.Batch(notice, login)
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
	if _, err := cfg.ConnectionForContext(contextName); err != nil {
		return nil, err
	}
	login := len(args) == 1 && args[0] == "login"
	// Full SSO does not depend on claims or the refresh token from the expired
	// session. --name binds it to this context, including on CLI 2.12, which
	// does not support --argocd-context (and relogin uses current-context).
	if login {
		for _, c := range cfg.Contexts {
			if c.Name == contextName {
				// The saved TLS/plaintext settings are explicit; avoid the CLI's
				// extra detection connection and interactive TLS-mode questions.
				// Certificate verification still follows the saved --insecure flag.
				args = []string{"login", c.Server, "--sso", "--name", contextName, "--skip-test-tls"}
			}
		}
	}
	args = append(args, "--config", configPath)
	if !login {
		args = append(args, "--argocd-context", contextName)
	}
	for _, c := range cfg.Contexts {
		if c.Name != contextName {
			continue
		}
		for _, s := range cfg.Servers {
			if s.Server == c.Server {
				if s.Insecure {
					args = append(args, "--insecure")
				}
				if s.PlainText {
					args = append(args, "--plaintext")
				}
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
	return m.maybeAutoReloginForContext(m.currentContextName)
}

func (m *Model) maybeAutoReloginForContext(name string) (tea.Cmd, bool) {
	if name == "" || !m.config.IsAutoReloginEnabled(name) {
		return nil, false
	}
	if m.reloginInProgress {
		if name != m.reloginContext && !slices.Contains(m.reloginQueue, name) {
			m.reloginQueue = append(m.reloginQueue, name)
		}
		return nil, true
	}
	// A CLI account read can exit successfully even when userinfo says
	// loggedIn=false. If the API rejects that probe, escalate to browser SSO
	// once, rather than throttling it as a failed browser login.
	if m.reloginProbed[name] {
		return m.beginContextRelogin(name, false), true
	}
	// An expired refresh token, cancelled SSO, or invalid credentials must not
	// open a browser or ask for a password in an endless retry loop.
	if last := m.reloginAttempts[name]; !last.IsZero() && m.now().Sub(last) < time.Minute {
		return nil, false
	}
	return m.beginContextRelogin(name, true), true
}

func (m *Model) markContextAuthenticated(name string) {
	delete(m.reloginAttempts, name)
	delete(m.reloginProbed, name)
}

// Browser callbacks and CLI config writes must run serially. Never drop a
// context just because another context currently owns the SSO callback port.
func (m *Model) nextContextRelogin() tea.Cmd {
	for !m.reloginInProgress && len(m.reloginQueue) > 0 {
		name := m.reloginQueue[0]
		m.reloginQueue = m.reloginQueue[1:]
		if cmd, handled := m.maybeAutoReloginForContext(name); handled {
			return cmd
		}
	}
	return nil
}

func (m *Model) autoReloginForError(err error) (tea.Cmd, bool) {
	if requiresRelogin(err) {
		_, cmd := m.recoverArgoAuthentication(true)
		return cmd, true
	}
	return nil, false
}

// Authentication is recoverable navigation state, never a full-screen dead end.
// Keep the picker alive even when SSO is disabled, cancelled or unavailable.
func (m *Model) recoverArgoAuthentication(renew bool) (tea.Model, tea.Cmd) {
	if m.state.Navigation.View == model.ViewKargo {
		return m, nil
	}
	var picker tea.Cmd
	if m.allContexts == nil {
		m.contextNeedsLoad = true
		m.cleanupAppWatcher()
		if m.state.Navigation.View != model.ViewContexts {
			if !m.reloginInProgress {
				m.switchEpoch++ // Retire responses from the rejected session.
			}
			_, picker = m.openContextPicker()
		}
		if m.state.Mode == model.ModeAuthRequired || m.state.Mode == model.ModeError || m.state.Mode == model.ModeConnectionError || m.state.Mode == model.ModeLoading {
			m.state.Mode = model.ModeNormal
		}
		m.state.Modals.InitialLoading = false
	}
	if renew {
		if login, handled := m.maybeAutoRelogin(); handled {
			return m, tea.Batch(picker, login)
		}
	}
	return m, tea.Batch(picker, m.showFooterNotice("Select a context; Ctrl+L: SSO login; Ctrl+A: auto SSO"))
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
	delete(m.reloginProbed, name)
	m.state.UI.FooterNotice = "SSO: signing in to " + name
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
	if cfg, err := config.ReadCLIConfigFromPath(path); err == nil {
		if server, err := cfg.ConnectionForContext(name); err == nil && argoTokenNeedsLogin(server.Token, m.now()) {
			return m.interactiveContextRelogin(name, epoch, seq, path, executable)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	m.reloginCancel = cancel
	// A harmless CLI read first picks up a concurrent login or uses the CLI's
	// existing SSO refresh token, without interrupting the terminal.
	return func() tea.Msg {
		msg := contextReloginMsg{ContextName: name, SwitchEpoch: epoch, RequestID: seq}
		defer cancel()
		cmd, err := argoCDCommand(ctx, executable, path, name, "account", "get-user-info", "--output", "json")
		if err != nil {
			msg.Error = err
			return msg
		}
		output, err := cmd.Output()
		if err != nil {
			msg.Interactive = true
			return msg
		}
		var info struct {
			LoggedIn *bool `json:"loggedIn"`
		}
		if json.Unmarshal(output, &info) == nil && info.LoggedIn != nil && !*info.LoggedIn {
			msg.Interactive = true
			return msg
		}
		msg.Server, msg.Error = readReloggedServer(path, name)
		if requiresRelogin(msg.Error) {
			msg.Error, msg.Interactive = nil, true
			return msg
		}
		msg.Probed = msg.Error == nil
		return msg
	}
}

// Expiry is only a hint to start SSO sooner, never proof of authentication.
// Argo CD still validates every credential. Avoid sending a known-expired JWT
// to user-info before starting the login the user has enabled.
func argoTokenNeedsLogin(token string, now time.Time) bool {
	if token == "" {
		return true
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Expiry *int64 `json:"exp"`
	}
	return json.Unmarshal(payload, &claims) == nil && claims.Expiry != nil && *claims.Expiry <= now.Add(30*time.Second).Unix()
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
	cmd, err := argoCDCommand(ctx, executable, path, name, "login")
	if err != nil {
		cancel()
		return func() tea.Msg {
			return contextReloginMsg{ContextName: name, SwitchEpoch: epoch, RequestID: seq, Error: err}
		}
	}
	// Browser SSO needs no terminal input. ExecProcess suspends Bubble Tea's
	// event loop for the entire IdP/MFA wait, trapping the user on the old view.
	// Run the context-bound CLI in a command goroutine; nil stdin is EOF and
	// output stays private. Switching context/quit cancels this process.
	return func() tea.Msg {
		defer cancel()
		msg := contextReloginMsg{ContextName: name, SwitchEpoch: epoch, RequestID: seq}
		before, err := config.ReadCLIConfigFromPath(path)
		if err != nil {
			msg.Error = err
			return msg
		}
		if err := cmd.Run(); err != nil {
			msg.Error = fmt.Errorf("Argo CD CLI SSO login did not complete; Ctrl+L retries")
			return msg
		}
		// `login --name` also changes the CLI's global current-context. Restore
		// it through the CLI, preserving its full config and refresh tokens.
		if before.CurrentContext != "" && before.CurrentContext != name {
			after, err := config.ReadCLIConfigFromPath(path)
			if err != nil {
				msg.Error = err
				return msg
			}
			if after.CurrentContext == name {
				restore := exec.CommandContext(ctx, executable, "context", before.CurrentContext, "--config", path)
				restore.Env = cmd.Env
				if err := restore.Run(); err != nil {
					msg.Error = fmt.Errorf("SSO completed but restoring the CLI context failed; select the context again")
					return msg
				}
			}
		}
		msg.Server, msg.Error = readReloggedServer(path, name)
		return msg
	}
}

func (m *Model) cancelContextRelogin() {
	if m.reloginCancel != nil {
		m.reloginCancel()
		m.reloginCancel = nil
	}
	m.reloginInProgress = false
	m.reloginContext = ""
	m.reloginQueue = nil
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
		m.reloginAttempts[msg.ContextName] = m.now()
		var picker tea.Cmd
		if m.allContexts == nil && m.state.Navigation.View != model.ViewKargo {
			m.contextNeedsLoad = true
			if m.state.Navigation.View != model.ViewContexts {
				_, picker = m.openContextPicker()
			}
		}
		next := m.nextContextRelogin()
		if next == nil && m.allContexts != nil {
			next = m.reloadAllContexts()
		}
		return m, tea.Batch(picker, m.showFooterNotice("Login failed: "+msg.ContextName+": "+msg.Error.Error()), next)
	}
	if m.reloginValidated == nil {
		m.reloginValidated = map[string]bool{}
	}
	m.reloginValidated[msg.ContextName] = true
	if msg.Probed {
		if m.reloginProbed == nil {
			m.reloginProbed = map[string]bool{}
		}
		m.reloginProbed[msg.ContextName] = true
	}
	if m.allContexts != nil {
		// Re-read tokens only after the queue drains. Reloading during SSO
		// otherwise cancels the other contexts and races CLI config updates.
		if next := m.nextContextRelogin(); next != nil {
			return m, next
		}
		reload := m.reloadAllContexts()
		var inspection tea.Cmd
		if st := m.inspection; st != nil && st.Target.ContextName == msg.ContextName {
			inspection = m.startResourceInspection(st.Target, st.Kind)
		}
		return m, tea.Batch(m.showFooterNotice("Logged in: "+msg.ContextName), reload, inspection)
	}
	// Logging into an inactive row refreshes its CLI credentials without
	// changing the currently selected server in this TUI.
	if msg.ContextName != m.currentContextName {
		return m, tea.Batch(m.showFooterNotice("Logged in: "+msg.ContextName), m.nextContextRelogin())
	}
	if m.state.Server != nil && msg.Server.BaseURL != m.state.Server.BaseURL {
		return m, m.showFooterNotice("Context server changed during login; select the context again")
	}
	if m.state.Navigation.View == model.ViewContexts {
		m.state.Server = msg.Server
		m.state.Apps, m.state.ApplicationSets, m.state.DestinationServers = nil, nil, nil
		m.rebuildAppIndex()
		m.contextNeedsLoad = true
		return m, tea.Batch(m.showFooterNotice("Logged in: "+msg.ContextName+"; Enter: open context"), m.nextContextRelogin())
	}
	m.contextSwitchSeq++
	return m.handleContextSwitchResult(model.ContextSwitchResultMsg{
		Server: msg.Server, ContextName: msg.ContextName, ContextNames: m.state.ContextNames,
		SwitchEpoch: m.switchEpoch, RequestID: m.contextSwitchSeq, Reload: true,
	})
}
