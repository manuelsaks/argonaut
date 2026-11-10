package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darksworm/argonaut/pkg/model"
)

func TestLowBandwidthKeepsAppNamesAndAppSetCountsAcrossProjectedPolls(t *testing.T) {
	var full, compact atomic.Int32
	var detached atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/applicationsets" {
			json.NewEncoder(w).Encode(map[string]any{"items": []any{
				map[string]any{"metadata": map[string]string{"name": "websites", "namespace": "argocd"}},
				map[string]any{"metadata": map[string]string{"name": "websites", "namespace": "team-b"}},
			}})
			return
		}
		if r.URL.Path != "/api/v1/applications" {
			t.Errorf("unexpected request: %s", r.URL)
			w.WriteHeader(404)
			return
		}
		fields := strings.Split(r.URL.Query().Get("fields"), ",")
		projected := r.URL.Query().Get("fields") != ""
		if projected {
			compact.Add(1)
		} else {
			full.Add(1)
		}
		var items []any
		for _, ns := range []string{"argocd", "team-b"} {
			metadata := map[string]any{"name": "web", "namespace": ns, "creationTimestamp": "2026-11-10T10:00:00Z"}
			if !projected && !detached.Load() {
				metadata["ownerReferences"] = []any{map[string]string{"kind": "ApplicationSet", "name": "websites"}}
			}
			if projected {
				for _, field := range []string{"name", "namespace", "creationTimestamp"} {
					if !slices.Contains(fields, "items.metadata."+field) {
						delete(metadata, field)
					}
				}
			}
			items = append(items, map[string]any{"metadata": metadata, "status": map[string]any{"sync": map[string]string{"status": "Synced"}, "health": map[string]string{"status": "Healthy"}}})
		}
		json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]string{"resourceVersion": "rv"}, "items": items})
	}))
	defer server.Close()
	m := buildDeleteTestModel(140, 35)
	m.config.Network.LowBandwidth, m.config.Network.RefreshInterval = true, "0"
	m.state.Server = &model.Server{BaseURL: server.URL}
	now := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.Update(m.startLoadingApplications()())
	m.Update(m.loadApplicationSets()())
	assertOwners := func(want int) {
		t.Helper()
		m.state.Navigation.View = model.ViewApps
		text := stripANSI(m.renderListView(10))
		for _, name := range []string{"argocd/web", "team-b/web"} {
			if !strings.Contains(text, name) {
				t.Fatalf("application identity missing: %s\n%s", name, text)
			}
		}
		m.state.Navigation.View = model.ViewApplicationSets
		for _, key := range []string{"argocd/websites", "team-b/websites"} {
			if count, _ := m.appSetLocation(key); count != want {
				t.Fatalf("%s count=%d, want %d", key, count, want)
			}
		}
	}
	assertOwners(1)
	_, poll := m.handleNetworkPollDue(networkPollDueMsg{m.switchEpoch, m.networkPollSeq})
	m.Update(poll())
	assertOwners(1)
	if full.Load() != 1 || compact.Load() != 1 {
		t.Fatalf("routine poll added unnecessary full requests: full=%d compact=%d", full.Load(), compact.Load())
	}
	// A real ownership change must eventually replace the cache, including
	// removal of an owner; a manual refresh must bypass the TTL immediately.
	detached.Store(true)
	now = now.Add(appMetadataTTL + time.Second)
	_, poll = m.handleNetworkPollDue(networkPollDueMsg{m.switchEpoch, m.networkPollSeq})
	m.Update(poll())
	assertOwners(0)
	detached.Store(false)
	m.Update(m.refreshNetworkSnapshot()())
	assertOwners(1)
	if full.Load() != 3 || compact.Load() != 1 {
		t.Fatalf("unexpected refresh traffic: full=%d compact=%d", full.Load(), compact.Load())
	}
	m.Update(m.loadApplicationSets()())
	m.state.Navigation.View = model.ViewApps
	t.Logf("\nApps:\n%s", stripANSI(m.renderListView(10)))
	m.state.Navigation.View = model.ViewApplicationSets
	t.Logf("\nAppSets:\n%s", stripANSI(m.renderListView(10)))
}

func TestChangingServerInvalidatesOwnershipSnapshot(t *testing.T) {
	m := buildDeleteTestModel(120, 30)
	m.config.Network.LowBandwidth, m.config.Network.RefreshInterval = true, "0"
	m.appMetadataAt = m.now()
	m.Update(model.SetServerMsg{Server: &model.Server{BaseURL: "https://another-context.invalid", Token: "new-identity"}})
	if !m.appMetadataAt.IsZero() {
		t.Fatal("another server/session must fetch its own complete ownership metadata")
	}
}

func TestAllContextsPreserveAppSetCountsDuringCompactRefresh(t *testing.T) {
	m := allContextsTestModel(t)
	driveAllContextLoads(t, m, m.loadAllContexts(false)())
	m.state.Navigation.View = model.ViewApps
	for _, app := range m.state.Apps {
		if app.Name != "web" || applicationOwner(app) != "appset:websites" {
			t.Fatal("projected all-context refresh lost a name or owner")
		}
	}
	m.state.Navigation.View = model.ViewApplicationSets
	for _, contextName := range []string{"dev", "prod"} {
		if count, _ := m.appSetLocation(model.ContextKey(contextName, "argocd/websites")); count != 1 {
			t.Fatalf("%s has wrong AppSet count after compact refresh: %d", contextName, count)
		}
	}
	t.Logf("\nAll-context AppSets:\n%s", stripANSI(m.renderListView(10)))
}
