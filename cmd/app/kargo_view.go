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
	if alias := resource.String("alias"); alias != "" {
		return alias
	}
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
	connection := st.Connections[st.Connection]
	project := st.Project
	if project == "" {
		project = "all"
	}
	network := "manual refresh"
	if interval := m.config.GetKargoRefreshInterval(); interval > 0 {
		network = "poll " + interval.String()
	}
	if m.config.IsLowBandwidth() {
		network += " / low bandwidth"
	}
	login := "manual SSO"
	if connection.AutoRelogin {
		login = "auto SSO"
	}
	if st.LoginBusy {
		login = "signing in..."
	}
	view := st.Kind
	if st.Detail != nil {
		view = st.Detail.Kind + " / " + st.DetailKind
	}
	var legend []viewHotkey
	switch {
	case st.Executing:
		legend = []viewHotkey{{"ctrl-c", "quit"}}
	case st.Form != nil:
		legend = []viewHotkey{{"tab/shift-tab", "focus"}, {"up/down", "choose"}, {"left/right", "mode"}, {"enter", "activate"}, {"esc", "cancel"}}
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
		if st.Kind == "Stage" {
			legend = append(legend, viewHotkey{"ctrl-u", "promote from upstream"})
		}
		if _, retryable := m.selectedKargoPromotion(); retryable {
			legend = append(legend, viewHotkey{"ctrl-t", "retry"})
		}
		if st.Kind != "Connection" {
			legend = append(legend, viewHotkey{"d/y", "describe/YAML"})
		}
		legend = append(legend, viewHotkey{"/", "filter"}, viewHotkey{"?", "help"})
	}
	meta := []resourceHeaderField{{"Context", connection.Name}, {"Server", hostFromURL(connection.Server)}, {"User", "SSO"}, {"Resource", "Kargo / " + view}, {"Project", project}, {"Login", login}, {"Network", network}}
	return m.renderResourceHeader(meta, legend)
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
	if m.kargo.Form != nil {
		base := m.kargoFormBackdrop()
		modal := m.renderKargoPromotionPopup()
		return m.composeOverlay(
			lipgloss.NewLayer(desaturateANSI(base)),
			lipgloss.NewLayer(m.renderKargoHeader()).X(1).Y(0).Z(1),
			lipgloss.NewLayer(modal).
				X(max(0, (m.state.Terminal.Cols-lipgloss.Width(modal))/2)).
				Y(max(0, (m.state.Terminal.Rows-lipgloss.Height(modal))/2)).Z(2),
		)
	}
	return m.renderKargoBaseView()
}

func (m *Model) renderKargoBaseView() string {
	st := m.kargo
	if m.state.Diff != nil && st.Form == nil {
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
			field := func(label, value string) string {
				return lipgloss.NewStyle().Foreground(cyanBright).Bold(true).Render(label+": ") + lipgloss.NewStyle().Foreground(whiteBright).Render(kargoSingleLine(value))
			}
			lines = []string{lipgloss.NewStyle().Foreground(syncedColor).Bold(true).Render("PROMOTE FREIGHT"), "", field("Instance", draft.Connection), field("Server", draft.Server), field("Project", draft.Project), field("Stage", draft.Stage), field("Freight", draft.Freight), "", renderHotkey(viewHotkey{"ctrl-p", "submit promotion"}) + "   " + renderHotkey(viewHotkey{"esc", "cancel"})}
			if draft.RetryOf != "" {
				lines[0] = lipgloss.NewStyle().Foreground(progressColor).Bold(true).Render("RETRY PROMOTION")
				lines = append(lines[:len(lines)-1], field("Previous", draft.RetryOf), "Creates a new Promotion for the same Freight and Stage, using the current Stage template.", renderHotkey(viewHotkey{"ctrl-t", "confirm retry"})+"   "+renderHotkey(viewHotkey{"esc", "cancel"}))
			}
			if st.Executing {
				lines[len(lines)-1] = lipgloss.NewStyle().Foreground(progressColor).Bold(true).Render("Submitting once... waiting for the Kargo API response")
			}
		}
	} else {
		rows := m.kargoRows()
		columns := kargoTableColumns(st.Kind, width, rows)
		var preview []string
		if st.Kind == "Promotion" && st.Cursor >= 0 && st.Cursor < len(rows) && height >= 8 {
			for _, line := range m.kargoPromotionSummary(rows[st.Cursor].Resource) {
				preview = append(preview, m.colorInspectionLine(kargoSingleLine(line), "Describe"))
			}
		}
		tableHeight := height
		if len(preview) > 0 {
			tableHeight -= len(preview) + 1
		}
		renderCells := func(values []string, selected bool) string {
			cells := make([]string, len(columns))
			for i, column := range columns {
				value := ""
				if column.Index < len(values) {
					value = kargoSingleLine(values[column.Index])
				}
				style := lipgloss.NewStyle().Foreground(whiteBright)
				if column.Index == 0 {
					style = style.Foreground(cyanBright)
				} else if column.Index == 1 && st.Kind != "Freight" && st.Kind != "Connection" {
					style = kargoStatusStyle(value)
				} else if st.Kind == "Promotion" && column.Index == 3 || st.Kind == "Freight" && column.Index == 1 {
					style = style.Foreground(magentaBright)
				} else if column.Index == len(values)-1 && value != "" && st.Kind != "Freight" && st.Kind != "Connection" {
					style = kargoStatusStyle(values[1])
				}
				if selected {
					style = style.Background(currentPalette.CursorBG).Foreground(ensureContrastingForeground(currentPalette.CursorBG, style.GetForeground()))
				}
				cells[i] = style.Render(tableCell(value, column.Width))
			}
			line := strings.Join(cells, "  ")
			if selected {
				line = lipgloss.NewStyle().Background(currentPalette.CursorBG).Render(line)
			}
			return line
		}
		heading := make([]string, len(columns))
		for i, column := range columns {
			heading[i] = tableCell(column.Header, column.Width)
		}
		lines = append(lines, headerStyle.Render(strings.Join(heading, "  ")))
		start := max(0, st.Cursor-max(0, tableHeight-2)+1)
		for i := start; i < min(len(rows), start+tableHeight-1); i++ {
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
		if len(preview) > 0 {
			for len(lines) < tableHeight {
				lines = append(lines, "")
			}
			lines = append(lines, lipgloss.NewStyle().Foreground(dimColor).Render(strings.Repeat("─", width)))
			lines = append(lines, preview...)
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
	footer := statusStyle
	if st.Error != "" {
		footer = footer.Foreground(redColor).Bold(true)
	} else if st.Loading || st.Executing {
		footer = footer.Foreground(progressColor)
	}
	sections = append(sections, body, clipAnsiToWidth(footer.Render(inspectionPlainText(status)), width+2))
	return mainContainerStyle.Render(strings.Join(sections, "\n"))
}
