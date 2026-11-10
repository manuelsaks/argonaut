package main

import (
	"os"

	"github.com/mattn/go-isatty"
	"golang.org/x/sys/windows"
)

func stopTerminalReports(output *os.File) {
	var mode uint32
	handle := windows.Handle(output.Fd())
	if windows.GetConsoleMode(handle, &mode) == nil || isatty.IsCygwinTerminal(output.Fd()) {
		resetTerminalReports(output)
	}
}

func terminalCleanup(input, output *os.File) func() {
	in, out := windows.Handle(input.Fd()), windows.Handle(output.Fd())
	var inputMode, outputMode uint32
	hasInput := windows.GetConsoleMode(in, &inputMode) == nil
	hasOutput := windows.GetConsoleMode(out, &outputMode) == nil
	return func() {
		if hasOutput {
			// Bubble Tea has already restored the console output mode. Enable
			// VT just for the reset, otherwise Windows may print the escapes.
			if windows.SetConsoleMode(out, outputMode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil {
				resetTerminalReports(output)
			}
			_ = windows.SetConsoleMode(out, outputMode)
		} else if isatty.IsCygwinTerminal(output.Fd()) {
			// Git Bash/mintty uses an MSYS PTY pipe rather than a Win32 console.
			resetTerminalReports(output)
		}
		if hasInput {
			// Discard already queued mouse/focus reports, not shell keystrokes
			// typed later. Reports were disabled before restoring cooked input.
			_ = windows.FlushConsoleInputBuffer(in)
			_ = windows.SetConsoleMode(in, inputMode)
		}
	}
}
