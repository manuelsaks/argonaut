package main

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// Run once under a real PTY as well as in the normal suite.
func TestTerminalWindowsRestoresConsoleModes(t *testing.T) {
	in, out := windows.Handle(os.Stdin.Fd()), windows.Handle(os.Stdout.Fd())
	var inputMode, outputMode uint32
	if windows.GetConsoleMode(in, &inputMode) != nil || windows.GetConsoleMode(out, &outputMode) != nil {
		t.Skip("requires a Windows console; covered by the explicit PTY test run")
	}
	restore := terminalCleanup(os.Stdin, os.Stdout)
	defer restore()
	if err := windows.SetConsoleMode(in, inputMode|windows.ENABLE_VIRTUAL_TERMINAL_INPUT); err != nil {
		t.Fatal(err)
	}
	if err := windows.SetConsoleMode(out, outputMode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		t.Fatal(err)
	}
	restore()
	var gotInput, gotOutput uint32
	if err := windows.GetConsoleMode(in, &gotInput); err != nil {
		t.Fatal(err)
	}
	if err := windows.GetConsoleMode(out, &gotOutput); err != nil {
		t.Fatal(err)
	}
	if gotInput != inputMode || gotOutput != outputMode {
		t.Fatal("original input/output console modes were not restored")
	}
}
