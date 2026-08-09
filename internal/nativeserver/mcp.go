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

package nativeserver

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Gerry3010/projecthub/internal/control"
	"github.com/Gerry3010/projecthub/internal/mcp"
	"github.com/Gerry3010/projecthub/internal/tabsession"
)

// mcpTools returns the tool catalog cmd/phmcp advertises to Claude Code.
func (s *Server) mcpTools(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, mcp.Tools())
}

// mcpCall executes one tool. Local tools (disk/file IO) run here; renderer tools
// (vault + workspace) are forwarded to the WASM UI over the control hub, which the
// renderer long-polls. Response body is the tool's raw JSON result.
func (s *Server) mcpCall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Tool == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !mcp.Known(req.Tool) {
		http.Error(w, "unknown tool: "+req.Tool, http.StatusNotFound)
		return
	}
	if mcp.IsRenderer(req.Tool) {
		// Give the renderer a bounded window to pick up + answer (it long-polls).
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		res, err := s.hub.Call(ctx, req.Tool, req.Args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(res)
		return
	}
	// Local tools.
	res, err := s.runLocalTool(req.Tool, req.Args)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, res)
}

// runLocalTool executes a sidecar-side MCP tool (no vault, no renderer needed).
func (s *Server) runLocalTool(tool string, args json.RawMessage) (any, error) {
	switch tool {
	case "session_list":
		var a struct {
			Cwd string `json:"cwd"`
		}
		_ = json.Unmarshal(args, &a)
		return tabsession.ScanClaudeSessions(a.Cwd)
	case "file_read":
		var a struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(args, &a)
		if !filepath.IsAbs(a.Path) {
			return nil, errBadTool("absolute path required")
		}
		data, err := os.ReadFile(a.Path)
		if err != nil {
			return nil, err
		}
		return map[string]string{"content": string(data)}, nil
	case "file_write":
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		_ = json.Unmarshal(args, &a)
		if !filepath.IsAbs(a.Path) {
			return nil, errBadTool("absolute path required")
		}
		if fi, err := os.Stat(a.Path); err == nil && !fi.Mode().IsRegular() {
			return nil, errBadTool("not a regular file")
		}
		if err := os.WriteFile(a.Path, []byte(a.Content), 0o644); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	}
	return nil, errBadTool("unhandled local tool: " + tool)
}

type errBadTool string

func (e errBadTool) Error() string { return string(e) }

// controlNext is the renderer's long-poll: it blocks until a renderer-bound command
// is queued (or ~25s elapse → 204, prompting the renderer to poll again).
func (s *Server) controlNext(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	cmd, ok := s.hub.Next(ctx)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, cmd)
}

// controlResult receives the renderer's answer to a command and hands it to the
// waiting mcpCall.
func (s *Server) controlResult(w http.ResponseWriter, r *http.Request) {
	var res control.Result
	if err := json.NewDecoder(r.Body).Decode(&res); err != nil || res.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.hub.Complete(res)
	w.WriteHeader(http.StatusNoContent)
}
