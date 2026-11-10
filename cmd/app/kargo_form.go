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
	Caret         *tea.Cursor
	ManualScroll  bool
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
	f.ManualScroll = false
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
		f.ManualScroll = false
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
		f.ManualScroll = false
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
	case "pgup":
		f.ManualScroll = true
		m.kargo.ConfirmOffset = max(0, m.kargo.ConfirmOffset-3)
	case "pgdown":
		f.ManualScroll = true
		m.kargo.ConfirmOffset += 3
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
	st.Form.ManualScroll = false
	st.Error = ""
	return m.loadKargoForm(false)
}

func (m *Model) renderKargoForm(width int) []string {
	st, f, draft := m.kargo, m.kargo.Form, m.kargo.Draft
	field := func(label, value string) string {
		prefix := lipgloss.NewStyle().Foreground(dimColor).Render(padRight(label+":", 11))
		value = kargoSingleLine(value)
		if lipgloss.Width(value) > width-11 {
			return prefix + "\n" + lipgloss.NewStyle().Foreground(cyanBright).Render(value)
		}
		return prefix + lipgloss.NewStyle().Foreground(cyanBright).Render(value)
	}
	title := lipgloss.NewStyle().Foreground(syncedColor).Bold(true).Render("PROMOTE FREIGHT")
	if draft.RetryOf != "" {
		title = lipgloss.NewStyle().Foreground(progressColor).Bold(true).Render("RETRY PROMOTION")
	}
	lines := []string{title, lipgloss.NewStyle().Foreground(cyanBright).Render(draft.Project + " / " + draft.Stage), "", field("Instance", draft.Connection), field("Server", draft.Server)}
	if f.Kind == "Freight" {
		normal, upstream := "Promote", "Promote from upstream"
		active := lipgloss.NewStyle().Foreground(syncedColor).Underline(true)
		inactive := lipgloss.NewStyle().Foreground(dimColor)
		if draft.FromUpstream {
			normal, upstream = inactive.Render(normal), active.Render(upstream)
		} else {
			normal, upstream = active.Render(normal), inactive.Render(upstream)
		}
		mode := lipgloss.NewStyle().Foreground(dimColor).Render(padRight("Mode:", 11)) + normal + "  " + upstream
		lines = append(lines, mode)
	}
	lines = append(lines, field("Stage", draft.Stage), field("Freight", draft.Freight))
	if draft.RetryOf != "" {
		lines = append(lines, field("Previous", draft.RetryOf))
	}
	if f.Kind != "" {
		if st.Loading {
			lines = append(lines, lipgloss.NewStyle().Foreground(progressColor).Render("Loading choices..."))
		} else {
			choices := m.kargoFormChoices()
			if len(choices) == 0 {
				text := "No matching choices"
				if draft.FromUpstream {
					text = "No eligible current upstream Freight"
				}
				lines = append(lines, lipgloss.NewStyle().Foreground(progressColor).Render(text))
			} else {
				r := choices[f.Cursor]
				if alias := kargoFreightAlias(r); alias != "" {
					lines = append(lines, field("Alias", alias))
				}
				lines = append(lines, field("Choice", fmt.Sprintf("%d / %d  (Up/Down)", f.Cursor+1, len(choices))))
				if sources := kargoSources(r); sources != "" {
					lines = append(lines, field("Revision", sources))
				}
				if draft.FromUpstream {
					lines = append(lines, field("Upstream", f.Sources[r.Name]))
				}
			}
		}
		query := field("Filter", f.Query)
		lines = append(lines, query)
	}
	button := func(text string, focus int, buttonColor color.Color) string {
		if focus == 2 && (st.Loading || draft.Stage == "" || draft.Freight == "") {
			buttonColor = dimColor
		}
		style := lipgloss.NewStyle().Foreground(buttonColor).Padding(0, 1)
		if f.Focus == focus {
			style = style.Background(buttonColor).Foreground(ensureContrastingForeground(buttonColor, whiteBright)).Bold(true)
		}
		return style.Render(text)
	}
	action := "Promote"
	if draft.RetryOf != "" {
		action = "Retry"
	}
	if draft.FromUpstream {
		action = "Promote from upstream"
	}
	lines = append(lines, "", button("Cancel", 1, cyanBright)+"    "+button(action, 2, syncedColor))
	if st.Executing {
		lines = append(lines, lipgloss.NewStyle().Foreground(progressColor).Render("Submitting... waiting for Kargo"))
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
	f := m.kargo.Form
	f.Caret = nil
	boxWidth := max(8, min(72, m.state.Terminal.Cols-8))
	innerWidth := max(1, boxWidth-6)
	lines := m.renderKargoForm(innerWidth)
	// k9s embeds the dialog's name in the frame, keeping the form itself quiet.
	lines = lines[1:]
	footerCount := 2
	if m.kargo.Executing {
		footerCount = 3
	}
	footer := lines[max(0, len(lines)-footerCount):]
	lines = lines[:max(0, len(lines)-footerCount)]
	var body []string
	activeRow, caretColumn := -1, 0
	activeLabel := ""
	if f.Focus == 0 {
		activeLabel = "Filter:"
	}
	if f.Focus == 3 {
		activeLabel = "Mode:"
	}
	for _, line := range lines {
		wrapped := wrapAnsiToWidth(line, innerWidth)
		body = append(body, wrapped...)
		if activeLabel != "" && strings.HasPrefix(strings.TrimSpace(inspectionPlainText(line)), activeLabel) {
			activeRow, caretColumn = len(body)-1, lipgloss.Width(wrapped[len(wrapped)-1])
		}
	}
	var summary []string
	if len(body) > 0 {
		summary = []string{centerDialogLine(body[0], innerWidth)}
		body = body[1:]
		activeRow--
	}
	if m.kargo.Error != "" {
		body = append(body, wrapAnsiToWidth(lipgloss.NewStyle().Foreground(redColor).Render(kargoSingleLine(m.kargo.Error)), innerWidth)...)
	}
	available := max(1, m.state.Terminal.Rows-8-len(footer)-len(summary))
	offset := min(m.kargo.ConfirmOffset, max(0, len(body)-available))
	if !f.ManualScroll && activeRow >= 0 {
		if activeRow < offset {
			offset = activeRow
		}
		if activeRow >= offset+available {
			offset = activeRow - available + 1
		}
	}
	m.kargo.ConfirmOffset = offset
	body = body[offset:min(len(body), offset+available)]
	for i := range footer {
		footer[i] = centerDialogLine(clipAnsiToWidth(footer[i], innerWidth), innerWidth)
	}
	lines = append(append(summary, body...), footer...)
	for i := range lines {
		lines[i] = padRight(clipAnsiToWidth(lines[i], innerWidth), innerWidth)
	}
	rendered := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(k9sFrameColor).
		Background(currentPalette.DarkBG).Foreground(whiteBright).Padding(1, 2).Width(boxWidth).Render(strings.Join(lines, "\n"))
	rows := strings.Split(rendered, "\n")
	title := "Promote"
	titleColor := syncedColor
	if m.kargo.Draft.FromUpstream {
		title = "Promote from upstream"
	}
	if m.kargo.Draft.RetryOf != "" {
		title, titleColor = "Retry Promotion", progressColor
	}
	caption := lipgloss.NewStyle().Foreground(titleColor).Render("<" + title + ">")
	outerWidth := lipgloss.Width(rendered)
	if !m.kargo.Executing && !m.kargo.Loading && activeRow >= offset && activeRow < offset+available {
		x := max(0, (m.state.Terminal.Cols-outerWidth)/2) + 3 + min(caretColumn, innerWidth-1)
		y := max(0, (m.state.Terminal.Rows-lipgloss.Height(rendered))/2) + 2 + len(summary) + activeRow - offset
		f.Caret = tea.NewCursor(x, y)
		f.Caret.Color = syncedColor
	}
	caption = clipAnsiToWidth(caption, max(0, outerWidth-2))
	left := max(0, (outerWidth-2-lipgloss.Width(caption))/2)
	border := lipgloss.NewStyle().Foreground(k9sFrameColor)
	rows[0] = border.Render("┌"+strings.Repeat("─", left)) + caption + border.Render(strings.Repeat("─", max(0, outerWidth-2-left-lipgloss.Width(caption)))+"┐")
	return strings.Join(rows, "\n")
}

func centerDialogLine(line string, width int) string {
	return strings.Repeat(" ", max(0, (width-lipgloss.Width(line))/2)) + line
}
