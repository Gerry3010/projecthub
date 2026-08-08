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
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Gerry3010/projecthub/internal/control"
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
	hub   *control.Hub // sidecar→renderer command channel for MCP renderer tools

	// getServer/setServer back the /server endpoint: the desktop UI reads and
	// changes the Passbubble upstream (device-local, account-independent). Nil ⇒
	// the endpoint reports empty / "not configurable".
	getServer func() string
	setServer func(string) error

	// notify is the desktop/in-app notification queue: POST /notify enqueues, the
	// renderer drains it via the /notify/next long-poll. Buffered + drop-oldest so a
	// producer (e.g. the chattr bridge) never blocks.
	notify chan notifyMsg
}

// notifyMsg is one queued desktop/in-app notification.
type notifyMsg struct {
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	Source string `json:"source,omitempty"` // e.g. "chattr", "terminal" (informational)
}

// New returns a native API server authenticated by token, using pty for terminals
// and tabs for the live browser-tab state (fed by the native-messaging host).
func New(token string, pty *ptyhost.Host, tabs *tabstate.Store) *Server {
	return &Server{token: token, pty: pty, tabs: tabs, hub: control.New(), notify: make(chan notifyMsg, 64)}
}

// SetServerHooks wires the /server endpoint to the live Passbubble upstream: get
// returns the current URL, set validates + applies + persists a new one.
func (s *Server) SetServerHooks(get func() string, set func(string) error) {
	s.getServer, s.setServer = get, set
}

// Handler builds the routed, auth-protected handler (mount it at /native).
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(s.auth)
	r.Get("/claude/suggestions", s.claudeSuggestions)
	r.Get("/claude/sessions", s.claudeSessions)
	r.Get("/claude/transcript", s.claudeTranscript)
	r.Get("/claude/tasks", s.claudeTasks)
	r.Post("/claude/resume", s.claudeResume)
	r.Post("/claude/chat", s.claudeChat)
	r.Post("/pty", s.ptyOpen)
	r.Get("/pty/{id}/ws", s.ptyWS)
	r.Get("/pty/{id}", s.ptyAlive)
	r.Delete("/pty/{id}", s.ptyClose)
	r.Post("/openin", s.openIn)
	r.Get("/server", s.serverGet)
	r.Post("/server", s.serverSet)
	r.Get("/file", s.fileRead)
	r.Post("/file", s.fileWrite)
	r.Get("/dir", s.dirList)
	r.Post("/mkdir", s.dirMake)
	r.Post("/move", s.fileMove)
	r.Post("/tabs/ingest", s.tabsIngest)
	r.Get("/tabs", s.tabsList)
	r.Post("/projects", s.projectsSet)
	r.Get("/projects", s.projectsList)
	r.Post("/tabs/command", s.tabsCommand)
	r.Get("/tabs/commands", s.tabsCommands)
	r.Get("/tabs/browsers", s.tabsBrowsers)
	r.Post("/pipepush/login", s.pipepushLogin)
	r.Get("/pipepush/pipelines", s.pipepushPipelines)
	r.Get("/pipepush/runs", s.pipepushRuns)
	r.Get("/redmine/issues", s.redmineIssues)
	r.Post("/notify", s.notifyPost)
	r.Get("/notify/next", s.notifyNext)
	// MCP: cmd/phmcp bridges Claude Code's MCP calls to these; renderer tools are
	// forwarded to the WASM UI over the control channel below.
	r.Get("/mcp/tools", s.mcpTools)
	r.Post("/mcp/call", s.mcpCall)
	r.Get("/control/next", s.controlNext)
	r.Post("/control/result", s.controlResult)
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

// claudeTranscript returns one session's full transcript, decoded into structured
// content blocks (?cwd=…&session_id=…), for the Claude tile's chat viewer.
func (s *Server) claudeTranscript(w http.ResponseWriter, r *http.Request) {
	cwd := r.URL.Query().Get("cwd")
	sessionID := r.URL.Query().Get("session_id")
	if cwd == "" || sessionID == "" {
		http.Error(w, "missing cwd/session_id", http.StatusBadRequest)
		return
	}
	entries, err := tabsession.ParseTranscript(cwd, sessionID)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, entries)
}

