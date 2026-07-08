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
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// ParseTranscript reads a Claude Code session's full JSON-lines transcript
// (<ClaudeProjectsDir>/<encodeCwd(cwd)>/<sessionId>.jsonl) and decodes every
// user/assistant line into structured content blocks — unlike parseClaudeSession
// (which only extracts a title/last-active summary for the session list), this
// keeps every block so a tile can render the full conversation: text, thinking,
// tool calls and their results. To keep the payload bounded for a live UI, the
// result is capped to the most recent maxTranscriptEntries entries and each
// block's text is capped to maxBlockBytes.
func ParseTranscript(cwd, sessionID string) ([]domain.TranscriptEntry, error) {
	base, err := ClaudeProjectsDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(base, encodeCwd(cwd), sessionID+".jsonl")

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("transcript: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Transcript lines can be long (large tool results); allow up to 8 MiB per line.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var entries []domain.TranscriptEntry
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var l ccLine
		if err := json.Unmarshal(line, &l); err != nil {
			continue
		}
		// Only chat turns carry a message; skip meta lines (ai-title, summaries, …).
		if (l.Type != "user" && l.Type != "assistant") || len(l.Message) == 0 {
			continue
		}
		blocks := decodeMessageBlocks(l.Message)
		if len(blocks) == 0 {
			continue
		}
		entries = append(entries, domain.TranscriptEntry{
			Role:        l.Type,
			Timestamp:   parseTime(l.Timestamp),
			IsSidechain: l.IsSidechain,
			Blocks:      blocks,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	if len(entries) > maxTranscriptEntries {
		entries = entries[len(entries)-maxTranscriptEntries:]
	}
	return entries, nil
}

const (
	maxTranscriptEntries = 400
	maxBlockBytes        = 16 * 1024
)

// messageWrapper mirrors the {role, content} shape every transcript line's
// "message" field carries; content is either a plain string (simple user
// prompts) or an array of typed content blocks (everything else).
type messageWrapper struct {
	Content json.RawMessage `json:"content"`
}

// contentBlock is a union of every content-block shape Claude Code's transcript
// format uses; only the fields relevant to its Type are populated.
type contentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	Name     string          `json:"name"`     // tool_use
	Input    json.RawMessage `json:"input"`    // tool_use
	Content  json.RawMessage `json:"content"`  // tool_result
	IsError  bool            `json:"is_error"` // tool_result
	Source   struct {
		MediaType string `json:"media_type"`
	} `json:"source"` // image
}

// decodeMessageBlocks decodes a transcript line's message.content into
// TranscriptBlocks. A plain string becomes a single text block; an array of
// content blocks is mapped one-for-one by type. Unknown block types are
// skipped rather than failing the whole entry.
func decodeMessageBlocks(raw json.RawMessage) []domain.TranscriptBlock {
	var wrapper messageWrapper
	if err := json.Unmarshal(raw, &wrapper); err != nil || len(wrapper.Content) == 0 {
		return nil
	}

	// content as a plain string (the common case for simple user prompts)
	var s string
	if json.Unmarshal(wrapper.Content, &s) == nil {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return []domain.TranscriptBlock{{Kind: "text", Text: truncate(s, maxBlockBytes)}}
	}

	// content as an array of typed blocks
	var raws []json.RawMessage
	if json.Unmarshal(wrapper.Content, &raws) != nil {
		return nil
	}
	var out []domain.TranscriptBlock
	for _, br := range raws {
		var b contentBlock
		if json.Unmarshal(br, &b) != nil {
			continue
		}
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			out = append(out, domain.TranscriptBlock{Kind: "text", Text: truncate(b.Text, maxBlockBytes)})
		case "thinking":
			if strings.TrimSpace(b.Thinking) == "" {
				continue
			}
			out = append(out, domain.TranscriptBlock{Kind: "thinking", Text: truncate(b.Thinking, maxBlockBytes)})
		case "tool_use":
			out = append(out, domain.TranscriptBlock{
				Kind: "tool_use", ToolName: b.Name, ToolInput: truncate(prettyJSON(b.Input), maxBlockBytes),
			})
		case "tool_result":
			out = append(out, domain.TranscriptBlock{
				Kind: "tool_result", Result: truncate(flattenToolResult(b.Content), maxBlockBytes), IsError: b.IsError,
			})
		case "image":
			out = append(out, domain.TranscriptBlock{Kind: "image", ImageMIME: b.Source.MediaType})
		default:
			// unknown block type (e.g. future additions) — skip rather than error
		}
	}
	return out
}

// flattenToolResult reduces a tool_result block's content (string, or an array
// of {type:"text",text:…}/{type:"image",…} blocks) to plain text for display;
// non-text blocks are represented by a short placeholder.
func flattenToolResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var raws []json.RawMessage
	if json.Unmarshal(raw, &raws) != nil {
		return ""
	}
	var parts []string
	for _, br := range raws {
		var b struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(br, &b) != nil {
			continue
		}
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		} else if b.Type == "image" {
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n")
}

// prettyJSON re-indents a raw JSON value for readable tool-input display,
// falling back to the raw bytes if it isn't valid JSON.
func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}
