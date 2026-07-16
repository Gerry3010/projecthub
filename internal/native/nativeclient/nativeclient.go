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

// Package nativeclient is the WASM UI's client for the ProjectHub sidecar's local
// API (/native/*). It is deliberately separate from pbclient so the two never mix:
// pbclient carries end-to-end-encrypted Passbubble traffic, nativeclient carries
// local-machine facts (paths, sessions, PTY control) that never touch the vault.
// On js/wasm net/http transparently uses the browser's fetch; the base URL and
// bearer token come from the Electron preload bridge (window.phNative).
package nativeclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/pipepush"
)

// Client talks to the sidecar. Zero value is unusable; use New. A nil *Client is a
// valid "not running under the desktop shell" sentinel — callers guard with Available.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New returns a client for the sidecar at base (e.g. http://127.0.0.1:54123) using
// the per-launch bearer token. Returns nil if base or token is empty, so hosted
// (non-Electron) builds simply have no native features.
func New(base, token string) *Client {
	if base == "" || token == "" {
		return nil
	}
	return &Client{base: base, token: token, http: &http.Client{Timeout: 15 * time.Second}}
}

// Available reports whether the sidecar API is usable (i.e. running under Electron).
func (c *Client) Available() bool { return c != nil }

// ClaudeSuggestion is a working dir Claude Code has been used in, offered as an
// "add this project?" candidate. Mirrors the sidecar's JSON; kept local so the WASM
// bundle needn't import the native-only tabsession/lz4 scanning code.
type ClaudeSuggestion struct {
	Cwd          string    `json:"cwd"`
	Title        string    `json:"title"`
	LastActive   time.Time `json:"last_active"`
	SessionCount int       `json:"session_count"`
}

