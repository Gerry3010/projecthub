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

package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/store"
	"github.com/Gerry3010/projecthub/internal/native/nativeclient"
)

// Workspace is a project's Warp-style tiling workspace. go-app owns the split tree;
// foreign-DOM tiles (terminal/browser/markdown) are hosted by web/shell.js and keyed
// by paneID so they survive relayout. The layout persists per project (ph-layout).
type Workspace struct {
	app.Compo

	Store  *store.Store
	Ref    domain.ProjectRef
	Back   func(ctx app.Context)
	Native *nativeclient.Client // nil in the hosted browser build
	// RegisterClaudeOpener lets the app-wide Claude sidebar (Root) open a Claude
	// terminal in THIS workspace; called once on mount with a ready-to-use opener.
	// A non-empty sessionID continues that conversation (see claudeTileParams).
	RegisterClaudeOpener func(open func(ctx app.Context, cwd, prompt, sessionID string))
	// OnColor notifies the parent (Root) that this project's accent changed, so it can
	// keep its project list (and thus the rail dot) in sync. nil in the hosted build.
	OnColor func(color string)

	layout   domain.Layout
	layoutID string
	loaded   bool
	status   string
	addOpen  bool   // add-tile menu visible
	focused  string // pane id highlighted by the MCP tile_focus tool ("" = none)

	// tile ⋯ overflow menu: which pane's menu is open + where to anchor it (viewport
	// coords). Rendered as a workspace-root popover so the tile's overflow:hidden and
	// backdrop-filter don't clip it.
	menuPane string
	menuX    int
	menuY    int

	wctx    app.Context   // captured in OnMount; drives cold-path re-renders from child tiles
	ctlStop chan struct{} // stops the MCP control long-poll loop

	// layout manager (toolbar popover: arrangements, balance, saved layouts)
	layoutOpen bool
	presetName string

	// appearance / background (the editing UI lives in the shared bgEditor component)
	accountBg *domain.Background
	apprOpen  bool
	apprScope string // "project" | "account" — seeds the bgEditor's initial scope

	saveTimer *time.Timer
}

// CompoID keys the workspace by project ID (go-app DismountEnforcer). Switching
// projects via the rail changes the ID, so go-app dismounts the old workspace and
// mounts a fresh one — re-running OnMount to load (restore) the new project's saved
// layout/sessions instead of reusing the previous project's stale state.
func (w *Workspace) CompoID() string { return "ws:" + w.Ref.ID }

func (w *Workspace) OnMount(ctx app.Context) {
	w.wctx = ctx // used by child tiles (e.g. Claude) to trigger cold-path label refreshes
	// Report divider-drag ratios from JS back into the layout tree.
	app.Window().Set("phWsRatio", app.FuncOf(func(_ app.Value, args []app.Value) any {
		if len(args) >= 2 {
			w.setRatio(args[0].String(), args[1].Float())
		}
		return nil
	}))
	// Hand the divider drag its snap table: the gesture lives in JS, the fractions
	// live in Go (SnapPoints) so the tile menu and the drag agree by construction.
	if sh := app.Window().Get("phShell"); sh.Truthy() {
		pts := make([]any, len(SnapPoints))
		for i, p := range SnapPoints {
			pts[i] = p
		}
		sh.Call("setSnapPoints", pts)
	}
	// Persist the browser tile's tab state (JS island owns the live chrome; this is
	// the cold-path callback for layout-restore + tile-label refresh). Args:
	// paneID, tabsJSON ([{url,title}]), activeIdx. Dispatch onto the render loop so
	// the mutated params re-render the tile label.
	app.Window().Set("phBrowserState", app.FuncOf(func(_ app.Value, args []app.Value) any {
		if len(args) >= 3 {
			paneID, tabsJSON, activeIdx := args[0].String(), args[1].String(), args[2].String()
			ctx.Dispatch(func(app.Context) { w.setBrowserState(paneID, tabsJSON, activeIdx) })
		}
		return nil
	}))
	// Persist the editor tile's currently-open file path (JS island owns the editor;
	// this records the path so the tile reopens the same file after relayout/restart
	// and the tile label shows the filename). Args: paneID, path.
	app.Window().Set("phEditorState", app.FuncOf(func(_ app.Value, args []app.Value) any {
		if len(args) >= 2 {
			paneID, path := args[0].String(), args[1].String()
			ctx.Dispatch(func(app.Context) {
				w.setParam(paneID, "path", path)
				w.persistSoon()
			})
		}
		return nil
	}))
	// Persist one instance-scoped param of a tile from its JS island. Used by the
	// terminal for pty_id (reattach to the still-running session after a renderer
	// reload, scrollback replayed by the sidecar), for session_id (the pinned Claude
	// conversation this tile owns, so a restart resumes it instead of opening a second,
	// empty one), and to clear a prompt once it has been sent. Args: paneID, key, value
	// — an empty value deletes the key.
	app.Window().Set("phTileParam", app.FuncOf(func(_ app.Value, args []app.Value) any {
		if len(args) >= 3 {
			paneID, key, val := args[0].String(), args[1].String(), args[2].String()
			ctx.Dispatch(func(app.Context) {
				w.setParam(paneID, key, val)
				w.persistSoon()
			})
		}
		return nil
	}))
	// Persist the editor tile's CodeMirror theme, scoped global (RootIndex) or per-
	// project (manifest + mirror), chosen in the editor's theme picker. Args: key, scope.
	app.Window().Set("phSetEditorTheme", app.FuncOf(func(_ app.Value, args []app.Value) any {
		if len(args) >= 2 {
			key, scope := args[0].String(), args[1].String()
			ctx.Async(func() {
				var err error
				if scope == "project" {
					err = w.Store.SetProjectEditorTheme(context.Background(), w.Ref.ID, key)
					ctx.Dispatch(func(app.Context) { w.Ref.EditorTheme = key })
				} else {
					err = w.Store.SetEditorTheme(context.Background(), key)
				}
				if err != nil {
					ctx.Dispatch(func(app.Context) { w.status = err.Error() })
				}
			})
		}
		return nil
	}))
	// Persist the browser tile's account-level default search engine (chosen in the
	// tile's engine picker) into the Passbubble-backed RootIndex, so it syncs devices.
	app.Window().Set("phSetSearchEngine", app.FuncOf(func(_ app.Value, args []app.Value) any {
		if len(args) >= 1 {
			key := args[0].String()
			ctx.Async(func() {
				if err := w.Store.SetSearchEngine(context.Background(), key); err != nil {
					ctx.Dispatch(func(app.Context) { w.status = err.Error() })
				}
			})
		}
		return nil
	}))
	if w.apprScope == "" {
		w.apprScope = "project"
	}
	w.startControlLoop(ctx) // MCP: let Claude Code drive this workspace
	// Let the app-wide Claude sidebar open Claude terminals in this workspace.
	if w.RegisterClaudeOpener != nil {
		w.RegisterClaudeOpener(func(ctx app.Context, cwd, prompt, sessionID string) {
			if cwd == "" {
				cwd = w.Ref.LocalPath
			}
			w.addTile(domain.TileTerminal, claudeTileParams(cwd, prompt, sessionID))
		})
	}
	ctx.Async(func() {
		item, err := w.Store.GetLayout(context.Background(), w.Ref.FolderID)
		accountBg, _ := w.Store.Background(context.Background())
		searchEngine, _ := w.Store.SearchEngine(context.Background())
		accountTheme, _ := w.Store.EditorTheme(context.Background())
		editorTheme := w.Ref.EditorTheme // project override wins
		if editorTheme == "" {
			editorTheme = accountTheme
		}
		accountUITheme, _ := w.Store.Theme(context.Background())
		uiTheme := w.Ref.Theme // project override wins
		if uiTheme == "" {
			uiTheme = accountUITheme
		}
		eff := w.Ref.Background
		if eff == nil {
			eff = accountBg
		}
		imgURL := resolveBgImageURL(w.Store, w.Native, eff)
		ctx.Dispatch(func(ctx app.Context) {
			if err == nil && item != nil {
				w.layout, w.layoutID = item.Val, item.ID
			}
			if w.layout.Root == nil {
				w.layout.Root = newLeaf(domain.TileTerminal, map[string]string{"cwd": w.Ref.LocalPath})
			}
			// Heal layouts saved before forkParams: a split used to copy pty_id, so two
			// panes could restore onto the same PTY session.
			dedupeInstanceParams(w.layout.Root)
			w.accountBg = accountBg
			w.loaded = true
			applyBackground(eff, imgURL)
			setDocTheme(uiTheme) // resolved UI theme (project override ⇒ else account)
			// Push the account default engine into the shell so all browser tiles
			// (and their pickers) use it; empty ⇒ shell keeps its own default.
			if searchEngine != "" {
				if shell := app.Window().Get("phShell"); shell.Truthy() {
					shell.Call("applySearchEngine", searchEngine)
				}
			}
			// Push the resolved editor theme (project override ⇒ else account) so all
			// editor tiles adopt it; empty ⇒ shell keeps its built-in default.
			if editorTheme != "" {
				if shell := app.Window().Get("phShell"); shell.Truthy() {
					shell.Call("applyEditorTheme", editorTheme)
				}
			}
		})
	})
}

