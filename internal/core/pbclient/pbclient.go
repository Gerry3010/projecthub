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

// Package pbclient is a standalone HTTP client for the Passbubble REST API,
// covering the subset ProjectHub needs (auth, entries, folders). It is modelled
// on Passbubble's cli/internal/apiclient (which is an internal package and thus
// cannot be imported) but exports its types and decodes the *full* login payload
// the server actually returns. It is WASM-safe: on js/wasm, net/http transparently
// uses the browser fetch API.
package pbclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// expiryLeeway is how long before the access token's expiry the client renews it
// on its own, so a request never goes out with a token that dies in flight.
const expiryLeeway = 60 * time.Second

// authPrefix marks the endpoints that carry their own credentials (login, refresh,
// logout). Requests there are never wrapped in a renew attempt — that would recurse.
const authPrefix = "/api/v1/auth/"

// Client talks to a Passbubble server. BaseURL is the origin the API is rooted
// at; for the hosted web app this is the same-origin reverse-proxy prefix
// (e.g. "/pb"), for the TUI it is the absolute server URL.
//
// The client keeps the session alive by itself: given a full login response (see
// SetSession) it renews the access token before it expires and, should a request
// still come back 401, refreshes and retries it once. That is what keeps a
// long-running desktop session from decaying into "api error 401" in every tile.
type Client struct {
	BaseURL string
	HTTP    *http.Client

	// Reauth, when set, is the last resort if the refresh token is gone or itself
	// rejected: a full re-login returning a fresh session. Nil ⇒ the 401 stands.
	Reauth func(ctx context.Context) (*LoginResponse, error)

	mu           sync.Mutex
	accessToken  string
	refreshToken string
	expiresAt    time.Time // zero ⇒ unknown, renew only reactively (on a 401)
	gen          uint64    // bumped on every session change; guards concurrent renews

	renewMu sync.Mutex // serialises renewals so a burst of 401s refreshes once
}

// New creates a Client for baseURL (trailing slash optional).
func New(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// SetToken sets the Bearer access token sent on subsequent requests. Prefer
// SetSession, which also adopts the refresh token so the session can renew itself.
func (c *Client) SetToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accessToken, c.gen = token, c.gen+1
}

// SetSession adopts a login/refresh response: the bearer token plus the refresh
// token and expiry the client needs to keep the session alive on its own.
func (c *Client) SetSession(resp *LoginResponse) {
	if resp == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accessToken = resp.AccessToken
	if resp.RefreshToken != "" { // a refresh may rotate it; an empty one keeps the old
		c.refreshToken = resp.RefreshToken
	}
	if resp.ExpiresIn > 0 {
		c.expiresAt = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	} else {
		c.expiresAt = time.Time{}
	}
	c.gen++
}

// Token returns the current bearer token (for callers that pass it on elsewhere).
func (c *Client) Token() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.accessToken
}