// claudeTasks returns a session's task list (?session_id=…), read from
// ~/.claude/tasks/<sessionId>/, so the Claude tile can show it as a checklist.
func (s *Server) claudeTasks(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "missing session_id", http.StatusBadRequest)
		return
	}
	tasks, err := tabsession.ScanClaudeTasks(sessionID)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, tasks)
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

// claudeChat runs one headless Claude turn (print mode) in cwd and returns immediately
// with the session id. The process runs to completion in the background, writing the
// normal session transcript (~/.claude/projects/<cwd>/<sessionId>.jsonl); the embedded
// sidebar chat streams the reply by polling that transcript — no terminal, no PTY.
func (s *Server) claudeChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cwd          string `json:"cwd"`
		Prompt       string `json:"prompt"`
		SessionID    string `json:"session_id"`
		SystemPrompt string `json:"system_prompt"`
		Resume       bool   `json:"resume"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Prompt == "" || req.SessionID == "" {
		http.Error(w, "prompt and session_id required", http.StatusBadRequest)
		return
	}
	cwd := req.Cwd
	if cwd == "" { // sidebar on the home view has no project cwd — fall back to $HOME
		if home, err := os.UserHomeDir(); err == nil {
			cwd = home
		}
	}
	name, args := local.ChatCommand(req.Prompt, req.SystemPrompt, req.SessionID, req.Resume)
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	cmd.Env = os.Environ() // inherit PATH so `claude` resolves like it does for the terminal
	if err := cmd.Start(); err != nil {
		httpError(w, err)
		return
	}
	go func() { _ = cmd.Wait() }() // reap the child; the UI streams via the transcript
	// Return the effective cwd so the UI polls the same transcript path (it may have
	// fallen back to $HOME when the sidebar chats from the home view).
	writeJSON(w, map[string]string{"session_id": req.SessionID, "cwd": cwd})
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

// ptyAlive reports whether a PTY session is still live (204) or gone (404) — the
// renderer checks this after a reload to decide reattach vs. start-fresh.
func (s *Server) ptyAlive(w http.ResponseWriter, r *http.Request) {
	if s.pty.Has(chi.URLParam(r, "id")) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Error(w, "unknown pty session", http.StatusNotFound)
}

// ─── Open-In ─────────────────────────────────────────────────────────────────

// openIn opens a URL or filesystem path in the system's default handler.
func (s *Server) openIn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type   string `json:"type"`           // "url" | "path"
		Target string `json:"target"`
		With   string `json:"with,omitempty"` // optional program, e.g. "code" (VS Code)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Target == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var err error
	switch {
	case req.With != "":
		err = local.OpenWith(req.With, req.Target)
	case req.Type == "path":
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

// ─── Passbubble server (device-local) ───────────────────────────────────────

// serverGet reports the current Passbubble upstream URL the /pb proxy forwards to.
func (s *Server) serverGet(w http.ResponseWriter, _ *http.Request) {
	url := ""
	if s.getServer != nil {
		url = s.getServer()
	}
	writeJSON(w, map[string]string{"url": url})
}

// serverSet points the /pb proxy at a new Passbubble upstream (validated + persisted
// device-locally by the setter). Takes effect immediately — no restart.
func (s *Server) serverSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if s.setServer == nil {
		http.Error(w, "server not configurable", http.StatusNotImplemented)
		return
	}
	if err := s.setServer(req.URL); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"url": req.URL})
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

// ─── pipepush proxy ──────────────────────────────────────────────────────────
//
// pipepush's server has no CORS, so the WASM UI (loaded from the sidecar's own
// origin) cannot reach it directly cross-origin. These routes are a thin
// same-origin relay: every payload stays exactly as encrypted as it arrived —
// the sidecar never sees a decryption key, only forwards the user's login
// credentials (for the login call) and JWT (via X-PP-Auth, for reads) straight
// through to the pipepush server the WASM UI names via ?base=. All crypto
// (KDF, private-key unwrap, payload decrypt) happens in WASM; see
// internal/pipepush/ppcrypto.

// maxPipepushBody caps the login request body; email+password+base URL is tiny.
const maxPipepushBody = 4 << 10 // 4 KiB

// pipepushHTTPTimeout bounds a single upstream pipepush call.
const pipepushHTTPTimeout = 15 * time.Second

// pipepushLogin relays POST /api/auth/login to the pipepush server named in the
// request body's "base" field, forwarding only email+password upstream.
func (s *Server) pipepushLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Base     string `json:"base"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPipepushBody)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validPipepushBase(req.Base) || req.Email == "" || req.Password == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	body, _ := json.Marshal(map[string]string{"email": req.Email, "password": req.Password})
	s.pipepushRelay(w, r, http.MethodPost, strings.TrimRight(req.Base, "/")+"/api/auth/login", bytes.NewReader(body))
}

