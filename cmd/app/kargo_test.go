package main

import (
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

	"charm.land/bubbles/v2/cursor"
	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/config"
	"github.com/darksworm/argonaut/pkg/kargo"
	"github.com/darksworm/argonaut/pkg/model"
	"gopkg.in/yaml.v3"
)

type kargoFixture struct {
	Promotions  atomic.Int32
	Reads       atomic.Int32
	Phase       atomic.Value
	LastProject atomic.Value
	Home        string
	Server      *httptest.Server
}

func newKargoFixture(t *testing.T) (*Model, *kargoFixture) {
	t.Helper()
	fixture := &kargoFixture{Home: t.TempDir()}
	fixture.Phase.Store("Running")
	fixture.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("Kargo API used another integration's credentials")
		}
		if r.Method != http.MethodPost {
			fixture.Reads.Add(1)
		}
		switch {
		case r.URL.Path == "/v1beta1/projects":
			fmt.Fprint(w, `{"items":[{"kind":"Project","metadata":{"name":"team-a"}},{"kind":"Project","metadata":{"name":"team-b"}}]}`)
		case strings.HasSuffix(r.URL.Path, "/stages"):
			project := strings.Split(r.URL.Path, "/")[3]
			fmt.Fprintf(w, `{"items":[{"kind":"Stage","metadata":{"name":"prod","namespace":%q},"status":{"health":{"status":"Healthy"},"freightSummary":"freight-old"}}]}`, project)
		case strings.HasSuffix(r.URL.Path, "/warehouses"):
			fmt.Fprint(w, `{"items":[{"kind":"Warehouse","metadata":{"name":"registry","namespace":"team-b"},"spec":{"subscriptions":[{"git":{"repoURL":"https://git.example/team/repo"}}]},"status":{"conditions":[{"type":"Ready","status":"False","message":"registry timeout"}],"lastFreightID":"freight-old"}}]}`)
		case strings.HasSuffix(r.URL.Path, "/freight"):
			fmt.Fprint(w, `{"groups":{"":{"items":[{"kind":"Freight","metadata":{"name":"freight-a","namespace":"team-b","labels":{"kargo.akuity.io/alias":"sleepy-owl"}},"origin":{"kind":"Warehouse","name":"registry"},"images":[{"repoURL":"registry.example/web","tag":"v2"}]}]}}}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stages/prod/promotions"):
			project := strings.Split(r.URL.Path, "/")[3]
			fixture.LastProject.Store(project)
			fixture.Promotions.Add(1)
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["freight"] != "freight-a" {
				t.Error("incorrect promotion payload")
			}
			fmt.Fprintf(w, `{"kind":"Promotion","metadata":{"name":"promo-1","namespace":%q},"spec":{"stage":"prod","freight":"freight-a"},"status":{"phase":"Running"}}`, project)
		case strings.HasSuffix(r.URL.Path, "/promotions/promo-1"):
			fmt.Fprintf(w, `{"kind":"Promotion","metadata":{"name":"promo-1","namespace":"team-b"},"spec":{"stage":"prod","freight":"freight-a"},"status":{"phase":%q,"currentStep":1,"message":"git push failed","stepExecutionMetadata":[{"alias":"push","status":"Failed","message":"permission denied by remote"}]}}`, fixture.Phase.Load())
		case strings.HasSuffix(r.URL.Path, "/promotions"):
			fmt.Fprint(w, `{"items":[]}`)
		default:
			t.Errorf("unexpected Kargo API request %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fixture.Server.Close)
	path := filepath.Join(fixture.Home, "kargo", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := yaml.Marshal(kargo.Credentials{APIAddress: fixture.Server.URL, BearerToken: "fixture-token", AuthMethod: "sso"})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARGONAUT_CONFIG", filepath.Join(t.TempDir(), "argonaut.toml"))
	m := buildDeleteTestModel(140, 35)
	m.config.Network = config.NetworkConfig{LowBandwidth: true, RefreshInterval: "0"}
	m.config.Kargo.Connections = map[string]config.KargoConnection{"cli": {Server: fixture.Server.URL, ConfigHome: fixture.Home}}
	m.state.Server = &model.Server{BaseURL: "https://argocd.invalid", Token: "different-argo-token"}
	_, cmd := m.openKargo()
	t.Cleanup(m.cancelKargoRequests)
	driveKargo(t, m, cmd)
	return m, fixture
}

func driveKargo(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil || (!m.kargo.Loading && !m.kargo.Executing) {
		return
	}
	msg := cmd()
	switch msg := msg.(type) {
	case kargoLoadedMsg:
		m.Update(msg)
	case kargoPromotedMsg:
		m.Update(msg)
	case tea.BatchMsg:
		for _, child := range msg {
			driveKargo(t, m, child)
		}
	case cursor.BlinkMsg:
		return
	default:
		t.Fatalf("unexpected command in fixture: %T", msg)
	}
}

func kargoKey(t *testing.T, m *Model, key string) {
	t.Helper()
	_, cmd := m.handleKeyMsg(testKeyMsg(key))
	driveKargo(t, m, cmd)
}

func selectKargoStage(t *testing.T, m *Model) {
	t.Helper()
	// Both projects contain a Stage named prod. Selecting team-b must remain
	// explicit through freight selection, confirmation and the final POST.
	m.kargo.Cursor = 1
	kargoKey(t, m, "enter")
	if m.kargo.Project != "team-b" || m.kargo.Kind != "Stage" {
		t.Fatal("project selection failed")
	}
}

func TestKargoPromotionRequiresConfirmationAndShowsErrors(t *testing.T) {
	m, fixture := newKargoFixture(t)
	if fixture.Reads.Load() != 1 {
		t.Fatal("startup downloaded project list twice")
	}
	selectKargoStage(t, m)
	kargoKey(t, m, "ctrl+p")
	if fixture.Promotions.Load() != 0 || m.kargo.Draft.Stage != "prod" || m.kargo.Stage != "prod" {
		t.Fatal("promotion created before picking freight")
	}
	kargoKey(t, m, "enter")
	if !m.kargo.Confirm || fixture.Promotions.Load() != 0 {
		t.Fatal("Enter should show confirmation only")
	}
	kargoKey(t, m, "enter")
	if fixture.Promotions.Load() != 0 {
		t.Fatal("repeated Enter bypassed Ctrl+P confirmation")
	}
	_, cmd := m.handleKeyMsg(testKeyMsg("ctrl+p"))
	if !m.kargo.Executing || cmd == nil {
		t.Fatal("confirmation did not queue exactly one request")
	}
	if _, duplicate := m.handleKeyMsg(testKeyMsg("ctrl+p")); duplicate != nil {
		t.Fatal("second key queued duplicate promotion")
	}
	driveKargo(t, m, cmd)
	if fixture.Promotions.Load() != 1 || fixture.LastProject.Load() != "team-b" || m.kargo.Detail == nil || m.kargo.Detail.Kind != "Promotion" {
		t.Fatal("promotion routed to another project or did not open its status")
	}
	fixture.Phase.Store("Errored")
	kargoKey(t, m, "ctrl+r")
	text := strings.Join(m.state.Diff.Content, "\n")
	for _, want := range []string{"Errored", "push", "permission denied by remote", "team-b"} {
		if !strings.Contains(text, want) {
			t.Fatalf("promotion details omit %q: %s", want, text)
		}
	}
	if fixture.Promotions.Load() != 1 {
		t.Fatal("status refresh replayed a write")
	}
	kargoKey(t, m, "y")
	if m.state.Diff.InspectionKind != "YAML" || !strings.Contains(strings.Join(m.state.Diff.Content, "\n"), "stepExecutionMetadata:") {
		t.Fatal("full promotion YAML is unavailable")
	}
}

func TestKargoCacheRefreshAndAuthenticationInvalidation(t *testing.T) {
	m, fixture := newKargoFixture(t)
	m.config.Network.RefreshInterval = "2m"
	selectKargoStage(t, m)
	reads := fixture.Reads.Load()
	kargoKey(t, m, "1")
	if fixture.Reads.Load() != reads {
		t.Fatal("project navigation ignored warm cache")
	}
	m.kargo.Cursor = 1
	kargoKey(t, m, "enter")
	if fixture.Reads.Load() != reads {
		t.Fatal("stage navigation ignored warm cache")
	}
	kargoKey(t, m, "ctrl+r")
	if fixture.Reads.Load() != reads+1 {
		t.Fatal("explicit refresh did not bypass cache")
	}
	kargoKey(t, m, "4")
	reads = fixture.Reads.Load()
	_, cmd := m.Update(kargoRefreshMsg{m.switchEpoch, m.kargo.Sequence, m.kargoTarget()})
	driveKargo(t, m, cmd)
	if fixture.Reads.Load() != reads+1 || m.kargo.Kind != "Warehouse" {
		t.Fatal("polling fetched unrelated views / projects")
	}
	connection := m.kargo.Connections[0]
	if err := os.WriteFile(connection.ConfigPath(), []byte("apiAddress: https://other.example\nbearerToken: other-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = m.loadKargoView(false)
	if len(m.kargo.Items) != 0 || len(m.kargo.Cache) != 0 {
		t.Fatal("old identity's cache survived a server change")
	}
	driveKargo(t, m, cmd)
	if m.kargo.Error == "" || fixture.Reads.Load() != reads+1 {
		t.Fatal("client followed the changed CLI server")
	}
}

func TestKargoSearchEnterNavigationAndCancelPromotion(t *testing.T) {
	m, fixture := newKargoFixture(t)
	kargoKey(t, m, "/")
	for _, key := range []string{"t", "e", "a", "m", "-", "b", "enter"} {
		kargoKey(t, m, key)
	}
	if m.state.Mode != model.ModeNormal || len(m.kargoRows()) != 1 {
		t.Fatal("search needs a second Enter")
	}
	kargoKey(t, m, "enter")
	if m.kargo.Project != "team-b" {
		t.Fatal("first key after search ignored or selected wrong project")
	}
	kargoKey(t, m, "ctrl+p")
	kargoKey(t, m, "enter")
	kargoKey(t, m, "esc")
	if m.kargo.Confirm || m.kargo.Draft != nil || fixture.Promotions.Load() != 0 {
		t.Fatal("cancelled promotion was sent")
	}
}

func TestKargoRepliesAndConfirmationsBindToExactTarget(t *testing.T) {
	m, fixture := newKargoFixture(t)
	selectKargoStage(t, m)
	_, cmd := m.handleKeyMsg(testKeyMsg("ctrl+p"))
	msg := cmd().(kargoLoadedMsg)
	m.cancelKargoLoad()
	m.Update(msg)
	if len(m.kargo.Items) != 0 {
		t.Fatal("cancelled request changed another view")
	}
	for _, variant := range []string{"epoch", "target"} {
		bad := msg
		bad.Sequence = m.kargo.Sequence
		if variant == "epoch" {
			bad.Epoch--
		} else {
			bad.Target.Project = "team-a"
		}
		m.Update(bad)
		if len(m.kargo.Items) != 0 {
			t.Fatalf("accepted stale %s", variant)
		}
	}
	driveKargo(t, m, m.loadKargoView(true))
	kargoKey(t, m, "enter")
	m.kargo.Project = "team-a"
	if m.executeKargoPromotion() != nil || fixture.Promotions.Load() != 0 || m.kargo.Confirm {
		t.Fatal("changed project retained an old confirmation")
	}
}

func TestKargoSSOPreferencesAndNoWriteReplay(t *testing.T) {
	m, fixture := newKargoFixture(t)
	connection := &m.kargo.Connections[0]
	connection.Command = os.Args[0] // Existing executable; never execute it in this test.
	if cmd := m.beginKargoLogin(); cmd == nil || !m.kargo.LoginBusy {
		t.Fatal("SSO was not scheduled")
	}
	m.kargo.Draft = &kargoPromotion{Connection: "cli", Project: "team-b", Stage: "prod", Freight: "freight-a"}
	_, reload := m.Update(kargoLoginMsg{m.switchEpoch, m.kargo.LoginSequence, "cli", nil})
	driveKargo(t, m, reload)
	if m.kargo.Draft != nil || fixture.Promotions.Load() != 0 {
		t.Fatal("SSO replayed a previous promotion")
	}
	if cmd := m.toggleKargoAutoLogin(); cmd != nil {
		t.Fatal("preference save failed")
	}
	stored, err := os.ReadFile(config.GetArgonautConfigPath())
	if err != nil || strings.Contains(string(stored), "fixture-token") || !m.kargo.Connections[0].AutoRelogin {
		t.Fatalf("unsafe auth preference save: %v", err)
	}
	m.kargo.LoginAttempts["cli"] = time.Now()
	if _, cmd := m.handleKargoLoaded(kargoLoadedMsg{Epoch: m.switchEpoch, Sequence: m.kargo.Sequence, Target: m.kargoTarget(), Error: &kargo.APIError{Status: 401, Message: "expired"}}); cmd != nil {
		t.Fatal("cancelled login caused a browser loop")
	}
}

func TestKargoLayoutsAndFullWarehouseDetails(t *testing.T) {
	m, _ := newKargoFixture(t)
	selectKargoStage(t, m)
	for _, size := range []struct{ cols, rows int }{{60, 20}, {80, 24}, {100, 30}, {120, 30}, {160, 40}} {
		m.state.Terminal.Cols, m.state.Terminal.Rows = size.cols, size.rows
		for _, key := range []string{"0", "1", "2", "3", "4", "5"} {
			kargoKey(t, m, key)
			text := m.renderKargoView()
			assertInspectionLayout(t, text, size.cols, size.rows)
			if size.cols == 120 {
				t.Logf("\nKargo %s\n%s", m.kargo.Kind, stripANSI(text))
			}
		}
		kargoKey(t, m, "4")
		kargoKey(t, m, "d")
		text := strings.Join(m.state.Diff.Content, "\n")
		if !strings.Contains(text, "registry timeout") || !strings.Contains(text, "https://git.example/team/repo") {
			t.Fatal("warehouse source/error missing")
		}
		assertInspectionLayout(t, m.renderKargoView(), size.cols, size.rows)
		kargoKey(t, m, "esc")
		kargoKey(t, m, "2")
		kargoKey(t, m, "ctrl+p")
		kargoKey(t, m, "enter")
		m.kargo.Draft.Server += "/" + strings.Repeat("long-server-prefix/", 12)
		assertInspectionLayout(t, m.renderKargoView(), size.cols, size.rows)
		kargoKey(t, m, "j")
		assertInspectionLayout(t, m.renderKargoView(), size.cols, size.rows)
		kargoKey(t, m, "esc")
	}
}

func TestKargoInstancesWithSameProjectAndStageStayIsolated(t *testing.T) {
	m, first := newKargoFixture(t)
	_, second := newKargoFixture(t)
	m.kargo.Connections = append(m.kargo.Connections, kargo.Connection{Name: "second", Server: second.Server.URL, Home: second.Home, Command: os.Args[0]})
	old := m.loadKargoView(true)().(kargoLoadedMsg)
	m.selectKargoConnection(1)
	m.setKargoView("Project")
	driveKargo(t, m, m.loadKargoView(true))
	m.Update(old)
	if m.kargo.Client.Connection.Server != second.Server.URL {
		t.Fatal("late reply restored another instance's client")
	}
	selectKargoStage(t, m)
	kargoKey(t, m, "ctrl+p")
	kargoKey(t, m, "enter")
	kargoKey(t, m, "ctrl+p")
	if first.Promotions.Load() != 0 || second.Promotions.Load() != 1 {
		t.Fatal("promotion targeted another instance's same-named Stage")
	}
}

func TestKargoReadCannotInstallAnotherSessionCache(t *testing.T) {
	m, fixture := newKargoFixture(t)
	old := m.loadKargoView(true)().(kargoLoadedMsg)
	path := m.kargo.Connections[0].ConfigPath()
	if err := os.WriteFile(path, []byte("apiAddress: "+fixture.Server.URL+"\nbearerToken: new-user-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, reload := m.Update(old)
	if reload == nil || len(m.kargo.Items) != 0 || len(m.kargo.Cache) != 0 {
		t.Fatal("reply from previous credentials was installed under another session")
	}
}
