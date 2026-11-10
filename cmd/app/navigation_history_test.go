package main

import (
	"reflect"
	"testing"

	"github.com/darksworm/argonaut/pkg/config"
	"github.com/darksworm/argonaut/pkg/model"
)

func TestEscapeRestoresActualDimensionOrigin(t *testing.T) {
	for _, view := range []model.View{model.ViewClusters, model.ViewNamespaces, model.ViewProjects, model.ViewApplicationSets} {
		t.Run(string(view), func(t *testing.T) {
			m := buildTestModelWithApps(120, 30)
			m.config.Network = config.NetworkConfig{LowBandwidth: true, RefreshInterval: "0"}
			for i := range m.state.Apps {
				m.state.Apps[i].ClusterID = stringPtr("site1")
				m.state.Apps[i].ApplicationSet = stringPtr("set-a")
			}
			m.rebuildAppIndex()
			m.state.Navigation.View = view
			m.state.Navigation.SelectedIdx = len(m.getVisibleItems()) - 1
			key := navigationKey(m.getVisibleItems()[m.state.Navigation.SelectedIdx])
			m.state.UI.ActiveFilter = key
			m.state.Navigation.SelectedIdx = 0
			m.handleKeyMsg(testKeyMsg("enter"))
			m.handleKeyMsg(testKeyMsg("esc"))
			if m.state.Navigation.View != view || m.state.UI.ActiveFilter != key || navigationKey(m.getVisibleItems()[m.state.Navigation.SelectedIdx]) != key {
				t.Fatalf("Escape lost entry point: view=%s filter=%q index=%d", m.state.Navigation.View, m.state.UI.ActiveFilter, m.state.Navigation.SelectedIdx)
			}
			if len(m.state.SavedNavigation) != 0 || len(m.state.Selections.SelectedApps) != 0 {
				t.Fatal("navigation left a stack entry or implicit selection")
			}
		})
	}
}

func TestTreeBackRestoresCursorByIdentityAfterReorder(t *testing.T) {
	m := buildTestModelWithApps(100, 30)
	m.state.Apps[1].AppNamespace = stringPtr("team-b")
	m.state.UI.ActiveFilter = "app-"
	m.state.UI.SearchQuery = "app-"
	m.listNav.SetViewportHeight(1)
	m.listNav.SetItemCount(3)
	m.listNav.SetCursor(1)
	m.handleKeyMsg(testKeyMsg("enter"))
	// A refresh inserts an alphabetically earlier application while inside.
	m.state.Apps = append(m.state.Apps, model.App{Name: "app-0"})
	m.rebuildAppIndex()
	m.handleKeyMsg(testKeyMsg("esc"))
	items := m.getVisibleItems()
	if m.state.Navigation.View != model.ViewApps || items[m.state.Navigation.SelectedIdx].(model.App).Key() != "team-b/app-b" || m.listNav.Cursor() != m.state.Navigation.SelectedIdx || m.listNav.ScrollOffset() == 0 {
		t.Fatal("back did not restore the same object and visible cursor")
	}
	m.handleKeyMsg(testKeyMsg("j"))
	if len(m.state.Selections.SelectedApps) != 0 {
		t.Fatal("Enter/back created a checkbox selection")
	}
	m.handleKeyMsg(testKeyMsg("space"))
	if len(m.state.Selections.SelectedApps) != 1 {
		t.Fatal("Space no longer selects an object")
	}
}

func TestChildTreeBackPreservesExpandedTreeAndCursor(t *testing.T) {
	m := buildEventsPaneTestModel()
	m.state.Apps = []model.App{{Name: "test-app", AppNamespace: stringPtr("test-namespace")}, {Name: "child", AppNamespace: stringPtr("team-a")}}
	m.treeView.SetSelectedIndex(1)
	m.treeNav.SetItemCount(m.treeView.VisibleCount())
	m.treeNav.SetCursor(1)
	parent := m.treeView
	m.handleNavigateToChildApp("child", "team-a")
	m.handleKeyMsg(testKeyMsg("esc"))
	if m.treeView != parent || m.treeView.SelectedIndex() != 1 || m.treeNav.Cursor() != 1 || m.state.UI.TreeApp.Name != "test-app" {
		t.Fatal("parent tree presentation was replaced on back")
	}
}

func TestExplicitEventsPaneBackStaysOnResource(t *testing.T) {
	m := buildEventsPaneTestModel()
	m.treeView.SetSelectedIndex(1)
	m.handleKeyMsg(testKeyMsg("enter"))
	m.handleKeyMsg(testKeyMsg("esc"))
	if m.state.Events != nil || m.state.Navigation.View != model.ViewTree || m.treeView.SelectedIndex() != 1 {
		t.Fatal("Esc from manually opened events must return to its resource")
	}
}

func TestAllContextsEnterDoesNotSelectAndBackSkipsInternalList(t *testing.T) {
	m := allContextsTestModel(t)
	m.state.Navigation.SelectedIdx = 1
	app := m.getVisibleItems()[1].(model.App)
	updated, _, handled := m.handleOverviewAction(testKeyMsg("enter"))
	child := updated.(*Model)
	if !handled || len(child.state.Selections.SelectedApps) != 0 {
		t.Fatal("Enter created an implicit overview selection")
	}
	back, _ := child.handleKeyMsg(testKeyMsg("esc"))
	if back != m || m.getVisibleItems()[m.state.Navigation.SelectedIdx].(model.App).Key() != app.Key() {
		t.Fatal("back opened an intermediate single-context list or moved cursor")
	}
	if !reflect.DeepEqual(m.state.Selections.SelectedApps, model.NewStringSet()) {
		t.Fatal("back changed explicit selections")
	}
}

func TestContextPickerBackPreservesViewAndCursor(t *testing.T) {
	m := buildTestModelWithApps(100, 30)
	m.state.UI.ActiveFilter = "app-"
	m.openContextPicker()
	m.handleKeyMsg(testKeyMsg("esc"))
	if m.state.Navigation.View != model.ViewApps || m.state.Navigation.SelectedIdx != 1 || m.state.UI.ActiveFilter != "app-" {
		t.Fatal("picker discarded entry point")
	}
}

func TestAllContextsCancelActionSkipsInternalList(t *testing.T) {
	m := allContextsTestModel(t)
	updated, _, _ := m.handleOverviewAction(testKeyMsg("ctrl+d"))
	child := updated.(*Model)
	back, _ := child.handleKeyMsg(testKeyMsg("esc"))
	if back != m {
		t.Fatal("cancelled action opened an intermediate single-context list")
	}
}
