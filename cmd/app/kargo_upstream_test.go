package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/darksworm/argonaut/pkg/kargo"
)

func upstreamStages(current string) string {
	return fmt.Sprintf(`{"items":[
	{"kind":"Stage","metadata":{"name":"prod","namespace":"team-b"},"spec":{"requestedFreight":[{"origin":{"kind":"Warehouse","name":"registry"},"sources":{"stages":["test"]}}]}},
	{"kind":"Stage","metadata":{"name":"test","namespace":"team-b"},"status":{"freightHistory":[{"items":{"Warehouse/registry":{"name":%q,"origin":{"kind":"Warehouse","name":"registry"}},"Warehouse/database":{"name":"wrong-origin","origin":{"kind":"Warehouse","name":"database"}}}},{"items":{"Warehouse/registry":{"name":"freight-old","origin":{"kind":"Warehouse","name":"registry"}}}}]}}
	]}`, current)
}

func eligibleUpstreamFreights() string {
	return `{"items":[{"kind":"Freight","metadata":{"name":"freight-a","namespace":"team-b"},"alias":"sleepy-owl","origin":{"kind":"Warehouse","name":"registry"}},
	{"kind":"Freight","metadata":{"name":"freight-old","namespace":"team-b"},"origin":{"kind":"Warehouse","name":"registry"}},
	{"kind":"Freight","metadata":{"name":"wrong-origin","namespace":"team-b"},"origin":{"kind":"Warehouse","name":"database"}}]}`
}

func TestKargoUpstreamPopupOnlyOffersCurrentEligibleFreight(t *testing.T) {
	m, fixture := newKargoFixture(t)
	fixture.Stages.Store(upstreamStages("freight-a"))
	fixture.Freights.Store(eligibleUpstreamFreights())
	selectKargoStage(t, m)
	before := fixture.Reads.Load()
	kargoKey(t, m, "ctrl+u")
	if !m.kargo.Draft.FromUpstream || m.kargo.Draft.Stage != "prod" || m.kargo.Draft.Freight != "freight-a" || len(m.kargo.Form.Choices) != 1 || m.kargo.Form.Sources["freight-a"] != "test" {
		t.Fatal("upstream must use current Freight of the configured Stage and requested Warehouse")
	}
	if fixture.Reads.Load() != before+2 || fixture.Promotions.Load() != 0 {
		t.Fatal("upstream form fanned out reads or submitted before confirmation")
	}
	modal := m.renderKargoPromotionPopup()
	if lipgloss.Width(modal) >= m.state.Terminal.Cols-2 || !strings.Contains(stripANSI(modal), "Upstream: test") || !strings.Contains(stripANSI(modal), "Promote from upstream") {
		t.Fatal("upstream source or mode missing from bounded popup")
	}
	base := stripANSI(m.kargoFormBackdrop())
	if !strings.Contains(base, "STAGE") || !strings.Contains(base, "prod") || strings.Contains(base, "PROMOTE FREIGHT") {
		t.Fatal("popup replaced originating Stage list")
	}
	for _, size := range [][2]int{{60, 20}, {80, 24}, {120, 30}} {
		m.state.Terminal.Cols, m.state.Terminal.Rows = size[0], size[1]
		out := m.renderKargoView()
		assertInspectionLayout(t, out, size[0], size[1])
		if !strings.Contains(stripANSI(out), "Cancel") || !strings.Contains(stripANSI(out), "Promote from upstream") {
			t.Fatal("upstream popup buttons disappeared")
		}
		t.Logf("Upstream popup %dx%d:\n%s", size[0], size[1], stripANSI(out))
	}
	kargoKey(t, m, "tab") // Cancel
	kargoKey(t, m, "tab") // Promote from upstream
	_, cmd := m.handleKeyMsg(testKeyMsg("enter"))
	if !m.kargo.Executing || cmd == nil {
		t.Fatal("popup did not start explicit promotion")
	}
	if _, duplicate := m.handleKeyMsg(testKeyMsg("enter")); duplicate != nil {
		t.Fatal("duplicate submission was scheduled")
	}
	driveKargo(t, m, cmd)
	if fixture.Promotions.Load() != 1 || fixture.LastProject.Load() != "team-b" {
		t.Fatal("upstream promotion used another project or was not submitted once")
	}
}

