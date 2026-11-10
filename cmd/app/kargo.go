package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/config"
	appcontext "github.com/darksworm/argonaut/pkg/context"
	"github.com/darksworm/argonaut/pkg/kargo"
	"github.com/darksworm/argonaut/pkg/model"
)

type kargoTarget struct{ Connection, Kind, Project, Stage, Warehouse, Name string }
type kargoCacheEntry struct {
	Items []kargo.Resource
	At    time.Time
}
type kargoPromotion struct{ Connection, Server, Project, Stage, Freight string }
type kargoUIState struct {
	Connections                             []kargo.Connection
	Connection                              int
	Client                                  *kargo.Client
	SessionIdentity                         string
	Kind, Project, Stage, Warehouse, Filter string
	Items                                   []kargo.Resource
	Cursor                                  int
	Cache                                   map[kargoTarget]kargoCacheEntry
	Detail                                  *kargo.Resource
	DetailKind                              string
	Draft                                   *kargoPromotion
	Confirm, Executing                      bool
	ConfirmOffset                           int
	Loading                                 bool
	Error                                   string
	Updated                                 time.Time
	Sequence                                int
	Cancel                                  context.CancelFunc
	LoginSequence                           int
	LoginBusy                               bool
	LoginCancel                             context.CancelFunc
	LoginAttempts                           map[string]time.Time
}

type kargoLoadedMsg struct {
	SessionIdentity string
	Epoch, Sequence int
	Target          kargoTarget
	Client          *kargo.Client
	Items           []kargo.Resource
	Object          *kargo.Resource
	Error           error
}
type kargoRefreshMsg struct {
	Epoch, Sequence int
	Target          kargoTarget
}
type kargoPromotedMsg struct {
	Epoch, Sequence int
	Target          kargoPromotion
	Promotion       kargo.Resource
	Error           error
}
type kargoLoginMsg struct {
	Epoch, Sequence int
	Connection      string
	Error           error
}

func (m *Model) kargoConnections() []kargo.Connection {
	connections := map[string]config.KargoConnection{"cli": {ConfigHome: kargo.DefaultHome()}}
	for name, connection := range m.config.Kargo.Connections {
		connections[name] = connection
	}
	var names []string
	for name := range connections {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]kargo.Connection, 0, len(names))
	for _, name := range names {
		cfg := connections[name]
		home := cfg.ConfigHome
		if home == "" && cfg.Server != "" {
			hash := sha256.Sum256([]byte(name + "\x00" + cfg.Server))
			home = filepath.Join(filepath.Dir(config.GetArgonautConfigPath()), "kargo-sessions", fmt.Sprintf("%x", hash[:12]))
		}
		if home == "" {
			home = kargo.DefaultHome()
		}
		connection := kargo.Connection{Name: name, Server: cfg.Server, Home: home, Command: m.config.GetKargoCommand(), CallbackPort: cfg.CallbackPort, AutoRelogin: cfg.AutoRelogin}
		if cfg.Command != "" {
			connection.Command = cfg.Command
		}
		if connection.Server == "" {
			credentials, _ := connection.Credentials()
			connection.Server = credentials.APIAddress
		}
		result = append(result, connection)
	}
	return result
}

func (m *Model) prepareKargo() {
	m.cancelAllContextRequests()
	m.cancelContextRelogin()
	m.cancelResourceInspection()
	m.cleanupAppWatcher()
	m.watchChan = nil
	m.cleanupTreeWatchers()
	m.switchEpoch++
	if m.kargo == nil {
		m.kargo = &kargoUIState{Kind: "Project", Cache: map[kargoTarget]kargoCacheEntry{}, LoginAttempts: map[string]time.Time{}}
	}
	name := ""
	if len(m.kargo.Connections) > m.kargo.Connection {
		name = m.kargo.Connections[m.kargo.Connection].Name
	}
	m.kargo.Connections = m.kargoConnections()
	for i, connection := range m.kargo.Connections {
		if connection.Name == name {
			m.kargo.Connection = i
		}
	}
	m.kargo.Connection = min(m.kargo.Connection, len(m.kargo.Connections)-1)
	m.kargo.Detail, m.kargo.DetailKind, m.kargo.Draft, m.kargo.Confirm = nil, "", nil, false
	m.state.Navigation.View = model.ViewKargo
	m.state.Mode = model.ModeNormal
	m.state.Diff = nil
	m.state.Modals.InitialLoading = false
	m.inputComponents.BlurInputs()
	m.inputComponents.ClearCommandInput()
}

