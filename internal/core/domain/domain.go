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

import (
	"path/filepath"
	"time"
)

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
	KindTodo     Kind = "ph-todo"
	KindTabSet   Kind = "ph-tabset"
	KindPin      Kind = "ph-pin"
	KindFile     Kind = "ph-file"

	KindCodeSession  Kind = "ph-ccsession" // a Claude Code session reference
	KindPipepushLink Kind = "ph-pipepush"  // link to a pipepush project (one per project)
	KindLayout       Kind = "ph-layout"    // the tiling workspace layout (one per project)
)

// PassbubbleEntryType is the Passbubble `type` we use for all ProjectHub entries.
// "note" is always accepted by the backend; the real kind lives in the Name marker
// and the encrypted payload, not in this field.
const PassbubbleEntryType = "note"

// Theming. Colors are UI-only (never sensitive), but like every payload they still
// live inside the encrypted RootIndex/manifest, so a chosen accent stays private.
const (
	ColorIndigo = "#6366f1"
	ColorTeal   = "#14b8a6"
	ColorViolet = "#a855f7"
)

// DefaultPalette is the built-in preset set offered as one-click swatches.
var DefaultPalette = []string{ColorIndigo, ColorTeal, ColorViolet}

// DefaultAccent is the app accent used until the user picks their own.
const DefaultAccent = ColorIndigo

// AutoColor returns a stable palette color derived from seed (typically a project
// id), so every project gets a distinct-but-consistent default without any input.
// Empty seed falls back to the default accent.
func AutoColor(seed string) string {
	if seed == "" {
		return DefaultAccent
	}
	// FNV-1a over the seed, then index into the palette.
	var h uint32 = 2166136261
	for i := 0; i < len(seed); i++ {
		h ^= uint32(seed[i])
		h *= 16777619
	}
	return DefaultPalette[int(h%uint32(len(DefaultPalette)))]
}

// RootIndex is the decrypted payload of the single ph-root entry: a catalog of all
// projects so the client can list them after unlocking without decrypting every
// project's contents.
type RootIndex struct {
	Version  int          `json:"version"`
	Projects []ProjectRef `json:"projects"`
	// AccentColor is the account-level app accent (a CSS hex like "#6366f1"). Empty
	// means "use DefaultAccent". Stored here so the choice syncs across devices.
	AccentColor string `json:"accent_color,omitempty"`
	// Background is the account-level default wallpaper/glass settings; a project
	// without its own Background inherits this. Nil means the flat --bg color.
	Background *Background `json:"background,omitempty"`
	// SearchEngine is the account-level default search engine for browser tiles
	// (a key like "brave"/"ddg"/"google"/"startpage"). Empty means the client
	// default. Stored here so the choice syncs across devices via Passbubble.
	SearchEngine string `json:"search_engine,omitempty"`
}

// Background describes the app/project wallpaper and the glassmorphism of panels.
// Applied as CSS variables; the image shows through translucent, blurred tiles.
type Background struct {
	Type  string  `json:"type,omitempty"`  // "" (flat --bg) | "color" | "image"
	Color string  `json:"color,omitempty"` // CSS hex for Type=="color"
	Image string  `json:"image,omitempty"` // Type=="image": a ph-file entry id (E2E, synced) OR "file:<abs path>" (local)
	Alpha float64 `json:"alpha,omitempty"` // panel translucency 0..1 (0 ⇒ opaque default)
	Blur  int     `json:"blur,omitempty"`  // panel backdrop-blur in px (blurs the wallpaper behind tiles)
	Dim   float64 `json:"dim,omitempty"`   // 0..1 dark overlay over the wallpaper for readability
}

// ProjectRef is a project's entry in the RootIndex.
type ProjectRef struct {
	ID       string `json:"id"`        // ProjectHub project id (uuid we mint)
	FolderID string `json:"folder_id"` // Passbubble folder id of the project folder
	Title    string `json:"title"`
	Slug     string `json:"slug"` // filesystem-safe; mirrors to <IndexRoot>/<slug>/
	// LocalPath is the project's real working directory on this machine (e.g. the
	// Claude Code cwd it was created from). When empty, the local dir falls back to
	// the legacy <IndexRoot>/<slug> convention — see Cwd. Mirrored from Project so
	// the list view resolves paths without decrypting every manifest. It lives in
	// the encrypted payload, so it stays private; it is machine-specific, so multi-
	// device path handling is future work.
	LocalPath string `json:"local_path,omitempty"`
	// Color is the project's accent (CSS hex, mirrored from the manifest) so the
	// list view can tint each project without decrypting it. Empty on projects
	// created before colors existed — see AccentColor for the fallback.
	Color string `json:"color,omitempty"`
	// Background mirrors the manifest's per-project wallpaper/glass override; nil ⇒
	// inherit the account default (RootIndex.Background).
	Background *Background `json:"background,omitempty"`
}

