package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/darksworm/argonaut/pkg/config"
	"github.com/darksworm/argonaut/pkg/model"
)

func resourceCommand(t *testing.T, m *Model, command string) *Model {
	t.Helper()
	m.handleKeyMsg(testKeyMsg(":"))
	m.inputComponents.SetCommandValue(command)
	updated, _ := m.handleKeyMsg(testKeyMsg("enter"))
	return updated.(*Model)
}

func TestRecentResourcesUseActualHistoryAndRestoreCursor(t *testing.T) {
	m := buildTestModelWithApps(160, 40)
	m.config.Network = config.NetworkConfig{LowBandwidth: true, RefreshInterval: "0"}
	m.currentContextName = "prod"
	m.state.UI.ActiveFilter = "app-"
	m = resourceCommand(t, m, "projects")
	m = resourceCommand(t, m, "namespaces")
	m = resourceCommand(t, m, "servers")
	entries := m.recentEntries()
	if len(entries) != 3 || entries[0].Argo.View != model.ViewNamespaces || entries[1].Argo.View != model.ViewProjects || entries[2].Argo.View != model.ViewApps {
		t.Fatal("recent slots are fixed views or in the wrong order")
	}
	m.handleKeyMsg(testKeyMsg("3"))
	if m.state.Navigation.View != model.ViewApps || m.state.Navigation.SelectedIdx != 1 || m.state.UI.ActiveFilter != "app-" || m.listNav.Cursor() != 1 || len(m.state.Selections.SelectedApps) != 0 {
		t.Fatal("recent jump lost filter/cursor or created a checkbox selection")
	}
	m.handleKeyMsg(testKeyMsg("1"))
	if m.state.Navigation.View != model.ViewClusters {
		t.Fatal("slot 1 should now return to the last viewed servers list")
	}
	m.handleKeyMsg(testKeyMsg("1"))
	if m.state.Navigation.View != model.ViewApps {
		t.Fatal("slot 1 must toggle actual last resource rather than a static view")
	}
	for _, command := range []string{"projects", "namespaces", "servers", "appsets", "apps"} {
		m = resourceCommand(t, m, command)
	}
	if len(m.recentEntries()) > 4 {
		t.Fatal("history exceeded four available slots")
	}
}

func TestRecentResourcesAreContextScopedAndDoNotBypassLogin(t *testing.T) {
	m := buildTestModelWithApps(120, 30)
	m.currentContextName = "prod"
	m = resourceCommand(t, m, "projects")
	m.currentContextName = "dev"
	m.handleKeyMsg(testKeyMsg("1"))
	if m.state.Navigation.View != model.ViewProjects || len(m.recentEntries()) != 0 {
		t.Fatal("recent resource switched context implicitly")
	}
	m.currentContextName = "prod"
	m.reloginInProgress = true
	_, cmd := m.handleKeyMsg(testKeyMsg("1"))
	if m.state.Navigation.View != model.ViewProjects || cmd != nil {
		t.Fatal("recent shortcut bypassed SSO")
	}
	m.reloginInProgress = false
	m.handleKeyMsg(testKeyMsg("1"))
	if m.state.Navigation.View != model.ViewApps {
		t.Fatal("context history was not retained")
	}
}