func (m *Model) openKargo() (tea.Model, tea.Cmd) {
	m.prepareKargo()
	return m, m.loadKargoView(false)
}

func (m *Model) leaveKargo() (tea.Model, tea.Cmd) {
	if m.kargo.Executing {
		return m, m.showFooterNotice("A promotion request is in flight; wait for its result")
	}
	m.cancelKargoRequests()
	m.switchEpoch++
	m.state.Mode = model.ModeNormal
	m.state.Diff = nil
	m.state.Navigation.View = model.ViewApps
	m.state.Navigation.SelectedIdx = 0
	m.listNav.Reset()
	if m.allContexts != nil {
		return m, m.loadAllContexts(false)
	}
	return m, m.startLoadingApplications()
}

func (m *Model) cancelKargoLoad() {
	if st := m.kargo; st != nil {
		if st.Cancel != nil {
			st.Cancel()
			st.Cancel = nil
		}
		st.Sequence++
		st.Loading = false
	}
}

func (m *Model) cancelKargoRequests() {
	m.cancelKargoLoad()
	if st := m.kargo; st != nil {
		if st.LoginCancel != nil {
			st.LoginCancel()
			st.LoginCancel = nil
		}
		st.LoginBusy = false
		st.LoginSequence++
	}
}

func (m *Model) kargoTarget() kargoTarget {
	st := m.kargo
	target := kargoTarget{Connection: st.Connections[st.Connection].Name, Kind: st.Kind, Project: st.Project, Stage: st.Stage, Warehouse: st.Warehouse}
	if st.Kind == "Project" {
		target.Project, target.Stage, target.Warehouse = "", "", ""
	}
	if st.Detail != nil {
		target.Kind, target.Project, target.Name = st.Detail.Kind, st.Detail.Project, st.Detail.Name
	}
	return target
}

func (m *Model) loadKargoView(force bool) tea.Cmd {
	st := m.kargo
	if st == nil || st.Kind == "Connection" || st.Confirm || st.Executing || st.LoginBusy || st.DetailKind == "Help" {
		return nil
	}
	m.cancelKargoLoad()
	connection := st.Connections[st.Connection]
	identity := connection.SessionIdentity()
	if identity == "" || identity != st.SessionIdentity {
		st.Cache, st.Client, st.Items = map[kargoTarget]kargoCacheEntry{}, nil, nil
		st.SessionIdentity = identity
		if m.state.Diff != nil && st.Detail != nil {
			m.state.Diff.Content = []string{"Session changed; loading current data..."}
		}
	}
	target := m.kargoTarget()
	interval := m.config.GetKargoRefreshInterval()
	ttl := interval
	if target.Kind == "Project" {
		ttl = 5 * time.Minute
	}
	if cached, ok := st.Cache[target]; ok && !force && target.Name == "" && ttl > 0 && m.now().Sub(cached.At) < ttl {
		st.Items, st.Updated, st.Error = cached.Items, cached.At, ""
		m.clampKargoCursor()
		return m.nextKargoRefresh()
	}
	epoch, seq := m.switchEpoch, st.Sequence
	client := st.Client
	ctx, cancel := context.WithCancel(context.Background())
	st.Cancel, st.Loading = cancel, true
	return func() tea.Msg {
		defer cancel()
		msg := kargoLoadedMsg{Epoch: epoch, Sequence: seq, Target: target, Client: client, SessionIdentity: identity}
		if client == nil {
			msg.Client, msg.Error = kargo.NewClient(connection)
		}
		if msg.Error != nil {
			return msg
		}
		requestCtx, requestCancel := appcontext.WithAPITimeout(ctx)
		defer requestCancel()
		if target.Name != "" {
			object, err := msg.Client.Get(requestCtx, target.Kind, target.Project, target.Name)
			msg.Object, msg.Error = &object, err
		} else {
			msg.Items, msg.Error = msg.Client.List(requestCtx, target.Kind, target.Project, target.Stage, target.Warehouse)
		}
		return msg
	}
}

func (m *Model) nextKargoRefresh() tea.Cmd {
	st := m.kargo
	interval := m.config.GetKargoRefreshInterval()
	if interval <= 0 || st.Kind == "Connection" || st.Confirm || st.Executing || st.DetailKind == "Help" {
		return nil
	}
	if st.Detail != nil {
		if st.Detail.Kind != "Promotion" || st.DetailKind == "YAML" {
			return nil
		}
		switch st.Detail.Phase() {
		case "Succeeded", "Failed", "Errored", "Aborted":
			return nil
		}
	}
	if st.Kind == "Project" && st.Detail == nil {
		interval = max(interval, 5*time.Minute)
	}
	msg := kargoRefreshMsg{m.switchEpoch, st.Sequence, m.kargoTarget()}
	return tea.Tick(interval, func(time.Time) tea.Msg { return msg })
}

