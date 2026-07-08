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
package ptyhost

import (
	"encoding/binary"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"

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

// OpenRequest describes a PTY session to start.
type OpenRequest struct {
	Cwd  string   `json:"cwd"`
	Cmd  string   `json:"cmd"`
	Args []string `json:"args"`
	Cols uint16   `json:"cols"`
	Rows uint16   `json:"rows"`
	Env  []string `json:"env,omitempty"` // extra KEY=VALUE entries appended to the parent env
}

type session struct {
	ptmx *os.File
	cmd  *exec.Cmd
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
	return &Host{sessions: make(map[string]*session), max: max}
}

// Open starts req.Cmd inside a new PTY and returns its id. The caller then attaches
// a WebSocket via ServeWS(id) to stream it.
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
	h.mu.Lock()
	h.sessions[id] = &session{ptmx: ptmx, cmd: cmd}
	h.mu.Unlock()
	return id, nil
}

// get returns the session for id, if any.
func (h *Host) get(id string) (*session, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[id]
	return s, ok
}

// Close terminates a session's process and frees its PTY.
func (h *Host) Close(id string) {
	h.mu.Lock()
	s, ok := h.sessions[id]
	delete(h.sessions, id)
	h.mu.Unlock()
	if !ok {
		return
	}
	s.close()
}

func (s *session) close() {
	_ = s.ptmx.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.cmd.Wait()
}

// CloseAll terminates every live session (used on sidecar shutdown).
func (h *Host) CloseAll() {
	h.mu.Lock()
	sessions := h.sessions
	h.sessions = make(map[string]*session)
	h.mu.Unlock()
	for _, s := range sessions {
		s.close()
	}
}

// ServeWS upgrades the request to a WebSocket and pipes it to the PTY: server→client
// frames carry raw PTY output; client→server frames are opcode-prefixed (data or
// resize). The socket closing (or the process exiting) tears the session down. id is
// the session returned by Open; the caller extracts it from the route.
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
	// Same-origin is enforced by websocket.Accept (renderer origin == sidecar origin).
	ctx := r.Context()

	// PTY → client: copy raw output until the process exits or the PTY closes.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := s.ptmx.Read(buf)
			if n > 0 {
				if werr := c.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		// Process gone or PTY closed → close the socket and reap the session.
		_ = c.Close(websocket.StatusNormalClosure, "pty closed")
		h.Close(id)
	}()

	// client → PTY: dispatch by opcode.
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			break
		}
		if typ != websocket.MessageBinary || len(data) == 0 {
			continue
		}
		switch data[0] {
		case opData:
			if _, err := s.ptmx.Write(data[1:]); err != nil {
				h.Close(id)
				return nil
			}
		case opResize:
			if len(data) >= 5 {
				cols := binary.BigEndian.Uint16(data[1:3])
				rows := binary.BigEndian.Uint16(data[3:5])
				_ = pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
			}
		}
	}
	h.Close(id)
	return nil
}
