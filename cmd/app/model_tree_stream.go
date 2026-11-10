package main

import (
	tea "charm.land/bubbletea/v2"
	cblog "github.com/charmbracelet/log"
)

// consumeTreeEvent reads a single tree stream event and returns it as a tea message
func (m *Model) consumeTreeEvent() tea.Cmd {
	if m.config.IsLowBandwidth() {
		return nil
	}
	ch := m.treeStream       // capture at call time
	done := m.treeStreamDone // capture at call time
	return func() tea.Msg {
		if ch == nil {
			return nil
		}
		// Pre-check: when both ch and done are ready Go selects randomly.
		// Drain done first so cleanup always wins over a pending event.
		select {
		case <-done:
			return nil
		default:
		}
		select {
		case ev, ok := <-ch:
			if !ok {
				cblog.With("component", "ui").Debug("Tree stream channel closed")
				return nil
			}
			cblog.With("component", "ui").Debug("Consumed tree event", "app", ev.AppName)
			return ev
		case <-done:
			return nil
		}
	}
}
