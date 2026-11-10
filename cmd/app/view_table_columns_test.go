package main

import (
	"strings"
	"testing"

	"github.com/darksworm/argonaut/pkg/kargo"
	"github.com/darksworm/argonaut/pkg/model"
)

func TestLongNamesUseAvailableTableWidth(t *testing.T) {
	name := "production-services-" + strings.Repeat("a", 44)
	for _, all := range []bool{false, true} {
		m := destinationTestModel()
		m.state.Terminal.Cols = 180
		if all {
			m.allContexts = &allContextsState{}
		}
		m.state.Apps = []model.App{{Name: name, ContextName: "production-west-company", Sync: "Synced", Health: "Healthy", Namespace: stringPtr("apps")}}
		m.rebuildAppIndex()
		m.state.Navigation.View = model.ViewApps
		if out := stripANSI(m.renderListView(10)); !strings.Contains(out, name) {
			t.Fatalf("app name truncated despite available width, all=%t:\n%s", all, out)
		}
		m.state.DestinationServers = []model.DestinationServer{{Name: name, Server: "https://site.example", ContextName: "production-west-company"}}
		m.rebuildAppIndex()
		m.state.Navigation.View = model.ViewClusters
		if out := stripANSI(m.renderListView(10)); !strings.Contains(out, name) {
			t.Fatalf("server name truncated despite available width, all=%t:\n%s", all, out)
		}
		m.state.Apps[0].Name = "web"
		m.state.Apps[0].ApplicationSet = &name
		m.rebuildAppIndex()
		m.state.Navigation.View = model.ViewApplicationSets
		if out := stripANSI(m.renderListView(10)); !strings.Contains(out, name) {
			t.Fatalf("ApplicationSet name truncated despite available width, all=%t:\n%s", all, out)
		}
		for _, cols := range []int{40, 60, 80, 120, 180, 300} {
			m.state.Terminal.Cols = cols
			for _, view := range []model.View{model.ViewApps, model.ViewClusters, model.ViewApplicationSets} {
				m.state.Navigation.View = view
				assertInspectionLayout(t, m.renderListView(10), cols, 30)
			}
		}
	}
}

func TestKargoLongNamesUseAvailableTableWidth(t *testing.T) {
	m, _ := newKargoFixture(t)
	m.state.Terminal.Cols = 160
	m.state.Terminal.Rows = 30
	name := "production-services-" + strings.Repeat("a", 44)
	for _, kind := range []string{"Project", "Stage", "Promotion", "Warehouse", "Freight"} {
		m.kargo.Kind = kind
		m.kargo.Items = []kargo.Resource{{Kind: kind, Name: name, Object: map[string]any{"status": map[string]any{"phase": "Ready"}}}}
		out := stripANSI(m.renderKargoView())
		assertInspectionLayout(t, out, 160, 30)
		// Every row is displayed in the table, even when no preview is present.
		if !strings.Contains(out, "│ "+name) {
			t.Fatalf("%s name is clipped despite enough horizontal space:\n%s", kind, out)
		}
	}
}
