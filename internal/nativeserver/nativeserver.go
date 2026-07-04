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

// Package nativeserver is the ProjectHub sidecar's local-machine API: the things a
// sandboxed browser cannot do — enumerate Claude Code projects/sessions on disk,
// run a PTY, and open URLs/paths in the system's default handler. It is bound to
// 127.0.0.1 and guarded by a per-launch bearer token so no other local process can
// drive it. It never touches Passbubble ciphertext or keys — crypto stays in WASM.
package nativeserver

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Gerry3010/projecthub/internal/local"
	"github.com/Gerry3010/projecthub/internal/ptyhost"
	"github.com/Gerry3010/projecthub/internal/tabsession"
)

// wsBearerPrefix carries the token in the WebSocket handshake's Sec-WebSocket-Protocol
// header, because the browser WebSocket API cannot set an Authorization header.
const wsBearerPrefix = "ph-bearer."

// Server exposes the native API. Construct with New and mount Handler under /native.
type Server struct {
	token string
	pty   *ptyhost.Host
}

// New returns a native API server authenticated by token, using pty for terminals.
func New(token string, pty *ptyhost.Host) *Server {
	return &Server{token: token, pty: pty}
}

// Handler builds the routed, auth-protected handler (mount it at /native).
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(s.auth)
	r.Get("/claude/suggestions", s.claudeSuggestions)
	r.Get("/claude/sessions", s.claudeSessions)
	r.Post("/claude/resume", s.claudeResume)
	r.Post("/pty", s.ptyOpen)
	r.Get("/pty/{id}/ws", s.ptyWS)
	r.Delete("/pty/{id}", s.ptyClose)
	r.Post("/openin", s.openIn)
	return r
}

// auth requires the per-launch bearer token, accepted either in the Authorization
// header (normal requests) or the Sec-WebSocket-Protocol header (WebSocket upgrades,
// which can't carry Authorization from a browser). Constant-time comparison.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(bearerFrom(r)), []byte(s.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerFrom extracts the presented token from either auth channel.
func bearerFrom(r *http.Request) string {
	if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return t
	}
	for _, p := range parseSubprotocols(r) {
		if t, ok := strings.CutPrefix(p, wsBearerPrefix); ok {
			return t
		}
	}
	return ""
}

// parseSubprotocols splits the comma-separated Sec-WebSocket-Protocol header.
func parseSubprotocols(r *http.Request) []string {
	raw := r.Header.Get("Sec-WebSocket-Protocol")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// ─── Claude Code ─────────────────────────────────────────────────────────────

// claudeSuggestions lists working dirs Claude Code has been used in, newest-first,
// as "add this project?" candidates. The client filters ones already added.
func (s *Server) claudeSuggestions(w http.ResponseWriter, _ *http.Request) {
	projects, err := tabsession.ScanClaudeProjects()
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, projects)
}

// claudeSessions lists the Claude Code sessions for one working dir (?cwd=…).
func (s *Server) claudeSessions(w http.ResponseWriter, r *http.Request) {
	cwd := r.URL.Query().Get("cwd")
	if cwd == "" {
		http.Error(w, "missing cwd", http.StatusBadRequest)
		return
	}
	sessions, err := tabsession.ScanClaudeSessions(cwd)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, sessions)
}

// claudeResume opens a PTY running `claude --resume <sessionId>` in cwd and returns
// its pty id; the client then attaches a WebSocket to stream it. The resume command
// is defined once in internal/local (ResumeCommand) so the embedded and external
// terminal paths agree.
func (s *Server) claudeResume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cwd       string `json:"cwd"`
		SessionID string `json:"session_id"`
		Cols      uint16 `json:"cols"`
		Rows      uint16 `json:"rows"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name, args := local.ResumeCommand(req.SessionID)
	id, err := s.pty.Open(ptyhost.OpenRequest{
		Cwd: req.Cwd, Cmd: name, Args: args, Cols: req.Cols, Rows: req.Rows,
	})
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, map[string]string{"pty_id": id})
}

// ─── PTY ─────────────────────────────────────────────────────────────────────

// ptyOpen starts an arbitrary command in a PTY (e.g. the user's shell) and returns
// its id.
func (s *Server) ptyOpen(w http.ResponseWriter, r *http.Request) {
	var req ptyhost.OpenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	id, err := s.pty.Open(req)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, map[string]string{"pty_id": id})
}

// ptyWS streams a PTY session over a WebSocket.
func (s *Server) ptyWS(w http.ResponseWriter, r *http.Request) {
	_ = s.pty.ServeWS(w, r, chi.URLParam(r, "id"))
}

// ptyClose terminates a PTY session.
func (s *Server) ptyClose(w http.ResponseWriter, r *http.Request) {
	s.pty.Close(chi.URLParam(r, "id"))
	w.WriteHeader(http.StatusNoContent)
}

// ─── Open-In ─────────────────────────────────────────────────────────────────

// openIn opens a URL or filesystem path in the system's default handler.
func (s *Server) openIn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type   string `json:"type"` // "url" | "path"
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Target == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var err error
	switch req.Type {
	case "path":
		err = local.OpenPath(req.Target)
	default:
		err = local.OpenURL(req.Target)
	}
	if err != nil {
		httpError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
