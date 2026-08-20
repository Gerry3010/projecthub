// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPresetURLKnownAndUnknown(t *testing.T) {
	if len(wallpapers) == 0 {
		t.Fatal("no bundled wallpapers")
	}
	known := wallpapers[0].File
	if got := presetURL(known); got != "/web/wallpapers/"+known {
		t.Errorf("presetURL(%q) = %q", known, got)
	}
	// Unknown / path-traversal attempts must not resolve to a URL.
	for _, bad := range []string{"", "../secret.jpg", "/etc/passwd", "nope.jpg"} {
		if got := presetURL(bad); got != "" {
			t.Errorf("presetURL(%q) = %q, want empty", bad, got)
		}
	}
}

func TestWallpaperCategoriesGrouping(t *testing.T) {
	cats := wallpaperCategories()
	if len(cats) == 0 {
		t.Fatal("no categories")
	}
	// Every wallpaper's category must appear exactly once, and wallpapersIn must
	// return only that category's items — together covering the whole set.
	total := 0
	for _, c := range cats {
		items := wallpapersIn(c)
		if len(items) == 0 {
			t.Errorf("category %q has no wallpapers", c)
		}
		for _, w := range items {
			if w.Category != c {
				t.Errorf("wallpapersIn(%q) returned %q (category %q)", c, w.File, w.Category)
			}
		}
		total += len(items)
	}
	if total != len(wallpapers) {
		t.Errorf("grouped %d items, have %d wallpapers", total, len(wallpapers))
	}
}

func TestPresetThumbURL(t *testing.T) {
	w := wallpapers[0]
	got := presetThumbURL(w.File)
	if !strings.HasPrefix(got, "/web/wallpapers/") || !strings.Contains(got, "thumb") {
		t.Errorf("presetThumbURL(%q) = %q", w.File, got)
	}
	if presetThumbURL("nope.jpg") != "" {
		t.Error("unknown thumb must be empty")
	}
}

// A project that still points at one of the retired NASA/Commons presets keeps a
// wallpaper: the reference resolves to the replacement's file, never to the old name.
func TestRetiredPresetsResolveToSurvivors(t *testing.T) {
	if len(retiredPresets) == 0 {
		t.Skip("nothing retired")
	}
	for old, repl := range retiredPresets {
		if _, ok := presetByFile(repl); !ok {
			t.Errorf("replacement %q for %q is not a bundled preset", repl, old)
		}
		if got := presetURL(old); got != "/web/wallpapers/"+repl {
			t.Errorf("presetURL(%q) = %q, want the replacement %q", old, got, repl)
		}
		if got := presetThumbURL(old); !strings.Contains(got, repl) {
			t.Errorf("presetThumbURL(%q) = %q, want the replacement's thumb", old, got)
		}
	}
}

// Every bundled preset must actually ship both files — a typo in the table would
// otherwise only show up as a blank tile in the picker.
func TestEveryPresetFileExists(t *testing.T) {
	for _, w := range wallpapers {
		for _, rel := range []string{w.File, w.Thumb} {
			if _, err := os.Stat(filepath.Join("..", "..", "web", "wallpapers", rel)); err != nil {
				t.Errorf("preset %q: %v", w.Key, err)
			}
		}
	}
}
