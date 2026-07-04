// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package tabsession

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSession(t *testing.T, home, cwd, sessID, body string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", encodeCwd(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sessID+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScanClaudeProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Two real projects, plus two dirs that must be skipped.
	writeSession(t, home, "/tmp/old", "a",
		`{"type":"user","sessionId":"a","cwd":"/tmp/old","timestamp":"2026-06-01T09:00:00.000Z","message":{"content":"old work"}}`+"\n")
	// newest project has two sessions → SessionCount must be 2, and it must rank first.
	writeSession(t, home, "/tmp/new", "b",
		`{"type":"user","sessionId":"b","cwd":"/tmp/new","timestamp":"2026-07-01T09:00:00.000Z","message":{"content":"recent work"}}`+"\n"+
			`{"type":"ai-title","sessionId":"b","aiTitle":"Recent title"}`+"\n")
	writeSession(t, home, "/tmp/new", "c",
		`{"type":"user","sessionId":"c","cwd":"/tmp/new","timestamp":"2026-06-15T09:00:00.000Z","message":{"content":"more"}}`+"\n")

	// A transcript with no cwd anywhere → unresolvable, must be skipped (not guessed
	// from the dashed dir name).
	writeSession(t, home, "/tmp/nocwd", "d",
		`{"type":"user","sessionId":"d","timestamp":"2026-07-02T09:00:00.000Z","message":{"content":"x"}}`+"\n")

	// Set deterministic mtimes so "newest transcript per dir" is unambiguous: within
	// /tmp/new, b (the ai-titled one) must be newer than c.
	chtime := func(cwd, sess string, mt time.Time) {
		p := filepath.Join(home, ".claude", "projects", encodeCwd(cwd), sess+".jsonl")
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	chtime("/tmp/old", "a", time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC))
	chtime("/tmp/new", "c", time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC))
	chtime("/tmp/new", "b", time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC))

	got, err := ScanClaudeProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d projects, want 2 (old+new; nocwd skipped): %+v", len(got), got)
	}
	if got[0].Cwd != "/tmp/new" {
		t.Errorf("newest-first ranking wrong: got[0].Cwd = %q, want /tmp/new", got[0].Cwd)
	}
	if got[0].Title != "Recent title" {
		t.Errorf("got[0].Title = %q, want the ai-title of the newest session", got[0].Title)
	}
	if got[0].SessionCount != 2 {
		t.Errorf("got[0].SessionCount = %d, want 2", got[0].SessionCount)
	}
	if got[1].Cwd != "/tmp/old" {
		t.Errorf("got[1].Cwd = %q, want /tmp/old", got[1].Cwd)
	}
}

func TestScanClaudeProjectsMissingDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // no ~/.claude/projects at all
	got, err := ScanClaudeProjects()
	if err != nil {
		t.Fatalf("missing projects dir must not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want no projects, got %d", len(got))
	}
}
