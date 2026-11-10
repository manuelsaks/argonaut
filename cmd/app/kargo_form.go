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
	Cursor, Focus int // choice, Cancel, Promote; retry uses only the two buttons
}

type kargoFormLoadedMsg struct {
	Epoch, Sequence int
	Target          kargoPromotion
	Identity        string
	Items           []kargo.Resource
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
	if cached, ok := st.Cache[cacheKey]; ok && !force && m.now().Sub(cached.At) < 2*time.Minute {
		m.setKargoFormChoices(cached.Items)
		return nil
	}
	ctx, cancel := appcontext.WithAPITimeout(context.Background())
	st.Cancel, st.Loading = cancel, true
	return func() tea.Msg {
		defer cancel()
		items, err := client.List(ctx, kind, target.Project, cacheKey.Stage, "")
		return kargoFormLoadedMsg{epoch, seq, target, identity, items, err}
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
		delta := 1
		if key == "shift+tab" {
			delta = 2
		}
		f.Focus = (f.Focus + delta) % 3
		if f.Kind == "" && f.Focus == 0 {
			if key == "tab" {
				f.Focus = 1
			} else {
				f.Focus = 2
			}
		}
	case "enter":
		switch f.Focus {
		case 0:
			f.Focus = 1
		case 1:
			m.cancelKargoForm()
			return m, m.nextKargoRefresh()
		case 2:
			return m, m.executeKargoPromotion()
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
				lines = append(lines, "No matching choices")
			}
			// Keep the active option visible even in a short terminal.
			if len(choices) > 0 {
				r := choices[f.Cursor]
				text := fmt.Sprintf("> (%d/%d) %s  %s  %s", f.Cursor+1, len(choices), r.Name, kargoFreightAlias(r), kargoSources(r))
				lines = append(lines, lipgloss.NewStyle().Foreground(whiteBright).Bold(true).Render(text))
			}
		}
	}
	button := func(text string, focus int, buttonColor color.Color) string {
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
	lines = append(lines, "", button("Cancel", 1, redColor)+"    "+button(action, 2, syncedColor))
	if st.Executing {
		lines = append(lines, lipgloss.NewStyle().Foreground(progressColor).Bold(true).Render("Submitting once... waiting for the Kargo API response"))
	}
	return lines
}
