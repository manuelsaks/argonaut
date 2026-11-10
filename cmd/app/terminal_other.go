//go:build !windows

package main

import (
	"os"

	"golang.org/x/term"
)

func stopTerminalReports(output *os.File) {
	if term.IsTerminal(int(output.Fd())) {
		resetTerminalReports(output)
	}
}

func terminalCleanup(input, output *os.File) func() {
	state, err := term.GetState(int(input.Fd()))
	isOutputTerminal := term.IsTerminal(int(output.Fd()))
	return func() {
		if isOutputTerminal {
			resetTerminalReports(output)
		}
		if err == nil {
			_ = term.Restore(int(input.Fd()), state)
		}
	}
}