// OnDismount tears down every live island so PTYs/webviews don't outlive the project.
func (w *Workspace) OnDismount() {
	// Flush any debounced layout save so switching projects never loses the last
	// change (added/closed/resized tile) made within the save window.
	if w.saveTimer != nil {
		w.saveTimer.Stop()
		w.saveTimer = nil
		w.persist()
	}
	w.stopControlLoop()
	shell := app.Window().Get("phShell")
	if !shell.Truthy() {
		return
	}
	for _, leaf := range leaves(w.layout.Root) {
		shell.Call("destroyIsland", leaf.PaneID)
	}
}

func (w *Workspace) Render() app.UI {
	if !w.loaded {
		return app.Div().Class("ph-workspace").Body(
			app.Div().Class("ph-ws-toolbar").Body(app.Span().Class("ph-muted").Text("Lade Workspace…")),
		)
	}
	return app.Div().Class("ph-workspace").Style("--accent", w.Ref.AccentColor()).Body(
		app.Div().Class("ph-ws-wallpaper"),
		w.toolbar(),
		app.Div().Class("ph-ws-body").Body(w.renderNode(w.layout.Root)),
		app.If(w.apprOpen, func() app.UI {
			return app.Div().Body(
				// outside-click catcher: closes the panel when clicking anywhere else
				app.Div().Class("ph-backdrop").OnClick(func(ctx app.Context, _ app.Event) { w.apprOpen = false }),
				w.appearancePanel(),
			)
		}),
		app.If(w.layoutOpen, func() app.UI {
			return app.Div().Body(
				app.Div().Class("ph-backdrop").OnClick(func(ctx app.Context, _ app.Event) { w.layoutOpen = false }),
				w.layoutPanel(),
			)
		}),
		app.If(w.menuPane != "", w.tileMenuPopover),
	)
}

// tileMenuPopover draws the open tile's ⋯ overflow menu at the workspace root, anchored
// at the click coords, so it escapes the tile's overflow:hidden/backdrop-filter clip.
func (w *Workspace) tileMenuPopover() app.UI {
	leaf := findLeaf(w.layout.Root, w.menuPane)
	if leaf == nil {
		w.menuPane = ""
		return app.Div()
	}
	items := w.tileMenu(leaf)
	return app.Div().Body(
		app.Div().Class("ph-backdrop").OnClick(func(ctx app.Context, _ app.Event) { w.menuPane = "" }),
		app.Div().Class("ph-menu ph-tile-menu").
			Style("top", strconv.Itoa(w.menuY)+"px").
			Style("left", strconv.Itoa(w.menuX)+"px").
			Body(
				app.Range(items).Slice(func(i int) app.UI {
					a := items[i]
					if a.Custom != nil {
						return a.Custom
					}
					cls := "ph-menu-item"
					if a.Danger {
						cls += " ph-menu-item-danger"
					}
					return app.Button().Class(cls).OnClick(func(ctx app.Context, e app.Event) {
						w.menuPane = ""
						if a.OnClick != nil {
							a.OnClick(ctx, e)
						}
					}).Body(
						app.If(a.SVG != "" || a.Icon != "", func() app.UI {
							return app.Span().Class("ph-menu-icon").Body(a.glyph(15))
						}),
						app.Span().Text(a.Label),
					)
				}),
			),
	)
}

