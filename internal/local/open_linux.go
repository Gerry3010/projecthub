// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

//go:build linux

package local

import (
	"fmt"
	"os"
	"os/exec"
)

// opener is the default-handler command. xdg-open is the freedesktop standard.
var opener = "xdg-open"

// terminalCandidates are the terminal emulators tried (in order) to host an
// interactive `claude --resume`, unless PROJECTHUB_TERMINAL overrides the choice.
var terminalCandidates = []string{
	"x-terminal-emulator", "kitty", "alacritty", "gnome-terminal", "konsole", "xterm",
}

// ResumeClaudeSession opens a new terminal window that resumes a Claude Code
// session (`claude --resume <sessionID>`) in its original working directory.
// Unlike a URL/path, `claude` is an interactive TUI, so it needs a terminal to
// host it — chosen via PROJECTHUB_TERMINAL or the first available candidate.
func ResumeClaudeSession(cwd, sessionID string) error {
	term := pickTerminal()
	if term == "" {
		return fmt.Errorf("no terminal emulator found; set PROJECTHUB_TERMINAL to one (tried %v)", terminalCandidates)
	}
	name, args := ResumeCommand(sessionID)
	// The `-e` convention is honoured by every candidate terminal above.
	cmd := exec.Command(term, append([]string{"-e", name}, args...)...)
	cmd.Dir = cwd
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch %s: %w", term, err)
	}
	return nil
}

// pickTerminal returns PROJECTHUB_TERMINAL if set, else the first candidate found
// on PATH, else "".
func pickTerminal() string {
	if t := os.Getenv("PROJECTHUB_TERMINAL"); t != "" {
		return t
	}
	for _, c := range terminalCandidates {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	return ""
}
