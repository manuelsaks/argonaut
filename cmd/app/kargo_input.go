package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/kargo"
	"github.com/darksworm/argonaut/pkg/model"
)

type kargoRow struct {
	Resource        kargo.Resource
	ConnectionIndex int
	Columns         []string
}

func (m *Model) kargoRows() []kargoRow {
	st := m.kargo
	var rows []kargoRow
	filter := strings.ToLower(st.Filter)
	if st.Kind == "Connection" {
		for i, connection := range st.Connections {
			login := "manual SSO"
			if connection.AutoRelogin {
				login = "auto SSO"
			}
			columns := []string{connection.Name, login, connection.Server}
			if filter == "" || strings.Contains(strings.ToLower(strings.Join(columns, " ")), filter) {
				rows = append(rows, kargoRow{ConnectionIndex: i, Columns: columns})
			}
		}
		return rows
	}
	for _, resource := range st.Items {
		columns := []string{resource.Name, resource.Phase()}
		switch st.Kind {
		case "Project":
			columns = append(columns, resource.Message())
		case "Stage":
			columns = append(columns, resource.String("status.freightSummary", "status.freight.name"), resource.String("status.currentPromotion.name", "status.lastPromotion.name"), resource.Message())
		case "Promotion":
			columns = append(columns, resource.String("spec.stage"), kargoPromotionRevision(resource), kargoPromotionCreated(resource), kargoPromotionDuration(resource, m.now()), resource.String("spec.freight", "status.freight.name"), resource.Message())
		case "Warehouse":
			columns = append(columns, resource.String("status.lastFreightID"), kargoSources(resource), resource.Message())
		case "Freight":
			columns = []string{resource.Name, kargoFreightAlias(resource), resource.String("origin.name"), kargoSources(resource)}
		}
		if filter == "" || strings.Contains(strings.ToLower(strings.Join(columns, " ")), filter) {
			rows = append(rows, kargoRow{Resource: resource, Columns: columns})
		}
	}
	return rows
}

func (m *Model) clampKargoCursor() {
	m.kargo.Cursor = max(0, min(m.kargo.Cursor, len(m.kargoRows())-1))
}

func (m *Model) handleKargoPaste(text string) (tea.Model, tea.Cmd) {
	if st := m.kargo; st.Form != nil {
		if st.Form.Focus == 0 && st.Form.Kind != "" && !st.Loading && !st.Executing {
			st.Form.Query += kargoSingleLine(text)
			st.Form.Cursor = 0
			m.chooseKargoFormItem()
		}
		return m, nil
	}
	switch m.state.Mode {
	case model.ModeSearch:
		m.inputComponents.SetSearchValue(m.inputComponents.GetSearchValue() + text)
		if m.state.Diff != nil {
			m.state.Diff.SearchQuery = m.inputComponents.GetSearchValue()
			m.findInspectionMatch(false)
		} else {
			m.kargo.Filter = m.inputComponents.GetSearchValue()
			m.kargo.Cursor = 0
		}
	case model.ModeCommand:
		m.inputComponents.SetCommandValue(m.inputComponents.GetCommandValue() + text)
	}
	return m, nil
}

