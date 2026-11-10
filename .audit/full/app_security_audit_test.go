package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/darksworm/argonaut/pkg/api"
	"github.com/darksworm/argonaut/pkg/model"
	"github.com/darksworm/argonaut/pkg/tui/treeview"
)

// These tests document observed security properties of the audited commit.
// PASS means the stated behavior was reproduced, not that the behavior is safe.
// All API traffic goes to a synthetic loopback server using a dummy token.

func TestAuditBatchSyncOmitsSelectedApplicationNamespace(t *testing.T) {
	var query string
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	ns := "team-a"
	m := NewModel(nil)
	m.state.Server = &model.Server{BaseURL: srv.URL, Token: "audit-dummy"}
	m.state.Apps = []model.App{{Name: "same-name", AppNamespace: &ns}}
	m.state.Selections.SelectedApps = map[string]bool{"same-name": true}
	msg := m.syncSelectedApplications(false, false, false)()
	if done, ok := msg.(model.MultiSyncCompletedMsg); !ok || !done.Success {
		t.Fatalf("unexpected result type %T", msg)
	}
	if strings.Contains(query, "appNamespace") {
		t.Fatalf("behavior changed: namespace now included")
	}
	if ns, ok := payload["appNamespace"]; ok && ns != "" && ns != nil {
		t.Fatal("behavior changed: namespace now nonempty in payload")
	}
	t.Log("CONFIRMED: selected team-a/same-name produced a sync without appNamespace")
}

func TestAuditBatchDeleteUsesFirstDuplicateName(t *testing.T) {
	var target string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target = r.URL.Query().Get("appNamespace")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	nsDefault, nsTeam := "argocd", "team-a"
	m := NewModel(nil)
	m.state.Server = &model.Server{BaseURL: srv.URL, Token: "audit-dummy"}
	m.state.Apps = []model.App{{Name: "same-name", AppNamespace: &nsDefault}, {Name: "same-name", AppNamespace: &nsTeam}}
	m.state.Index = model.BuildAppIndex(m.state.Apps)
	m.state.Navigation.View = model.ViewApps
	m.state.Navigation.SelectedIdx = 1
	m.state.Selections.SelectedApps = map[string]bool{"same-name": true}
	_ = m.deleteSelectedApplications(true, "foreground")()
	if target != "argocd" {
		t.Fatalf("behavior changed: first duplicate no longer targeted, got %q", target)
	}
	t.Log("CONFIRMED: a selected duplicate name targets first matching Application in argocd")
}

func TestAuditWatchUpdateOverwritesOtherNamespace(t *testing.T) {
	nsA, nsB := "team-a", "team-b"
	m := NewModel(nil)
	m.state.Apps = []model.App{{Name: "same-name", AppNamespace: &nsA}, {Name: "same-name", AppNamespace: &nsB}}
	m.state.Index = model.BuildAppIndex(m.state.Apps)
	m.applyBatchAppUpdate(model.AppUpdatedMsg{App: model.App{Name: "same-name", AppNamespace: &nsA}})
	if len(m.state.Apps) != 2 || m.state.Apps[1].AppNamespace == nil || *m.state.Apps[1].AppNamespace != "team-a" {
		t.Fatal("behavior changed: duplicate index no longer overwrites other namespace")
	}
	t.Log("CONFIRMED: team-a watch event replaced the team-b list entry")
}

func TestAuditEventRenderingRetainsTerminalControl(t *testing.T) {
	payload := "\x1b]52;c;QVVESVQ=\x07"
	event := model.ResourceEvent{Type: "Normal", Reason: "Audit", Message: "message " + payload, Count: 1, LastSeen: time.Now()}
	out := strings.Join(renderEventCards([]model.ResourceEvent{event}, 100, time.Now(), ""), "\n")
	if !strings.Contains(out, payload) {
		t.Fatal("behavior changed: renderer removed the OSC52 payload")
	}
	t.Log("CONFIRMED: event renderer retains OSC52 bytes; terminal execution was not attempted")
}

func TestAuditFinalTerminalRenderingControlHandling(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		want          bool
	}{
		{"direct-OSC52", "\x1b]52;c;QVVESVQ=\x07", false},
		{"DCS-tmux-passthrough", "\x1bPtmux;\x1b\x1b]52;c;QVVESVQ=\x07\x1b\\", false},
		{"DCS-device-control", "\x1bPqAUDIT\x1b\\", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := model.ResourceEvent{Reason: "Audit", Message: "before " + tc.payload + " after", Count: 1, LastSeen: time.Now()}
			rendered := strings.Join(renderEventCards([]model.ResourceEvent{event}, 100, time.Now(), ""), "\n")
			var captured bytes.Buffer
			screen := uv.NewTerminalScreen(&captured, uv.Environ{"TERM=xterm-256color"})
			screen.Resize(100, 10)
			if err := screen.Display(uv.NewStyledString(rendered)); err != nil {
				t.Fatal(err)
			}
			found := bytes.Contains(captured.Bytes(), []byte(tc.payload))
			if found != tc.want {
				t.Fatalf("pass-through=%v, expected %v", found, tc.want)
			}
			t.Logf("Terminal output captured in memory only: payload pass-through=%v", found)
		})
	}
}