// ─── DTOs ───────────────────────────────────────────────────────────────────

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginResponse mirrors the map the server returns from Login/Register/VerifyTOTP
// (see backend auth.go::respondWithSession) — note models.LoginResponse omits the
// key material, so we decode the real, fuller payload here. All key fields are
// base64. A 2FA-gated login instead returns Status="2fa_required" + PendingToken.
type LoginResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`

	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	Role   string `json:"role"`

	PubX25519       string `json:"pub_x25519"`
	PubMLKEM768     string `json:"pub_mlkem768"`
	EncPrivX25519   string `json:"enc_priv_x25519"`
	EncPrivMLKEM768 string `json:"enc_priv_mlkem768"`
	KDFSalt         string `json:"kdf_salt"`
	KDFTime         uint32 `json:"kdf_time"`
	KDFMemory       uint32 `json:"kdf_memory"`

	// 2FA gate (HTTP 202)
	Status       string `json:"status"`
	PendingToken string `json:"pending_token"`
}

// RequiresTOTP reports whether login stopped at the 2FA step.
func (r *LoginResponse) RequiresTOTP() bool { return r.Status == "2fa_required" }

type EntryKey struct {
	UserID       string `json:"user_id"`
	EncryptedKey string `json:"encrypted_key"`
}

type CreateEntryRequest struct {
	FolderID      *string    `json:"folder_id,omitempty"`
	Type          string     `json:"type"`
	Name          string     `json:"name"`
	URL           string     `json:"url,omitempty"`
	MatchPatterns []string   `json:"match_patterns,omitempty"`
	EncryptedData string     `json:"encrypted_data"`
	DataNonce     string     `json:"data_nonce"`
	EntryKeys     []EntryKey `json:"entry_keys"`
}

type UpdateEntryRequest struct {
	FolderID      *string    `json:"folder_id,omitempty"`
	Name          string     `json:"name,omitempty"`
	URL           string     `json:"url,omitempty"`
	MatchPatterns []string   `json:"match_patterns,omitempty"`
	EncryptedData string     `json:"encrypted_data,omitempty"`
	DataNonce     string     `json:"data_nonce,omitempty"`
	EntryKeys     []EntryKey `json:"entry_keys,omitempty"`
}

type EntryResponse struct {
	ID            string    `json:"id"`
	FolderID      *string   `json:"folder_id,omitempty"`
	OwnerID       string    `json:"owner_id"`
	Type          string    `json:"type"`
	Name          string    `json:"name"`
	URL           string    `json:"url,omitempty"`
	EncryptedData string    `json:"encrypted_data,omitempty"`
	DataNonce     string    `json:"data_nonce,omitempty"`
	EntryKey      *EntryKey `json:"entry_key,omitempty"`
	CreatedAt     string    `json:"created_at"`
	UpdatedAt     string    `json:"updated_at"`
}

type CreateFolderRequest struct {
	Name     string  `json:"name"`
	ParentID *string `json:"parent_id,omitempty"`
}

type FolderResponse struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	ParentID  *string           `json:"parent_id,omitempty"`
	Children  []*FolderResponse `json:"children,omitempty"`
	CreatedAt string            `json:"created_at"`
}

type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// ─── Auth ───────────────────────────────────────────────────────────────────

func (c *Client) Login(ctx context.Context, req LoginRequest) (*LoginResponse, error) {
	var resp LoginResponse
	return &resp, c.do(ctx, http.MethodPost, "/api/v1/auth/login", req, &resp)
}

func (c *Client) Refresh(ctx context.Context, refreshToken string) (*LoginResponse, error) {
	var resp LoginResponse
	body := map[string]string{"refresh_token": refreshToken}
	return &resp, c.do(ctx, http.MethodPost, "/api/v1/auth/refresh", body, &resp)
}

func (c *Client) Logout(ctx context.Context, refreshToken string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/auth/logout", map[string]string{"refresh_token": refreshToken}, nil)
}

// ─── Entries ────────────────────────────────────────────────────────────────

func (c *Client) ListEntries(ctx context.Context) ([]EntryResponse, error) {
	var resp []EntryResponse
	return resp, c.do(ctx, http.MethodGet, "/api/v1/entries", nil, &resp)
}

func (c *Client) GetEntry(ctx context.Context, id string) (*EntryResponse, error) {
	var resp EntryResponse
	return &resp, c.do(ctx, http.MethodGet, "/api/v1/entries/"+url.PathEscape(id), nil, &resp)
}

func (c *Client) CreateEntry(ctx context.Context, req CreateEntryRequest) (*EntryResponse, error) {
	var resp EntryResponse
	return &resp, c.do(ctx, http.MethodPost, "/api/v1/entries", req, &resp)
}

func (c *Client) UpdateEntry(ctx context.Context, id string, req UpdateEntryRequest) (*EntryResponse, error) {
	var resp EntryResponse
	return &resp, c.do(ctx, http.MethodPut, "/api/v1/entries/"+url.PathEscape(id), req, &resp)
}

func (c *Client) DeleteEntry(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/entries/"+url.PathEscape(id), nil, nil)
}

// ─── Folders ────────────────────────────────────────────────────────────────

func (c *Client) ListFolders(ctx context.Context) ([]FolderResponse, error) {
	var resp []FolderResponse
	return resp, c.do(ctx, http.MethodGet, "/api/v1/folders", nil, &resp)
}

// CreateFolder creates a folder and returns its new ID.
func (c *Client) CreateFolder(ctx context.Context, req CreateFolderRequest) (string, error) {
	var resp struct {
		ID string `json:"id"`
	}
	return resp.ID, c.do(ctx, http.MethodPost, "/api/v1/folders", req, &resp)
}

func (c *Client) DeleteFolder(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/folders/"+url.PathEscape(id), nil, nil)
}

// ─── transport ──────────────────────────────────────────────────────────────

// APIError is a non-2xx response from the Passbubble API. It is returned as a typed
// error so callers (and the renew path below) can tell an expired session (401)
// apart from any other failure.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("api error %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("api error %d", e.Status)
}

// isUnauthorized reports whether err is a 401 — an expired or invalid access token.
func isUnauthorized(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized
}

// errNoRenew is returned when the session cannot be renewed (no refresh token left
// and no Reauth hook) — the caller then surfaces the original 401.
var errNoRenew = errors.New("session cannot be renewed")

// do sends a request, keeping the session alive around it: renew first when the
// access token is about to expire, and on a 401 renew + retry the request once.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var raw []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		raw = b
	}
	// The auth endpoints carry their own credentials — send them straight through.
	if strings.HasPrefix(path, authPrefix) {
		return c.doOnce(ctx, method, path, raw, out)
	}

	c.renewIfExpiring(ctx)
	gen := c.generation()
	err := c.doOnce(ctx, method, path, raw, out)
	if !isUnauthorized(err) {
		return err
	}
	if renewErr := c.renew(ctx, gen); renewErr != nil {
		return err // the 401 is the useful error; the renew failure is its cause
	}
	return c.doOnce(ctx, method, path, raw, out)
}

// doOnce performs a single request with the current bearer token.
func (c *Client) doOnce(ctx context.Context, method, path string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok := c.Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		var apiErr errorResponse
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		return &APIError{Status: resp.StatusCode, Message: apiErr.Error}
	}

	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// ─── session renewal ────────────────────────────────────────────────────────

// generation returns the current session generation (changes on every renewal).
func (c *Client) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// renewIfExpiring renews ahead of time when the access token's known lifetime is
// nearly over, so requests don't have to fail first. Best effort: a failure here
// just means the request goes out with the old token and takes the 401 path.
func (c *Client) renewIfExpiring(ctx context.Context) {
	c.mu.Lock()
	exp, gen := c.expiresAt, c.gen
	c.mu.Unlock()
	if exp.IsZero() || time.Until(exp) > expiryLeeway {
		return
	}
	_ = c.renew(ctx, gen)
}

// renew swaps in a fresh session: refresh token first, then the Reauth hook (a full
// re-login) if the refresh token is missing or itself rejected — that case shows up
// after the refresh token's own lifetime has passed, e.g. a desktop window left open
// over a long weekend. gen is the session generation the caller saw; if it has moved
// on, another goroutine already renewed and this is a no-op (so a burst of parallel
// 401s from several tiles triggers exactly one refresh).
func (c *Client) renew(ctx context.Context, gen uint64) error {
	c.renewMu.Lock()
	defer c.renewMu.Unlock()
	if c.generation() != gen {
		return nil
	}

	c.mu.Lock()
	rt := c.refreshToken
	c.mu.Unlock()
	if rt != "" {
		if resp, err := c.Refresh(ctx, rt); err == nil && resp.AccessToken != "" {
			c.SetSession(resp)
			return nil
		}
	}

	if c.Reauth == nil {
		return errNoRenew
	}
	resp, err := c.Reauth(ctx)
	if err != nil {
		return err
	}
	if resp == nil || resp.AccessToken == "" {
		return errNoRenew
	}
	c.SetSession(resp)
	return nil
}
