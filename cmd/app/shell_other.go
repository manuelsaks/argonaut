//go:build !windows

package main

import "os/exec"

func configuredShellCommand(command string, _ bool) *exec.Cmd {
	return exec.Command("sh", "-lc", command)
}

func shellEnvironmentArgument(name string) string { return `"$` + name + `"` }
