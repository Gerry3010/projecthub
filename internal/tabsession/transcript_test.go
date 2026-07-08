// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package tabsession

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// richTranscript exercises every content-block kind ParseTranscript decodes:
// a plain-string user prompt, then an assistant turn with thinking+text+
// tool_use, then a user turn carrying the tool_result (+is_error), and a
// trailing ai-title meta line that must be skipped (no "message" field).
const richTranscript = `{"type":"user","sessionId":"sess-1","cwd":"/tmp/proj","timestamp":"2026-07-01T09:00:00.000Z","isSidechain":false,"message":{"role":"user","content":"fix the bug"}}
{"type":"assistant","sessionId":"sess-1","cwd":"/tmp/proj","timestamp":"2026-07-01T09:01:00.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"let me look at the file"},{"type":"text","text":"I'll check the file."},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/tmp/proj/main.go"}}]}}
{"type":"user","sessionId":"sess-1","cwd":"/tmp/proj","timestamp":"2026-07-01T09:02:00.000Z","isSidechain":false,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"package main\n","is_error":false}]}}
{"type":"ai-title","sessionId":"sess-1","aiTitle":"Fix the bug"}
`

func writeTranscript(t *testing.T, home, cwd, sessionID, body string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", encodeCwd(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestParseTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/tmp/proj"
	writeTranscript(t, home, cwd, "sess-1", richTranscript)

	entries, err := ParseTranscript(cwd, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 entries (ai-title meta line must be skipped), got %d: %+v", len(entries), entries)
	}

	// entry 0: plain-string user prompt → one text block
	if entries[0].Role != "user" || len(entries[0].Blocks) != 1 || entries[0].Blocks[0].Kind != "text" ||
		entries[0].Blocks[0].Text != "fix the bug" {
		t.Fatalf("unexpected entry 0: %+v", entries[0])
	}

	// entry 1: assistant turn with thinking + text + tool_use
	e1 := entries[1]
	if e1.Role != "assistant" || len(e1.Blocks) != 3 {
		t.Fatalf("unexpected entry 1: %+v", e1)
	}
	if e1.Blocks[0].Kind != "thinking" || e1.Blocks[0].Text != "let me look at the file" {
		t.Errorf("unexpected thinking block: %+v", e1.Blocks[0])
	}
	if e1.Blocks[1].Kind != "text" || e1.Blocks[1].Text != "I'll check the file." {
		t.Errorf("unexpected text block: %+v", e1.Blocks[1])
	}
	if e1.Blocks[2].Kind != "tool_use" || e1.Blocks[2].ToolName != "Read" {
		t.Errorf("unexpected tool_use block: %+v", e1.Blocks[2])
	}
	if e1.Blocks[2].ToolInput == "" {
		t.Errorf("expected non-empty pretty-printed tool input")
	}

	// entry 2: tool_result carried in a user-role message
	e2 := entries[2]
	if e2.Role != "user" || len(e2.Blocks) != 1 {
		t.Fatalf("unexpected entry 2: %+v", e2)
	}
	if e2.Blocks[0].Kind != "tool_result" || e2.Blocks[0].Result != "package main\n" || e2.Blocks[0].IsError {
		t.Errorf("unexpected tool_result block: %+v", e2.Blocks[0])
	}
}

func TestParseTranscriptMissingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := ParseTranscript("/does/not/exist", "nope"); err == nil {
		t.Fatal("expected an error for a missing transcript file")
	}
}

func TestParseTranscriptSkipsUnparsableLines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/tmp/proj2"
	body := "not json at all\n" +
		`{"type":"user","sessionId":"sess-2","cwd":"/tmp/proj2","timestamp":"2026-07-01T09:00:00.000Z","isSidechain":false,"message":{"role":"user","content":"hello"}}` + "\n"
	writeTranscript(t, home, cwd, "sess-2", body)

	entries, err := ParseTranscript(cwd, "sess-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Blocks[0].Text != "hello" {
		t.Fatalf("expected the garbage line to be skipped, got %+v", entries)
	}
}

func TestParseTranscriptCapsEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/tmp/proj3"

	var sb strings.Builder
	for range maxTranscriptEntries + 50 {
		sb.WriteString(`{"type":"user","sessionId":"sess-3","cwd":"/tmp/proj3","timestamp":"2026-07-01T09:00:00.000Z","isSidechain":false,"message":{"role":"user","content":"msg"}}` + "\n")
	}
	writeTranscript(t, home, cwd, "sess-3", sb.String())

	entries, err := ParseTranscript(cwd, "sess-3")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != maxTranscriptEntries {
		t.Fatalf("expected entries capped at %d, got %d", maxTranscriptEntries, len(entries))
	}
}
