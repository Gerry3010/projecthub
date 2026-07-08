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

// Command tabhost is the Chrome/Chromium native-messaging host for ProjectHub's
// browser extension. The browser spawns it (per the installed host manifest) and
// speaks the native-messaging wire protocol over stdin/stdout: each message is a
// little-endian uint32 byte-length prefix followed by that many bytes of UTF-8 JSON.
//
// It relays two things between the extension and the running ProjectHub sidecar:
//   - extension → sidecar: coupled tab-group reports ("tabs") and project-roster
//     requests ("getProjects"), both forwarded to the sidecar's loopback API.
//   - sidecar → extension: queued commands (focus/reopen a tab or group), delivered
//     by polling the sidecar roughly once a second and pushed as unsolicited
//     "command" frames.
//
// Both directions authenticate with the per-launch bearer token read from the
// sidecar's discovery file, re-read on every call so a sidecar restart (new port +
// token) is picked up transparently. stdout is reserved for the protocol (shared by
// the read loop's replies and the command-poll's pushes, hence the write mutex), so
// all logging goes to stderr; the process exits cleanly when the browser closes the
// pipe (EOF).
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/discovery"
)

// maxMessage caps a single inbound message. A tab-group report is small; anything
// this large is a bug or abuse, not a legitimate report.
const maxMessage = 8 << 20 // 8 MiB

// commandPollInterval is how often tabhost checks the sidecar for queued commands.
const commandPollInterval = 1 * time.Second

func main() {
	log.SetFlags(0)
	log.SetPrefix("tabhost: ")
	h := newHost(os.Stdout, &http.Client{Timeout: 10 * time.Second})

	stop := make(chan struct{})
	go h.pollCommands(commandPollInterval, stop)

	err := h.run(os.Stdin)
	close(stop)
	if err != nil && !errors.Is(err, io.EOF) {
		log.Printf("exiting: %v", err)
		os.Exit(1)
	}
}

// host holds the state shared between the stdin read loop and the background
// command-poll goroutine: the HTTP client, the framed stdout (mutex-guarded since
// both sides write to it), and the most recently seen browser id (used to scope the
// command poll — set once the extension's first "tabs" report arrives).
type host struct {
	client *http.Client
	out    io.Writer
	outMu  sync.Mutex

	browserMu sync.RWMutex
	browser   string
}

func newHost(out io.Writer, client *http.Client) *host {
	return &host{client: client, out: out}
}

func (h *host) setBrowser(b string) {
	if b == "" {
		return
	}
	h.browserMu.Lock()
	h.browser = b
	h.browserMu.Unlock()
}

func (h *host) getBrowser() string {
	h.browserMu.RLock()
	defer h.browserMu.RUnlock()
	return h.browser
}

func (h *host) writeFrame(v any) error {
	h.outMu.Lock()
	defer h.outMu.Unlock()
	return writeMessage(h.out, v)
}

// run is the read→handle→reply loop, factored out for testing. It returns nil on a
// clean EOF (browser closed the port).
func (h *host) run(in io.Reader) error {
	for {
		raw, err := readMessage(in)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := h.writeFrame(h.handle(raw)); err != nil {
			return err
		}
	}
}

// ─── extension → sidecar ────────────────────────────────────────────────────────

// inEnvelope is one extension→host message: {"type":"tabs","payload":{…}} or
// {"type":"getProjects"}.
type inEnvelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ackReply acknowledges a "tabs" report (or reports a malformed/unknown message).
type ackReply struct {
	Type  string `json:"type"` // "ack"
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// projectsReply answers "getProjects" with the sidecar's project roster.
type projectsReply struct {
	Type  string               `json:"type"` // "projects"
	OK    bool                 `json:"ok"`
	Data  []domain.RosterEntry `json:"data,omitempty"`
	Error string               `json:"error,omitempty"`
}

// handle dispatches one decoded inbound message to its reply. Exported as a pure
// function of (raw bytes) → (reply value) so tests don't need to pipe stdio.
func (h *host) handle(raw []byte) any {
	var env inEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return ackReply{Type: "ack", OK: false, Error: "bad message: " + err.Error()}
	}
	switch env.Type {
	case "tabs":
		return h.handleTabs(env.Payload)
	case "getProjects":
		return h.handleGetProjects()
	default:
		return ackReply{Type: "ack", OK: false, Error: "unknown message type: " + env.Type}
	}
}

