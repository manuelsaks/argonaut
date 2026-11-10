package main

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/darksworm/argonaut/pkg/model"
)

// renderListView - custom list/table rendering with fixed inner width
func (m *Model) renderListView(availableRows int) string {
	visibleItems := m.getVisibleItems()

	contentWidth := max(0, m.contentInnerWidth())
	// Leave room for the table header row inside the bordered area
	tableHeight := max(3, availableRows-1)

	// Prepare data and update the appropriate table directly
	var tableView string

	// Handle empty state - let it flow through normal rendering but with empty tableView
	if len(visibleItems) == 0 {
		tableView = "" // Empty table view will render clean empty space
	} else {

		switch m.state.Navigation.View {
		case model.ViewApps:
			// Custom-render apps list to restore full-row selection highlight and per-cell colors
			// Use edge-based scrolling with ListNavigator
			total := len(visibleItems)
			visibleRows := max(0, tableHeight-1) // leave 1 line for header
			if visibleRows <= 0 {
				visibleRows = 0
			}
			cursor := m.state.Navigation.SelectedIdx
			if cursor < 0 {
				cursor = 0
			}
			if cursor >= total {
				cursor = max(0, total-1)
			}

			// Use the list navigator's scroll offset
			start := m.listNav.ScrollOffset()
			// Clamp start to valid range
			if start < 0 {
				start = 0
			}
			if start > max(0, total-visibleRows) {
				start = max(0, total-visibleRows)
			}
			end := min(total, start+visibleRows)

			// Build header + rows
			var b strings.Builder
			b.WriteString(m.renderListHeader())
			b.WriteString("\n")
			for i := start; i < end; i++ {
				app := visibleItems[i].(model.App)
				isCursor := (i == cursor)
				b.WriteString(m.renderAppRow(app, isCursor))
				if i < end-1 {
					b.WriteString("\n")
				}
			}
			// Pad remaining lines to maintain fixed height inside border
			for pad := end - start; pad < visibleRows; pad++ {
				b.WriteString("\n")
			}
			tableView = b.String()

		case model.ViewClusters, model.ViewNamespaces, model.ViewProjects, model.ViewApplicationSets, model.ViewContexts:
			// Custom-render single-column lists with full-row highlight
			total := len(visibleItems)
			visibleRows := max(0, tableHeight-1)
			cursor := m.state.Navigation.SelectedIdx
			if cursor < 0 {
				cursor = 0
			}
			if cursor >= total {
				cursor = max(0, total-1)
			}

			// Use the list navigator's scroll offset
			start := m.listNav.ScrollOffset()
			// Clamp start to valid range
			if start < 0 {
				start = 0
			}
			if start > max(0, total-visibleRows) {
				start = max(0, total-visibleRows)
			}
			end := min(total, start+visibleRows)

			var b strings.Builder
			b.WriteString(m.renderListHeader())
			b.WriteString("\n")
			for i := start; i < end; i++ {
				label := fmt.Sprintf("%v", visibleItems[i])
				isCursor := (i == cursor)
				b.WriteString(m.renderDimensionRow(label, isCursor))
				if i < end-1 {
					b.WriteString("\n")
				}
			}
			for pad := end - start; pad < visibleRows; pad++ {
				b.WriteString("\n")
			}
			tableView = b.String()
		}
	}

	// Render the table/content ensuring each line fits the content width
	var content strings.Builder
	content.WriteString(normalizeLinesToWidth(tableView, contentWidth))

	// Apply border style with proper width. For empty content, set fixed height to fill space
	if tableView == "" {
		// Empty state: use fixed height to fill available space like other views
		// Adjust width to properly fill horizontal space
		adjustedWidth := max(0, m.state.Terminal.Cols-2) // Expand width to fill space
		return contentBorderStyle.Width(adjustedWidth).Height(availableRows + 1).AlignVertical(lipgloss.Center).Render(content.String())
	}

	// Non-empty content: let height auto-size to content to avoid tmux line-wrapping issues
	return contentBorderStyle.Render(content.String())
}