// AccentColor returns the project's accent, falling back to a stable auto color
// derived from its id when unset (older projects, or ones never customized).
func (r ProjectRef) AccentColor() string {
	if r.Color != "" {
		return r.Color
	}
	return AutoColor(r.ID)
}

// Cwd resolves the project's local working directory: LocalPath if set, else the
// legacy <indexRoot>/<slug> convention. This is the single resolution both the TUI
// and the sidecar use so old (path-less) and new projects behave consistently.
func (r ProjectRef) Cwd(indexRoot string) string {
	if r.LocalPath != "" {
		return r.LocalPath
	}
	return filepath.Join(indexRoot, r.Slug)
}

// Project is the decrypted ph-manifest payload.
type Project struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Description string      `json:"description,omitempty"`
	Slug        string      `json:"slug"`
	LocalPath   string      `json:"local_path,omitempty"` // real cwd on this machine; see ProjectRef.Cwd
	Color       string      `json:"color,omitempty"`      // accent (CSS hex); mirrored to ProjectRef.Color
	Background  *Background `json:"background,omitempty"` // per-project wallpaper/glass; mirrored to ProjectRef.Background
	CreatedAt   time.Time   `json:"created_at"`
}

// NoteDoc is the decrypted ph-note payload.
type NoteDoc struct {
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TodoItem is the decrypted ph-todo payload: one entry in a project's checklist.
// Each todo is its own Passbubble entry so an item can be toggled or removed on its
// own (mirrors ph-note). Ordered newest-first when listed.
type TodoItem struct {
	Text      string    `json:"text"`
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"created_at"`
	DoneAt    time.Time `json:"done_at"`
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

// Live browser-tab wire types. These are ephemeral: never encrypted or persisted —
// they live only in the sidecar's in-memory tabstate and are served to the WASM UI
// over /native/tabs. Only tab groups the user has *coupled* to a project in the
// extension popup are reported, so a browser with hundreds of tabs stays manageable.

// LiveBrowserGroups is one browser's coupled tab groups, reported live by the browser
// extension via the native-messaging host on every relevant change.
type LiveBrowserGroups struct {
	Browser   string         `json:"browser"` // "chrome" | "chromium" | "brave" | "edge" | "vivaldi"
	Groups    []LiveTabGroup `json:"groups"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// LiveTabGroup is one Chrome tab group coupled to a ProjectHub project. GroupKey is a
// stable (title-derived) key used for coupling; GroupID is the live Chrome group id
// (session-scoped, used to focus/reopen the group).
type LiveTabGroup struct {
	ProjectID string    `json:"project_id"`
	GroupKey  string    `json:"group_key"`
	Title     string    `json:"title"`
	Color     string    `json:"color"` // Chrome tab-group color name: grey/blue/red/…
	GroupID   int       `json:"group_id"`
	Browser   string    `json:"browser,omitempty"` // set by the sidecar on read, not by the extension
	Tabs      []LiveTab `json:"tabs"`
}

// LiveTab is a single currently-open browser tab. It carries a favicon URL, the active
// flag, and the live Chrome tab/window ids (used to focus an existing tab) — all of
// which the persisted domain.Tab deliberately omits.
type LiveTab struct {
	URL        string `json:"url"`
	Title      string `json:"title,omitempty"`
	FavIconURL string `json:"fav_icon_url,omitempty"`
	TabID      int    `json:"tab_id,omitempty"`
	WindowID   int    `json:"window_id"`
	Active     bool   `json:"active,omitempty"`
	Pinned     bool   `json:"pinned,omitempty"`
}

// RosterEntry is one project's id+title, pushed by the unlocked WASM app into the
// sidecar so the extension popup can list projects to couple groups to. Titles are the
// only user content that reaches the sidecar here, over the same local channel the tab
// URLs/titles already use; nothing is persisted.
type RosterEntry struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// TabCommand is a ProjectHub → extension request (via the sidecar command queue and the
// native-messaging host) to act on a live tab/group in the browser. Actions:
// "focusTab" | "focusGroup" | "openGroup" | "createGroup" | "deleteGroup" | "renameGroup" |
// "recolorGroup" | "addTab" | "removeTab".
type TabCommand struct {
	Browser  string   `json:"browser"`
	Action   string   `json:"action"`
	TabID    int      `json:"tab_id,omitempty"`
	WindowID int      `json:"window_id,omitempty"`
	GroupID  int      `json:"group_id,omitempty"`
	GroupKey string   `json:"group_key,omitempty"`
	URLs     []string `json:"urls,omitempty"` // fallback for reopening a closed group

	// Title/Color/ProjectID/URL back createGroup/renameGroup/recolorGroup/addTab: Title
	// names a new or renamed group, Color is a Chrome tab-group color name, ProjectID
	// couples a freshly created group to a project, URL is the tab to add.
	Title     string `json:"title,omitempty"`
	Color     string `json:"color,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	URL       string `json:"url,omitempty"`
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

// TranscriptEntry is one line of a Claude Code session transcript (~/.claude/
// projects/<cwd>/<sessionId>.jsonl), decoded into structured content blocks.
// Unlike CodeSession (which only tracks title/cwd/last-active for the session
// list), this carries every message's content so a tile can render the full
// conversation — text, thinking, tool calls/results. Never persisted: it is
// read live off disk by the sidecar (see internal/tabsession.ParseTranscript)
// and served to the WASM UI over /native/claude/transcript.
type TranscriptEntry struct {
	Role        string            `json:"role"` // "user" | "assistant"
	Timestamp   time.Time         `json:"timestamp"`
	IsSidechain bool              `json:"is_sidechain,omitempty"`
	Blocks      []TranscriptBlock `json:"blocks"`
}

// TranscriptBlock is one content block within a transcript entry's message.
// Which fields are set depends on Kind: text/thinking use Text; tool_use uses
// ToolName+ToolInput; tool_result uses Result(+IsError); image uses ImageMIME
// (the image bytes themselves are not carried — too large for a live view).
type TranscriptBlock struct {
	Kind      string `json:"kind"` // "text" | "thinking" | "tool_use" | "tool_result" | "image"
	Text      string `json:"text,omitempty"`
	ToolName  string `json:"tool_name,omitempty"`
	ToolInput string `json:"tool_input,omitempty"`
	Result    string `json:"result,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	ImageMIME string `json:"image_mime,omitempty"`
}

// PipepushLink is the decrypted ph-pipepush payload: it couples a ProjectHub
// project to a pipepush project plus a webhook token. The token is secret but,
// like every payload, encrypted at rest in Passbubble. At most one per project.
//
// Email/Password are the user's pipepush account credentials, needed to read
// runs (the pp_… Token below only authorizes the outbound webhook, not reads —
// see internal/pipepush's package doc). Like everything here they're encrypted
// at rest in the vault; ProjectHub only sends them to the sidecar's pipepush
// proxy (internal/nativeserver) which relays them straight to the pipepush
// server for POST /api/auth/login and never persists them.
type PipepushLink struct {
	BaseURL   string    `json:"base_url"`           // e.g. https://pipepush.geraldhofbauer.net
	ProjectID string    `json:"project_id"`         // pipepush project UUID
	Label     string    `json:"label,omitempty"`    // human label for the link
	Token     string    `json:"token,omitempty"`    // pp_… notification token for POST /api/webhook
	Pipeline  string    `json:"pipeline,omitempty"` // routing name (project-scoped tokens fan out by pipeline)
	Email     string    `json:"email,omitempty"`    // pipepush account email, for reading runs
	Password  string    `json:"password,omitempty"` // pipepush account password, for reading runs
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

// TileType identifies a workspace tile's content. terminal/browser/markdown are
// "island" tiles hosted by JS (web/shell.js) as foreign DOM that survives relayout;
// notes/files/sessions are native go-app views.
type TileType string

const (
	TileTerminal TileType = "terminal"
	TileBrowser  TileType = "browser"
	TileMarkdown TileType = "markdown"
	TileNotes    TileType = "notes"
	TileTodo     TileType = "todo"
	TileFiles    TileType = "files"
	TileSessions TileType = "sessions"
	TileTabs     TileType = "tabs"     // live open browser tabs, fed by the browser extension
	TileClaude   TileType = "claude"   // Claude Code chat overview + transcript viewer/starter
	TilePipepush TileType = "pipepush" // pipepush CI run overview + detail
)

// Layout is the decrypted ph-layout payload: a project's Warp-style tiling workspace,
// persisted per project so it reopens exactly as left. Root is nil for an empty
// workspace. One ph-layout entry per project folder.
type Layout struct {
	Version int         `json:"version"`
	Root    *LayoutNode `json:"root,omitempty"`
}

// LayoutNode is a node in the binary split tree. It is EITHER a split (Dir set, with
// children A and B and a Ratio for A's share) OR a leaf tile (PaneID + Type set).
type LayoutNode struct {
	// split node
	Dir   string      `json:"dir,omitempty"`   // "row" (side by side) | "col" (stacked); empty ⇒ leaf
	Ratio float64     `json:"ratio,omitempty"` // A's fraction of the split (0.05–0.95); 0 ⇒ 0.5
	A     *LayoutNode `json:"a,omitempty"`
	B     *LayoutNode `json:"b,omitempty"`

	// leaf node
	PaneID string            `json:"pane_id,omitempty"` // stable per-instance id (uuid)
	Type   TileType          `json:"type,omitempty"`
	Params map[string]string `json:"params,omitempty"` // instance state: cwd, session_id, url, path, note_id, …
}

// IsLeaf reports whether the node is a tile (as opposed to a split).
func (n *LayoutNode) IsLeaf() bool { return n != nil && n.Dir == "" }
