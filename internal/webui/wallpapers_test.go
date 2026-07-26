// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package webui

import (
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