func TestKargoPromotionModeSwitchAndStaleReply(t *testing.T) {
	m, fixture := newKargoFixture(t)
	fixture.Stages.Store(upstreamStages("freight-a"))
	fixture.Freights.Store(eligibleUpstreamFreights())
	selectKargoStage(t, m)
	_, cmd := m.handleKeyMsg(testKeyMsg("ctrl+p"))
	stale := cmd().(kargoFormLoadedMsg)
	kargoKey(t, m, "shift+tab") // mode
	kargoKey(t, m, "right")     // upstream
	if !m.kargo.Draft.FromUpstream || len(m.kargo.Form.Choices) != 1 {
		t.Fatal("mode field did not switch to upstream")
	}
	m.Update(stale)
	if len(m.kargo.Form.Choices) != 1 {
		t.Fatal("old normal-mode response replaced upstream choices")
	}
	before := fixture.Reads.Load()
	kargoKey(t, m, "left") // normal, reuses unfiltered eligible Freight cache
	if m.kargo.Draft.FromUpstream || len(m.kargo.Form.Choices) != 3 || fixture.Reads.Load() != before {
		t.Fatal("switching back lost normal choices or bypassed warm cache")
	}
	kargoKey(t, m, "esc")
	if m.kargo.Form != nil || m.kargoRows()[m.kargo.Cursor].Resource.Name != "prod" || fixture.Promotions.Load() != 0 {
		t.Fatal("popup cancel changed selection or created a Promotion")
	}
}

func TestKargoUpstreamRejectsChangedOrMissingFreight(t *testing.T) {
	for _, change := range []string{"no-upstream", "not-eligible", "changed-before-confirm"} {
		t.Run(change, func(t *testing.T) {
			m, fixture := newKargoFixture(t)
			fixture.Stages.Store(upstreamStages("freight-a"))
			fixture.Freights.Store(eligibleUpstreamFreights())
			if change == "no-upstream" {
				fixture.Stages.Store(upstreamStages(""))
			}
			if change == "not-eligible" {
				fixture.Freights.Store(`{"items":[]}`)
			}
			selectKargoStage(t, m)
			kargoKey(t, m, "ctrl+u")
			if change == "changed-before-confirm" {
				fixture.Stages.Store(upstreamStages("freight-other"))
			}
			kargoKey(t, m, "ctrl+p")
			if fixture.Promotions.Load() != 0 {
				t.Fatal("missing, ineligible or changed upstream Freight was promoted")
			}
			if change == "changed-before-confirm" && !strings.Contains(m.kargo.Error, "upstream Freight changed") {
				t.Fatal("changed upstream did not request a new review")
			}
		})
	}
}

func TestKargoUpstreamNeverUsesHistoryOrAnotherProject(t *testing.T) {
	var envelope struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(upstreamStages("freight-a")), &envelope); err != nil {
		t.Fatal(err)
	}
	var stages []kargo.Resource
	for _, raw := range envelope.Items {
		resource, err := kargo.DecodeResource(raw, "Stage")
		if err != nil {
			t.Fatal(err)
		}
		stages = append(stages, resource)
	}
	if sources := kargoUpstreamFreight(stages, "team-b", "prod"); len(sources) != 1 || sources["freight-a"] != "test" {
		t.Fatal("history or other Warehouse leaked into choices")
	}
	stages[1].Project = "another-project"
	if sources := kargoUpstreamFreight(stages, "team-b", "prod"); len(sources) != 0 {
		t.Fatal("upstream resolved outside project")
	}
}
