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
//
// Platform-specific pieces (the default-opener command and the external-terminal
// `claude --resume` launcher) live in open_<goos>.go. The desktop Electron app
// resumes Claude in an embedded PTY (internal/ptyhost) and never uses the
// external-terminal path; that path is only the headless TUI companion's fallback.
package local

import (
	"fmt"
	"os/exec"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// OpenURL opens a URL in the user's default browser (a new tab/window).
func OpenURL(url string) error { return spawn(opener, url) }

// OpenPath opens a file or directory in the user's default application / file
// manager.
func OpenPath(path string) error { return spawn(opener, path) }

// OpenWith opens a target (path or URL) in a specific program, e.g. `code <path>`
// for VS Code. Unlike OpenPath it bypasses the default handler. The program is run
// detached with the target as its sole argument.
func OpenWith(program, target string) error { return spawn(program, target) }

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

// ResumeCommand returns the command + args that resume a Claude Code session,
// without spawning anything. The embedded terminal (Go PTY host) starts this
// inside its own pane; the external-terminal path wraps the same command.
// Keeping it a pure function means both paths agree on exactly how a resume runs.
func ResumeCommand(sessionID string) (name string, args []string) {
	return "claude", []string{"--resume", sessionID}
}

// ChatCommand returns the command + args for one headless Claude chat turn, used by
// the embedded sidebar chat (no terminal). It runs in print mode (-p) and persists to
// the normal session transcript (~/.claude/projects/<cwd>/<sessionId>.jsonl), so the
// UI streams it simply by polling that transcript. A fresh chat passes resume=false
// with a client-minted sessionID (--session-id); a follow-up passes resume=true to
// continue the same session (--resume). Keeping it here alongside ResumeCommand makes
// internal/local the single source of truth for how Claude is invoked.
func ChatCommand(prompt, systemPrompt, sessionID string, resume bool) (name string, args []string) {
	args = []string{"-p", prompt}
	if systemPrompt != "" {
		args = append(args, "--append-system-prompt", systemPrompt)
	}
	if resume {
		args = append(args, "--resume", sessionID)
	} else {
		args = append(args, "--session-id", sessionID)
	}
	return "claude", args
}

// spawn starts a detached process and returns without waiting.
func spawn(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Start()
}