func TestRecentResourceAcrossKargoAndArgoCanEscapeBack(t *testing.T) {
	m, _ := newKargoFixture(t)
	m.handleKeyMsg(testKeyMsg("a"))
	m = resourceCommand(t, m, "kargo")
	selectKargoStage(t, m)
	kargoCommand(t, m, "warehouses")
	kargoCommand(t, m, "freight")
	entries := m.recentEntries()
	if len(entries) < 2 || entries[0].Kargo == nil || entries[0].Kargo.Kind != "Warehouse" {
		t.Fatal("Kargo lists were not remembered")
	}
	kargoKey(t, m, "1")
	if m.kargo.Kind != "Warehouse" {
		t.Fatal("recent slot did not restore Kargo list")
	}
	// The originating Argo Apps list is also available, without a static screen menu.
	index := -1
	for i, entry := range m.recentEntries() {
		if entry.Kargo == nil && entry.Argo.View == model.ViewApps {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("Argo source was not retained in recent resources")
	}
	updated, cmd := m.handleKeyMsg(testKeyMsg(string(rune('1' + index))))
	m = updated.(*Model)
	if m.state.Navigation.View != model.ViewApps {
		t.Fatal("recent Argo route did not leave Kargo")
	}
	_ = cmd // Watch commands are not run against the fixture's unrelated Argo server.
	updated, _ = m.handleKeyMsg(testKeyMsg("esc"))
	m = updated.(*Model)
	if m.state.Navigation.View != model.ViewKargo || m.kargo.Kind != "Warehouse" {
		t.Fatal("Esc from a recent jump did not return to the Kargo source")
	}
}

func TestRecentNumbersRemainInputInsidePromotionPopup(t *testing.T) {
	m, fixture := newKargoFixture(t)
	selectKargoStage(t, m)
	kargoCommand(t, m, "warehouses")
	kargoCommand(t, m, "stages")
	kargoKey(t, m, "ctrl+p")
	kargoKey(t, m, "1")
	if m.kargo.Form == nil || m.kargo.Form.Query != "1" || fixture.Promotions.Load() != 0 {
		t.Fatal("numeric resource shortcut stole form input")
	}
}

func TestK9sHeaderOnlyListsRecentResourcesAndActions(t *testing.T) {
	m := buildTestModelWithApps(160, 40)
	m.currentContextName = "prod"
	m = resourceCommand(t, m, "projects")
	header := stripANSI(m.renderBanner())
	for _, want := range []string{"Context:", "Server:", "User:", "Resource:", "<1> apps"} {
		if !strings.Contains(header, want) {
			t.Fatalf("header lacks %q: %s", want, header)
		}
	}
	for _, absent := range []string{"<2> servers", "<3> ns", "<4> proj", "<5>", "<6>"} {
		if strings.Contains(header, absent) {
			t.Fatalf("fixed screen menu remains: %s", header)
		}
	}
}

func TestK9sStyleReferenceCaptures(t *testing.T) {
	for _, size := range [][2]int{{60, 20}, {120, 30}, {160, 40}} {
		m := buildTestModelWithApps(size[0], size[1])
		m.currentContextName, m.currentUsername = "production", "sso@example.test"
		m.state.Server = &model.Server{BaseURL: "https://argocd.example.test"}
		m.config.Network = config.NetworkConfig{LowBandwidth: true, RefreshInterval: "0"}
		for _, command := range []string{"projects", "namespaces", "servers", "appsets", "apps"} {
			m = resourceCommand(t, m, command)
		}
		capture := func(name, content string) {
			t.Helper()
			assertInspectionLayout(t, content, size[0], size[1])
			if os.Getenv("ARGONAUT_STYLE_CAPTURE") == "1" {
				path := filepath.Join("..", "..", ".audit", fmt.Sprintf("k9s-%s-%dx%d.ansi", name, size[0], size[1]))
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
				t.Logf("%s %dx%d:\n%s", name, size[0], size[1], stripANSI(content))
			}
		}
		capture("argo", m.renderMainLayout())
		km, fixture := newKargoFixture(t)
		fixture.Stages.Store(upstreamStages("freight-a"))
		fixture.Freights.Store(eligibleUpstreamFreights())
		selectKargoStage(t, km)
		kargoCommand(t, km, "warehouses")
		kargoCommand(t, km, "freight")
		kargoCommand(t, km, "stages")
		km.state.Terminal.Cols, km.state.Terminal.Rows = size[0], size[1]
		kargoKey(t, km, "ctrl+u")
		capture("popup", km.renderKargoView())
		modal := stripANSI(km.renderKargoPromotionPopup())
		if !strings.Contains(modal, "Filter:") || km.View().Cursor == nil {
			t.Fatal("focused input must stay visible and use a native terminal cursor")
		}
		caret := km.View().Cursor
		rows := strings.Split(stripANSI(km.renderKargoView()), "\n")
		if caret.Y < 0 || caret.Y >= len(rows) || !strings.Contains(rows[caret.Y], "Filter:") {
			t.Fatal("native cursor is outside its visible input row")
		}
		if !strings.Contains(modal, "<Promote from upstream>") || !strings.Contains(modal, "┌") || strings.Contains(modal, "╭") {
			t.Fatal("popup does not use the reference's titled square frame")
		}
		if !strings.Contains(modal, "Cancel") || !strings.Contains(modal, "Promote from upstream") {
			t.Fatal("popup lost its action buttons")
		}
	}
}
