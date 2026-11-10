package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/api"
	appcontext "github.com/darksworm/argonaut/pkg/context"
	"github.com/darksworm/argonaut/pkg/model"
)

type destinationServersMsg struct {
	SwitchEpoch, Sequence int
	Servers               []model.DestinationServer
	Error                 error
}

func (m *Model) ensureDestinationServers() tea.Cmd {
	if m.destinationLoading || (m.destinationLoaded && m.now().Sub(m.destinationFetchedAt) < 5*time.Minute) {
		return nil
	}
	return m.loadDestinationServers()
}

func (m *Model) loadDestinationServers() tea.Cmd {
	epoch, server := m.switchEpoch, m.state.Server
	m.destinationSeq++
	seq := m.destinationSeq
	m.destinationLoading = true
	return func() tea.Msg {
		msg := destinationServersMsg{SwitchEpoch: epoch, Sequence: seq}
		if server == nil {
			msg.Error = fmt.Errorf("no Argo CD server configured")
			return msg
		}
		ctx, cancel := appcontext.WithAPITimeout(context.Background())
		defer cancel()
		msg.Servers, msg.Error = api.NewApplicationService(server).ListDestinationServers(ctx)
		return msg
	}
}

func (m *Model) handleDestinationServers(msg destinationServersMsg) (tea.Model, tea.Cmd) {
	if msg.SwitchEpoch != m.switchEpoch || msg.Sequence != m.destinationSeq {
		return m, nil
	}
	m.destinationLoading = false
	m.destinationError = ""
	if msg.Error != nil {
		m.destinationError = msg.Error.Error()
		return m, nil
	}
	m.destinationLoaded = true
	m.destinationFetchedAt = m.now()
	m.state.DestinationServers = msg.Servers
	sort.Slice(m.state.DestinationServers, func(i, j int) bool {
		if m.state.DestinationServers[i].Name != m.state.DestinationServers[j].Name {
			return m.state.DestinationServers[i].Name < m.state.DestinationServers[j].Name
		}
		return m.state.DestinationServers[i].Key() < m.state.DestinationServers[j].Key()
	})
	m.rebuildAppIndex()
	m.state.Navigation.SelectedIdx = m.navigationService.ValidateBounds(m.state.Navigation.SelectedIdx, len(m.getVisibleItems()))
	return m, nil
}

func (m *Model) rebuildAppIndex() {
	m.state.Index = model.BuildAppIndex(m.state.Apps)
	// Keep existing scope aliases working; server rows use exact URL/project keys.
	for _, server := range m.state.DestinationServers {
		m.state.Index.ByCluster[server.Key()] = nil
	}
	for i, app := range m.state.Apps {
		if server, ok := model.ResolveDestination(app, m.state.DestinationServers); ok {
			key := server.Key()
			m.state.Index.ByCluster[key] = append(m.state.Index.ByCluster[key], i)
		}
	}
}

func (m *Model) destinationByKey(key string) (model.DestinationServer, bool) {
	for _, server := range m.state.DestinationServers {
		if server.Key() == key {
			return server, true
		}
	}
	return model.DestinationServer{}, false
}

func (m *Model) destinationScopeKey(input string) (string, error) {
	if input == allServersChoice {
		return input, nil
	}
	if _, ok := m.destinationByKey(input); ok {
		return input, nil
	}
	var matches []string
	for _, server := range m.state.DestinationServers {
		if strings.EqualFold(input, server.Name) {
			matches = append(matches, server.Key())
		}
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("server name %q is ambiguous; choose its address/project", input)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return input, nil // Existing app-derived scope alias.
}

func (m *Model) applicationSetScopeKey(input string) (string, error) {
	if m.state.Index == nil {
		m.rebuildAppIndex()
	}
	var matches []string
	for _, key := range m.state.Index.ApplicationSets {
		_, localKey := model.SplitContextKey(key)
		name, _ := model.SplitAppKey(localKey)
		if strings.EqualFold(key, input) {
			return key, nil
		}
		if strings.EqualFold(name, input) || strings.EqualFold(localKey, input) {
			matches = append(matches, key)
		}
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ApplicationSet %q is ambiguous; use namespace/name", input)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return input, nil
}

func (m *Model) applicationLocation(app model.App) string {
	location := app.DestinationName
	if server, ok := model.ResolveDestination(app, m.state.DestinationServers); ok {
		location = server.Name
		if location == "" {
			location = server.Server
		}
	}
	if location == "" {
		location = app.DestinationServer
	}
	if location == "" && app.ClusterLabel != nil {
		location = *app.ClusterLabel
	}
	if location == "" {
		location = "?"
	}
	if app.Namespace != nil && *app.Namespace != "" {
		location += "/" + *app.Namespace
	}
	return location
}

func applicationOwner(app model.App) string {
	if app.ApplicationSet == nil || *app.ApplicationSet == "" {
		return "standalone"
	}
	return "appset:" + *app.ApplicationSet
}

func (m *Model) refreshCurrentView() (tea.Model, tea.Cmd) {
	if m.allContexts != nil {
		return m, m.reloadAllContexts()
	}
	if m.state.Navigation.View == model.ViewClusters {
		return m, m.loadDestinationServers()
	}
	return m.handleRefreshCommand("", false)
}

func (m *Model) destinationScopeLabel() string {
	if len(m.state.Selections.ScopeClusters) == 0 {
		return "all"
	}
	var names []string
	for key, selected := range m.state.Selections.ScopeClusters {
		if !selected {
			continue
		}
		if server, ok := m.destinationByKey(key); ok && server.Name != "" {
			names = append(names, server.Name)
		} else {
			names = append(names, key)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func (m *Model) destinationListHeading() string {
	if m.destinationLoading {
		return "DESTINATION SERVERS (loading...)"
	}
	if m.destinationError != "" {
		return "Servers: " + m.destinationError + " | Ctrl+R: retry (showing last known / app destinations)"
	}
	if !m.destinationLoaded {
		return "APP DESTINATIONS (registry not loaded; Ctrl+R to load)"
	}
	return "NAME"
}

func (m *Model) appSetLocation(key string) (int, string) {
	locations := map[string]bool{}
	count := 0
	if m.state.Index != nil {
		for _, i := range m.state.Index.ByApplicationSet[key] {
			count++
			locations[m.applicationLocation(m.state.Apps[i])] = true
		}
	}
	values := make([]string, 0, len(locations))
	for value := range locations {
		values = append(values, value)
	}
	sort.Strings(values)
	return count, strings.Join(values, ", ")
}
