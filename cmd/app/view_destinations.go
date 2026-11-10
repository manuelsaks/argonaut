package main

import (
	"fmt"

	"charm.land/lipgloss/v2"
	"github.com/darksworm/argonaut/pkg/model"
)

type appColumnWidths struct{ name, owner, sync, health, destination int }

func appColumns(width int) appColumnWidths {
	w := appColumnWidths{sync: 2, health: 2}
	if width >= 75 {
		w.sync, w.health = 11, 13
	}
	remaining := max(0, width-w.sync-w.health-4)
	w.name = remaining * 36 / 100
	w.owner = remaining * 28 / 100
	w.destination = remaining - w.name - w.owner
	return w
}

func tableCell(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) > width {
		text = truncateWithEllipsis(text, width)
	}
	return padRight(text, width)
}

func appColumnRow(w appColumnWidths, name, owner, sync, health, destination string) string {
	return tableCell(name, w.name) + " " + tableCell(owner, w.owner) + " " + tableCell(sync, w.sync) + " " + tableCell(health, w.health) + " " + tableCell(destination, w.destination)
}

func (m *Model) dimensionRow(name, count, location string) string {
	width := m.contentInnerWidth()
	nameWidth := max(8, (width-9)*36/100)
	return tableCell(name, nameWidth) + " " + tableCell(count, 5) + " " + tableCell(location, max(0, width-nameWidth-7))
}

func (m *Model) renderDimensionRow(key string, cursor bool) string {
	row := ""
	switch m.state.Navigation.View {
	case model.ViewClusters:
		if server, ok := m.destinationByKey(key); ok {
			name := server.Name
			if name == "" {
				name = "(unnamed)"
			}
			address := server.Server
			if server.Project != "" {
				address += " [" + server.Project + "]"
			}
			count := 0
			if m.state.Index != nil {
				count = len(m.state.Index.ByCluster[key])
			}
			row = m.dimensionRow(name, fmt.Sprint(count), address)
		}
	case model.ViewApplicationSets:
		count, location := m.appSetLocation(key)
		row = m.dimensionRow(key, fmt.Sprint(count), location)
	}
	if row == "" {
		return m.renderSimpleRow(key, cursor)
	}
	if cursor && !m.willDesaturateBase() {
		return selectedStyle.Render(row)
	}
	return row
}
