package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/darksworm/argonaut/pkg/kargo"
	"github.com/darksworm/argonaut/pkg/model"
	"github.com/darksworm/argonaut/pkg/theme"
)

func promotionInfoFixture(t *testing.T) kargo.Resource {
	t.Helper()
	resource, err := kargo.DecodeResource([]byte(`{
		"kind":"Promotion",
		"metadata":{"name":"production-web-services-release-2026-11-10-abcdefghijklmnop","namespace":"team-b","creationTimestamp":"2026-11-10T12:01:00Z"},
		"spec":{"stage":"prod","freight":"freight-a"},
		"status":{"phase":"Failed","startedAt":"2026-11-10T12:02:00Z","finishedAt":"2026-11-10T12:04:03Z","message":"git push failed",
		"freightCollection":{"freight":{
			"Warehouse/registry":{"name":"freight-a","commits":[{"repoURL":"https://user:secret@git.example/team/web.git?token=secret","id":"abcdef1234567890abcdef1234567890abcdef12","tag":"v2.1"}],"images":[{"repoURL":"registry.example/web","tag":"2.1.0"}],"charts":[{"name":"web-chart","version":"3.2.0"}]},
			"Warehouse/other":{"name":"old-freight","commits":[{"repoURL":"https://git.example/other.git","id":"wrong-revision"}]}
		}}}
	}`), "Promotion")
	if err != nil {
		t.Fatal(err)
	}
	return resource
}

func TestKargoPromotionRevisionUsesTargetFreightArtifacts(t *testing.T) {
	r := promotionInfoFixture(t)
	want := "web@v2.1 (abcdef123456) | web:2.1.0 | web-chart@3.2.0"
	if got := kargoPromotionRevision(r); got != want {
		t.Fatalf("artifact revision = %q, want %q", got, want)
	}
	status := r.Value("status").(map[string]any)
	collection := r.Value("status.freightCollection.freight").(map[string]any)
	status["freight"] = collection["Warehouse/registry"]
	// Legacy API responses embed a single FreightReference instead.
	delete(status, "freightCollection")
	if got := kargoPromotionRevision(r); got != want {
		t.Fatalf("legacy artifact revision = %q", got)
	}
	status["freight"] = collection["Warehouse/other"]
	if got := kargoPromotionRevision(r); got != "-" {
		t.Fatalf("carried-over Freight must not become the target revision: %q", got)
	}
	delete(status, "freight")
	if got := kargoPromotionRevision(r); got != "-" {
		t.Fatalf("missing artifacts must not turn a Freight ID into a Git revision: %q", got)
	}
}

func TestKargoPromotionDatesAndDuration(t *testing.T) {
	r := promotionInfoFixture(t)
	created := time.Date(2026, 11, 10, 12, 1, 0, 0, time.UTC)
	if got := kargoPromotionCreated(r); got != created.Local().Format("2006-01-02 15:04") {
		t.Fatalf("created = %q", got)
	}
	if got := kargoTimestamp(r, "metadata.creationTimestamp"); got != created.Local().Format("2006-01-02 15:04:05 MST") {
		t.Fatalf("full local timestamp = %q", got)
	}
	now := created.Add(6 * time.Minute)
	if got := kargoPromotionDuration(r, now); got != "2m3s" {
		t.Fatalf("finished duration = %q", got)
	}
	status := r.Value("status").(map[string]any)
	delete(status, "finishedAt")
	if got := kargoPromotionDuration(r, now); got != "-" {
		t.Fatalf("must not invent an end time for a failed Promotion: %q", got)
	}
	status["phase"] = "Running"
	if got := kargoPromotionDuration(r, now); got != "5m0s" {
		t.Fatalf("running duration = %q", got)
	}
	status["startedAt"] = "0001-01-01T00:00:00Z"
	if got := kargoPromotionDuration(r, now); got != "-" {
		t.Fatalf("zero start time = %q", got)
	}
	delete(r.Object, "metadata")
	if kargoPromotionCreated(r) != "-" || kargoTimestamp(r, "metadata.creationTimestamp") != "-" {
		t.Fatal("missing timestamps must remain unknown")
	}
}

