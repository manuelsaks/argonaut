package main

import (
	"io"
	"os"

	tea "charm.land/bubbletea/v2"
)

// Only resets: unlike terminal capability queries these cannot generate input
// replies after Bubble Tea has stopped reading and the shell resumes.
const terminalReportReset = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?1015l\x1b[?1016l\x1b[?1004l\x1b[?2004l\x1b[>4;0m\x1b[0m\x1b[?25h"

func resetTerminalReports(w io.Writer) {
	_, _ = io.WriteString(w, terminalReportReset)
}

func runTUI(m *Model) error {
	restore := terminalCleanup(os.Stdin, os.Stdout)
	defer restore()
	p := tea.NewProgram(m, tea.WithFilter(func(current tea.Model, msg tea.Msg) tea.Msg {
		if _, quitting := msg.(tea.QuitMsg); quitting {
			if model, ok := current.(*Model); ok {
				model.cancelContextRelogin()
			}
			// Stop generating mouse reports before cancelling the input reader.
			stopTerminalReports(os.Stdout)
		}
		return msg
	}))
	m.SetProgram(p)
	final, err := p.Run()
	if current, ok := final.(*Model); ok {
		current.cancelContextRelogin()
		current.cleanupAppWatcher()
		current.cleanupTreeWatchers()
	}
	return err
}
