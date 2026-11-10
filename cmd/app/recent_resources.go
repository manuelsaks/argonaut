package main

import (
	"encoding/json"
	"maps"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/model"
	"github.com/darksworm/argonaut/pkg/tui/listnav"
)

// Session-only list history is scoped to the Argo context. It stores route and
// cursor state, never credentials, clients, confirmations or pending writes.
type resourceHistory struct{ Contexts map[string][]recentResource }
type recentResource struct {
	Key, Context, Label string
	Argo                model.NavigationState
	List                listnav.ListNavigator
	Kargo               *kargoLocation
	Instance            string
}

func cloneResourceSelections(s model.SelectionState) model.SelectionState {
	return model.SelectionState{ScopeClusters: maps.Clone(s.ScopeClusters), ScopeNamespaces: maps.Clone(s.ScopeNamespaces), ScopeProjects: maps.Clone(s.ScopeProjects), ScopeApplicationSets: maps.Clone(s.ScopeApplicationSets), SelectedApps: maps.Clone(s.SelectedApps)}
}

func (m *Model) recentLocation() (recentResource, bool) {
	r := recentResource{Context: m.currentContextName}
	if m.state.Mode != model.ModeNormal && m.state.Mode != model.ModeCommand {
		return r, false
	}
	if m.state.Navigation.View == model.ViewKargo && m.kargo != nil {
		st := m.kargo
		if st.Form != nil || st.Confirm || st.Executing || st.LoginBusy || st.Detail != nil || st.Kind == "Connection" || len(st.Connections) == 0 {
			return r, false
		}
		loc := m.kargoLocation()
		r.Kargo, r.Instance = &loc, st.Connections[st.Connection].Name
		project := loc.Project
		if loc.Kind == "Project" {
			project = ""
		}
		key, _ := json.Marshal([]string{"kargo", r.Context, r.Instance, loc.Kind, project, loc.Stage, loc.Warehouse})
		r.Key = string(key)
		r.Label = strings.ToLower(loc.Kind)
		if loc.Kind != "Freight" {
			r.Label += "s"
		}
		if project != "" {
			r.Label += "/" + project
		}
		if r.Instance != "cli" {
			r.Label += "@" + r.Instance
		}
		return r, true
	}
	if m.allOverview != nil && m.state.Navigation.View == model.ViewApps {
		return r, false
	}
	labels := map[model.View]string{model.ViewApps: "apps", model.ViewClusters: "servers", model.ViewNamespaces: "namespaces", model.ViewProjects: "projects", model.ViewApplicationSets: "appsets"}
	label, ok := labels[m.state.Navigation.View]
	if !ok {
		return r, false
	}
	r.Argo = m.state.Navigation
	r.Label = label
	if r.Argo.View == model.ViewApps {
		if len(m.state.Selections.ScopeClusters) > 0 {
			r.Label += "/" + m.destinationScopeLabel()
		} else if projects := sortedScopeProjects(m.state.Selections.ScopeProjects); len(projects) > 0 {
			r.Label += "/" + strings.Join(projects, ",")
		}
	}
	r.Argo.SavedSearchQuery, r.Argo.SavedActiveFilter = m.state.UI.SearchQuery, m.state.UI.ActiveFilter
	selections := cloneResourceSelections(m.state.Selections)
	r.Argo.SavedSelections = &selections
	if m.listNav != nil {
		r.List = *m.listNav
	}
	items := m.getVisibleItemsForCurrentView()
	i := m.state.Navigation.SelectedIdx
	if i >= 0 && i < len(items) {
		r.Argo.FocusKey = navigationKey(items[i])
	}
	key, _ := json.Marshal([]any{"argo", r.Context, r.Argo.View, sortedScopeProjects(selections.ScopeClusters), sortedScopeProjects(selections.ScopeNamespaces), sortedScopeProjects(selections.ScopeProjects), sortedScopeProjects(selections.ScopeApplicationSets)})
	r.Key = string(key)
	return r, true
}

