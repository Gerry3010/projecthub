// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// The arithmetic and tree surgery behind the layout manager: the fractions a
// splitter snaps to, the built-in arrangements, and saved per-project layouts.
// Everything here is pure — no go-app, no store — so it is directly testable.

package webui

import (
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// SnapPoints are the fractions a divider snaps to while dragging, and the fractions
// the tile menu offers. Quarters and thirds — the two ways people actually split a
// screen. Kept here as the single source: the drag itself happens in JS, which is
// handed this list on mount (phShell.setSnapPoints).
var SnapPoints = []float64{0.25, 1.0 / 3, 0.5, 2.0 / 3, 0.75}

// snapLabels name the same fractions for buttons and tooltips, in SnapPoints order.
var snapLabels = []string{"¼", "⅓", "½", "⅔", "¾"}

// snapRatio pulls r onto the nearest snap point when it is within thresh, and leaves
// it alone otherwise. Mirrored in app/renderer/shell/index.ts for the live drag; this
// copy serves the menu presets and the tests that pin the table down.
func snapRatio(r, thresh float64) float64 {
	best, bestDist := r, thresh
	for _, p := range SnapPoints {
		d := p - r
		if d < 0 {
			d = -d
		}
		if d <= bestDist {
			best, bestDist = p, d
		}
	}
	return best
}

// findSplitOfLeaf returns the split that directly contains the leaf, plus which side
// it sits on ("a" or "b"). A leaf that is the whole workspace has no split — the
// caller must then hide any "make this pane 1/3 wide" affordance, because there is
// nothing to take the space from.
func findSplitOfLeaf(n *domain.LayoutNode, paneID string) (*domain.LayoutNode, string) {
	if n == nil || n.IsLeaf() {
		return nil, ""
	}
	if n.A.IsLeaf() && n.A.PaneID == paneID {
		return n, "a"
	}
	if n.B.IsLeaf() && n.B.PaneID == paneID {
		return n, "b"
	}
	if s, side := findSplitOfLeaf(n.A, paneID); s != nil {
		return s, side
	}
	return findSplitOfLeaf(n.B, paneID)
}

// balanceRatios resets every split to an even share.
func balanceRatios(n *domain.LayoutNode) {
	if n == nil || n.IsLeaf() {
		return
	}
	n.Ratio = 0.5
	balanceRatios(n.A)
	balanceRatios(n.B)
}

// splitIDs collects the ids of every split node, top-down — the JS side needs them to
// re-apply ratios after a rearrangement (go-app does not reliably re-write an inline
// custom property on re-render; see applyColor).
func splitIDs(n *domain.LayoutNode) []string {
	if n == nil || n.IsLeaf() {
		return nil
	}
	return append([]string{n.PaneID}, append(splitIDs(n.A), splitIDs(n.B)...)...)
}

// ─── built-in arrangements ────────────────────────────────────────────────────

// layoutTemplate is a fixed arrangement, independent of any project. build receives
// exactly len(shape) tiles — packTiles guarantees that — and wires them into a tree.
type layoutTemplate struct {
	Key   string
	Label string
	Slots int // how many places the arrangement has
	// Cells draws the arrangement for the picker: x, y, w, h in 0..1, in the same
	// order Build fills them. Keeping the picture next to the tree means the two
	// cannot drift apart.
	Cells [][4]float64
	Build func(t []*domain.LayoutNode) *domain.LayoutNode
}

// layoutTemplates are the arrangements the layout manager offers, in display order.
func layoutTemplates() []layoutTemplate {
	return []layoutTemplate{
		{Key: "stack", Label: "Gestapelt", Slots: 1,
			Cells: [][4]float64{{0, 0, 1, 1}},
			Build: func(t []*domain.LayoutNode) *domain.LayoutNode {
				return t[0]
			}},
		{Key: "cols2", Label: "2 Spalten", Slots: 2,
			Cells: [][4]float64{{0, 0, 0.5, 1}, {0.5, 0, 0.5, 1}},
			Build: func(t []*domain.LayoutNode) *domain.LayoutNode {
				return split("row", 0.5, t[0], t[1])
			}},
		{Key: "cols3", Label: "3 Spalten", Slots: 3,
			Cells: [][4]float64{{0, 0, 1.0 / 3, 1}, {1.0 / 3, 0, 1.0 / 3, 1}, {2.0 / 3, 0, 1.0 / 3, 1}},
			Build: func(t []*domain.LayoutNode) *domain.LayoutNode {
				return split("row", 1.0/3, t[0], split("row", 0.5, t[1], t[2]))
			}},
		{Key: "sidebar", Label: "Haupt + Seitenleiste", Slots: 2,
			Cells: [][4]float64{{0, 0, 2.0 / 3, 1}, {2.0 / 3, 0, 1.0 / 3, 1}},
			Build: func(t []*domain.LayoutNode) *domain.LayoutNode {
				return split("row", 2.0/3, t[0], t[1])
			}},
		{Key: "grid4", Label: "2×2-Raster", Slots: 4,
			Cells: [][4]float64{{0, 0, 0.5, 0.5}, {0.5, 0, 0.5, 0.5}, {0, 0.5, 0.5, 0.5}, {0.5, 0.5, 0.5, 0.5}},
			Build: func(t []*domain.LayoutNode) *domain.LayoutNode {
				return split("row", 0.5, split("col", 0.5, t[0], t[2]), split("col", 0.5, t[1], t[3]))
			}},
		{Key: "main2", Label: "Haupt + 2 unten", Slots: 3,
			Cells: [][4]float64{{0, 0, 1, 2.0 / 3}, {0, 2.0 / 3, 0.5, 1.0 / 3}, {0.5, 2.0 / 3, 0.5, 1.0 / 3}},
			Build: func(t []*domain.LayoutNode) *domain.LayoutNode {
				return split("col", 2.0/3, t[0], split("row", 0.5, t[1], t[2]))
			}},
	}
}

func split(dir string, ratio float64, a, b *domain.LayoutNode) *domain.LayoutNode {
	return &domain.LayoutNode{PaneID: uuid.NewString(), Dir: dir, Ratio: ratio, A: a, B: b}
}

// stack piles tiles into one column, evenly.
func stack(tiles []*domain.LayoutNode) *domain.LayoutNode {
	if len(tiles) == 0 {
		return nil
	}
	out := tiles[len(tiles)-1]
	for i := len(tiles) - 2; i >= 0; i-- {
		out = split("col", 1/float64(len(tiles)-i), tiles[i], out)
	}
	return out
}

// packTiles distributes the existing tiles over a template's slots: one tile per slot
// in reading order, with any surplus stacked into the last slot. Fewer tiles than
// slots simply means fewer slots. No tile is ever dropped — rearranging must never
// cost you a running terminal.
func packTiles(tiles []*domain.LayoutNode, slots int) []*domain.LayoutNode {
	if len(tiles) <= slots {
		return tiles
	}
	out := append([]*domain.LayoutNode(nil), tiles[:slots-1]...)
	return append(out, stack(tiles[slots-1:]))
}

// applyTemplate rebuilds root with the given arrangement, keeping every tile (and its
// pane id, so islands stay mounted with their PTY/session). Returns nil when there is
// nothing to arrange.
func applyTemplate(root *domain.LayoutNode, tpl layoutTemplate) *domain.LayoutNode {
	tiles := leaves(root)
	if len(tiles) == 0 {
		return nil
	}
	slots := tpl.Slots
	if slots > len(tiles) {
		// Truncate the arrangement to what we can fill: 3 columns with 2 tiles is
		// 2 columns, not an empty pane.
		for _, alt := range layoutTemplates() {
			if alt.Slots == len(tiles) && alt.Key != tpl.Key {
				return alt.Build(tiles)
			}
		}
		return stack(tiles)
	}
	return tpl.Build(packTiles(tiles, slots))
}

// ─── saved layouts (per project) ──────────────────────────────────────────────

// snapshotTree copies a tree for storage: fresh pane ids and no instance-scoped
// params, so applying a saved layout opens the same TILES rather than trying to
// re-adopt PTYs and Claude sessions that are long gone.
func snapshotTree(n *domain.LayoutNode) *domain.LayoutNode {
	if n == nil {
		return nil
	}
	if n.IsLeaf() {
		return &domain.LayoutNode{PaneID: uuid.NewString(), Type: n.Type, Params: forkParams(n.Params)}
	}
	return &domain.LayoutNode{
		PaneID: uuid.NewString(), Dir: n.Dir, Ratio: n.Ratio,
		A: snapshotTree(n.A), B: snapshotTree(n.B),
	}
}

// savePreset adds (or replaces, by name) a named snapshot of root.
func savePreset(presets []domain.LayoutPreset, name string, root *domain.LayoutNode) []domain.LayoutPreset {
	name = strings.TrimSpace(name)
	if name == "" || root == nil {
		return presets
	}
	p := domain.LayoutPreset{ID: uuid.NewString(), Name: name, SavedAt: time.Now(), Root: snapshotTree(root)}
	for i := range presets {
		if strings.EqualFold(presets[i].Name, name) {
			p.ID = presets[i].ID
			presets[i] = p
			return presets
		}
	}
	return append(presets, p)
}

// deletePreset removes the preset with id, keeping the order of the rest.
func deletePreset(presets []domain.LayoutPreset, id string) []domain.LayoutPreset {
	out := presets[:0:0]
	for _, p := range presets {
		if p.ID != id {
			out = append(out, p)
		}
	}
	return out
}

// presetNames lists the saved layouts by name (for layout_get / diagnostics).
func presetNames(presets []domain.LayoutPreset) []string {
	out := make([]string, 0, len(presets))
	for _, p := range presets {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}
