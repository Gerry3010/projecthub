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

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// Item couples a decrypted payload of type T with its Passbubble entry id, so the
// caller can later update or delete it.
type Item[T any] struct {
	ID  string
	Val T
}

// listItems fetches and decrypts every entry of the given kind in a project folder.
// Listing is metadata-only at the API; each item is then fetched individually to
// obtain and decrypt its payload.
func listItems[T any](ctx context.Context, s *Store, folderID string, kind domain.Kind) ([]Item[T], error) {
	metas, err := s.entriesOfKind(ctx, folderID, kind)
	if err != nil {
		return nil, err
	}
	out := make([]Item[T], 0, len(metas))
	for _, m := range metas {
		var v T
		if err := s.getEntry(ctx, m.ID, &v); err != nil {
			return nil, err
		}
		out = append(out, Item[T]{ID: m.ID, Val: v})
	}
	return out, nil
}

// DeleteItem removes any ProjectHub entry (tab set, pin, file, note) by id.
func (s *Store) DeleteItem(ctx context.Context, id string) error {
	return s.api.DeleteEntry(ctx, id)
}

// ─── tab sets ───────────────────────────────────────────────────────────────

func (s *Store) CreateTabSet(ctx context.Context, projectFolderID string, ts domain.TabSet) (string, error) {
	return s.putEntry(ctx, &projectFolderID, domain.KindTabSet, ts)
}

func (s *Store) ListTabSets(ctx context.Context, projectFolderID string) ([]Item[domain.TabSet], error) {
	return listItems[domain.TabSet](ctx, s, projectFolderID, domain.KindTabSet)
}

// ─── pinned items ───────────────────────────────────────────────────────────

func (s *Store) CreatePin(ctx context.Context, projectFolderID string, p domain.PinnedItem) (string, error) {
	return s.putEntry(ctx, &projectFolderID, domain.KindPin, p)
}

func (s *Store) ListPins(ctx context.Context, projectFolderID string) ([]Item[domain.PinnedItem], error) {
	return listItems[domain.PinnedItem](ctx, s, projectFolderID, domain.KindPin)
}

// ─── Claude Code sessions ─────────────────────────────────────────────────────

func (s *Store) CreateCodeSession(ctx context.Context, projectFolderID string, cs domain.CodeSession) (string, error) {
	return s.putEntry(ctx, &projectFolderID, domain.KindCodeSession, cs)
}

func (s *Store) ListCodeSessions(ctx context.Context, projectFolderID string) ([]Item[domain.CodeSession], error) {
	return listItems[domain.CodeSession](ctx, s, projectFolderID, domain.KindCodeSession)
}

// ─── pipepush link ────────────────────────────────────────────────────────────

// GetPipepushLink returns the project's pipepush link, or nil if none is set.
func (s *Store) GetPipepushLink(ctx context.Context, projectFolderID string) (*Item[domain.PipepushLink], error) {
	links, err := listItems[domain.PipepushLink](ctx, s, projectFolderID, domain.KindPipepushLink)
	if err != nil {
		return nil, err
	}
	if len(links) == 0 {
		return nil, nil
	}
	return &links[0], nil
}

// SetPipepushLink creates the project's pipepush link, or updates it in place if
// one already exists. A project links to at most one pipepush project.
func (s *Store) SetPipepushLink(ctx context.Context, projectFolderID string, l domain.PipepushLink) (string, error) {
	existing, err := s.GetPipepushLink(ctx, projectFolderID)
	if err != nil {
		return "", err
	}
	if existing != nil {
		return existing.ID, s.updateEntry(ctx, existing.ID, &projectFolderID, domain.KindPipepushLink, l)
	}
	return s.putEntry(ctx, &projectFolderID, domain.KindPipepushLink, l)
}

// ─── files ──────────────────────────────────────────────────────────────────

// CreateFile stores an uploaded file inline (encrypted). Keep to small files in
// v1; see domain.FileBlob.
func (s *Store) CreateFile(ctx context.Context, projectFolderID string, f domain.FileBlob) (string, error) {
	return s.putEntry(ctx, &projectFolderID, domain.KindFile, f)
}

// ListFiles decrypts every file entry in a project folder, including its bytes.
// (Filenames live in the encrypted payload, so metadata can't be read without
// decrypting — acceptable for the small files v1 targets.)
func (s *Store) ListFiles(ctx context.Context, projectFolderID string) ([]Item[domain.FileBlob], error) {
	return listItems[domain.FileBlob](ctx, s, projectFolderID, domain.KindFile)
}

// GetFile fetches and decrypts a single file by entry id.
func (s *Store) GetFile(ctx context.Context, id string) (domain.FileBlob, error) {
	var f domain.FileBlob
	err := s.getEntry(ctx, id, &f)
	return f, err
}
