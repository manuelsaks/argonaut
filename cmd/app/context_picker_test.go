package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/model"
)

const pickerCLIConfig = `current-context: dev
contexts:
  - name: prod
    server: prod.example.test
    user: prod-user
  - name: dev
    server: dev.example.test
    user: dev-user
servers:
  - server: dev.example.test
  - server: prod.example.test
    insecure: true
    grpc-web-root-path: /argo
users:
  - name: dev-user
    auth-token: dev-test-token
  - name: prod-user
    auth-token: prod-test-token
`

func contextPickerTestModel(t *testing.T) *Model {
	t.Helper()
	dir := t.TempDir()
	// Argo CD contexts must work even with a nonexistent kubeconfig and with
	// neither argocd nor kubectl (nor a shell) available to spawn.
	t.Setenv("KUBECONFIG", filepath.Join(dir, "no-kubeconfig"))
	t.Setenv("PATH", dir)
	m := buildSyncTestModel(100, 30)
	m.argoConfigPath = filepath.Join(dir, "argocd-config")
	if err := os.WriteFile(m.argoConfigPath, []byte(pickerCLIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	m.currentContextName = "prod"
	m.state.Server = &model.Server{BaseURL: "https://prod.example.test", Token: "prod-test-token"}
	return m
}

func TestContextPickerHotkeyAndCommands(t *testing.T) {
	for _, command := range []string{"c", "ctx", "context", "contexts", "argocd"} {
		t.Run(command, func(t *testing.T) {
			m := contextPickerTestModel(t)
			m.state.ContextNames = []string{"stale"}
			m.state.UI.ActiveFilter = "an-app-filter"
			m.state.UI.SearchQuery = "an-app-filter"
			if command == "c" {
				_, _ = m.handleKeyMsg(testKeyMsg("c"))
			} else {
				_, _ = m.handleEnterCommandMode()
				m.inputComponents.SetCommandValue(command)
				_, _ = m.handleKeyMsg(testKeyMsg("enter"))
			}
			if m.state.Navigation.View != model.ViewContexts || m.state.Mode != model.ModeNormal {
				t.Fatalf("picker did not open: view=%s mode=%s", m.state.Navigation.View, m.state.Mode)
			}
			if !reflect.DeepEqual(m.state.ContextNames, []string{"dev", "prod"}) {
				t.Fatalf("contexts were not refreshed from CLI config: %v", m.state.ContextNames)
			}
			if m.state.UI.ActiveFilter != "" || m.state.UI.SearchQuery != "" {
				t.Fatal("app filter should not hide contexts")
			}
			if m.state.Navigation.SelectedIdx != 1 || m.listNav.Cursor() != 1 {
				t.Fatal("active context should be selected in both state and navigator")
			}
			view := stripANSI(m.renderListView(10))
			if !strings.Contains(view, "ARGO CD CONTEXTS") || !strings.Contains(view, "* prod (active)") {
				t.Fatalf("picker should show instructions and active context:\n%s", view)
			}
		})
	}
}

func TestContextPickerSwitchesServerAndTokenWithoutKubeconfig(t *testing.T) {
	m := contextPickerTestModel(t)
	m.state.Apps = []model.App{{Name: "old-context-app"}}
	m.state.Selections.SelectedApps = model.StringSetFromSlice([]string{"old-context-app"})
	cleanupCount := 0
	m.appWatchCleanup = func() { cleanupCount++ }
	_, _ = m.handleKeyMsg(testKeyMsg("c"))
	_, _ = m.handleKeyMsg(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.state.Navigation.SelectedIdx != 0 {
		t.Fatal("up should select dev")
	}
	_, _ = m.handleKeyMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	_, _ = m.handleKeyMsg(testKeyMsg("k"))
	_, cmd := m.handleKeyMsg(testKeyMsg("enter"))
	if cmd == nil {
		t.Fatal("Enter should schedule the switch")
	}
	result, load := m.Update(cmd())
	newM := result.(*Model)
	if newM == m || load == nil || newM.currentContextName != "dev" || newM.switchEpoch != 1 {
		t.Fatal("switch should create a fresh model and begin loading the new context")
	}
	if newM.state.Server.BaseURL != "https://dev.example.test" || newM.state.Server.Token != "dev-test-token" {
		t.Fatalf("wrong context server selected: %s", newM.state.Server.BaseURL)
	}
	if len(newM.state.Apps) != 0 || len(newM.state.Selections.SelectedApps) != 0 || cleanupCount != 1 {
		t.Fatal("old context apps, selections and watcher must be cleared")
	}
	contents, err := os.ReadFile(m.argoConfigPath)
	if err != nil || string(contents) != pickerCLIConfig {
		t.Fatal("switch must not change the Argo CD CLI configuration")
	}
}

func TestContextPickerFilteredEnterAndTokenRefresh(t *testing.T) {
	m := contextPickerTestModel(t)
	m.currentContextName = "dev"
	_, _ = m.handleKeyMsg(testKeyMsg("c"))
	// A concurrent CLI login can update the token after the picker opens.
	updatedConfig := strings.ReplaceAll(pickerCLIConfig, "prod-test-token", "refreshed-test-token")
	if err := os.WriteFile(m.argoConfigPath, []byte(updatedConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = m.handleKeyMsg(testKeyMsg("/"))
	for _, key := range []string{"p", "r", "o", "d"} {
		_, _ = m.handleKeyMsg(testKeyMsg(key))
	}
	if items := m.getVisibleItems(); len(items) != 1 || items[0] != "prod" {
		t.Fatalf("search should filter to prod: %v", items)
	}
	_, cmd := m.handleKeyMsg(testKeyMsg("enter"))
	if cmd != nil || m.state.Mode != model.ModeNormal {
		t.Fatal("Enter must finish filtering immediately")
	}
	_, cmd = m.handleKeyMsg(testKeyMsg("enter"))
	if cmd == nil {
		t.Fatal("Enter on filtered context should switch")
	}
	msg := cmd().(model.ContextSwitchResultMsg)
	if msg.Error != nil {
		t.Fatal(msg.Error)
	}
	if msg.ContextName != "prod" || msg.Server.Token != "refreshed-test-token" || !msg.Server.Insecure || msg.Server.GrpcWebRootPath != "/argo" {
		t.Fatal("switch should use the selected context's latest token and server options")
	}
	if m.state.UI.ActiveFilter != "prod" || len(m.getVisibleItems()) != 1 {
		t.Fatal("filter should stay applied while the switch is pending")
	}
}

func TestContextPickerEscapeAndActiveContext(t *testing.T) {
	for _, key := range []string{"esc", "enter"} {
		t.Run(key, func(t *testing.T) {
			m := contextPickerTestModel(t)
			_, _ = m.handleKeyMsg(testKeyMsg("c"))
			_, cmd := m.handleKeyMsg(testKeyMsg(key))
			if cmd != nil {
				_, _ = m.Update(cmd())
			}
			if m.state.Navigation.View != model.ViewApps || m.currentContextName != "prod" || m.switchEpoch != 0 {
				t.Fatal("Esc or Enter on the active context should return to apps without a switch")
			}
		})
	}
}

func TestContextPickerFromTreeAndErrorModes(t *testing.T) {
	for _, mode := range []model.Mode{model.ModeNormal, model.ModeLoading, model.ModeAuthRequired, model.ModeConnectionError, model.ModeError} {
		t.Run(string(mode), func(t *testing.T) {
			m := contextPickerTestModel(t)
			m.state.Navigation.View = model.ViewTree
			m.state.Mode = mode
			m.setTreeApp(model.App{Name: "old-tree"})
			cleanupCount := 0
			m.treeWatchCleanups = []func(){func() { cleanupCount++ }}
			_, _ = m.handleKeyMsg(testKeyMsg("c"))
			if m.state.Navigation.View != model.ViewContexts || m.state.Mode != model.ModeNormal || cleanupCount != 1 || m.state.UI.TreeApp != nil {
				t.Fatal("picker should open from an unavailable context and stop the old resource tree")
			}
			_, load := m.Update(model.SetModeMsg{Mode: model.ModeLoading})
			if m.state.Mode != model.ModeNormal || load == nil {
				t.Fatal("background loading must not disable picker navigation")
			}
			_, _ = m.handleKeyMsg(testKeyMsg("k"))
			if m.state.Navigation.SelectedIdx != 0 {
				t.Fatal("picker should remain navigable during loading")
			}
		})
	}
}

func TestContextPickerDoesNotInterceptTextInput(t *testing.T) {
	m := contextPickerTestModel(t)
	_, _ = m.handleKeyMsg(testKeyMsg("/"))
	_, _ = m.handleKeyMsg(testKeyMsg("c"))
	if m.state.Mode != model.ModeSearch || m.state.Navigation.View != model.ViewApps || m.inputComponents.GetSearchValue() != "c" {
		t.Fatal("c in search must remain text input")
	}
	_, _ = m.openContextPicker()
	_, _ = m.handleKeyMsg(testKeyMsg("/"))
	_, _ = m.handleKeyMsg(testKeyMsg("c"))
	_, _ = m.Update(model.SetModeMsg{Mode: model.ModeNormal})
	if m.state.Mode != model.ModeSearch || m.inputComponents.GetSearchValue() != "c" {
		t.Fatal("background load completion must not dismiss context search")
	}
}

func TestContextSwitchRejectsStaleAndCancelledResults(t *testing.T) {
	for _, variant := range []string{"older-request", "older-epoch", "cancelled", "stale-error", "stale-noop"} {
		t.Run(variant, func(t *testing.T) {
			m := contextPickerTestModel(t)
			_, _ = m.openContextPicker()
			cmd := m.performContextSwitch("dev")
			msg := cmd().(model.ContextSwitchResultMsg)
			switch variant {
			case "older-request":
				_ = m.performContextSwitch("prod")
			case "older-epoch":
				m.switchEpoch++
			case "cancelled":
				_, _ = m.handleKeyMsg(testKeyMsg("esc"))
			case "stale-error":
				msg.Error = errors.New("stale error")
				m.switchEpoch++
			case "stale-noop":
				msg = m.performContextSwitch("prod")().(model.ContextSwitchResultMsg)
				_ = m.performContextSwitch("dev")
			}
			previousView := m.state.Navigation.View
			previousStatus := m.statusService.GetCurrentStatus()
			cleanupCount := 0
			m.appWatchCleanup = func() { cleanupCount++ }
			updated, cmd := m.Update(msg)
			if updated != m || cmd != nil || cleanupCount != 0 || m.currentContextName != "prod" || m.state.Navigation.View != previousView || m.statusService.GetCurrentStatus() != previousStatus {
				t.Fatal("stale result must not switch context, close picker, clean watchers or alter status")
			}
		})
	}
}

func TestContextPickerReadErrorStillOpensNavigablePicker(t *testing.T) {
	m := contextPickerTestModel(t)
	if err := os.Remove(m.argoConfigPath); err != nil {
		t.Fatal(err)
	}
	_, _ = m.handleKeyMsg(testKeyMsg("c"))
	if m.state.Navigation.View != model.ViewContexts || m.state.Mode != model.ModeNormal || m.currentContextName != "prod" {
		t.Fatal("unreadable config must not trap the user outside the picker")
	}
	if !strings.Contains(stripANSI(m.renderStatusLine()), "Cannot load Argo CD contexts") {
		t.Fatal("config read error must be visible in the UI")
	}
}

func TestContextPickerSwitchFailureStaysOnCurrentServer(t *testing.T) {
	for _, variant := range []string{"missing-token", "core", "port-forward"} {
		t.Run(variant, func(t *testing.T) {
			m := contextPickerTestModel(t)
			_, _ = m.openContextPicker()
			badConfig := pickerCLIConfig
			switch variant {
			case "missing-token":
				badConfig = strings.ReplaceAll(badConfig, "auth-token: dev-test-token", "auth-token: ''")
			case "core":
				badConfig = strings.ReplaceAll(badConfig, "  - server: dev.example.test\n", "  - server: dev.example.test\n    core: true\n")
			case "port-forward":
				badConfig = strings.ReplaceAll(badConfig, "dev.example.test", "port-forward")
			}
			if err := os.WriteFile(m.argoConfigPath, []byte(badConfig), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _ = m.handleKeyMsg(testKeyMsg("k"))
			_, switchCmd := m.handleKeyMsg(testKeyMsg("enter"))
			msg := switchCmd().(model.ContextSwitchResultMsg)
			if msg.Error == nil {
				t.Fatal("expected invalid or unsupported context to fail")
			}
			updated, _ := m.Update(msg)
			if updated != m || m.state.Navigation.View != model.ViewContexts || m.currentContextName != "prod" || m.state.Server.Token != "prod-test-token" {
				t.Fatal("failed context must leave the picker open and retain the previous server")
			}
			if !strings.Contains(stripANSI(m.renderStatusLine()), "Context switch failed") {
				t.Fatal("switch failure must be visible in the picker")
			}
		})
	}
}
