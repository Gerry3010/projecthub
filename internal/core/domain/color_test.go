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

package domain

import (
	"slices"
	"testing"
)

func TestAutoColorStableAndInPalette(t *testing.T) {
	const seed = "3f1c2b9a-0000-4000-8000-abc123def456"
	first := AutoColor(seed)
	if first != AutoColor(seed) {
		t.Fatalf("AutoColor not deterministic for %q", seed)
	}
	if !slices.Contains(DefaultPalette, first) {
		t.Fatalf("AutoColor returned %q, not in DefaultPalette", first)
	}
	if AutoColor("") != DefaultAccent {
		t.Fatalf("empty seed = %q, want DefaultAccent %q", AutoColor(""), DefaultAccent)
	}
}

func TestProjectRefAccentColorFallback(t *testing.T) {
	// Explicit color wins.
	r := ProjectRef{ID: "x", Color: ColorViolet}
	if r.AccentColor() != ColorViolet {
		t.Fatalf("explicit color ignored: %q", r.AccentColor())
	}
	// Empty color falls back to the id-derived auto color (older projects).
	r2 := ProjectRef{ID: "some-id"}
	if got := r2.AccentColor(); got != AutoColor("some-id") {
		t.Fatalf("fallback = %q, want %q", got, AutoColor("some-id"))
	}
}
