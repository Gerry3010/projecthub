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

// Package pipepush sends CI/CD status notifications to a pipepush server. It only
// needs a notification token (pp_…); pipepush's own end-to-end encryption is done
// server-side from the owning user's public key, so ProjectHub never touches
// pipepush key material. This is a native-only helper used by the TUI companion.
package pipepush

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Status values accepted by pipepush's webhook endpoint.
const (
	StatusSuccess   = "success"
	StatusFailure   = "failure"
	StatusCancelled = "cancelled"
	StatusRunning   = "running"
	StatusSkipped   = "skipped"
)

// WebhookRequest mirrors pipepush's models.WebhookRequest (POST /api/webhook).
type WebhookRequest struct {
	Token    string `json:"token"`
	Status   string `json:"status"`
	Pipeline string `json:"pipeline,omitempty"`
	RunID    string `json:"runId,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Duration string `json:"duration,omitempty"`
	Message  string `json:"message,omitempty"`
}

// SendWebhook posts a status notification to <baseURL>/api/webhook. The token in
// req authenticates the call; no user session is required.
func SendWebhook(ctx context.Context, baseURL string, req WebhookRequest) error {
	if req.Token == "" {
		return fmt.Errorf("pipepush: missing token")
	}
	if req.Status == "" {
		return fmt.Errorf("pipepush: missing status")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	url := strings.TrimRight(baseURL, "/") + "/api/webhook"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("pipepush: send webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("pipepush: webhook rejected (%s): %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}
