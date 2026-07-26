// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

//go:build darwin

package local

import (
	"fmt"
	"os/exec"
	"strings"
)

// opener is the default-handler command. macOS `open` opens URLs, files and dirs
// in the default application / Finder.
var opener = "open"

// ResumeClaudeSession opens Terminal.app and resumes a Claude Code session in its
// original working directory. macOS Terminal has no `-e` convention, so we drive it
// via AppleScript (`osascript`). Used only by the headless TUI companion — the
// Electron app resumes in an embedded PTY instead.
func ResumeClaudeSession(cwd, sessionID string) error {
	name, args := ResumeCommand(sessionID)
	cmdLine := fmt.Sprintf("cd %s && %s %s", shellQuote(cwd), name, strings.Join(args, " "))
	// `do script` runs the line in a new Terminal window/tab.
	script := fmt.Sprintf("tell application \"Terminal\"\nactivate\ndo script %s\nend tell", appleQuote(cmdLine))
	if err := exec.Command("osascript", "-e", script).Start(); err != nil {
		return fmt.Errorf("launch Terminal via osascript: %w", err)
	}
	return nil
}

// shellQuote single-quotes a string for a POSIX shell (Terminal runs the user's
// login shell), escaping embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// appleQuote double-quotes a string for AppleScript, escaping backslashes/quotes.
func appleQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
