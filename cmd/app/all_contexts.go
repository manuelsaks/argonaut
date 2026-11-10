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
	Sequence     int
	Servers      map[string]*model.Server
	Apps         map[string][]model.App
	Destinations map[string][]model.DestinationServer
	Errors       map[string]string
	Pending      map[string]bool
	Cancel       context.CancelFunc
}

type allContextLoadedMsg struct {
	SwitchEpoch, Sequence   int
	ContextName             string
	Apps                    []model.App
	Destinations            []model.DestinationServer
	Error, DestinationError error
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
	ctx, cancel := context.WithCancel(context.Background())
	st.Cancel = cancel
	limit := make(chan struct{}, 4)
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
		if previous := previousServers[name]; previous != nil && (previous.BaseURL != server.BaseURL || previous.GrpcWebRootPath != server.GrpcWebRootPath) {
			delete(st.Apps, name)
			delete(st.Destinations, name)
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
			if msg.Error != nil {
				return msg
			}
			serverCtx, serverCancel := appcontext.WithAPITimeout(ctx)
			msg.Destinations, msg.DestinationError = service.ListDestinationServers(serverCtx)
			serverCancel()
			return msg
		})
	}
	for name := range st.Apps {
		if st.Servers[name] == nil {
			delete(st.Apps, name)
			delete(st.Destinations, name)
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
	epoch, seq := m.switchEpoch, m.allContexts.Sequence
	return tea.Tick(20*time.Second, func(time.Time) tea.Msg { return allContextsRefreshMsg{epoch, seq} })
}

func (m *Model) handleAllContextLoaded(msg allContextLoadedMsg) (tea.Model, tea.Cmd) {
	st := m.allContexts
	if st == nil || msg.SwitchEpoch != m.switchEpoch || msg.Sequence != st.Sequence || !st.Pending[msg.ContextName] {
		return m, nil
	}
	delete(st.Pending, msg.ContextName)
	var relogin tea.Cmd
	if msg.Error != nil {
		delete(st.Apps, msg.ContextName)
		delete(st.Destinations, msg.ContextName)
		st.Errors[msg.ContextName] = msg.Error.Error()
		if requiresRelogin(msg.Error) {
			relogin, _ = m.maybeAutoReloginForContext(msg.ContextName)
		}
	} else {
		for i := range msg.Apps {
			msg.Apps[i].ContextName = msg.ContextName
		}
		for i := range msg.Destinations {
			msg.Destinations[i].ContextName = msg.ContextName
		}
		st.Apps[msg.ContextName] = msg.Apps
		st.Destinations[msg.ContextName] = msg.Destinations
		if msg.DestinationError != nil {
			st.Errors[msg.ContextName] = "server registry: " + msg.DestinationError.Error()
		}
	}
	m.rebuildAllContextItems()
	m.updateAllContextsStatus()
	if len(st.Pending) == 0 {
		return m, tea.Batch(relogin, m.nextAllContextsRefresh())
	}
	return m, relogin
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
		failures = append(failures, name+": "+inspectionPlainText(err))
	}
	sort.Strings(failures)
	status := fmt.Sprintf("all contexts: %d apps", len(m.state.Apps))
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
	return parent, parent.reloadAllContexts()
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