func (m *Model) handleKargoKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := m.kargo
	key := msg.String()
	if key == "ctrl+c" {
		return m, func() tea.Msg { return model.QuitMsg{} }
	}
	if key == "esc" && m.selection != nil && !m.selection.IsEmpty() {
		m.selection.Clear()
		m.selectionSnapshot, m.selectedText = "", ""
	}
	if st.Executing {
		if key == "q" {
			return m, func() tea.Msg { return model.QuitMsg{} }
		}
		return m, nil
	}
	if m.state.Mode == model.ModeSearch || m.state.Mode == model.ModeCommand {
		search := m.state.Mode == model.ModeSearch
		switch key {
		case "esc":
			m.inputComponents.BlurInputs()
			if m.state.Diff != nil {
				m.state.Mode = model.ModeDiff
			} else {
				m.state.Mode = model.ModeNormal
				if search {
					st.Filter = ""
				}
			}
			return m, nil
		case "enter":
			m.inputComponents.BlurInputs()
			if search {
				if m.state.Diff != nil {
					m.state.Mode = model.ModeDiff
				} else {
					m.state.Mode = model.ModeNormal
					st.Filter = m.inputComponents.GetSearchValue()
					m.clampKargoCursor()
				}
				return m, nil
			}
			parts := strings.Fields(m.inputComponents.GetCommandValue())
			command := ""
			if len(parts) > 0 {
				command = strings.ToLower(parts[0])
			}
			m.state.Mode = model.ModeNormal
			m.inputComponents.ClearCommandInput()
			switch command {
			case "argo", "argocd", "apps":
				return m.leaveKargo()
			case "ctx", "contexts", "instances":
				st.Draft = nil
				return m, m.kargoNavigate("Connection")
			case "project", "projects":
				st.Draft = nil
				return m, m.kargoNavigate("Project")
			case "stage", "stages":
				st.Draft = nil
				return m, m.kargoNavigate("Stage")
			case "promotion", "promotions":
				st.Draft = nil
				return m, m.kargoNavigate("Promotion")
			case "warehouse", "warehouses":
				st.Draft = nil
				return m, m.kargoNavigate("Warehouse")
			case "freight":
				st.Draft = nil
				return m, m.kargoNavigate("Freight")
			case "refresh":
				return m, m.loadKargoView(true)
			case "login":
				if len(parts) > 1 {
					server, err := kargo.NormalizeServer(parts[1])
					if err != nil {
						st.Error = err.Error()
						return m, nil
					}
					connection := &st.Connections[st.Connection]
					if connection.Server != "" && connection.Server != server {
						st.Error = "Choose another instance to log in to a different server"
						return m, nil
					}
					connection.Server = server
				}
				return m, m.beginKargoLogin()
			default:
				st.Error = "Commands: projects, stages, promotions, warehouses, freight, ctx, refresh, login, argo"
			}
			return m, nil
		default:
			if search {
				cmd := m.inputComponents.UpdateSearchInput(msg)
				if m.state.Diff != nil {
					m.state.Diff.SearchQuery = m.inputComponents.GetSearchValue()
					m.state.Diff.MatchIndex = -1
					m.findInspectionMatch(false)
				} else {
					st.Filter = m.inputComponents.GetSearchValue()
					st.Cursor = 0
				}
				return m, cmd
			}
			return m, m.inputComponents.UpdateCommandInput(msg)
		}
	}
	if st.Form != nil {
		return m.handleKargoFormKey(msg)
	}
	if st.Confirm {
		switch key {
		case "j", "down":
			st.ConfirmOffset++
		case "k", "up":
			st.ConfirmOffset = max(0, st.ConfirmOffset-1)
		case "pgdown":
			st.ConfirmOffset += 5
		case "pgup":
			st.ConfirmOffset = max(0, st.ConfirmOffset-5)
		case "esc", "q":
			st.Confirm, st.Draft = false, nil
			st.Error = ""
		case "ctrl+p", "ctrl+t":
			if st.Draft != nil && ((st.Draft.RetryOf == "" && key == "ctrl+p") || (st.Draft.RetryOf != "" && key == "ctrl+t")) {
				return m, m.executeKargoPromotion()
			}
		}
		return m, nil
	}
	if m.state.Diff != nil {
		switch key {
		case "ctrl+t":
			return m, m.prepareKargoRetry()
		case "esc", "q":
			return m.backKargo()
		case "d", "y":
			if st.Detail != nil {
				kind := "Describe"
				if key == "y" {
					kind = "YAML"
				}
				m.showKargoDocument(*st.Detail, kind, false)
				if st.Client != nil && st.Client.UsesConnect() {
					return m, m.loadKargoView(true)
				}
				return m, m.nextKargoRefresh()
			}
		case "ctrl+r":
			return m, m.loadKargoView(true)
		case "n", "N":
			m.findInspectionMatch(key == "N")
		case "j", "down":
			m.state.Diff.Offset++
		case "k", "up":
			m.state.Diff.Offset = max(0, m.state.Diff.Offset-1)
		case "pgdown", "ctrl+f":
			m.state.Diff.Offset += m.diffPageSize()
		case "pgup", "ctrl+b":
			m.state.Diff.Offset = max(0, m.state.Diff.Offset-m.diffPageSize())
		case "g", "home":
			m.state.Diff.Offset = 0
		case "G", "end":
			m.state.Diff.Offset = max(0, len(m.state.Diff.Content)-m.diffPageSize()+1)
		case "h", "left":
			m.state.Diff.HorizontalOffset = max(0, m.state.Diff.HorizontalOffset-8)
		case "l", "right":
			m.state.Diff.HorizontalOffset += 8
		case "/":
			m.inputComponents.ClearSearchInput()
			m.inputComponents.FocusSearchInput()
			m.state.Mode = model.ModeSearch
		case "ctrl+l":
			return m, m.beginKargoLogin()
		}
		return m, nil
	}
	switch key {
	case "ctrl+t":
		return m, m.prepareKargoRetry()
	case "q":
		return m, func() tea.Msg { return model.QuitMsg{} }
	case "a":
		return m.leaveKargo()
	case "ctrl+r":
		if st.Kind == "Connection" {
			st.Connections = m.kargoConnections()
			st.Connection = min(st.Connection, len(st.Connections)-1)
			m.clampKargoCursor()
			return m, nil
		}
		return m, m.loadKargoView(true)
	case "ctrl+l":
		if st.Kind == "Connection" {
			rows := m.kargoRows()
			if len(rows) > 0 {
				m.selectKargoConnection(rows[st.Cursor].ConnectionIndex)
			}
		}
		return m, m.beginKargoLogin()
	case "ctrl+a":
		return m, m.toggleKargoAutoLogin()
	case "ctrl+p":
		return m, m.startKargoPromotion()
	case "ctrl+e":
		if st.Error != "" {
			m.saveKargoLocation()
			m.showKargoText("Last error", st.Error)
		}
		return m, nil
	case "0", "c":
		st.Draft = nil
		return m, m.kargoNavigate("Connection")
	case "1", "6":
		st.Draft = nil
		return m, m.kargoNavigate("Project")
	case "2":
		st.Draft = nil
		return m, m.kargoNavigate("Stage")
	case "3":
		st.Draft = nil
		return m, m.kargoNavigate("Promotion")
	case "4":
		st.Draft = nil
		return m, m.kargoNavigate("Warehouse")
	case "5":
		st.Draft = nil
		return m, m.kargoNavigate("Freight")
	case "/":
		m.inputComponents.ClearSearchInput()
		m.inputComponents.FocusSearchInput()
		m.state.Mode = model.ModeSearch
	case ":":
		m.inputComponents.ClearCommandInput()
		m.inputComponents.FocusCommandInput()
		m.state.Mode = model.ModeCommand
	case "?":
		m.saveKargoLocation()
		m.showKargoText("Kargo help", "Navigate:\n  0/c: instances, 1: projects, 2: stages, 3: promotions\n  4: warehouses, 5: freight, a: Argo CD\n\nInspect:\n  Enter: open; d: describe; y: full YAML; /: filter\n  Ctrl+E: show the last request error\n\nPromote:\n  Select Stage or Freight and press Ctrl+P.\n  Choose with arrows; type to filter; Tab / Shift+Tab moves focus.\n  Review instance, project, Stage and Freight.\n  Tab to Cancel / Promote, then Enter; Esc cancels.\n  Ctrl+P also submits the reviewed promotion.\n  The result opens with phase, step results and error messages.\n  Ctrl+R: refresh now. Writes are never retried automatically.\n\nSSO:\n  Ctrl+L: Kargo CLI browser SSO.\n  Ctrl+A: toggle auto-login for the current/selected instance.\n\nNetwork:\n  Normal: poll the visible data every 10s.\n  --low-bandwidth: every 2m, cached lists, no background fan-out.\n  [network] refresh_interval=\"0\": manual refresh only.")
	case "j", "down":
		st.Cursor++
	case "k", "up":
		st.Cursor--
	case "pgdown", "ctrl+f":
		st.Cursor += max(1, m.state.Terminal.Rows-10)
	case "pgup", "ctrl+b":
		st.Cursor -= max(1, m.state.Terminal.Rows-10)
	case "g", "home":
		st.Cursor = 0
	case "G", "end":
		st.Cursor = len(m.kargoRows()) - 1
	case "esc":
		return m.backKargo()
	case "enter", "d", "y":
		rows := m.kargoRows()
		if st.Cursor < 0 || st.Cursor >= len(rows) {
			return m, nil
		}
		row := rows[st.Cursor]
		if st.Kind == "Connection" {
			m.saveKargoLocation()
			m.selectKargoConnection(row.ConnectionIndex)
			m.setKargoView("Project")
			return m, m.loadKargoView(false)
		}
		if key == "enter" {
			switch st.Kind {
			case "Project":
				m.saveKargoLocation()
				st.Project = row.Resource.Name
				m.setKargoView("Stage")
				return m, m.loadKargoView(false)
			case "Stage":
				if st.Draft != nil && st.Draft.Freight != "" {
					st.Draft.Stage = row.Resource.Name
					st.Confirm = true
					return m, nil
				}
				m.saveKargoLocation()
				m.setKargoView("Promotion")
				st.Stage = row.Resource.Name
				return m, m.loadKargoView(false)
			case "Warehouse":
				m.saveKargoLocation()
				m.setKargoView("Freight")
				st.Warehouse = row.Resource.Name
				return m, m.loadKargoView(false)
			case "Freight":
				if st.Draft != nil && st.Draft.Stage != "" {
					st.Draft.Freight = row.Resource.Name
					st.Confirm = true
					m.cancelKargoLoad()
					return m, nil
				}
			}
		}
		kind := "Describe"
		if key == "y" {
			kind = "YAML"
		}
		m.saveKargoLocation()
		m.cancelKargoLoad()
		m.showKargoDocument(row.Resource, kind, false)
		if st.Client != nil && st.Client.UsesConnect() {
			return m, m.loadKargoView(true)
		}
		return m, m.nextKargoRefresh()
	}
	m.clampKargoCursor()
	return m, nil
}

