// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package local

import (
	"slices"
	"testing"
)

func TestChatCommandNewSession(t *testing.T) {
	name, args := ChatCommand("hallo", "du bist X", "sid-123", false)
	if name != "claude" {
		t.Fatalf("name = %q, want claude", name)
	}
	want := []string{"-p", "hallo", "--append-system-prompt", "du bist X", "--session-id", "sid-123"}
	if !slices.Equal(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestChatCommandResume(t *testing.T) {
	_, args := ChatCommand("weiter", "sp", "sid-9", true)
	// resume must use --resume (not --session-id) so it continues the same transcript
	if slices.Contains(args, "--session-id") {
		t.Errorf("resume must not pass --session-id: %v", args)
	}
	if !slices.Contains(args, "--resume") || !slices.Contains(args, "sid-9") {
		t.Errorf("resume must pass --resume sid-9: %v", args)
	}
}

func TestChatCommandNoSystemPrompt(t *testing.T) {
	_, args := ChatCommand("x", "", "sid", false)
	if slices.Contains(args, "--append-system-prompt") {
		t.Errorf("empty system prompt must be omitted: %v", args)
	}
}
