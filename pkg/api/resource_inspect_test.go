package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/darksworm/argonaut/pkg/model"
)

func TestResourceObjectFullLiveAndIdentity(t *testing.T) {
	for _, namespace := range []string{"workloads", ""} {
		t.Run("namespace="+namespace, func(t *testing.T) {
			manifest := `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","uid":"p1","managedFields":[{"manager":"controller"}]},"status":{"phase":"Running"},"custom":9007199254740993}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/applications/web/resource" {
					t.Errorf("path = %s", r.URL.Path)
				}
				for key, value := range map[string]string{"appNamespace": "team-b", "resourceName": "web-1", "namespace": namespace, "group": "", "version": "v1", "kind": "Pod"} {
					if !r.URL.Query().Has(key) || r.URL.Query().Get(key) != value {
						t.Errorf("%s: query = %s", key, r.URL.RawQuery)
					}
				}
				if r.Header.Get("Authorization") != "Bearer example-token" {
					t.Error("missing Argo CD authentication")
				}
				json.NewEncoder(w).Encode(map[string]string{"manifest": manifest})
			}))
			defer server.Close()
			result, err := NewApplicationService(&model.Server{BaseURL: server.URL, Token: "example-token"}).GetResourceObject(t.Context(), ResourceQuery{AppName: "web", AppNamespace: "team-b", Version: "v1", Kind: "Pod", Name: "web-1", Namespace: namespace})
			if err != nil {
				t.Fatal(err)
			}
			if result.Desired || string(result.Manifest) != manifest {
				t.Fatalf("full live object lost: %+v", result)
			}
		})
	}
}

func TestResourceObjectDesiredFallbackOnlyOnNotFound(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var fallback atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("appNamespace") != "team-b" {
					t.Error("lost owning Application namespace")
				}
				if strings.HasSuffix(r.URL.Path, "/resource") {
					w.WriteHeader(status)
					w.Write([]byte(`{"message":"unavailable"}`))
					return
				}
				fallback.Add(1)
				json.NewEncoder(w).Encode(ManagedResourcesResponse{Items: []ManagedResourceDiff{
					{Group: "apps", Kind: "Deployment", Name: "web", Namespace: "wrong", TargetState: `{"metadata":{"namespace":"wrong"}}`},
					{Group: "apps", Kind: "Deployment", Name: "web", Namespace: "right", TargetState: `{"metadata":{"namespace":"right"},"spec":{"replicas":3}}`},
				}})
			}))
			defer server.Close()
			result, err := NewApplicationService(&model.Server{BaseURL: server.URL}).GetResourceObject(t.Context(), ResourceQuery{AppName: "web", AppNamespace: "team-b", Group: "apps", Version: "v1", Kind: "Deployment", Name: "web", Namespace: "right"})
			if status == http.StatusNotFound {
				if err != nil {
					t.Fatal(err)
				}
				if !result.Desired || !strings.Contains(string(result.Manifest), `"right"`) || fallback.Load() != 1 {
					t.Fatalf("wrong desired manifest: %+v", result)
				}
			} else if err == nil || fallback.Load() != 0 {
				t.Fatalf("authorization errors must not use fallback: err=%v calls=%d", err, fallback.Load())
			}
		})
	}
}

func TestInspectionApplicationObjectsPreserveExtraFields(t *testing.T) {
	for _, appset := range []bool{false, true} {
		t.Run(map[bool]string{false: "Application", true: "ApplicationSet"}[appset], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := "appNamespace"
				if appset {
					key = "appsetNamespace"
				}
				if r.URL.Query().Get(key) != "team & b" {
					t.Errorf("namespace not escaped: %s", r.URL)
				}
				w.Write([]byte(`{"metadata":{"name":"web"},"spec":{"unknownToTypedModel":true},"status":{"custom":"kept"}}`))
			}))
			defer server.Close()
			service := NewApplicationService(&model.Server{BaseURL: server.URL})
			var object json.RawMessage
			var err error
			if appset {
				object, err = service.GetApplicationSetObject(t.Context(), "web", "team & b")
			} else {
				object, err = service.GetApplicationObject(t.Context(), "web", "team & b")
			}
			if err != nil || !strings.Contains(string(object), "unknownToTypedModel") {
				t.Fatalf("object=%s err=%v", object, err)
			}
		})
	}
}

func TestDestinationRegistryDropsCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/clusters" {
			t.Errorf("wrong endpoint: %s", r.URL)
		}
		w.Write([]byte(`{"items":[{"name":"localhost","server":"https://kubernetes.default.svc","config":{"bearerToken":"sensitive-token"}},{"name":"site1","server":"https://site1.example"},{"name":"site2","server":"https://site2.example"}]}`))
	}))
	defer server.Close()
	servers, err := NewApplicationService(&model.Server{BaseURL: server.URL}).ListDestinationServers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(servers)
	if len(servers) != 3 || strings.Contains(string(raw), "sensitive-token") || strings.Contains(string(raw), "bearerToken") {
		t.Fatalf("unsafe/incomplete display metadata: %s", raw)
	}
}

func TestEmptyApplicationAndManagedResourceLists(t *testing.T) {
	for _, body := range []string{`{"metadata":{"resourceVersion":"7"},"items":[]}`, `{"items":null}`, `{}`, `[]`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer server.Close()
			service := NewApplicationService(&model.Server{BaseURL: server.URL})
			apps, err := service.ListApplicationsWithMeta(t.Context())
			if err != nil || len(apps.Apps) != 0 {
				t.Fatalf("empty context must load successfully: %+v %v", apps, err)
			}
			if strings.Contains(body, "resourceVersion") && apps.ResourceVersion != "7" {
				t.Fatal("lost resource version")
			}
			diffs, err := service.GetManagedResourceDiffs(t.Context(), "web", "team-b")
			if err != nil || len(diffs) != 0 {
				t.Fatalf("empty diff: %+v %v", diffs, err)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"items":`)) }))
	defer server.Close()
	if _, err := NewApplicationService(&model.Server{BaseURL: server.URL}).GetManagedResourceDiffs(t.Context(), "web", "team-b"); err == nil {
		t.Fatal("invalid response must not be reported as no changes")
	}
}