func (m *Model) selectKargoConnection(index int) {
	m.cancelKargoRequests()
	st := m.kargo
	st.Connection, st.Client, st.Project = index, nil, ""
	st.Items, st.Detail, st.Draft = nil, nil, nil
	st.Stage, st.Warehouse, st.Filter, st.Error, st.DetailKind = "", "", "", "", ""
	st.Confirm, st.Executing, st.Cursor = false, false, 0
	st.Form = nil
	st.Cache = map[kargoTarget]kargoCacheEntry{}
	st.SessionIdentity = ""
	if st.Kind == "Connection" {
		st.Cursor = index
	}
	m.state.Diff, m.state.Mode = nil, model.ModeNormal
}

func (m *Model) startKargoPromotion() tea.Cmd {
	st := m.kargo
	rows := m.kargoRows()
	if st.Cursor < 0 || st.Cursor >= len(rows) || st.Client == nil {
		return m.showFooterNotice("Select a Stage or Freight after loading the project")
	}
	resource := rows[st.Cursor].Resource
	if resource.Kind != "Stage" && resource.Kind != "Freight" {
		return m.showFooterNotice("Select a Stage or Freight and press Ctrl+P")
	}
	origin := m.kargoLocation()
	m.cancelKargoLoad()
	connection := st.Connections[st.Connection]
	st.Draft = &kargoPromotion{Connection: connection.Name, Server: st.Client.Connection.Server, Project: st.Project}
	kind := "Freight"
	if resource.Kind == "Stage" {
		st.Draft.Stage = resource.Name
	} else {
		st.Draft.Freight = resource.Name
		kind = "Stage"
	}
	st.Form = &kargoPromotionForm{Origin: origin, Kind: kind}
	st.Confirm, st.ConfirmOffset, st.Error = true, 0, ""
	return m.loadKargoForm(false)
}

