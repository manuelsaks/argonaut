package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/api"
	"github.com/darksworm/argonaut/pkg/model"
	"github.com/darksworm/argonaut/pkg/services"
)

func duplicateApps() []model.App {
	a, b := "team-a", "team-b"
	return []model.App{{Name: "shared", AppNamespace: &a}, {Name: "shared", AppNamespace: &b}}
}

func TestBatchMutationsPreserveApplicationIdentity(t *testing.T) {
	for _, operation := range []string{"sync", "delete", "refresh", "resource-sync", "resource-delete"} {
		t.Run(operation, func(t *testing.T) {
			var mu sync.Mutex
			var namespaces, names []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				namespace := r.URL.Query().Get("appNamespace")
				var body struct {
					AppNamespace string `json:"appNamespace"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body.AppNamespace != "" {
					namespace = body.AppNamespace
				}
				mu.Lock()
				namespaces = append(namespaces, namespace)
				names = append(names, r.URL.Path)
				mu.Unlock()
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			m := NewModel(nil)
			m.state.Server = &model.Server{BaseURL: srv.URL, Token: "dummy"}
			m.state.Apps = duplicateApps()
			m.state.Selections.SelectedApps = map[string]bool{"team-a/shared": true, "team-b/shared": true, "ignored": false}
			var cmd tea.Cmd
			switch operation {
			case "sync":
				cmd = m.syncSelectedApplications(false, false, false)
			case "delete":
				cmd = m.deleteSelectedApplications(true, "foreground")
			case "refresh":
				cmd = m.refreshMultipleApplications(false)
			case "resource-sync":
				var targets []model.ResourceSyncTarget
				for _, app := range m.state.Apps {
					targets = append(targets, model.ResourceSyncTarget{AppName: app.Name, AppNamespace: app.AppNamespace, Kind: "ConfigMap", Name: "cfg", Namespace: "workload"})
				}
				cmd = m.syncSelectedResources(targets, false, false)
			case "resource-delete":
				var targets []model.ResourceDeleteTarget
				for _, app := range m.state.Apps {
					targets = append(targets, model.ResourceDeleteTarget{AppName: app.Name, AppNamespace: app.AppNamespace, Kind: "ConfigMap", Name: "cfg", Namespace: "workload", Version: "v1"})
				}
				cmd = m.deleteSelectedResources(targets, DeleteOptions{Cascade: true})
			}
			// Async work must retain the selected identity when UI state changes.
			m.state.Apps = nil
			m.state.Selections.SelectedApps = nil
			msg := cmd()
			mu.Lock()
			defer mu.Unlock()
			if len(namespaces) != 2 {
				t.Fatalf("expected exactly 2 calls, got %v; result: %#v", namespaces, msg)
			}
			seen := map[string]bool{}
			for i, namespace := range namespaces {
				seen[namespace] = true
				if !strings.HasPrefix(names[i], "/api/v1/applications/shared") {
					t.Errorf("invalid application API path %q", names[i])
				}
			}
			if !seen["team-a"] || !seen["team-b"] {
				t.Fatalf("wrong namespaces: %v", namespaces)
			}
		})
	}
}

func TestAmbiguousSelectionsMakeNoRequests(t *testing.T) {
	for _, operation := range []string{"sync", "delete", "resource-sync", "resource-delete"} {
		t.Run(operation, func(t *testing.T) {
			m := NewModel(nil)
			m.state.Server = &model.Server{BaseURL: "http://127.0.0.1:1"}
			m.state.Apps = duplicateApps()
			m.state.Selections.SelectedApps = map[string]bool{"shared": true}
			var cmd tea.Cmd
			switch operation {
			case "sync":
				cmd = m.syncSelectedApplications(false, false, false)
			case "delete":
				cmd = m.deleteSelectedApplications(true, "foreground")
			case "resource-sync":
				cmd = m.syncSelectedResources([]model.ResourceSyncTarget{{AppName: "shared", Kind: "ConfigMap", Name: "cfg"}}, false, false)
			case "resource-delete":
				cmd = m.deleteSelectedResources([]model.ResourceDeleteTarget{{AppName: "shared", Kind: "ConfigMap", Name: "cfg"}}, DeleteOptions{})
			}
			msg := cmd()
			encoded, _ := json.Marshal(msg)
			if !strings.Contains(string(encoded), "ambiguous") {
				t.Fatalf("expected local ambiguity error, got %#v", msg)
			}
		})
	}
}

func TestWatchAndDeleteKeepDuplicateApplicationsSeparate(t *testing.T) {
	m := NewModel(nil)
	m.ready = true
	m.state.Apps = duplicateApps()
	m.state.Index = model.BuildAppIndex(m.state.Apps)
	updated := m.state.Apps[0]
	updated.Health = "Degraded"
	m.applyBatchAppUpdate(model.AppUpdatedMsg{App: updated})
	if len(m.state.Apps) != 2 || m.state.Apps[1].Key() != "team-b/shared" || m.state.Apps[1].Health == "Degraded" {
		t.Fatal("watch update changed the other namespace")
	}
	event := classifyWatchEvent(services.ArgoApiEvent{Type: "app-deleted", AppName: "shared", AppNamespace: "team-a"}, 0)
	if event.deleteName != "team-a/shared" {
		t.Fatalf("watch deletion lost identity: %#v", event)
	}
	m.Update(model.AppDeleteSuccessMsg{AppName: "shared", AppNamespace: updated.AppNamespace})
	if len(m.state.Apps) != 1 || m.state.Apps[0].Key() != "team-b/shared" {
		t.Fatal("deleted wrong list entry")
	}
}

func TestSelectingSameNamedRowsKeepsBothApplications(t *testing.T) {
	m := NewModel(nil)
	m.state.Navigation.View = model.ViewApps
	m.state.Apps = duplicateApps()
	m.state.Index = model.BuildAppIndex(m.state.Apps)
	for _, row := range []int{0, 1} {
		m.state.Navigation.SelectedIdx = row
		m.handleToggleSelection()
	}
	selected, err := m.selectedApplications()
	if err != nil || len(selected) != 2 || !m.state.Selections.SelectedApps["team-a/shared"] || !m.state.Selections.SelectedApps["team-b/shared"] {
		t.Fatalf("rows did not retain separate identities: %v, %v", m.state.Selections.SelectedApps, err)
	}
}

func TestDeleteCommandUsesApplicationNamespace(t *testing.T) {
	for _, command := range []string{"delete", "delete team-b/shared"} {
		t.Run(command, func(t *testing.T) {
			m := buildDeleteTestModel(100, 30)
			m.state.Apps = duplicateApps()
			workload := "workload-namespace"
			m.state.Apps[1].Namespace = &workload
			m.state.Index = model.BuildAppIndex(m.state.Apps)
			m.state.Navigation.View, m.state.Navigation.SelectedIdx = model.ViewApps, 1
			m = runCommand(t, m, command)
			if m.state.Modals.DeleteAppName == nil || *m.state.Modals.DeleteAppName != "shared" || m.state.Modals.DeleteAppNamespace == nil || *m.state.Modals.DeleteAppNamespace != "team-b" {
				t.Fatalf("delete confirmation lost the selected identity: name=%v namespace=%v", m.state.Modals.DeleteAppName, m.state.Modals.DeleteAppNamespace)
			}
		})
	}
}

func TestQualifiedCommandsRetainSelectedApplication(t *testing.T) {
	for _, command := range []string{"sync team-b/shared", "app team-b/shared"} {
		t.Run(command, func(t *testing.T) {
			m := buildDeleteTestModel(100, 30)
			m.state.Apps = duplicateApps()
			m.state.Index = model.BuildAppIndex(m.state.Apps)
			m.state.Navigation.View, m.state.Navigation.SelectedIdx = model.ViewApps, 0
			m = runCommand(t, m, command)
			if strings.HasPrefix(command, "sync") {
				if m.state.Modals.ConfirmTarget == nil || *m.state.Modals.ConfirmTarget != "shared" || m.state.Modals.ConfirmTargetNamespace == nil || *m.state.Modals.ConfirmTargetNamespace != "team-b" {
					t.Fatal("explicit sync target replaced by cursor application")
				}
			} else if !m.state.Selections.SelectedApps["team-b/shared"] || len(m.state.Selections.SelectedApps) != 1 {
				t.Fatal("explicit application selection lost namespace")
			}
		})
	}
}

func TestCurrentTreeMessagesKeepSameNamedApplicationsSeparate(t *testing.T) {
	m := NewModel(nil)
	m.ready = true
	m.state.Navigation.View = model.ViewTree
	data, _ := json.Marshal(api.ResourceTree{Nodes: []api.ResourceNode{{UID: "same-uid", Kind: "ConfigMap", Name: "cfg"}}})
	for _, namespace := range []string{"team-a", "team-b"} {
		m.Update(model.ResourceTreeLoadedMsg{AppName: "shared", AppNamespace: namespace, TreeJSON: data, SwitchEpoch: m.switchEpoch, TreeSession: m.treeSession})
		m.Update(model.ResourceTreeStreamMsg{AppName: "shared", AppNamespace: namespace, TreeJSON: data, SwitchEpoch: m.switchEpoch, TreeSession: m.treeSession})
	}
	if m.treeView.VisibleCount() != 4 {
		t.Fatalf("current tree messages collided: %d rows", m.treeView.VisibleCount())
	}
	m.treeView.SetSelectedIndex(3)
	name, namespace := m.selectedTreeApplication()
	if name != "shared" || namespace == nil || *namespace != "team-b" {
		t.Fatalf("wrong selected owner: %s, %v", name, namespace)
	}
}

func TestK9sApplicationRootPreservesQualifiedIdentity(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "kubeconfig")
	content := "apiVersion: v1\nkind: Config\ncurrent-context: demo\ncontexts:\n- name: demo\n  context:\n    cluster: demo\n    user: demo\nclusters:\n- name: demo\n  cluster:\n    server: https://127.0.0.1\nusers:\n- name: demo\n  user: {}\n"
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", configPath)
	m := NewModel(nil)
	m.state.Apps = duplicateApps()
	m.state.Navigation.View = model.ViewTree
	m.treeView.UpsertAppTree("team-b/shared", &api.ResourceTree{})
	m.handleOpenK9s()
	if m.k9sPendingName != "shared" || m.k9sPendingNamespace != "team-b" {
		t.Fatalf("K9s root identity lost: %s/%s", m.k9sPendingNamespace, m.k9sPendingName)
	}
}

func TestTreeMessagesRejectRetiredSessions(t *testing.T) {
	for _, staleContext := range []bool{false, true} {
		for _, kind := range []string{"stream", "load", "error", "watch-start"} {
			t.Run(kind+map[bool]string{false: "/session", true: "/context"}[staleContext], func(t *testing.T) {
				m := NewModel(nil)
				m.ready = true
				m.state.Navigation.View = model.ViewTree
				m.treeLoading = true
				epoch, session := m.switchEpoch, m.treeSession
				if staleContext {
					epoch--
				} else {
					session--
				}
				data, _ := json.Marshal(api.ResourceTree{Nodes: []api.ResourceNode{{UID: "stale", Kind: "ConfigMap", Name: "stale"}}})
				cleaned := false
				var msg tea.Msg
				switch kind {
				case "stream":
					msg = model.ResourceTreeStreamMsg{AppName: "shared", AppNamespace: "team-a", TreeJSON: data, SwitchEpoch: epoch, TreeSession: session}
				case "load":
					msg = model.ResourceTreeLoadedMsg{AppName: "shared", AppNamespace: "team-a", TreeJSON: data, SwitchEpoch: epoch, TreeSession: session}
				case "error":
					msg = treeLoadFailedMsg{epoch, session, "stale error"}
				case "watch-start":
					msg = treeWatchStartedMsg{cleanup: func() { cleaned = true }, switchEpoch: epoch, treeSession: session}
				}
				_, cmd := m.Update(msg)
				if !m.treeLoading || m.treeView.VisibleCount() != 0 || cmd != nil {
					t.Fatal("retired tree message changed the active session")
				}
				if len(m.treeWatchCleanups) != 0 {
					t.Fatal("retired watcher attached to active session")
				}
				if kind == "watch-start" && !cleaned {
					t.Fatal("retired watcher was not stopped")
				}
			})
		}
	}
}

func TestTreeSessionCleanupInvalidatesQueuedWork(t *testing.T) {
	m := NewModel(nil)
	oldSession, oldChannel, oldDone := m.treeSession, m.treeStream, m.treeStreamDone
	m.cleanupTreeWatchers()
	if m.treeSession == oldSession || m.treeStream == oldChannel {
		t.Fatal("session or delivery channel reused")
	}
	select {
	case <-oldDone:
	default:
		t.Fatal("old consumer was not canceled")
	}
}

func TestDiffFilesArePrivateAndRemoved(t *testing.T) {
	left, right, cleanup, err := writeDiffFiles([]string{"sensitive"}, []string{"changed"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, path := range []string{left, right, filepath.Dir(left)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
			t.Errorf("broad permissions on %s: %v", path, info.Mode())
		}
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(left)); !os.IsNotExist(err) {
		t.Fatalf("temporary directory remains: %v", err)
	}
}

func TestDiffSessionsAlwaysRemoveManifestFiles(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git required")
	}
	for _, kind := range []string{"application", "resource", "rollback"} {
		for _, missingGit := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/viewer", true: "/git-failure"}[missingGit], func(t *testing.T) {
				tmp := t.TempDir()
				t.Setenv("TMP", tmp)
				t.Setenv("TEMP", tmp)
				t.Setenv("TMPDIR", tmp)
				if missingGit {
					t.Setenv("PATH", t.TempDir())
				} else {
					t.Setenv("PATH", filepath.Dir(gitPath))
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/manifests") {
						value := r.URL.Query().Get("revision")
						_ = json.NewEncoder(w).Encode(map[string]any{"manifests": []string{`{"kind":"ConfigMap","data":{"value":"` + value + `"}}`}})
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]string{"kind": "ConfigMap", "name": "cfg", "normalizedLiveState": `{"kind":"ConfigMap","data":{"secret":"dummy"}}`, "predictedLiveState": `{"kind":"ConfigMap","data":{"secret":"changed"}}`}}})
				}))
				defer srv.Close()
				m := NewModel(nil)
				m.state.Server = &model.Server{BaseURL: srv.URL, Token: "dummy"}
				m.config.Diff.Viewer = "exit 0"
				var cmd tea.Cmd
				switch kind {
				case "application":
					cmd = m.startDiffSession("demo", nil)
				case "resource":
					cmd = m.startResourceDiffSession(ResourceIdentifier{AppName: "demo", Kind: "ConfigMap", Name: "cfg"})
				case "rollback":
					cmd = m.startRollbackDiffSession("demo", nil, "before", "after")
				}
				msg := cmd()
				if missingGit {
					if _, ok := msg.(model.ApiErrorMsg); !ok {
						t.Fatalf("expected git start error, got %#v", msg)
					}
				}
				entries, err := os.ReadDir(tmp)
				if err != nil || len(entries) != 0 {
					t.Fatalf("manifest files remained: %v, %v", entries, err)
				}
			})
		}
	}
}
