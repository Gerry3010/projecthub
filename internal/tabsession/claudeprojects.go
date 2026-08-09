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
// returns the working dirs Claude Code has been used in, newest-first, collapsed to
// their project root: cwds inside the same git repository (e.g. a monorepo's
// steuer-rechner, steuer-rechner/apps/web, …/apps/api) are grouped into one entry at
// the repo root, with session counts summed and the newest activity kept. The real
// cwd is read from the transcript's cwd field (authoritative) rather than decoded
// from the dashed directory name, which is lossy: encodeCwd collapses both '/' and
// '.' to '-' and cannot be reversed. Dirs whose cwd cannot be resolved are skipped;
// a missing projects dir is not an error.
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

	// Group per project root so subfolder-cwds of the same repo collapse into one.
	byRoot := make(map[string]*ClaudeProject)
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		p, ok := scanProjectDir(filepath.Join(base, d.Name()))
		if !ok {
			continue
		}
		root := projectRoot(p.Cwd)
		if agg, seen := byRoot[root]; seen {
			agg.SessionCount += p.SessionCount
			if p.LastActive.After(agg.LastActive) {
				agg.LastActive = p.LastActive
				agg.Title = p.Title // keep the newest session's title
			}
			continue
		}
		pc := p
		pc.Cwd = root
		byRoot[root] = &pc
	}

	projects := make([]ClaudeProject, 0, len(byRoot))
	for _, p := range byRoot {
		projects = append(projects, *p)
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].LastActive.After(projects[j].LastActive)
	})
	return projects, nil
}

// projectRoot walks up from cwd to the nearest git repository root (the directory
// containing a .git entry — a dir for a normal clone, a file for a worktree). It
// returns cwd unchanged if no repo is found, so non-git dirs stay standalone.
func projectRoot(cwd string) string {
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir { // reached the filesystem root
			return cwd
		}
		dir = parent
	}
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
