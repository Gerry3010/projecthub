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
// Provenance + licenses are documented in web/wallpapers/CREDITS.md — every image is
// public domain / CC0 so bundling is safe under the repo's AGPL license.
type wallpaper struct {
	Key      string // stable id (== File without extension)
	Label    string // human name shown in the picker
	Category string // "Space" | "Wissenschaft" | "Natur" | "Meer"
	File     string // full-size file under web/wallpapers/
	Thumb    string // thumbnail under web/wallpapers/thumbs/
}

// wallpapers is the curated preset set. Ordered by category so the picker groups them
// naturally (wallpaperCategories preserves this order).
var wallpapers = []wallpaper{
	{"space-carina", "Carina-Nebel", "Space", "space-carina.jpg", "thumbs/space-carina.jpg"},
	{"space-pillars", "Säulen der Schöpfung", "Space", "space-pillars.jpg", "thumbs/space-pillars.jpg"},
	{"space-deepfield", "Webb Deep Field", "Space", "space-deepfield.jpg", "thumbs/space-deepfield.jpg"},
	{"science-earthnight", "Erde bei Nacht", "Wissenschaft", "science-earthnight.jpg", "thumbs/science-earthnight.jpg"},
	{"science-aurora", "Polarlicht (ISS)", "Wissenschaft", "science-aurora.jpg", "thumbs/science-aurora.jpg"},
	{"nature-mountains", "Berge von oben", "Natur", "nature-mountains.jpg", "thumbs/nature-mountains.jpg"},
	{"nature-dunes", "Namib-Wüste", "Natur", "nature-dunes.jpg", "thumbs/nature-dunes.jpg"},
	{"nature-forest", "Nebelwald", "Natur", "nature-forest.jpg", "thumbs/nature-forest.jpg"},
	{"nature-lake", "Bergsee", "Natur", "nature-lake.jpg", "thumbs/nature-lake.jpg"},
	{"nature-autumn", "Herbstwald", "Natur", "nature-autumn.jpg", "thumbs/nature-autumn.jpg"},
	{"nature-valley", "Grünes Tal", "Natur", "nature-valley.jpg", "thumbs/nature-valley.jpg"},
	{"sea-reef", "Great Barrier Reef", "Meer", "sea-reef.jpg", "thumbs/sea-reef.jpg"},
	{"sea-sunglint", "Ozean im Sonnenlicht", "Meer", "sea-sunglint.jpg", "thumbs/sea-sunglint.jpg"},
	{"city-night", "Stadt bei Nacht", "Stadt", "city-night.jpg", "thumbs/city-night.jpg"},
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

// presetByFile finds a preset by its File field (empty struct + false when unknown).
func presetByFile(file string) (wallpaper, bool) {
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
	if _, ok := presetByFile(file); !ok {
		return ""
	}
	return "/web/wallpapers/" + file
}

// presetThumbURL maps a preset file to its thumbnail URL (falls back to the full-size
// asset if the preset is somehow missing a thumb).
func presetThumbURL(file string) string {
	if w, ok := presetByFile(file); ok {
		return "/web/wallpapers/" + w.Thumb
	}
	return ""
}