// ─── toolbar ────────────────────────────────────────────────────────────────

func (w *Workspace) toolbar() app.UI {
	return app.Div().Class("ph-ws-toolbar").Body(
		app.Button().Class("ph-tile-btn").Text("← Projekte").OnClick(func(ctx app.Context, _ app.Event) {
			if w.Back != nil {
				w.Back(ctx)
			}
		}),
		nexusIcon(w.Ref.AccentColor(), 20),
		app.Span().Class("ph-tile-title").Text(w.Ref.Title),
		app.Div().Class("ph-spacer"),
		app.If(w.status != "", func() app.UI { return app.Span().Class("ph-muted").Text(w.status) }),
		&accentPicker{Current: w.Ref.AccentColor(), OnPick: w.pickColor, OnCustom: w.customColor},
		app.Button().Class("ph-tile-btn ph-tile-btn-lg").Title("Layout").
			OnClick(func(ctx app.Context, _ app.Event) {
				w.layoutOpen = !w.layoutOpen
				w.apprOpen = false
			}).
			Body(icon("layout", 17)),
		app.Button().Class("ph-tile-btn ph-tile-btn-lg").Title("Aussehen / Hintergrund").
			OnClick(func(ctx app.Context, _ app.Event) {
				w.apprOpen = !w.apprOpen
				w.layoutOpen = false
			}).
			Body(icon("sliders", 17)),
		app.Div().Class("ph-add").Body(
			app.Button().Class("ph-btn").Text("+ Tile").OnClick(func(ctx app.Context, _ app.Event) {
				w.addOpen = !w.addOpen
			}),
			app.If(w.addOpen, func() app.UI {
				return app.Div().Body(
					// Outside-click catcher: closes the menu when clicking anywhere else.
					app.Div().Class("ph-backdrop").OnClick(func(ctx app.Context, _ app.Event) {
						w.addOpen = false
					}),
					w.addMenu(),
				)
			}),
		),
	)
}

func (w *Workspace) addMenu() app.UI {
	opt := func(label string, t domain.TileType, params map[string]string) app.UI {
		return app.Button().Class("ph-menu-item").Text(label).OnClick(func(ctx app.Context, _ app.Event) {
			w.addOpen = false
			w.addTile(t, params)
		})
	}
	cwd := w.Ref.LocalPath
	return app.Div().Class("ph-menu").Body(
		opt("Terminal", domain.TileTerminal, map[string]string{"cwd": cwd}),
		opt("Terminal (Claude)", domain.TileTerminal, map[string]string{"cwd": cwd, "cmd": "claude"}),
		opt("Markdown-Preview", domain.TileMarkdown, map[string]string{"path": ""}),
		opt("Code-Editor", domain.TileEditor, map[string]string{"path": ""}),
		opt("Dateien (lokal)", domain.TileFileTree, map[string]string{"path": cwd}),
		opt("Browser", domain.TileBrowser, map[string]string{"url": "about:blank"}),
		opt("Notizen", domain.TileNotes, nil),
		opt("Todo", domain.TileTodo, nil),
		opt("Dateien", domain.TileFiles, nil),
		opt("Claude-Sessions", domain.TileSessions, nil),
		opt("Browser-Tabs", domain.TileTabs, nil),
		opt("Claude", domain.TileClaude, nil),
		opt("Pipepush", domain.TilePipepush, nil),
		opt("Passbubble", domain.TilePassbubble, nil),
		opt("Redmine", domain.TileRedmine, nil),
	)
}

// ─── split-tree rendering ─────────────────────────────────────────────────────

// renderNode wraps every layout node in a nodeView keyed by its PaneID. go-app's
// HTML reconciler only compares tag names, so without this it would morph a
// collapsing split's <div class=ph-split> in place into a <div class=ph-tile> and
// positionally recycle its children — landing the wrong island in the wrong slot
// ("closing the wrong tile"). Keying by PaneID (DismountEnforcer.CompoID) makes
// go-app dismount+mount cleanly whenever a position's node identity changes.
func (w *Workspace) renderNode(n *domain.LayoutNode) app.UI {
	return &nodeView{w: w, Node: n, Rev: nextRev()}
}

// nodeView is the keyed wrapper for one split/leaf position in the layout tree.
// Node/Rev are exported so go-app actually reconciles it — see compo.go. Without
// Rev the tree froze at its first render: closing a tile left the focus ring on the
// gone pane, and a tile label that depends on live params (browser page title,
// Claude chat title) never caught up.
type nodeView struct {
	app.Compo
	Node *domain.LayoutNode
	Rev  int
	w    *Workspace
}

// CompoID keys reconciliation by the node's PaneID (empty slot → stable sentinel).
func (v *nodeView) CompoID() string {
	if v.Node == nil {
		return "ph-empty"
	}
	return v.Node.PaneID
}

func (v *nodeView) Render() app.UI {
	n := v.Node
	if n == nil {
		return app.Div().Class("ph-empty").Text("Leerer Workspace — oben ein Tile hinzufügen.")
	}
	if n.IsLeaf() {
		return v.w.renderTile(n)
	}
	ratio := n.Ratio
	if ratio == 0 {
		ratio = 0.5
	}
	return app.Div().Class("ph-split").Attr("data-dir", n.Dir).Style("--r", fmt.Sprintf("%.4f", ratio)).Body(
		app.Div().Class("ph-split-a").Body(v.w.renderNode(n.A)),
		app.Div().Class("ph-divider").Attr("data-node", n.PaneID),
		app.Div().Class("ph-split-b").Body(v.w.renderNode(n.B)),
	)
}

