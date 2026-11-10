package main

import (
	"context"
	"fmt"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/api"
	appcontext "github.com/darksworm/argonaut/pkg/context"
	"github.com/darksworm/argonaut/pkg/model"
)

type networkPollDueMsg struct{ Epoch, Sequence int }
type networkSnapshotMsg struct {
	Epoch, Sequence int
	Result          *api.ListApplicationsResult
	Error           error
}

func (m *Model) nextNetworkPoll() tea.Cmd {
	interval := m.config.GetNetworkRefreshInterval()
	if !m.config.IsLowBandwidth() || interval <= 0 || m.state.Server == nil || m.allContexts != nil {
		return nil
	}
	epoch, seq := m.switchEpoch, m.networkPollSeq
	return tea.Tick(interval, func(time.Time) tea.Msg { return networkPollDueMsg{epoch, seq} })
}

func (m *Model) handleNetworkPollDue(msg networkPollDueMsg) (tea.Model, tea.Cmd) {
	if msg.Epoch != m.switchEpoch || msg.Sequence != m.networkPollSeq || !m.config.IsLowBandwidth() || m.state.Server == nil || m.allContexts != nil {
		return m, nil
	}
	if m.networkPollLoading {
		return m, nil
	}
	// Never close or replace an operation/inspection modal during a background
	// refresh. Large object data is fetched only for the currently open tree.
	if m.state.Mode != model.ModeNormal && m.state.Mode != model.ModeSearch && m.state.Mode != model.ModeCommand {
		return m, m.nextNetworkPoll()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.networkPollCancel = cancel
	m.networkPollLoading = true
	server := m.state.Server
	previous := slices.Clone(m.state.Apps)
	metadataFresh := !m.appMetadataAt.IsZero() && m.now().Sub(m.appMetadataAt) < appMetadataTTL
	load := func() tea.Msg {
		defer cancel()
		requestCtx, requestCancel := appcontext.WithAPITimeout(ctx)
		defer requestCancel()
		service := api.NewApplicationService(server)
		var result *api.ListApplicationsResult
		var err error
		if metadataFresh {
			result, err = service.ListApplicationsWithCachedOwners(requestCtx, previous)
		} else {
			result, err = service.ListApplicationsWithMeta(requestCtx)
		}
		return networkSnapshotMsg{Epoch: msg.Epoch, Sequence: msg.Sequence, Result: result, Error: err}
	}
	return m, load
}

func (m *Model) handleNetworkSnapshot(msg networkSnapshotMsg) (tea.Model, tea.Cmd) {
	if msg.Epoch != m.switchEpoch || msg.Sequence != m.networkPollSeq || !m.networkPollLoading || !m.config.IsLowBandwidth() || m.allContexts != nil {
		return m, nil
	}
	m.networkPollCancel = nil
	m.networkPollLoading = false
	if msg.Error != nil {
		if requiresRelogin(msg.Error) {
			if cmd, handled := m.maybeAutoRelogin(); handled {
				return m, cmd
			}
		}
		return m, tea.Batch(m.showFooterNotice("Background refresh failed; showing last known data: "+msg.Error.Error()), m.nextNetworkPoll())
	}
	if msg.Result == nil {
		return m, m.nextNetworkPoll()
	}
	m.markContextAuthenticated(m.currentContextName)
	selected := ""
	if m.state.Navigation.View == model.ViewApps {
		items := m.getVisibleItems()
		if i := m.state.Navigation.SelectedIdx; i >= 0 && i < len(items) {
			selected = items[i].(model.App).Key()
		}
	}
	m.state.Apps = msg.Result.Apps
	if msg.Result.FullMetadata {
		m.appMetadataAt = m.now()
	}
	m.lastResourceVersion = msg.Result.ResourceVersion
	m.rebuildAppIndex()
	items := m.getVisibleItems()
	if selected != "" {
		for i, item := range items {
			if app, ok := item.(model.App); ok && app.Key() == selected {
				m.state.Navigation.SelectedIdx = i
				break
			}
		}
	}
	m.state.Navigation.SelectedIdx = m.navigationService.ValidateBounds(m.state.Navigation.SelectedIdx, len(items))
	m.listNav.SetItemCount(len(items))
	m.listNav.SetCursor(m.state.Navigation.SelectedIdx)
	m.statusService.Set(fmt.Sprintf("Updated %s | low bandwidth | refresh %s", m.now().Format("15:04:05"), m.config.GetNetworkRefreshInterval()))
	return m, tea.Batch(m.ensureDestinationServers(), m.ensureApplicationSets(), m.nextNetworkPoll())
}

// An explicit refresh replaces the pending poll instead of starting another
// loop. It also works when periodic refresh is disabled.
func (m *Model) refreshNetworkSnapshot() tea.Cmd {
	if !m.config.IsLowBandwidth() {
		return nil
	}
	m.cleanupAppWatcher()
	m.appMetadataAt = time.Time{} // Explicit refresh also reloads ownership.
	_, cmd := m.handleNetworkPollDue(networkPollDueMsg{m.switchEpoch, m.networkPollSeq})
	return cmd
}

type networkTreeDueMsg struct {
	Epoch, Session int
	App            model.App
}
type networkTreeSnapshotMsg struct {
	Due    networkTreeDueMsg
	Result tea.Msg
}

func (m *Model) nextNetworkTreePoll(app model.App) tea.Cmd {
	interval := m.config.GetNetworkRefreshInterval()
	if interval <= 0 {
		return nil
	}
	due := networkTreeDueMsg{m.switchEpoch, m.treeSession, app}
	return tea.Tick(interval, func(time.Time) tea.Msg { return due })
}

func (m *Model) handleNetworkTreeDue(msg networkTreeDueMsg) (tea.Model, tea.Cmd) {
	if msg.Epoch != m.switchEpoch || msg.Session != m.treeSession || m.state.Navigation.View != model.ViewTree || !m.config.IsLowBandwidth() {
		return m, nil
	}
	if m.state.Mode != model.ModeNormal {
		return m, m.nextNetworkTreePoll(msg.App)
	}
	namespace := ""
	if msg.App.AppNamespace != nil {
		namespace = *msg.App.AppNamespace
	}
	if app := m.findAppByNameAndNamespace(msg.App.Name, namespace); app != nil {
		msg.App = *app
	}
	load := m.startLoadingResourceTree(msg.App)
	return m, func() tea.Msg { return networkTreeSnapshotMsg{Due: msg, Result: load()} }
}

func (m *Model) handleNetworkTreeSnapshot(msg networkTreeSnapshotMsg) (tea.Model, tea.Cmd) {
	if msg.Due.Epoch != m.switchEpoch || msg.Due.Session != m.treeSession || m.state.Navigation.View != model.ViewTree || !m.config.IsLowBandwidth() {
		return m, nil
	}
	var notice tea.Cmd
	switch result := msg.Result.(type) {
	case model.ResourceTreeLoadedMsg:
		result.Background = true
		_, notice = m.Update(result)
	case treeLoadFailedMsg:
		notice = m.showFooterNotice("Tree refresh failed; showing last known data: " + result.message)
	}
	return m, tea.Batch(notice, m.nextNetworkTreePoll(msg.Due.App))
}
