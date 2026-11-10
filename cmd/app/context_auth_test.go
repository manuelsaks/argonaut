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
	"gopkg.in/yaml.v3"
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
		if index < 0 || index+1 >= len(args) || len(args) == 0 {
			os.Exit(8)
		}
		path := args[index+1]
		if args[0] == "context" && len(args) > 1 {
			data, err := os.ReadFile(path)
			var cfg map[string]any
			if err != nil || yaml.Unmarshal(data, &cfg) != nil {
				os.Exit(9)
			}
			cfg["current-context"] = args[1]
			data, err = yaml.Marshal(cfg)
			if err != nil || os.WriteFile(path, data, 0600) != nil {
				os.Exit(10)
			}
			os.Exit(0)
		}
		if args[0] == "login" {
			// Match older real CLIs: login binds the context with --name and
			// rejects --argocd-context. Never let a fake accept invalid flags.
			if contextIndex >= 0 || !slices.Contains(args, "--sso") {
				os.Exit(8)
			}
			contextIndex = slices.Index(args, "--name")
		}
		if contextIndex < 0 || contextIndex+1 >= len(args) || !slices.Contains([]string{"dev", "prod"}, args[contextIndex+1]) || args[0] == "relogin" {
			os.Exit(8)
		}
		if os.Getenv("ARGONAUT_TEST_CLI_LEGACY") == "1" && slices.Contains(args, "get-user-info") {
			fmt.Fprintln(os.Stderr, "Error: unknown flag: --argocd-context")
			os.Exit(1)
		}
		if userInfo := os.Getenv("ARGONAUT_TEST_CLI_USERINFO"); userInfo != "" && slices.Contains(args, "get-user-info") {
			fmt.Print(userInfo)
			os.Exit(0)
		}
		if waitDir := os.Getenv("ARGONAUT_TEST_CLI_WAIT_DIR"); waitDir != "" && !slices.Contains(args, "get-user-info") {
			if err := os.WriteFile(filepath.Join(waitDir, "started"), []byte("ready"), 0600); err != nil {
				os.Exit(11)
			}
			deadline := time.Now().Add(20 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(waitDir, "continue")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					os.Exit(12)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			os.Exit(9)
		}
		name := args[contextIndex+1]
		renewed := "renewed-test-token"
		if name == "dev" {
			renewed = "renewed-dev-test-token"
		}
		data = []byte(strings.ReplaceAll(string(data), name+"-test-token", renewed))
		if args[0] == "login" {
			var cfg map[string]any
			if yaml.Unmarshal(data, &cfg) != nil {
				os.Exit(9)
			}
			cfg["current-context"] = name
			for _, value := range cfg["users"].([]any) {
				user := value.(map[string]any)
				if user["name"] == name+"-user" {
					user["auth-token"] = renewed
				}
			}
			data, err = yaml.Marshal(cfg)
			if err != nil {
				os.Exit(10)
			}
		}
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
	if load == nil || updated.switchEpoch != 1 || updated.state.Server.Token != "renewed-test-token" || updated.currentContextName != "prod" || updated.state.Navigation.View != model.ViewContexts || updated.state.Mode != model.ModeNormal {
		t.Fatal("renewed credentials should return to an operable context picker")
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
	if cmd, handled := updated.maybeAutoRelogin(); !handled || cmd == nil || !updated.reloginInProgress {
		t.Fatal("an API rejection after the silent CLI probe must escalate to SSO")
	}
	updated.cancelContextRelogin()
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
		t.Fatal("SSO must run asynchronously while the picker remains available")
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
	cmd, err := argoCDCommand(t.Context(), m.config.GetArgoCDCommand(), m.argoConfigPath, "prod", "login")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "login prod.example.test --sso --name prod") || strings.Contains(joined, "--argocd-context") || !strings.Contains(joined, "--grpc-web-root-path /argo") || strings.Contains(joined, "test-token") || strings.Contains(joined, "--password") {
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
	t.Cleanup(m.cancelContextRelogin)
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

func TestBrowserSSORunsAsynchronouslyAndReloadsCLICredentials(t *testing.T) {
	m := &reloginRoundTripModel{Model: authTestModel(t)}
	p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	if !m.completed || m.err != nil || m.state.Server.Token != "renewed-test-token" || m.switchEpoch != 1 {
		t.Fatalf("interactive CLI round trip failed: completed=%v err=%v epoch=%d", m.completed, m.err, m.switchEpoch)
	}
}
