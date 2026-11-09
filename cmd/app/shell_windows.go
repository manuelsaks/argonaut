package main

import (
	"encoding/base64"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"unicode/utf16"
)

// Windows ships PowerShell; Git Bash/WSL and sh are not required. EncodedCommand
// avoids cmd.exe's incompatible quoting rules. File paths are passed separately
// via environment variables by diffViewerCommand.
func configuredShellCommand(command string, pipeInput bool) *exec.Cmd {
	script := "$ErrorActionPreference = 'Stop'\n$ProgressPreference = 'SilentlyContinue'\n" +
		"$OutputEncoding = [System.Text.UTF8Encoding]::new($false)\n" +
		"[Console]::InputEncoding = $OutputEncoding\n" +
		"[Console]::OutputEncoding = $OutputEncoding\n"
	if pipeInput {
		script += "$input | "
	}
	script += "& " + command + "\nif ($null -ne $LASTEXITCODE) { exit $LASTEXITCODE }\n"
	units := utf16.Encode([]rune(script))
	encoded := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(encoded[i*2:], unit)
	}
	shell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	return exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-InputFormat", "Text", "-OutputFormat", "Text", "-EncodedCommand", base64.StdEncoding.EncodeToString(encoded))
}

func shellEnvironmentArgument(name string) string { return "$env:" + name }
