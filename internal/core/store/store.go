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

// Package store maps ProjectHub's domain model onto Passbubble entries/folders.
// It is the single place that knows the encrypted-storage layout; both the WASM
// web frontend and the TUI use it. All encryption happens here via the crypto
// session — the Passbubble server only ever receives ciphertext.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Gerry3010/projecthub/internal/core/crypto"
	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/pbclient"
)

// Store is a high-level, encryption-aware view over a Passbubble account.
type Store struct {
	api  *pbclient.Client
	keys *crypto.Keys

	rootFolderID string // cached id of the __PROJECT_HUB__ folder
	rootEntryID  string // cached id of the ph-root (RootIndex) entry, if it exists
}

// New creates a Store bound to an authenticated API client and unlocked keys.
func New(api *pbclient.Client, keys *crypto.Keys) *Store {
	return &Store{api: api, keys: keys}
}

// ─── low-level entry helpers ────────────────────────────────────────────────

// putEntry seals payload and creates a new Passbubble entry tagged with kind.
func (s *Store) putEntry(ctx context.Context, folderID *string, kind domain.Kind, payload any) (string, error) {
	encData, encKey, err := s.seal(payload)
	if err != nil {
		return "", err
	}
	resp, err := s.api.CreateEntry(ctx, pbclient.CreateEntryRequest{
		FolderID:      folderID,
		Type:          domain.PassbubbleEntryType,
		Name:          string(kind),
		EncryptedData: encData,
		DataNonce:     emptyNonceB64, // server stores but ignores it (nonce is embedded in encData)
		EntryKeys:     []pbclient.EntryKey{{UserID: s.keys.UserID, EncryptedKey: encKey}},
	})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// updateEntry re-seals payload into an existing entry. folderID must be echoed
// back or the backend moves the entry to the root.
func (s *Store) updateEntry(ctx context.Context, id string, folderID *string, kind domain.Kind, payload any) error {
	encData, encKey, err := s.seal(payload)
	if err != nil {
		return err
	}
	_, err = s.api.UpdateEntry(ctx, id, pbclient.UpdateEntryRequest{
		FolderID:      folderID,
		Name:          string(kind),
		EncryptedData: encData,
		DataNonce:     emptyNonceB64,
		EntryKeys:     []pbclient.EntryKey{{UserID: s.keys.UserID, EncryptedKey: encKey}},
	})
	return err
}

// getEntry fetches a single entry by id and decrypts its payload into out.
func (s *Store) getEntry(ctx context.Context, id string, out any) error {
	e, err := s.api.GetEntry(ctx, id)
	if err != nil {
		return err
	}
	return s.open(e, out)
}

// seal encrypts payload (as JSON) for the owner, returning base64 strings ready
// for the API: encrypted_data (nonce||ciphertext) and the owner's wrapped key.
func (s *Store) seal(payload any) (encDataB64, encKeyB64 string, err error) {
	plain, err := json.Marshal(payload)
	if err != nil {
		return "", "", err
	}
	encData, encKey, err := s.keys.Seal(plain)
	if err != nil {
		return "", "", err
	}
	return b64(encData), b64(encKey), nil
}

// open decrypts a single-GET entry response (which carries EncryptedData + the
// caller's EntryKey) into out.
func (s *Store) open(e *pbclient.EntryResponse, out any) error {
	if e.EntryKey == nil {
		return fmt.Errorf("entry %s has no key for current user", e.ID)
	}
	encData, err := unb64(e.EncryptedData)
	if err != nil {
		return fmt.Errorf("decode encrypted_data: %w", err)
	}
	encKey, err := unb64(e.EntryKey.EncryptedKey)
	if err != nil {
		return fmt.Errorf("decode entry_key: %w", err)
	}
	plain, err := s.keys.Open(encData, encKey)
	if err != nil {
		return fmt.Errorf("decrypt entry %s: %w", e.ID, err)
	}
	return json.Unmarshal(plain, out)
}

// entriesOfKind lists (metadata-only) entries in folderID matching kind. The
// returned EntryResponses do NOT carry encrypted_data — call getEntry to read
// content.
func (s *Store) entriesOfKind(ctx context.Context, folderID string, kind domain.Kind) ([]pbclient.EntryResponse, error) {
	all, err := s.api.ListEntries(ctx)
	if err != nil {
		return nil, err
	}
	var out []pbclient.EntryResponse
	for _, e := range all {
		if e.Name == string(kind) && e.FolderID != nil && *e.FolderID == folderID {
			out = append(out, e)
		}
	}
	return out, nil
}

// ─── root folder + index ────────────────────────────────────────────────────

// EnsureRoot makes sure the __PROJECT_HUB__ folder exists and caches its id.
func (s *Store) EnsureRoot(ctx context.Context) (string, error) {
	if s.rootFolderID != "" {
		return s.rootFolderID, nil
	}
	folders, err := s.api.ListFolders(ctx)
	if err != nil {
		return "", err
	}
	if id := findFolder(folders, domain.RootFolderName); id != "" {
		s.rootFolderID = id
		return id, nil
	}
	id, err := s.api.CreateFolder(ctx, pbclient.CreateFolderRequest{Name: domain.RootFolderName})
	if err != nil {
		return "", err
	}
	s.rootFolderID = id
	return id, nil
}

// loadIndex returns the RootIndex, creating an empty one in memory if no ph-root
// entry exists yet. It caches the ph-root entry id for later saves.
func (s *Store) loadIndex(ctx context.Context) (*domain.RootIndex, error) {
	root, err := s.EnsureRoot(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := s.entriesOfKind(ctx, root, domain.KindRoot)
	if err != nil {
		return nil, err
	}
	idx := &domain.RootIndex{Version: 1}
	if len(entries) == 0 {
		return idx, nil
	}
	s.rootEntryID = entries[0].ID
	if err := s.getEntry(ctx, entries[0].ID, idx); err != nil {
		return nil, err
	}
	return idx, nil
}

// saveIndex persists idx into the ph-root entry, creating it on first save.
func (s *Store) saveIndex(ctx context.Context, idx *domain.RootIndex) error {
	root, err := s.EnsureRoot(ctx)
	if err != nil {
		return err
	}
	if s.rootEntryID == "" {
		id, err := s.putEntry(ctx, &root, domain.KindRoot, idx)
		if err != nil {
			return err
		}
		s.rootEntryID = id
		return nil
	}
	return s.updateEntry(ctx, s.rootEntryID, &root, domain.KindRoot, idx)
}

// ─── projects ───────────────────────────────────────────────────────────────

// ListProjects returns the project catalog from the RootIndex.
func (s *Store) ListProjects(ctx context.Context) ([]domain.ProjectRef, error) {
	idx, err := s.loadIndex(ctx)
	if err != nil {
		return nil, err
	}
	return idx.Projects, nil
}

// CreateProject creates a project: a Passbubble subfolder under __PROJECT_HUB__,
// a ph-manifest entry, and a new RootIndex entry. Returns the created project.
// localPath is the project's real working directory on this machine (e.g. a Claude
// Code cwd); pass "" to use the legacy <IndexRoot>/<slug> convention.
func (s *Store) CreateProject(ctx context.Context, title, description, localPath string) (*domain.Project, error) {
	root, err := s.EnsureRoot(ctx)
	if err != nil {
		return nil, err
	}
	idx, err := s.loadIndex(ctx)
	if err != nil {
		return nil, err
	}

	folderID, err := s.api.CreateFolder(ctx, pbclient.CreateFolderRequest{
		Name: domain.ProjectFolderName, ParentID: &root,
	})
	if err != nil {
		return nil, err
	}

	p := &domain.Project{
		ID:          uuid.NewString(),
		Title:       title,
		Description: description,
		Slug:        uniqueSlug(title, idx.Projects),
		LocalPath:   localPath,
		CreatedAt:   time.Now(),
	}
	p.Color = domain.AutoColor(p.ID) // distinct-but-stable default from the id; user can change it
	if _, err := s.putEntry(ctx, &folderID, domain.KindManifest, p); err != nil {
		return nil, err
	}

	idx.Projects = append(idx.Projects, domain.ProjectRef{
		ID: p.ID, FolderID: folderID, Title: p.Title, Slug: p.Slug, LocalPath: p.LocalPath, Color: p.Color,
	})
	if err := s.saveIndex(ctx, idx); err != nil {
		return nil, err
	}
	return p, nil
}

// DeleteProject removes a project's folder (recursively, server-side) and its
// RootIndex entry.
func (s *Store) DeleteProject(ctx context.Context, projectID string) error {
	idx, err := s.loadIndex(ctx)
	if err != nil {
		return err
	}
	kept := idx.Projects[:0]
	var folderID string
	for _, p := range idx.Projects {
		if p.ID == projectID {
			folderID = p.FolderID
			continue
		}
		kept = append(kept, p)
	}
	if folderID == "" {
		return fmt.Errorf("project %s not found", projectID)
	}
	if err := s.api.DeleteFolder(ctx, folderID); err != nil {
		return err
	}
	idx.Projects = kept
	return s.saveIndex(ctx, idx)
}

// SetProjectColor updates a project's accent (a CSS hex like "#6366f1"; "" clears
// it) in both the ph-manifest and its RootIndex mirror, so the change is visible in
// the list view without decrypting the manifest.
func (s *Store) SetProjectColor(ctx context.Context, projectID, color string) error {
	idx, err := s.loadIndex(ctx)
	if err != nil {
		return err
	}
	i := indexOfProject(idx.Projects, projectID)
	if i < 0 {
		return fmt.Errorf("project %s not found", projectID)
	}
	folderID := idx.Projects[i].FolderID

	entryID, manifest, err := s.projectManifest(ctx, folderID)
	if err != nil {
		return err
	}
	manifest.Color = color
	if err := s.updateEntry(ctx, entryID, &folderID, domain.KindManifest, manifest); err != nil {
		return err
	}

	idx.Projects[i].Color = color
	return s.saveIndex(ctx, idx)
}

// AccentColor returns the account-level app accent, or domain.DefaultAccent when
// the user has not chosen one yet.
func (s *Store) AccentColor(ctx context.Context) (string, error) {
	idx, err := s.loadIndex(ctx)
	if err != nil {
		return "", err
	}
	if idx.AccentColor == "" {
		return domain.DefaultAccent, nil
	}
	return idx.AccentColor, nil
}

// SetAccentColor persists the account-level app accent in the RootIndex so it
// syncs across devices.
func (s *Store) SetAccentColor(ctx context.Context, color string) error {
	idx, err := s.loadIndex(ctx)
	if err != nil {
		return err
	}
	idx.AccentColor = color
	return s.saveIndex(ctx, idx)
}

// Background returns the account-level default background (nil ⇒ flat --bg color).
func (s *Store) Background(ctx context.Context) (*domain.Background, error) {
	idx, err := s.loadIndex(ctx)
	if err != nil {
		return nil, err
	}
	return idx.Background, nil
}

// SetBackground persists the account-level default background in the RootIndex.
func (s *Store) SetBackground(ctx context.Context, bg *domain.Background) error {
	idx, err := s.loadIndex(ctx)
	if err != nil {
		return err
	}
	idx.Background = bg
	return s.saveIndex(ctx, idx)
}

// SetProjectBackground updates a project's per-project background override in both
// the ph-manifest and its RootIndex mirror (nil ⇒ inherit the account default).
func (s *Store) SetProjectBackground(ctx context.Context, projectID string, bg *domain.Background) error {
	idx, err := s.loadIndex(ctx)
	if err != nil {
		return err
	}
	i := indexOfProject(idx.Projects, projectID)
	if i < 0 {
		return fmt.Errorf("project %s not found", projectID)
	}
	folderID := idx.Projects[i].FolderID

	entryID, manifest, err := s.projectManifest(ctx, folderID)
	if err != nil {
		return err
	}
	manifest.Background = bg
	if err := s.updateEntry(ctx, entryID, &folderID, domain.KindManifest, manifest); err != nil {
		return err
	}

	idx.Projects[i].Background = bg
	return s.saveIndex(ctx, idx)
}

// projectManifest loads a project folder's ph-manifest entry id and decrypted payload.
func (s *Store) projectManifest(ctx context.Context, folderID string) (string, *domain.Project, error) {
	metas, err := s.entriesOfKind(ctx, folderID, domain.KindManifest)
	if err != nil {
		return "", nil, err
	}
	if len(metas) == 0 {
		return "", nil, fmt.Errorf("no manifest in folder %s", folderID)
	}
	var p domain.Project
	if err := s.getEntry(ctx, metas[0].ID, &p); err != nil {
		return "", nil, err
	}
	return metas[0].ID, &p, nil
}

// indexOfProject returns the position of projectID in refs, or -1.
func indexOfProject(refs []domain.ProjectRef, projectID string) int {
	for i := range refs {
		if refs[i].ID == projectID {
			return i
		}
	}
	return -1
}

// ─── notes ──────────────────────────────────────────────────────────────────

// Note couples a decrypted note with its Passbubble entry id (for update/delete).
type Note struct {
	ID  string
	Doc domain.NoteDoc
}

// CreateNote stores a new note in the given project's folder.
func (s *Store) CreateNote(ctx context.Context, projectFolderID string, doc domain.NoteDoc) (string, error) {
	return s.putEntry(ctx, &projectFolderID, domain.KindNote, doc)
}

// UpdateNote re-encrypts an existing note in place.
func (s *Store) UpdateNote(ctx context.Context, id, projectFolderID string, doc domain.NoteDoc) error {
	return s.updateEntry(ctx, id, &projectFolderID, domain.KindNote, doc)
}

// DeleteNote removes a note entry.
func (s *Store) DeleteNote(ctx context.Context, id string) error {
	return s.api.DeleteEntry(ctx, id)
}

// ListNotes fetches and decrypts every note in a project folder.
func (s *Store) ListNotes(ctx context.Context, projectFolderID string) ([]Note, error) {
	metas, err := s.entriesOfKind(ctx, projectFolderID, domain.KindNote)
	if err != nil {
		return nil, err
	}
	notes := make([]Note, 0, len(metas))
	for _, m := range metas {
		var doc domain.NoteDoc
		if err := s.getEntry(ctx, m.ID, &doc); err != nil {
			return nil, err
		}
		notes = append(notes, Note{ID: m.ID, Doc: doc})
	}
	return notes, nil
}
