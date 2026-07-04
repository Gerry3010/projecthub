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

package tabsession

import (
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ClaudeProject is a working directory Claude Code has been used in, discovered by
// enumerating ~/.claude/projects. It backs ProjectHub's "add this project?"
// suggestions: the real Cwd, a human title, recency and how many sessions exist.
type ClaudeProject struct {
	Cwd          string    `json:"cwd"`           // real absolute working dir (from transcript, never the dashed dir name)
	Title        string    `json:"title"`         // title of the most-recent session (best-effort)
	LastActive   time.Time `json:"last_active"`   // newest activity across the dir's sessions
	SessionCount int       `json:"session_count"` // number of *.jsonl transcripts
}

// ScanClaudeProjects enumerates every ~/.claude/projects/<encoded-cwd> directory and
// returns one ClaudeProject per directory, newest-first. The real working directory
// is read from the transcript's cwd field (authoritative) rather than decoded from
// the dashed directory name, which is lossy: encodeCwd collapses both '/' and '.' to
// '-' and cannot be reversed. Directories whose cwd cannot be resolved are skipped
// (we won't suggest a path we can't trust). A missing projects dir is not an error.
func ScanClaudeProjects() ([]ClaudeProject, error) {
	base, err := ClaudeProjectsDir()
	if err != nil {
		return nil, err
	}
	dirs, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var projects []ClaudeProject
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		p, ok := scanProjectDir(filepath.Join(base, d.Name()))
		if !ok {
			continue
		}
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].LastActive.After(projects[j].LastActive)
	})
	return projects, nil
}

// scanProjectDir resolves one project directory into a ClaudeProject. To stay cheap
// it fully parses only the most-recently-modified transcript (for the real cwd,
// title and last-active time) and counts the rest by globbing. Returns ok=false if
// the directory has no transcripts or the cwd can't be resolved.
func scanProjectDir(dir string) (ClaudeProject, bool) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(matches) == 0 {
		return ClaudeProject{}, false
	}

	newest, newestMod := "", time.Time{}
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		if mt := fi.ModTime(); mt.After(newestMod) {
			newest, newestMod = m, mt
		}
	}
	if newest == "" {
		return ClaudeProject{}, false
	}

	// fallbackCwd "" so an unresolved cwd stays empty and we can skip it.
	cs, err := parseClaudeSession(newest, "")
	if err != nil || cs.Cwd == "" {
		return ClaudeProject{}, false
	}

	last := cs.LastActive
	if last.IsZero() {
		last = newestMod // transcript had no timestamps; fall back to file mtime
	}
	return ClaudeProject{
		Cwd:          cs.Cwd,
		Title:        cs.Title,
		LastActive:   last,
		SessionCount: len(matches),
	}, true
}
