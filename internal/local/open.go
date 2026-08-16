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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

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

// SessionCommand returns the command that opens a terminal Claude session with a KNOWN
// id: --resume when that transcript already exists, --session-id to pin a brand-new
// session to the id the caller minted. Pinning is what keeps a tile on one conversation
// — plain `claude` would start a fresh, nameless session on every app restart and leave
// its predecessor behind as a duplicate in the resume list. A non-empty prompt is passed
// positionally, so the TUI opens with it already sent.
func SessionCommand(sessionID, prompt string, exists bool) (name string, args []string) {
	if sessionID == "" { // nothing to pin — behave like a plain Claude start
		name, args = "claude", nil
	} else if exists {
		name, args = ResumeCommand(sessionID)
	} else {
		name, args = "claude", []string{"--session-id", sessionID}
	}
	if prompt != "" {
		args = append(args, prompt)
	}
	return name, args
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

// ─── ProjectHub MCP wiring for tile-hosted Claude ────────────────────────────────
//
// A Claude Code started inside a project tile should be able to drive its own project
// (tiles, todos, files) via the ProjectHub MCP bridge, phmcp. DecorateClaude injects the
// bridge as a --mcp-config server at every embedded launch. Kept here so internal/local
// stays the single source of truth for how Claude is invoked.

// isExecutableFile reports whether p is an existing, executable regular file.
func isExecutableFile(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode()&0o111 != 0
}

// PhmcpPath resolves the phmcp MCP-bridge binary. It ships next to the sidecar (phd) — in
// build/ during dev, beside the app in the packaged bundle — so look next to the running
// executable first, then PATH. Returns "" if it can't be found (MCP injection is then off).
func PhmcpPath() string {
	name := "phmcp"
	if runtime.GOOS == "windows" {
		name = "phmcp.exe"
	}
	if exe, err := os.Executable(); err == nil {
		if cand := filepath.Join(filepath.Dir(exe), name); isExecutableFile(cand) {
			return cand
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// ClaudeMCPArgs returns the flags registering ProjectHub's MCP bridge (phmcp) as the
// "projecthub" server for a Claude invocation. Additive (no --strict-mcp-config), JSON-encoded
// so an odd phmcp path stays safe. nil when phmcp can't be found — the caller launches plain
// Claude then.
func ClaudeMCPArgs() []string { return claudeMCPArgsFor(PhmcpPath()) }

// claudeMCPArgsFor builds the --mcp-config args for a given phmcp path (split out for testing).
func claudeMCPArgsFor(phmcp string) []string {
	if phmcp == "" {
		return nil
	}
	cfg, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"projecthub": map[string]any{"command": phmcp},
		},
	})
	if err != nil {
		return nil
	}
	return []string{"--mcp-config", string(cfg)}
}

// ClaudeBin resolves the claude executable. exec/pty resolve commands against the sidecar's
// PATH, which is minimal when the app is launched from Launchpad/Finder — so fall back to the
// common user install locations. Returns "claude" if none is found (let exec surface the error).
func ClaudeBin() string {
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, c := range []string{
		filepath.Join(home, ".local", "bin", "claude"),
		"/opt/homebrew/bin/claude",
		"/usr/local/bin/claude",
		filepath.Join(home, ".claude", "local", "claude"),
	} {
		if isExecutableFile(c) {
			return c
		}
	}
	return "claude"
}

// DecorateClaude augments a claude invocation for the embedded PTY / headless paths: it resolves
// claude to an absolute binary and appends the ProjectHub MCP args. Non-claude commands (a plain
// shell tile) are returned untouched. Deliberately NOT folded into ResumeCommand/ChatCommand:
// the TUI's external-terminal path renders those into a shell line where the JSON arg would break
// quoting; the embedded paths pass argv as a []string, so there's no quoting issue.
func DecorateClaude(name string, args []string) (string, []string) {
	if filepath.Base(name) != "claude" {
		return name, args
	}
	out := append(append([]string{}, args...), ClaudeMCPArgs()...)
	return ClaudeBin(), out
}

// spawn starts a detached process and returns without waiting.
func spawn(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Start()
}
