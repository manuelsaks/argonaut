package main

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/darksworm/argonaut/pkg/model"
	"github.com/darksworm/argonaut/pkg/tui/selection"
)

func TestDescribeCompactFormattingPreservesAcronymsAndValues(t *testing.T) {
	for key, want := range map[string]string{"repoURL": "Repo URL", "URL": "URL", "apiURL": "Api URL", "UID": "UID", "targetRevision": "Target Revision"} {
		if got := describeLabel(key); got != want {
			t.Errorf("%s => %q, want %q", key, got, want)
		}
	}
	object, err := decodeInspectionObject(json.RawMessage(`{"kind":"Example","metadata":{"name":"web","annotations":{"kubectl.kubernetes.io/last-applied-configuration":"huge historical json"}},"spec":{"empty":[],"nil":null,"nested":{"empty":{}},"enabled":false,"replicas":0,"containers":[{"name":"web","ports":[]}]}}`), false)
	if err != nil {
		t.Fatal(err)
	}
	text := describeInspectionObject(object, false, nil, "")
	for _, unwanted := range []string{"Empty:", "Nil:", "Nested:", "Ports:", "huge historical", "-\n"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("noisy describe still contains %q:\n%s", unwanted, text)
		}
	}
	for _, want := range []string{"Enabled: false", "Replicas: 0", "- Name: web"} {
		if !strings.Contains(text, want) {
			t.Fatalf("lost meaningful %q:\n%s", want, text)
		}
	}
}

func TestSearchEnterImmediatelyEnablesNavigationAndActions(t *testing.T) {
	m := buildDeleteTestModel(100, 30)
	m.state.Apps = []model.App{{Name: "a-other"}, {Name: "pay-api"}, {Name: "pay-web"}, {Name: "z-other"}}
	m.rebuildAppIndex()
	m.state.Navigation.SelectedIdx = 3
	m.listNav.SetItemCount(4)
	m.listNav.SetCursor(3)
	m.handleKeyMsg(testKeyMsg("/"))
	for _, key := range []string{"p", "a", "y"} {
		m.handleKeyMsg(testKeyMsg(key))
	}
	m.handleKeyMsg(testKeyMsg("enter"))
	if m.state.Mode != model.ModeNormal || len(m.getVisibleItems()) != 2 {
		t.Fatal("Enter must finish search")
	}
	if m.listNav.Cursor() != m.state.Navigation.SelectedIdx {
		t.Fatal("filtered navigator and cursor diverged")
	}
	m.handleKeyMsg(testKeyMsg("k"))
	if m.state.Navigation.SelectedIdx != 0 {
		t.Fatal("first navigation key after search was ignored")
	}
	m.handleKeyMsg(testKeyMsg("ctrl+s"))
	if m.state.Mode != model.ModeConfirmSync || m.state.Modals.ConfirmTarget == nil || *m.state.Modals.ConfirmTarget != "pay-api" {
		t.Fatal("first action after search did not target the filtered row")
	}
}

func TestMouseSelectionPersistsAndFreezesCopiedText(t *testing.T) {
	m := buildDeleteTestModel(100, 30)
	m.View()
	m.handleMouseClickMsg(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: 1})
	m.handleMouseMotionMsg(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 16, Y: 1})
	_, cmd := m.handleMouseReleaseMsg(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 16, Y: 1})
	if cmd == nil || m.selection.Active || !m.selection.HasContent || m.selectedText == "" {
		t.Fatal("release must retain the selected text and schedule a copy")
	}
	snapshot, text := m.selectionSnapshot, m.selectedText
	m.state.Apps = []model.App{{Name: "background-watch-change"}}
	m.rebuildAppIndex()
	m.View()
	if m.selectionSnapshot != snapshot || m.selection.ExtractText(m.lastRenderedLines) != text {
		t.Fatal("background render moved the selected text")
	}
	_, cmd = m.handleKeyMsg(testKeyMsg("ctrl+c"))
	if cmd == nil || m.selection.IsEmpty() {
		t.Fatal("Ctrl+C must copy the selection without quitting or clearing it")
	}
	m.handleKeyMsg(testKeyMsg("esc"))
	if !m.selection.IsEmpty() || m.selectionSnapshot != "" || m.selectedText != "" {
		t.Fatal("Esc must clear selection explicitly")
	}
}

