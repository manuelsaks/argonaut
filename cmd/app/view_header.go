package main

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/darksworm/argonaut/pkg/model"
)

type viewHotkey struct{ key, action string }

func (m *Model) viewHotkeys() []viewHotkey {
	switch m.state.Mode {
	case model.ModeSearch:
		return []viewHotkey{{"enter", "apply"}, {"esc", "cancel"}, {"up/down", "select"}}
	case model.ModeCommand:
		return []viewHotkey{{"enter", "run"}, {"tab", "complete"}, {"esc", "cancel"}}
	case model.ModeHelp:
		return []viewHotkey{{"esc/?", "close help"}, {"ctrl-c", "quit"}}
	case model.ModeAuthRequired, model.ModeConnectionError, model.ModeError:
		return []viewHotkey{{"ctrl-l", "SSO login"}, {"c", "contexts"}, {"q", "quit"}}
	case model.ModeDiff:
		if m.inspection != nil {
			return []viewHotkey{{"d", "describe"}, {"y", "YAML"}, {"ctrl-y", "copy"}, {"tab", "describe/diff"}, {"ctrl-r", "reload"}, {"j/k", "scroll"}, {"h/l", "pan"}, {"/", "find"}, {"esc/q", "back"}}
		}
		return []viewHotkey{{"j/k", "scroll"}, {"h/l", "pan"}, {"/", "filter"}, {"esc/q", "close"}}
	case model.ModeConfirmSync:
		if m.state.Modals.ConfirmSyncForcePending {
			return []viewHotkey{{"enter", "confirm force"}, {"h/l", "choose"}, {"esc", "back"}}
		}
		return []viewHotkey{{"enter", "confirm"}, {"p", "prune"}, {"f", "force"}, {"d", "dry-run"}, {"w", "watch"}, {"esc", "cancel"}}
	case model.ModeConfirmAppDelete:
		return []viewHotkey{{"y", "delete"}, {"c", "cascade"}, {"p", "policy"}, {"esc", "cancel"}}
	case model.ModeConfirmResourceDelete:
		return []viewHotkey{{"y", "delete"}, {"c", "cascade"}, {"p", "policy"}, {"f", "force"}, {"esc", "cancel"}}
	case model.ModeConfirmResourceSync:
		return []viewHotkey{{"enter", "confirm"}, {"h/l", "choose"}, {"p", "prune"}, {"f", "force"}, {"esc", "cancel"}}
	case model.ModeConfirmTerminate:
		return []viewHotkey{{"enter", "confirm"}, {"h/l", "choose"}, {"esc", "cancel"}}
	case model.ModeResourceAction:
		return []viewHotkey{{"j/k", "select"}, {"enter", "run action"}, {"esc", "cancel"}}
	case model.ModeRollback:
		if m.state.Rollback != nil && m.state.Rollback.Mode == "confirm" {
			return []viewHotkey{{"enter", "confirm"}, {"p", "prune"}, {"w", "watch"}, {"h/l", "choose"}, {"esc", "back"}}
		}
		return []viewHotkey{{"enter", "select revision"}, {"d", "revision diff"}, {"u/i", "scroll details"}, {"esc", "apps"}}
	case model.ModeLoading:
		return []viewHotkey{{"c", "contexts"}, {"ctrl-l", "SSO login"}, {"ctrl-c", "quit"}}
	}
	if m.state.Navigation.View == model.ViewContexts {
		back := viewHotkey{"esc", "apps"}
		if m.contextNeedsLoad {
			back = viewHotkey{"ctrl-c", "quit"}
		}
		return []viewHotkey{{"enter", "switch"}, {"ctrl-a", "auto-login on/off"}, {"ctrl-l", "SSO login"}, {"/", "filter"}, back}
	}
	if m.state.Navigation.View == model.ViewTree {
		return []viewHotkey{{"enter/e", "events"}, {"h/l", "fold/unfold"}, {"space", "select"}, {"ctrl-s", "sync"}, {"d", "describe"}, {"y", "YAML"}, {"ctrl-r", "refresh"}, {"ctrl-a", "actions"}, {"ctrl-d", "delete"}, {"ctrl-b", "rollback"}, {"ctrl-t", "terminate"}, {"ctrl-k", "k9s"}, {"u/i", "scroll events"}, {"esc", "back"}}
	}
	if m.state.Navigation.View == model.ViewApps {
		return []viewHotkey{{"enter/r", "resources"}, {"space", "select"}, {"ctrl-s", "sync"}, {"d", "describe"}, {"y", "YAML"}, {"ctrl-r", "refresh"}, {"ctrl-b", "rollback"}, {"ctrl-d", "delete"}, {"ctrl-t", "terminate"}, {"ctrl-k", "k9s"}, {"/", "filter"}, {"esc", "clear/back"}}
	}
	if m.state.Navigation.View == model.ViewApplicationSets {
		return []viewHotkey{{"enter", "generated apps"}, {"d", "describe"}, {"y", "YAML"}, {"/", "filter"}, {"esc", "back"}}
	}
	if m.state.Navigation.View == model.ViewClusters {
		return []viewHotkey{{"enter", "server apps"}, {"ctrl-r", "reload servers"}, {"/", "filter name/address"}, {"esc", "back"}}
	}
	return []viewHotkey{{"enter", "open"}, {"/", "filter"}, {"esc", "back"}, {"j/k", "select"}}
}