func (w *Workspace) renderTile(n *domain.LayoutNode) app.UI {
	paneID := n.PaneID
	cls := "ph-tile"
	if paneID == w.focused {
		cls += " ph-tile-focus"
	}
	// The whole tile is the DROP target, but only the titlebar is draggable — else a
	// drag started inside a terminal/webview and the tile could vanish.
	return app.Div().Class(cls).Attr("data-pane", paneID).
		// Clicking anywhere in a tile selects it AND hands it the keyboard, so the
		// focus ring and where your typing lands can never disagree. mousedown (not
		// click) so it wins before the browser settles focus on its own.
		OnMouseDown(func(ctx app.Context, _ app.Event) { w.focusTile(paneID) }).
		OnDragOver(func(ctx app.Context, e app.Event) { e.PreventDefault() }).
		OnDrop(func(ctx app.Context, e app.Event) {
			e.PreventDefault()
			dt := e.Get("dataTransfer")
			// A file dragged from a file tree onto this tile → open it here.
			if p := dt.Call("getData", "application/x-ph-path").String(); p != "" {
				w.dropPathInTile(ctx, paneID, p)
				return
			}
			src := dt.Call("getData", "text/plain").String()
			if src != "" && src != paneID {
				w.dropTile(src, paneID, dropEdge(ctx.JSSrc(), e))
			}
		}).
		Body(
			w.tileBar(n),
			app.Div().Class("ph-tile-body").Body(w.renderTileBody(n)),
		)
}

func (w *Workspace) tileBar(n *domain.LayoutNode) app.UI {
	paneID := n.PaneID
	actions := w.tileActions(n)
	return app.Div().Class("ph-tile-bar").
		Attr("draggable", true).
		OnDragStart(func(ctx app.Context, e app.Event) {
			dt := e.Get("dataTransfer")
			dt.Call("setData", "text/plain", paneID)
			// Typed marker: dataTransfer contents are unreadable during dragover, but
			// the type list is not — that is how the shell's drop hint tells a tile
			// rearrange apart from a todo reorder or a file drag.
			dt.Call("setData", "application/x-ph-tile", paneID)
		}).
		Body(
			app.Span().Class("ph-tile-dot"),
			app.Span().Class("ph-tile-title").Text(tileLabel(n)),
			// tile-contributed action buttons (declared, never custom chrome)
			app.Range(actions).Slice(func(i int) app.UI {
				a := actions[i]
				return app.Button().Class("ph-tile-btn").Title(a.Label).
					OnClick(a.OnClick).Body(a.glyph(15))
			}),
			// ⋯ overflow: split + any tile menu actions, drawn at the workspace root
			app.Button().Class("ph-tile-btn").Title("mehr").Text("⋯").OnClick(func(ctx app.Context, e app.Event) {
				w.menuPane = paneID
				w.menuX = e.Get("clientX").Int()
				w.menuY = e.Get("clientY").Int()
			}),
			app.Button().Class("ph-tile-btn").Title("schließen").Text("✕").OnClick(func(ctx app.Context, _ app.Event) {
				w.closeTile(paneID)
			}),
		)
}

// tileActions are a tile's primary action buttons, shown inline in its bar. Only
// declared actions are allowed — tiles never render their own chrome.
func (w *Workspace) tileActions(n *domain.LayoutNode) []TileAction {
	switch n.Type {
	case domain.TileEditor:
		paneID := n.PaneID
		return []TileAction{
			{SVG: "save", Label: "Speichern (⌘S)", OnClick: func(ctx app.Context, _ app.Event) { w.callEditor("phEditorSave", paneID) }},
			{SVG: "external", Label: "In VS Code öffnen", OnClick: func(ctx app.Context, _ app.Event) { w.callEditor("phEditorOpenInCode", paneID) }},
		}
	}
	return nil
}

// tileMenu is the ⋯ overflow content: the universal split actions, the size
// fractions (when the tile sits in a split), plus any type-specific secondary actions.
func (w *Workspace) tileMenu(n *domain.LayoutNode) []TileAction {
	paneID := n.PaneID
	items := []TileAction{
		{Icon: "⇆", Label: "Horizontal teilen", OnClick: func(ctx app.Context, _ app.Event) { w.splitTile(paneID, "row") }},
		{Icon: "⇅", Label: "Vertikal teilen", OnClick: func(ctx app.Context, _ app.Event) { w.splitTile(paneID, "col") }},
	}
	if row := w.sizeRow(paneID); row != nil {
		items = append(items, TileAction{Custom: row})
	}
	return items
}

// sizeRow is the fraction picker for one tile: how much of its split it takes. nil
// when the tile is the whole workspace — there is no neighbour to give space to.
func (w *Workspace) sizeRow(paneID string) app.UI {
	sp, side := findSplitOfLeaf(w.layout.Root, paneID)
	if sp == nil {
		return nil
	}
	label := "Breite"
	if sp.Dir == "col" {
		label = "Höhe"
	}
	current := sp.Ratio
	if current == 0 {
		current = 0.5
	}
	if side == "b" {
		current = 1 - current
	}
	nodeID := sp.PaneID
	return app.Div().Class("ph-menu-row").Body(
		app.Span().Class("ph-menu-row-label").Text(label),
		app.Div().Class("ph-frac").Body(
			app.Range(SnapPoints).Slice(func(i int) app.UI {
				frac := SnapPoints[i]
				cls := "ph-frac-btn"
				if nearRatio(current, frac) {
					cls += " is-current"
				}
				return app.Button().Class(cls).Title(snapLabels[i]).
					OnClick(func(ctx app.Context, _ app.Event) {
						w.menuPane = ""
						w.applyRatio(nodeID, frac, side)
					}).
					Body(fracGlyph(frac, sp.Dir), app.Span().Class("ph-frac-label").Text(snapLabels[i]))
			}),
		),
	)
}

// nearRatio reports whether two fractions are the same for display purposes (⅓ is
// stored as 0.3333…, and a drag lands a hair off).
func nearRatio(a, b float64) bool { return a-b < 0.005 && b-a < 0.005 }

// applyRatio sets a split's ratio so that the tile on the given side gets frac. It
// writes the CSS variable directly first because go-app does not reliably re-apply an
// inline custom property on re-render (same reason as applyColor).
func (w *Workspace) applyRatio(nodeID string, frac float64, side string) {
	r := frac
	if side == "b" {
		r = 1 - frac
	}
	setSplitRatioJS(nodeID, r)
	w.setRatio(nodeID, r)
}

// setSplitRatioJS moves a divider in the DOM without a re-render.
func setSplitRatioJS(nodeID string, r float64) {
	if sh := app.Window().Get("phShell"); sh.Truthy() {
		sh.Call("setSplitRatio", nodeID, r)
	}
}

// callEditor invokes a per-pane editor bridge (phEditorSave/phEditorOpenInCode)
// registered by the JS editor island for paneID; a no-op if the bridge is absent.
func (w *Workspace) callEditor(fn, paneID string) {
	if f := app.Window().Get(fn); f.Truthy() {
		f.Invoke(paneID)
	}
}

