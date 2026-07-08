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

	layout   domain.Layout
	layoutID string
	loaded   bool
	status   string
	addOpen  bool // add-tile menu visible

	// appearance / background
	accountBg  *domain.Background
	apprOpen   bool
	apprScope  string // "project" | "account"
	bgImageURL string // cached resolved data URL for the current wallpaper image

	saveTimer *time.Timer
	bgTimer   *time.Timer
}

func (w *Workspace) OnMount(ctx app.Context) {
	// Report divider-drag ratios from JS back into the layout tree.
	app.Window().Set("phWsRatio", app.FuncOf(func(_ app.Value, args []app.Value) any {
		if len(args) >= 2 {
			w.setRatio(args[0].String(), args[1].Float())
		}
		return nil
	}))
	if w.apprScope == "" {
		w.apprScope = "project"
	}
	ctx.Async(func() {
		item, err := w.Store.GetLayout(context.Background(), w.Ref.FolderID)
		accountBg, _ := w.Store.Background(context.Background())
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
			w.accountBg = accountBg
			w.bgImageURL = imgURL
			w.loaded = true
			applyBackground(eff, imgURL)
		})
	})
}

// OnDismount tears down every live island so PTYs/webviews don't outlive the project.
func (w *Workspace) OnDismount() {
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
		app.If(w.apprOpen, w.appearancePanel),
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
		swatchBar(w.Ref.AccentColor(), w.pickColor, w.customColor),
		app.Button().Class("ph-tile-btn").Title("Aussehen / Hintergrund").Text("🎨").
			OnClick(func(ctx app.Context, _ app.Event) { w.apprOpen = !w.apprOpen }),
		app.Div().Class("ph-add").Body(
			app.Button().Class("ph-btn").Text("+ Tile").OnClick(func(ctx app.Context, _ app.Event) {
				w.addOpen = !w.addOpen
			}),
			app.If(w.addOpen, w.addMenu),
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
		opt("Browser", domain.TileBrowser, map[string]string{"url": "about:blank"}),
		opt("Notizen", domain.TileNotes, nil),
		opt("Todo", domain.TileTodo, nil),
		opt("Dateien", domain.TileFiles, nil),
		opt("Claude-Sessions", domain.TileSessions, nil),
		opt("Browser-Tabs", domain.TileTabs, nil),
		opt("Claude", domain.TileClaude, nil),
		opt("Pipepush", domain.TilePipepush, nil),
	)
}

// ─── split-tree rendering ─────────────────────────────────────────────────────

func (w *Workspace) renderNode(n *domain.LayoutNode) app.UI {
	if n == nil {
		return app.Div().Class("ph-empty").Text("Leerer Workspace — oben ein Tile hinzufügen.")
	}
	if n.IsLeaf() {
		return w.renderTile(n)
	}
	ratio := n.Ratio
	if ratio == 0 {
		ratio = 0.5
	}
	return app.Div().Class("ph-split").Attr("data-dir", n.Dir).Style("--r", fmt.Sprintf("%.4f", ratio)).Body(
		app.Div().Class("ph-split-a").Body(w.renderNode(n.A)),
		app.Div().Class("ph-divider").Attr("data-node", n.PaneID),
		app.Div().Class("ph-split-b").Body(w.renderNode(n.B)),
	)
}

