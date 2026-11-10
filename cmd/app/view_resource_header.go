package main

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/darksworm/argonaut/pkg/model"
)

var k9sFrameColor = lipgloss.Color("4")

type resourceHeaderField struct{ Label, Value string }

func headerField(field resourceHeaderField, width int) string {
	label := lipgloss.NewStyle().Foreground(yellowBright).Render(field.Label + ": ")
	value := kargoSingleLine(field.Value)
	style := lipgloss.NewStyle().Foreground(whiteBright)
	if field.Label == "Login" {
		if strings.Contains(value, "signing") {
			style = style.Foreground(progressColor)
		} else if strings.Contains(value, "auto") {
			style = style.Foreground(syncedColor)
		}
	}
	return padRight(clipAnsiToWidth(label+style.Render(value), width), width)
}

// The three groups mirror a k9s terminal header: connection facts, actual
// recent resources, then the current resource's actions. No fixed view menu.
func (m *Model) renderResourceHeader(meta []resourceHeaderField, actions []viewHotkey) string {
	width := max(1, m.state.Terminal.Cols-2)
	if m.state.Mode == model.ModeNormal && (m.kargo == nil || m.state.Navigation.View != model.ViewKargo || (!m.kargo.Confirm && !m.kargo.Executing)) {
		actions = append([]viewHotkey{{":", "command"}, {"?", "help"}}, actions...)
	}
	entries := m.recentEntries()
	recent := func(i, cellWidth int) string {
		if i >= len(entries) {
			return strings.Repeat(" ", cellWidth)
		}
		key := lipgloss.NewStyle().Foreground(magentaBright).Render(fmt.Sprintf("<%d>", i+1))
		label := lipgloss.NewStyle().Foreground(dimColor).Render(truncateWithEllipsis(entries[i].Label, max(0, cellWidth-4)))
		return padRight(clipAnsiToWidth(key+" "+label, cellWidth), cellWidth)
	}
	brand := lipgloss.NewStyle().Foreground(yellowBright).Render("Argonaut")
	var rows []string
	if width >= 98 && m.state.Terminal.Rows > 22 {
		metaWidth := min(36, width/4)
		recentWidth := 0
		if len(entries) > 0 {
			recentWidth = min(25, width/5)
		}
		brandWidth := max(lipgloss.Width(brand), lipgloss.Width(appVersion))
		legendWidth := max(1, width-metaWidth-recentWidth-brandWidth-6)
		columns := max(1, min(3, legendWidth/23))
		cellWidth := legendWidth / columns
		count := (len(actions) + columns - 1) / columns
		height := min(max(len(meta), count), max(4, m.state.Terminal.Rows/3))
		for row := range height {
			left := strings.Repeat(" ", metaWidth)
			if row < len(meta) {
				left = headerField(meta[row], metaWidth)
			}
			line := left + "  "
			if recentWidth > 0 {
				line += recent(row, recentWidth) + "  "
			}
			for col := range columns {
				idx := row + col*count
				cell := ""
				if row < count && idx < len(actions) {
					cell = renderHotkey(actions[idx])
				}
				line += padRight(clipAnsiToWidth(cell, max(1, cellWidth-2)), max(1, cellWidth-2)) + "  "
			}
			right := ""
			if row == 0 {
				right = brand
			}
			if row == 1 {
				right = lipgloss.NewStyle().Foreground(dimColor).Render(kargoSingleLine(appVersion))
			}
			line = padRight(line, width-brandWidth) + padRight(right, brandWidth)
			rows = append(rows, clipAnsiToWidth(line, width))
		}
	} else {
		rows = append(rows, headerField(meta[0], width))
		var resource, scope string
		for _, field := range meta {
			if field.Label == "Resource" {
				resource = field.Value
			}
			if field.Label == "Scope" || field.Label == "Project" {
				scope = field.Value
			}
		}
		rows = append(rows, headerField(resourceHeaderField{"Resource", resource + " | " + scope}, width))
		if len(entries) > 0 {
			var row string
			for i := range entries {
				row += recent(i, width/len(entries))
			}
			rows = append(rows, clipAnsiToWidth(row, width))
		}
		height := max(4, min(6, m.state.Terminal.Rows/4))
		line := ""
		for _, action := range actions {
			cell := renderHotkey(action)
			if line != "" && lipgloss.Width(line)+lipgloss.Width(cell)+2 > width {
				rows = append(rows, line)
				line = ""
				if len(rows) >= height {
					break
				}
			}
			if line != "" {
				line += "  "
			}
			line += cell
		}
		if line != "" && len(rows) < height {
			rows = append(rows, clipAnsiToWidth(line, width))
		}
	}
	for i := range rows {
		rows[i] = padRight(clipAnsiToWidth(rows[i], width), width)
	}
	return strings.Join(rows, "\n")
}
