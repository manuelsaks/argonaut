package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/darksworm/argonaut/pkg/model"
)

func TestBuiltInPagerWorksWithoutExternalTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	m := buildSyncTestModel(80, 24)
	m.switchEpoch = 7
	m.state.UI.SearchQuery = "apps-filter"
	text := strings.Repeat("first line\r\n", 30) + "target żółć\r\n"
	msg := m.openTextPager("Diff", text)()
	if _, ok := msg.(textPagerMsg); !ok {
		t.Fatalf("external pager still required: %#v", msg)
	}
	m.Update(msg)
	if m.state.Mode != model.ModeDiff || m.state.Diff.Loading || m.inPager {
		t.Fatal("built-in pager did not open")
	}
	m.handleKeyMsg(testKeyMsg("j"))
	if m.state.Diff.Offset != 1 {
		t.Fatal("pager did not scroll")
	}
	m.handleKeyMsg(testKeyMsg("l"))
	if m.state.Diff.HorizontalOffset != 8 {
		t.Fatal("horizontal scroll did not work")
	}
	m.handleKeyMsg(testKeyMsg("h"))
	m.handleKeyMsg(testKeyMsg("/"))
	for _, char := range "target" {
		m.handleEnhancedSearchModeKeys(testKeyMsg(string(char)))
	}
	if m.state.Diff.SearchQuery != "target" || !strings.Contains(ansi.Strip(m.renderDiffView()), "target żółć") {
		t.Fatal("pager search did not show the matching line")
	}
	if m.state.UI.SearchQuery != "apps-filter" {
		t.Fatal("pager search changed application filtering")
	}
	m.Update(tea.PasteMsg{Content: " żółć"})
	if m.state.Diff.SearchQuery != "target żółć" || m.state.UI.SearchQuery != "apps-filter" {
		t.Fatal("pasting in pager search changed application filtering")
	}
	m.handleEnhancedSearchModeKeys(testKeyMsg("enter"))
	if m.state.Mode != model.ModeDiff {
		t.Fatal("search did not return to pager")
	}
	m.handleDiffModeKeys(testKeyMsg("q"))
	if m.state.Mode != model.ModeNormal || m.state.Diff != nil {
		t.Fatal("pager did not close")
	}
}

func TestBuiltInPagerIgnoresOldContextAndReturnsToHistory(t *testing.T) {
	m := buildSyncTestModel(80, 24)
	m.switchEpoch = 3
	m.state.Diff = nil
	m.Update(textPagerMsg{Title: "old", Text: "stale", SwitchEpoch: 2})
	if m.state.Diff != nil {
		t.Fatal("stale pager content applied")
	}
	m.state.Rollback = &model.RollbackState{AppName: "demo", Mode: "list"}
	m.Update(textPagerMsg{Title: "new", Text: "current", SwitchEpoch: 3})
	m.handleDiffModeKeys(testKeyMsg("esc"))
	if m.state.Mode != model.ModeRollback {
		t.Fatal("closing rollback diff lost deployment history")
	}
}

func TestBuiltInPagerClipsLongLinesAndHandlesEmptySearch(t *testing.T) {
	m := buildSyncTestModel(80, 24)
	m.showTextPager(textPagerMsg{Title: "Diff", Text: "\x1b[31m" + strings.Repeat("界", 200) + "\x1b[0m"})
	for _, line := range strings.Split(m.renderDiffView(), "\n") {
		if ansi.StringWidth(line) > 80 {
			t.Fatalf("pager overflowed terminal: %d cells", ansi.StringWidth(line))
		}
	}
	m.state.Diff.SearchQuery = "not-present"
	if !strings.Contains(ansi.Strip(m.renderDiffView()), "0-0/0") {
		t.Fatal("empty search count is invalid")
	}
}
