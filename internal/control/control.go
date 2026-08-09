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

// Package control is the sidecar→renderer command channel that lets MCP tools reach
// state only the WASM renderer can touch — the E2E-encrypted Passbubble vault
// (todos/projects) and the live tiling workspace. The sidecar holds NO vault keys,
// so these tools can't run there; instead the sidecar enqueues a command and the
// renderer long-polls for it, executes it against its Store/Workspace, and posts the
// result back. Delivery is a bounded in-memory queue with per-command result waiters
// — no persistence, no WebSocket (WASM-friendly plain HTTP long-poll).
package control

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
)

// Command is one renderer-bound tool invocation.
type Command struct {
	ID   string          `json:"id"`
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// Result is the renderer's reply to a Command (exactly one of Result/Error set).
type Result struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Hub brokers commands from the sidecar to a single connected renderer.
type Hub struct {
	queue   chan Command
	mu      sync.Mutex
	waiters map[string]chan Result
	seq     atomic.Int64
}

// New builds a Hub with a bounded command buffer.
func New() *Hub {
	return &Hub{queue: make(chan Command, 64), waiters: map[string]chan Result{}}
}

// ErrNoRenderer is returned by Call when no renderer picks up the command in time.
var ErrNoRenderer = errors.New("no renderer connected")

// Call enqueues tool(args) for the renderer and blocks until it returns a result or
// ctx is done (a missing/idle renderer surfaces as ctx deadline → ErrNoRenderer).
func (h *Hub) Call(ctx context.Context, tool string, args json.RawMessage) (json.RawMessage, error) {
	id := strconv.FormatInt(h.seq.Add(1), 10)
	ch := make(chan Result, 1)
	h.mu.Lock()
	h.waiters[id] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.waiters, id)
		h.mu.Unlock()
	}()

	select {
	case h.queue <- Command{ID: id, Tool: tool, Args: args}:
	case <-ctx.Done():
		return nil, ErrNoRenderer
	}
	select {
	case r := <-ch:
		if r.Error != "" {
			return nil, errors.New(r.Error)
		}
		return r.Result, nil
	case <-ctx.Done():
		return nil, ErrNoRenderer
	}
}

// Next long-polls for the next queued command; ok=false when ctx is done first.
func (h *Hub) Next(ctx context.Context) (Command, bool) {
	select {
	case c := <-h.queue:
		return c, true
	case <-ctx.Done():
		return Command{}, false
	}
}

// Complete delivers a renderer's result to the waiting Call (dropped if the caller
// already gave up).
func (h *Hub) Complete(r Result) {
	h.mu.Lock()
	ch := h.waiters[r.ID]
	h.mu.Unlock()
	if ch != nil {
		select {
		case ch <- r:
		default:
		}
	}
}