// Suggestions returns Claude Code project candidates, newest-first.
func (c *Client) Suggestions(ctx context.Context) ([]ClaudeSuggestion, error) {
	var out []ClaudeSuggestion
	if err := c.get(ctx, "/native/claude/suggestions", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Sessions returns the Claude Code sessions recorded for a working directory.
func (c *Client) Sessions(ctx context.Context, cwd string) ([]domain.CodeSession, error) {
	var out []domain.CodeSession
	q := "/native/claude/sessions?cwd=" + url.QueryEscape(cwd)
	if err := c.get(ctx, q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Transcript returns one Claude Code session's full transcript, decoded into
// structured content blocks (text/thinking/tool_use/tool_result/image), for the
// Claude tile's chat viewer.
func (c *Client) Transcript(ctx context.Context, cwd, sessionID string) ([]domain.TranscriptEntry, error) {
	var out []domain.TranscriptEntry
	q := "/native/claude/transcript?cwd=" + url.QueryEscape(cwd) + "&session_id=" + url.QueryEscape(sessionID)
	if err := c.get(ctx, q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Resume opens a PTY running `claude --resume <sessionID>` in cwd and returns the
// pty id to attach a terminal WebSocket to.
func (c *Client) Resume(ctx context.Context, cwd, sessionID string, cols, rows uint16) (string, error) {
	body := map[string]any{"cwd": cwd, "session_id": sessionID, "cols": cols, "rows": rows}
	var out struct {
		PtyID string `json:"pty_id"`
	}
	if err := c.post(ctx, "/native/claude/resume", body, &out); err != nil {
		return "", err
	}
	return out.PtyID, nil
}

// OpenIn opens a URL ("url") or filesystem path ("path") in the system default app.
func (c *Client) OpenIn(ctx context.Context, kind, target string) error {
	return c.post(ctx, "/native/openin", map[string]string{"type": kind, "target": target}, nil)
}

// Server returns the Passbubble upstream URL the sidecar's /pb proxy currently
// forwards to (device-local, account-independent).
func (c *Client) Server(ctx context.Context) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := c.get(ctx, "/native/server", &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

// SetServer points the sidecar's /pb proxy at a new Passbubble upstream (validated +
// persisted device-locally). Takes effect immediately.
func (c *Client) SetServer(ctx context.Context, serverURL string) error {
	return c.post(ctx, "/native/server", map[string]string{"url": serverURL}, nil)
}

// LiveGroups returns the tab groups coupled to one project, as reported live by the
// browser extension(s) through the native-messaging host. Empty when nothing is
// coupled or no browser is reporting.
func (c *Client) LiveGroups(ctx context.Context, projectID string) ([]domain.LiveTabGroup, error) {
	var out []domain.LiveTabGroup
	if err := c.get(ctx, "/native/tabs?project="+url.QueryEscape(projectID), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetProjects pushes the project roster (id+title only) so the extension popup can
// list projects to couple tab groups to. Safe to call repeatedly; it replaces the
// previous roster.
func (c *Client) SetProjects(ctx context.Context, roster []domain.RosterEntry) error {
	return c.post(ctx, "/native/projects", roster, nil)
}

// SendCommand asks the target browser's extension to focus, reopen, or manage a
// tab/group (create/delete/rename/recolor a group, add/remove a tab).
func (c *Client) SendCommand(ctx context.Context, cmd domain.TabCommand) error {
	return c.post(ctx, "/native/tabs/command", cmd, nil)
}

// Browsers lists the browsers currently reporting in (e.g. "chrome", "brave"), never
// nil. Used to offer a target browser when creating a new tab group for a project that
// has no existing coupled group to infer one from.
func (c *Client) Browsers(ctx context.Context) ([]string, error) {
	var out []string
	if err := c.get(ctx, "/native/tabs/browsers", &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// ─── pipepush proxy ──────────────────────────────────────────────────────────
//
// pipepush has no CORS, so the WASM UI reaches it only through the sidecar's
// same-origin relay (see internal/nativeserver's /pipepush/* routes) — every
// payload it returns stays exactly as encrypted as pipepush sent it; decryption
// happens here in WASM via internal/pipepush/ppcrypto, never in the sidecar.

// PipepushLogin logs into the pipepush server at base, returning its JWT + the
// user's encrypted key material (unwrap with ppcrypto.DecryptPrivateKey).
func (c *Client) PipepushLogin(ctx context.Context, base, email, password string) (*pipepush.LoginResponse, error) {
	body := map[string]string{"base": base, "email": email, "password": password}
	var out pipepush.LoginResponse
	if err := c.post(ctx, "/native/pipepush/login", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PipepushPipelines lists a pipepush project's pipelines, authorized by jwt
// (from a prior PipepushLogin).
func (c *Client) PipepushPipelines(ctx context.Context, base, jwt, projectID string) ([]pipepush.PPPipeline, error) {
	var out []pipepush.PPPipeline
	q := "/native/pipepush/pipelines?base=" + url.QueryEscape(base) + "&project=" + url.QueryEscape(projectID)
	if err := c.getAuthed(ctx, q, jwt, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PipepushRuns lists one pipeline's runs (newest-first, capped at limit),
// authorized by jwt (from a prior PipepushLogin).
func (c *Client) PipepushRuns(ctx context.Context, base, jwt, pipelineID string, limit int) ([]pipepush.PPRun, error) {
	var out []pipepush.PPRun
	q := fmt.Sprintf("/native/pipepush/runs?base=%s&pipeline=%s&limit=%d",
		url.QueryEscape(base), url.QueryEscape(pipelineID), limit)
	if err := c.getAuthed(ctx, q, jwt, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// getAuthed is get, plus an X-PP-Auth header carrying the target pipepush JWT
// (the sidecar maps it to the upstream Authorization header — do() already
// sets Authorization to the sidecar's own native bearer, so the two must
// travel separately).
func (c *Client) getAuthed(ctx context.Context, path, jwt string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	if jwt != "" {
		req.Header.Set("X-PP-Auth", jwt)
	}
	return c.do(req, out)
}

// FetchFile reads a local file's bytes + content type via the sidecar (used for local
// background images, which the WASM UI turns into a data URL).
func (c *Client) FetchFile(ctx context.Context, path string) (data []byte, contentType string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/native/file?path="+url.QueryEscape(path), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("fetch file: %s", resp.Status)
	}
	data, err = io.ReadAll(resp.Body)
	return data, resp.Header.Get("Content-Type"), err
}

// ─── low-level ───────────────────────────────────────────────────────────────

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("sidecar %s: %s", req.URL.Path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
