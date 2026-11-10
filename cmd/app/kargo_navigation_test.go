package main

import (
	"strings"
	"testing"

	"github.com/darksworm/argonaut/pkg/model"
	"github.com/darksworm/argonaut/pkg/tui/selection"
)

func TestKargoBackRestoresEveryEntryPoint(t *testing.T) {
	m, _ := newKargoFixture(t)
	m.kargo.Filter = "team-b"
	kargoKey(t, m, "enter") // projects -> stages
	kargoKey(t, m, "enter") // stages -> promotions
	kargoKey(t, m, "esc")
	if m.kargo.Kind != "Stage" || m.kargo.Project != "team-b" || m.kargoRows()[m.kargo.Cursor].Resource.Name != "prod" {
		t.Fatal("Promotion did not return directly to its Stage")
	}
	kargoKey(t, m, "4")
	kargoKey(t, m, "enter") // warehouse -> freight
	kargoKey(t, m, "esc")
	if m.kargo.Kind != "Warehouse" {
		t.Fatal("Freight did not return to its Warehouse")
	}
	kargoKey(t, m, "esc")
	if m.kargo.Kind != "Stage" {
		t.Fatal("view shortcut lost its origin")
	}
	kargoKey(t, m, "esc")
	if m.kargo.Kind != "Project" || m.kargo.Filter != "team-b" || m.kargo.Cursor != 0 {
		t.Fatal("project entry filter/cursor lost")
	}
}

func TestKargoPromotionFormTabCancelAndReturnFromResult(t *testing.T) {
	m, fixture := newKargoFixture(t)
	selectKargoStage(t, m)
	reads := fixture.Reads.Load()
	kargoKey(t, m, "ctrl+p")
	if m.kargo.Kind != "Stage" || m.kargo.Form == nil || fixture.Reads.Load() != reads+1 {
		t.Fatal("form changed the list or fanned out unrelated requests")
	}
	kargoKey(t, m, "tab")
	if m.kargo.Form.Focus != 1 {
		t.Fatal("Tab should focus Cancel")
	}
	kargoKey(t, m, "enter")
	if m.kargo.Form != nil || m.kargo.Kind != "Stage" || m.kargo.Cursor != 0 || fixture.Promotions.Load() != 0 {
		t.Fatal("Cancel did not restore the Stage")
	}
	reads = fixture.Reads.Load()
	kargoKey(t, m, "ctrl+p")
	if fixture.Reads.Load() != reads {
		t.Fatal("reopening form ignored cached Freight")
	}
	kargoKey(t, m, "shift+tab")
	if m.kargo.Form.Focus != 3 {
		t.Fatal("Shift+Tab should focus promotion mode")
	}
	kargoKey(t, m, "shift+tab")
	if m.kargo.Form.Focus != 2 {
		t.Fatal("Shift+Tab should focus Promote")
	}
	kargoKey(t, m, "enter")
	if fixture.Promotions.Load() != 1 || m.kargo.Detail == nil {
		t.Fatal("form button did not submit and open result")
	}
	kargoKey(t, m, "esc")
	if m.kargo.Kind != "Stage" || m.kargo.Detail != nil || m.kargoRows()[m.kargo.Cursor].Resource.Name != "prod" {
		t.Fatal("result did not return to original Stage")
	}
}

func TestKargoFormFilterAndEmptyChoiceCannotSubmit(t *testing.T) {
	m, fixture := newKargoFixture(t)
	selectKargoStage(t, m)
	kargoKey(t, m, "ctrl+p")
	for range 3 {
		kargoKey(t, m, "z")
	}
	kargoKey(t, m, "ctrl+p")
	if fixture.Promotions.Load() != 0 || m.kargo.Draft.Freight != "" {
		t.Fatal("empty choice was submitted")
	}
	for range 3 {
		kargoKey(t, m, "backspace")
	}
	if m.kargo.Draft.Freight != "freight-a" {
		t.Fatal("clearing form filter did not restore its choice")
	}
	for _, size := range [][2]int{{60, 20}, {80, 24}, {120, 30}} {
		m.state.Terminal.Cols, m.state.Terminal.Rows = size[0], size[1]
		view := m.renderKargoView()
		t.Logf("Form %dx%d:\n%s", size[0], size[1], stripANSI(view))
		assertInspectionLayout(t, view, size[0], size[1])
		if plain := stripANSI(view); !strings.Contains(plain, "Cancel") || !strings.Contains(plain, "Promote") {
			t.Fatal("action buttons disappeared in short terminal")
		}
	}
	kargoKey(t, m, "esc")
	if m.state.Mode != model.ModeNormal || m.kargo.Form != nil {
		t.Fatal("form Esc did not close one layer")
	}
}

func TestKargoFormChangedTargetClosesSafely(t *testing.T) {
	m, fixture := newKargoFixture(t)
	selectKargoStage(t, m)
	kargoKey(t, m, "ctrl+p")
	m.kargo.Project = "another-project"
	kargoKey(t, m, "ctrl+p")
	if fixture.Promotions.Load() != 0 || m.kargo.Form != nil || m.kargo.Draft != nil || m.kargo.Error == "" {
		t.Fatal("changed target retained a live form")
	}
	assertInspectionLayout(t, m.renderKargoView(), m.state.Terminal.Cols, m.state.Terminal.Rows)
}

func TestKargoRetryCancelReturnsToOriginalDescribe(t *testing.T) {
	m, _ := newKargoFixture(t)
	r := failedKargoPromotion(t, m, "Failed")
	m.showKargoDocument(r, "YAML", false)
	m.state.Diff.Offset = 3
	kargoKey(t, m, "ctrl+t")
	kargoKey(t, m, "enter") // Cancel is focused by default for retry.
	if m.state.Diff == nil || m.state.Diff.InspectionKind != "YAML" || m.state.Diff.Offset != 3 || m.kargo.Detail.Name != r.Name {
		t.Fatal("retry cancel lost its detail origin")
	}
}

func TestKargoEscapeWithTextSelectionStillReturnsToOrigin(t *testing.T) {
	m, _ := newKargoFixture(t)
	selectKargoStage(t, m)
	m.selection.SetStart(selection.Position{Row: 1, Col: 1})
	m.selection.SetEnd(selection.Position{Row: 1, Col: 5})
	m.selection.Finalize()
	kargoKey(t, m, "esc")
	if m.kargo.Kind != "Project" || !m.selection.IsEmpty() {
		t.Fatal("text selection consumed the back key")
	}
}

func TestLeavingKargoRestoresArgoCursorAndView(t *testing.T) {
	fixtureModel, _ := newKargoFixture(t)
	m := buildTestModelWithApps(120, 30)
	m.config = fixtureModel.config
	m.state.Navigation.View, m.state.Navigation.SelectedIdx = model.ViewProjects, 2
	_, cmd := m.openKargo()
	driveKargo(t, m, cmd)
	m.handleKeyMsg(testKeyMsg("esc"))
	if m.state.Navigation.View != model.ViewProjects || m.state.Navigation.SelectedIdx != 2 {
		t.Fatal("leaving Kargo discarded Argo entry point")
	}
}