func (w *Workspace) renderTile(n *domain.LayoutNode) app.UI {
	paneID := n.PaneID
	// The whole tile is the DROP target, but only the titlebar is draggable — else a
	// drag started inside a terminal/webview and the tile could vanish.
	return app.Div().Class("ph-tile").Attr("data-pane", paneID).
		OnDragOver(func(ctx app.Context, e app.Event) { e.PreventDefault() }).
		OnDrop(func(ctx app.Context, e app.Event) {
			e.PreventDefault()
			src := e.Get("dataTransfer").Call("getData", "text/plain").String()
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
	return app.Div().Class("ph-tile-bar").
		Attr("draggable", true).
		OnDragStart(func(ctx app.Context, e app.Event) {
			e.Get("dataTransfer").Call("setData", "text/plain", paneID)
		}).
		Body(
			app.Span().Class("ph-tile-title").Text(tileLabel(n)),
			app.Button().Class("ph-tile-btn").Title("horizontal teilen").Text("⇆").OnClick(func(ctx app.Context, _ app.Event) {
				w.splitTile(paneID, "row")
			}),
			app.Button().Class("ph-tile-btn").Title("vertikal teilen").Text("⇅").OnClick(func(ctx app.Context, _ app.Event) {
				w.splitTile(paneID, "col")
			}),
			app.Button().Class("ph-tile-btn").Title("schließen").Text("✕").OnClick(func(ctx app.Context, _ app.Event) {
				w.closeTile(paneID)
			}),
		)
}

func (w *Workspace) renderTileBody(n *domain.LayoutNode) app.UI {
	switch n.Type {
	case domain.TileTerminal, domain.TileBrowser, domain.TileMarkdown:
		return w.islandTile(n)
	case domain.TileNotes:
		return &notesTile{Store: w.Store, FolderID: w.Ref.FolderID}
	case domain.TileTodo:
		return &todoTile{Store: w.Store, FolderID: w.Ref.FolderID}
	case domain.TileFiles:
		return &filesTile{Store: w.Store, FolderID: w.Ref.FolderID}
	case domain.TileSessions:
		return &sessionsTile{Store: w.Store, FolderID: w.Ref.FolderID, Cwd: w.Ref.LocalPath,
			OpenTerminal: func(ctx app.Context, cwd, sessionID string) {
				w.addTile(domain.TileTerminal, map[string]string{"cwd": cwd, "session_id": sessionID})
			}}
	case domain.TileTabs:
		return &tabsTile{Native: w.Native, ProjectID: w.Ref.ID}
	case domain.TileClaude:
		return &claudeTile{Native: w.Native, Cwd: w.Ref.LocalPath,
			OpenClaude: func(ctx app.Context, cwd, prompt string) {
				w.addTile(domain.TileTerminal, map[string]string{"cwd": cwd, "cmd": "claude", "prompt": prompt})
			}}
	case domain.TilePipepush:
		return &pipepushTile{Store: w.Store, Native: w.Native, FolderID: w.Ref.FolderID}
	default:
		return app.Div().Class("ph-muted").Text("Unbekannter Tile-Typ")
	}
}

// islandTile renders the empty slot + (for browser/markdown) an address/path bar;
// the JS island layer attaches the live element into #ph-slot-<paneID>.
func (w *Workspace) islandTile(n *domain.LayoutNode) app.UI {
	var bar app.UI = app.Div()
	switch n.Type {
	case domain.TileBrowser:
		bar = w.browserBar(n)
	case domain.TileMarkdown:
		bar = w.pathBar(n)
	}
	return app.Div().Class("ph-island-wrap").Body(
		bar,
		&tileIsland{PaneID: n.PaneID, Type: n.Type, Params: cloneParams(n.Params)},
	)
}

// setParam updates one leaf param in place and returns the leaf (nil if gone).
func (w *Workspace) setParam(paneID, key, val string) *domain.LayoutNode {
	leaf := findLeaf(w.layout.Root, paneID)
	if leaf == nil {
		return nil
	}
	if leaf.Params == nil {
		leaf.Params = map[string]string{}
	}
	leaf.Params[key] = val
	return leaf
}

// browserBar is the browser tile's address bar: type a URL, press Enter or click →,
// and the <webview> navigates (no tile rebuild). Prepends https:// in JS if omitted.
func (w *Workspace) browserBar(n *domain.LayoutNode) app.UI {
	paneID := n.PaneID
	nav := func(url string) {
		w.setParam(paneID, "url", url)
		if shell := app.Window().Get("phShell"); shell.Truthy() {
			shell.Call("navigate", paneID, url)
		}
		w.persistSoon()
	}
	return app.Div().Class("ph-island-bar ph-browser-bar").Body(
		app.Input().Class("ph-island-input").Type("text").ID("ph-url-"+paneID).
			Placeholder("https://…").Value(n.Params["url"]).
			OnKeyDown(func(ctx app.Context, e app.Event) {
				if e.Get("key").String() == "Enter" {
					nav(ctx.JSSrc().Get("value").String())
				}
			}),
		app.Button().Class("ph-btn ph-go").Attr("title", "Laden").Text("→").
			OnClick(func(ctx app.Context, e app.Event) {
				el := app.Window().Get("document").Call("getElementById", "ph-url-"+paneID)
				if el.Truthy() {
					nav(el.Get("value").String())
				}
			}),
	)
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

func (w *Workspace) addTile(t domain.TileType, params map[string]string) {
	leaf := newLeaf(t, params)
	if w.layout.Root == nil {
		w.layout.Root = leaf
	} else {
		// Split the whole workspace, new tile on the right.
		w.layout.Root = &domain.LayoutNode{Dir: "row", Ratio: 0.5, PaneID: uuid.NewString(), A: w.layout.Root, B: leaf}
	}
	w.persistSoon()
}

func (w *Workspace) splitTile(paneID, dir string) {
	parent, side := findParentOf(&w.layout.Root, paneID)
	if parent == nil {
		return
	}
	old := *parent
	*parent = &domain.LayoutNode{
		Dir: dir, Ratio: 0.5, PaneID: uuid.NewString(),
		A: old,
		B: newLeaf(old.Type, cloneParams(old.Params)),
	}
	_ = side
	w.persistSoon()
}

func (w *Workspace) closeTile(paneID string) {
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
	if ws := app.Window().Get("document").Call("querySelector", ".ph-workspace"); ws.Truthy() {
		ws.Get("style").Call("setProperty", "--accent", color)
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
		if n.Params["session_id"] != "" {
			return "Claude · Resume"
		}
		if n.Params["cmd"] == "claude" {
			return "Claude"
		}
		return "Terminal"
	case domain.TileBrowser:
		return "Browser"
	case domain.TileMarkdown:
		return "Markdown"
	case domain.TileNotes:
		return "Notizen"
	case domain.TileTodo:
		return "Todo"
	case domain.TileFiles:
		return "Dateien"
	case domain.TileSessions:
		return "Claude-Sessions"
	case domain.TileTabs:
		return "Browser-Tabs"
	case domain.TileClaude:
		return "Claude"
	case domain.TilePipepush:
		return "Pipepush"
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
