package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/config"
	apperrors "github.com/darksworm/argonaut/pkg/errors"
	"github.com/darksworm/argonaut/pkg/model"
	"gopkg.in/yaml.v3"
)

func allContextsTestModel(t *testing.T) *Model {
	t.Helper()
	cli := config.ArgoCLIConfig{CurrentContext: "dev"}
	for _, name := range []string{"dev", "prod"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer token-"+name {
				t.Errorf("%s received wrong context credentials", name)
			}
			switch r.URL.Path {
			case "/api/v1/applications":
				metadata := map[string]any{"name": "web", "namespace": "argocd", "creationTimestamp": "2026-11-10T10:00:00Z", "ownerReferences": []any{map[string]string{"kind": "ApplicationSet", "name": "websites"}}}
				if fields := r.URL.Query().Get("fields"); fields != "" {
					// Argo CD's list forwarder matches exact allowlisted paths:
					// items.metadata and ownerReferences are not supported.
					delete(metadata, "ownerReferences")
					for _, field := range []string{"name", "namespace", "creationTimestamp"} {
						if !strings.Contains(","+fields+",", ",items.metadata."+field+",") {
							delete(metadata, field)
						}
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"metadata": metadata, "spec": map[string]any{"destination": map[string]string{"name": "site1", "namespace": "apps"}}, "status": map[string]any{"sync": map[string]string{"status": "Synced"}, "health": map[string]string{"status": "Healthy"}}}}})
			case "/api/v1/clusters":
				w.Write([]byte(`{"items":[{"name":"site1","server":"https://same-cluster.example"}]}`))
			case "/api/v1/applicationsets":
				fmt.Fprint(w, `{"items":[{"metadata":{"name":"websites","namespace":"argocd"}},{"metadata":{"name":"empty-set","namespace":"argocd"}}]}`)
			case "/api/v1/applications/web":
				if r.URL.Query().Get("appNamespace") != "argocd" {
					t.Error("missing Application namespace")
				}
				json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]string{"name": "web", "namespace": "argocd"}, "spec": map[string]string{"contextMarker": name}})
			default:
				t.Errorf("unexpected %s request %s", name, r.URL)
				w.WriteHeader(404)
			}
		}))
		t.Cleanup(server.Close)
		cli.Contexts = append(cli.Contexts, config.ArgoContext{Name: name, Server: server.URL, User: name})
		cli.Servers = append(cli.Servers, config.ArgoServer{Server: server.URL, PlainText: true})
		cli.Users = append(cli.Users, config.ArgoUser{Name: name, AuthToken: "token-" + name})
	}
	path := filepath.Join(t.TempDir(), "argocd-config")
	raw, err := yaml.Marshal(cli)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	m := buildDeleteTestModel(140, 35)
	m.argoConfigPath = path
	m.state.ContextNames = []string{"dev", "prod"}
	updated, cmd := m.openAllContexts()
	m = updated.(*Model)
	t.Cleanup(m.cancelAllContextRequests)
	driveAllContextLoads(t, m, cmd())
	return m
}

func driveAllContextLoads(t *testing.T, m *Model, msg tea.Msg) {
	t.Helper()
	switch msg := msg.(type) {
	case tea.BatchMsg:
		for _, cmd := range msg {
			driveAllContextLoads(t, m, cmd())
		}
	case allContextLoadedMsg:
		m.Update(msg)
	case allContextDestinationsMsg:
		m.Update(msg)
	case applicationSetsMsg:
		m.Update(msg)
	default:
		t.Fatalf("unexpected all-context load result %T", msg)
	}
}

func TestAllContextsIsolatesSameNamedApplicationsAndServers(t *testing.T) {
	m := allContextsTestModel(t)
	if m.state.Server != nil || len(m.state.Apps) != 2 || len(m.state.DestinationServers) != 2 {
		t.Fatal("overview must aggregate without a default API server")
	}
	if m.state.Apps[0].Key() == m.state.Apps[1].Key() {
		t.Fatal("identical namespace/name in different contexts collided")
	}
	if m.findAppByNameAndNamespace("web", "") != nil || m.findAppByNameAndNamespace("argocd/web", "") != nil {
		t.Fatal("unqualified operations must reject ambiguous contexts")
	}
	for _, app := range m.state.Apps {
		if applicationOwner(app) != "appset:websites" {
			t.Fatal("owner must be available on the first load")
		}
		server, ok := model.ResolveDestination(app, m.state.DestinationServers)
		if !ok || server.ContextName != app.ContextName {
			t.Fatal("destination resolution crossed contexts")
		}
		m.state.Selections.ScopeClusters = map[string]bool{server.Key(): true}
		items := m.getVisibleItems()
		if len(items) != 1 || items[0].(model.App).ContextName != app.ContextName {
			t.Fatal("server scope crossed contexts")
		}
	}
	m.state.Selections.ScopeClusters = model.NewStringSet()
	m.state.UI.ActiveFilter = "prod"
	if len(m.getVisibleItems()) != 1 {
		t.Fatal("context name must be searchable")
	}
	if !strings.Contains(stripANSI(m.renderListView(10)), "CONTEXT") {
		t.Fatal("overview needs the context column")
	}
	_, cmd := m.handleKeyMsg(testKeyMsg("y"))
	msg := cmd().(resourceInspectionMsg)
	if msg.Error != nil || !strings.Contains(msg.Text, "contextMarker: prod") {
		t.Fatalf("YAML used the wrong Argo CD server: %s %v", msg.Text, msg.Error)
	}
	m.Update(msg)
	m.handleKeyMsg(testKeyMsg("esc"))
	updated, _, handled := m.handleOverviewAction(testKeyMsg("ctrl+d"))
	child := updated.(*Model)
	if !handled || child == m || child.currentContextName != "prod" || child.state.Server.Token != "token-prod" || child.state.Mode != model.ModeConfirmAppDelete {
		t.Fatal("delete confirmation did not bind the selected context")
	}
	if child.allOverview != m {
		t.Fatal("context action must retain a way back to the overview")
	}
}