func (w *Workspace) renderTileBody(n *domain.LayoutNode) app.UI {
	switch n.Type {
	case domain.TileTerminal, domain.TileBrowser, domain.TileMarkdown, domain.TileEditor:
		return w.islandTile(n)
	case domain.TileNotes:
		return &notesTile{Store: w.Store, FolderID: w.Ref.FolderID}
	case domain.TileTodo:
		return &todoTile{Store: w.Store, FolderID: w.Ref.FolderID}
	case domain.TileFiles:
		return &filesTile{Store: w.Store, Native: w.Native, FolderID: w.Ref.FolderID,
			LocalRoot: w.Ref.LocalPath, PaneID: n.PaneID, OpenEditor: w.openEditorFor}
	case domain.TileFileTree:
		root := n.Params["path"]
		if root == "" {
			root = w.Ref.LocalPath
		}
		return &fileTreeTile{Native: w.Native, Store: w.Store, FolderID: w.Ref.FolderID,
			PaneID: n.PaneID, Root: root, OpenEditor: w.openEditorFor, OpenTile: w.openTileFor}
	case domain.TileSessions:
		return &sessionsTile{Store: w.Store, Native: w.Native, FolderID: w.Ref.FolderID, Cwd: w.Ref.LocalPath,
			OpenTerminal: func(ctx app.Context, cwd, sessionID string) {
				w.addTile(domain.TileTerminal, claudeTileParams(cwd, "", sessionID))
			}}
	case domain.TileTabs:
		return &tabsTile{Native: w.Native, ProjectID: w.Ref.ID}
	case domain.TileClaude:
		pane := n.PaneID
		return &claudeTile{Native: w.Native, Cwd: w.Ref.LocalPath,
			OpenClaude: func(ctx app.Context, cwd, prompt, sessionID string) {
				w.addTile(domain.TileTerminal, claudeTileParams(cwd, prompt, sessionID))
			},
			OnActiveChat: func(title string) {
				// Cold-path: dispatch on the workspace ctx (captured in OnMount, always
				// set by the time this tile has rendered) so the parent tile toolbar
				// (tileLabel) re-renders, not just the child claudeTile.
				w.wctx.Dispatch(func(app.Context) {
					w.setParam(pane, "chat_title", title)
					w.persistSoon()
				})
			}}
	case domain.TilePipepush:
		return &pipepushTile{Store: w.Store, Native: w.Native, FolderID: w.Ref.FolderID}
	case domain.TilePassbubble:
		return &passbubbleTile{Store: w.Store, FolderID: w.Ref.FolderID}
	case domain.TileRedmine:
		return &redmineTile{Store: w.Store, Native: w.Native, FolderID: w.Ref.FolderID}
	default:
		return app.Div().Class("ph-muted").Text("Unbekannter Tile-Typ")
	}
}

// islandTile renders the empty slot + (for browser/markdown) an address/path bar;
// the JS island layer attaches the live element into #ph-slot-<paneID>.
func (w *Workspace) islandTile(n *domain.LayoutNode) app.UI {
	var bar app.UI = app.Div()
	switch n.Type {
	case domain.TileMarkdown:
		bar = w.pathBar(n)
	}
	return app.Div().Class("ph-island-wrap").Body(
		bar,
		&tileIsland{PaneID: n.PaneID, Type: n.Type, Params: cloneParams(n.Params)},
	)
}

// focusTile makes paneID the focused tile: it moves the focus ring and puts the
// keyboard into that tile's island (terminal/editor), which is the part the ring
// alone never did. The re-render is dispatched on the workspace context so the
// PREVIOUSLY focused tile drops its ring too — an event handler on its own only
// refreshes the tile that was clicked.
func (w *Workspace) focusTile(paneID string) {
	focusIsland(paneID)
	if w.focused == paneID {
		return
	}
	w.wctx.Dispatch(func(app.Context) { w.focused = paneID })
}

// focusIsland asks the JS island layer to put the keyboard into paneID's island;
// a no-op for tiles go-app renders itself (they take focus natively).
func focusIsland(paneID string) {
	if shell := app.Window().Get("phShell"); shell.Truthy() {
		shell.Call("focusIsland", paneID)
	}
}

// claudeTileParams builds the params of a terminal tile that runs Claude Code.
// sessionID continues an existing conversation ("" lets the island mint and pin a new
// one on start, so the tile keeps talking to the same session across restarts);
// prompt is a one-shot start argument the island clears once it has been sent.
func claudeTileParams(cwd, prompt, sessionID string) map[string]string {
	p := map[string]string{"cwd": cwd, "cmd": "claude"}
	if prompt != "" {
		p["prompt"] = prompt
	}
	if sessionID != "" {
		p["session_id"] = sessionID
	}
	return p
}

// setParam updates one leaf param in place and returns the leaf (nil if gone). An
// empty value removes the key instead of storing a blank one, so a spent prompt or a
// dropped pty id leaves no trace in the persisted layout.
func (w *Workspace) setParam(paneID, key, val string) *domain.LayoutNode {
	leaf := findLeaf(w.layout.Root, paneID)
	if leaf == nil {
		return nil
	}
	if val == "" {
		delete(leaf.Params, key)
		return leaf
	}
	if leaf.Params == nil {
		leaf.Params = map[string]string{}
	}
	leaf.Params[key] = val
	return leaf
}

// browserTab mirrors one entry of the JS island's tab state (Params["tabs"] JSON).
type browserTab struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// setBrowserState records the browser tile's tab state persisted by the JS island.
// It keeps Params["tabs"]/["active"] verbatim and derives Params["url"] (active URL,
// back-compat + used by tileLabel) so the layout store and the tab label stay in sync.
func (w *Workspace) setBrowserState(paneID, tabsJSON, activeIdx string) {
	leaf := w.setParam(paneID, "tabs", tabsJSON)
	if leaf == nil {
		return
	}
	w.setParam(paneID, "active", activeIdx)
	if tab := activeBrowserTab(tabsJSON, activeIdx); tab != nil {
		w.setParam(paneID, "url", tab.URL)
	}
	w.persistSoon()
}

