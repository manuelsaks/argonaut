package main

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/model"
	"github.com/darksworm/argonaut/pkg/tui/listnav"
	"github.com/darksworm/argonaut/pkg/tui/treeview"
)

// Keep presentation state alongside the data-only navigation stack. Each entry
// owns its cursor, scroll position and (for app-of-apps) expanded resource tree.
type navigationFrame struct {
	list, tree    listnav.ListNavigator
	view          *treeview.TreeView
	kargo         *kargoLocation
	kargoInstance string
}

func navigationKey(item interface{}) string {
	if app, ok := item.(model.App); ok {
		return app.Key()
	}
	return fmt.Sprint(item)
}

func (m *Model) saveNavigationState() {
	if m.navigationFrames == nil {
		m.navigationFrames = make(map[int]navigationFrame)
	}
	frame := navigationFrame{view: m.treeView}
	if m.state.Navigation.View == model.ViewKargo && m.kargo != nil {
		loc := m.kargoLocation()
		frame.kargo = &loc
		frame.kargoInstance = m.kargo.Connections[m.kargo.Connection].Name
	}
	if m.listNav != nil {
		frame.list = *m.listNav
	}
	if m.treeNav != nil {
		frame.tree = *m.treeNav
	}
	i := len(m.state.SavedNavigation)
	m.navigationFrames[i] = frame
	m.state.SaveNavigationState()
	items := m.getVisibleItemsForCurrentView()
	idx := m.state.Navigation.SelectedIdx
	if idx >= 0 && idx < len(items) {
		m.state.SavedNavigation[i].FocusKey = navigationKey(items[idx])
	}
}

func (m *Model) restoreNavigation() (bool, tea.Cmd) {
	n := len(m.state.SavedNavigation) - 1
	if n < 0 {
		return false, nil
	}
	top := m.state.SavedNavigation[n]
	frame, hasFrame := m.navigationFrames[n]
	delete(m.navigationFrames, n)
	m.cancelResourceInspection()
	m.cleanupTreeWatchers()
	m.state.RestoreNavigationState()
	m.state.Mode, m.state.Diff = model.ModeNormal, nil
	m.state.UI.SearchQuery, m.state.UI.ActiveFilter = top.SavedSearchQuery, top.SavedActiveFilter
	m.state.UI.Command = ""
	if top.SavedSelections != nil {
		m.state.Selections = *top.SavedSelections
	}
	m.state.UI.TreeApp = top.TreeApp
	if hasFrame {
		m.listNav = &frame.list
		m.treeNav = &frame.tree
		m.treeView = frame.view
	}
	if top.View == model.ViewKargo && frame.kargo != nil {
		m.cancelAllContextRequests()
		m.cleanupAppWatcher()
		m.watchChan = nil
		index := -1
		for i, connection := range m.kargo.Connections {
			if connection.Name == frame.kargoInstance {
				index = i
				break
			}
		}
		if index < 0 {
			m.kargo.Kind, m.kargo.Error = "Connection", "Previous instance is no longer configured"
			return true, nil
		}
		if m.kargo.Connection != index {
			m.selectKargoConnection(index)
		}
		loc := *frame.kargo
		loc.Connection = index
		m.restoreKargoLocation(loc)
		return true, m.loadKargoView(false)
	}
	if top.View == model.ViewTree && top.TreeApp != nil {
		if m.treeView == nil || !hasFrame {
			m.treeView = m.newTreeView()
			m.treeNav.Reset()
		}
		ns := ""
		if top.TreeApp.AppNamespace != nil {
			ns = *top.TreeApp.AppNamespace
		}
		if app := m.findAppByNameAndNamespace(top.TreeApp.Name, ns); app != nil {
			m.treeLoading = !hasFrame
			return true, tea.Batch(m.reloadRestoredTree(*app), m.startWatchingResourceTree(*app), m.consumeTreeEvent())
		}
	}
	if top.View == model.ViewTree && top.TreeApp == nil {
		var cmds []tea.Cmd
		for key, selected := range m.state.Selections.SelectedApps {
			if !selected {
				continue
			}
			if app := m.findAppByNameAndNamespace(key, ""); app != nil {
				cmds = append(cmds, m.reloadRestoredTree(*app), m.startWatchingResourceTree(*app))
			}
		}
		return true, tea.Batch(append(cmds, m.consumeTreeEvent())...)
	}
	items := m.getVisibleItemsForCurrentView()
	for i, item := range items {
		if top.FocusKey != "" && navigationKey(item) == top.FocusKey {
			m.state.Navigation.SelectedIdx = i
			break
		}
	}
	m.state.Navigation.SelectedIdx = m.navigationService.ValidateBounds(m.state.Navigation.SelectedIdx, len(items))
	m.listNav.SetItemCount(len(items))
	m.listNav.SetCursor(m.state.Navigation.SelectedIdx)
	if m.allOverview != nil && top.View == model.ViewApps {
		return true, nil
	}
	return true, m.maybeRestartWatchForScope()
}

func (m *Model) reloadRestoredTree(app model.App) tea.Cmd {
	load := m.startLoadingResourceTree(app)
	return func() tea.Msg {
		msg := load()
		if loaded, ok := msg.(model.ResourceTreeLoadedMsg); ok {
			loaded.Background = true
			return loaded
		}
		return msg
	}
}
