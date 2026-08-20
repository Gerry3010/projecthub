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

// Package ptyhost runs interactive commands inside a pseudo-terminal and streams
// them to the Electron renderer over a WebSocket. It is the piece that lets the
// desktop app host `claude --resume` (and any shell) in an embedded xterm.js pane
// instead of spawning an external terminal emulator. Native-only (creack/pty).
//
// A session's process life is decoupled from the WebSocket: a per-session pump
// drains the PTY into a bounded scrollback ring even while no client is attached,
// so a renderer reload (Ctrl+R), a backgrounded window, or a dropped socket does
// NOT kill the process — the next attach replays the scrollback and resumes live.
// A session dies only when its process exits, on an explicit Close (tile closed),
// or when the idle reaper collects a long-detached session.
package ptyhost

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
	"github.com/google/uuid"
)

// Opcodes prefix every client→server WebSocket frame; server→client frames are raw
// PTY output (no prefix). Keeping one binary socket keeps data and resize ordered.
const (
	opData   = 0x00 // rest of frame = bytes to write to the PTY
	opResize = 0x01 // rest of frame = 4 bytes: uint16 cols, uint16 rows (big-endian)
)

// scrollbackCap bounds a session's replay buffer (the last N bytes of PTY output),
// replayed on (re)attach so a reloaded terminal shows recent context.
const scrollbackCap = 256 << 10 // 256 KiB

// idleReapAfter collects a session that has had no attached client AND produced no
// output for this long — a safety net for windows/tiles closed without an explicit
// Close (e.g. the whole window was closed).
const idleReapAfter = 30 * time.Minute

// OpenRequest describes a PTY session to start.
type OpenRequest struct {
	Cwd  string   `json:"cwd"`
	Cmd  string   `json:"cmd"`
	Args []string `json:"args"`
	Cols uint16   `json:"cols"`
	Rows uint16   `json:"rows"`
	Env  []string `json:"env,omitempty"` // extra KEY=VALUE entries appended to the parent env
}

// subscriber is the currently-attached client's delivery channel. dead is closed (once)
// when the subscriber is detached/replaced so the pump and writer unblock immediately.
type subscriber struct {
	ch   chan []byte
	dead chan struct{}
	once sync.Once
}

func (sub *subscriber) kill() { sub.once.Do(func() { close(sub.dead) }) }

type session struct {
	ptmx *os.File
	cmd  *exec.Cmd

	// what this session is running — kept for diagnostics (Host.List → app_info),
	// never for control flow.
	cwd     string
	cmdline string
	started time.Time

	mu       sync.Mutex
	ring     []byte      // bounded scrollback (last scrollbackCap bytes)
	sub      *subscriber // current subscriber, or nil when detached
	lastAct  time.Time   // last output / attach time (idle reaper)
	done     chan struct{}
	doneOnce sync.Once
}

// appendRing appends b to the scrollback, trimming to the cap. Caller holds s.mu.
func (s *session) appendRing(b []byte) {
	s.ring = append(s.ring, b...)
	if len(s.ring) > scrollbackCap {
		s.ring = append(s.ring[:0:0], s.ring[len(s.ring)-scrollbackCap:]...)
	}
}

// attach makes sub the session's sole subscriber (kicking any previous one) and returns
// it with a snapshot of the current scrollback to replay first.
func (s *session) attach() (*subscriber, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sub != nil {
		s.sub.kill()
	}
	sub := &subscriber{ch: make(chan []byte, 512), dead: make(chan struct{})}
	s.sub = sub
	s.lastAct = time.Now()
	snap := append([]byte(nil), s.ring...)
	return sub, snap
}

// detach removes sub if it is still the current subscriber; the process keeps running.
func (s *session) detach(sub *subscriber) {
	s.mu.Lock()
	if s.sub == sub {
		s.sub = nil
	}
	s.mu.Unlock()
	sub.kill()
}

// firstSubprotocol returns the first entry of a comma-separated Sec-WebSocket-Protocol
// header, trimmed. Empty if none.
func firstSubprotocol(header string) string {
	if header == "" {
		return ""
	}
	if i := strings.IndexByte(header, ','); i >= 0 {
		header = header[:i]
	}
	return strings.TrimSpace(header)
}

// Host owns the live PTY sessions, capped so a runaway renderer can't fork-bomb.
type Host struct {
	mu       sync.Mutex
	sessions map[string]*session
	max      int
}

