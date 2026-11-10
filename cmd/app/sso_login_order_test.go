package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/config"
	"github.com/darksworm/argonaut/pkg/model"
	"gopkg.in/yaml.v3"
)

func testArgoJWT(expiry time.Time) string {
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"iss":"fixture-sso","exp":%d}`, expiry.Unix()))) + ".fixture-signature"
}

func TestExpiredTokenStartsSSOBeforeAnyApplicationRequest(t *testing.T) {
	m := authTestModel(t)
	m.config.Network.LowBandwidth, m.config.Network.RefreshInterval = true, "0"
	var reads, rejected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Header.Get("Authorization") != "Bearer renewed-test-token" {
			rejected.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/session/userinfo":
			fmt.Fprint(w, `{"loggedIn":true,"username":"fixture"}`)
		case "/api/version":
			fmt.Fprint(w, `{"Version":"v2.12.3"}`)
		default:
			fmt.Fprint(w, `{"items":[]}`)
		}
	}))
	t.Cleanup(server.Close)
	expired := testArgoJWT(time.Now().Add(-time.Hour))
	raw := strings.ReplaceAll(pickerCLIConfig, "prod.example.test", server.URL)
	raw = strings.ReplaceAll(raw, "prod-test-token", expired)
	raw = strings.ReplaceAll(raw, "    grpc-web-root-path: /argo\n", "")
	if err := os.WriteFile(m.argoConfigPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	m.state.Server = &model.Server{BaseURL: server.URL, Token: expired}
	waitDir := t.TempDir()
	t.Setenv("ARGONAUT_TEST_CLI_WAIT_DIR", waitDir)
	t.Setenv("ARGONAUT_TEST_CLI_USERINFO", `{"loggedIn":true}`)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	p := tea.NewProgram(&responsiveSSOModel{Model: m}, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	defer func() { p.Kill(); <-done }()
	for {
		if _, err := os.Stat(filepath.Join(waitDir, "started")); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("full SSO did not start for an expired JWT")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for range 3 {
		sendStartupKey(t, p, "enter")
		if got := probeStartup(t, p); !got.Busy || got.Mode != model.ModeNormal || got.View != model.ViewContexts {
			t.Fatalf("Enter interrupted SSO or hid the context picker: %+v", got)
		}
	}
	if reads.Load() != 0 {
		t.Fatal("application API was contacted before SSO completed")
	}
	if err := os.WriteFile(filepath.Join(waitDir, "continue"), []byte("complete SSO"), 0600); err != nil {
		t.Fatal(err)
	}
	for {
		if got := probeStartup(t, p); !got.Busy && got.Renewed {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("new CLI credentials were not loaded after SSO")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The full CLI login also switches current-context. Its restoration must
	// preserve the other context and all its credentials.
	cfg, err := config.ReadCLIConfigFromPath(m.argoConfigPath)
	if err != nil || cfg.CurrentContext != "dev" {
		t.Fatal("SSO changed the CLI's global context")
	}
	if dev, err := cfg.ToServerConfigForContext("dev"); err != nil || dev.Token != "dev-test-token" {
		t.Fatal("SSO changed another context's credentials")
	}
	sendStartupKey(t, p, "enter")
	for reads.Load() == 0 && ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	if reads.Load() == 0 || rejected.Load() != 0 {
		t.Fatalf("API must start only with the new token: reads=%d rejected=%d", reads.Load(), rejected.Load())
	}
}

func TestLegacyCLIProbeFallsBackToFullContextBoundSSO(t *testing.T) {
	m := authTestModel(t)
	t.Setenv("ARGONAUT_TEST_CLI_LEGACY", "1")
	cmd, handled := m.maybeAutoRelogin()
	if !handled {
		t.Fatal("auto SSO was not started")
	}
	msg := cmd().(contextReloginMsg)
	if !msg.Interactive || msg.Error != nil {
		t.Fatal("unsupported probe flag must fall back to SSO")
	}
	_, login := m.Update(msg)
	result := login().(contextReloginMsg)
	if result.Error != nil || result.Server == nil || result.Server.Token != "renewed-test-token" {
		t.Fatalf("full SSO must work without --argocd-context: %+v", result)
	}
	m.Update(result)
	t.Cleanup(m.cancelContextRelogin)
}

func TestRealArgoCDCLIFullSSOReachesSettingsWithoutExpiredAuthorization(t *testing.T) {
	executable := os.Getenv("ARGONAUT_REAL_ARGOCD")
	if executable == "" {
		t.Skip("set ARGONAUT_REAL_ARGOCD to verify installed CLI against a local HTTP/2 fixture")
	}
	m := authTestModel(t)
	t.Setenv("ARGONAUT_TEST_ARGOCD_CLI", "")
	var settings atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("real CLI request: %s", r.URL.Path)
		if r.Header.Get("Authorization") != "" {
			t.Error("full SSO sent an expired or unrelated credential")
		}
		if r.URL.Path == "/cluster.SettingsService/Get" {
			settings.Add(1)
		}
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status, Grpc-Message")
		w.WriteHeader(http.StatusOK)
		if strings.HasSuffix(r.URL.Path, "/Version") {
			// VersionMessage.Version is protobuf string field 1. The CLI
			// checks it before making the SSO Settings request.
			payload := append([]byte{0x0a, byte(len("v2.12.3"))}, []byte("v2.12.3")...)
			_, _ = w.Write(append([]byte{0, 0, 0, 0, byte(len(payload))}, payload...))
			w.Header().Set("Grpc-Status", "0")
			return
		}
		w.Header().Set("Grpc-Status", "7")
		w.Header().Set("Grpc-Message", "fixture-stops-before-external-idp")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	cfg, err := config.ReadCLIConfigFromPath(m.argoConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cfg.Contexts {
		if cfg.Contexts[i].Name == "prod" {
			cfg.Contexts[i].Server = strings.TrimPrefix(server.URL, "https://")
		}
	}
	for i := range cfg.Servers {
		if cfg.Servers[i].Server == "prod.example.test" {
			cfg.Servers[i].Server = strings.TrimPrefix(server.URL, "https://")
			cfg.Servers[i].GrpcWebRootPath = ""
		}
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil || os.WriteFile(m.argoConfigPath, raw, 0600) != nil {
		t.Fatal("writing local fixture config")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd, err := argoCDCommand(ctx, executable, m.argoConfigPath, "prod", "login")
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.CombinedOutput()
	// The fixture deliberately stops before any browser/IdP access. Reaching
	// Settings proves this exact real CLI accepted all production arguments.
	if err == nil || settings.Load() != 1 || strings.Contains(string(output), "unknown flag") {
		t.Fatalf("real CLI failed before reaching SSO settings: %v %s (settings=%d)", err, output, settings.Load())
	}
}