func (m *Model) handleKargoLoaded(msg kargoLoadedMsg) (tea.Model, tea.Cmd) {
	st := m.kargo
	if st == nil || m.state.Navigation.View != model.ViewKargo || msg.Epoch != m.switchEpoch || msg.Sequence != st.Sequence || msg.Target != m.kargoTarget() {
		return m, nil
	}
	if msg.SessionIdentity != "" && msg.SessionIdentity != st.Connections[st.Connection].SessionIdentity() {
		st.Cache, st.Client, st.Items = map[kargoTarget]kargoCacheEntry{}, nil, nil
		if st.Detail != nil {
			st.Detail = &kargo.Resource{Kind: st.Detail.Kind, Name: st.Detail.Name, Project: st.Detail.Project}
			if m.state.Diff != nil {
				m.state.Diff.Content = []string{"Session changed; reloading..."}
			}
		}
		return m, m.loadKargoView(true)
	}
	st.Loading, st.Cancel, st.Error = false, nil, ""
	if msg.Error != nil {
		st.Error = inspectionPlainText(msg.Error.Error())
		if m.state.Diff != nil && st.Detail != nil {
			m.state.Diff.Title = st.Detail.Kind + " / " + st.Detail.Name + " [refresh failed; Esc, Ctrl+E]"
		}
		connection := st.Connections[st.Connection]
		if kargo.NeedsLogin(msg.Error) && connection.AutoRelogin && m.now().Sub(st.LoginAttempts[connection.Name]) >= time.Minute {
			return m, m.beginKargoLogin()
		}
		if kargo.NeedsLogin(msg.Error) || kargo.IsForbidden(msg.Error) {
			return m, nil
		}
		return m, m.nextKargoRefresh()
	}
	st.Client, st.Updated = msg.Client, m.now()
	st.SessionIdentity = st.Connections[st.Connection].SessionIdentity()
	if msg.Object != nil {
		st.Detail = msg.Object
		m.showKargoDocument(*msg.Object, st.DetailKind, true)
	} else {
		key := ""
		rows := m.kargoRows()
		if st.Cursor >= 0 && st.Cursor < len(rows) {
			key = rows[st.Cursor].Resource.Name
		}
		st.Items = msg.Items
		st.Cache[msg.Target] = kargoCacheEntry{msg.Items, st.Updated}
		for i, row := range m.kargoRows() {
			if row.Resource.Name == key {
				st.Cursor = i
				break
			}
		}
		m.clampKargoCursor()
	}
	return m, m.nextKargoRefresh()
}

func (m *Model) kargoNavigate(kind string) tea.Cmd {
	m.setKargoView(kind)
	return m.loadKargoView(false)
}

func (m *Model) setKargoView(kind string) {
	st := m.kargo
	m.cancelKargoLoad()
	st.Kind, st.Stage, st.Warehouse, st.Filter, st.Error = kind, "", "", "", ""
	st.Items, st.Detail, st.DetailKind = nil, nil, ""
	st.Cursor, st.Confirm = 0, false
	st.ConfirmOffset = 0
	m.state.Mode, m.state.Diff = model.ModeNormal, nil
	if kind != "Project" && kind != "Connection" && st.Project == "" {
		st.Kind = "Project"
		st.Error = "Choose a Project first"
	}
}

func (m *Model) beginKargoLogin() tea.Cmd {
	st := m.kargo
	if st.LoginBusy || st.Executing {
		return nil
	}
	m.cancelKargoLoad()
	connection := st.Connections[st.Connection]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	command, err := connection.LoginCommand(ctx)
	if err != nil {
		cancel()
		st.Error = err.Error()
		return nil
	}
	if command.Err != nil {
		cancel()
		st.Error = "Kargo CLI executable not found; install kargo or set [kargo] command"
		return nil
	}
	st.LoginSequence++
	epoch, seq := m.switchEpoch, st.LoginSequence
	st.LoginBusy, st.LoginCancel = true, cancel
	st.LoginAttempts[connection.Name] = m.now()
	return tea.ExecProcess(command, func(err error) tea.Msg {
		defer cancel()
		msg := kargoLoginMsg{Epoch: epoch, Sequence: seq, Connection: connection.Name}
		if err != nil {
			msg.Error = fmt.Errorf("Kargo SSO login did not complete; Ctrl+L retries")
		} else {
			_, msg.Error = connection.Credentials()
		}
		return msg
	})
}

