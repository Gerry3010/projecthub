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

// TestForkParamsDropsInstanceScoped pins the split-a-terminal bug: splitting a tile
// used to clone pty_id into the new pane, so both panes attached to the SAME PTY.
// The sidecar serves one subscriber per session, so the second attach took it over —
// you typed into one terminal and it appeared in the other.
func TestForkParamsDropsInstanceScoped(t *testing.T) {
	src := map[string]string{
		"cwd":        "/home/x/proj",
		"cmd":        "claude",
		"pty_id":     "pty-abc",
		"session_id": "sess-1",
		"prompt":     "hallo",
	}
	got := forkParams(src)
	for _, k := range []string{"pty_id", "session_id", "prompt"} {
		if _, ok := got[k]; ok {
			t.Errorf("forkParams kept instance-scoped %q — the new pane would hijack the old one's session", k)
		}
	}
	if got["cwd"] != "/home/x/proj" || got["cmd"] != "claude" {
		t.Fatalf("forkParams dropped tile-kind params: %+v", got)
	}
	// The source must be untouched: it still owns its live session.
	if src["pty_id"] != "pty-abc" {
		t.Fatal("forkParams mutated the source params")
	}
}

func TestSplitTileGivesNewPaneItsOwnSession(t *testing.T) {
	w := &Workspace{}
	w.layout.Root = &domain.LayoutNode{
		PaneID: "a", Type: domain.TileTerminal,
		Params: map[string]string{"cwd": "/p", "cmd": "claude", "pty_id": "pty-1"},
	}
	w.splitTile("a", "row")
	ls := leaves(w.layout.Root)
	if len(ls) != 2 {
		t.Fatalf("expected 2 leaves after split, got %d", len(ls))
	}
	if ls[0].Params["pty_id"] != "pty-1" {
		t.Fatal("the original pane lost its running session")
	}
	if ls[1].Params["pty_id"] != "" {
		t.Fatalf("the new pane inherited pty_id %q — both panes would drive one PTY", ls[1].Params["pty_id"])
	}
	if ls[1].Params["cwd"] != "/p" || ls[1].Params["cmd"] != "claude" {
		t.Fatalf("the new pane should still be the same KIND of tile: %+v", ls[1].Params)
	}
}

// TestDedupeInstanceParamsHealsSavedLayout covers layouts persisted before the split
// fix, which can already hold the duplicated pty_id on disk.
func TestDedupeInstanceParamsHealsSavedLayout(t *testing.T) {
	a := &domain.LayoutNode{PaneID: "a", Type: domain.TileTerminal, Params: map[string]string{"pty_id": "dup"}}
	b := &domain.LayoutNode{PaneID: "b", Type: domain.TileTerminal, Params: map[string]string{"pty_id": "dup"}}
	c := &domain.LayoutNode{PaneID: "c", Type: domain.TileTerminal, Params: map[string]string{"pty_id": "own"}}
	root := &domain.LayoutNode{Dir: "row", PaneID: "s1", A: a,
		B: &domain.LayoutNode{Dir: "col", PaneID: "s2", A: b, B: c}}

	dedupeInstanceParams(root)

	if a.Params["pty_id"] != "dup" {
		t.Fatal("the first claimant should keep the session")
	}
	if _, ok := b.Params["pty_id"]; ok {
		t.Fatal("the duplicate should have been stripped")
	}
	if c.Params["pty_id"] != "own" {
		t.Fatal("an unrelated pane's own session must survive")
	}
}
