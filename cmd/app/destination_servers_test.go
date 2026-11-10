package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/darksworm/argonaut/pkg/model"
)

func destinationTestModel() *Model {
	m := buildDeleteTestModel(120, 30)
	m.currentContextName = "production"
	m.state.DestinationServers = []model.DestinationServer{
		{Name: "localhost", Server: "https://kubernetes.default.svc"},
		{Name: "site1", Server: "https://site1.example"},
		{Name: "site2", Server: "https://site2.example"},
	}
	m.destinationLoaded = true
	m.state.Apps = []model.App{
		{Name: "shared", DestinationServer: "https://kubernetes.default.svc", ClusterLabel: stringPtr("in-cluster"), Namespace: stringPtr("shared"), Sync: "Synced", Health: "Healthy"},
		{Name: "web", AppNamespace: stringPtr("argocd"), DestinationServer: "https://site1.example", ClusterLabel: stringPtr("site1.example"), Namespace: stringPtr("apps"), ApplicationSet: stringPtr("websites"), Sync: "OutOfSync", Health: "Healthy"},
		{Name: "worker", AppNamespace: stringPtr("argocd"), DestinationName: "site1", ClusterLabel: stringPtr("site1"), Namespace: stringPtr("apps"), ApplicationSet: stringPtr("websites"), Sync: "Synced", Health: "Healthy"},
	}
	m.rebuildAppIndex()
	return m
}

func TestDestinationServersRegistryAndDrillDown(t *testing.T) {
	m := destinationTestModel()
	m.state.Navigation.View = model.ViewClusters
	items := m.getVisibleItems()
	if len(items) != 3 {
		t.Fatal("server without applications is missing")
	}
	text := stripANSI(m.renderListView(10))
	for _, want := range []string{"localhost", "site1", "site2", "https://site2.example"} {
		if !strings.Contains(text, want) {
			t.Fatalf("server row missing %s:\n%s", want, text)
		}
	}
	m.state.UI.ActiveFilter = "site1.example"
	if len(m.getVisibleItems()) != 1 {
		t.Fatal("address filter failed")
	}
	m.state.Navigation.SelectedIdx = 0
	m.handleKeyMsg(testKeyMsg("enter"))
	apps := m.getVisibleItems()
	if m.state.Navigation.View != model.ViewApps || len(apps) != 2 {
		t.Fatalf("server must open its apps, matching both name and URL destinations: %s %+v", m.state.Navigation.View, apps)
	}
	for _, item := range apps {
		if m.applicationLocation(item.(model.App)) != "site1/apps" {
			t.Fatal("wrong destination label")
		}
	}
	// A streamed app update must retain the canonical server scope.
	m.applyBatchAppUpdate(model.AppUpdatedMsg{App: model.App{Name: "later", DestinationName: "site1", Namespace: stringPtr("apps")}})
	m.rebuildAppIndex()
	if len(m.getVisibleItems()) != 3 {
		t.Fatal("watch update did not match the registered server")
	}
}

func TestDestinationNameFilterAndEmptyContext(t *testing.T) {
	m := destinationTestModel()
	m.state.DestinationServers[1].Name = "friendly-name"
	m.state.Navigation.View = model.ViewClusters
	m.state.UI.ActiveFilter = "friendly-name"
	if len(m.getVisibleItems()) != 1 {
		t.Fatal("server filter must include registered names")
	}
	m.state.Apps = nil
	m.state.UI.ActiveFilter = ""
	m.rebuildAppIndex()
	if m.activeOverlay() != nil || len(m.getVisibleItems()) != 3 {
		t.Fatal("empty apps must not hide registered servers")
	}
}

func TestDestinationScopeUsesProjectAndExactKeys(t *testing.T) {
	m := destinationTestModel()
	m.state.DestinationServers = []model.DestinationServer{
		{Name: "same", Server: "https://one.example", Project: "a"},
		{Name: "same", Server: "https://two.example", Project: "b"},
	}
	m.state.Apps = []model.App{
		{Name: "a", Project: stringPtr("a"), DestinationName: "same"},
		{Name: "b", Project: stringPtr("b"), DestinationName: "same"},
		{Name: "wrong-project", Project: stringPtr("b"), DestinationServer: "https://one.example", ClusterLabel: stringPtr("a@https://one.example")},
	}
	m.rebuildAppIndex()
	if _, err := m.destinationScopeKey("same"); err == nil {
		t.Fatal("ambiguous registered server name must be rejected")
	}
	m.state.Selections.ScopeClusters = map[string]bool{"a@https://one.example": true}
	m.state.Navigation.View = model.ViewApps
	items := m.getVisibleItems()
	if len(items) != 1 || items[0].(model.App).Name != "a" {
		t.Fatal("server scope crossed projects or matched a legacy alias")
	}
}

func TestNamedServerAndApplicationSetCommands(t *testing.T) {
	m := destinationTestModel()
	m.state.Mode = model.ModeCommand
	m.inputComponents.SetCommandValue("servers site2")
	m.handleEnhancedCommandModeKeys(testKeyMsg("enter"))
	if m.state.Navigation.View != model.ViewApps || len(m.getVisibleItems()) != 0 || !m.state.Selections.ScopeClusters["https://site2.example"] {
		t.Fatal("named empty server command must select its canonical scope")
	}
	m.state.Selections.ScopeClusters = model.NewStringSet()
	m.state.Mode = model.ModeCommand
	m.inputComponents.SetCommandValue("appset websites")
	m.handleEnhancedCommandModeKeys(testKeyMsg("enter"))
	if len(m.getVisibleItems()) != 2 || !m.state.Selections.ScopeApplicationSets["argocd/websites"] {
		t.Fatal("unique unqualified AppSet command must remain compatible")
	}
}

