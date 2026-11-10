package main

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	appcontext "github.com/darksworm/argonaut/pkg/context"
	"github.com/darksworm/argonaut/pkg/kargo"
	"github.com/darksworm/argonaut/pkg/model"
)

// One choice field is editable: the Stage or Freight from which the user opened
// the form stays fixed. Origin is restored on cancel, including a detail pager.
type kargoPromotionForm struct {
	Origin        kargoLocation
	Kind, Query   string
	Choices       []kargo.Resource
	Sources       map[string]string
	Cursor, Focus int // choice, Cancel, Promote, promotion mode
}

type kargoFormLoadedMsg struct {
	Epoch, Sequence int
	Target          kargoPromotion
	Identity        string
	Items           []kargo.Resource
	Sources         map[string]string
	Error           error
}

func (m *Model) loadKargoForm(force bool) tea.Cmd {
	st := m.kargo
	if st.Form == nil || st.Draft == nil || st.Client == nil {
		return nil
	}
	m.cancelKargoLoad()
	target, kind, epoch, seq, client := *st.Draft, st.Form.Kind, m.switchEpoch, st.Sequence, st.Client
	identity := st.Connections[st.Connection].SessionIdentity()
	cacheKey := kargoTarget{Connection: target.Connection, Kind: kind, Project: target.Project, Stage: target.Stage}
	if kind == "Stage" {
		cacheKey.Stage = ""
	}
	cached, warm := st.Cache[cacheKey]
	warm = warm && !force && m.now().Sub(cached.At) < 2*time.Minute
	if warm && !target.FromUpstream {
		m.setKargoFormChoices(cached.Items)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	st.Cancel, st.Loading = cancel, true
	return func() tea.Msg {
		defer cancel()
		msg := kargoFormLoadedMsg{Epoch: epoch, Sequence: seq, Target: target, Identity: identity}
		if target.FromUpstream {
			readCtx, done := appcontext.WithAPITimeout(ctx)
			stages, err := client.List(readCtx, "Stage", target.Project, "", "")
			done()
			if err != nil {
				msg.Error = err
				return msg
			}
			msg.Sources = kargoUpstreamFreight(stages, target.Project, target.Stage)
		}
		if warm {
			msg.Items = cached.Items
		} else {
			readCtx, done := appcontext.WithAPITimeout(ctx)
			msg.Items, msg.Error = client.List(readCtx, kind, target.Project, cacheKey.Stage, "")
			done()
		}
		return msg
	}
}

func (m *Model) handleKargoFormLoaded(msg kargoFormLoadedMsg) (tea.Model, tea.Cmd) {
	st := m.kargo
	if st == nil || m.state.Navigation.View != model.ViewKargo || st.Form == nil || st.Draft == nil || st.Executing || msg.Epoch != m.switchEpoch || msg.Sequence != st.Sequence || msg.Target != *st.Draft || msg.Identity != st.Connections[st.Connection].SessionIdentity() {
		return m, nil
	}
	st.Loading, st.Cancel = false, nil
	if msg.Error != nil {
		st.Error = inspectionPlainText(msg.Error.Error())
		if kargo.NeedsLogin(msg.Error) && st.Connections[st.Connection].AutoRelogin {
			return m, m.beginKargoLogin()
		}
		return m, nil
	}
	key := kargoTarget{Connection: msg.Target.Connection, Kind: st.Form.Kind, Project: msg.Target.Project, Stage: msg.Target.Stage}
	if st.Form.Kind == "Stage" {
		key.Stage = ""
	}
	st.Cache[key] = kargoCacheEntry{msg.Items, m.now()}
	st.Form.Sources = msg.Sources
	if msg.Target.FromUpstream {
		var current []kargo.Resource
		for _, item := range msg.Items {
			if msg.Sources[item.Name] != "" {
				current = append(current, item)
			}
		}
		msg.Items = current
	}
	m.setKargoFormChoices(msg.Items)
	return m, nil
}

func (m *Model) kargoFormChoices() []kargo.Resource {
	var choices []kargo.Resource
	for _, r := range m.kargo.Form.Choices {
		if strings.Contains(strings.ToLower(r.Name+" "+kargoFreightAlias(r)+" "+kargoSources(r)), strings.ToLower(m.kargo.Form.Query)) {
			choices = append(choices, r)
		}
	}
	return choices
}

func (m *Model) setKargoFormChoices(items []kargo.Resource) {
	m.kargo.Form.Choices = items
	m.chooseKargoFormItem()
}

func (m *Model) chooseKargoFormItem() {
	f, draft := m.kargo.Form, m.kargo.Draft
	items := m.kargoFormChoices()
	f.Cursor = max(0, min(f.Cursor, len(items)-1))
	value := ""
	if len(items) > 0 {
		value = items[f.Cursor].Name
	}
	if f.Kind == "Stage" {
		draft.Stage = value
	} else {
		draft.Freight = value
	}
}

func (m *Model) cancelKargoForm() {
	st := m.kargo
	origin := st.Form.Origin
	st.Draft, st.Form, st.Confirm = nil, nil, false
	m.restoreKargoLocation(origin)
}

func (m *Model) handleKargoFormKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	st, key := m.kargo, msg.String()
	f := st.Form
	switch key {
	case "esc":
		m.cancelKargoForm()
		return m, m.nextKargoRefresh()
	case "tab", "shift+tab":
		order := []int{0, 1, 2}
		if f.Kind == "" {
			order = []int{1, 2}
		} else if f.Kind == "Freight" {
			order = []int{3, 0, 1, 2}
		}
		index := 0
		for i, focus := range order {
			if focus == f.Focus {
				index = i
			}
		}
		delta := 1
		if key == "shift+tab" {
			delta = len(order) - 1
		}
		f.Focus = order[(index+delta)%len(order)]
	case "enter":
		switch f.Focus {
		case 0:
			f.Focus = 1
		case 1:
			m.cancelKargoForm()
			return m, m.nextKargoRefresh()
		case 2:
			return m, m.executeKargoPromotion()
		case 3:
			f.Focus = 0
		}
	case "ctrl+p", "ctrl+t":
		if (st.Draft.RetryOf == "" && key == "ctrl+p") || (st.Draft.RetryOf != "" && key == "ctrl+t") {
			return m, m.executeKargoPromotion()
		}
	case "ctrl+r":
		if f.Kind != "" {
			return m, m.loadKargoForm(true)
		}
	case "ctrl+l":
		return m, m.beginKargoLogin()
	case "ctrl+u":
		return m, m.switchKargoPromotionMode(true)
	case "left", "right":
		if f.Focus == 3 {
			return m, m.switchKargoPromotionMode(key == "right")
		}
	case "down", "up":
		if f.Focus == 0 && f.Kind != "" && !st.Loading {
			if key == "down" {
				f.Cursor++
			} else {
				f.Cursor--
			}
			m.chooseKargoFormItem()
		}
	case "backspace":
		if f.Focus == 0 && len(f.Query) > 0 && !st.Loading {
			r := []rune(f.Query)
			f.Query = string(r[:len(r)-1])
			f.Cursor = 0
			m.chooseKargoFormItem()
		}
	default:
		if f.Focus == 0 && !st.Loading && len([]rune(key)) == 1 {
			f.Query += key
			f.Cursor = 0
			m.chooseKargoFormItem()
		}
	}
	return m, nil
}

