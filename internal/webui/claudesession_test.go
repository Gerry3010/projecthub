// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package webui

import (
	"testing"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

func TestClaudeTileParams(t *testing.T) {
	cases := []struct {
		name              string
		prompt, sessionID string
		want              map[string]string
	}{
		{
			name: "plain start",
			want: map[string]string{"cwd": "/p", "cmd": "claude"},
		},
		{
			name:   "with prompt",
			prompt: "mach was",
			want:   map[string]string{"cwd": "/p", "cmd": "claude", "prompt": "mach was"},
		},
		{
			name:      "continue a chat",
			sessionID: "sid-1",
			want:      map[string]string{"cwd": "/p", "cmd": "claude", "session_id": "sid-1"},
		},
		{
			name: "prompt into a chat", prompt: "weiter", sessionID: "sid-2",
			want: map[string]string{"cwd": "/p", "cmd": "claude", "prompt": "weiter", "session_id": "sid-2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := claudeTileParams("/p", tc.prompt, tc.sessionID)
			if len(got) != len(tc.want) {
				t.Fatalf("params = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("params[%q] = %q, want %q (all: %v)", k, got[k], v, got)
				}
			}
		})
	}
}

func TestWindowTitle(t *testing.T) {
	cases := []struct {
		name string
		ref  *domain.ProjectRef
		want string
	}{
		{name: "home view", ref: nil, want: "ProjectHub"},
		{name: "open project", ref: &domain.ProjectRef{Title: "Chattr"}, want: "Chattr · ProjectHub"},
		{name: "padded title", ref: &domain.ProjectRef{Title: "  Homepage  "}, want: "Homepage · ProjectHub"},
		{name: "untitled project", ref: &domain.ProjectRef{Title: "   "}, want: "ProjectHub"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowTitle(tc.ref); got != tc.want {
				t.Fatalf("windowTitle = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSyncWindowTitleOnlyWritesOnChange(t *testing.T) {
	// The render loop calls this on every pass, so it must be a no-op once the title
	// matches — the DOM write is guarded by the cached value, not by the caller.
	r := &Root{selected: &domain.ProjectRef{Title: "Chattr"}}
	r.docTitle = "Chattr · ProjectHub"
	r.syncWindowTitle() // no app.Window() call: would panic outside wasm if it wrote
	if r.docTitle != "Chattr · ProjectHub" {
		t.Fatalf("docTitle = %q, want it untouched", r.docTitle)
	}
}

func TestSetParamClearsOnEmptyValue(t *testing.T) {
	w := &Workspace{layout: domain.Layout{Root: leaf("pane-1")}}
	if w.setParam("pane-1", "session_id", "sid-1") == nil {
		t.Fatal("setParam returned nil for an existing pane")
	}
	if got := w.layout.Root.Params["session_id"]; got != "sid-1" {
		t.Fatalf("session_id = %q, want sid-1", got)
	}
	// A spent prompt must leave no key behind: the island clears it after sending, and
	// a blank "prompt" key would still be a param the next restore has to reason about.
	w.setParam("pane-1", "prompt", "los")
	w.setParam("pane-1", "prompt", "")
	if _, ok := w.layout.Root.Params["prompt"]; ok {
		t.Fatalf("empty value must delete the key, params = %v", w.layout.Root.Params)
	}
	if w.setParam("unknown-pane", "session_id", "x") != nil {
		t.Error("setParam on a missing pane must return nil")
	}
}