func TestDestinationLoadGatesContextAndSequence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/clusters" {
			t.Errorf("unexpected endpoint: %s", r.URL)
		}
		w.Write([]byte(`{"items":[{"name":"site2","server":"https://site2.example"}]}`))
	}))
	defer server.Close()
	m := destinationTestModel()
	m.state.Server = &model.Server{BaseURL: server.URL}
	cmd := m.loadDestinationServers()
	msg := cmd().(destinationServersMsg)
	for _, variant := range []string{"context", "request"} {
		stale := msg
		if variant == "context" {
			stale.SwitchEpoch--
		} else {
			stale.Sequence--
		}
		m.Update(stale)
		if len(m.state.DestinationServers) != 3 || !m.destinationLoading {
			t.Fatal("stale registry load mutated current context")
		}
	}
	m.Update(msg)
	if len(m.state.DestinationServers) != 1 || m.destinationLoading {
		t.Fatal("current registry load was ignored")
	}
	_, cmd = m.refreshCurrentView() // apps refresh remains an app operation
	if cmd == nil {
		t.Fatal("missing app refresh")
	}
	m.state.Navigation.View = model.ViewClusters
	_, cmd = m.handleKeyMsg(testKeyMsg("ctrl+r"))
	if cmd == nil || !m.destinationLoading {
		t.Fatal("server refresh must reload its registry")
	}
}

func TestApplicationSetsAreNamespacedAndReadable(t *testing.T) {
	m := destinationTestModel()
	m.state.Apps = append(m.state.Apps, model.App{Name: "web", AppNamespace: stringPtr("other"), ApplicationSet: stringPtr("websites"), DestinationName: "site2"})
	m.rebuildAppIndex()
	m.state.Navigation.View = model.ViewApplicationSets
	items := m.getVisibleItems()
	if len(items) != 2 || items[0] != "argocd/websites" || items[1] != "other/websites" {
		t.Fatalf("same-named ApplicationSets were merged: %+v", items)
	}
	text := stripANSI(m.renderListView(10))
	for _, want := range []string{"APPSET NAME", "APPS", "argocd/websites", "site1/apps"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	m.handleKeyMsg(testKeyMsg("enter"))
	if m.state.Navigation.View != model.ViewApps || len(m.getVisibleItems()) != 2 {
		t.Fatal("ApplicationSet drill-down crossed namespaces")
	}
	_, _ = m.handleKeyMsg(testKeyMsg("d"))
	if m.inspection == nil || !m.inspection.Target.Application || m.inspection.Target.AppNamespace != "argocd" {
		t.Fatal("describe must bind the Application namespace")
	}
	m.cancelResourceInspection()
}

func TestDestinationAndInspectionLayouts(t *testing.T) {
	for _, size := range []struct{ cols, rows int }{{60, 20}, {80, 24}, {100, 30}, {120, 30}, {160, 40}} {
		for _, view := range []model.View{model.ViewApps, model.ViewClusters, model.ViewApplicationSets} {
			m := destinationTestModel()
			m.state.Terminal.Cols, m.state.Terminal.Rows = size.cols, size.rows
			m.state.Navigation.View = view
			text := m.renderMainLayout()
			assertInspectionLayout(t, text, size.cols, size.rows)
			if size.cols == 80 || size.cols == 120 {
				t.Logf("\n%s %dx%d\n%s", view, size.cols, size.rows, stripANSI(text))
			}
		}
		m := destinationTestModel()
		m.state.Terminal.Cols, m.state.Terminal.Rows = size.cols, size.rows
		m.state.Mode = model.ModeHelp
		assertInspectionLayout(t, m.renderHelpModal(), size.cols, size.rows)
		m.handleKeyMsg(testKeyMsg("G"))
		bottom := m.renderHelpModal()
		assertInspectionLayout(t, bottom, size.cols, size.rows)
		if !strings.Contains(stripANSI(bottom), ":events") {
			t.Fatal("help must scroll to its last section")
		}
		m.state.Mode = model.ModeNormal
		for _, kind := range []string{"YAML", "Describe"} {
			m.startResourceInspection(inspectionTarget{Application: true, AppName: "web", AppNamespace: "argocd", Kind: "Application", Name: "web"}, kind)
			m.state.Diff.Content = []string{"Name:           web", "Namespace:      argocd", "", "Spec:", "  Destination:", "    Name: site1", "    Namespace: apps", "", "Events:", "  No events.", "", "Diff (live -> desired):", "  No changes."}
			m.state.Diff.Loading = false
			text := m.renderDiffView()
			assertInspectionLayout(t, text, size.cols, size.rows)
			if size.cols == 80 {
				t.Logf("\n%s %dx%d\n%s", kind, size.cols, size.rows, stripANSI(text))
			}
			m.cancelResourceInspection()
		}
	}
}

func assertInspectionLayout(t *testing.T, text string, cols, rows int) {
	t.Helper()
	lines := strings.Split(text, "\n")
	if len(lines) > rows {
		t.Fatalf("layout too tall for %dx%d: %d\n%s", cols, rows, len(lines), stripANSI(text))
	}
	for _, line := range lines {
		if lipgloss.Width(line) > cols {
			t.Fatalf("layout overflows %dx%d: width %d\n%s", cols, rows, lipgloss.Width(line), stripANSI(text))
		}
	}
}
