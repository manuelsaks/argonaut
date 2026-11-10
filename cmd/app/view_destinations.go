package main

import (
	"fmt"

	"charm.land/lipgloss/v2"
	"github.com/darksworm/argonaut/pkg/model"
)

type appColumnWidths struct{ context, name, owner, sync, health, destination int }

func appColumns(width int) appColumnWidths {
	w := appColumnWidths{sync: 2, health: 2}
	if width >= 75 {
		w.sync, w.health = 11, 13
	}
	remaining := max(0, width-w.sync-w.health-4)
	w.name = remaining * 32 / 100
	w.owner = remaining * 34 / 100
	w.destination = remaining - w.name - w.owner
	return w
}

func (m *Model) appListColumns() appColumnWidths {
	width := m.contentInnerWidth()
	ctx := 0
	if m.allContexts != nil {
		ctx = min(20, max(10, width/6))
		width -= ctx + 1
	}
	w := appColumns(width)
	w.context = ctx
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
	if m.allContexts != nil {
		contextWidth := min(20, max(10, m.contentInnerWidth()/6))
		return tableCell("CONTEXT", contextWidth) + " " + dimensionRowAtWidth(name, count, location, m.contentInnerWidth()-contextWidth-1)
	}
	return dimensionRowAtWidth(name, count, location, m.contentInnerWidth())
}

func dimensionRowAtWidth(name, count, location string, width int) string {
	nameWidth := max(8, (width-9)*36/100)
	return tableCell(name, nameWidth) + " " + tableCell(count, 5) + " " + tableCell(location, max(0, width-nameWidth-7))
}

func (m *Model) renderDimensionRow(key string, cursor bool) string {
	row := ""
	ctx := ""
	name, count, location := "", "", ""
	switch m.state.Navigation.View {
	case model.ViewClusters:
		if key == allServersChoice {
			return m.renderSimpleRow("all  (all destination servers)", cursor)
		}
		if server, ok := m.destinationByKey(key); ok {
			name = server.Name
			ctx = server.ContextName
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
			location = address
		}
	case model.ViewApplicationSets:
		appCount, where := m.appSetLocation(key)
		contextName, localKey := model.SplitContextKey(key)
		ctx = contextName
		name = localKey
		count = fmt.Sprint(appCount)
		location = where
		row = m.dimensionRow(localKey, count, location)
	}
	if m.allContexts != nil && row != "" {
		if m.state.Navigation.View == model.ViewClusters {
			count = fmt.Sprint(len(m.state.Index.ByCluster[key]))
		}
		contextWidth := min(20, max(10, m.contentInnerWidth()/6))
		row = tableCell(ctx, contextWidth) + " " + dimensionRowAtWidth(name, count, location, m.contentInnerWidth()-contextWidth-1)
	}
	if row == "" {
		return m.renderSimpleRow(key, cursor)
	}
	if cursor && !m.willDesaturateBase() {
		return selectedStyle.Render(row)
	}
	return row
}
