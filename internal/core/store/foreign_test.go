// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package store

import (
	"context"
	"testing"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/pbclient"
)

func TestForeignEntriesAndLinks(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// A ProjectHub project (creates ph-* entries that must NOT appear as foreign).
	if _, err := s.CreateProject(ctx, "Proj", "", ""); err != nil {
		t.Fatal(err)
	}
	refs, _ := s.ListProjects(ctx)
	folder := refs[0].FolderID
	if _, err := s.CreateNote(ctx, folder, domain.NoteDoc{Title: "a note"}); err != nil {
		t.Fatal(err)
	}

	// Seed a "foreign" Passbubble entry (a login) directly via the API, sealed with
	// the user's key — like something created in the Passbubble app itself.
	encData, encKey, err := s.seal(map[string]string{"username": "bob", "password": "hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.api.CreateEntry(ctx, pbclient.CreateEntryRequest{
		Type:          "login",
		Name:          "GitHub",
		EncryptedData: encData,
		DataNonce:     emptyNonceB64,
		EntryKeys:     []pbclient.EntryKey{{UserID: s.keys.UserID, EncryptedKey: encKey}},
	})
	if err != nil {
		t.Fatalf("seed foreign: %v", err)
	}

	// ListForeignEntries must include the login but NOT any ph-* entry.
	foreign, err := s.ListForeignEntries(ctx)
	if err != nil {
		t.Fatalf("list foreign: %v", err)
	}
	if len(foreign) != 1 || foreign[0].Title != "GitHub" || foreign[0].Type != "login" || !foreign[0].Owned {
		t.Fatalf("expected only the GitHub login, got %+v", foreign)
	}

	// GetForeignEntry decrypts its fields.
	fields, err := s.GetForeignEntry(ctx, resp.ID)
	if err != nil {
		t.Fatalf("get foreign: %v", err)
	}
	if fields["username"] != "bob" || fields["password"] != "hunter2" {
		t.Fatalf("decrypt mismatch: %+v", fields)
	}

	// Link CRUD round-trips.
	if _, err := s.CreatePassbubbleLink(ctx, folder, domain.PassbubbleLink{EntryID: resp.ID, Title: "GitHub", EntryType: "login"}); err != nil {
		t.Fatalf("create link: %v", err)
	}
	links, err := s.ListPassbubbleLinks(ctx, folder)
	if err != nil || len(links) != 1 || links[0].Val.EntryID != resp.ID {
		t.Fatalf("list links mismatch: %v %+v", err, links)
	}
	// A linked ph-pblink entry must itself stay out of the foreign list.
	foreign2, _ := s.ListForeignEntries(ctx)
	if len(foreign2) != 1 {
		t.Fatalf("ph-pblink leaked into foreign list: %+v", foreign2)
	}
	if err := s.DeletePassbubbleLink(ctx, links[0].ID); err != nil {
		t.Fatalf("delete link: %v", err)
	}
	if links, _ := s.ListPassbubbleLinks(ctx, folder); len(links) != 0 {
		t.Fatalf("link not deleted")
	}
}