// Kargo's own Retry action submits the same Freight to the same Stage as a
// new Promotion. Terminal failures cannot be resumed from the failed step.
func kargoPromotionRetryable(resource kargo.Resource) bool {
	if resource.Kind != "Promotion" {
		return false
	}
	switch resource.Phase() {
	case "Failed", "Errored", "Aborted":
		return true
	}
	return false
}

func (m *Model) selectedKargoPromotion() (kargo.Resource, bool) {
	st := m.kargo
	if st.Detail != nil {
		return *st.Detail, kargoPromotionRetryable(*st.Detail)
	}
	rows := m.kargoRows()
	if st.Cursor >= 0 && st.Cursor < len(rows) {
		resource := rows[st.Cursor].Resource
		return resource, kargoPromotionRetryable(resource)
	}
	return kargo.Resource{}, false
}

func (m *Model) prepareKargoRetry() tea.Cmd {
	st := m.kargo
	resource, retryable := m.selectedKargoPromotion()
	if !retryable || st.Client == nil || st.LoginBusy {
		return m.showFooterNotice("Retry is available for Failed, Errored or Aborted Promotions")
	}
	stage, freight := resource.String("spec.stage"), resource.String("spec.freight")
	if resource.Project != st.Project || stage == "" || freight == "" {
		return m.showFooterNotice("Promotion has no unambiguous project, Stage or Freight; refresh first")
	}
	origin := m.kargoLocation()
	m.cancelKargoLoad()
	connection := st.Connections[st.Connection]
	st.Form = &kargoPromotionForm{Origin: origin, Focus: 1}
	st.Draft = &kargoPromotion{Connection: connection.Name, Server: st.Client.Connection.Server, Project: st.Project, Stage: stage, Freight: freight, RetryOf: resource.Name}
	st.Confirm, st.ConfirmOffset, st.Error = true, 0, ""
	st.Detail, st.DetailKind = nil, ""
	m.state.Diff, m.state.Mode = nil, model.ModeNormal
	return nil
}

