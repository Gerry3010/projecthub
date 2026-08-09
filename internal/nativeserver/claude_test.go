// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package nativeserver

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

func TestClaudeTranscriptEndpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/tmp/proj"
	sessionID := "sess-1"

	// ClaudeProjectsDir/encodeCwd mirror tabsession's own naming (see its tests);
	// replicate that here rather than importing the unexported helper.
	encoded := strings.NewReplacer("/", "-", ".", "-").Replace(cwd)
	dir := filepath.Join(home, ".claude", "projects", encoded)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","sessionId":"sess-1","cwd":"/tmp/proj","timestamp":"2026-07-01T09:00:00.000Z","isSidechain":false,"message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	h := newTestServer()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq("GET", "/claude/transcript?cwd="+cwd+"&session_id="+sessionID, nil))
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var got []domain.TranscriptEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Role != "user" || len(got[0].Blocks) != 1 || got[0].Blocks[0].Text != "hello" {
		t.Fatalf("unexpected transcript: %+v", got)
	}
}

func TestClaudeTranscriptEndpointRequiresParams(t *testing.T) {
	h := newTestServer()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq("GET", "/claude/transcript", nil))
	if rec.Code != 400 {
		t.Fatalf("want 400 without cwd/session_id, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq("GET", "/claude/transcript?cwd=/tmp/x", nil))
	if rec.Code != 400 {
		t.Fatalf("want 400 without session_id, got %d", rec.Code)
	}
}

func TestClaudeTranscriptEndpointMissingSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	h := newTestServer()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq("GET", "/claude/transcript?cwd=/does/not/exist&session_id=nope", nil))
	if rec.Code != 500 {
		t.Fatalf("want 500 for a missing transcript file, got %d (%s)", rec.Code, rec.Body.String())
	}
}