func (m *Model) handleKargoLogin(msg kargoLoginMsg) (tea.Model, tea.Cmd) {
	st := m.kargo
	if st == nil || m.state.Navigation.View != model.ViewKargo || msg.Epoch != m.switchEpoch || msg.Sequence != st.LoginSequence || msg.Connection != st.Connections[st.Connection].Name {
		return m, nil
	}
	st.LoginBusy, st.LoginCancel = false, nil
	if msg.Error != nil {
		st.Error = msg.Error.Error()
		return m, nil
	}
	// A write that failed before login is never replayed. New promotion actions
	// always require a fresh user confirmation after authentication.
	st.Client, st.Draft, st.Confirm = nil, nil, false
	st.Cache = map[kargoTarget]kargoCacheEntry{}
	return m, m.loadKargoView(true)
}

func (m *Model) toggleKargoAutoLogin() tea.Cmd {
	st := m.kargo
	index := st.Connection
	if st.Kind == "Connection" {
		rows := m.kargoRows()
		if len(rows) == 0 {
			return nil
		}
		index = rows[st.Cursor].ConnectionIndex
	}
	connection := st.Connections[index]
	next := *m.config
	next.Kargo.Connections = maps.Clone(m.config.Kargo.Connections)
	if next.Kargo.Connections == nil {
		next.Kargo.Connections = map[string]config.KargoConnection{}
	}
	next.Kargo.Connections[connection.Name] = config.KargoConnection{Server: connection.Server, ConfigHome: connection.Home, CallbackPort: connection.CallbackPort, AutoRelogin: !connection.AutoRelogin, Command: next.Kargo.Connections[connection.Name].Command}
	if err := config.SaveArgonautConfig(&next); err != nil {
		return m.showFooterNotice("Cannot save Kargo auto-login preference")
	}
	m.config = &next
	st.Connections[index].AutoRelogin = !connection.AutoRelogin
	return nil
}

func (m *Model) executeKargoPromotion() tea.Cmd {
	st := m.kargo
	if st.Draft == nil || !st.Confirm || st.Executing || st.Client == nil {
		return nil
	}
	target := *st.Draft
	connection := st.Connections[st.Connection]
	if target.Connection != connection.Name || target.Server != st.Client.Connection.Server || target.Project != st.Project {
		st.Error = "Promotion target changed; select the stage and Freight again"
		st.Draft, st.Confirm = nil, false
		return nil
	}
	m.cancelKargoLoad()
	st.Executing = true
	epoch, seq, client := m.switchEpoch, st.Sequence, st.Client
	ctx, cancel := appcontext.WithAPITimeout(context.Background())
	st.Cancel = cancel
	return func() tea.Msg {
		defer cancel()
		promotion, err := client.Promote(ctx, target.Project, target.Stage, target.Freight)
		return kargoPromotedMsg{epoch, seq, target, promotion, err}
	}
}

func (m *Model) handleKargoPromoted(msg kargoPromotedMsg) (tea.Model, tea.Cmd) {
	st := m.kargo
	if st == nil || m.state.Navigation.View != model.ViewKargo || msg.Epoch != m.switchEpoch || msg.Sequence != st.Sequence || st.Draft == nil || msg.Target != *st.Draft {
		return m, nil
	}
	st.Executing, st.Confirm, st.Cancel, st.Draft = false, false, nil, nil
	st.Cache = map[kargoTarget]kargoCacheEntry{}
	if msg.Error != nil {
		st.Error = inspectionPlainText(msg.Error.Error())
		connection := st.Connections[st.Connection]
		if kargo.NeedsLogin(msg.Error) && connection.AutoRelogin && m.now().Sub(st.LoginAttempts[connection.Name]) >= time.Minute {
			return m, m.beginKargoLogin()
		}
		return m, nil
	}
	st.Kind, st.Stage, st.Warehouse, st.Error = "Promotion", msg.Target.Stage, "", ""
	st.Items = []kargo.Resource{msg.Promotion}
	m.showKargoDocument(msg.Promotion, "Describe", false)
	if st.Client != nil && st.Client.UsesConnect() {
		return m, m.loadKargoView(true)
	}
	return m, m.nextKargoRefresh()
}
