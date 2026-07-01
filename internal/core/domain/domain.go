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

// Package domain defines ProjectHub's data model and how it maps onto Passbubble.
//
// Mapping principle (max privacy): every user-facing value — titles, names, paths,
// note bodies — is encrypted inside a Passbubble entry's encrypted_data. The only
// thing the server sees in cleartext is a *structural* marker we put in the entry's
// Name field (the Kind* constants below), which reveals that ProjectHub is in use
// but never any user content.
//
//	__PROJECT_HUB__/                 (root folder, RootFolderName)
//	├── ph-root        entry: RootIndex   (catalog of projects)
//	└── <project folder, name=ProjectFolderName>
//	    ├── ph-manifest   entry: Project    (title, description, slug)
//	    ├── ph-note       entries: NoteDoc
//	    ├── ph-tabset     entries: TabSet
//	    ├── ph-pin        entries: PinnedItem
//	    ├── ph-file       entries: FileBlob
//	    ├── ph-ccsession  entries: CodeSession   (Claude Code session references)
//	    └── ph-pipepush   entry:   PipepushLink  (at most one per project)
package domain

import "time"

// Structural folder names (cleartext, server-visible — not user content).
const (
	RootFolderName    = "__PROJECT_HUB__" // single root namespace folder
	ProjectFolderName = "ph-prj"          // generic name for every project folder
)

// Kind is the structural marker stored in a Passbubble entry's Name field. It lets
// ProjectHub classify entries without decrypting them.
type Kind string

const (
	KindRoot     Kind = "ph-root"     // the RootIndex catalog (one per account, in root folder)
	KindManifest Kind = "ph-manifest" // a project's metadata
	KindNote     Kind = "ph-note"
	KindTabSet   Kind = "ph-tabset"
	KindPin      Kind = "ph-pin"
	KindFile     Kind = "ph-file"

	KindCodeSession  Kind = "ph-ccsession" // a Claude Code session reference
	KindPipepushLink Kind = "ph-pipepush"  // link to a pipepush project (one per project)
)

// PassbubbleEntryType is the Passbubble `type` we use for all ProjectHub entries.
// "note" is always accepted by the backend; the real kind lives in the Name marker
// and the encrypted payload, not in this field.
const PassbubbleEntryType = "note"

// RootIndex is the decrypted payload of the single ph-root entry: a catalog of all
// projects so the client can list them after unlocking without decrypting every
// project's contents.
type RootIndex struct {
	Version  int          `json:"version"`
	Projects []ProjectRef `json:"projects"`
}

// ProjectRef is a project's entry in the RootIndex.
type ProjectRef struct {
	ID       string `json:"id"`        // ProjectHub project id (uuid we mint)
	FolderID string `json:"folder_id"` // Passbubble folder id of the project folder
	Title    string `json:"title"`
	Slug     string `json:"slug"` // filesystem-safe; mirrors to <IndexRoot>/<slug>/
}

// Project is the decrypted ph-manifest payload.
type Project struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Slug        string    `json:"slug"`
	CreatedAt   time.Time `json:"created_at"`
}

// NoteDoc is the decrypted ph-note payload.
type NoteDoc struct {
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TabSet is the decrypted ph-tabset payload: a saved set of browser tabs/windows.
type TabSet struct {
	Title   string    `json:"title"`
	Tabs    []Tab     `json:"tabs"`
	SavedAt time.Time `json:"saved_at"`
	Browser string    `json:"browser,omitempty"` // "firefox" | "chromium" | ...
}

// Tab is a single saved browser tab.
type Tab struct {
	URL    string `json:"url"`
	Title  string `json:"title,omitempty"`
	Window int    `json:"window"` // window index for multi-window restore
	Pinned bool   `json:"pinned,omitempty"`
}

// PinnedItem is the decrypted ph-pin payload: a reference to a local file/dir,
// stored relative to the machine-local index root (<IndexRoot>/<project slug>/).
// Absolute paths never leave the local machine.
type PinnedItem struct {
	Label   string `json:"label"`
	RelPath string `json:"rel_path"` // relative to <IndexRoot>/<slug>/
	IsDir   bool   `json:"is_dir"`
}

// CodeSession is the decrypted ph-ccsession payload: a reference to a Claude Code
// session so it can be resumed later. Claude Code persists each session as
// <sessionId>.jsonl under ~/.claude/projects/<cwd-as-dashes>/; resuming means
// running `claude --resume <SessionID>` in the original working directory.
type CodeSession struct {
	SessionID  string    `json:"session_id"` // == <sessionId>.jsonl filename (Claude Code session id)
	Title      string    `json:"title"`      // ai-title from the transcript, else first user prompt
	Cwd        string    `json:"cwd"`        // working dir the session ran in (used for --resume)
	LastActive time.Time `json:"last_active"`
}

// PipepushLink is the decrypted ph-pipepush payload: it couples a ProjectHub
// project to a pipepush project plus a webhook token. The token is secret but,
// like every payload, encrypted at rest in Passbubble. At most one per project.
type PipepushLink struct {
	BaseURL   string    `json:"base_url"`           // e.g. https://pipepush.geraldhofbauer.net
	ProjectID string    `json:"project_id"`         // pipepush project UUID
	Label     string    `json:"label,omitempty"`    // human label for the link
	Token     string    `json:"token,omitempty"`    // pp_… notification token for POST /api/webhook
	Pipeline  string    `json:"pipeline,omitempty"` // routing name (project-scoped tokens fan out by pipeline)
	LinkedAt  time.Time `json:"linked_at"`
}

// FileBlob is the decrypted ph-file payload: an uploaded file stored inline. Keep
// to small files in v1 (base64 in JSON inflates ~33%); large files are future work
// (chunking / external blob store).
type FileBlob struct {
	Filename string `json:"filename"`
	MIME     string `json:"mime"`
	Size     int64  `json:"size"`
	Bytes    []byte `json:"bytes"` // encoding/json renders []byte as base64
}