func TestAuditManifestDiffLeavesPlaintextTempFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	t.Setenv("TMPDIR", tmp)
	sentinel := "AUDIT-SYNTHETIC-SENSITIVE-VALUE"
	resource := map[string]any{"group": "", "kind": "ConfigMap", "namespace": "default", "name": "audit-config", "normalizedLiveState": `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"audit-config"},"data":{"password":"` + sentinel + `"}}`, "predictedLiveState": `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"audit-config"},"data":{"password":"changed"}}`}
	response, _ := json.Marshal(map[string]any{"items": []any{resource}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(response)
	}))
	defer srv.Close()
	m := NewModel(nil)
	m.state.Server = &model.Server{BaseURL: srv.URL, Token: "audit-dummy"}
	_ = m.startResourceDiffSession(ResourceIdentifier{AppName: "audit-app", Kind: "ConfigMap", Name: "audit-config", Namespace: "default"})()
	matches, err := filepath.Glob(filepath.Join(tmp, "current-*.yaml"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one persisted current YAML, got %d (%v)", len(matches), err)
	}
	content, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), sentinel) {
		t.Fatal("sensitive sentinel absent from persisted YAML")
	}
	t.Log("CONFIRMED: diff returned while plaintext current-*.yaml remained on disk")
}

func TestAuditBatchCommandContinuesAfterContextSwitch(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write([]byte(`{}`)) }))
	defer srv.Close()
	m := NewModel(nil)
	m.state.Server = &model.Server{BaseURL: srv.URL, Token: "old-context-dummy"}
	m.state.Selections.SelectedApps = map[string]bool{"audit-app": true}
	cmd := m.syncSelectedApplications(false, false, false)
	m.switchEpoch++
	m.state.Server = &model.Server{BaseURL: "http://127.0.0.1:1", Token: "new-context-dummy"}
	_ = cmd()
	if calls != 1 {
		t.Fatalf("expected queued operation to old server, got %d", calls)
	}
	t.Log("CONFIRMED: queued mutation remains bound to old server and is not cancelled by epoch change")
}

func TestAuditResourceDeletionLosesApplicationNamespace(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { query = r.URL.RawQuery; w.Write([]byte(`{}`)) }))
	defer srv.Close()
	ns := "team-a"
	m := NewModel(nil)
	m.state.Server = &model.Server{BaseURL: srv.URL, Token: "audit-dummy"}
	m.state.Apps = []model.App{{Name: "selected", AppNamespace: &ns}}
	target := model.ResourceDeleteTarget{AppName: "selected", Name: "audit-config", Kind: "ConfigMap", Namespace: "workload", Version: "v1"}
	msg := m.deleteSelectedResources([]model.ResourceDeleteTarget{target}, DeleteOptions{Cascade: true, PropagationPolicy: "foreground"})()
	if _, ok := msg.(model.ResourceDeleteSuccessMsg); !ok {
		t.Fatalf("unexpected result %T", msg)
	}
	if strings.Contains(query, "appNamespace") {
		t.Fatal("behavior changed: namespace now included")
	}
	t.Log("CONFIRMED: resource deletion does not send the selected Application namespace")
}

func TestAuditTreeStreamHasNoContextEpochGuard(t *testing.T) {
	m := NewModel(nil)
	m.ready = true
	m.switchEpoch = 5
	m.state.Server = &model.Server{BaseURL: "https://new-context.example", Token: "new-context-dummy"}
	m.state.Navigation.View = model.ViewTree
	m.treeView = treeview.NewTreeView(100, 30)
	m.treeView.SetAppMeta("shared", "Healthy", "Synced")
	m.treeView.UpsertAppTree("shared", &api.ResourceTree{Nodes: []api.ResourceNode{{UID: "new", Kind: "ConfigMap", Name: "new-context-object"}}})
	stale, _ := json.Marshal(api.ResourceTree{Nodes: []api.ResourceNode{{UID: "old", Kind: "ConfigMap", Name: "old-context-object"}}})
	updated, _ := m.Update(model.ResourceTreeStreamMsg{AppName: "shared", TreeJSON: stale})
	result := updated.(*Model)
	result.treeView.SetSelectedIndex(1)
	selected, ok := result.treeView.SelectedResourceDetail()
	if !ok || selected.Name != "old-context-object" {
		t.Fatalf("behavior changed: stale tree not installed")
	}
	t.Log("CONFIRMED: stream handler has no epoch guard and accepted an old-context payload")
}
