package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func unifiedDiff(left, right string) (string, error) {
	// Manifest files use LF on every platform. Global Git line-ending settings,
	// external diff drivers and stderr warnings must not change the diff content.
	cmd := exec.Command("git", "--no-pager", "-c", "core.autocrlf=false", "-c", "core.safecrlf=false",
		"diff", "--no-index", "--no-color", "--no-ext-diff", "--no-textconv", "--", left, right)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			if exitErr != nil {
				return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
			}
			return "", err
		}
	}
	return stripDiffHeader(string(out)), nil
}
