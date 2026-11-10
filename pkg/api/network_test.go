package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/darksworm/argonaut/pkg/model"
)

func TestClientsReuseConnectionsWithoutSharingBearerTokens(t *testing.T) {
	SetHTTPClient(nil)
	t.Cleanup(func() { SetHTTPClient(nil) })
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Header.Get("Authorization"))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	for _, token := range []string{"dev-token", "prod-token", "renewed-token"} {
		client := NewClient(&model.Server{BaseURL: server.URL, Token: token})
		body, err := client.Get(t.Context(), "/apps")
		if err != nil || string(body) != "Bearer "+token {
			t.Fatalf("credentials leaked between clients: %q %v", body, err)
		}
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("3 API clients opened %d TCP connections, want 1", got)
	}
}

func TestPooledTransportKeepsTLSVerificationIsolated(t *testing.T) {
	SetHTTPClient(nil)
	t.Cleanup(func() { SetHTTPClient(nil) })
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer server.Close()
	insecure := NewClient(&model.Server{BaseURL: server.URL, Insecure: true})
	if _, err := insecure.request(t.Context(), http.MethodGet, "/", nil); err != nil {
		t.Fatal(err)
	}
	verified := NewClient(&model.Server{BaseURL: server.URL})
	if _, err := verified.request(t.Context(), http.MethodGet, "/", nil); err == nil {
		t.Fatal("insecure context contaminated the verified TLS pool")
	}
}

func TestRESTUsesGzipAndHTTP2WithCustomTrust(t *testing.T) {
	// A representative highly repetitive application response. The server sends
	// actual gzip bytes so this checks transfer size, not just configuration.
	payload := strings.Repeat(`{"metadata":{"name":"web","namespace":"argocd"}},`, 2000)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := io.WriteString(writer, payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var wireBytes atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("expected HTTP/2 with custom trust, got %s", r.Proto)
		}
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			t.Error("compression not negotiated")
		}
		w.Header().Set("Content-Encoding", "gzip")
		n, _ := w.Write(compressed.Bytes())
		wireBytes.Add(int64(n))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	SetHTTPClient(server.Client())
	t.Cleanup(func() { SetHTTPClient(nil) })
	client := NewClient(&model.Server{BaseURL: server.URL})
	data, err := client.Get(context.Background(), "/applications")
	if err != nil || string(data) != payload {
		t.Fatalf("compressed response changed: %v", err)
	}
	if wireBytes.Load() >= int64(len(payload)/10) {
		t.Fatalf("gzip transferred %d / %d bytes", wireBytes.Load(), len(payload))
	}
	t.Logf("gzip fixture transferred %d bytes instead of %d (%.1f%% less)", wireBytes.Load(), len(payload), 100*(1-float64(wireBytes.Load())/float64(len(payload))))
}

func TestResourceStatusesRequestKeepsExactApplicationNamespace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fields") != "status.resources" {
			t.Errorf("request downloads unnecessary fields: %s", r.URL)
		}
		if r.URL.Query().Get("appNamespace") != "team-b" {
			t.Error("lost namespace of same-named Application")
		}
		fmt.Fprint(w, `{"status":{"resources":[{"kind":"Deployment","name":"web","namespace":"team-b","status":"Synced"}]}}`)
	}))
	defer server.Close()
	resources, err := NewApplicationService(&model.Server{BaseURL: server.URL}).GetApplicationResourceStatuses(t.Context(), "web", "team-b")
	if err != nil || len(resources) != 1 || resources[0].Namespace != "team-b" {
		t.Fatalf("resource statuses: %+v %v", resources, err)
	}
}