func (m *Model) headerViewName() string {
	switch m.state.Mode {
	case model.ModeDiff:
		if m.state.Diff != nil && m.state.Diff.InspectionKind != "" {
			return m.state.Diff.InspectionKind
		}
		return "Diff"
	case model.ModeRollback:
		return "Deployment history"
	case model.ModeAuthRequired:
		return "Login required"
	case model.ModeConnectionError:
		return "Connection error"
	case model.ModeHelp:
		return "Help"
	}
	switch m.state.Navigation.View {
	case model.ViewApps:
		return "Applications"
	case model.ViewTree:
		if m.state.UI.TreeApp != nil {
			return "Resources / " + m.state.UI.TreeApp.Name
		}
		return "Resources"
	case model.ViewContexts:
		return "Argo CD contexts"
	case model.ViewClusters:
		return "Destination servers"
	case model.ViewNamespaces:
		return "Namespaces"
	case model.ViewProjects:
		return "Projects"
	case model.ViewApplicationSets:
		return "ApplicationSets"
	}
	return string(m.state.Navigation.View)
}

func hotkeyColor(action string) color.Color {
	action = strings.ToLower(action)
	switch {
	case strings.Contains(action, "delete"), strings.Contains(action, "terminate"):
		return redColor
	case strings.Contains(action, "retry"), strings.Contains(action, "force"):
		return progressColor
	case strings.Contains(action, "promot"), strings.Contains(action, "sync"), strings.Contains(action, "confirm"):
		return syncedColor
	default:
		return cyanBright
	}
}

func renderHotkey(k viewHotkey) string {
	style := lipgloss.NewStyle().Foreground(hotkeyColor(k.action))
	return style.Bold(true).Render("<"+k.key+">") + " " + style.Render(k.action)
}

