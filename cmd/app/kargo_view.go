package main

import (
	"fmt"
	"net/url"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/darksworm/argonaut/pkg/kargo"
	"github.com/darksworm/argonaut/pkg/model"
)

func kargoFreightAlias(resource kargo.Resource) string {
	labels, _ := resource.Value("metadata.labels").(map[string]any)
	alias, _ := labels["kargo.akuity.io/alias"].(string)
	return alias
}

func kargoSources(resource kargo.Resource) string {
	var sources []string
	appendSource := func(object map[string]any) {
		for _, key := range []string{"repoURL", "repoUrl", "image", "repository", "tag", "id", "version"} {
			if value, ok := object[key].(string); ok && value != "" {
				if u, err := url.Parse(value); err == nil && u.Host != "" {
					u.User, u.RawQuery, u.Fragment = nil, "", ""
					value = u.String()
				}
				sources = append(sources, value)
			}
		}
	}
	if subscriptions, ok := resource.Value("spec.subscriptions").([]any); ok {
		for _, value := range subscriptions {
			object, _ := value.(map[string]any)
			for _, key := range []string{"git", "image", "chart"} {
				if source, ok := object[key].(map[string]any); ok {
					appendSource(source)
				}
			}
		}
	}
	for _, key := range []string{"commits", "images", "charts"} {
		if artifacts, ok := resource.Value(key).([]any); ok {
			for _, value := range artifacts {
				if source, ok := value.(map[string]any); ok {
					appendSource(source)
				}
			}
		}
	}
	return strings.Join(sources, " | ")
}

func (m *Model) renderKargoHeader() string {
	st := m.kargo
	width := max(8, m.state.Terminal.Cols-2)
	connection := st.Connections[st.Connection]
	connection.Name, connection.Server = kargoSingleLine(connection.Name), kargoSingleLine(connection.Server)
	project := kargoSingleLine(st.Project)
	if project == "" {
		project = "choose a project"
	}
	network := "refresh: " + m.config.GetKargoRefreshInterval().String()
	if m.config.GetKargoRefreshInterval() <= 0 {
		network = "manual refresh"
	}
	if m.config.IsLowBandwidth() {
		network = "low bandwidth | " + network
	}
	login := "SSO manual"
	if connection.AutoRelogin {
		login = "SSO auto"
	}
	if st.LoginBusy {
		login = "SSO signing in..."
	}
	view := st.Kind
	if st.Detail != nil {
		view = st.Detail.Kind + " / " + st.DetailKind
	}
	style := lipgloss.NewStyle().Bold(true).Foreground(cyanBright)
	rows := []string{
		style.Render("Kargo") + " | Instance: " + connection.Name + " | Project: " + project,
		"View: " + view + " | " + login + " | " + network,
	}
	if connection.Server != "" && m.state.Terminal.Rows > 22 {
		rows = append(rows, "Server: "+connection.Server)
	}
	var legend []viewHotkey
	switch {
	case st.Executing:
		legend = []viewHotkey{{"ctrl-c", "quit"}}
	case st.Confirm:
		legend = []viewHotkey{{"ctrl-p", "confirm promotion"}, {"j/k", "scroll"}, {"esc", "cancel"}}
		if st.Draft != nil && st.Draft.RetryOf != "" {
			legend[0] = viewHotkey{"ctrl-t", "confirm retry"}
		}
	case m.state.Mode == model.ModeSearch || m.state.Mode == model.ModeCommand:
		legend = []viewHotkey{{"enter", "apply"}, {"esc", "cancel"}}
	case m.state.Diff != nil:
		legend = []viewHotkey{{"d/y", "describe/YAML"}, {"ctrl-y", "copy"}, {"ctrl-r", "refresh"}, {"/", "find"}, {"esc", "back"}}
		if _, retryable := m.selectedKargoPromotion(); retryable {
			legend = append(legend, viewHotkey{"ctrl-t", "retry"})
		}
	default:
		legend = []viewHotkey{{"enter", "open"}, {"ctrl-r", "refresh"}, {"ctrl-l", "SSO"}, {"ctrl-a", "auto SSO"}}
		if st.Kind == "Stage" || st.Kind == "Freight" {
			legend = append(legend, viewHotkey{"ctrl-p", "promote"})
		}
		if _, retryable := m.selectedKargoPromotion(); retryable {
			legend = append(legend, viewHotkey{"ctrl-t", "retry"})
		}
		if st.Kind != "Connection" {
			legend = append(legend, viewHotkey{"d/y", "describe/YAML"})
		}
		legend = append(legend, viewHotkey{"/", "filter"}, viewHotkey{"?", "help"})
	}
	line := ""
	for _, action := range legend {
		cell := renderHotkey(action)
		if line != "" && lipgloss.Width(line)+lipgloss.Width(cell)+2 > width {
			rows = append(rows, line)
			line = ""
		}
		if line != "" {
			line += "  "
		}
		line += cell
	}
	if line != "" {
		rows = append(rows, line)
	}
	if m.state.Diff == nil && !st.Confirm && !st.Executing && m.state.Mode == model.ModeNormal {
		navigation := "<0/c> instances  <1> projects  <2> stages  <3> promotions  <4> warehouses  <5> freight  <a> Argo CD"
		if width < 105 {
			navigation = "<0> ctx <1> proj <2> stage <3> promo <4> wh <5> freight <a> Argo"
		}
		rows = append(rows, style.Render(navigation))
	}
	if m.state.Terminal.Rows <= 22 && len(rows) > 5 {
		rows = rows[:5]
	}
	for i := range rows {
		rows[i] = clipAnsiToWidth(rows[i], width)
	}
	return strings.Join(rows, "\n")
}

