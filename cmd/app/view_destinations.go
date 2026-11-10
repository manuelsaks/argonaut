package main

import (
	"fmt"

	"charm.land/lipgloss/v2"
	"github.com/darksworm/argonaut/pkg/model"
)

type appColumnWidths struct{ context, name, owner, sync, health, destination int }

func (m *Model) appListColumns() appColumnWidths {
	width := m.contentInnerWidth()
	desired := []int{9, 5, 5, 7, 23, 0}
	counts := make(map[string]int, len(m.state.Apps))
	for _, app := range m.state.Apps {
		counts[app.Name]++
	}
	for _, app := range m.state.Apps {
		name := app.Name
		if counts[name] > 1 {
			name = model.AppKey(name, app.AppNamespace)
		}
		values := []string{name, applicationOwner(app), m.getSyncIcon(app.Sync) + " " + app.Sync, m.getHealthIcon(app.Health) + " " + app.Health, m.applicationLocation(app), app.ContextName}
		for i, value := range values {
			desired[i] = max(desired[i], lipgloss.Width(value))
		}
	}
	minimum := []int{min(16, desired[0]), min(12, desired[1]), desired[2], desired[3], 12, 0}
	if width < 75 {
		desired[2], desired[3], minimum[2], minimum[3] = 2, 2, 2, 2
	}
	if m.allContexts == nil {
		desired = desired[:5]
		minimum = minimum[:5]
	} else {
		desired[5] = max(7, desired[5])
		minimum[5] = min(10, desired[5])
	}
	widths := fitTableColumns(width-len(desired)+1, desired, minimum, []int{8, 3, 1, 1, 2, 2})
	w := appColumnWidths{name: widths[0], owner: widths[1], sync: widths[2], health: widths[3], destination: widths[4]}
	if len(widths) > 5 {
		w.context = min(widths[5], desired[5])
		w.destination += widths[5] - w.context
	}
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

type dimensionColumnWidths struct{ context, name, count, location int }

func (m *Model) dimensionColumns() dimensionColumnWidths {
	if m.state.Navigation.View != model.ViewClusters && m.state.Navigation.View != model.ViewApplicationSets {
		return dimensionColumnWidths{}
	}
	desired := []int{11, 4, 23, 0}
	for _, item := range m.getVisibleItems() {
		key, ok := item.(string)
		if !ok || key == allServersChoice {
			continue
		}
		ctx, name, count, location := m.dimensionValues(key)
		for i, value := range []string{name, count, location, ctx} {
			desired[i] = max(desired[i], lipgloss.Width(value))
		}
	}
	minimum := []int{min(18, desired[0]), 4, 12, 0}
	if m.allContexts == nil {
		desired, minimum = desired[:3], minimum[:3]
	} else {
		desired[3] = max(7, desired[3])
		minimum[3] = min(10, desired[3])
	}
	widths := fitTableColumns(m.contentInnerWidth()-len(desired)+1, desired, minimum, []int{8, 1, 2, 2})
	w := dimensionColumnWidths{name: widths[0], count: widths[1], location: widths[2]}
	if len(widths) > 3 {
		w.context = min(widths[3], desired[3])
		w.location += widths[3] - w.context
	}
	return w
}

func dimensionColumnRow(w dimensionColumnWidths, ctx, name, count, location string) string {
	row := tableCell(name, w.name) + " " + tableCell(count, w.count) + " " + tableCell(location, w.location)
	if w.context > 0 {
		row = tableCell(ctx, w.context) + " " + row
	}
	return row
}

func (m *Model) dimensionValues(key string) (ctx, name, count, location string) {
	switch m.state.Navigation.View {
	case model.ViewClusters:
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
			appCount := 0
			if m.state.Index != nil {
				appCount = len(m.state.Index.ByCluster[key])
			}
			count = fmt.Sprint(appCount)
			location = address
		}
	case model.ViewApplicationSets:
		appCount, where := m.appSetLocation(key)
		contextName, localKey := model.SplitContextKey(key)
		ctx = contextName
		name = localKey
		count = fmt.Sprint(appCount)
		location = where
	}
	return
}

func (m *Model) renderDimensionRow(key string, cursor bool, w dimensionColumnWidths) string {
	if m.state.Navigation.View == model.ViewClusters && key == allServersChoice {
		return m.renderSimpleRow("all  (all destination servers)", cursor)
	}
	ctx, name, count, location := m.dimensionValues(key)
	if name == "" {
		return m.renderSimpleRow(key, cursor)
	}
	row := dimensionColumnRow(w, ctx, name, count, location)
	if cursor && !m.willDesaturateBase() {
		return selectedStyle.Render(row)
	}
	return row
}
