// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, either version 3 of the
// License, or (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

// Package local performs local-machine actions for the ProjectHub TUI companion —
// opening URLs, files and directories via the desktop's default handler. These are
// exactly the actions a sandboxed hosted web app cannot do.
package local

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// opener is the command used to open URLs/paths. xdg-open is the freedesktop
// standard on Linux; var so tests can stub it.
var opener = "xdg-open"

// terminalCandidates are the terminal emulators tried (in order) to host an
// interactive `claude --resume`, unless PROJECTHUB_TERMINAL overrides the choice.
var terminalCandidates = []string{
	"x-terminal-emulator", "kitty", "alacritty", "gnome-terminal", "konsole", "xterm",
}

// OpenURL opens a URL in the user's default browser (a new tab/window).
func OpenURL(url string) error { return spawn(opener, url) }

// OpenPath opens a file or directory in the user's default application / file
// manager.
func OpenPath(path string) error { return spawn(opener, path) }

// RestoreTabs reopens every tab's URL. It opens them in the current default
// browser; precise multi-window restoration is left to the future browser
// extension. The first failure is returned, but every URL is attempted.
func RestoreTabs(tabs []domain.Tab) error {
	var firstErr error
	for _, t := range tabs {
		if err := OpenURL(t.URL); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("open %s: %w", t.URL, err)
		}
	}
	return firstErr
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
	// The `-e` convention is honoured by every candidate terminal above.
	cmd := exec.Command(term, "-e", "claude", "--resume", sessionID)
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

// spawn starts a detached process and returns without waiting.
func spawn(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Start()
}
