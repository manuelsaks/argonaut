package kargo

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOlderKargoConnectJSONAndRawObjects(t *testing.T) {
	var writes, projectReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("Connect lost credentials")
		}
		if !strings.HasPrefix(r.URL.Path, connectService) {
			fmt.Fprint(w, "<html>legacy Kargo UI</html>")
			return
		}
		if r.Method != "POST" || r.Header.Get("Connect-Protocol-Version") != "1" {
			t.Error("incorrect Connect framing")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		switch strings.TrimPrefix(r.URL.Path, connectService) {
		case "ListProjects":
			projectReads.Add(1)
			if body["page"].(float64) == 0 {
				fmt.Fprint(w, `{"projects":[{"metadata":{"name":"team-a"}}],"total":2}`)
			} else {
				fmt.Fprint(w, `{"projects":[{"metadata":{"name":"team-b"}}],"total":2}`)
			}
		case "ListStages":
			if body["project"] != "team-b" {
				t.Error("Connect project scope lost")
			}
			fmt.Fprint(w, `{"stages":[{"metadata":{"name":"prod","namespace":"team-b"},"status":{"phase":"Steady"}}]}`)
		case "QueryFreight":
			if body["stage"] != "prod" || body["project"] != "team-b" {
				t.Error("Connect freight selection lost stage")
			}
			fmt.Fprint(w, `{"groups":{"":{"freight":[{"metadata":{"name":"freight-a","namespace":"team-b"}}]}}}`)
		case "PromoteToStage":
			writes.Add(1)
			if body["stage"] != "prod" || body["project"] != "team-b" || body["freight"] != "freight-a" {
				t.Error("Connect promotion lost exact target")
			}
			fmt.Fprint(w, `{"promotion":{"metadata":{"name":"promo-1","namespace":"team-b"},"spec":{"stage":"prod","freight":"freight-a"},"status":{"phase":"Running"}}}`)
		case "GetPromotion":
			if body["format"] != "RAW_FORMAT_JSON" || body["project"] != "team-b" || body["name"] != "promo-1" {
				t.Error("raw inspection request incorrect")
			}
			raw := `{"apiVersion":"kargo.akuity.io/v1alpha1","kind":"Promotion","metadata":{"name":"promo-1","namespace":"team-b"},"spec":{"steps":[{"config":{"someUnknownField":9007199254740993}}]},"status":{"phase":"Errored","message":"push denied"}}`
			fmt.Fprintf(w, `{"raw":%q}`, base64.StdEncoding.EncodeToString([]byte(raw)))
		default:
			t.Errorf("unexpected RPC %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client, err := NewClient(testConnection(t, server.URL, "fixture-token"))
	if err != nil {
		t.Fatal(err)
	}
	projects, err := client.List(t.Context(), "Project", "", "", "")
	if err != nil || len(projects) != 2 || projectReads.Load() != 2 || !client.UsesConnect() {
		t.Fatalf("Connect detection/pagination: %d %v", len(projects), err)
	}
	stages, err := client.List(t.Context(), "Stage", "team-b", "", "")
	if err != nil || len(stages) != 1 || stages[0].Project != "team-b" {
		t.Fatalf("Connect stages: %+v %v", stages, err)
	}
	freight, err := client.List(t.Context(), "Freight", "team-b", "prod", "")
	if err != nil || len(freight) != 1 {
		t.Fatalf("Connect freight: %+v %v", freight, err)
	}
	promotion, err := client.Promote(t.Context(), "team-b", "prod", "freight-a")
	if err != nil || promotion.Phase() != "Running" || writes.Load() != 1 {
		t.Fatalf("Connect promotion: %+v %v", promotion, err)
	}
	object, err := client.Get(t.Context(), "Promotion", "team-b", "promo-1")
	if err != nil || object.Phase() != "Errored" || !strings.Contains(string(object.Raw), "9007199254740993") {
		t.Fatalf("Connect raw object changed: %s %v", object.Raw, err)
	}
	if writes.Load() != 1 {
		t.Fatal("Connect inspection replayed promotion")
	}
}
