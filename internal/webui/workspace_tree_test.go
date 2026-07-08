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
