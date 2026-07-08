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
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/local"
	"github.com/Gerry3010/projecthub/internal/ptyhost"
	"github.com/Gerry3010/projecthub/internal/tabsession"
	"github.com/Gerry3010/projecthub/internal/tabstate"
)

// wsBearerPrefix carries the token in the WebSocket handshake's Sec-WebSocket-Protocol
// header, because the browser WebSocket API cannot set an Authorization header.
const wsBearerPrefix = "ph-bearer."

// Server exposes the native API. Construct with New and mount Handler under /native.
type Server struct {
	token string
	pty   *ptyhost.Host
	tabs  *tabstate.Store
}

// New returns a native API server authenticated by token, using pty for terminals
// and tabs for the live browser-tab state (fed by the native-messaging host).
func New(token string, pty *ptyhost.Host, tabs *tabstate.Store) *Server {
	return &Server{token: token, pty: pty, tabs: tabs}
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
	r.Get("/file", s.fileRead)
	r.Post("/tabs/ingest", s.tabsIngest)
	r.Get("/tabs", s.tabsList)
	r.Post("/projects", s.projectsSet)
	r.Get("/projects", s.projectsList)
	r.Post("/tabs/command", s.tabsCommand)
	r.Get("/tabs/commands", s.tabsCommands)
	r.Get("/tabs/browsers", s.tabsBrowsers)
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
	if req.Cmd == "" { // default to the user's login shell
		if sh := os.Getenv("SHELL"); sh != "" {
			req.Cmd = sh
		} else {
			req.Cmd = "/bin/bash"
		}
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

// ─── live browser tabs ───────────────────────────────────────────────────────

// tabsIngest records one browser's coupled tab groups, pushed by the native-messaging
// host (which the browser extension speaks to). The bearer token it presents comes
// from the sidecar's launch discovery file, so only a host started on this machine can
// post here.
func (s *Server) tabsIngest(w http.ResponseWriter, r *http.Request) {
	if s.tabs == nil {
		http.Error(w, "tabs unavailable", http.StatusServiceUnavailable)
		return
	}
	var b domain.LiveBrowserGroups
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTabsBody)).Decode(&b); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if b.Browser == "" {
		http.Error(w, "missing browser", http.StatusBadRequest)
		return
	}
	s.tabs.Set(b)
	w.WriteHeader(http.StatusNoContent)
}

// maxTabsBody caps a single tab report so a runaway extension can't exhaust memory.
const maxTabsBody = 4 << 20 // 4 MiB

// tabsList returns coupled groups for the WASM UI's tabs tile: scoped to one project
// with ?project=<id>, or every live browser's groups (debugging) without it.
func (s *Server) tabsList(w http.ResponseWriter, r *http.Request) {
	if s.tabs == nil {
		writeJSON(w, []domain.LiveTabGroup{})
		return
	}
	if project := r.URL.Query().Get("project"); project != "" {
		writeJSON(w, s.tabs.GroupsForProject(project))
		return
	}
	writeJSON(w, s.tabs.Snapshot())
}

// ─── project roster ─────────────────────────────────────────────────────────────

// maxRosterBody caps the roster push; a project list is tiny, so this is generous.
const maxRosterBody = 1 << 20 // 1 MiB

// projectsSet is pushed by the unlocked WASM app (id+title only) so the extension
// popup can list projects to couple tab groups to.
func (s *Server) projectsSet(w http.ResponseWriter, r *http.Request) {
	if s.tabs == nil {
		http.Error(w, "tabs unavailable", http.StatusServiceUnavailable)
		return
	}
	var roster []domain.RosterEntry
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRosterBody)).Decode(&roster); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.tabs.SetRoster(roster)
	w.WriteHeader(http.StatusNoContent)
}

// projectsList returns the current roster for the extension popup.
func (s *Server) projectsList(w http.ResponseWriter, _ *http.Request) {
	if s.tabs == nil {
		writeJSON(w, []domain.RosterEntry{})
		return
	}
	writeJSON(w, s.tabs.Roster())
}

// ─── tab commands (ProjectHub → extension) ──────────────────────────────────────

// tabsCommand queues a focus/reopen request for the target browser's extension.
func (s *Server) tabsCommand(w http.ResponseWriter, r *http.Request) {
	if s.tabs == nil {
		http.Error(w, "tabs unavailable", http.StatusServiceUnavailable)
		return
	}
	var c domain.TabCommand
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTabsBody)).Decode(&c); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if c.Browser == "" || c.Action == "" {
		http.Error(w, "missing browser/action", http.StatusBadRequest)
		return
	}
	s.tabs.Enqueue(c)
	w.WriteHeader(http.StatusNoContent)
}

// tabsCommands is polled by tabhost (per browser) to relay queued commands to the
// extension. Draining clears the queue, so each command is delivered once.
func (s *Server) tabsCommands(w http.ResponseWriter, r *http.Request) {
	browser := r.URL.Query().Get("browser")
	if browser == "" {
		http.Error(w, "missing browser", http.StatusBadRequest)
		return
	}
	if s.tabs == nil {
		writeJSON(w, []domain.TabCommand{})
		return
	}
	writeJSON(w, s.tabs.DrainCommands(browser))
}

// tabsBrowsers lists the browsers currently reporting in (e.g. "chrome", "brave"), for
// the "+ Neue Gruppe" UI to offer a target browser when the project has no existing
// coupled group to infer one from.
func (s *Server) tabsBrowsers(w http.ResponseWriter, _ *http.Request) {
	if s.tabs == nil {
		writeJSON(w, []string{})
		return
	}
	writeJSON(w, s.tabs.Browsers())
}

// ─── local file read ───────────────────────────────────────────────────────────

// maxFileRead caps a single /file response so a huge file can't exhaust memory.
const maxFileRead = 25 << 20 // 25 MiB

// fileRead serves a local file's bytes: used for the markdown tile's live reload,
// local background images, and file previews. With ?mtime=<unixnano>, it returns 304
// if the file is unchanged so pollers stay cheap. Access is gated by the bearer
// token (loopback), and reads are limited to regular files under maxFileRead.
func (s *Server) fileRead(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" || !filepath.IsAbs(path) {
		http.Error(w, "absolute path required", http.StatusBadRequest)
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if !fi.Mode().IsRegular() {
		http.Error(w, "not a regular file", http.StatusBadRequest)
		return
	}
	mtime := fi.ModTime().UnixNano()
	if q := r.URL.Query().Get("mtime"); q != "" && q == strconv.FormatInt(mtime, 10) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if fi.Size() > maxFileRead {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		httpError(w, err)
		return
	}
	if ct := mime.TypeByExtension(filepath.Ext(path)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("X-Mtime", strconv.FormatInt(mtime, 10))
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
