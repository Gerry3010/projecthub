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

// Package tabstate holds the sidecar's in-memory view of the browsers' coupled tab
// groups, fed live by browser extensions through the native-messaging host and served
// to the WASM UI over /native/tabs. It also brokers the project roster (so the popup
// can list projects) and a per-browser command queue (so a ProjectHub tile can ask the
// extension to focus/reopen a group). Everything here is ephemeral: nothing is
// encrypted or persisted — a fresh launch starts empty and rebuilds as extensions
// report in. A browser whose extension stops reporting ages out after TTL.
package tabstate

import (
	"sort"
	"sync"
	"time"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// DefaultTTL is how long a browser's last report stays live without a fresh update.
// The extension sends a heartbeat (via chrome.alarms, clamped to ~30s minimum) well
// inside this window, so a browser only ages out once it is actually gone.
const DefaultTTL = 90 * time.Second

// Store is concurrency-safe. It holds three independent things: the coupled tab groups
// per browser, the project roster, and a per-browser outbound command queue.
type Store struct {
	mu     sync.Mutex
	ttl    time.Duration
	now    func() time.Time // injectable clock for tests
	by     map[string]domain.LiveBrowserGroups
	roster []domain.RosterEntry
	cmds   map[string][]domain.TabCommand
}

// New returns an empty store with DefaultTTL and the wall clock.
func New() *Store {
	return &Store{
		ttl:  DefaultTTL,
		now:  time.Now,
		by:   map[string]domain.LiveBrowserGroups{},
		cmds: map[string][]domain.TabCommand{},
	}
}

// ─── coupled tab groups ─────────────────────────────────────────────────────────

// Set records (or replaces) one browser's coupled groups. UpdatedAt is stamped with
// the store's clock so staleness is measured against a single time source.
func (s *Store) Set(b domain.LiveBrowserGroups) {
	if b.Browser == "" {
		return
	}
	b.UpdatedAt = s.now()
	s.mu.Lock()
	s.by[b.Browser] = b
	s.mu.Unlock()
}

// Snapshot returns every non-stale browser's groups (for debugging / the un-scoped
// endpoint), evicting stale browsers as a side effect.
func (s *Store) Snapshot() []domain.LiveBrowserGroups {
	cutoff := s.now().Add(-s.ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.LiveBrowserGroups, 0, len(s.by))
	for name, b := range s.by {
		if b.UpdatedAt.Before(cutoff) {
			delete(s.by, name)
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Browser < out[j].Browser })
	return out
}

// GroupsForProject returns the coupled groups (across all live browsers) belonging to
// one project, tagging each group with the browser it came from. Stale browsers are
// evicted as a side effect.
func (s *Store) GroupsForProject(projectID string) []domain.LiveTabGroup {
	cutoff := s.now().Add(-s.ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.LiveTabGroup{} // never nil: an empty result serializes as "[]", not "null"
	for name, b := range s.by {
		if b.UpdatedAt.Before(cutoff) {
			delete(s.by, name)
			continue
		}
		for _, g := range b.Groups {
			if g.ProjectID != projectID {
				continue
			}
			g.Browser = b.Browser
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Browser != out[j].Browser {
			return out[i].Browser < out[j].Browser
		}
		return out[i].Title < out[j].Title
	})
	return out
}

// ─── project roster ─────────────────────────────────────────────────────────────

// SetRoster replaces the project roster (pushed by the unlocked WASM app).
func (s *Store) SetRoster(r []domain.RosterEntry) {
	s.mu.Lock()
	s.roster = r
	s.mu.Unlock()
}

// Roster returns the current project roster (never nil).
func (s *Store) Roster() []domain.RosterEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.RosterEntry, len(s.roster))
	copy(out, s.roster)
	return out
}

// ─── command queue ──────────────────────────────────────────────────────────────

// Enqueue queues a command for the target browser's extension to pick up.
func (s *Store) Enqueue(c domain.TabCommand) {
	if c.Browser == "" {
		return
	}
	s.mu.Lock()
	s.cmds[c.Browser] = append(s.cmds[c.Browser], c)
	s.mu.Unlock()
}

// DrainCommands atomically returns and clears the queued commands for one browser.
// Never nil (an empty queue serializes as "[]", not "null"), consistent with the
// store's other list-returning methods.
func (s *Store) DrainCommands(browser string) []domain.TabCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.TabCommand, len(s.cmds[browser]))
	copy(out, s.cmds[browser])
	delete(s.cmds, browser)
	return out
}
