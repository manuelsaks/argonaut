package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestTerminalResetDisablesReportsWithoutQueryingTheShell(t *testing.T) {
	var output bytes.Buffer
	resetTerminalReports(&output)
	for _, mode := range []string{"1000", "1002", "1003", "1006", "1015", "1016", "1004", "2004"} {
		if !strings.Contains(output.String(), "\x1b[?"+mode+"l") {
			t.Fatalf("terminal mode %s was left enabled", mode)
		}
	}
	if strings.Contains(output.String(), "$p") || strings.Contains(output.String(), "6n") {
		t.Fatal("cleanup must not solicit terminal responses after input stops")
	}
	m := buildSyncTestModel(80, 24)
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("hover movement should not generate terminal input")
	}
}

func TestTerminalCleanupDoesNotWriteEscapesIntoRedirectedOutput(t *testing.T) {
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	restore := terminalCleanup(input, output)
	stopTerminalReports(output)
	restore()
	output.Close()
	data, err := io.ReadAll(input)
	if err != nil || len(data) != 0 {
		t.Fatal("redirected output must not receive terminal reset escapes")
	}
}
