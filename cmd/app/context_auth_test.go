package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/config"
	apperrors "github.com/darksworm/argonaut/pkg/errors"
	"github.com/darksworm/argonaut/pkg/model"
)

// Run the test binary as a real fake CLI subprocess, without external tools,
// an identity provider, user credentials, kubeconfig, or shell dependencies.
func TestMain(m *testing.M) {
	if os.Getenv("ARGONAUT_TEST_ARGOCD_CLI") == "1" {
		if os.Getenv("ARGONAUT_TEST_CLI_FAIL") == "1" {
			os.Exit(7)
		}
		args := os.Args[1:]
		index := slices.Index(args, "--config")
		contextIndex := slices.Index(args, "--argocd-context")
		if index < 0 || contextIndex < 0 || contextIndex+1 >= len(args) || args[contextIndex+1] != "prod" {
			os.Exit(8)
		}
		path := args[index+1]
		data, err := os.ReadFile(path)
		if err != nil {
			os.Exit(9)
		}
		data = []byte(strings.ReplaceAll(string(data), "prod-test-token", "renewed-test-token"))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			os.Exit(10)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func authTestModel(t *testing.T) *Model {
	t.Helper()
	m := contextPickerTestModel(t)
	t.Setenv("ARGONAUT_CONFIG", filepath.Join(t.TempDir(), "argonaut.toml"))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m.config.Auth.Command = executable
	m.config.Auth.AutoRelogin = map[string]bool{"prod": true}
	m.state.Mode = model.ModeNormal
	t.Setenv("ARGONAUT_TEST_ARGOCD_CLI", "1")
	return m
}

func TestAutoReloginRefreshesOnlySelectedContextViaCLI(t *testing.T) {
	m := authTestModel(t)
	m.state.Apps = []model.App{{Name: "old-app"}}
	t.Setenv("ARGOCD_SERVER", "wrong-server.test")
	t.Setenv("ARGOCD_AUTH_TOKEN", "wrong-token")
	_, cmd := m.Update(model.AuthErrorMsg{Error: errors.New("401 unauthorized"), SwitchEpoch: m.switchEpoch})
	if cmd == nil || !m.reloginInProgress {
		t.Fatal("expired session should start auto-login")
	}
	msg, ok := cmd().(contextReloginMsg)
	if !ok || msg.Error != nil || msg.Interactive || msg.ContextName != "prod" {
		t.Fatalf("silent CLI refresh failed: %+v", msg)
	}
	if msg.Server.Token != "renewed-test-token" {
		t.Fatal("CLI token was not reread")
	}
	result, load := m.Update(msg)
	updated := result.(*Model)
	if load == nil || updated == m || updated.switchEpoch != 1 || updated.state.Server.Token != "renewed-test-token" || updated.currentContextName != "prod" {
		t.Fatal("renewed credentials should replace the server and reload data")
	}
	if len(updated.state.Apps) != 0 {
		t.Fatal("old authenticated data must not survive the reload")
	}
	cfg, err := config.ReadCLIConfigFromPath(m.argoConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := cfg.ToServerConfigForContext("dev")
	if err != nil || dev.Token != "dev-test-token" || cfg.CurrentContext != "dev" {
		t.Fatal("refresh must not change another context or current-context")
	}
	if _, handled := updated.maybeAutoRelogin(); handled {
		t.Fatal("failed renewed credentials must not trigger an immediate login loop")
	}
}

func TestAutoReloginFallsBackToSSOAndThrottlesFailures(t *testing.T) {
	m := authTestModel(t)
	t.Setenv("ARGONAUT_TEST_CLI_FAIL", "1")
	cmd, handled := m.maybeAutoRelogin()
	if !handled || cmd == nil {
		t.Fatal("auto-login should be enabled for prod")
	}
	msg := cmd().(contextReloginMsg)
	if !msg.Interactive || msg.Error != nil {
		t.Fatal("an expired refresh token should request interactive SSO")
	}
	_, interactive := m.Update(msg)
	if interactive == nil || !m.reloginInProgress {
		t.Fatal("SSO must run through terminal handoff")
	}
	// Simulate cancelled/failed SSO; a subsequent 401 must not reopen it.
	_, _ = m.Update(contextReloginMsg{ContextName: "prod", SwitchEpoch: m.switchEpoch, RequestID: m.reloginSeq, Error: errors.New("cancelled")})
	if m.reloginInProgress || !strings.Contains(m.state.UI.FooterNotice, "Login failed") {
		t.Fatal("SSO failure should be visible and stop loading")
	}
	if _, handled := m.maybeAutoRelogin(); handled {
		t.Fatal("SSO must not loop after cancellation")
	}
	m.cancelContextRelogin()
}

func TestReloginGatesRepliesAndCancellation(t *testing.T) {
	for _, variant := range []string{"epoch", "request", "context", "quit"} {
		t.Run(variant, func(t *testing.T) {
			m := authTestModel(t)
			cmd := m.beginContextRelogin("prod", true)
			msg := cmd().(contextReloginMsg)
			switch variant {
			case "epoch":
				m.switchEpoch++
			case "request":
				m.reloginSeq++
			case "context":
				msg.ContextName = "dev"
			case "quit":
				_, _ = m.Update(model.QuitMsg{})
			}
			result, next := m.Update(msg)
			if result != m || next != nil || m.state.Server.Token != "prod-test-token" {
				t.Fatal("stale login must not change credentials or restart loading")
			}
			m.cancelContextRelogin()
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	m := authTestModel(t)
	m.reloginCancel = cancel
	m.cancelContextRelogin()
	if ctx.Err() != context.Canceled {
		t.Fatal("logout must cancel the active CLI process context")
	}
}

func TestCLILoginArgumentsAndEnvironmentAreContextBound(t *testing.T) {
	m := authTestModel(t)
	t.Setenv("ARGOCD_SERVER", "https://attacker.example.test")
	t.Setenv("ARGOCD_OPTS", "--argocd-context wrong")
	t.Setenv("HTTPS_PROXY", "http://proxy.example.test")
	cmd, err := argoCDCommand(t.Context(), m.config.GetArgoCDCommand(), m.argoConfigPath, "prod", "relogin")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "--argocd-context prod") || !strings.Contains(joined, "--grpc-web-root-path /argo") || strings.Contains(joined, "test-token") || strings.Contains(joined, "--password") {
		t.Fatal("CLI args must select context/options without carrying secrets")
	}
	for _, env := range cmd.Env {
		if strings.HasPrefix(env, "ARGOCD_") {
			t.Fatal("CLI target override leaked into child environment")
		}
	}
	if !slices.Contains(cmd.Env, "HTTPS_PROXY=http://proxy.example.test") {
		t.Fatal("proxy settings should be preserved")
	}
}

func TestContextAutoReloginPreferencePersistsPerContext(t *testing.T) {
	m := authTestModel(t)
	_, _ = m.openContextPicker()
	_, _ = m.handleKeyMsg(testKeyMsg("ctrl+a")) // prod, active row
	if m.config.IsAutoReloginEnabled("prod") {
		t.Fatal("a should disable the active row")
	}
	_, _ = m.handleKeyMsg(testKeyMsg("k")) // dev, inactive row
	_, _ = m.handleKeyMsg(testKeyMsg("ctrl+a"))
	if !m.config.IsAutoReloginEnabled("dev") || m.config.IsAutoReloginEnabled("prod") {
		t.Fatal("preference must apply to the selected row only")
	}
	cfg, err := config.LoadArgonautConfig()
	if err != nil || !cfg.IsAutoReloginEnabled("dev") || cfg.IsAutoReloginEnabled("prod") {
		t.Fatal("preferences did not persist")
	}
	if !strings.Contains(stripANSI(m.renderSimpleRow("dev", false)), "auto-login: ON") {
		t.Fatal("enabled state must be visible on inactive rows")
	}
	data, err := os.ReadFile(config.GetArgonautConfigPath())
	if err != nil || strings.Contains(string(data), "test-token") {
		t.Fatal("Argonaut preferences must not store credentials")
	}
}

func TestAutoReloginOnlyHandlesAuthenticationFailures(t *testing.T) {
	var nilAuthError *apperrors.ArgonautError
	if requiresRelogin(nilAuthError) {
		t.Fatal("nil structured error is not an expired session")
	}
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{errors.New("401 unauthorized"), true},
		{errors.New("token is expired"), true},
		{errors.New("403 forbidden"), false},
		{errors.New("connection refused"), false},
		{apperrors.New(apperrors.ErrorPermission, "FORBIDDEN", "Insufficient permissions"), false},
		{apperrors.New(apperrors.ErrorAuth, "UNAUTHORIZED", "Session expired"), true},
	} {
		t.Run(fmt.Sprint(tc.err), func(t *testing.T) {
			if got := requiresRelogin(tc.err); got != tc.want {
				t.Fatalf("requiresRelogin=%v, want %v", got, tc.want)
			}
		})
	}
	m := authTestModel(t)
	m.currentContextName = "dev"
	if _, handled := m.maybeAutoRelogin(); handled {
		t.Fatal("disabled context must not automatically launch CLI")
	}
	m.currentContextName = "prod"
	m.reloginAttempts = map[string]time.Time{"prod": m.now().Add(-2 * time.Minute)}
	if cmd, handled := m.maybeAutoRelogin(); !handled || cmd == nil {
		t.Fatal("a later auth failure should be allowed to retry")
	}
	m.cancelContextRelogin()
}

func TestAutoReloginHandlesAuthenticationFailureFromResourceViews(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			m := authTestModel(t)
			m.state.Navigation.View = model.ViewTree
			_, _ = m.Update(model.ApiErrorMsg{Message: "request failed", StatusCode: status, SwitchEpoch: m.switchEpoch})
			if m.reloginInProgress != (status == 401) {
				t.Fatalf("unexpected auto-login for HTTP %d", status)
			}
			m.cancelContextRelogin()
		})
	}
}

type reloginRoundTripModel struct {
	*Model
	completed bool
	err       error
}

func (m *reloginRoundTripModel) Init() tea.Cmd { return m.beginContextRelogin("prod", false) }
func (m *reloginRoundTripModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := message.(contextReloginMsg); ok {
		m.err = msg.Error
		updated, _ := m.handleContextRelogin(msg)
		m.Model = updated.(*Model)
		m.completed = true
		// Inspect the handoff result without executing subsequent API calls.
		return m, tea.Quit
	}
	return m, nil
}

func TestInteractiveSSOHandsOffTerminalAndReloadsCLICredentials(t *testing.T) {
	m := &reloginRoundTripModel{Model: authTestModel(t)}
	p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	if !m.completed || m.err != nil || m.state.Server.Token != "renewed-test-token" || m.switchEpoch != 1 {
		t.Fatalf("interactive CLI round trip failed: completed=%v err=%v epoch=%d", m.completed, m.err, m.switchEpoch)
	}
}