func (m *Model) rememberResource(r recentResource) {
	if m.recentResources == nil {
		m.recentResources = &resourceHistory{}
	}
	if m.recentResources.Contexts == nil {
		m.recentResources.Contexts = make(map[string][]recentResource)
	}
	entries := []recentResource{r}
	for _, old := range m.recentResources.Contexts[r.Context] {
		if old.Key != r.Key && len(entries) < 4 {
			entries = append(entries, old)
		}
	}
	m.recentResources.Contexts[r.Context] = entries
}

func (m *Model) removeCurrentRecent() {
	if m.recentResources == nil || m.recentResources.Contexts == nil {
		return
	}
	current, ok := m.recentLocation()
	if !ok {
		return
	}
	entries := m.recentResources.Contexts[current.Context]
	var keep []recentResource
	for _, entry := range entries {
		if entry.Key != current.Key {
			keep = append(keep, entry)
		}
	}
	m.recentResources.Contexts[current.Context] = keep
}

func (m *Model) recentEntries() []recentResource {
	if m.recentResources == nil {
		return nil
	}
	return m.recentResources.Contexts[m.currentContextName]
}

func (m *Model) openRecentResource(index int) (tea.Model, tea.Cmd) {
	entries := m.recentEntries()
	if index < 0 || index >= len(entries) {
		return m, m.showFooterNotice("No recent resource in this slot; use : to open a resource")
	}
	r := entries[index]
	if r.Context != m.currentContextName {
		return m, nil
	}
	if r.Kargo != nil {
		if m.state.Navigation.View != model.ViewKargo {
			m.prepareKargo()
		} else {
			m.saveKargoLocation()
		}
		index := -1
		for i, connection := range m.kargo.Connections {
			if connection.Name == r.Instance {
				index = i
				break
			}
		}
		if index < 0 {
			return m, m.showFooterNotice("Recent Kargo instance is no longer configured")
		}
		if m.kargo.Connection != index {
			m.selectKargoConnection(index)
		}
		loc := *r.Kargo
		loc.Connection = index
		m.restoreKargoLocation(loc)
		return m, m.loadKargoView(false)
	}
	fromKargo := m.state.Navigation.View == model.ViewKargo
	m.saveNavigationState()
	if fromKargo {
		m.cancelKargoRequests()
		m.switchEpoch++
	}
	m.cancelResourceInspection()
	m.cleanupTreeWatchers()
	m.state.Mode, m.state.Diff = model.ModeNormal, nil
	m.state.Navigation = r.Argo
	m.state.Selections = cloneResourceSelections(*r.Argo.SavedSelections)
	m.state.UI.ActiveFilter, m.state.UI.SearchQuery, m.state.UI.Command = r.Argo.SavedActiveFilter, r.Argo.SavedSearchQuery, ""
	m.clearTreeApp()
	nav := r.List
	m.listNav = &nav
	items := m.getVisibleItemsForCurrentView()
	for i, item := range items {
		if navigationKey(item) == r.Argo.FocusKey {
			m.state.Navigation.SelectedIdx = i
			break
		}
	}
	m.state.Navigation.SelectedIdx = m.navigationService.ValidateBounds(m.state.Navigation.SelectedIdx, len(items))
	m.listNav.SetItemCount(len(items))
	m.listNav.SetCursor(m.state.Navigation.SelectedIdx)
	var refresh tea.Cmd
	if fromKargo {
		if m.allContexts != nil {
			refresh = m.loadAllContexts(false)
		} else {
			refresh = m.restartWatchWithScope()
		}
	} else {
		refresh = m.maybeRestartWatchForScope()
	}
	if r.Argo.View == model.ViewClusters {
		return m, tea.Batch(refresh, m.ensureDestinationServers())
	}
	if r.Argo.View == model.ViewApplicationSets {
		return m, tea.Batch(refresh, m.ensureApplicationSets())
	}
	return m, refresh
}

func isRecentNavigationKey(key string) bool {
	return key == "enter" || key == "esc" || key == "r" || key == "c" || key == "a" || (len(key) == 1 && key >= "1" && key <= "4")
}
