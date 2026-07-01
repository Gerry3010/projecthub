// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package tabsession

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEncodeCwd(t *testing.T) {
	cases := map[string]string{
		"/home/user/Projects/GO-Projekte/projecthub": "-home-user-Projects-GO-Projekte-projecthub",
		"/home/user/x/.claude-worktrees/browser":          "-home-user-x--claude-worktrees-browser",
	}
	for in, want := range cases {
		if got := encodeCwd(in); got != want {
			t.Errorf("encodeCwd(%q) = %q, want %q", in, got, want)
		}
	}
}

// sample transcript: two informative lines plus an ai-title. The session id must
// come from the filename, the title from the ai-title, and cwd/last-active from
// the message lines.
const sampleTranscript = `{"type":"user","sessionId":"sess-1","cwd":"/tmp/proj","timestamp":"2026-07-01T09:00:00.000Z","isSidechain":false,"message":{"role":"user","content":"first prompt here"}}
{"type":"assistant","sessionId":"sess-1","cwd":"/tmp/proj","timestamp":"2026-07-01T09:05:30.000Z","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}
{"type":"ai-title","sessionId":"sess-1","aiTitle":"Refactor the parser"}
`

func TestParseClaudeSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-1.jsonl")
	if err := os.WriteFile(path, []byte(sampleTranscript), 0o600); err != nil {
		t.Fatal(err)
	}

	cs, err := parseClaudeSession(path, "/fallback")
	if err != nil {
		t.Fatal(err)
	}
	if cs.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", cs.SessionID)
	}
	if cs.Title != "Refactor the parser" {
		t.Errorf("Title = %q, want ai-title", cs.Title)
	}
	if cs.Cwd != "/tmp/proj" {
		t.Errorf("Cwd = %q, want /tmp/proj", cs.Cwd)
	}
	if cs.LastActive.Format("15:04:05") != "09:05:30" {
		t.Errorf("LastActive = %v, want the latest timestamp", cs.LastActive)
	}
}

func TestParseClaudeSessionTitleFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-2.jsonl")
	line := `{"type":"user","sessionId":"sess-2","cwd":"/tmp/p","timestamp":"2026-07-01T10:00:00.000Z","isSidechain":false,"message":{"role":"user","content":"do a thing\nsecond line"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	cs, err := parseClaudeSession(path, "/tmp/p")
	if err != nil {
		t.Fatal(err)
	}
	if cs.Title != "do a thing" {
		t.Errorf("Title = %q, want first user prompt line", cs.Title)
	}
}

func TestScanClaudeSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := "/tmp/proj"
	dir := filepath.Join(home, ".claude", "projects", encodeCwd(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sess-1.jsonl"), []byte(sampleTranscript), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ScanClaudeSessions(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "sess-1" {
		t.Fatalf("ScanClaudeSessions = %+v, want one session sess-1", got)
	}

	// A non-existent project dir must yield no sessions, not an error.
	empty, err := ScanClaudeSessions("/does/not/exist")
	if err != nil {
		t.Fatalf("scan of missing dir errored: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected no sessions for missing dir, got %d", len(empty))
	}
}