func TestKargoPromotionMetadataAndColorsRenderWithoutNetworkReads(t *testing.T) {
	applyTheme(theme.Default())
	t.Cleanup(func() { applyTheme(theme.Default()) })
	m, fixture := newKargoFixture(t)
	r := promotionInfoFixture(t)
	m.kargo.Kind, m.kargo.Project, m.kargo.Stage = "Promotion", "team-b", "prod"
	m.kargo.Items = []kargo.Resource{r}
	m.now = func() time.Time { return time.Date(2026, 11, 10, 13, 0, 0, 0, time.UTC) }
	before := fixture.Reads.Load()
	for _, cols := range []int{40, 60, 80, 100, 120, 160, 220, 300} {
		m.state.Terminal.Cols, m.state.Terminal.Rows = cols, 35
		out := m.renderKargoView()
		assertInspectionLayout(t, out, cols, 35)
		plain := stripANSI(out)
		if cols >= 120 && (!strings.Contains(plain, r.Name) || !strings.Contains(plain, "abcdef123456") || !strings.Contains(plain, "2m3s")) {
			t.Fatalf("name, revision or duration missing at %d columns:\n%s", cols, plain)
		}
		if cols >= 220 {
			// Check the table row itself, not only the selected-resource preview.
			var tableRow string
			for _, line := range strings.Split(plain, "\n") {
				if strings.Contains(line, "Failed") {
					tableRow = line
					break
				}
			}
			if !strings.Contains(tableRow, r.Name) || !strings.Contains(tableRow, "abcdef123456") {
				t.Fatalf("spare space must be assigned to name and revision: %s", tableRow)
			}
		}
		if cols >= 80 && !strings.Contains(plain, "2026-11-10") {
			t.Fatalf("date missing at %d columns", cols)
		}
		if cols == 120 || cols == 220 {
			t.Logf("\nPromotion %d columns\n%s", cols, plain)
		}
	}
	if got := renderHotkey(viewHotkey{"ctrl-t", "retry"}); !strings.Contains(got, lipgloss.NewStyle().Foreground(progressColor).Render("retry")) || !strings.Contains(got, "\x1b[") {
		t.Fatalf("retry label lost its semantic color: %q", got)
	}
	if got := renderHotkey(viewHotkey{"ctrl-p", "promote"}); !strings.Contains(got, lipgloss.NewStyle().Foreground(syncedColor).Render("promote")) {
		t.Fatalf("promote label lost its success color: %q", got)
	}
	kargoKey(t, m, "ctrl+t")
	if out := m.renderKargoView(); !strings.Contains(out, lipgloss.NewStyle().Foreground(progressColor).Bold(true).Render("RETRY PROMOTION")) {
		t.Fatal("retry confirmation title lost its warning color")
	}
	kargoKey(t, m, "esc")
	m.showKargoDocument(r, "Describe", false)
	plain := strings.Join(m.state.Diff.Content, "\n")
	for _, want := range []string{"Revision: web@v2.1 (abcdef123456)", "Created:", "Started:", "Finished:", "Duration: 2m3s"} {
		if !strings.Contains(stripANSI(plain), want) {
			t.Fatalf("Describe lacks %q", want)
		}
	}
	m.state.Mode, m.state.Diff = model.ModeNormal, nil
	m.kargo.Filter = "abcdef123456"
	if len(m.kargoRows()) != 1 {
		t.Fatal("revision must be searchable")
	}
	if fixture.Reads.Load() != before || fixture.Promotions.Load() != 0 {
		t.Fatal("render/describe/filter must use loaded metadata without extra requests or writes")
	}
	m.state.Diff, m.kargo.Detail, m.kargo.Filter = nil, nil, ""
	m.kargo.Draft = &kargoPromotion{Connection: "cli", Server: fixture.Server.URL, Project: "team-b", Stage: "prod", Freight: "freight-a"}
	m.kargo.Confirm = true
	if out := m.renderKargoView(); !strings.Contains(out, lipgloss.NewStyle().Foreground(syncedColor).Bold(true).Render("PROMOTE FREIGHT")) {
		t.Fatal("promotion confirmation title lost its success color")
	}
	// The renderer must not mutate resource data or lose the full SHA in YAML.
	raw, err := json.Marshal(r.Object)
	if err != nil || !strings.Contains(string(raw), "abcdef1234567890abcdef1234567890abcdef12") {
		t.Fatal("display shortening changed the source resource")
	}
}
