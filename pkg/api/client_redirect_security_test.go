package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/darksworm/argonaut/pkg/model"
)

func TestAuthenticatedRedirectsStayOnOrigin(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tlsTarget := range []bool{false, true} {
			t.Run(map[bool]string{false: "request", true: "stream"}[stream]+map[bool]string{false: "/downgrade", true: "/different-port"}[tlsTarget], func(t *testing.T) {
				var leaked atomic.Int32
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					leaked.Add(1)
					w.WriteHeader(http.StatusOK)
				})
				var target *httptest.Server
				if tlsTarget {
					target = httptest.NewTLSServer(handler)
				} else {
					target = httptest.NewServer(handler)
				}
				defer target.Close()
				source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
				}))
				defer source.Close()
				client := NewClient(&model.Server{BaseURL: source.URL, Token: "dummy-secret", Insecure: true})
				var err error
				if stream {
					_, err = client.Stream(context.Background(), "/watch")
				} else {
					_, err = client.request(context.Background(), http.MethodPost, "/sync", map[string]string{"name": "demo"})
				}
				if err == nil {
					t.Fatal("unsafe redirect succeeded")
				}
				if leaked.Load() != 0 {
					t.Fatal("redirect target received a request")
				}
			})
		}
	}
}

func TestAuthenticatedSameOriginRedirect(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "request", true: "stream"}[stream], func(t *testing.T) {
			var received atomic.Bool
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "/end", http.StatusFound)
					return
				}
				received.Store(r.Header.Get("Authorization") == "Bearer dummy-secret")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			client := NewClient(&model.Server{BaseURL: srv.URL, Token: "dummy-secret", Insecure: true})
			if stream {
				response, err := client.Stream(context.Background(), "/start")
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
			} else if _, err := client.request(context.Background(), http.MethodGet, "/start", nil); err != nil {
				t.Fatal(err)
			}
			if !received.Load() {
				t.Fatal("same-origin redirect lost authentication")
			}
		})
	}
}

func TestRedirectPolicyRejectsOtherHostsAndBoundsLoops(t *testing.T) {
	origin, _ := http.NewRequest(http.MethodGet, "https://argocd.example/start", nil)
	policy := sameOriginRedirectPolicy(nil)
	for _, target := range []string{"http://argocd.example/end", "https://child.argocd.example/end", "https://argocd.example:8443/end", "https://unrelated.example/end"} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		if policy(req, []*http.Request{origin}) == nil {
			t.Errorf("allowed %s", target)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "https://ARGOCD.example:443/end", nil)
	if err := policy(req, []*http.Request{origin}); err != nil {
		t.Fatal(err)
	}
	if policy(req, make([]*http.Request, 0)) == nil {
		t.Fatal("missing origin allowed")
	}
	if policy(req, []*http.Request{origin, origin, origin, origin, origin, origin, origin, origin, origin, origin}) == nil {
		t.Fatal("redirect loop allowed")
	}
	denied := errors.New("custom policy")
	if !errors.Is(sameOriginRedirectPolicy(func(*http.Request, []*http.Request) error { return denied })(req, []*http.Request{origin}), denied) {
		t.Fatal("custom policy bypassed")
	}
}
