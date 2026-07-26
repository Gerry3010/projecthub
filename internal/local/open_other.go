// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

//go:build !linux && !darwin

package local

import "fmt"

// opener falls back to the freedesktop command on unrecognised platforms. The
// desktop app targets Linux and macOS; other platforms are best-effort.
var opener = "xdg-open"

// ResumeClaudeSession is unsupported outside Linux/macOS (no terminal convention
// wired up). The Electron app never calls this — it uses the embedded PTY.
func ResumeClaudeSession(cwd, sessionID string) error {
	return fmt.Errorf("external-terminal resume is not supported on this platform")
}
