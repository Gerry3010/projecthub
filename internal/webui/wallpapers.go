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

package webui

// wallpaper is one bundled preset background image, served as a same-origin static
// asset from web/wallpapers/. Referenced from a domain.Background as "preset:<File>".
// Provenance is documented in web/wallpapers/CREDITS.md — the set is self-generated
// (plus one CC0 image), so bundling is safe under the repo's AGPL license.
type wallpaper struct {
	Key      string // stable id (== File without extension)
	Label    string // human name shown in the picker
	Category string // "Abstrakt" | "Space" | "Natur" | "Code"
	File     string // full-size file under web/wallpapers/
	Thumb    string // thumbnail under web/wallpapers/thumbs/
}

// wallpapers is the curated preset set. Ordered by category so the picker groups them
// naturally (wallpaperCategories preserves this order).
var wallpapers = []wallpaper{
	{"abstract-aurora-1", "Nordlicht", "Abstrakt", "abstract-aurora-1.jpg", "thumbs/abstract-aurora-1.jpg"},
	{"abstract-aurora-2", "Nordlicht II", "Abstrakt", "abstract-aurora-2.jpg", "thumbs/abstract-aurora-2.jpg"},
	{"abstract-obsidian-1", "Obsidian", "Abstrakt", "abstract-obsidian-1.jpg", "thumbs/abstract-obsidian-1.jpg"},
	{"abstract-obsidian-2", "Obsidian II", "Abstrakt", "abstract-obsidian-2.jpg", "thumbs/abstract-obsidian-2.jpg"},
	{"space-nebula-1", "Stiller Nebel", "Space", "space-nebula-1.jpg", "thumbs/space-nebula-1.jpg"},
	{"space-nebula-2", "Stiller Nebel II", "Space", "space-nebula-2.jpg", "thumbs/space-nebula-2.jpg"},
	{"space-nebula-3", "Stiller Nebel III", "Space", "space-nebula-3.jpg", "thumbs/space-nebula-3.jpg"},
	{"space-nebula-4", "Stiller Nebel IV", "Space", "space-nebula-4.jpg", "thumbs/space-nebula-4.jpg"},
	{"space-orbit-1", "Sonnenaufgang aus dem Orbit", "Space", "space-orbit-1.jpg", "thumbs/space-orbit-1.jpg"},
	{"space-orbit-2", "Sonnenaufgang aus dem Orbit II", "Space", "space-orbit-2.jpg", "thumbs/space-orbit-2.jpg"},
	{"nature-lake", "Bergsee", "Natur", "nature-lake.jpg", "thumbs/nature-lake.jpg"},
	{"nature-dusklake-1", "See in der Dämmerung", "Natur", "nature-dusklake-1.jpg", "thumbs/nature-dusklake-1.jpg"},
	{"nature-dusklake-2", "See in der Dämmerung II", "Natur", "nature-dusklake-2.jpg", "thumbs/nature-dusklake-2.jpg"},
	{"nature-ridges-1", "Nebelgrate", "Natur", "nature-ridges-1.jpg", "thumbs/nature-ridges-1.jpg"},
	{"nature-ridges-2", "Nebelgrate II", "Natur", "nature-ridges-2.jpg", "thumbs/nature-ridges-2.jpg"},
	{"code-circuit-1", "Platine", "Code", "code-circuit-1.jpg", "thumbs/code-circuit-1.jpg"},
	{"code-circuit-2", "Platine II", "Code", "code-circuit-2.jpg", "thumbs/code-circuit-2.jpg"},
	{"code-terminal-1", "Terminalregen", "Code", "code-terminal-1.jpg", "thumbs/code-terminal-1.jpg"},
	{"code-terminal-2", "Terminalregen II", "Code", "code-terminal-2.jpg", "thumbs/code-terminal-2.jpg"},
}

// wallpaperCategories returns the distinct categories in first-seen (curated) order,
// so the picker renders a stable, grouped list.
func wallpaperCategories() []string {
	var cats []string
	seen := map[string]bool{}
	for _, w := range wallpapers {
		if !seen[w.Category] {
			seen[w.Category] = true
			cats = append(cats, w.Category)
		}
	}
	return cats
}

// wallpapersIn returns the presets of one category, in list order.
func wallpapersIn(category string) []wallpaper {
	var out []wallpaper
	for _, w := range wallpapers {
		if w.Category == category {
			out = append(out, w)
		}
	}
	return out
}

// retiredPresets maps a preset file that has been dropped from the set to its closest
// surviving replacement. A project that still points at an old NASA/Commons preset
// keeps a wallpaper instead of falling back to a bare colour; picking a new one
// overwrites the stored reference anyway.
var retiredPresets = map[string]string{
	"space-carina.jpg":       "space-nebula-1.jpg",
	"space-pillars.jpg":      "space-nebula-2.jpg",
	"space-deepfield.jpg":    "space-nebula-1.jpg",
	"science-earthnight.jpg": "space-orbit-1.jpg",
	"science-aurora.jpg":     "abstract-aurora-1.jpg",
	"nature-mountains.jpg":   "nature-ridges-1.jpg",
	"nature-dunes.jpg":       "abstract-obsidian-1.jpg",
	"nature-forest.jpg":      "nature-ridges-2.jpg",
	"nature-autumn.jpg":      "nature-dusklake-1.jpg",
	"nature-valley.jpg":      "nature-dusklake-2.jpg",
	"sea-reef.jpg":           "nature-dusklake-1.jpg",
	"sea-sunglint.jpg":       "space-orbit-2.jpg",
	"space-orbit.jpg":        "space-orbit-1.jpg",
	"city-night.jpg":         "code-circuit-1.jpg",
}

// presetByFile finds a preset by its File field (empty struct + false when unknown),
// following retiredPresets so old references still resolve.
func presetByFile(file string) (wallpaper, bool) {
	if repl, ok := retiredPresets[file]; ok {
		file = repl
	}
	for _, w := range wallpapers {
		if w.File == file {
			return w, true
		}
	}
	return wallpaper{}, false
}

// presetURL maps a preset file to its same-origin static URL, or "" if the file is
// not a known preset (guards against arbitrary paths reaching the CSS url()).
func presetURL(file string) string {
	w, ok := presetByFile(file)
	if !ok {
		return ""
	}
	return "/web/wallpapers/" + w.File // w.File, not file: retired names resolve here
}

// presetThumbURL maps a preset file to its thumbnail URL (falls back to the full-size
// asset if the preset is somehow missing a thumb).
func presetThumbURL(file string) string {
	if w, ok := presetByFile(file); ok {
		return "/web/wallpapers/" + w.Thumb
	}
	return ""
}
