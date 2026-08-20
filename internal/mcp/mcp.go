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

// Package mcp is the single source of truth for the tools ProjectHub exposes to
// Claude Code over MCP. Each tool is either "local" (the sidecar runs it directly:
// disk scans, file IO) or "renderer" (only the WASM UI can — it holds the vault keys
// and owns the tiling workspace; those are dispatched over the control hub). Both the
// sidecar's dispatcher and the cmd/phmcp stdio bridge read this catalog.
package mcp

import "encoding/json"

// Tool describes one MCP tool: its name, human description, JSON-Schema for inputs,
// and where it runs.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Renderer    bool            `json:"-"` // true ⇒ executed in the renderer via the control hub
}

// obj is a tiny helper to write JSON-Schema literals.
func obj(s string) json.RawMessage { return json.RawMessage(s) }

// Tools returns the full catalog, in a stable order.
func Tools() []Tool {
	return []Tool{
		// ── renderer tools (vault + workspace) ──
		{
			Name:        "project_list",
			Description: "List the user's ProjectHub projects (id, title, local path).",
			InputSchema: obj(`{"type":"object","properties":{},"additionalProperties":false}`),
			Renderer:    true,
		},
		{
			Name:        "layout_get",
			Description: "Return the active project's tiling layout as a tree: splits with direction and ratio, leaves with pane id, tile type and params. Use this (rather than tile_list) when the arrangement itself matters.",
			InputSchema: obj(`{"type":"object","properties":{},"additionalProperties":false}`),
			Renderer:    true,
		},
		{
			Name:        "tile_list",
			Description: "List the tiles currently open in the active project's workspace (pane id, type).",
			InputSchema: obj(`{"type":"object","properties":{},"additionalProperties":false}`),
			Renderer:    true,
		},
		{
			Name:        "tile_create",
			Description: "Open a new tile in the active project's workspace. type is one of terminal, browser, markdown, editor, notes, todo, files, sessions, tabs, claude, pipepush. params is an optional map (e.g. {\"path\":\"/abs/file\"} for editor/markdown, {\"url\":\"https://…\"} for browser, {\"cwd\":\"/abs\",\"cmd\":\"claude\"} for terminal).",
			InputSchema: obj(`{"type":"object","properties":{"type":{"type":"string"},"params":{"type":"object","additionalProperties":{"type":"string"}}},"required":["type"],"additionalProperties":false}`),
			Renderer:    true,
		},
		{
			Name:        "tile_close",
			Description: "Close a tile by its pane id.",
			InputSchema: obj(`{"type":"object","properties":{"pane_id":{"type":"string"}},"required":["pane_id"],"additionalProperties":false}`),
			Renderer:    true,
		},
		{
			Name:        "tile_focus",
			Description: "Focus (highlight) a tile by its pane id.",
			InputSchema: obj(`{"type":"object","properties":{"pane_id":{"type":"string"}},"required":["pane_id"],"additionalProperties":false}`),
			Renderer:    true,
		},
		{
			Name:        "todo_list",
			Description: "List the to-dos of the active project (text, done, order).",
			InputSchema: obj(`{"type":"object","properties":{},"additionalProperties":false}`),
			Renderer:    true,
		},
		{
			Name:        "todo_create",
			Description: "Add a to-do to the active project.",
			InputSchema: obj(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
			Renderer:    true,
		},

		// ── local tools (sidecar, no vault) ──
		{
			Name:        "app_info",
			Description: "Report which ProjectHub build is running and what it is doing: version/commit/dirty flag of the sidecar, the MCP bridge and the shipped web/app.wasm; the Electron shell's versions, bundle path and open windows; live PTY sessions; and the config/discovery paths. Compare build.phd.commit with the repo's HEAD to tell whether an update actually reached the running app.",
			InputSchema: obj(`{"type":"object","properties":{},"additionalProperties":false}`),
		},
		{
			Name:        "session_list",
			Description: "List the Claude Code sessions recorded on disk for a working directory.",
			InputSchema: obj(`{"type":"object","properties":{"cwd":{"type":"string"}},"required":["cwd"],"additionalProperties":false}`),
		},
		{
			Name:        "file_read",
			Description: "Read a local file's contents (absolute path).",
			InputSchema: obj(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`),
		},
		{
			Name:        "file_write",
			Description: "Write content to a local file (absolute path); overwrites or creates it.",
			InputSchema: obj(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`),
		},
	}
}

// IsRenderer reports whether the named tool runs in the renderer (via the control
// hub). Unknown names return false (treated as local; the dispatcher will 404).
func IsRenderer(name string) bool {
	for _, t := range Tools() {
		if t.Name == name {
			return t.Renderer
		}
	}
	return false
}

// Known reports whether name is in the catalog.
func Known(name string) bool {
	for _, t := range Tools() {
		if t.Name == name {
			return true
		}
	}
	return false
}
