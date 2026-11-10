package kargo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func testConnection(t *testing.T, address, token string) Connection {
	t.Helper()
	connection := Connection{Name: "fixture", Home: t.TempDir(), Server: address}
	writeSession(t, connection, Credentials{APIAddress: address, BearerToken: token, AuthMethod: "sso"})
	return connection
}

func writeSession(t *testing.T, connection Connection, credentials Credentials) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(connection.ConfigPath()), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(credentials)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(connection.ConfigPath(), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRESTVersionsScopesAndPromotion(t *testing.T) {
	for _, version := range []string{"v1beta1", "v1alpha1"} {
		t.Run(version, func(t *testing.T) {
			var projectReads, writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("missing context token")
				}
				if !strings.HasPrefix(r.URL.Path, "/"+version+"/") {
					w.WriteHeader(404)
					fmt.Fprint(w, `{"message":"not found"}`)
					return
				}
				switch r.URL.Path {
				case "/" + version + "/projects":
					projectReads.Add(1)
					fmt.Fprint(w, `{"items":[{"metadata":{"name":"team-a"}},{"metadata":{"name":"team-b"}}]}`)
				case "/" + version + "/projects/team-b/freight":
					if r.URL.Query().Get("stage") != "prod" || r.URL.Query().Get("origins") != "registry" {
						t.Error("query lost stage / warehouse scope")
					}
					fmt.Fprint(w, `{"groups":{"":{"items":[{"metadata":{"name":"freight-abc","namespace":"team-b"},"origin":{"kind":"Warehouse","name":"registry"}}]}}}`)
				case "/" + version + "/projects/team-b/stages/prod/promotions":
					if r.Method != http.MethodPost {
						t.Error("expected POST")
					}
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["freight"] != "freight-abc" {
						t.Error("wrong freight in promotion request")
					}
					writes.Add(1)
					fmt.Fprint(w, `{"kind":"Promotion","metadata":{"name":"promo-1","namespace":"team-b"},"spec":{"stage":"prod","freight":"freight-abc"},"status":{"phase":"Pending"}}`)
				default:
					t.Errorf("unexpected request: %s", r.URL)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			client, err := NewClient(testConnection(t, server.URL, "fixture-token"))
			if err != nil {
				t.Fatal(err)
			}
			projects, err := client.List(t.Context(), "Project", "", "", "")
			if err != nil || len(projects) != 2 || projectReads.Load() != 1 {
				t.Fatalf("initial list downloaded twice or failed: %v %d %d", err, len(projects), projectReads.Load())
			}
			freight, err := client.List(t.Context(), "Freight", "team-b", "prod", "registry")
			if err != nil || len(freight) != 1 || freight[0].Project != "team-b" {
				t.Fatalf("wrong freight scope: %+v %v", freight, err)
			}
			promotion, err := client.Promote(t.Context(), "team-b", "prod", freight[0].Name)
			if err != nil || promotion.Project != "team-b" || promotion.Phase() != "Pending" || writes.Load() != 1 {
				t.Fatalf("promotion: %+v %v", promotion, err)
			}
			if _, err := client.Promote(t.Context(), "../team-a", "prod", "freight-abc"); err == nil || writes.Load() != 1 {
				t.Fatal("unsafe resource name reached API")
			}
		})
	}
}

func TestPromotionIsNeverRetriedAfterLostResponse(t *testing.T) {
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"items":[]}`)
			return
		}
		writes.Add(1)
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	}))
	defer server.Close()
	client, err := NewClient(testConnection(t, server.URL, "fixture-token"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Promote(t.Context(), "team-b", "prod", "freight-a")
	if err == nil || writes.Load() != 1 || !strings.Contains(err.Error(), "check Promotions") {
		t.Fatalf("write retried or uncertain result hidden: writes=%d err=%v", writes.Load(), err)
	}
}

func TestKargoRedirectCannotLeakToken(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprint(write), func(t *testing.T) {
			var received atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
			defer target.Close()
			source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			}))
			defer source.Close()
			connection := testConnection(t, source.URL, "fixture-secret")
			writeSession(t, connection, Credentials{APIAddress: source.URL, BearerToken: "fixture-secret", Insecure: true})
			client, err := NewClient(connection)
			if err != nil {
				t.Fatal(err)
			}
			method := http.MethodGet
			if write {
				method = http.MethodPost
			}
			_, err = client.request(t.Context(), method, "/redirect", map[string]string{"freight": "a"})
			if err == nil || received.Load() != 0 {
				t.Fatal("redirect leaked credentials or replayed write")
			}
		})
	}
}

func TestConnectionPinsCredentialsAndRedactsAPIErrors(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(403)
		fmt.Fprint(w, `{"message":"denied fixture-secret fixture-refresh"}`)
	}))
	defer server.Close()
	connection := testConnection(t, server.URL, "fixture-secret")
	writeSession(t, connection, Credentials{APIAddress: server.URL, BearerToken: "fixture-secret", RefreshToken: "fixture-refresh"})
	client, err := NewClient(connection)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.List(t.Context(), "Project", "", "", "")
	if err == nil || NeedsLogin(err) || strings.Contains(err.Error(), "fixture-secret") || strings.Contains(err.Error(), "fixture-refresh") || requests.Load() != 1 {
		t.Fatalf("RBAC error leaked tokens, retried or triggered login: %v", err)
	}
	oldIdentity := connection.SessionIdentity()
	writeSession(t, connection, Credentials{APIAddress: "https://another.example", BearerToken: "other-token"})
	if connection.SessionIdentity() != "" || oldIdentity == "" {
		t.Fatal("server change preserved session identity")
	}
	if _, err := client.List(t.Context(), "Stage", "team-b", "", ""); err == nil || requests.Load() != 1 {
		t.Fatal("client followed another connection's credentials")
	}
}

func TestSSOCommandIsolatedOnWindowsAndUnix(t *testing.T) {
	t.Setenv("KARGO_API_ADDRESS", "https://wrong.example")
	t.Setenv("KARGO_API_TOKEN", "do-not-inherit")
	t.Setenv("KUBECONFIG", "do-not-use")
	connection := testConnection(t, "https://kargo.example/prefix", "fixture-token")
	connection.Command, connection.CallbackPort = os.Args[0], 8085
	cmd, err := connection.LoginCommand(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cmd.Args[1:], " ") != "login https://kargo.example/prefix --sso --port 8085" {
		t.Fatalf("unsafe or incorrect SSO args: %v", cmd.Args)
	}
	for _, entry := range cmd.Env {
		upper := strings.ToUpper(entry)
		if strings.HasPrefix(upper, "KARGO_API_") || strings.HasPrefix(upper, "KUBECONFIG=") {
			t.Fatalf("cross-context env inherited: %s", strings.Split(entry, "=")[0])
		}
	}
	if !strings.Contains(strings.Join(cmd.Env, "\n"), "XDG_CONFIG_HOME="+connection.Home) {
		t.Fatal("CLI used the wrong credential store")
	}
	for _, server := range []string{"https://user:password@kargo.example", "https://kargo.example?token=x", "file:///tmp/config", "https://kargo.example/#token"} {
		if _, err := NormalizeServer(server); err == nil {
			t.Errorf("accepted unsafe URL %s", server)
		}
	}
}

func TestTokenExpiryAndPromotionSteps(t *testing.T) {
	now := time.Now()
	token := "unused." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, now.Add(-time.Minute).Unix()))) + ".unused"
	if !tokenNeedsRefresh(token, now) || tokenNeedsRefresh("opaque-token", now) {
		t.Fatal("expiry hint parsing")
	}
	resource, err := DecodeResource(json.RawMessage(`{"kind":"Promotion","metadata":{"name":"promo","namespace":"team-b"},"status":{"phase":"Errored","message":"git push denied","stepExecutionMetadata":[{"alias":"push","status":"Failed","message":"permission denied"}]}}`), "Promotion")
	if err != nil || resource.Phase() != "Errored" || resource.Message() != "git push denied" || resource.Value("status.stepExecutionMetadata") == nil {
		t.Fatalf("lost promotion failure: %+v %v", resource, err)
	}
}

func TestKargoClientRejectsCrossProjectResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1beta1/projects" {
			fmt.Fprint(w, `{"items":[]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/stages") {
			fmt.Fprint(w, `{"items":[{"kind":"Stage","metadata":{"name":"prod","namespace":"team-a"}}]}`)
			return
		}
		fmt.Fprint(w, `{"kind":"Stage","metadata":{"name":"prod","namespace":"team-a"}}`)
	}))
	defer server.Close()
	client, err := NewClient(testConnection(t, server.URL, "fixture-token"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.List(t.Context(), "Stage", "team-b", "", ""); err == nil {
		t.Fatal("list accepted a resource from another project")
	}
	if _, err := client.Get(t.Context(), "Stage", "team-b", "prod"); err == nil {
		t.Fatal("get accepted a resource from another project")
	}
}

func TestKargoCancellationReleasesSlowVPNRead(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1beta1/projects" {
			fmt.Fprint(w, `{"items":[]}`)
			return
		}
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	client, err := NewClient(testConnection(t, server.URL, "fixture-token"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := client.List(ctx, "Stage", "team-b", "", ""); result <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("read did not start")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled context left API request alive")
	}
	select {
	case err := <-result:
		if err == nil || NeedsLogin(err) {
			t.Fatalf("cancellation treated as success/auth failure: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled request did not return")
	}
}
