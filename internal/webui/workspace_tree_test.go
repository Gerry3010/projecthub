// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package webui

import (
	"testing"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

func leaf(id string) *domain.LayoutNode {
	return &domain.LayoutNode{PaneID: id, Type: domain.TileTerminal}
}

func TestRemoveLeafCollapsesSplit(t *testing.T) {
	root := &domain.LayoutNode{Dir: "row", PaneID: "s", A: leaf("a"), B: leaf("b")}
	if !removeLeaf(&root, "a") {
		t.Fatal("removeLeaf returned false")
	}
	if !root.IsLeaf() || root.PaneID != "b" {
		t.Fatalf("split should collapse to sibling b, got %+v", root)
	}
}

func TestMoveTileEdgeSplit(t *testing.T) {
	// Two tiles side by side; move "a" onto the bottom edge of "b".
	root := &domain.LayoutNode{Dir: "row", PaneID: "s", A: leaf("a"), B: leaf("b")}
	if !moveTileInTree(&root, "a", "b", "bottom") {
		t.Fatal("moveTileInTree returned false")
	}
	// After: root should be just the split that used to hold b, now a col-split of
	// {b, a} (a on the bottom). The old row split collapsed when a was removed.
	if root.IsLeaf() || root.Dir != "col" {
		t.Fatalf("expected a col split at root, got %+v", root)
	}
	if root.A.PaneID != "b" || root.B.PaneID != "a" {
		t.Fatalf("bottom edge → target on top (A=b), moved below (B=a); got A=%s B=%s", root.A.PaneID, root.B.PaneID)
	}
	// Both tiles still present exactly once.
	if len(leaves(root)) != 2 {
		t.Fatalf("expected 2 leaves, got %d", len(leaves(root)))
	}
}

func TestMoveTileLeftEdgeOrder(t *testing.T) {
	root := &domain.LayoutNode{Dir: "col", PaneID: "s", A: leaf("a"), B: leaf("b")}
	if !moveTileInTree(&root, "a", "b", "left") {
		t.Fatal("move failed")
	}
	if root.Dir != "row" || root.A.PaneID != "a" || root.B.PaneID != "b" {
		t.Fatalf("left edge → moved on the left (A=a); got dir=%s A=%s B=%s", root.Dir, root.A.PaneID, root.B.PaneID)
	}
}

func TestSetBrowserStateMutatesLeaf(t *testing.T) {
	browser := &domain.LayoutNode{PaneID: "b", Type: domain.TileBrowser, Params: map[string]string{"url": "about:blank"}}
	w := &Workspace{}
	w.layout.Root = &domain.LayoutNode{Dir: "row", PaneID: "s", A: leaf("a"), B: browser}

	tabs := `[{"url":"https://a.test","title":"A"},{"url":"https://b.test","title":"B"}]`
	w.setBrowserState("b", tabs, "1")
	if w.saveTimer != nil {
		w.saveTimer.Stop() // don't let the debounced persist fire on a nil Store
	}

	if browser.Params["tabs"] != tabs {
		t.Fatalf("tabs not stored: %q", browser.Params["tabs"])
	}
	if browser.Params["active"] != "1" {
		t.Fatalf("active = %q, want 1", browser.Params["active"])
	}
	// Params["url"] tracks the active tab (index 1) for label + back-compat.
	if browser.Params["url"] != "https://b.test" {
		t.Fatalf("url = %q, want https://b.test", browser.Params["url"])
	}
	if got := tileLabel(browser); got != "B" {
		t.Fatalf("tileLabel = %q, want active title B", got)
	}
}

func TestActiveBrowserTab(t *testing.T) {
	tabs := `[{"url":"u0","title":"T0"},{"url":"u1","title":"T1"}]`
	cases := []struct {
		name, json, idx, wantURL string
	}{
		{"in-range", tabs, "1", "u1"},
		{"out-of-range falls back to 0", tabs, "9", "u0"},
		{"bad index falls back to 0", tabs, "x", "u0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tab := activeBrowserTab(c.json, c.idx)
			if tab == nil || tab.URL != c.wantURL {
				t.Fatalf("got %+v, want url %s", tab, c.wantURL)
			}
		})
	}
	if activeBrowserTab("", "0") != nil {
		t.Fatal("empty json should yield nil")
	}
	if activeBrowserTab("[]", "0") != nil {
		t.Fatal("empty array should yield nil")
	}
	if activeBrowserTab("not json", "0") != nil {
		t.Fatal("invalid json should yield nil")
	}
}