// pipepushPipelines relays GET /api/projects/{project}/pipelines
// (?base=&project=), authorized by the caller's X-PP-Auth JWT.
func (s *Server) pipepushPipelines(w http.ResponseWriter, r *http.Request) {
	base := r.URL.Query().Get("base")
	project := r.URL.Query().Get("project")
	if !validPipepushBase(base) || project == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	target := strings.TrimRight(base, "/") + "/api/projects/" + url.PathEscape(project) + "/pipelines"
	s.pipepushRelay(w, r, http.MethodGet, target, nil)
}

// pipepushRuns relays GET /api/pipelines/{pipeline}/runs?limit=
// (?base=&pipeline=&limit=), authorized by the caller's X-PP-Auth JWT.
func (s *Server) pipepushRuns(w http.ResponseWriter, r *http.Request) {
	base := r.URL.Query().Get("base")
	pipeline := r.URL.Query().Get("pipeline")
	if !validPipepushBase(base) || pipeline == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	target := strings.TrimRight(base, "/") + "/api/pipelines/" + url.PathEscape(pipeline) + "/runs"
	if limit := r.URL.Query().Get("limit"); limit != "" {
		target += "?limit=" + url.QueryEscape(limit)
	}
	s.pipepushRelay(w, r, http.MethodGet, target, nil)
}

