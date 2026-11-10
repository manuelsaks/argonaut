package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/darksworm/argonaut/pkg/model"
)

func TestAuditRedirectDowngradeForwardsBearer(t *testing.T) {
	var auth string
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`{}`))
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/capture", http.StatusFound)
	}))
	defer secure.Close()
	SetHTTPClient(secure.Client())
	defer SetHTTPClient(nil)
	client := NewClient(&model.Server{BaseURL: secure.URL, Token: "audit-dummy-token"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.Get(ctx, "/api/v1/session/userinfo"); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer audit-dummy-token" {
		t.Fatal("behavior changed: bearer not forwarded on downgrade")
	}
	t.Log("CONFIRMED: HTTPS redirect to HTTP on the same hostname forwarded the bearer token")
}

type auditRoundTripper func(*http.Request) (*http.Response, error)

func (fn auditRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestAuditUnrelatedHostRedirectStripsBearer(t *testing.T) {
	calls := 0
	transport := auditRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		header := make(http.Header)
		if calls == 1 {
			header.Set("Location", "https://unrelated.example/audit")
			return &http.Response{StatusCode: 302, Header: header, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatal("bearer forwarded to unrelated host")
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})
	SetHTTPClient(&http.Client{Transport: transport})
	defer SetHTTPClient(nil)
	client := NewClient(&model.Server{BaseURL: "https://argocd.example", Token: "audit-dummy"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.Get(ctx, "/api/v1/session/userinfo"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expected two synthetic requests, got %d", calls)
	}
	t.Log("PASS: default redirect behavior strips bearer for an unrelated hostname")
}

func TestAuditResponseBodyHasNoApplicationSizeLimit(t *testing.T) {
	const bodySize = 4 * 1024 * 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(w, io.LimitReader(auditByteReader{}, bodySize)) }))
	defer srv.Close()
	client := NewClient(&model.Server{BaseURL: srv.URL, Token: "audit-dummy"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	body, err := client.Get(ctx, "/audit")
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != bodySize {
		t.Fatalf("expected %d bytes, got %d", bodySize, len(body))
	}
	t.Log("OBSERVED: ordinary API response accepts 4 MiB; source uses unbounded io.ReadAll")
}

type auditByteReader struct{}

func (auditByteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}