func TestSelectionUsesTerminalCellsForWideText(t *testing.T) {
	s := selection.New()
	s.SetStart(selection.Position{Row: 0, Col: 2})
	s.SetEnd(selection.Position{Row: 0, Col: 5})
	s.Finalize()
	if got := s.ExtractText([]string{"界abc tail"}); got != "abc" {
		t.Fatalf("wrong terminal columns: %q", got)
	}
}

func TestInspectionColorsAndSearchKeepTheDocument(t *testing.T) {
	m := buildDeleteTestModel(120, 30)
	for _, sample := range []struct{ text, kind string }{{"repoURL: https://example.test", "YAML"}, {"+ image: web:v2", "Diff"}, {"- image: web:v1", "Diff"}, {"Health: Healthy", "Describe"}} {
		colored := m.colorInspectionLine(sample.text, sample.kind)
		if colored == sample.text || ansi.Strip(colored) != sample.text {
			t.Fatalf("missing/destructive highlighting for %q: %q", sample.text, colored)
		}
	}
	m.startResourceInspection(inspectionTarget{Application: true, AppName: "web", Kind: "Application", Name: "web"}, "Describe")
	m.state.Diff.Content = []string{"Name: web", "Spec:", "  repoURL: https://one", "  other: value", "  repoURL: https://two"}
	m.state.Diff.SideContent = []string{"--- live", "+++ desired", "@@ -1 +1 @@", "- old", "+ new"}
	m.state.Diff.Loading = false
	m.handleKeyMsg(testKeyMsg("/"))
	m.Update(tea.PasteMsg{Content: "URL"})
	m.handleKeyMsg(testKeyMsg("enter"))
	if m.state.Mode != model.ModeDiff || m.state.Diff.MatchIndex != 2 || len(m.state.Diff.Content) != 5 {
		t.Fatal("find should retain full document and focus first match")
	}
	m.renderInspectionView()
	m.handleKeyMsg(testKeyMsg("n"))
	if m.state.Diff.MatchIndex != 4 {
		t.Fatal("next match should work independently of viewport clamping")
	}
	m.handleKeyMsg(testKeyMsg("tab"))
	if !m.state.Diff.FocusDiff {
		t.Fatal("Tab must focus the diff pane")
	}
	m.cancelResourceInspection()
}

func TestUXLayoutsAllContextsAndSplitDescribe(t *testing.T) {
	m := allContextsTestModel(t)
	for _, size := range []struct{ cols, rows int }{{60, 20}, {80, 24}, {100, 30}, {120, 30}, {160, 40}} {
		m.state.Terminal.Cols, m.state.Terminal.Rows = size.cols, size.rows
		m.state.UI.ActiveFilter = ""
		for _, view := range []model.View{model.ViewApps, model.ViewClusters, model.ViewApplicationSets, model.ViewContexts} {
			m.state.Mode = model.ModeNormal
			m.state.Navigation.View = view
			text := m.renderMainLayout()
			assertInspectionLayout(t, text, size.cols, size.rows)
			if size.cols == 80 || size.cols == 120 {
				t.Logf("\n%s %dx%d\n%s", view, size.cols, size.rows, stripANSI(text))
			}
		}
		m.startResourceInspection(inspectionTarget{ContextName: "prod", Application: true, AppName: "web", Kind: "Application", Name: "web"}, "Describe")
		m.state.Diff.Loading = false
		m.state.Diff.Content = []string{"Name: web", "Namespace: argocd", "", "Spec:", "  Repo URL: https://example.test/really/long/path/that/should/pan/without/wrapping", "  Containers:", "    - Name: web", "      Image: registry.example/web:v1"}
		m.state.Diff.SideContent = []string{"--- live", "+++ desired", "@@ -1 +1 @@", "- image: web:v1", "+ image: web:v2"}
		for _, focus := range []bool{false, true} {
			m.state.Diff.FocusDiff = focus
			text := m.renderInspectionView()
			assertInspectionLayout(t, text, size.cols, size.rows)
			if size.cols >= 100 && !strings.Contains(stripANSI(text), "DIFF: LIVE -> DESIRED") {
				t.Fatal("wide describe should have a right-hand diff column")
			}
			if size.cols == 80 || size.cols == 120 {
				t.Logf("\ndescribe focus=%v %dx%d\n%s", focus, size.cols, size.rows, stripANSI(text))
			}
		}
		m.cancelResourceInspection()
		m.state.Diff = nil
	}
}
