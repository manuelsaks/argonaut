package main

import (
	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/kargo"
	"github.com/darksworm/argonaut/pkg/model"
)

type kargoLocation struct {
	Connection                                     int
	Kind, Project, Stage, Warehouse, Filter, Focus string
	Items                                          []kargo.Resource
	Cursor                                         int
	Detail                                         *kargo.Resource
	DetailKind                                     string
	Diff                                           *model.DiffState
}

func (m *Model) kargoLocation() kargoLocation {
	st := m.kargo
	loc := kargoLocation{Connection: st.Connection, Kind: st.Kind, Project: st.Project, Stage: st.Stage,
		Warehouse: st.Warehouse, Filter: st.Filter, Items: st.Items, Cursor: st.Cursor,
		Detail: st.Detail, DetailKind: st.DetailKind, Diff: m.state.Diff}
	rows := m.kargoRows()
	if st.Cursor >= 0 && st.Cursor < len(rows) {
		loc.Focus = rows[st.Cursor].Columns[0]
	}
	return loc
}

func (m *Model) saveKargoLocation() { m.kargo.History = append(m.kargo.History, m.kargoLocation()) }

func (m *Model) restoreKargoLocation(loc kargoLocation) {
	st := m.kargo
	m.cancelKargoLoad()
	if st.Connection != loc.Connection {
		st.Client, st.SessionIdentity = nil, ""
	}
	st.Connection, st.Kind, st.Project, st.Stage, st.Warehouse = loc.Connection, loc.Kind, loc.Project, loc.Stage, loc.Warehouse
	st.Filter, st.Items, st.Cursor = loc.Filter, loc.Items, loc.Cursor
	st.Detail, st.DetailKind, m.state.Diff = loc.Detail, loc.DetailKind, loc.Diff
	st.Error = ""
	for i, row := range m.kargoRows() {
		if row.Columns[0] == loc.Focus {
			st.Cursor = i
			break
		}
	}
	m.clampKargoCursor()
	m.state.Mode = model.ModeNormal
	if loc.Diff != nil {
		m.state.Mode = model.ModeDiff
	}
}

func (m *Model) backKargo() (tea.Model, tea.Cmd) {
	st := m.kargo
	if n := len(st.History); n > 0 {
		loc := st.History[n-1]
		st.History = st.History[:n-1]
		m.restoreKargoLocation(loc)
		return m, m.loadKargoView(false)
	}
	return m.leaveKargo()
}