// activeBrowserTab decodes tabsJSON and returns the tab at activeIdx, or nil.
func activeBrowserTab(tabsJSON, activeIdx string) *browserTab {
	var tabs []browserTab
	if json.Unmarshal([]byte(tabsJSON), &tabs) != nil || len(tabs) == 0 {
		return nil
	}
	i, err := strconv.Atoi(activeIdx)
	if err != nil || i < 0 || i >= len(tabs) {
		i = 0
	}
	return &tabs[i]
}

// pathBar is the markdown tile's file-path bar: entering a path rebuilds the island
// so it watches the new file.
func (w *Workspace) pathBar(n *domain.LayoutNode) app.UI {
	paneID := n.PaneID
	load := func(path string) {
		w.setParam(paneID, "path", path)
		if shell := app.Window().Get("phShell"); shell.Truthy() {
			shell.Call("destroyIsland", paneID) // rebuild → re-reads/watches the new file
		}
		w.persistSoon()
	}
	return app.Div().Class("ph-island-bar").Body(
		app.Input().Class("ph-island-input").Type("text").Placeholder("/pfad/zur/datei.md").Value(n.Params["path"]).
			OnChange(func(ctx app.Context, e app.Event) { load(ctx.JSSrc().Get("value").String()) }),
	)
}

// ─── mutations ────────────────────────────────────────────────────────────────

// parkIslands moves every live island element into a hidden off-tree holding pen
// before a structural re-render. go-app keys tree nodes by PaneID (see nodeView) and
// dismounts a collapsing subtree via replaceChild BEFORE OnDismount fires — which
// would tear a still-embedded <webview>/terminal out of the DOM and destroy its
// guest. Parking first (an atomic appendChild move, not a remove) preserves the
// guests; each new slot then re-homes its island via attachIsland on mount.
func (w *Workspace) parkIslands() {
	if shell := app.Window().Get("phShell"); shell.Truthy() {
		shell.Call("parkIslands")
	}
}

// addTile appends a new tile (splitting the workspace to the right) and returns its
// pane id.
func (w *Workspace) addTile(t domain.TileType, params map[string]string) string {
	w.parkIslands()
	leaf := newLeaf(t, params)
	if w.layout.Root == nil {
		w.layout.Root = leaf
	} else {
		// Split the whole workspace, new tile on the right.
		w.layout.Root = &domain.LayoutNode{Dir: "row", Ratio: 0.5, PaneID: uuid.NewString(), A: w.layout.Root, B: leaf}
	}
	w.persistSoon()
	return leaf.PaneID
}

func (w *Workspace) splitTile(paneID, dir string) {
	parent, side := findParentOf(&w.layout.Root, paneID)
	if parent == nil {
		return
	}
	w.parkIslands()
	old := *parent
	*parent = &domain.LayoutNode{
		Dir: dir, Ratio: 0.5, PaneID: uuid.NewString(),
		A: old,
		B: newLeaf(old.Type, forkParams(old.Params)),
	}
	_ = side
	w.persistSoon()
}

// splitWith splits the given pane (new tile on side B) with an explicit type+params,
// returns the new leaf's pane id. Unlike splitTile it does not clone the source type.
func (w *Workspace) splitWith(paneID, dir string, t domain.TileType, params map[string]string) string {
	parent, _ := findParentOf(&w.layout.Root, paneID)
	leaf := newLeaf(t, params)
	w.parkIslands()
	if parent == nil {
		// paneID is the whole root leaf → wrap it in a split.
		if w.layout.Root != nil {
			w.layout.Root = &domain.LayoutNode{Dir: dir, Ratio: 0.5, PaneID: uuid.NewString(), A: w.layout.Root, B: leaf}
		} else {
			w.layout.Root = leaf
		}
		w.persistSoon()
		return leaf.PaneID
	}
	old := *parent
	*parent = &domain.LayoutNode{Dir: dir, Ratio: 0.5, PaneID: uuid.NewString(), A: old, B: leaf}
	w.persistSoon()
	return leaf.PaneID
}

// openTileFor opens params in a tile of type t: it reuses an existing tile of that
// type (re-homing it onto the new params) or, if none exists, splits a fresh one in
// next to the source tile. Focuses it either way. For island tiles (editor/markdown/
// browser) the island is rebuilt (destroyIsland) so it reloads with the new params.
func (w *Workspace) openTileFor(ctx app.Context, sourcePaneID string, t domain.TileType, params map[string]string) {
	for _, leaf := range leaves(w.layout.Root) {
		if leaf.Type != t {
			continue
		}
		for k, v := range params {
			w.setParam(leaf.PaneID, k, v)
		}
		if shell := app.Window().Get("phShell"); shell.Truthy() {
			shell.Call("destroyIsland", leaf.PaneID) // rebuild → reloads with new params
		}
		w.focused = leaf.PaneID
		w.persistSoon()
		return
	}
	w.focused = w.splitWith(sourcePaneID, "row", t, params)
}

// openEditorFor opens a file path in an editor tile (reuse-or-split next to source).
func (w *Workspace) openEditorFor(ctx app.Context, sourcePaneID, path string) {
	w.openTileFor(ctx, sourcePaneID, domain.TileEditor, map[string]string{"path": path})
}

// dropPathInTile opens a file (dragged from a file tree) inside the tile it was
// dropped on, by that tile's kind: editor/markdown load the path; browser navigates
// to file://; a filetree drop moves the file into that tree's root. The island tiles
// are rebuilt so they reload with the new params.
func (w *Workspace) dropPathInTile(ctx app.Context, targetPaneID, srcPath string) {
	leaf := findLeaf(w.layout.Root, targetPaneID)
	if leaf == nil || srcPath == "" {
		return
	}
	rebuild := func() {
		if shell := app.Window().Get("phShell"); shell.Truthy() {
			shell.Call("destroyIsland", targetPaneID)
		}
		w.focused = targetPaneID
		w.persistSoon()
	}
	switch leaf.Type {
	case domain.TileEditor, domain.TileMarkdown:
		w.setParam(targetPaneID, "path", srcPath)
		rebuild()
	case domain.TileBrowser:
		// Open the file fresh: set the initial URL and drop any saved tab state.
		w.setParam(targetPaneID, "url", "file://"+srcPath)
		w.setParam(targetPaneID, "tabs", "")
		w.setParam(targetPaneID, "active", "")
		rebuild()
	case domain.TileFileTree:
		if w.Native != nil {
			dst := filepath.Join(leaf.Params["path"], filepath.Base(srcPath))
			if dst != srcPath {
				ctx.Async(func() {
					if err := w.Native.Move(context.Background(), srcPath, dst); err != nil {
						ctx.Dispatch(func(app.Context) { w.status = "Verschieben: " + err.Error() })
					}
				})
			}
		}
	}
}

