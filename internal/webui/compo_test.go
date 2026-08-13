// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package webui

import (
	"reflect"
	"testing"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/store"
	"github.com/Gerry3010/projecthub/internal/native/nativeclient"
)

// wouldReconcile mirrors go-app's nodeManager.updateComponent field scan (pkg/app/
// node.go): it copies settable fields from the freshly rendered component onto the
// mounted one and bails out — no OnUpdate, no re-render — when none of them differed.
// Reproduced here rather than called, because go-app keeps it unexported.
func wouldReconcile(mounted, fresh any) bool {
	v := reflect.Indirect(reflect.ValueOf(mounted))
	nv := reflect.Indirect(reflect.ValueOf(fresh))
	for i := 0; i < v.NumField(); i++ {
		field, newField := v.Field(i), nv.Field(i)
		if !field.CanSet() { // unexported → invisible to go-app
			continue
		}
		if _, isCompo := field.Interface().(app.Compo); isCompo {
			continue
		}
		if !reflect.DeepEqual(field.Interface(), newField.Interface()) {
			return true
		}
	}
	return false
}

// TestKeyedWrappersReconcile guards the whole "the UI just doesn't refresh" bug class:
// a keyed wrapper (CompoID) whose fields are all unexported is frozen at its first
// render, because go-app has nothing settable to copy and returns before re-rendering.
// Symptoms this pinned down: renaming a session never showed the input, and closing a
// tile left the focus ring on the pane that was gone. Every wrapper must therefore
// reconcile when re-rendered by its parent — that is what Rev (compo.go) buys.
func TestKeyedWrappersReconcile(t *testing.T) {
	node := &domain.LayoutNode{PaneID: "p1", Type: domain.TileTerminal}
	ws := &Workspace{}

	cases := []struct {
		name  string
		build func() any
	}{
		{"nodeView", func() any { return &nodeView{w: ws, Node: node, Rev: nextRev()} }},
		{"projectItem", func() any { return &projectItem{P: domain.ProjectRef{ID: "x"}, Rev: nextRev()} }},
		{"suggestItem", func() any {
			return &suggestItem{S: nativeclient.ClaudeSuggestion{Cwd: "/tmp"}, Rev: nextRev()}
		}},
		{"todoRow", func() any { return &todoRow{Item: store.Item[domain.TodoItem]{ID: "t"}, Rev: nextRev()} }},
		{"fileRow", func() any { return &fileRow{Item: store.Item[domain.FileBlob]{ID: "f"}, Rev: nextRev()} }},
		{"treeRow", func() any { return &treeRow{Item: treeItem{path: "/a"}, Rev: nextRev()} }},
		{"sessionRow", func() any { return &sessionRow{Item: store.Item[domain.CodeSession]{ID: "s"}, Rev: nextRev()} }},
		{"scannedRow", func() any { return &scannedRow{CS: domain.CodeSession{SessionID: "sc"}, Rev: nextRev()} }},
		{"pbLinkRow", func() any { return &pbLinkRow{Item: store.Item[domain.PassbubbleLink]{ID: "l"}, Rev: nextRev()} }},
	}
	for _, tc := range cases {
		mounted, fresh := tc.build(), tc.build()
		if !wouldReconcile(mounted, fresh) {
			t.Errorf("%s: go-app would skip the update — the component is frozen at its first "+
				"render. Give it an exported field that changes per render (see compo.go).", tc.name)
		}
	}
}

// frozenWrapper is the shape every keyed wrapper here used to have: a parent
// back-pointer plus its data, both unexported. It is the negative control for
// TestKeyedWrappersReconcile — without it, a wouldReconcile that always said "yes"
// would let that test pass while checking nothing.
type frozenWrapper struct {
	app.Compo
	t    *Workspace
	item store.Item[domain.CodeSession]
}

func TestWouldReconcileDetectsFrozenWrapper(t *testing.T) {
	a := &frozenWrapper{t: &Workspace{}, item: store.Item[domain.CodeSession]{ID: "1"}}
	b := &frozenWrapper{t: &Workspace{}, item: store.Item[domain.CodeSession]{ID: "2"}}
	if wouldReconcile(a, b) {
		t.Fatal("wouldReconcile must report NO update for an all-unexported wrapper — " +
			"otherwise TestKeyedWrappersReconcile proves nothing")
	}
}

// TestNodeViewCompoIDKeysByPane pins the reconciliation key: renaming Node to an
// exported field must not silently change what a layout position is keyed by.
func TestNodeViewCompoIDKeysByPane(t *testing.T) {
	v := &nodeView{Node: &domain.LayoutNode{PaneID: "pane-7"}}
	if got := v.CompoID(); got != "pane-7" {
		t.Fatalf("CompoID = %q, want the PaneID", got)
	}
	if got := (&nodeView{}).CompoID(); got != "ph-empty" {
		t.Fatalf("empty slot CompoID = %q, want the ph-empty sentinel", got)
	}
}