// Remove remote terminal controls and line breaks before adding local styling.
func kargoSingleLine(text string) string {
	return strings.Join(strings.Fields(inspectionPlainText(text)), " ")
}

func kargoStatusStyle(phase string) lipgloss.Style {
	style := lipgloss.NewStyle()
	switch phase {
	case "Ready", "Healthy", "Succeeded", "Available", "Successful":
		return style.Foreground(syncedColor)
	case "Failed", "Errored", "Error", "Aborted", "NotReady", "Degraded":
		return style.Foreground(redColor).Bold(true)
	case "Running", "Promoting", "Pending", "Verifying":
		return style.Foreground(progressColor)
	default:
		return style.Foreground(dimColor)
	}
}

func (m *Model) renderKargoView() string {
	st := m.kargo
	if m.state.Diff != nil {
		return m.renderInspectionView()
	}
	header := m.renderKargoHeader()
	boxWidth := max(8, m.state.Terminal.Cols-4)
	width := max(1, boxWidth-contentBorderStyle.GetHorizontalFrameSize())
	input := ""
	if m.state.Mode == model.ModeSearch {
		input = "Filter / " + m.inputComponents.searchInput.View()
	}
	if m.state.Mode == model.ModeCommand {
		input = ":" + m.inputComponents.commandInput.View()
	}
	height := max(3, m.state.Terminal.Rows-countLines(header)-5)
	if input != "" {
		height--
	}
	var lines []string
	if st.Confirm || st.Executing {
		draft := st.Draft
		if draft != nil {
			lines = []string{headerStyle.Render("PROMOTE FREIGHT"), "", "Instance: " + kargoSingleLine(draft.Connection), "Server: " + kargoSingleLine(draft.Server), "Project: " + kargoSingleLine(draft.Project), "Stage: " + kargoSingleLine(draft.Stage), "Freight: " + kargoSingleLine(draft.Freight), "", "Ctrl+P: submit this promotion   Esc: cancel"}
			if draft.RetryOf != "" {
				lines[0] = headerStyle.Render("RETRY PROMOTION")
				lines = append(lines[:len(lines)-1], "Previous: "+kargoSingleLine(draft.RetryOf), "Creates a new Promotion for the same Freight and Stage, using the current Stage template.", "Ctrl+T: confirm retry   Esc: cancel")
			}
			if st.Executing {
				lines[len(lines)-1] = "Submitting once... waiting for the Kargo API response"
			}
		}
	} else {
		rows := m.kargoRows()
		columns := []string{"NAME", "STATUS", "DETAILS"}
		switch st.Kind {
		case "Connection":
			columns = []string{"INSTANCE", "LOGIN", "API SERVER"}
		case "Stage":
			columns = []string{"STAGE", "STATUS", "FREIGHT", "PROMOTION", "MESSAGE"}
		case "Promotion":
			columns = []string{"PROMOTION", "PHASE", "STAGE", "FREIGHT", "MESSAGE"}
		case "Warehouse":
			columns = []string{"WAREHOUSE", "STATUS", "LATEST FREIGHT", "SOURCES", "MESSAGE"}
		case "Freight":
			columns = []string{"FREIGHT", "ALIAS", "WAREHOUSE", "ARTIFACTS"}
		}
		visibleColumns := len(columns)
		if width < 75 {
			visibleColumns = min(3, visibleColumns)
		}
		if width < 48 {
			visibleColumns = 2
		}
		cellWidths := make([]int, visibleColumns)
		remaining := width - (visibleColumns-1)*2
		for i := range cellWidths {
			cellWidths[i] = remaining / (visibleColumns - i)
			remaining -= cellWidths[i]
		}
		renderCells := func(values []string, selected bool) string {
			cells := make([]string, visibleColumns)
			for i := range cells {
				value := ""
				if i < len(values) {
					value = inspectionPlainText(values[i])
					value = strings.Join(strings.Fields(value), " ")
				}
				cell := tableCell(value, cellWidths[i])
				if i == 1 && st.Kind != "Freight" && st.Kind != "Connection" {
					cell = kargoStatusStyle(value).Render(cell)
				}
				cells[i] = cell
			}
			line := strings.Join(cells, "  ")
			if selected {
				line = lipgloss.NewStyle().Background(currentPalette.CursorBG).Foreground(ensureContrastingForeground(currentPalette.CursorBG, whiteBright)).Render(line)
			}
			return line
		}
		lines = append(lines, headerStyle.Render(renderCells(columns, false)))
		start := max(0, st.Cursor-max(0, height-2)+1)
		for i := start; i < min(len(rows), start+height-1); i++ {
			lines = append(lines, renderCells(rows[i].Columns, i == st.Cursor))
		}
		if len(rows) == 0 {
			message := "No resources match this view. Ctrl+R: reload"
			if st.Loading {
				message = "Loading Kargo data..."
			}
			if st.Project == "" && st.Kind != "Project" && st.Kind != "Connection" {
				message = "Press 1 and choose a Project"
			}
			lines = append(lines, lipgloss.NewStyle().Foreground(dimColor).Render(message))
		}
	}
	if st.Confirm || st.Executing {
		var wrapped []string
		for _, line := range lines {
			wrapped = append(wrapped, wrapAnsiToWidth(line, width)...)
		}
		st.ConfirmOffset = min(st.ConfirmOffset, max(0, len(wrapped)-height))
		lines = wrapped[st.ConfirmOffset:min(len(wrapped), st.ConfirmOffset+height)]
	}
	for i := range lines {
		lines[i] = padRight(clipAnsiToWidth(lines[i], width), width)
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	body := contentBorderStyle.Width(boxWidth).Render(strings.Join(lines, "\n"))
	status := fmt.Sprintf("%d items", len(m.kargoRows()))
	if !st.Updated.IsZero() {
		status += " | updated " + st.Updated.Format("15:04:05")
	}
	if st.Loading {
		status += " | loading..."
	}
	if st.Filter != "" {
		status += " | filter: " + st.Filter
	}
	if st.Draft != nil && !st.Confirm {
		status = "Select " + st.Kind + " for promotion; Enter continues, Esc cancels"
	}
	if st.Error != "" {
		status = "ERROR: " + st.Error + " | Ctrl+E: full error; Ctrl+L: SSO"
	}
	if m.state.UI.FooterNotice != "" {
		status = m.state.UI.FooterNotice
	}
	sections := []string{header}
	if input != "" {
		sections = append(sections, clipAnsiToWidth(input, width+2))
	}
	sections = append(sections, body, clipAnsiToWidth(statusStyle.Render(inspectionPlainText(status)), width+2))
	return mainContainerStyle.Render(strings.Join(sections, "\n"))
}