// renderListHeader - matches ListView header row with responsive widths
func (m *Model) renderListHeader() string {
	if m.state.Navigation.View == model.ViewApps {
		w := appColumns(m.contentInnerWidth())
		name, sync, health := "APP NAME", "SYNC", "HEALTH"
		indicator := m.state.UI.Sort.Direction.Indicator()
		switch m.state.UI.Sort.Field {
		case model.SortFieldName:
			name = indicator + name
		case model.SortFieldSync:
			sync = indicator + sync
		case model.SortFieldHealth:
			health = indicator + health
		}
		if w.sync < 5 {
			sync = "S"
		}
		if w.health < 7 {
			health = "H"
		}
		return headerStyle.Render(appColumnRow(w, name, "OWNER", sync, health, "DESTINATION / NAMESPACE"))
	}
	if m.state.Navigation.View == model.ViewClusters {
		if !m.destinationLoaded || m.destinationError != "" || m.destinationLoading {
			return headerStyle.Render(clipAnsiToWidth(m.destinationListHeading(), m.contentInnerWidth()))
		}
		return headerStyle.Render(m.dimensionRow("SERVER NAME", "APPS", "ADDRESS / PROJECT"))
	}
	if m.state.Navigation.View == model.ViewApplicationSets {
		return headerStyle.Render(m.dimensionRow("APPSET NAME", "APPS", "DESTINATION / NAMESPACE"))
	}

	// Simple header for other views padded to full content width
	contentWidth := m.contentInnerWidth()
	heading := "NAME"
	if m.state.Navigation.View == model.ViewContexts {
		heading = "ARGO CD CONTEXTS (Ctrl+A: auto-login, Ctrl+L: SSO login)"
	}
	hdr := headerStyle.Render(heading)
	if lipgloss.Width(hdr) < contentWidth {
		hdr = padRight(hdr, contentWidth)
	} else if lipgloss.Width(hdr) > contentWidth {
		hdr = clipAnsiToWidth(hdr, contentWidth)
	}
	return hdr
}

// renderAppRow - matches ListView app row rendering
func (m *Model) renderAppRow(app model.App, isCursor bool) string {
	// Selection checking (matches ListView isChecked logic)
	isSelected := m.state.Selections.HasSelectedApp(app.Key())
	// While a desaturating modal is up, suppress the cursor-only highlight
	// so unrelated rows don't appear bright behind the popup. Truly
	// selected rows still keep their bg so the user can see what's
	// being acted on.
	desaturating := m.willDesaturateBase()
	if desaturating {
		isCursor = false
	}
	active := isCursor || isSelected

	w := appColumns(m.contentInnerWidth())
	fullRowWidth := m.contentInnerWidth()
	syncText := fmt.Sprintf("%s %s", m.getSyncIcon(app.Sync), app.Sync)
	healthText := fmt.Sprintf("%s %s", m.getHealthIcon(app.Health), app.Health)
	if w.sync < 5 {
		syncText = m.getSyncIcon(app.Sync)
	}
	if w.health < 7 {
		healthText = m.getHealthIcon(app.Health)
	}
	if !active {
		syncText = m.getColorForStatus(app.Sync).Render(syncText)
		healthText = m.getColorForStatus(app.Health).Render(healthText)
	}
	name := app.Name
	// Namespaces identify same-named Applications, not their deployment targets.
	for _, other := range m.state.Apps {
		if other.Name == app.Name && other.Key() != app.Key() {
			name = app.Key()
			break
		}
	}
	row := appColumnRow(w, name, applicationOwner(app), syncText, healthText, m.applicationLocation(app))

	// Check if this app should flash (refresh feedback). Suppress
	// flash highlight while a desaturating modal is up — a transient
	// success-color flash on an unrelated row reads as "this random
	// app is highlighted" against the dimmed base.
	isFlashing := m.state.UI.RefreshFlashApps != nil && m.state.UI.RefreshFlashApps[app.Name] && !desaturating

	// Apply highlight: use a distinct style when cursor overlaps a selected row
	if isFlashing {
		row = refreshFlashStyle.Render(row)
	} else if isCursor && isSelected {
		row = cursorOnSelectedStyle.Render(row)
	} else if active { // cursor or selected
		row = selectedStyle.Render(row)
	}
	// After styling, clip again defensively (some terminals render bold differently)
	if lipgloss.Width(row) > fullRowWidth {
		row = clipAnsiToWidth(row, fullRowWidth)
	}

	return row
}

// renderSimpleRow - matches ListView non-app row rendering
func (m *Model) renderSimpleRow(label string, isCursor bool) string {
	// Check if selected based on view (matches ListView isChecked logic)
	isSelected := false
	switch m.state.Navigation.View {
	case model.ViewClusters:
		isSelected = m.state.Selections.HasCluster(label)
	case model.ViewNamespaces:
		isSelected = m.state.Selections.HasNamespace(label)
	case model.ViewProjects:
		isSelected = m.state.Selections.HasProject(label)
	case model.ViewContexts:
		contextName := label
		if label == m.currentContextName {
			label = "* " + label + " (active)"
		} else {
			label = "  " + label
		}
		if m.config.IsAutoReloginEnabled(contextName) {
			label += "  [auto-login: ON]"
		} else {
			label += "  [auto-login: OFF]"
		}
	}

	if m.willDesaturateBase() {
		isCursor = false
	}
	active := isCursor || isSelected

	// Calculate available width for simple rows (full content width minus padding)
	contentWidth := m.contentInnerWidth()

	// Truncate and pad label to full width
	truncatedLabel := truncateWithEllipsis(label, contentWidth)
	row := padRight(truncatedLabel, contentWidth)

	// Apply selection highlight if active
	if active {
		return selectedStyle.Render(row)
	}
	return row
}
