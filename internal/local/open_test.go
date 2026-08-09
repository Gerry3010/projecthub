// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package local

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestClaudeMCPArgsForEmptyPath(t *testing.T) {
	if got := claudeMCPArgsFor(""); got != nil {
		t.Fatalf("no phmcp path must yield nil args, got %v", got)
	}
}

func TestClaudeMCPArgsForBuildsConfig(t *testing.T) {
	args := claudeMCPArgsFor("/opt/ph/phmcp")
	if len(args) != 2 || args[0] != "--mcp-config" {
		t.Fatalf("args = %v, want [--mcp-config <json>]", args)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(args[1]), &cfg); err != nil {
		t.Fatalf("mcp-config is not valid JSON: %v", err)
	}
	if got := cfg.MCPServers["projecthub"].Command; got != "/opt/ph/phmcp" {
		t.Fatalf("projecthub.command = %q, want /opt/ph/phmcp", got)
	}
}

func TestDecorateClaudeLeavesNonClaudeAlone(t *testing.T) {
	name, args := DecorateClaude("/bin/zsh", []string{"-l"})
	if name != "/bin/zsh" || !slices.Equal(args, []string{"-l"}) {
		t.Fatalf("non-claude command must pass through unchanged: %q %v", name, args)
	}
}

func TestDecorateClaudeKeepsOriginalArgsAndResolvesBin(t *testing.T) {
	name, args := DecorateClaude("claude", []string{"--resume", "sid-1"})
	if filepath.Base(name) != "claude" {
		t.Fatalf("resolved bin base = %q, want claude", filepath.Base(name))
	}
	// The original resume args must survive as a prefix, ahead of any injected --mcp-config.
	if len(args) < 2 || args[0] != "--resume" || args[1] != "sid-1" {
		t.Fatalf("original args must be preserved as a prefix: %v", args)
	}
	// DecorateClaude must not mutate the caller's slice.
	orig := []string{"--resume", "sid-1"}
	_, _ = DecorateClaude("claude", orig)
	if !slices.Equal(orig, []string{"--resume", "sid-1"}) {
		t.Fatalf("caller args were mutated: %v", orig)
	}
}

func TestIsExecutableFile(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "bin")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isExecutableFile(exe) {
		t.Errorf("executable file not detected: %s", exe)
	}
	if isExecutableFile(plain) {
		t.Errorf("non-executable file reported executable: %s", plain)
	}
	if isExecutableFile(dir) {
		t.Errorf("directory reported executable: %s", dir)
	}
}
