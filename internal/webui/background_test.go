// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package webui

import (
	"testing"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// A preset background resolves to a same-origin static URL without touching the store
// or sidecar (nil is safe), so the CSS gets a plain /web/wallpapers/ path.
func TestResolveBgImageURLPreset(t *testing.T) {
	file := wallpapers[0].File
	bg := &domain.Background{Type: "image", Image: "preset:" + file}
	if got := resolveBgImageURL(nil, nil, bg); got != "/web/wallpapers/"+file {
		t.Errorf("resolveBgImageURL preset = %q", got)
	}
	// An unknown preset resolves to empty (guards the CSS url()).
	bad := &domain.Background{Type: "image", Image: "preset:../evil.jpg"}
	if got := resolveBgImageURL(nil, nil, bad); got != "" {
		t.Errorf("unknown preset should be empty, got %q", got)
	}
	// Non-image backgrounds resolve to empty.
	if got := resolveBgImageURL(nil, nil, &domain.Background{Type: "color", Color: "#fff"}); got != "" {
		t.Errorf("color bg should have no image url, got %q", got)
	}
}