func (m *Model) switchKargoPromotionMode(upstream bool) tea.Cmd {
	st := m.kargo
	if st.Form.Kind != "Freight" || st.Draft.FromUpstream == upstream {
		return nil
	}
	m.cancelKargoLoad()
	st.Draft.FromUpstream, st.Draft.Freight = upstream, ""
	st.Form.Query, st.Form.Cursor, st.Form.Choices, st.Form.Sources = "", 0, nil, nil
	st.Error = ""
	return m.loadKargoForm(false)
}

func (m *Model) renderKargoForm(width int) []string {
	st, f, draft := m.kargo, m.kargo.Form, m.kargo.Draft
	label := func(name, value string) string {
		return lipgloss.NewStyle().Foreground(cyanBright).Bold(true).Render(name+": ") + kargoSingleLine(value)
	}
	lines := []string{lipgloss.NewStyle().Foreground(syncedColor).Bold(true).Render("PROMOTE FREIGHT"), label("Instance", draft.Connection), label("Server", draft.Server), label("Project", draft.Project), label("Stage", draft.Stage), label("Freight", draft.Freight)}
	if draft.RetryOf != "" {
		lines[0] = lipgloss.NewStyle().Foreground(progressColor).Bold(true).Render("RETRY PROMOTION")
		lines = append(lines, label("Previous", draft.RetryOf))
	}
	if f.Kind == "Freight" {
		normal, upstream := "Promote", "Promote from upstream"
		active := lipgloss.NewStyle().Foreground(syncedColor).Bold(true)
		if draft.FromUpstream {
			upstream = active.Render("[ " + upstream + " ]")
		} else {
			normal = active.Render("[ " + normal + " ]")
		}
		mode := "Mode: " + normal + " | " + upstream
		if f.Focus == 3 {
			mode = lipgloss.NewStyle().Foreground(cyanBright).Bold(true).Render("> " + mode)
		}
		lines = append(lines, mode)
	}
	if f.Kind != "" {
		field := "Choose " + f.Kind + " / filter: " + f.Query
		if f.Focus == 0 {
			field = lipgloss.NewStyle().Foreground(cyanBright).Bold(true).Render("> " + field)
		}
		lines = append(lines, field)
		if st.Loading {
			lines = append(lines, "Loading choices...")
		} else {
			choices := m.kargoFormChoices()
			if len(choices) == 0 {
				text := "No matching choices"
				if draft.FromUpstream {
					text = "No current upstream Freight eligible for this Stage"
				}
				lines = append(lines, lipgloss.NewStyle().Foreground(progressColor).Render(text))
			}
			// Keep the active option visible even in a short terminal.
			if len(choices) > 0 {
				r := choices[f.Cursor]
				text := fmt.Sprintf("> (%d/%d) %s  %s  %s", f.Cursor+1, len(choices), r.Name, kargoFreightAlias(r), kargoSources(r))
				lines = append(lines, lipgloss.NewStyle().Foreground(whiteBright).Bold(true).Render(text))
				if draft.FromUpstream {
					lines = append(lines, label("Upstream", f.Sources[r.Name]))
				}
			}
		}
	}
	button := func(text string, focus int, buttonColor color.Color) string {
		if focus == 2 && (st.Loading || draft.Stage == "" || draft.Freight == "") {
			buttonColor = dimColor
		}
		style := lipgloss.NewStyle().Foreground(buttonColor).Bold(true)
		if f.Focus == focus {
			style = style.Background(buttonColor).Foreground(lipgloss.Color("0"))
			text = "> " + text + " <"
		}
		return style.Render("[ " + text + " ]")
	}
	action := "Promote"
	if draft.RetryOf != "" {
		action = "Retry"
	}
	if draft.FromUpstream {
		action = "Promote from upstream"
	}
	lines = append(lines, "", button("Cancel", 1, redColor)+"    "+button(action, 2, syncedColor))
	if st.Executing {
		lines = append(lines, lipgloss.NewStyle().Foreground(progressColor).Bold(true).Render("Submitting once... waiting for the Kargo API response"))
	}
	return lines
}