func (w *Workspace) closeTile(paneID string) {
	// Park the survivors (so a collapsing split doesn't destroy their guests), then
	// destroy only the closed tile's island.
	w.parkIslands()
	if shell := app.Window().Get("phShell"); shell.Truthy() {
		shell.Call("destroyIsland", paneID)
	}
	if w.layout.Root != nil && w.layout.Root.IsLeaf() && w.layout.Root.PaneID == paneID {
		w.layout.Root = nil
		w.persistSoon()
		return
	}
	// Find the split whose child is the leaf; replace that split with the sibling.
	removeLeaf(&w.layout.Root, paneID)
	w.persistSoon()
}

// dropEdge classifies where on the target tile a drop landed: "left"/"right"/"top"/
// "bottom" near an edge, else "center". Drives edge-split vs. swap.
func dropEdge(el app.Value, e app.Event) string {
	rect := el.Call("getBoundingClientRect")
	width, height := rect.Get("width").Float(), rect.Get("height").Float()
	if width == 0 || height == 0 {
		return "center"
	}
	fx := (e.Get("clientX").Float() - rect.Get("left").Float()) / width
	fy := (e.Get("clientY").Float() - rect.Get("top").Float()) / height
	switch {
	case fx < 0.25:
		return "left"
	case fx > 0.75:
		return "right"
	case fy < 0.25:
		return "top"
	case fy > 0.75:
		return "bottom"
	default:
		return "center"
	}
}

// dropTile moves the src tile relative to the target: onto an edge splits the target
// (Warp-style), onto the center swaps the two. Islands survive because they follow
// their paneID through the tree change.
func (w *Workspace) dropTile(src, target, edge string) {
	w.parkIslands()
	if edge == "center" {
		w.swapTiles(src, target)
		return
	}
	if moveTileInTree(&w.layout.Root, src, target, edge) {
		w.persistSoon()
	}
}

// moveTileInTree is the pure edge-split mutation: detach the src leaf and re-insert
// it as a new split beside the target on the given edge. Returns false if src can't
// be moved (e.g. it is the sole root). Kept Store-free so it is unit-testable.
func moveTileInTree(root **domain.LayoutNode, src, target, edge string) bool {
	srcLeaf := findLeaf(*root, src)
	if srcLeaf == nil {
		return false
	}
	moved := &domain.LayoutNode{PaneID: srcLeaf.PaneID, Type: srcLeaf.Type, Params: srcLeaf.Params}
	if !removeLeaf(root, src) {
		return false // src was the sole root — nothing to move onto
	}
	slot, _ := findParentOf(root, target)
	if slot == nil {
		return false
	}
	tgt := *slot
	dir := "row"
	if edge == "top" || edge == "bottom" {
		dir = "col"
	}
	split := &domain.LayoutNode{Dir: dir, Ratio: 0.5, PaneID: uuid.NewString()}
	if edge == "left" || edge == "top" {
		split.A, split.B = moved, tgt
	} else {
		split.A, split.B = tgt, moved
	}
	*slot = split
	return true
}

func (w *Workspace) swapTiles(a, b string) {
	la, lb := findLeaf(w.layout.Root, a), findLeaf(w.layout.Root, b)
	if la == nil || lb == nil {
		return
	}
	la.Type, lb.Type = lb.Type, la.Type
	la.Params, lb.Params = lb.Params, la.Params
	la.PaneID, lb.PaneID = lb.PaneID, la.PaneID // keep islands with their content
	w.persistSoon()
}

func (w *Workspace) setRatio(nodeID string, r float64) {
	if n := findSplit(w.layout.Root, nodeID); n != nil {
		n.Ratio = r
		w.persistSoon() // no re-render: JS already moved the divider visually
	}
}

// ─── per-project accent (reuses the swatchBar from nexus.go) ──────────────────

func (w *Workspace) pickColor(color string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) { w.applyColor(ctx, color) }
}
func (w *Workspace) customColor(ctx app.Context, _ app.Event) {
	w.applyColor(ctx, ctx.JSSrc().Get("value").String())
}
func (w *Workspace) applyColor(ctx app.Context, color string) {
	if color == "" {
		return
	}
	w.Ref.Color = color
	// go-app does NOT reliably re-apply an inline CSS custom property (--accent) on
	// re-render, so the swatch click would have no visible effect. Set it on the
	// workspace element directly, mirroring how the background vars are applied.
	doc := app.Window().Get("document")
	if ws := doc.Call("querySelector", ".ph-workspace"); ws.Truthy() {
		ws.Get("style").Call("setProperty", "--accent", color)
	}
	// Live-update this project's rail dot too (it lives in the Root component, which
	// this child event won't re-render) so the sidebar reflects the new colour at once.
	if dot := doc.Call("querySelector", ".ph-rail-dot-active"); dot.Truthy() {
		dot.Get("style").Call("setProperty", "--dot", color)
	}
	// Keep Root's project list in sync so a later Root render / project switch shows
	// the new colour (best-effort; nil in tests).
	if w.OnColor != nil {
		w.OnColor(color)
	}
	ctx.Async(func() {
		if err := w.Store.SetProjectColor(context.Background(), w.Ref.ID, color); err != nil {
			ctx.Dispatch(func(ctx app.Context) { w.status = err.Error() })
		}
	})
}

// ─── persistence (debounced) ──────────────────────────────────────────────────

func (w *Workspace) persistSoon() {
	if w.saveTimer != nil {
		w.saveTimer.Stop()
	}
	w.saveTimer = time.AfterFunc(400*time.Millisecond, w.persist)
}

func (w *Workspace) persist() {
	layout := w.layout
	go func() {
		if id, err := w.Store.SetLayout(context.Background(), w.Ref.FolderID, layout); err == nil {
			w.layoutID = id
		}
	}()
}

// ─── tree helpers ─────────────────────────────────────────────────────────────

