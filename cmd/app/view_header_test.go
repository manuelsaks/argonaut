package main

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/darksworm/argonaut/pkg/model"
)

func TestHeaderShowsCurrentViewAndAvailableActions(t *testing.T) {
	for _, tc := range []struct {
		view         model.View
		mode         model.Mode
		want, absent string
	}{
		{model.ViewApps, model.ModeNormal, "<s> sync", "<a> actions"},
		{model.ViewTree, model.ModeNormal, "<a> actions", "auto-login on/off"},
		{model.ViewContexts, model.ModeNormal, "<a> auto-login on/off", "<ctrl-d> delete"},
		{model.ViewApps, model.ModeSearch, "<enter> apply", "<ctrl-d> delete"},
		{model.ViewApps, model.ModeConfirmAppDelete, "<y> delete", "<enter> confirm"},
		{model.ViewTree, model.ModeConfirmResourceDelete, "<f> force", "<enter> confirm"},
		{model.ViewApps, model.ModeAuthRequired, "<L> SSO login", "<s> sync"},
		{model.ViewApps, model.ModeDiff, "<h/l> pan", "<s> sync"},
	} {
		t.Run(string(tc.view)+"/"+string(tc.mode), func(t *testing.T) {
			m := buildSyncTestModel(140, 35)
			m.currentContextName = "production"
			m.state.Navigation.View, m.state.Mode = tc.view, tc.mode
			text := stripANSI(m.renderBanner())
			if !strings.Contains(text, "production") || !strings.Contains(text, tc.want) || strings.Contains(text, tc.absent) {
				t.Fatalf("wrong view legend:\n%s", text)
			}
			m.state.Mode = model.ModeConfirmSync
			m.state.Modals.ConfirmSyncForcePending = true
			if strings.Contains(stripANSI(m.renderBanner()), "<p> prune") {
				t.Fatal("force confirmation must not advertise inactive options")
			}
		})
	}
}

func TestViewSwitchShortcutsRespectTextInputAndConfirmation(t *testing.T) {
	for _, tc := range []struct {
		key  string
		view model.View
	}{
		{"0", model.ViewContexts}, {"1", model.ViewApps}, {"2", model.ViewClusters},
		{"3", model.ViewNamespaces}, {"4", model.ViewProjects}, {"5", model.ViewApplicationSets},
	} {
		t.Run(tc.key, func(t *testing.T) {
			m := contextPickerTestModel(t)
			_, _ = m.handleKeyMsg(testKeyMsg(tc.key))
			if m.state.Navigation.View != tc.view {
				t.Fatalf("%s opened %s, want %s", tc.key, m.state.Navigation.View, tc.view)
			}
		})
	}
	m := contextPickerTestModel(t)
	_, _ = m.handleKeyMsg(testKeyMsg("/"))
	_, _ = m.handleKeyMsg(testKeyMsg("3"))
	if m.state.Mode != model.ModeSearch || m.inputComponents.GetSearchValue() != "3" {
		t.Fatal("view shortcuts must not intercept text")
	}
	m.state.Mode = model.ModeConfirmSync
	_, _ = m.handleKeyMsg(testKeyMsg("3"))
	if m.state.Mode != model.ModeConfirmSync || m.state.Navigation.View != model.ViewApps {
		t.Fatal("view shortcuts must not bypass confirmations")
	}
}

// One batched visual capture at the shipped terminal sizes. Its verbose test
// log is also an inspectable rendering of the actual UI, not a mockup.
func TestHeaderLayouts(t *testing.T) {
	for _, size := range []struct{ cols, rows int }{{80, 24}, {100, 30}, {120, 30}, {160, 40}, {60, 20}} {
		for _, view := range []model.View{model.ViewApps, model.ViewTree, model.ViewContexts} {
			m := buildSyncTestModel(size.cols, size.rows)
			m.currentContextName = "production-eu"
			m.state.Navigation.View = view
			m.state.ContextNames = []string{"development", "production-eu", "staging"}
			m.config.Auth.AutoRelogin = map[string]bool{"production-eu": true}
			m.state.Apps = []model.App{{Name: "payments-api", Sync: "Synced", Health: "Healthy"}}
			header := m.renderBanner()
			for _, line := range strings.Split(header, "\n") {
				if lipgloss.Width(line) > size.cols-2 {
					t.Fatalf("header overflows at %dx%d: %s", size.cols, size.rows, line)
				}
			}
			text := m.renderMainLayout()
			if len(strings.Split(text, "\n")) > size.rows {
				t.Fatalf("%s layout exceeds %dx%d", view, size.cols, size.rows)
			}
			if size.cols == 80 || size.cols == 120 {
				t.Logf("\n%s %dx%d\n%s", view, size.cols, size.rows, stripANSI(text))
			}
		}
	}
}
