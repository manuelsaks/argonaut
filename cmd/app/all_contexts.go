package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/api"
	"github.com/darksworm/argonaut/pkg/config"
	appcontext "github.com/darksworm/argonaut/pkg/context"
	"github.com/darksworm/argonaut/pkg/model"
)

const allContextsChoice = "\x00all-contexts"
const allServersChoice = "all"

type allContextsState struct {
	Sequence             int
	Servers              map[string]*model.Server
	Apps                 map[string][]model.App
	Destinations         map[string][]model.DestinationServer
	DestinationFetchedAt map[string]time.Time
	DestinationPending   map[string]bool
	Errors               map[string]string
	Pending              map[string]bool
	Cancel               context.CancelFunc
}

type allContextLoadedMsg struct {
	SwitchEpoch, Sequence int
	ContextName           string
	Apps                  []model.App
	Error                 error
}

type allContextDestinationsMsg struct {
	SwitchEpoch, Sequence int
	ContextName           string
	Servers               []model.DestinationServer
	Error                 error
}

type allContextsRefreshMsg struct{ SwitchEpoch, Sequence int }

func (m *Model) cancelAllContextRequests() {
	if st := m.allContexts; st != nil {
		if st.Cancel != nil {
			st.Cancel()
			st.Cancel = nil
		}
		st.Sequence++
	}
}

func (m *Model) openAllContexts() (tea.Model, tea.Cmd) {
	m.cancelResourceInspection()
	m.cancelContextRelogin()
	m.cleanupAppWatcher()
	m.cleanupTreeWatchers()
	m.cancelAllContextRequests()
	next := NewModel(m.config)
	next.applyThemeToModel()
	next.program = m.program
	next.ready = true
	next.state.Terminal = m.state.Terminal
	next.argoConfigPath = m.argoConfigPath
	next.state.ContextNames = append([]string(nil), m.state.ContextNames...)
	next.switchEpoch = m.switchEpoch + 1
	next.reloginAttempts = m.reloginAttempts
	next.currentContextName = "all"
	next.state.Mode = model.ModeNormal
	next.state.Navigation.View = model.ViewApps
	next.state.Selections = *model.NewSelectionState()
	next.pendingDefaultViewScope = nil
	next.allContexts = &allContextsState{Apps: map[string][]model.App{}, Destinations: map[string][]model.DestinationServer{}}
	return next, next.reloadAllContexts()
}

func (m *Model) reloadAllContexts() tea.Cmd {
	return m.loadAllContexts(true)
}

