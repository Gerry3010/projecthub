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

package store

import (
	"bytes"
	"context"
	"testing"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

func TestTabSetsPinsFiles(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, err := s.CreateProject(ctx, "Items", "", "")
	if err != nil {
		t.Fatal(err)
	}
	refs, _ := s.ListProjects(ctx)
	folder := refs[0].FolderID
	_ = p

	// tab set
	tsID, err := s.CreateTabSet(ctx, folder, domain.TabSet{
		Title:   "Arbeit",
		Browser: "firefox",
		Tabs:    []domain.Tab{{URL: "https://example.com", Title: "Ex"}, {URL: "https://go.dev"}},
	})
	if err != nil {
		t.Fatalf("create tabset: %v", err)
	}
	tabsets, err := s.ListTabSets(ctx, folder)
	if err != nil || len(tabsets) != 1 {
		t.Fatalf("list tabsets: %v (%d)", err, len(tabsets))
	}
	if len(tabsets[0].Val.Tabs) != 2 || tabsets[0].Val.Tabs[0].URL != "https://example.com" {
		t.Fatalf("tabset round-trip mismatch: %+v", tabsets[0].Val)
	}

	// pin
	if _, err := s.CreatePin(ctx, folder, domain.PinnedItem{Label: "Docs", RelPath: "docs", IsDir: true}); err != nil {
		t.Fatalf("create pin: %v", err)
	}
	pins, err := s.ListPins(ctx, folder)
	if err != nil || len(pins) != 1 || pins[0].Val.RelPath != "docs" {
		t.Fatalf("pin round-trip: %v %+v", err, pins)
	}

	// file
	content := []byte("binary\x00content SECRET")
	fID, err := s.CreateFile(ctx, folder, domain.FileBlob{Filename: "notes.txt", MIME: "text/plain", Size: int64(len(content)), Bytes: content})
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	files, err := s.ListFiles(ctx, folder)
	if err != nil || len(files) != 1 {
		t.Fatalf("list files: %v (%d)", err, len(files))
	}
	if files[0].Val.Filename != "notes.txt" || !bytes.Equal(files[0].Val.Bytes, content) {
		t.Fatalf("file round-trip mismatch: name=%q bytesEqual=%v", files[0].Val.Filename, bytes.Equal(files[0].Val.Bytes, content))
	}
	got, err := s.GetFile(ctx, fID)
	if err != nil || !bytes.Equal(got.Bytes, content) {
		t.Fatalf("get file mismatch: %v", err)
	}

	// delete each kind
	for _, id := range []string{tsID, fID} {
		if err := s.DeleteItem(ctx, id); err != nil {
			t.Fatalf("delete %s: %v", id, err)
		}
	}
	if ts, _ := s.ListTabSets(ctx, folder); len(ts) != 0 {
		t.Fatalf("tabset not deleted")
	}
	if fs, _ := s.ListFiles(ctx, folder); len(fs) != 0 {
		t.Fatalf("file not deleted")
	}
}

func TestLayoutRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, err := s.CreateProject(ctx, "Tiling", "", "")
	if err != nil {
		t.Fatal(err)
	}
	refs, _ := s.ListProjects(ctx)
	folder := refs[0].FolderID
	_ = p

	// none yet
	if l, err := s.GetLayout(ctx, folder); err != nil || l != nil {
		t.Fatalf("expected no layout, got %+v err=%v", l, err)
	}

	layout := domain.Layout{Version: 1, Root: &domain.LayoutNode{
		Dir: "row", Ratio: 0.4, PaneID: "split-1",
		A: &domain.LayoutNode{PaneID: "t1", Type: domain.TileTerminal, Params: map[string]string{"cwd": "/tmp/x"}},
		B: &domain.LayoutNode{PaneID: "m1", Type: domain.TileMarkdown, Params: map[string]string{"path": "/tmp/x/README.md"}},
	}}
	id, err := s.SetLayout(ctx, folder, layout)
	if err != nil {
		t.Fatalf("set layout: %v", err)
	}

	got, err := s.GetLayout(ctx, folder)
	if err != nil || got == nil {
		t.Fatalf("get layout: %v", err)
	}
	if got.Val.Root == nil || got.Val.Root.Dir != "row" || got.Val.Root.A.Type != domain.TileTerminal ||
		got.Val.Root.B.Params["path"] != "/tmp/x/README.md" {
		t.Fatalf("layout round-trip mismatch: %+v", got.Val.Root)
	}

	// update in place keeps the same entry (one ph-layout per project)
	layout.Root.Ratio = 0.6
	id2, err := s.SetLayout(ctx, folder, layout)
	if err != nil || id2 != id {
		t.Fatalf("update should reuse entry: id=%s id2=%s err=%v", id, id2, err)
	}
	got2, _ := s.GetLayout(ctx, folder)
	if got2.Val.Root.Ratio != 0.6 {
		t.Fatalf("ratio not updated: %v", got2.Val.Root.Ratio)
	}
}

func TestBackgroundRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, err := s.CreateProject(ctx, "BG", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// account default
	if bg, err := s.Background(ctx); err != nil || bg != nil {
		t.Fatalf("expected no account bg, got %+v err=%v", bg, err)
	}
	acc := &domain.Background{Type: "color", Color: "#101820", Alpha: 0.7, Blur: 12, Dim: 0.3}
	if err := s.SetBackground(ctx, acc); err != nil {
		t.Fatalf("set account bg: %v", err)
	}
	if bg, _ := s.Background(ctx); bg == nil || bg.Alpha != 0.7 || bg.Blur != 12 {
		t.Fatalf("account bg round-trip mismatch: %+v", bg)
	}

	// per-project override mirrored into the RootIndex
	pbg := &domain.Background{Type: "image", Image: "file:/tmp/wall.jpg", Alpha: 0.5}
	if err := s.SetProjectBackground(ctx, p.ID, pbg); err != nil {
		t.Fatalf("set project bg: %v", err)
	}
	refs, _ := s.ListProjects(ctx)
	if refs[0].Background == nil || refs[0].Background.Image != "file:/tmp/wall.jpg" {
		t.Fatalf("project bg mirror missing: %+v", refs[0].Background)
	}
}