// New returns a Host allowing at most max concurrent PTYs.
func New(max int) *Host {
	if max <= 0 {
		max = 16
	}
	h := &Host{sessions: make(map[string]*session), max: max}
	go h.reapLoop()
	return h
}

// Open starts req.Cmd inside a new PTY and returns its id. A pump goroutine begins
// draining it immediately (into the scrollback ring); the caller attaches a WebSocket
// via ServeWS(id) to stream it.
func (h *Host) Open(req OpenRequest) (string, error) {
	if req.Cmd == "" {
		return "", fmt.Errorf("ptyhost: empty command")
	}
	h.mu.Lock()
	if len(h.sessions) >= h.max {
		h.mu.Unlock()
		return "", fmt.Errorf("ptyhost: too many sessions (max %d)", h.max)
	}
	h.mu.Unlock()

	cmd := exec.Command(req.Cmd, req.Args...)
	cmd.Dir = req.Cwd
	env := os.Environ()
	if os.Getenv("TERM") == "" {
		env = append(env, "TERM=xterm-256color") // interactive shells/tools expect a TERM
	}
	cmd.Env = append(env, req.Env...)

	rows, cols := req.Rows, req.Cols
	if rows == 0 {
		rows = 24
	}
	if cols == 0 {
		cols = 80
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return "", fmt.Errorf("ptyhost: start %s: %w", req.Cmd, err)
	}

	id := uuid.NewString()
	s := &session{
		ptmx: ptmx, cmd: cmd,
		cwd:     req.Cwd,
		cmdline: cmdline(req),
		started: time.Now(),
		lastAct: time.Now(),
		done:    make(chan struct{}),
	}
	h.mu.Lock()
	h.sessions[id] = s
	h.mu.Unlock()
	go h.pump(id, s)
	return id, nil
}

// cmdline renders a session's command for diagnostics, bounded so a long argument
// (a Claude prompt, an --mcp-config blob) cannot bloat a status response.
func cmdline(req OpenRequest) string {
	line := strings.TrimSpace(req.Cmd + " " + strings.Join(req.Args, " "))
	if len(line) > 160 {
		line = line[:157] + "…"
	}
	return line
}

