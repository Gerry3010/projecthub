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

package pipepush

import "time"

// The types below mirror the read side of pipepush's own internal/models
// (github.com/Gerry3010/pipepush) — only the fields ProjectHub's Pipepush tile
// actually consumes (project/pipeline/run listings + login). Field names and
// JSON tags match the real server exactly, since these are decoded straight
// from its HTTP responses via the sidecar's pipepush proxy (see
// internal/nativeserver's /pipepush/* routes).

// LoginResponse is POST /api/auth/login's response: a session JWT plus the
// user's E2E key material (EncryptedPrivateKey+KDFSalt need the same password
// to unwrap — see ppcrypto.DecryptPrivateKey).
type LoginResponse struct {
	JWT                 string `json:"jwt"`
	PublicKey           string `json:"publicKey"`
	EncryptedPrivateKey string `json:"encryptedPrivateKey"`
	KDFSalt             string `json:"kdfSalt"`
}

// PPProject is one pipepush project (GET /api/projects). EncryptedName is
// ECIES-encrypted with the user's own public key; decrypt with ppcrypto.
type PPProject struct {
	ID                   string    `json:"id"`
	EncryptedName        string    `json:"encryptedName"`
	EncryptedDescription string    `json:"encryptedDescription,omitempty"`
	CreatedAt            time.Time `json:"createdAt"`
}

// PPPipeline is one pipeline within a project (GET /api/projects/{id}/pipelines).
type PPPipeline struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"projectId"`
	EncryptedName string    `json:"encryptedName"`
	RoutingKey    string    `json:"routingKey,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

// PPRun is one CI run notification (GET /api/pipelines/{id}/runs). Status is
// the only field the server keeps in cleartext; everything else lives inside
// EncryptedPayload (see RunPayload).
type PPRun struct {
	ID               string    `json:"id"`
	ProjectID        string    `json:"projectId"`
	PipelineID       string    `json:"pipelineId"`
	Status           string    `json:"status"` // "success" | "failure" | "cancelled" | "running" | "skipped"
	EncryptedPayload string    `json:"encryptedPayload"`
	ReceivedAt       time.Time `json:"receivedAt"`
}

// RunPayload is a run's decrypted EncryptedPayload.
type RunPayload struct {
	Status   string `json:"status"`
	Pipeline string `json:"pipeline,omitempty"`
	RunID    string `json:"runId,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Duration string `json:"duration,omitempty"`
	Message  string `json:"message,omitempty"`
	Logs     string `json:"logs,omitempty"`
}
