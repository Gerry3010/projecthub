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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	pb "github.com/Gerry3010/passbubble/backend/pkg/crypto"
	"github.com/Gerry3010/projecthub/internal/core/crypto"
	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/pbclient"
)

func mkNote(title, body string) domain.NoteDoc {
	return domain.NoteDoc{Title: title, Body: body}
}

// fakePB is a tiny in-memory stand-in for the Passbubble REST API, covering the
// folder/entry subset Store uses. It checks the contract that matters for E2E:
// the server only ever receives ciphertext (it never decrypts anything).
type fakePB struct {
	srv     *httptest.Server
	folders map[string]fakeFolder
	entries map[string]fakeEntry
	seq     int
}

type fakeFolder struct {
	ID, Name string
	Parent   *string
}
type fakeEntry struct {
	ID, Name, Type string
	FolderID       *string
	EncryptedData  string
	EntryKey       pbclient.EntryKey
}

func newFakePB(t *testing.T) *fakePB {
	f := &fakePB{folders: map[string]fakeFolder{}, entries: map[string]fakeEntry{}}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/folders", func(w http.ResponseWriter, r *http.Request) {
		var req pbclient.CreateFolderRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.seq++
		id := "folder-" + itoa(f.seq)
		f.folders[id] = fakeFolder{ID: id, Name: req.Name, Parent: req.ParentID}
		writeJSON(w, map[string]string{"id": id})
	})
	mux.HandleFunc("GET /api/v1/folders", func(w http.ResponseWriter, r *http.Request) {
		var out []pbclient.FolderResponse
		for _, fl := range f.folders {
			out = append(out, pbclient.FolderResponse{ID: fl.ID, Name: fl.Name, ParentID: fl.Parent})
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("DELETE /api/v1/folders/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		delete(f.folders, id)
		for eid, e := range f.entries { // recursive: drop entries in the folder
			if e.FolderID != nil && *e.FolderID == id {
				delete(f.entries, eid)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/v1/entries", func(w http.ResponseWriter, r *http.Request) {
		var req pbclient.CreateEntryRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		// Contract: the server must never see plaintext.
		if strings.Contains(req.EncryptedData, "SECRET") || strings.Contains(req.Name, "SECRET") {
			t.Errorf("plaintext leaked to server in create: name=%q data=%q", req.Name, req.EncryptedData)
		}
		f.seq++
		id := "entry-" + itoa(f.seq)
		var key pbclient.EntryKey
		if len(req.EntryKeys) > 0 {
			key = req.EntryKeys[0]
		}
		f.entries[id] = fakeEntry{ID: id, Name: req.Name, Type: req.Type, FolderID: req.FolderID, EncryptedData: req.EncryptedData, EntryKey: key}
		writeJSON(w, pbclient.EntryResponse{ID: id, Name: req.Name, Type: req.Type, FolderID: req.FolderID})
	})
	mux.HandleFunc("GET /api/v1/entries", func(w http.ResponseWriter, r *http.Request) {
		var out []pbclient.EntryResponse // metadata only — no encrypted_data, no entry_key
		for _, e := range f.entries {
			out = append(out, pbclient.EntryResponse{ID: e.ID, Name: e.Name, Type: e.Type, FolderID: e.FolderID})
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /api/v1/entries/{id}", func(w http.ResponseWriter, r *http.Request) {
		e, ok := f.entries[r.PathValue("id")]
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		key := e.EntryKey
		writeJSON(w, pbclient.EntryResponse{ID: e.ID, Name: e.Name, Type: e.Type, FolderID: e.FolderID, EncryptedData: e.EncryptedData, EntryKey: &key})
	})
	mux.HandleFunc("PUT /api/v1/entries/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req pbclient.UpdateEntryRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		e := f.entries[id]
		e.EncryptedData = req.EncryptedData
		if len(req.EntryKeys) > 0 {
			e.EntryKey = req.EntryKeys[0]
		}
		e.FolderID = req.FolderID
		f.entries[id] = e
		writeJSON(w, pbclient.EntryResponse{ID: id})
	})
	mux.HandleFunc("DELETE /api/v1/entries/{id}", func(w http.ResponseWriter, r *http.Request) {
		delete(f.entries, r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// testKeys builds an unlocked crypto session with freshly generated keypairs.
func testKeys(t *testing.T) *crypto.Keys {
	t.Helper()
	privX, pubX, err := pb.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	privM, pubM, err := pb.GenerateMLKEM768()
	if err != nil {
		t.Fatal(err)
	}
	return &crypto.Keys{UserID: "user-1", PrivX25519: privX, PrivMLKEM: privM, PubX25519: pubX, PubMLKEM: pubM}
}

func newTestStore(t *testing.T) *Store {
	f := newFakePB(t)
	api := pbclient.New(f.srv.URL)
	return New(api, testKeys(t))
}

func TestProjectLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	p, err := s.CreateProject(ctx, "Mein SECRET Projekt", "geheime Beschreibung", "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if p.Slug != "mein-secret-projekt" {
		t.Fatalf("unexpected slug %q", p.Slug)
	}

	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(projects) != 1 || projects[0].Title != "Mein SECRET Projekt" {
		t.Fatalf("expected 1 project with decrypted title, got %+v", projects)
	}

	// Fresh store (no cache) must reconstruct identical state from the server.
	s2 := New(s.api, s.keys)
	again, err := s2.ListProjects(ctx)
	if err != nil || len(again) != 1 || again[0].ID != p.ID {
		t.Fatalf("reload mismatch: %+v err=%v", again, err)
	}

	if err := s.DeleteProject(ctx, p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	left, _ := New(s.api, s.keys).ListProjects(ctx)
	if len(left) != 0 {
		t.Fatalf("expected 0 projects after delete, got %d", len(left))
	}
}

func TestProjectColor(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	p, err := s.CreateProject(ctx, "Farbe", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// New projects get a stable default from the built-in palette.
	if !slices.Contains(domain.DefaultPalette, p.Color) {
		t.Fatalf("new project color %q not from DefaultPalette", p.Color)
	}
	refs, _ := s.ListProjects(ctx)
	if refs[0].Color != p.Color {
		t.Fatalf("index mirror %q != manifest %q", refs[0].Color, p.Color)
	}

	if err := s.SetProjectColor(ctx, p.ID, domain.ColorViolet); err != nil {
		t.Fatalf("set color: %v", err)
	}
	// A fresh store must see the new color in both the index mirror and the manifest.
	s2 := New(s.api, s.keys)
	refs2, _ := s2.ListProjects(ctx)
	if refs2[0].Color != domain.ColorViolet {
		t.Fatalf("index color not persisted: %q", refs2[0].Color)
	}
	_, manifest, err := s2.projectManifest(ctx, refs2[0].FolderID)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if manifest.Color != domain.ColorViolet {
		t.Fatalf("manifest color not persisted: %q", manifest.Color)
	}
}

func TestAccentColor(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	got, err := s.AccentColor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != domain.DefaultAccent {
		t.Fatalf("default accent = %q, want %q", got, domain.DefaultAccent)
	}
	if err := s.SetAccentColor(ctx, domain.ColorTeal); err != nil {
		t.Fatalf("set accent: %v", err)
	}
	// Persisted in the RootIndex → a fresh store reads it back.
	again, err := New(s.api, s.keys).AccentColor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again != domain.ColorTeal {
		t.Fatalf("accent not persisted: %q", again)
	}
}

func TestNotesRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, err := s.CreateProject(ctx, "Notes", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Need the folder id for the project.
	refs, _ := s.ListProjects(ctx)
	folderID := refs[0].FolderID

	id, err := s.CreateNote(ctx, folderID, mkNote("Titel", "SECRET body"))
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	notes, err := s.ListNotes(ctx, folderID)
	if err != nil || len(notes) != 1 {
		t.Fatalf("list notes: %v (%d)", err, len(notes))
	}
	if notes[0].Doc.Body != "SECRET body" {
		t.Fatalf("note body mismatch: %q", notes[0].Doc.Body)
	}

	if err := s.UpdateNote(ctx, id, folderID, mkNote("Titel", "edited")); err != nil {
		t.Fatalf("update: %v", err)
	}
	notes, _ = s.ListNotes(ctx, folderID)
	if notes[0].Doc.Body != "edited" {
		t.Fatalf("update not persisted: %q", notes[0].Doc.Body)
	}
	if err := s.DeleteNote(ctx, id); err != nil {
		t.Fatal(err)
	}
	notes, _ = s.ListNotes(ctx, folderID)
	if len(notes) != 0 {
		t.Fatalf("expected 0 notes after delete, got %d", len(notes))
	}
	_ = p
}

func TestTodosRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.CreateProject(ctx, "Todos", "", ""); err != nil {
		t.Fatal(err)
	}
	refs, _ := s.ListProjects(ctx)
	folderID := refs[0].FolderID

	id, err := s.CreateTodo(ctx, folderID, domain.TodoItem{Text: "SECRET task"})
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}
	todos, err := s.ListTodos(ctx, folderID)
	if err != nil || len(todos) != 1 {
		t.Fatalf("list todos: %v (%d)", err, len(todos))
	}
	if todos[0].Val.Text != "SECRET task" || todos[0].Val.Done {
		t.Fatalf("todo mismatch: %+v", todos[0].Val)
	}

	// Toggle done and persist.
	it := todos[0].Val
	it.Done = true
	if err := s.UpdateTodo(ctx, id, folderID, it); err != nil {
		t.Fatalf("update: %v", err)
	}
	todos, _ = s.ListTodos(ctx, folderID)
	if !todos[0].Val.Done {
		t.Fatalf("toggle not persisted: %+v", todos[0].Val)
	}

	if err := s.DeleteTodo(ctx, id); err != nil {
		t.Fatal(err)
	}
	todos, _ = s.ListTodos(ctx, folderID)
	if len(todos) != 0 {
		t.Fatalf("expected 0 todos after delete, got %d", len(todos))
	}
}
