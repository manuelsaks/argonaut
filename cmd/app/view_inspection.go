package main

import (
	"fmt"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/darksworm/argonaut/pkg/model"
)

// Render from plain source lines: highlighting never changes copied YAML or
// search coordinates, and remote terminal escape sequences are already removed.
func (m *Model) colorInspectionLine(line, kind string) string {
	paint := func(color string, text string) string {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(text)
	}
	if kind == "Diff" {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "@@"):
			return paint("14", line)
		case strings.HasPrefix(line, "+"):
			return paint("10", line)
		case strings.HasPrefix(line, "-"):
			return paint("9", line)
		}
		return line
	}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") {
		return lipgloss.NewStyle().Foreground(dimColor).Render(line)
	}
	// A mapping colon must be outside quotes and followed by whitespace/end.
	quote := rune(0)
	for i, r := range line {
		if r == '\'' || r == '"' {
			if quote == 0 {
				quote = r
			} else if quote == r {
				quote = 0
			}
		}
		if r != ':' || quote != 0 || (i+1 < len(line) && !unicode.IsSpace(rune(line[i+1]))) {
			continue
		}
		key, value := line[:i+1], line[i+1:]
		key = lipgloss.NewStyle().Foreground(cyanBright).Render(key)
		plain := strings.TrimSpace(value)
		switch plain {
		case "true", "false", "null", "~":
			value = paint("13", value)
		case "Healthy", "Synced", "Running", "Succeeded", "Normal":
			value = paint("10", value)
		case "Degraded", "Failed", "Errored", "Aborted", "NotReady", "Error", "Warning", "OutOfSync", "Missing":
			value = paint("9", value)
		default:
			if plain != "" {
				value = paint("11", value)
			}
		}
		return key + value
	}
	return line
}

func (m *Model) inspectionPane(lines []string, title, kind string, width, height, offset int, focused bool) string {
	inner := max(1, width-4)
	offset = min(max(0, offset), max(0, len(lines)-height+1))
	body := []string{headerStyle.Render(tableCell(title, inner))}
	for i := offset; i < min(len(lines), offset+height-1); i++ {
		line := m.colorInspectionLine(lines[i], kind)
		if query := m.state.Diff.SearchQuery; query != "" {
			line = highlightInspectionMatch(line, query)
		}
		pan := 0
		if focused {
			pan = m.state.Diff.HorizontalOffset
		}
		body = append(body, padRight(ansi.Cut(line, pan, pan+inner), inner))
	}
	for len(body) < height {
		body = append(body, strings.Repeat(" ", inner))
	}
	border := contentBorderStyle
	if focused {
		border = border.BorderForeground(cyanBright)
	}
	return border.Render(strings.Join(body, "\n"))
}

func highlightInspectionMatch(line, query string) string {
	plain := ansi.Strip(line)
	start := strings.Index(strings.ToLower(plain), strings.ToLower(query))
	if start < 0 {
		return line
	}
	// Byte offsets are converted to terminal cell positions before ANSI slicing.
	left := lipgloss.Width(plain[:start])
	right := left + lipgloss.Width(plain[start:start+len(query)])
	return ansi.Cut(line, 0, left) + lipgloss.NewStyle().Reverse(true).Render(ansi.Cut(line, left, right)) + ansi.Cut(line, right, lipgloss.Width(line))
}

func (m *Model) renderInspectionView() string {
	st := m.state.Diff
	header := m.renderBanner()
	height := max(2, m.state.Terminal.Rows-countLines(header)-4)
	if m.state.Mode == model.ModeSearch {
		height--
	}
	height = max(2, height)
	width := max(8, m.state.Terminal.Cols-2)
	active := st.Content
	if st.FocusDiff {
		active = st.SideContent
	}
	st.Offset = min(max(0, st.Offset), max(0, len(active)-height+1))
	var body string
	if len(st.SideContent) > 0 && width >= 98 {
		leftWidth := (width - 1) / 2
		leftOffset, rightOffset := st.Offset, st.OtherOffset
		if st.FocusDiff {
			leftOffset, rightOffset = st.OtherOffset, st.Offset
		}
		left := m.inspectionPane(st.Content, "DESCRIBE", "Describe", leftWidth, height, leftOffset, !st.FocusDiff)
		right := m.inspectionPane(st.SideContent, "DIFF: LIVE -> DESIRED", "Diff", width-leftWidth-1, height, rightOffset, st.FocusDiff)
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
	} else {
		kind := st.InspectionKind
		if st.FocusDiff {
			kind = "Diff"
		}
		body = m.inspectionPane(active, strings.ToUpper(kind), kind, width, height, st.Offset, true)
	}
	footer := fmt.Sprintf("%d/%d  j/k: scroll  h/l: pan  /: find  n/N: next/prev  Esc: back", min(st.Offset+1, len(active)), len(active))
	if len(st.SideContent) > 0 {
		footer = "Tab: describe/diff  " + footer
	}
	sections := []string{header, clipAnsiToWidth(headerStyle.Render(st.Title), width), body}
	if m.state.Mode == model.ModeSearch {
		sections = append(sections, m.renderEnhancedSearchBar())
	}
	sections = append(sections, clipAnsiToWidth(statusStyle.Render(footer), width))
	return mainContainerStyle.Render(strings.Join(sections, "\n"))
}

func (m *Model) findInspectionMatch(backward bool) {
	st := m.state.Diff
	if st == nil || st.SearchQuery == "" {
		return
	}
	lines := st.Content
	if st.FocusDiff {
		lines = st.SideContent
	}
	direction := 1
	if backward {
		direction = -1
	}
	start := st.MatchIndex
	if start < 0 {
		if backward {
			start = 0
		} else {
			start = -1
		}
	}
	for distance := 1; distance <= len(lines); distance++ {
		i := (start + direction*distance + len(lines)) % len(lines)
		if strings.Contains(strings.ToLower(lines[i]), strings.ToLower(st.SearchQuery)) {
			st.Offset = i
			st.MatchIndex = i
			return
		}
	}
}
