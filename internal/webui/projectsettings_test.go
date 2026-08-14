// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package webui

import (
	"strings"
	"testing"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/native/nativeclient"
)

func TestNormalizeLocalPath(t *testing.T) {
	cases := []struct {
		in, want string
		reject   bool
	}{
		{in: "/home/gerry/proj", want: "/home/gerry/proj"},
		{in: "  /home/gerry/proj  ", want: "/home/gerry/proj"}, // trimmed
		{in: "/home/gerry/proj/", want: "/home/gerry/proj"},    // trailing sep dropped
		{in: "/home/gerry/proj///", want: "/home/gerry/proj"},
		{in: "/", want: "/"},   // root survives the trailing-sep trim
		{in: "", want: ""},     // empty clears the path — allowed
		{in: "   ", want: ""},  // whitespace only is the same as empty
		{in: "relative/path", reject: true},
		{in: "~/proj", reject: true}, // the sidecar does not expand ~
		{in: "C:\\proj", reject: true},
		{in: "/home/\x00evil", reject: true},
	}
	for _, c := range cases {
		got, reason := normalizeLocalPath(c.in)
		if c.reject {
			if reason == "" {
				t.Errorf("normalizeLocalPath(%q) = %q, want rejection", c.in, got)
			}
			if got != "" {
				t.Errorf("normalizeLocalPath(%q) returned %q alongside a rejection", c.in, got)
			}
			continue
		}
		if reason != "" {
			t.Errorf("normalizeLocalPath(%q) rejected: %s", c.in, reason)
		}
		if got != c.want {
			t.Errorf("normalizeLocalPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// applyLocalPath must update every place the path is mirrored — the open project, the
// home list — and drop a suggestion the project now covers.
func TestApplyLocalPathMirrorsEverywhere(t *testing.T) {
	sel := domain.ProjectRef{ID: "p1", Title: "Eins"}
	r := &Root{
		selected: &sel,
		projects: []domain.ProjectRef{{ID: "p0", Title: "Null"}, {ID: "p1", Title: "Eins"}},
		suggestions: []nativeclient.ClaudeSuggestion{
			{Cwd: "/home/gerry/eins"}, {Cwd: "/home/gerry/anderes"},
		},
	}
	r.applyLocalPath("p1", "/home/gerry/eins")

	if r.selected.LocalPath != "/home/gerry/eins" {
		t.Errorf("selected.LocalPath = %q", r.selected.LocalPath)
	}
	if r.projects[1].LocalPath != "/home/gerry/eins" {
		t.Errorf("projects[1].LocalPath = %q", r.projects[1].LocalPath)
	}
	if r.projects[0].LocalPath != "" {
		t.Errorf("unrelated project was touched: %q", r.projects[0].LocalPath)
	}
	if len(r.suggestions) != 1 || r.suggestions[0].Cwd != "/home/gerry/anderes" {
		t.Errorf("suggestions = %+v, want only the uncovered one", r.suggestions)
	}
	// The edit buffer must now belong to this project, so re-opening the tab does not
	// refill it from the (already updated) ref and lose nothing.
	if r.projPathFor != "p1" || r.projPath != "/home/gerry/eins" {
		t.Errorf("buffer = (%q, %q)", r.projPathFor, r.projPath)
	}
}

// The buffer refills when the settings screen switches to another project, instead of
// showing the previous project's path.
func TestProjectPathBufferRefillsOnProjectSwitch(t *testing.T) {
	a := domain.ProjectRef{ID: "a", LocalPath: "/srv/a"}
	b := domain.ProjectRef{ID: "b", LocalPath: "/srv/b"}
	r := &Root{selected: &a}

	if got := r.projectPathBuffer(); got != "/srv/a" {
		t.Fatalf("first read = %q, want /srv/a", got)
	}
	r.projPath = "/srv/a-edited" // user typed something, but did not save
	if got := r.projectPathBuffer(); got != "/srv/a-edited" {
		t.Errorf("same project re-read = %q, want the unsaved edit kept", got)
	}
	r.selected = &b
	if got := r.projectPathBuffer(); got != "/srv/b" {
		t.Errorf("after switch = %q, want /srv/b", got)
	}
	if r.projPathMsg != "" {
		t.Errorf("stale message survived the switch: %q", r.projPathMsg)
	}
	// A project without a path yields an empty buffer, not the previous one.
	c := domain.ProjectRef{ID: "c"}
	r.selected = &c
	if got := r.projectPathBuffer(); got != "" {
		t.Errorf("path-less project = %q, want empty", got)
	}
	// No project open at all must not panic.
	r.selected = nil
	if got := r.projectPathBuffer(); got != "" {
		t.Errorf("no project = %q, want empty", got)
	}
}

// The Projekt tab only exists while a project is open; closing it must not strand the
// settings pane on a tab that is no longer rendered.
func TestSettingsTabKeyFallsBackWhenProjectClosed(t *testing.T) {
	sel := domain.ProjectRef{ID: "p1"}
	r := &Root{selected: &sel, settingsTab: "project"}
	if got := r.settingsTabKey(); got != "project" {
		t.Errorf("with project open = %q, want project", got)
	}
	r.selected = nil
	if got := r.settingsTabKey(); got != "appearance" {
		t.Errorf("with project closed = %q, want appearance", got)
	}
	// Unrelated tabs are unaffected by the project state.
	r.settingsTab = "terminal"
	if got := r.settingsTabKey(); got != "terminal" {
		t.Errorf("terminal tab = %q", got)
	}
}

func TestNewPathLabel(t *testing.T) {
	r := &Root{}
	if got := r.newPathLabel(); got != "📁 Ordner…" {
		t.Errorf("empty = %q", got)
	}
	r.newPath = "/home/gerry/Sync-Projekte/projecthub"
	if got := r.newPathLabel(); got != "📁 projecthub" {
		t.Errorf("picked = %q, want the folder's base name", got)
	}
	if got := r.newPathTitle(); !strings.Contains(got, r.newPath) {
		t.Errorf("title = %q, want the full path in it", got)
	}
}