// pipepushRelay issues the upstream request (mapping the caller's X-PP-Auth
// header, if present, to the upstream Authorization: Bearer) and copies the
// upstream status/body/content-type straight through — errors included, so the
// WASM client sees the same message pipepush's own CLI would.
func (s *Server) pipepushRelay(w http.ResponseWriter, r *http.Request, method, target string, body io.Reader) {
	req, err := http.NewRequestWithContext(r.Context(), method, target, body)
	if err != nil {
		httpError(w, err)
		return
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if jwt := r.Header.Get("X-PP-Auth"); jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}
	client := &http.Client{Timeout: pipepushHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		httpError(w, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// validPipepushBase requires an absolute http(s) URL, guarding the relay
// against being pointed at a non-HTTP scheme. (Generic http(s) validator; the
// Redmine relay reuses it.)
func validPipepushBase(base string) bool {
	u, err := url.Parse(base)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// ─── redmine proxy ────────────────────────────────────────────────────────────
//
// Like pipepush, a Redmine server rarely sends CORS headers for the WASM origin,
// and its API key is a credential we don't want exposed to arbitrary JS. So the
// WASM UI reaches Redmine only through this thin same-origin relay: it forwards
// the caller's X-Redmine-Key as the upstream X-Redmine-API-Key header and copies
// the response straight back. The key is never persisted here.

// redmineDefaultLimit caps how many issues are fetched for the overview.
const redmineDefaultLimit = "50"

// redmineIssues relays GET {base}/issues.json for the project named by ?project=
// (optional), authorized by the caller's X-Redmine-Key header.
func (s *Server) redmineIssues(w http.ResponseWriter, r *http.Request) {
	base := r.URL.Query().Get("base")
	if !validPipepushBase(base) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	target := strings.TrimRight(base, "/") + "/issues.json?status_id=open&sort=updated_on:desc&limit=" + redmineDefaultLimit
	if project := r.URL.Query().Get("project"); project != "" {
		target += "&project_id=" + url.QueryEscape(project)
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		httpError(w, err)
		return
	}
	req.Header.Set("Accept", "application/json")
	if key := r.Header.Get("X-Redmine-Key"); key != "" {
		req.Header.Set("X-Redmine-API-Key", key)
	}
	client := &http.Client{Timeout: pipepushHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		httpError(w, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// ─── notifications ────────────────────────────────────────────────────────────
//
// A tiny fan-in queue for desktop/in-app notifications. Producers POST /notify
// (the renderer's own terminal triggers call the JS emit path directly, but an
// external bridge — e.g. the chattr realtime listener — POSTs here); the renderer
// drains them with the /notify/next long-poll and shows a toast + OS notification.

// maxNotifyBody caps the notify POST body (title+body are short).
const maxNotifyBody = 8 << 10 // 8 KiB

// notifyPost enqueues a notification. Non-blocking: if the buffer is full the oldest
// entry is dropped so a producer never blocks.
func (s *Server) notifyPost(w http.ResponseWriter, r *http.Request) {
	var m notifyMsg
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNotifyBody)).Decode(&m); err != nil || m.Title == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	select {
	case s.notify <- m:
	default:
		select { // drop oldest, then enqueue — best-effort
		case <-s.notify:
		default:
		}
		select {
		case s.notify <- m:
		default:
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// notifyNext long-polls for the next notification (~25s), returning it as JSON or 204
// when nothing arrived (the renderer immediately reconnects).
func (s *Server) notifyNext(w http.ResponseWriter, r *http.Request) {
	select {
	case m := <-s.notify:
		writeJSON(w, m)
	case <-time.After(25 * time.Second):
		w.WriteHeader(http.StatusNoContent)
	case <-r.Context().Done():
		w.WriteHeader(http.StatusNoContent)
	}
}

// ─── local file read ───────────────────────────────────────────────────────────

// maxFileRead caps a single /file response so a huge file can't exhaust memory.
const maxFileRead = 25 << 20 // 25 MiB

// fileWrite saves content to an absolute path (the editor tile's save action, and
// the MCP file.write tool). It overwrites an existing regular file or creates a new
// one (parent dir must exist); it refuses to clobber a directory/special file and
// caps size at maxFileRead. Returns the new mtime so the editor's file-watch won't
// treat its own save as an external change. Gated by the bearer token (loopback).
func (s *Server) fileWrite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		// Content is the file body as text; ContentB64 is a base64 alternative for
		// binary data (e.g. syncing a vault blob to disk). At most one is set.
		Content    string `json:"content"`
		ContentB64 string `json:"content_b64,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Path == "" || !filepath.IsAbs(req.Path) {
		http.Error(w, "absolute path required", http.StatusBadRequest)
		return
	}
	data := []byte(req.Content)
	if req.ContentB64 != "" {
		dec, err := base64.StdEncoding.DecodeString(req.ContentB64)
		if err != nil {
			http.Error(w, "bad base64", http.StatusBadRequest)
			return
		}
		data = dec
	}
	if len(data) > maxFileRead {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}
	if fi, err := os.Stat(req.Path); err == nil && !fi.Mode().IsRegular() {
		http.Error(w, "not a regular file", http.StatusBadRequest)
		return
	}
	if err := os.WriteFile(req.Path, data, 0o644); err != nil {
		httpError(w, err)
		return
	}
	var mtime int64
	if fi, err := os.Stat(req.Path); err == nil {
		mtime = fi.ModTime().UnixNano()
	}
	writeJSON(w, map[string]string{"mtime": strconv.FormatInt(mtime, 10)})
}

// dirList returns a single directory's entries (?path=<abs>), folders first then
// files alphabetically, for the file-tree tile's lazy per-folder expansion.
// Unreadable/vanished entries are skipped rather than failing the whole listing.
func (s *Server) dirList(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" || !filepath.IsAbs(path) {
		http.Error(w, "absolute path required", http.StatusBadRequest)
		return
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		httpError(w, err)
		return
	}
	out := make([]domain.DirEntry, 0, len(ents))
	for _, e := range ents {
		de := domain.DirEntry{Name: e.Name(), IsDir: e.IsDir()}
		if info, err := e.Info(); err == nil {
			de.Size = info.Size()
		}
		out = append(out, de)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir // folders first
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	writeJSON(w, out)
}

// fileMove moves/renames a file or folder (drag-and-drop in the file tree). Both
// paths must be absolute; refuses to overwrite an existing destination.
func (s *Server) fileMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Src == "" || req.Dst == "" || !filepath.IsAbs(req.Src) || !filepath.IsAbs(req.Dst) {
		http.Error(w, "absolute src and dst required", http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(req.Dst); err == nil {
		http.Error(w, "destination exists", http.StatusConflict)
		return
	}
	if err := os.Rename(req.Src, req.Dst); err != nil {
		httpError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// dirMake creates a directory (and any missing parents) at an absolute path.
func (s *Server) dirMake(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Path == "" || !filepath.IsAbs(req.Path) {
		http.Error(w, "absolute path required", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(req.Path, 0o755); err != nil {
		httpError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

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