// Render a snapshot of the originating screen without changing live form or
// request state. Retry keeps its describe/YAML pager behind the popup as well.
func (m *Model) kargoFormBackdrop() string {
	copyModel, state, st := *m, *m.state, *m.kargo
	loc := st.Form.Origin
	st.Kind, st.Project, st.Stage, st.Warehouse = loc.Kind, loc.Project, loc.Stage, loc.Warehouse
	st.Items, st.Cursor, st.Filter, st.Detail, st.DetailKind = loc.Items, loc.Cursor, loc.Filter, loc.Detail, loc.DetailKind
	st.Form, st.Draft, st.Confirm, st.Executing, st.Loading, st.Error = nil, nil, false, false, false, ""
	state.Mode, state.Diff = model.ModeNormal, nil
	if loc.Diff != nil {
		diff := *loc.Diff
		state.Diff, state.Mode = &diff, model.ModeDiff
	}
	copyModel.kargo, copyModel.state = &st, &state
	return copyModel.renderKargoBaseView()
}

func (m *Model) renderKargoPromotionPopup() string {
	boxWidth := max(1, min(90, m.state.Terminal.Cols-6))
	innerWidth := max(1, boxWidth-4)
	lines := m.renderKargoForm(innerWidth)
	footerCount := 2
	if m.kargo.Executing {
		footerCount = 3
	}
	footer := lines[max(0, len(lines)-footerCount):]
	lines = lines[:max(0, len(lines)-footerCount)]
	var body []string
	for _, line := range lines {
		body = append(body, wrapAnsiToWidth(line, innerWidth)...)
	}
	help := "Tab focus | Enter activate | Esc cancel"
	if m.kargo.Form.Focus == 0 && m.kargo.Form.Kind != "" {
		help = "Up/Down choose | Tab focus | Enter OK | Esc cancel"
	}
	if m.kargo.Form.Focus == 3 {
		help = "<-/-> mode | Tab focus | Enter OK | Esc cancel"
	}
	footer = append(footer, lipgloss.NewStyle().Foreground(dimColor).Render(help))
	if m.kargo.Error != "" {
		footer = append(footer, lipgloss.NewStyle().Foreground(redColor).Render(kargoSingleLine(m.kargo.Error)))
	}
	available := max(1, m.state.Terminal.Rows-6-len(footer))
	if len(body) > available {
		// Preserve the title; the selected option and source remain beside buttons.
		body = append(body[:1], body[len(body)-max(0, available-1):]...)
	}
	lines = append(body, footer...)
	for i := range lines {
		lines[i] = padRight(clipAnsiToWidth(lines[i], innerWidth), innerWidth)
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cyanBright).
		Background(currentPalette.DarkBG).Foreground(whiteBright).Padding(0, 1).
		Width(boxWidth).Render(strings.Join(lines, "\n"))
}
