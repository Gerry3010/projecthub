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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client talks to a Passbubble server. BaseURL is the origin the API is rooted
// at; for the hosted web app this is the same-origin reverse-proxy prefix
// (e.g. "/pb"), for the TUI it is the absolute server URL.
type Client struct {
	BaseURL     string
	AccessToken string
	HTTP        *http.Client
}

// New creates a Client for baseURL (trailing slash optional).
func New(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// SetToken sets the Bearer access token sent on subsequent requests.
func (c *Client) SetToken(token string) { c.AccessToken = token }

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

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		var apiErr errorResponse
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		if apiErr.Error != "" {
			return fmt.Errorf("api error %d: %s", resp.StatusCode, apiErr.Error)
		}
		return fmt.Errorf("api error %d", resp.StatusCode)
	}

	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
