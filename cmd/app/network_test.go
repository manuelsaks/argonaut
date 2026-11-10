package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/api"
	"github.com/darksworm/argonaut/pkg/config"
	"github.com/darksworm/argonaut/pkg/model"
)

func networkOverviewModel(t *testing.T, serverURL string) *Model {
	t.Helper()
	path := filepath.Join(t.TempDir(), "argocd-config")
	text := fmt.Sprintf("contexts:\n- name: vpn\n  server: %s\n  user: test\nservers:\n- server: %s\n  plain-text: true\nusers:\n- name: test\n  auth-token: fixture-token\n", serverURL, serverURL)
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	m := buildDeleteTestModel(140, 35)
	m.argoConfigPath = path
	m.state.ContextNames = []string{"vpn"}
	updated, cmd := m.openAllContexts()
	m = updated.(*Model)
	t.Cleanup(m.cancelAllContextRequests)
	driveAllContextLoads(t, m, cmd())
	return m
}

func TestOverviewCachesRegistryAndManualRefreshBypassesCache(t *testing.T) {
	var apps, registries, appsets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/applications":
			apps.Add(1)
			fmt.Fprint(w, `{"items":[{"metadata":{"name":"web","namespace":"argocd"}}]}`)
		case "/api/v1/clusters":
			registries.Add(1)
			fmt.Fprint(w, `{"items":[{"name":"site1","server":"https://site1.example"}]}`)
		case "/api/v1/applicationsets":
			appsets.Add(1)
			fmt.Fprint(w, `{"items":[]}`)
		default:
			t.Errorf("unexpected request: %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	m := networkOverviewModel(t, server.URL)
	driveAllContextLoads(t, m, m.loadAllContexts(false)())
	if apps.Load() != 2 || registries.Load() != 1 || appsets.Load() != 1 {
		t.Fatalf("fresh registry was downloaded again: apps=%d registry=%d", apps.Load(), registries.Load())
	}
	m.allContexts.DestinationFetchedAt["vpn"] = m.now().Add(-6 * time.Minute)
	driveAllContextLoads(t, m, m.loadAllContexts(false)())
	if apps.Load() != 3 || registries.Load() != 2 {
		t.Fatal("expired registry was not refreshed")
	}
	driveAllContextLoads(t, m, m.reloadAllContexts()())
	if apps.Load() != 4 || registries.Load() != 3 || appsets.Load() != 2 {
		t.Fatal("manual refresh must bypass cache")
	}
	raw, err := os.ReadFile(m.argoConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.argoConfigPath, []byte(strings.ReplaceAll(string(raw), "fixture-token", "renewed-token")), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := m.loadAllContexts(false)
	if len(m.state.Apps) != 0 || len(m.state.DestinationServers) != 0 {
		t.Fatal("changed credentials reused previous identity's snapshot")
	}
	driveAllContextLoads(t, m, cmd())
	if registries.Load() != 4 {
		t.Fatal("changed credentials did not invalidate registry cache")
	}
}

func TestOverviewRendersAppsWithoutWaitingForRegistry(t *testing.T) {
	m := allContextsTestModel(t)
	commands := m.reloadAllContexts()().(tea.BatchMsg)
	// Commands are split per endpoint: running the app load must neither wait
	// for the registry command nor clear the cached registry while it is pending.
	result := make(chan tea.Msg, 1)
	go func() { result <- commands[0]() }()
	select {
	case msg := <-result:
		appMsg, ok := msg.(allContextLoadedMsg)
		if !ok || appMsg.Error != nil {
			t.Fatalf("app command waited for or combined registry: %T", msg)
		}
		m.Update(appMsg)
		if len(m.state.Apps) != 2 || len(m.allContexts.DestinationPending) != 2 {
			t.Fatal("apps should render while registry loads remain pending")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("app results were blocked by registry")
	}
	// A late registry reply from a superseded refresh must not alter cached data.
	seq := m.allContexts.Sequence
	m.cancelAllContextRequests()
	m.Update(allContextDestinationsMsg{SwitchEpoch: m.switchEpoch, Sequence: seq, ContextName: "dev", Servers: []model.DestinationServer{{Name: "stale"}}})
	if m.allContexts.Destinations["dev"][0].Name == "stale" {
		t.Fatal("accepted old registry response")
	}
}

func TestLowBandwidthPollPreservesModeCursorAndContext(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/v1/applications" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Errorf("unexpected request/credentials: %s", r.URL)
		}
		fmt.Fprint(w, `{"metadata":{"resourceVersion":"new"},"items":[{"metadata":{"name":"aaa","namespace":"other"}},{"metadata":{"name":"web","namespace":"argocd"}},{"metadata":{"name":"web","namespace":"team-b"}}]}`)
	}))
	defer server.Close()
	m := buildDeleteTestModel(120, 30)
	m.config.Network.LowBandwidth = true
	m.config.Network.RefreshInterval = "0"
	m.state.Server = &model.Server{BaseURL: server.URL, Token: "fixture-token"}
	m.state.Apps = []model.App{{Name: "web", AppNamespace: stringPtr("argocd")}, {Name: "web", AppNamespace: stringPtr("team-b")}}
	m.rebuildAppIndex()
	m.state.Navigation.SelectedIdx = 1
	m.state.Mode = model.ModeCommand
	m.inputComponents.SetCommandValue("sync")
	msg := m.refreshNetworkSnapshot()().(networkSnapshotMsg)
	m.Update(msg)
	if requests.Load() != 1 || m.state.Mode != model.ModeCommand || m.inputComponents.GetCommandValue() != "sync" {
		t.Fatal("background refresh interrupted command entry")
	}
	items := m.getVisibleItems()
	if items[m.state.Navigation.SelectedIdx].(model.App).Key() != "team-b/web" || m.lastResourceVersion != "new" {
		t.Fatal("poll changed same-named application selection")
	}
	if m.startWatchingApplications() != nil || m.startWatchingResourceTree(m.state.Apps[0]) != nil {
		t.Fatal("manual-only low-bandwidth mode must not open streams or arm timers")
	}
	m.networkPollLoading = true
	oldSeq := m.networkPollSeq
	m.cleanupAppWatcher()
	m.Update(networkSnapshotMsg{Epoch: m.switchEpoch, Sequence: oldSeq, Result: &api.ListApplicationsResult{Apps: []model.App{{Name: "stale"}}}})
	if len(m.state.Apps) != 3 {
		t.Fatal("accepted cancelled poll")
	}
	if _, cmd := m.handleNetworkPollDue(networkPollDueMsg{m.switchEpoch - 1, m.networkPollSeq}); cmd != nil {
		t.Fatal("old context started a request")
	}
	m.state.Mode = model.ModeConfirmSync
	if _, cmd := m.handleNetworkPollDue(networkPollDueMsg{m.switchEpoch, m.networkPollSeq}); cmd != nil || requests.Load() != 1 {
		t.Fatal("poll fetched during confirmation")
	}
}

