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
	"sort"
	"strings"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/pbclient"
)

// This file implements linking to the user's OTHER Passbubble entries — the logins/
// notes they created in the Passbubble app itself, living on the SAME server outside
// ProjectHub's __PROJECT_HUB__ namespace. ProjectHub only ever stores a *reference*
// (ph-pblink); the secret content is decrypted on demand and never copied in.

// ForeignEntry is metadata for one non-ProjectHub Passbubble vault entry, for the
// link picker. It carries no secret content — GetForeignEntry decrypts on demand.
type ForeignEntry struct {
	ID     string `json:"id"`
	Title  string `json:"title"` // Passbubble entry name
	Type   string `json:"type"`  // "login" | "note" | …
	Folder string `json:"folder,omitempty"`
	Owned  bool   `json:"owned"` // false ⇒ shared by another user (may not be decryptable)
}

// ListForeignEntries returns the user's Passbubble entries that are NOT part of the
// ProjectHub namespace (everything the app itself did not create), so they can be
// linked into a project. ProjectHub entries are marked by a "ph-" Name prefix; any
// other entry is "foreign". Result is sorted by title.
func (s *Store) ListForeignEntries(ctx context.Context) ([]ForeignEntry, error) {
	all, err := s.api.ListEntries(ctx)
	if err != nil {
		return nil, err
	}
	folderNames := s.folderNameMap(ctx)
	var out []ForeignEntry
	for _, e := range all {
		if strings.HasPrefix(e.Name, "ph-") {
			continue // ProjectHub's own entry (ph-note/ph-todo/…)
		}
		fid := ""
		if e.FolderID != nil {
			fid = *e.FolderID
		}
		out = append(out, ForeignEntry{
			ID:     e.ID,
			Title:  e.Name,
			Type:   e.Type,
			Folder: folderNames[fid],
			Owned:  e.OwnerID == "" || e.OwnerID == s.keys.UserID,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out, nil
}

// folderNameMap builds id→name for every folder (walking any nested children), for
// display in the picker. Best-effort: a failed list yields an empty map.
func (s *Store) folderNameMap(ctx context.Context) map[string]string {
	folders, err := s.api.ListFolders(ctx)
	if err != nil {
		return map[string]string{}
	}
	m := map[string]string{}
	var walk func(fs []pbclient.FolderResponse)
	walk = func(fs []pbclient.FolderResponse) {
		for i := range fs {
			m[fs[i].ID] = fs[i].Name
			if len(fs[i].Children) > 0 {
				children := make([]pbclient.FolderResponse, 0, len(fs[i].Children))
				for _, c := range fs[i].Children {
					if c != nil {
						children = append(children, *c)
					}
				}
				walk(children)
			}
		}
	}
	walk(folders)
	return m
}

// GetForeignEntry fetches and decrypts one foreign Passbubble entry's payload into a
// flat map of string fields for display (e.g. a login's username/password/url). The
// crypto stays in-process (WASM) — the sidecar never sees plaintext. Fails cleanly
// if the entry is not addressed to the current user (shared, no key).
func (s *Store) GetForeignEntry(ctx context.Context, id string) (map[string]string, error) {
	e, err := s.api.GetEntry(ctx, id)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := s.open(e, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		switch t := v.(type) {
		case string:
			out[k] = t
		default:
			b, _ := json.Marshal(t)
			out[k] = string(b)
		}
	}
	return out, nil
}

// ─── project links to foreign entries ─────────────────────────────────────────

// CreatePassbubbleLink references a foreign Passbubble entry from a project.
func (s *Store) CreatePassbubbleLink(ctx context.Context, projectFolderID string, l domain.PassbubbleLink) (string, error) {
	return s.putEntry(ctx, &projectFolderID, domain.KindPassbubbleLink, l)
}

// ListPassbubbleLinks returns a project's linked Passbubble entries.
func (s *Store) ListPassbubbleLinks(ctx context.Context, projectFolderID string) ([]Item[domain.PassbubbleLink], error) {
	return listItems[domain.PassbubbleLink](ctx, s, projectFolderID, domain.KindPassbubbleLink)
}

// DeletePassbubbleLink removes a link (leaves the referenced Passbubble entry alone).
func (s *Store) DeletePassbubbleLink(ctx context.Context, id string) error {
	return s.api.DeleteEntry(ctx, id)
}
