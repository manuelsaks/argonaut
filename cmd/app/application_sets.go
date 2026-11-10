package main

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/api"
	appcontext "github.com/darksworm/argonaut/pkg/context"
	"github.com/darksworm/argonaut/pkg/model"
)

const appMetadataTTL = 5 * time.Minute

type applicationSetsState struct {
	Sequence int
	Loading  bool
	Loaded   bool
	At       time.Time
	Error    string
}

type applicationSetsMsg struct {
	SwitchEpoch, Sequence int
	ContextName           string
	Items                 []model.ApplicationSet
	Error                 error
}

func (m *Model) ensureApplicationSets() tea.Cmd {
	if m.allContexts != nil {
		return nil // Included in the bounded aggregate load.
	}
	if st := m.applicationSets; st != nil && (st.Loading || (st.Loaded && m.now().Sub(st.At) < 5*time.Minute)) {
		return nil
	}
	return m.loadApplicationSets()
}

func (m *Model) loadApplicationSets() tea.Cmd {
	if m.applicationSets == nil {
		m.applicationSets = &applicationSetsState{}
	}
	st := m.applicationSets
	st.Sequence++
	st.Loading = true
	epoch, seq, server := m.switchEpoch, st.Sequence, m.state.Server
	return func() tea.Msg {
		msg := applicationSetsMsg{SwitchEpoch: epoch, Sequence: seq}
		if server == nil {
			msg.Error = fmt.Errorf("no Argo CD server configured")
			return msg
		}
		ctx, cancel := appcontext.WithAPITimeout(context.Background())
		defer cancel()
		msg.Items, msg.Error = api.NewApplicationService(server).ListApplicationSets(ctx)
		return msg
	}
}

func (m *Model) handleApplicationSets(msg applicationSetsMsg) (tea.Model, tea.Cmd) {
	if msg.SwitchEpoch != m.switchEpoch {
		return m, nil
	}
	if st := m.allContexts; st != nil {
		if msg.Sequence != st.Sequence || !st.AppSetPending[msg.ContextName] {
			return m, nil
		}
		delete(st.AppSetPending, msg.ContextName)
		var login tea.Cmd
		if msg.Error != nil {
			st.Errors[msg.ContextName+" (appsets)"] = msg.Error.Error()
			if requiresRelogin(msg.Error) {
				var handled bool
				login, handled = m.maybeAutoReloginForContext(msg.ContextName)
				if handled {
					st.Errors[msg.ContextName+" (appsets)"] = "SSO: signing in..."
				}
			}
		} else {
			for i := range msg.Items {
				msg.Items[i].ContextName = msg.ContextName
			}
			st.ApplicationSets[msg.ContextName] = msg.Items
			st.AppSetFetchedAt[msg.ContextName] = m.now()
		}
		m.rebuildAllContextItems()
		m.updateAllContextsStatus()
		if m.allContextLoadsDone() {
			return m, tea.Batch(login, m.nextAllContextsRefresh())
		}
		return m, login
	}
	st := m.applicationSets
	if st == nil || st.Sequence != msg.Sequence || msg.ContextName != "" {
		return m, nil
	}
	st.Loading = false
	if msg.Error != nil {
		st.Error = msg.Error.Error()
		if login, handled := m.autoReloginForError(msg.Error); handled {
			st.Error = "SSO: signing in..."
			return m, login
		}
		return m, nil
	}
	st.Loaded, st.At, st.Error = true, m.now(), ""
	m.state.ApplicationSets = msg.Items
	m.rebuildAppIndex()
	m.state.Navigation.SelectedIdx = m.navigationService.ValidateBounds(m.state.Navigation.SelectedIdx, len(m.getVisibleItems()))
	m.listNav.SetItemCount(len(m.getVisibleItems()))
	m.listNav.SetCursor(m.state.Navigation.SelectedIdx)
	return m, nil
}

func (m *Model) applicationSetListHeading() string {
	if st := m.applicationSets; st != nil {
		if st.Loading {
			return "APPLICATIONSETS (loading...)"
		}
		if st.Error != "" {
			return "ApplicationSets: " + st.Error + " | Ctrl+R: retry"
		}
	}
	return ""
}