func TestLowBandwidthCancellationStopsInFlightRequest(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	m := buildDeleteTestModel(120, 30)
	m.config.Network = config.NetworkConfig{LowBandwidth: true}
	m.state.Server = &model.Server{BaseURL: server.URL}
	_, cmd := m.handleNetworkPollDue(networkPollDueMsg{m.switchEpoch, m.networkPollSeq})
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	m.cleanupAppWatcher()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("context switch left network request running")
	}
	select {
	case msg := <-result:
		m.Update(msg)
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled request did not complete")
	}
	if m.networkPollLoading {
		t.Fatal("cancelled poll remains loading")
	}
}

func TestDescribeLoadsEventsAndDiffConcurrently(t *testing.T) {
	started, release := make(chan string, 2), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("appNamespace") != "team-b" {
			t.Errorf("request lost exact application identity: %s", r.URL)
		}
		if strings.HasSuffix(r.URL.Path, "/events") || strings.HasSuffix(r.URL.Path, "/managed-resources") {
			started <- r.URL.Path
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, `{"items":[]}`)
			return
		}
		fmt.Fprint(w, `{"metadata":{"name":"web","namespace":"team-b"},"spec":{"project":"default"}}`)
	}))
	defer server.Close()
	// Release before Close even on test failure, so blocked handlers can finish.
	defer close(release)
	m := buildDeleteTestModel(120, 30)
	m.state.Server = &model.Server{BaseURL: server.URL}
	cmd := m.startResourceInspection(inspectionTarget{Application: true, Name: "web", Kind: "Application", AppName: "web", AppNamespace: "team-b"}, "Describe")
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("describe serialized events and diff")
		}
	}
	m.cancelResourceInspection()
	select {
	case <-result:
	case <-time.After(3 * time.Second):
		t.Fatal("describe did not cancel both concurrent requests")
	}
}

func TestLowBandwidthTreeRefreshKeepsSelectionAndIgnoresStaleSession(t *testing.T) {
	m := buildEventsPaneTestModel()
	m.config.Network = config.NetworkConfig{LowBandwidth: true, RefreshInterval: "0"}
	m.treeView.SetSelectedIndex(1)
	m.state.Navigation.SelectedIdx = 1
	appName, appNamespace := m.selectedTreeApplication()
	namespace := ""
	if appNamespace != nil {
		namespace = *appNamespace
	}
	loaded := model.ResourceTreeLoadedMsg{AppName: appName, AppNamespace: namespace, SwitchEpoch: m.switchEpoch, TreeSession: m.treeSession, TreeJSON: []byte(`{"nodes":[]}`)}
	due := networkTreeDueMsg{Epoch: m.switchEpoch, Session: m.treeSession}
	m.Update(networkTreeSnapshotMsg{Due: due, Result: loaded})
	if m.state.Navigation.SelectedIdx != 1 {
		t.Fatal("periodic tree snapshot reset navigation")
	}
	m.cleanupTreeWatchers()
	if _, cmd := m.Update(networkTreeSnapshotMsg{Due: due, Result: loaded}); cmd != nil {
		t.Fatal("old tree session restarted polling")
	}
	m.switchEpoch++
	if _, cmd := m.Update(model.RefreshCompletedMsg{SwitchEpoch: m.switchEpoch - 1, Success: true, AppName: appName, AppNamespace: appNamespace}); cmd != nil {
		t.Fatal("old context's refresh triggered new API requests")
	}
}

func TestLowBandwidthHeaderFitsSmallTerminals(t *testing.T) {
	m := allContextsTestModel(t)
	m.config.Network.LowBandwidth = true
	for _, size := range []struct{ cols, rows int }{{60, 20}, {80, 24}, {100, 30}, {120, 30}, {160, 40}} {
		m.state.Terminal.Cols, m.state.Terminal.Rows = size.cols, size.rows
		for _, view := range []model.View{model.ViewApps, model.ViewClusters, model.ViewContexts} {
			m.state.Navigation.View = view
			assertInspectionLayout(t, m.renderMainLayout(), size.cols, size.rows)
		}
	}
}