// handleTabs forwards a coupled tab-groups report to the sidecar verbatim (it already
// matches domain.LiveBrowserGroups, so tabhost stays a near-dumb relay) and remembers
// the reporting browser so the command poll knows which queue to drain.
func (h *host) handleTabs(payload json.RawMessage) ackReply {
	var groups domain.LiveBrowserGroups
	if err := json.Unmarshal(payload, &groups); err != nil {
		return ackReply{Type: "ack", OK: false, Error: "bad tabs payload: " + err.Error()}
	}
	h.setBrowser(groups.Browser)
	ep, err := discovery.Read()
	if err != nil {
		return ackReply{Type: "ack", OK: false, Error: "sidecar not running: " + err.Error()}
	}
	if err := h.post(ep, "/native/tabs/ingest", payload); err != nil {
		return ackReply{Type: "ack", OK: false, Error: err.Error()}
	}
	return ackReply{Type: "ack", OK: true}
}

// handleGetProjects fetches the current project roster for the extension popup.
func (h *host) handleGetProjects() projectsReply {
	ep, err := discovery.Read()
	if err != nil {
		return projectsReply{Type: "projects", OK: false, Error: "sidecar not running: " + err.Error()}
	}
	var roster []domain.RosterEntry
	if err := h.get(ep, "/native/projects", &roster); err != nil {
		return projectsReply{Type: "projects", OK: false, Error: err.Error()}
	}
	return projectsReply{Type: "projects", OK: true, Data: roster}
}

// ─── sidecar → extension (command poll) ─────────────────────────────────────────

// commandFrame is an unsolicited host→extension push carrying one queued command.
type commandFrame struct {
	Type string `json:"type"` // "command"
	domain.TabCommand
}

// pollCommands periodically drains the sidecar's command queue for the last-seen
// browser and pushes each command to the extension. It only starts acting once a
// "tabs" report has told it which browser it's relaying for.
func (h *host) pollCommands(interval time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			browser := h.getBrowser()
			if browser == "" {
				continue
			}
			cmds, err := h.fetchCommands(browser)
			if err != nil {
				continue // sidecar down/restarting — try again next tick
			}
			for _, c := range cmds {
				_ = h.writeFrame(commandFrame{Type: "command", TabCommand: c})
			}
		}
	}
}

func (h *host) fetchCommands(browser string) ([]domain.TabCommand, error) {
	ep, err := discovery.Read()
	if err != nil {
		return nil, err
	}
	var cmds []domain.TabCommand
	if err := h.get(ep, "/native/tabs/commands?browser="+url.QueryEscape(browser), &cmds); err != nil {
		return nil, err
	}
	return cmds, nil
}

// ─── HTTP helpers ────────────────────────────────────────────────────────────────

func (h *host) post(ep discovery.Endpoint, path string, body []byte) error {
	req, err := http.NewRequest(http.MethodPost, ep.Base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("sidecar: %s", resp.Status)
	}
	return nil
}

func (h *host) get(ep discovery.Endpoint, path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, ep.Base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("sidecar: %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ─── native-messaging framing ────────────────────────────────────────────────────

// readMessage reads one native-messaging frame: a little-endian uint32 length then
// that many bytes of JSON.
func readMessage(r io.Reader) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err // io.EOF here means the port was closed cleanly
	}
	n := binary.LittleEndian.Uint32(lenBuf[:])
	if n == 0 {
		return []byte{}, nil
	}
	if n > maxMessage {
		return nil, fmt.Errorf("message too large: %d bytes", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// writeMessage frames v as JSON with the little-endian uint32 length prefix.
func writeMessage(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(data)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