func (m *Model) loadAllContexts(force bool) tea.Cmd {
	st := m.allContexts
	if st == nil {
		return nil
	}
	m.cancelAllContextRequests()
	epoch, sequence := m.switchEpoch, st.Sequence
	path := m.argoConfigPath
	if path == "" {
		path = config.GetConfigPath()
	}
	cfg, err := config.ReadCLIConfigFromPath(path)
	if err != nil {
		st.Pending = map[string]bool{}
		st.Errors = map[string]string{"config": err.Error()}
		m.updateAllContextsStatus()
		return tea.Batch(m.showFooterNotice("Cannot load Argo CD contexts: "+err.Error()), m.nextAllContextsRefresh())
	}
	m.state.ContextNames = cfg.GetContextNames()
	previousServers := st.Servers
	st.Servers = map[string]*model.Server{}
	st.Errors = map[string]string{}
	st.Pending = map[string]bool{}
	st.DestinationPending = map[string]bool{}
	if st.DestinationFetchedAt == nil {
		st.DestinationFetchedAt = map[string]time.Time{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	st.Cancel = cancel
	concurrency := 4
	if m.config.IsLowBandwidth() {
		concurrency = 2
	}
	limit := make(chan struct{}, concurrency)
	var commands []tea.Cmd
	for _, name := range m.state.ContextNames {
		if core, _ := cfg.IsContextCore(name); core {
			st.Errors[name] = "core mode uses kubeconfig and is excluded"
			continue
		}
		if forward, _ := cfg.IsContextPortForward(name); forward {
			st.Errors[name] = "port-forward context is excluded"
			continue
		}
		server, err := cfg.ToServerConfigForContext(name)
		if err != nil {
			st.Errors[name] = err.Error()
			continue
		}
		st.Servers[name] = server
		if previous := previousServers[name]; previous != nil && *previous != *server {
			delete(st.Apps, name)
			delete(st.Destinations, name)
			delete(st.DestinationFetchedAt, name)
		}
		st.Pending[name] = true
		commands = append(commands, func() tea.Msg {
			msg := allContextLoadedMsg{SwitchEpoch: epoch, Sequence: sequence, ContextName: name}
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				msg.Error = ctx.Err()
				return msg
			}
			defer func() { <-limit }()
			service := api.NewApplicationService(server)
			appCtx, appCancel := appcontext.WithAPITimeout(ctx)
			msg.Apps, msg.Error = service.ListApplications(appCtx)
			appCancel()
			return msg
		})
		// Registry data changes infrequently. Keep it in memory, and deliver it
		// independently so a slow /clusters never delays rendering Applications.
		if force || m.now().Sub(st.DestinationFetchedAt[name]) >= 5*time.Minute {
			st.DestinationPending[name] = true
			commands = append(commands, func() tea.Msg {
				msg := allContextDestinationsMsg{SwitchEpoch: epoch, Sequence: sequence, ContextName: name}
				select {
				case limit <- struct{}{}:
				case <-ctx.Done():
					msg.Error = ctx.Err()
					return msg
				}
				defer func() { <-limit }()
				serverCtx, serverCancel := appcontext.WithAPITimeout(ctx)
				defer serverCancel()
				msg.Servers, msg.Error = api.NewApplicationService(server).ListDestinationServers(serverCtx)
				return msg
			})
		}
	}
	for name := range st.Apps {
		if st.Servers[name] == nil {
			delete(st.Apps, name)
			delete(st.Destinations, name)
			delete(st.DestinationFetchedAt, name)
		}
	}
	m.rebuildAllContextItems()
	m.updateAllContextsStatus()
	if len(commands) == 0 {
		commands = append(commands, m.nextAllContextsRefresh())
	}
	return tea.Batch(commands...)
}

func (m *Model) nextAllContextsRefresh() tea.Cmd {
	interval := m.config.GetNetworkRefreshInterval()
	if interval <= 0 {
		return nil
	}
	epoch, seq := m.switchEpoch, m.allContexts.Sequence
	return tea.Tick(interval, func(time.Time) tea.Msg { return allContextsRefreshMsg{epoch, seq} })
}

func (m *Model) handleAllContextLoaded(msg allContextLoadedMsg) (tea.Model, tea.Cmd) {
	st := m.allContexts
	if st == nil || msg.SwitchEpoch != m.switchEpoch || msg.Sequence != st.Sequence || !st.Pending[msg.ContextName] {
		return m, nil
	}
	delete(st.Pending, msg.ContextName)
	var relogin tea.Cmd
	if msg.Error != nil {
		// Keep the last snapshot during transient VPN failures; auth failures
		// must clear it so a previous identity's data cannot leak into a session.
		if isAuthenticationError(msg.Error.Error()) {
			delete(st.Apps, msg.ContextName)
			delete(st.Destinations, msg.ContextName)
			delete(st.DestinationFetchedAt, msg.ContextName)
		}
		st.Errors[msg.ContextName] = msg.Error.Error()
		if requiresRelogin(msg.Error) {
			relogin, _ = m.maybeAutoReloginForContext(msg.ContextName)
		}
	} else {
		for i := range msg.Apps {
			msg.Apps[i].ContextName = msg.ContextName
		}
		st.Apps[msg.ContextName] = msg.Apps
	}
	m.rebuildAllContextItems()
	m.updateAllContextsStatus()
	if len(st.Pending) == 0 && len(st.DestinationPending) == 0 {
		return m, tea.Batch(relogin, m.nextAllContextsRefresh())
	}
	return m, relogin
}

func (m *Model) handleAllContextDestinations(msg allContextDestinationsMsg) (tea.Model, tea.Cmd) {
	st := m.allContexts
	if st == nil || msg.SwitchEpoch != m.switchEpoch || msg.Sequence != st.Sequence || !st.DestinationPending[msg.ContextName] {
		return m, nil
	}
	delete(st.DestinationPending, msg.ContextName)
	if msg.Error != nil {
		st.Errors[msg.ContextName+" (servers)"] = "server registry: " + msg.Error.Error()
	} else if st.Errors[msg.ContextName] == "" {
		for i := range msg.Servers {
			msg.Servers[i].ContextName = msg.ContextName
		}
		st.Destinations[msg.ContextName] = msg.Servers
		st.DestinationFetchedAt[msg.ContextName] = m.now()
	}
	m.rebuildAllContextItems()
	m.updateAllContextsStatus()
	if len(st.Pending) == 0 && len(st.DestinationPending) == 0 {
		return m, m.nextAllContextsRefresh()
	}
	return m, nil
}

func (m *Model) rebuildAllContextItems() {
	st := m.allContexts
	selectedKey := ""
	if m.state.Navigation.View == model.ViewApps {
		items := m.getVisibleItems()
		i := m.state.Navigation.SelectedIdx
		if i >= 0 && i < len(items) {
			if app, ok := items[i].(model.App); ok {
				selectedKey = app.Key()
			}
		}
	}
	m.state.Apps = nil
	m.state.DestinationServers = nil
	for _, name := range m.state.ContextNames {
		m.state.Apps = append(m.state.Apps, st.Apps[name]...)
		m.state.DestinationServers = append(m.state.DestinationServers, st.Destinations[name]...)
	}
	m.destinationLoaded = true
	m.rebuildAppIndex()
	if selectedKey != "" {
		for i, item := range m.getVisibleItems() {
			if app, ok := item.(model.App); ok && app.Key() == selectedKey {
				m.state.Navigation.SelectedIdx = i
				break
			}
		}
	}
	m.state.Navigation.SelectedIdx = m.navigationService.ValidateBounds(m.state.Navigation.SelectedIdx, len(m.getVisibleItems()))
	m.listNav.SetItemCount(len(m.getVisibleItems()))
	m.listNav.SetCursor(m.state.Navigation.SelectedIdx)
}

func (m *Model) updateAllContextsStatus() {
	st := m.allContexts
	var failures []string
	for name, err := range st.Errors {
		if len(st.Apps[name]) > 0 {
			err += " (showing last known data)"
		}
		failures = append(failures, name+": "+inspectionPlainText(err))
	}
	sort.Strings(failures)
	status := fmt.Sprintf("all contexts: %d apps", len(m.state.Apps))
	if interval := m.config.GetNetworkRefreshInterval(); interval > 0 {
		status += " | refresh " + interval.String()
	} else {
		status += " | manual refresh"
	}
	if len(st.Pending) > 0 {
		status += fmt.Sprintf(" | loading %d", len(st.Pending))
	}
	if len(failures) > 0 {
		status += " | " + strings.Join(failures, "; ")
	}
	m.statusService.Set(status)
}

// Operations enter an ordinary single-context model before opening their
// confirmation. All existing API call sites retain their isolation guarantees.
func (m *Model) enterOverviewApplication(app model.App, selected []model.App) *Model {
	m.inputComponents.BlurInputs()
	m.inputComponents.ClearCommandInput()
	m.state.UI.Command = ""
	m.state.Mode = model.ModeNormal
	m.cancelResourceInspection()
	m.cancelAllContextRequests()
	child := NewModel(m.config)
	child.applyThemeToModel()
	child.program = m.program
	child.ready = true
	child.state.Terminal = m.state.Terminal
	child.argoConfigPath = m.argoConfigPath
	child.currentContextName = app.ContextName
	child.state.Server = m.allContexts.Servers[app.ContextName]
	child.state.ContextNames = append([]string(nil), m.state.ContextNames...)
	child.switchEpoch = m.switchEpoch + 1
	child.reloginAttempts = m.reloginAttempts
	child.state.Mode = model.ModeNormal
	child.state.Navigation.View = model.ViewApps
	child.state.Selections = *model.NewSelectionState()
	child.pendingDefaultViewScope = nil
	child.state.Apps = append([]model.App(nil), m.allContexts.Apps[app.ContextName]...)
	for i := range child.state.Apps {
		child.state.Apps[i].ContextName = ""
	}
	child.state.DestinationServers = append([]model.DestinationServer(nil), m.allContexts.Destinations[app.ContextName]...)
	for i := range child.state.DestinationServers {
		child.state.DestinationServers[i].ContextName = ""
	}
	child.destinationLoaded = true
	child.destinationFetchedAt = m.allContexts.DestinationFetchedAt[app.ContextName]
	child.rebuildAppIndex()
	child.allOverview = m
	for i, item := range child.getVisibleItems() {
		if item.(model.App).Key() == model.AppKey(app.Name, app.AppNamespace) {
			child.state.Navigation.SelectedIdx = i
			child.listNav.SetCursor(i)
			break
		}
	}
	for _, item := range selected {
		child.state.Selections.SelectedApps[model.AppKey(item.Name, item.AppNamespace)] = true
	}
	return child
}

func (m *Model) returnToAllOverview() (tea.Model, tea.Cmd) {
	parent := m.allOverview
	m.cancelContextRelogin()
	m.cancelResourceInspection()
	m.cleanupAppWatcher()
	m.cleanupTreeWatchers()
	parent.switchEpoch = m.switchEpoch + 1
	parent.state.Mode = model.ModeNormal
	parent.state.Navigation.View = model.ViewApps
	return parent, parent.loadAllContexts(false)
}

func (m *Model) overviewOperationTarget() (model.App, []model.App, error) {
	selected, err := m.selectedApplications()
	if err != nil {
		return model.App{}, nil, err
	}
	if len(selected) == 0 {
		items := m.getVisibleItems()
		i := m.state.Navigation.SelectedIdx
		if i < 0 || i >= len(items) {
			return model.App{}, nil, fmt.Errorf("select an application first")
		}
		app, ok := items[i].(model.App)
		if !ok {
			return model.App{}, nil, fmt.Errorf("select an application first")
		}
		selected = []model.App{app}
	}
	for _, app := range selected {
		if app.ContextName != selected[0].ContextName {
			return model.App{}, nil, fmt.Errorf("select applications from one context for an operation")
		}
	}
	return selected[0], selected, nil
}

func (m *Model) handleOverviewAction(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.allContexts == nil || m.state.Navigation.View != model.ViewApps {
		return m, nil, false
	}
	switch msg.String() {
	case "ctrl+r":
		return m, m.reloadAllContexts(), true
	case "enter", "r", "ctrl+s", "ctrl+d", "ctrl+b", "ctrl+t", "ctrl+k":
		app, selected, err := m.overviewOperationTarget()
		if err != nil {
			return m, m.showFooterNotice(err.Error()), true
		}
		child := m.enterOverviewApplication(app, selected)
		updated, cmd := child.handleNormalModeGlobalKeys(msg)
		return updated, tea.Batch(child.startWatchingApplications(), cmd), true
	}
	return m, nil, false
}

func (m *Model) handleOverviewCommand(command, arg string) (tea.Model, tea.Cmd, bool) {
	if m.allContexts == nil {
		return m, nil, false
	}
	switch command {
	case "refresh", "refresh!":
		if arg == "" {
			return m, m.reloadAllContexts(), true
		}
	case "sync", "delete", "resources", "diff", "rollback", "terminate":
	default:
		return m, nil, false
	}
	var app model.App
	var selected []model.App
	var err error
	if arg != "" {
		found := m.findAppByNameAndNamespace(arg, "")
		if found == nil {
			return m, m.showFooterNotice("Select an application with its context"), true
		}
		app = *found
	} else {
		app, selected, err = m.overviewOperationTarget()
	}
	if err != nil {
		return m, m.showFooterNotice(err.Error()), true
	}
	child := m.enterOverviewApplication(app, selected)
	child.state.Mode = model.ModeCommand
	input := command
	if arg != "" {
		input += " " + model.AppKey(app.Name, app.AppNamespace)
	}
	child.inputComponents.SetCommandValue(input)
	updated, cmd := child.handleEnhancedCommandModeKeys(tea.KeyPressMsg{Code: tea.KeyEnter})
	return updated, tea.Batch(child.startWatchingApplications(), cmd), true
}