func TestAllContextRepliesAreGatedAndFailuresAreVisible(t *testing.T) {
	m := allContextsTestModel(t)
	for _, variant := range []string{"epoch", "sequence", "context", "duplicate"} {
		msg := allContextLoadedMsg{SwitchEpoch: m.switchEpoch, Sequence: m.allContexts.Sequence, ContextName: "dev", Apps: []model.App{{Name: "stale"}}}
		switch variant {
		case "epoch":
			msg.SwitchEpoch--
		case "sequence":
			msg.Sequence--
		case "context":
			msg.ContextName = "unknown"
		}
		m.Update(msg)
		if len(m.state.Apps) != 2 {
			t.Fatalf("accepted %s reply", variant)
		}
	}
	m.allContexts.Pending["dev"] = true
	m.Update(allContextLoadedMsg{SwitchEpoch: m.switchEpoch, Sequence: m.allContexts.Sequence, ContextName: "dev", Error: fmt.Errorf("401 unauthorized")})
	if len(m.state.Apps) != 1 || m.state.Apps[0].ContextName != "prod" || !strings.Contains(m.allContexts.Errors["dev"], "401 unauthorized") {
		t.Fatal("one context failure must preserve other context results")
	}
	seq := m.allContexts.Sequence
	m.cancelAllContextRequests()
	_, cmd := m.Update(allContextsRefreshMsg{m.switchEpoch, seq})
	if cmd != nil {
		t.Fatal("cancelled overview must not restart its polling")
	}
}

func TestAllChoicesInContextAndServerPickers(t *testing.T) {
	m := allContextsTestModel(t)
	m.openContextPicker()
	items := m.getVisibleItems()
	if items[len(items)-1] != allContextsChoice || m.state.Navigation.SelectedIdx != len(items)-1 {
		t.Fatal("all option should be visible and selected")
	}
	if m.selectedArgoContext() != "" {
		t.Fatal("virtual all option must not be passed to Argo CD CLI login")
	}
	m.state.Navigation.View = model.ViewClusters
	m.state.UI.ActiveFilter = ""
	items = m.getVisibleItems()
	if len(items) != 3 || items[0] != allServersChoice {
		t.Fatal("server list needs all plus per-context destinations")
	}
	m.state.Navigation.SelectedIdx = 0
	m.state.Selections.ScopeClusters = map[string]bool{"restrictive": true}
	m.handleKeyMsg(testKeyMsg("enter"))
	if m.state.Navigation.View != model.ViewApps || len(m.getVisibleItems()) != 2 {
		t.Fatal("server all must clear the destination scope")
	}
}

func TestAllContextNamedCommandAndLateWatchStayIsolated(t *testing.T) {
	m := allContextsTestModel(t)
	m.config.DefaultView = "namespace unrelated-default"
	m.state.Mode = model.ModeCommand
	m.inputComponents.SetCommandValue("delete prod::argocd/web")
	updated, _ := m.handleEnhancedCommandModeKeys(testKeyMsg("enter"))
	child := updated.(*Model)
	if child == m || child.currentContextName != "prod" || child.state.Mode != model.ModeConfirmAppDelete {
		t.Fatal("qualified command must enter the right context before confirmation")
	}
	cleaned := false
	child.watchStartSequence = 1
	child.Update(watchStartedMsg{switchEpoch: child.switchEpoch - 1, startSequenceNum: 1, cleanup: func() { cleaned = true }})
	if !cleaned || child.watchChan != nil {
		t.Fatal("watch from previous context must be retired even if request sequence matches")
	}
	child.state.Mode = model.ModeNormal
	updated, _ = child.handleKeyMsg(testKeyMsg("esc"))
	parent := updated.(*Model)
	if parent != m || parent.state.Mode != model.ModeNormal || parent.inputComponents.GetCommandValue() != "" {
		t.Fatal("Esc must return to a usable overview")
	}
}

func TestAllContextAutoLoginUsesFailedSourceAndKeepsOverview(t *testing.T) {
	m := allContextsTestModel(t)
	m.config.Auth.AutoRelogin = map[string]bool{"prod": true}
	m.allContexts.Pending["prod"] = true
	_, cmd := m.handleAllContextLoaded(allContextLoadedMsg{SwitchEpoch: m.switchEpoch, Sequence: m.allContexts.Sequence, ContextName: "prod", Error: apperrors.New(apperrors.ErrorAuth, "UNAUTHORIZED", "session expired")})
	if cmd == nil || !m.reloginInProgress || m.reloginContext != "prod" || m.state.Mode != model.ModeNormal {
		t.Fatal("auto-login must use the failed real context without hiding the overview")
	}
	m.Update(contextReloginMsg{SwitchEpoch: m.switchEpoch, RequestID: m.reloginSeq, ContextName: "prod", Error: fmt.Errorf("SSO cancelled")})
	if m.state.Mode != model.ModeNormal || m.reloginInProgress {
		t.Fatal("failed SSO must preserve other contexts")
	}
	if _, handled := m.maybeAutoReloginForContext("prod"); handled {
		t.Fatal("cancelled SSO must be throttled")
	}
	m.cancelContextRelogin()
}
