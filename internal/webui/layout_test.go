// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package webui

import (
	"math"
	"testing"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

func TestSnapRatio(t *testing.T) {
	const thresh = 0.03
	cases := []struct {
		name string
		in   float64
		want float64
	}{
		{"just below a half snaps to it", 0.485, 0.5},
		{"just above a third snaps to it", 0.35, 1.0 / 3},
		{"a quarter", 0.262, 0.25},
		{"two thirds", 0.657, 2.0 / 3},
		{"three quarters", 0.762, 0.75},
		{"far from every point stays put", 0.42, 0.42},
		{"exactly between two points stays put", 0.4, 0.4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := snapRatio(tc.in, thresh); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("snapRatio(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestSnapRatioThresholdIsRespected(t *testing.T) {
	// A tiny threshold must leave everything alone except an exact hit — that is what
	// keeps a very wide split from snapping across a visible distance.
	if got := snapRatio(0.48, 0.001); got != 0.48 {
		t.Fatalf("snapRatio(0.48, 0.001) = %v, want it untouched", got)
	}
	if got := snapRatio(0.5, 0.001); got != 0.5 {
		t.Fatalf("snapRatio(0.5, 0.001) = %v, want 0.5", got)
	}
}

func TestFindSplitOfLeaf(t *testing.T) {
	inner := &domain.LayoutNode{PaneID: "s2", Dir: "col", Ratio: 0.25, A: leaf("b"), B: leaf("c")}
	root := &domain.LayoutNode{PaneID: "s1", Dir: "row", Ratio: 0.5, A: leaf("a"), B: inner}

	sp, side := findSplitOfLeaf(root, "a")
	if sp == nil || sp.PaneID != "s1" || side != "a" {
		t.Fatalf("leaf a: got %v/%q, want s1/a", sp, side)
	}
	sp, side = findSplitOfLeaf(root, "c")
	if sp == nil || sp.PaneID != "s2" || side != "b" {
		t.Fatalf("leaf c: got %v/%q, want s2/b", sp, side)
	}
	if sp, _ := findSplitOfLeaf(leaf("only"), "only"); sp != nil {
		t.Error("a lone root leaf has no enclosing split")
	}
	if sp, _ := findSplitOfLeaf(root, "nope"); sp != nil {
		t.Error("unknown pane must not match a split")
	}
}

func TestBalanceRatios(t *testing.T) {
	root := &domain.LayoutNode{PaneID: "s1", Dir: "row", Ratio: 0.8, A: leaf("a"),
		B: &domain.LayoutNode{PaneID: "s2", Dir: "col", Ratio: 0.15, A: leaf("b"), B: leaf("c")}}
	balanceRatios(root)
	if root.Ratio != 0.5 || root.B.Ratio != 0.5 {
		t.Fatalf("ratios = %v/%v, want 0.5/0.5", root.Ratio, root.B.Ratio)
	}
	if ids := splitIDs(root); len(ids) != 2 || ids[0] != "s1" {
		t.Fatalf("splitIDs = %v, want [s1 s2]", ids)
	}
}

// paneIDs of every tile, in tree order — the check that no tile is lost or duplicated
// when an arrangement is applied.
func paneIDs(n *domain.LayoutNode) []string {
	out := []string{}
	for _, l := range leaves(n) {
		out = append(out, l.PaneID)
	}
	return out
}

func tiles(ids ...string) *domain.LayoutNode {
	nodes := make([]*domain.LayoutNode, 0, len(ids))
	for _, id := range ids {
		nodes = append(nodes, leaf(id))
	}
	return stack(nodes)
}

func templateByKey(t *testing.T, key string) layoutTemplate {
	t.Helper()
	for _, tpl := range layoutTemplates() {
		if tpl.Key == key {
			return tpl
		}
	}
	t.Fatalf("no template %q", key)
	return layoutTemplate{}
}

func TestApplyTemplateKeepsEveryTile(t *testing.T) {
	cols2 := templateByKey(t, "cols2")
	grid4 := templateByKey(t, "grid4")

	// Exactly as many tiles as slots.
	got := applyTemplate(tiles("a", "b"), cols2)
	if ids := paneIDs(got); len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("cols2 with 2 tiles = %v, want [a b]", ids)
	}
	if got.Dir != "row" || got.Ratio != 0.5 {
		t.Fatalf("cols2 root = %s/%v, want row/0.5", got.Dir, got.Ratio)
	}

	// More tiles than slots: the surplus stacks into the last slot, nothing is lost.
	got = applyTemplate(tiles("a", "b", "c", "d", "e"), cols2)
	if ids := paneIDs(got); len(ids) != 5 {
		t.Fatalf("cols2 with 5 tiles kept %v, want all five", ids)
	}
	if !got.A.IsLeaf() || got.A.PaneID != "a" {
		t.Fatalf("first slot = %v, want leaf a", got.A)
	}
	if got.B.IsLeaf() {
		t.Fatal("the surplus must be stacked into the second slot")
	}

	// Fewer tiles than slots: the arrangement is trimmed, never padded with a blank.
	got = applyTemplate(tiles("a", "b"), grid4)
	if ids := paneIDs(got); len(ids) != 2 {
		t.Fatalf("grid4 with 2 tiles = %v, want exactly the two tiles", ids)
	}
	if leaves(got)[0].PaneID != "a" {
		t.Fatalf("reading order not kept: %v", paneIDs(got))
	}
}

func TestApplyTemplateSetsFractionRatios(t *testing.T) {
	got := applyTemplate(tiles("a", "b"), templateByKey(t, "sidebar"))
	if math.Abs(got.Ratio-2.0/3) > 1e-9 {
		t.Fatalf("sidebar ratio = %v, want 2/3", got.Ratio)
	}
	if applyTemplate(nil, templateByKey(t, "cols2")) != nil {
		t.Error("an empty workspace has nothing to arrange")
	}
}

func TestPresetSnapshotDropsInstanceParams(t *testing.T) {
	root := tiles("a", "b")
	leaves(root)[0].Params = map[string]string{
		"cwd": "/p", "cmd": "claude", "pty_id": "pty-1", "session_id": "sid-1", "prompt": "los",
	}
	presets := savePreset(nil, "Arbeit", root)
	if len(presets) != 1 || presets[0].Name != "Arbeit" {
		t.Fatalf("presets = %v, want one named Arbeit", presets)
	}
	saved := leaves(presets[0].Root)[0]
	for _, k := range instanceParams {
		if _, ok := saved.Params[k]; ok {
			t.Fatalf("saved layout still carries %q: %v", k, saved.Params)
		}
	}
	if saved.Params["cwd"] != "/p" || saved.Params["cmd"] != "claude" {
		t.Fatalf("saved params lost what identifies the tile: %v", saved.Params)
	}
	if saved.PaneID == "a" {
		t.Error("a saved tile must get a fresh pane id, not the live one")
	}
}

func TestSavePresetReplacesSameName(t *testing.T) {
	presets := savePreset(nil, "Arbeit", tiles("a", "b"))
	presets = savePreset(presets, "arbeit", tiles("a", "b", "c"))
	if len(presets) != 1 {
		t.Fatalf("same name twice made %d presets, want 1", len(presets))
	}
	if n := len(leaves(presets[0].Root)); n != 3 {
		t.Fatalf("preset holds %d tiles, want the newer 3", n)
	}
	if names := presetNames(presets); len(names) != 1 || names[0] != "arbeit" {
		t.Fatalf("presetNames = %v", names)
	}
	presets = deletePreset(presets, presets[0].ID)
	if len(presets) != 0 {
		t.Fatalf("deletePreset left %v", presets)
	}
}

func TestApplyPresetMintsFreshPaneIDs(t *testing.T) {
	w := &Workspace{layout: domain.Layout{Root: tiles("a", "b")}}
	defer stopSave(w)
	w.layout.Presets = savePreset(nil, "Arbeit", w.layout.Root)
	savedIDs := paneIDs(w.layout.Presets[0].Root)

	w.applyLayoutPreset(w.layout.Presets[0].ID)
	live := paneIDs(w.layout.Root)
	if len(live) != 2 {
		t.Fatalf("restored %v, want two tiles", live)
	}
	for i := range live {
		if live[i] == savedIDs[i] {
			t.Fatalf("restored pane reuses the stored id %q — a second restore would collide", live[i])
		}
	}
}

func TestResetAndBalanceKeepEveryTile(t *testing.T) {
	w := &Workspace{layout: domain.Layout{Root: applyTemplate(tiles("a", "b", "c"), templateByKey(t, "cols3"))}}
	defer stopSave(w)
	w.resetLayout()
	if ids := paneIDs(w.layout.Root); len(ids) != 3 {
		t.Fatalf("reset kept %v, want all three tiles", ids)
	}
	if w.layoutOpen {
		t.Error("applying an arrangement closes the panel")
	}
}

// stopSave cancels the debounced persist a mutation arms; without it the timer fires
// into a nil store after the test is done.
func stopSave(w *Workspace) {
	if w.saveTimer != nil {
		w.saveTimer.Stop()
	}
}