// SessionInfo describes one live PTY session. It is the shape app_info reports, so
// it carries only what helps answer "what is still running in there?".
type SessionInfo struct {
	ID       string `json:"id"`
	Cmd      string `json:"cmd,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	PID      int    `json:"pid,omitempty"`
	Attached bool   `json:"attached"` // false ⇒ running detached (no terminal on screen)
	UptimeS  int64  `json:"uptime_s"`
	IdleS    int64  `json:"idle_s"` // seconds since the last output/attach
}

// List snapshots the live sessions, sorted by id so callers get a stable order.
func (h *Host) List() []SessionInfo {
	h.mu.Lock()
	ids := make([]string, 0, len(h.sessions))
	snap := make(map[string]*session, len(h.sessions))
	for id, s := range h.sessions {
		ids = append(ids, id)
		snap[id] = s
	}
	h.mu.Unlock()
	sort.Strings(ids)

	now := time.Now()
	out := make([]SessionInfo, 0, len(ids))
	for _, id := range ids {
		s := snap[id]
		s.mu.Lock()
		info := SessionInfo{
			ID: id, Cmd: s.cmdline, Cwd: s.cwd,
			Attached: s.sub != nil,
			UptimeS:  int64(now.Sub(s.started).Seconds()),
			IdleS:    int64(now.Sub(s.lastAct).Seconds()),
		}
		s.mu.Unlock()
		if s.cmd != nil && s.cmd.Process != nil {
			info.PID = s.cmd.Process.Pid
		}
		out = append(out, info)
	}
	return out
}

// pump is the single reader of the PTY: it drains output into the scrollback ring and
// forwards it to the current subscriber (if any). It runs for the session's whole life,
// independent of any WebSocket, so the process survives detach. It reaps the session
// when the process exits / the PTY closes.
func (h *Host) pump(id string, s *session) {
	buf := make([]byte, 32*1024)
	for {
		n, rerr := s.ptmx.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			s.appendRing(chunk)
			s.lastAct = time.Now()
			sub := s.sub
			s.mu.Unlock()
			if sub != nil {
				select {
				case sub.ch <- chunk:
				case <-sub.dead: // subscriber went away — output stays in the ring
				case <-s.done:
					return
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	h.Close(id) // process exited / PTY closed → reap
}

// Has reports whether a live session exists for id (used by the reattach check).
func (h *Host) Has(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.sessions[id]
	return ok
}

// get returns the session for id, if any.
func (h *Host) get(id string) (*session, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[id]
	return s, ok
}

// Close terminates a session's process and frees its PTY (explicit kill: tile closed,
// process exited, or idle-reaped).
func (h *Host) Close(id string) {
	h.mu.Lock()
	s, ok := h.sessions[id]
	delete(h.sessions, id)
	h.mu.Unlock()
	if !ok {
		return
	}
	s.doneOnce.Do(func() { close(s.done) })
	s.mu.Lock()
	if s.sub != nil {
		s.sub.kill()
		s.sub = nil
	}
	s.mu.Unlock()
	s.close()
}

func (s *session) close() {
	_ = s.ptmx.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.cmd.Wait()
}

// reapLoop collects long-detached idle sessions (window/tile closed without an explicit
// Close) so they don't accumulate.
func (h *Host) reapLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		var stale []string
		h.mu.Lock()
		for id, s := range h.sessions {
			s.mu.Lock()
			detached, idle := s.sub == nil, now.Sub(s.lastAct) > idleReapAfter
			s.mu.Unlock()
			if detached && idle {
				stale = append(stale, id)
			}
		}
		h.mu.Unlock()
		for _, id := range stale {
			h.Close(id)
		}
	}
}

// CloseAll terminates every live session (used on sidecar shutdown).
func (h *Host) CloseAll() {
	h.mu.Lock()
	sessions := h.sessions
	h.sessions = make(map[string]*session)
	h.mu.Unlock()
	for _, s := range sessions {
		s.doneOnce.Do(func() { close(s.done) })
		s.close()
	}
}

// ServeWS upgrades the request to a WebSocket and attaches it to the PTY session: it
// first replays the scrollback, then streams live output (server→client frames are raw
// PTY bytes) while dispatching client→server opcode frames (data/resize). When the
// socket drops WITHOUT an explicit Close, the session keeps running (the pump keeps
// draining into the ring) so a later ServeWS can reattach by the same id.
func (h *Host) ServeWS(w http.ResponseWriter, r *http.Request, id string) error {
	s, ok := h.get(id)
	if !ok {
		http.Error(w, "unknown pty session", http.StatusNotFound)
		return fmt.Errorf("ptyhost: unknown session %q", id)
	}
	// Echo the client's requested subprotocol. The browser WebSocket API sends the
	// bearer token as a subprotocol (it can't set an Authorization header); if the
	// server doesn't confirm it, Chromium closes the connection immediately.
	opts := &websocket.AcceptOptions{}
	if p := firstSubprotocol(r.Header.Get("Sec-WebSocket-Protocol")); p != "" {
		opts.Subprotocols = []string{p}
	}
	c, err := websocket.Accept(w, r, opts)
	if err != nil {
		return err
	}

	sub, snap := s.attach()
	defer s.detach(sub)

	// Sole writer to the socket: replay the scrollback, then stream live chunks.
	go func() {
		bg := context.Background()
		if len(snap) > 0 {
			if werr := c.Write(bg, websocket.MessageBinary, snap); werr != nil {
				s.detach(sub)
				return
			}
		}
		for {
			select {
			case chunk := <-sub.ch:
				if werr := c.Write(bg, websocket.MessageBinary, chunk); werr != nil {
					s.detach(sub)
					return
				}
			case <-sub.dead:
				return
			case <-s.done:
				_ = c.Close(websocket.StatusNormalClosure, "pty closed")
				return
			}
		}
	}()

	// client → PTY: dispatch by opcode. On socket error the loop ends and the deferred
	// detach fires — the process survives for a later reattach.
	ctx := r.Context()
	for {
		typ, data, rerr := c.Read(ctx)
		if rerr != nil {
			break
		}
		if typ != websocket.MessageBinary || len(data) == 0 {
			continue
		}
		switch data[0] {
		case opData:
			if _, werr := s.ptmx.Write(data[1:]); werr != nil {
				break
			}
		case opResize:
			if len(data) >= 5 {
				cols := binary.BigEndian.Uint16(data[1:3])
				rows := binary.BigEndian.Uint16(data[3:5])
				_ = pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
			}
		}
	}
	return nil
}