func (m *Model) renderK9sHeader() string {
	width := max(1, m.state.Terminal.Cols-2)
	ctx := m.currentContextName
	if ctx == "" {
		ctx = "no context"
		if m.state.Server != nil {
			ctx = hostFromURL(m.state.Server.BaseURL)
		}
	}
	server := "not connected"
	if m.state.Server != nil {
		server = hostFromURL(m.state.Server.BaseURL)
	}
	if m.allContexts != nil {
		ctx = "all"
		server = "all Argo CD instances"
	}
	ns, project := m.effectiveNamespaceProjectScope()
	if ns == "—" {
		ns = "all"
	}
	if project == "—" {
		project = "all"
	}
	login := "manual"
	if m.config.IsAutoReloginEnabled(m.currentContextName) {
		login = "auto SSO"
	}
	if m.allContexts != nil {
		login = "per context"
	}
	if m.reloginInProgress {
		login = "signing in..."
	}
	meta := []string{"Context: " + ctx, "Server:  " + server, "View:    " + m.headerViewName(), "Scope:   " + ns + " / " + project, "Login:   " + login}
	if m.currentUsername != "" {
		meta[4] += " (" + m.currentUsername + ")"
	}
	if m.config.IsLowBandwidth() {
		label := "manual refresh"
		if interval := m.config.GetNetworkRefreshInterval(); interval > 0 {
			label = "poll " + interval.String()
		}
		meta = append(meta, "Network: low bandwidth / "+label)
	}
	if len(m.state.Selections.ScopeClusters) > 0 {
		meta = append(meta, "Target:  "+m.destinationScopeLabel())
	}
	keyStyle := lipgloss.NewStyle().Foreground(cyanBright).Bold(true)
	title := keyStyle.Render("Context: ") + ctx
	if m.config.IsLowBandwidth() {
		title += " [slow link]"
	}
	brand := lipgloss.NewStyle().Foreground(magentaBright).Bold(true).Render("Argonaut " + appVersion)
	actions := m.viewHotkeys()
	var rows []string
	if width >= 105 && m.state.Terminal.Rows > 22 {
		leftWidth := min(42, width/3)
		brandWidth := lipgloss.Width(brand)
		legendWidth := width - leftWidth - 3 - brandWidth - 2
		// K9s-style columns: context on the left, view actions beside it.
		cells := 2
		if legendWidth >= 78 {
			cells = 3
		}
		cellWidth := legendWidth / cells
		columnRows := (len(actions) + cells - 1) / cells
		for i := 0; i < max(len(meta), columnRows); i++ {
			left := ""
			if i < len(meta) {
				left = meta[i]
			}
			line := padRight(truncateWithEllipsis(left, leftWidth), leftWidth) + "   "
			for col := 0; col < cells; col++ {
				n := i + col*columnRows
				cell := ""
				if i < columnRows && n < len(actions) {
					cell = renderHotkey(actions[n])
				}
				line += padRight(clipAnsiToWidth(cell, cellWidth), cellWidth)
			}
			if i == 0 {
				line = padRight(line, width-brandWidth) + brand
			}
			rows = append(rows, clipAnsiToWidth(line, width))
		}
	} else {
		brandWidth := lipgloss.Width(brand) + 2
		if width > brandWidth+20 {
			title = joinWithRightAlignment(clipAnsiToWidth(title, width-brandWidth), brand, width)
		}
		rows = append(rows, clipAnsiToWidth(title, width))
		rows = append(rows, clipAnsiToWidth(fmt.Sprintf("View: %s | Target: %s | Scope: %s/%s | Login: %s", m.headerViewName(), m.destinationScopeLabel(), ns, project, login), width))
		line := ""
		for _, action := range actions {
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
			rows = append(rows, clipAnsiToWidth(line, width))
		}
		// Keep useful content visible on very small terminals; full help is ?.
		if m.state.Terminal.Rows <= 22 && len(rows) > 4 {
			rows = rows[:4]
		}
	}
	if m.state.Mode == model.ModeNormal {
		nav := []viewHotkey{{"0/c", "ctx"}, {"1", "apps"}, {"2", "servers"}, {"3", "ns"}, {"4", "proj"}, {"5", "sets"}, {"6", "kargo"}, {":", "command"}, {"?", "help"}}
		line := ""
		for _, k := range nav {
			if line != "" {
				line += "  "
			}
			line += renderHotkey(k)
		}
		// On narrow screens retain context/app switching and the help affordance.
		if lipgloss.Width(line) > width {
			line = renderHotkey(nav[0]) + "  " + renderHotkey(nav[1]) + "  " + renderHotkey(viewHotkey{"6", "kargo"}) + "  " + renderHotkey(viewHotkey{":", "views"}) + "  " + renderHotkey(nav[len(nav)-1])
		}
		rows = append(rows, clipAnsiToWidth(line, width))
	}
	for i := range rows {
		rows[i] = padRight(clipAnsiToWidth(rows[i], width), width)
	}
	return strings.Join(rows, "\n")
}