func (m *Model) showKargoText(title, text string) {
	m.cancelKargoLoad()
	m.kargo.DetailKind = "Help"
	m.state.Mode = model.ModeDiff
	m.state.Diff = &model.DiffState{Title: title, InspectionKind: "Describe", Content: strings.Split(inspectionPlainText(text), "\n")}
}

func (m *Model) showKargoDocument(resource kargo.Resource, kind string, preserve bool) {
	st := m.kargo
	st.Detail, st.DetailKind = &resource, kind
	text := ""
	if kind == "YAML" {
		object := make(map[string]any, len(resource.Object)+2)
		for key, value := range resource.Object {
			object[key] = value
		}
		if object["kind"] == nil {
			object["kind"] = resource.Kind
		}
		if object["apiVersion"] == nil {
			object["apiVersion"] = "kargo.akuity.io/v1alpha1"
		}
		var err error
		text, err = inspectionYAML(object)
		if err != nil {
			text = err.Error()
		}
	} else {
		var out strings.Builder
		fmt.Fprintf(&out, "Instance: %s\nServer: %s\nProject: %s\nKind: %s\nName: %s\nPhase: %s\n", st.Connections[st.Connection].Name, st.Connections[st.Connection].Server, resource.Project, resource.Kind, resource.Name, resource.Phase())
		if resource.Kind == "Promotion" {
			for _, line := range m.kargoPromotionSummary(resource)[1:] {
				fmt.Fprintln(&out, line)
			}
		}
		if message := resource.Message(); message != "" {
			fmt.Fprintf(&out, "Message: %s\n", message)
		}
		for _, section := range []string{"spec", "status", "origin", "commits", "images", "charts"} {
			value := resource.Value(section)
			if emptyDescribeValue(value) {
				continue
			}
			if section == "status" {
				if status, ok := value.(map[string]any); ok {
					compact := map[string]any{}
					for key, value := range status {
						if key != "state" && key != "freightHistory" && key != "discoveredArtifacts" && !emptyDescribeValue(value) {
							compact[key] = value
						}
					}
					value = compact
				}
			}
			fmt.Fprintf(&out, "\n%s:\n", describeLabel(section))
			describeValue(&out, value, 2)
		}
		text = out.String()
	}
	previous := m.state.Diff
	m.state.Diff = &model.DiffState{Title: kargoSingleLine(st.Connections[st.Connection].Name + " / " + resource.Project + " / " + resource.Kind + " / " + resource.Name), InspectionKind: kind, Content: strings.Split(strings.TrimSuffix(inspectionPlainText(text), "\n"), "\n"), MatchIndex: -1}
	if preserve && previous != nil {
		m.state.Diff.Offset, m.state.Diff.HorizontalOffset, m.state.Diff.SearchQuery = previous.Offset, previous.HorizontalOffset, previous.SearchQuery
	}
	if m.state.Mode != model.ModeSearch {
		m.state.Mode = model.ModeDiff
	}
}
