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
