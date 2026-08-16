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

package tabsession

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// Claude Code persists each session as a JSON-lines transcript at
// <ClaudeProjectsDir>/<encodeCwd(cwd)>/<sessionId>.jsonl. Scanning these lets the
// TUI companion capture resumable session references per project. Like the Firefox
// capture, this is a local-machine operation a hosted web app cannot perform.

// ClaudeProjectsDir returns ~/.claude/projects, where Claude Code stores per-cwd
// session transcripts.
func ClaudeProjectsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// encodeCwd mirrors Claude Code's directory naming: every '/' and '.' in the
// working directory becomes '-' (e.g. /home/x/.claude-wt → -home-x--claude-wt).
func encodeCwd(cwd string) string {
	return strings.NewReplacer("/", "-", ".", "-").Replace(cwd)
}

// ccLine is one transcript line; only the fields we index are decoded.
type ccLine struct {
	Type        string          `json:"type"`
	SessionID   string          `json:"sessionId"`
	Cwd         string          `json:"cwd"`
	Timestamp   string          `json:"timestamp"`
	AiTitle     string          `json:"aiTitle"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
}

// ClaudeSessionFile returns the transcript path Claude Code uses for one session id in
// a working directory. The file only exists once the session has actually run.
func ClaudeSessionFile(cwd, sessionID string) (string, error) {
	base, err := ClaudeProjectsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, encodeCwd(cwd), sessionID+".jsonl"), nil
}

// HasClaudeSession reports whether a session id already has a transcript in cwd — i.e.
// whether it can be resumed (`--resume`) or still has to be started (`--session-id`).
func HasClaudeSession(cwd, sessionID string) bool {
	if cwd == "" || sessionID == "" {
		return false
	}
	path, err := ClaudeSessionFile(cwd, sessionID)
	if err != nil {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// ScanClaudeSessions returns a CodeSession reference for every Claude Code session
// recorded for the given working directory, newest first. The title prefers the
// transcript's ai-title, falling back to the first (non-sidechain) user prompt.
// A missing project directory is not an error — it just yields no sessions.
func ScanClaudeSessions(cwd string) ([]domain.CodeSession, error) {
	base, err := ClaudeProjectsDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, encodeCwd(cwd))
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}

	var sessions []domain.CodeSession
	for _, path := range matches {
		cs, msgs, err := parseClaudeSession(path, cwd)
		if err != nil {
			// Skip unreadable/partial transcripts rather than failing the whole scan.
			continue
		}
		if msgs == 0 {
			// A transcript without a single message is a session that was created but
			// never talked to (Claude Code writes its title/name line before the first
			// prompt). Listing those means showing a chat's empty twin right next to it,
			// and "resuming" it lands in an empty conversation — so they stay hidden.
			continue
		}
		sessions = append(sessions, cs)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastActive.After(sessions[j].LastActive)
	})
	return sessions, nil
}

// parseClaudeSession reads a single <sessionId>.jsonl transcript into a CodeSession.
// The session id comes from the filename (authoritative for `claude --resume`). The
// second return value is the number of conversation messages, which tells an actual
// chat apart from a session that only ever got its metadata line (see ScanClaudeSessions).
func parseClaudeSession(path, fallbackCwd string) (domain.CodeSession, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return domain.CodeSession{}, 0, err
	}
	defer f.Close()

	cs := domain.CodeSession{
		SessionID: strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		Cwd:       fallbackCwd,
	}
	var firstPrompt string
	msgs := 0

	sc := bufio.NewScanner(f)
	// Transcript lines can be long (large tool results); allow up to 8 MiB per line.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var l ccLine
		if err := json.Unmarshal(line, &l); err != nil {
			continue
		}
		if l.Cwd != "" {
			cs.Cwd = l.Cwd
		}
		if l.AiTitle != "" {
			cs.Title = l.AiTitle // later ai-titles refine earlier ones; keep the last
		}
		if ts := parseTime(l.Timestamp); !ts.IsZero() && ts.After(cs.LastActive) {
			cs.LastActive = ts
		}
		if l.Type == "user" || l.Type == "assistant" {
			msgs++
		}
		if firstPrompt == "" && l.Type == "user" && !l.IsSidechain {
			firstPrompt = firstUserText(l.Message)
		}
	}
	if err := sc.Err(); err != nil {
		return domain.CodeSession{}, 0, err
	}

	if cs.Title == "" {
		cs.Title = truncate(firstPrompt, 80)
	}
	if cs.Title == "" {
		cs.Title = cs.SessionID
	}
	return cs, msgs, nil
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// firstUserText extracts a plain-text summary from a user message's content, which
// Claude Code encodes either as a string or as an array of content blocks.
func firstUserText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	// content as a plain string
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return firstLine(s)
	}
	// content as an array of blocks with {type:"text", text:"..."}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				return firstLine(b.Text)
			}
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}
