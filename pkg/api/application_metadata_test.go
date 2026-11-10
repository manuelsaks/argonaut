package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/darksworm/argonaut/pkg/model"
)

func TestApplicationSnapshotsPreserveNamesNamespacesAndOwners(t *testing.T) {
	var full, compact atomic.Int32
	var generation atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/applications" {
			t.Errorf("unexpected per-application request: %s", r.URL)
		}
		fields := strings.Split(r.URL.Query().Get("fields"), ",")
		projected := r.URL.Query().Get("fields") != ""
		if projected {
			compact.Add(1)
			for _, field := range []string{"items.metadata.name", "items.metadata.namespace", "items.metadata.creationTimestamp"} {
				if !slices.Contains(fields, field) {
					t.Errorf("identity field missing from projection: %s", field)
				}
			}
		} else {
			full.Add(1)
		}
		var items []any
		for _, namespace := range []string{"team-a", "team-b"} {
			owner := "websites"
			if generation.Load() > 0 {
				owner = "replacement"
			}
			metadata := map[string]any{"name": "web", "namespace": namespace, "creationTimestamp": fmt.Sprintf("2026-11-10T10:00:0%dZ", generation.Load()), "ownerReferences": []any{map[string]string{"kind": "ApplicationSet", "name": owner}}}
			if projected {
				// Emulate the exact field allowlist from Argo CD's application
				// forwarder. ownerReferences is unavailable in projected lists.
				delete(metadata, "ownerReferences")
				for _, field := range []string{"name", "namespace", "creationTimestamp"} {
					if !slices.Contains(fields, "items.metadata."+field) {
						delete(metadata, field)
					}
				}
			}
			items = append(items, map[string]any{"metadata": metadata, "spec": map[string]any{"destination": map[string]string{"namespace": "deployment-namespace"}}, "status": map[string]any{"sync": map[string]string{"status": "Synced"}}})
		}
		json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]string{"resourceVersion": "rv"}, "items": items})
	}))
	defer server.Close()
	svc := NewApplicationService(&model.Server{BaseURL: server.URL})
	initial, err := svc.ListApplicationsWithMeta(t.Context())
	if err != nil || len(initial.Apps) != 2 || !initial.FullMetadata || initial.Apps[0].Name != "web" || initial.Apps[0].ApplicationSet == nil {
		t.Fatalf("initial identity/owner missing: %+v, %v", initial, err)
	}
	if initial.Apps[0].Key() == initial.Apps[1].Key() || initial.Apps[0].ApplicationSetKey() != "team-a/websites" {
		t.Fatal("must use the Application namespace, not its deployment namespace")
	}
	polled, err := svc.ListApplicationsWithCachedOwners(t.Context(), initial.Apps)
	if err != nil || polled.FullMetadata || len(polled.Apps) != 2 || *polled.Apps[0].ApplicationSet != "websites" || full.Load() != 1 || compact.Load() != 1 {
		t.Fatalf("compact poll lost ownership or added requests: %v full=%d compact=%d", err, full.Load(), compact.Load())
	}
	// A recreated Application must not inherit the old owner's metadata just
	// because it reuses the same namespace/name.
	generation.Store(1)
	recreated, err := svc.ListApplicationsWithCachedOwners(t.Context(), polled.Apps)
	if err != nil || !recreated.FullMetadata || *recreated.Apps[0].ApplicationSet != "replacement" || full.Load() != 2 || compact.Load() != 2 {
		t.Fatal("recreated app reused stale ownership instead of one bulk refresh")
	}
	// A new identity in a projected snapshot also needs one complete list.
	added, err := svc.ListApplicationsWithCachedOwners(t.Context(), recreated.Apps[:1])
	if err != nil || !added.FullMetadata || len(added.Apps) != 2 || full.Load() != 3 || compact.Load() != 3 {
		t.Fatal("new application did not trigger a single bulk refresh")
	}
}

func TestApplicationListRejectsRowsWithoutIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"items":[{"spec":{"project":"default"},"status":{"sync":{"status":"Synced"}}}]}`)
	}))
	defer server.Close()
	if _, err := NewApplicationService(&model.Server{BaseURL: server.URL}).ListApplicationsWithMeta(t.Context()); err == nil || !strings.Contains(err.Error(), "metadata.name") {
		t.Fatal("nameless rows must not be accepted into the UI or operation index")
	}
}