func newLeaf(t domain.TileType, params map[string]string) *domain.LayoutNode {
	return &domain.LayoutNode{PaneID: uuid.NewString(), Type: t, Params: params}
}

func leaves(n *domain.LayoutNode) []*domain.LayoutNode {
	if n == nil {
		return nil
	}
	if n.IsLeaf() {
		return []*domain.LayoutNode{n}
	}
	return append(leaves(n.A), leaves(n.B)...)
}

func findLeaf(n *domain.LayoutNode, paneID string) *domain.LayoutNode {
	if n == nil {
		return nil
	}
	if n.IsLeaf() {
		if n.PaneID == paneID {
			return n
		}
		return nil
	}
	if l := findLeaf(n.A, paneID); l != nil {
		return l
	}
	return findLeaf(n.B, paneID)
}

func findSplit(n *domain.LayoutNode, nodeID string) *domain.LayoutNode {
	if n == nil || n.IsLeaf() {
		return nil
	}
	if n.PaneID == nodeID {
		return n
	}
	if s := findSplit(n.A, nodeID); s != nil {
		return s
	}
	return findSplit(n.B, nodeID)
}

// findParentOf returns the address of the pointer to the leaf with paneID (so the
// caller can replace it in place) and a side hint.
func findParentOf(slot **domain.LayoutNode, paneID string) (**domain.LayoutNode, string) {
	n := *slot
	if n == nil {
		return nil, ""
	}
	if n.IsLeaf() {
		if n.PaneID == paneID {
			return slot, ""
		}
		return nil, ""
	}
	if p, s := findParentOf(&n.A, paneID); p != nil {
		return p, s
	}
	return findParentOf(&n.B, paneID)
}

// removeLeaf replaces the split containing the leaf with the leaf's sibling.
func removeLeaf(slot **domain.LayoutNode, paneID string) bool {
	n := *slot
	if n == nil || n.IsLeaf() {
		return false
	}
	if n.A.IsLeaf() && n.A.PaneID == paneID {
		*slot = n.B
		return true
	}
	if n.B.IsLeaf() && n.B.PaneID == paneID {
		*slot = n.A
		return true
	}
	return removeLeaf(&n.A, paneID) || removeLeaf(&n.B, paneID)
}

// instanceParams are params bound to ONE live tile instance rather than to the kind
// of tile it is. They must never be copied into a second pane: pty_id addresses a
// running PTY, and the sidecar allows a single subscriber per session — so two panes
// sharing one id means the later one takes the session over and the earlier goes
// deaf, i.e. you type in one terminal and it lands in the other. session_id would
// likewise start a second `claude --resume` against the same transcript, and prompt
// is a one-shot start argument that must not be replayed.
var instanceParams = []string{"pty_id", "session_id", "prompt"}

// forkParams clones params for a NEW pane derived from an existing one (tile split),
// dropping everything instance-scoped. Use cloneParams only when the params stay with
// the same pane.
func forkParams(p map[string]string) map[string]string {
	out := cloneParams(p)
	for _, k := range instanceParams {
		delete(out, k)
	}
	return out
}

// dedupeInstanceParams strips instance-scoped params from every pane that shares them
// with an earlier pane, keeping the first occurrence. Layouts persisted before
// forkParams existed can hold a duplicated pty_id, which would have both panes
// reattach to the same PTY on restore.
func dedupeInstanceParams(root *domain.LayoutNode) {
	seen := map[string]bool{}
	for _, leaf := range leaves(root) {
		for _, k := range instanceParams {
			v := leaf.Params[k]
			if v == "" {
				continue
			}
			if seen[k+"\x00"+v] {
				delete(leaf.Params, k)
				continue
			}
			seen[k+"\x00"+v] = true
		}
	}
}

func cloneParams(p map[string]string) map[string]string {
	if p == nil {
		return nil
	}
	out := make(map[string]string, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}

func tileLabel(n *domain.LayoutNode) string {
	switch n.Type {
	case domain.TileTerminal:
		// A session id no longer means "resumed from the session list" — every Claude
		// terminal pins one (claudeTileParams), so it only tells Claude from a shell.
		if n.Params["cmd"] == "claude" || n.Params["session_id"] != "" {
			return "Claude"
		}
		return "Terminal"
	case domain.TileBrowser:
		if tab := activeBrowserTab(n.Params["tabs"], n.Params["active"]); tab != nil && tab.Title != "" {
			return tab.Title
		}
		return "Browser"
	case domain.TileMarkdown:
		return "Markdown"
	case domain.TileEditor:
		if p := n.Params["path"]; p != "" {
			return "✎ " + filepath.Base(p)
		}
		return "Editor"
	case domain.TileNotes:
		return "Notizen"
	case domain.TileTodo:
		return "Todo"
	case domain.TileFiles:
		return "Dateien (Tresor)"
	case domain.TileFileTree:
		return "Dateien (lokal)"
	case domain.TileSessions:
		return "Claude-Sessions"
	case domain.TileTabs:
		return "Browser-Tabs"
	case domain.TileClaude:
		return orText(n.Params["chat_title"], "Claude")
	case domain.TilePipepush:
		return "Pipepush"
	case domain.TilePassbubble:
		return "Passbubble"
	case domain.TileRedmine:
		return "Redmine"
	}
	return string(n.Type)
}

// ─── island slot component ────────────────────────────────────────────────────

// tileIsland renders the empty slot go-app owns; JS (phShell.attachIsland) homes the
// live foreign-DOM element into it on every (re)mount. It deliberately does NOT
// destroy the island on dismount — relayout re-parents it; only closeTile destroys.
type tileIsland struct {
	app.Compo
	PaneID string
	Type   domain.TileType
	Params map[string]string
}

func (t *tileIsland) Render() app.UI {
	return app.Div().Class("ph-island").ID("ph-slot-" + t.PaneID)
}
func (t *tileIsland) OnMount(ctx app.Context)  { t.attach() }
func (t *tileIsland) OnUpdate(ctx app.Context) { t.attach() }
func (t *tileIsland) attach() {
	shell := app.Window().Get("phShell")
	if !shell.Truthy() {
		return
	}
	b, _ := json.Marshal(t.Params)
	shell.Call("attachIsland", t.PaneID, string(t.Type), string(b), "ph-slot-"+t.PaneID)
}
