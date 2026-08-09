// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package webui

import (
	"strings"
	"testing"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

func TestBuildManagerSystemPrompt(t *testing.T) {
	projects := []domain.ProjectRef{
		{Title: "ProjectHub", LocalPath: "/home/x/projecthub"},
		{Title: "Homepage"}, // no local path
	}
	sp := buildManagerSystemPrompt(projects)
	for _, want := range []string{"ProjectHub-Assistent", "ProjectHub", "/home/x/projecthub", "Homepage"} {
		if !strings.Contains(sp, want) {
			t.Errorf("system prompt missing %q\n%s", want, sp)
		}
	}
}

func TestBuildManagerSystemPromptEmpty(t *testing.T) {
	sp := buildManagerSystemPrompt(nil)
	if !strings.Contains(sp, "noch keine Projekte") {
		t.Errorf("empty project list should be noted:\n%s", sp)
	}
}

func TestTranscriptSigChangesOnGrowth(t *testing.T) {
	a := []domain.TranscriptEntry{{Role: "user", Blocks: []domain.TranscriptBlock{{Kind: "text", Text: "hi"}}}}
	b := []domain.TranscriptEntry{
		{Role: "user", Blocks: []domain.TranscriptBlock{{Kind: "text", Text: "hi"}}},
		{Role: "assistant", Blocks: []domain.TranscriptBlock{{Kind: "text", Text: "hel"}}},
	}
	c := []domain.TranscriptEntry{
		{Role: "user", Blocks: []domain.TranscriptBlock{{Kind: "text", Text: "hi"}}},
		{Role: "assistant", Blocks: []domain.TranscriptBlock{{Kind: "text", Text: "hello there"}}}, // grew
	}
	if transcriptSig(a) == transcriptSig(b) {
		t.Error("adding an entry must change the signature")
	}
	if transcriptSig(b) == transcriptSig(c) {
		t.Error("a growing last block must change the signature")
	}
	if transcriptSig(c) != transcriptSig(c) {
		t.Error("signature must be stable for identical input")
	}
	if transcriptSig(nil) != "0" {
		t.Error("empty transcript signature should be 0")
	}
}

func TestLastEntryIsAssistant(t *testing.T) {
	if lastEntryIsAssistant(nil) {
		t.Error("empty transcript is not an assistant reply")
	}
	userLast := []domain.TranscriptEntry{{Role: "assistant"}, {Role: "user"}}
	if lastEntryIsAssistant(userLast) {
		t.Error("user-last transcript should be false")
	}
	asstLast := []domain.TranscriptEntry{{Role: "user"}, {Role: "assistant"}}
	if !lastEntryIsAssistant(asstLast) {
		t.Error("assistant-last transcript should be true")
	}
}
