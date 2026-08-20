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

import "github.com/maxence-charriere/go-app/v10/pkg/app"

// TileAction is one control a tile contributes to the otherwise-fixed tile top bar.
// Tiles may only add these — never custom chrome markup — so every tile bar looks
// and behaves identically: dot · title · actions · ⋯ overflow · ✕. Primary actions
// render as icon buttons in the bar; secondary ones live in the ⋯ overflow menu.
type TileAction struct {
	SVG     string           // named line icon (icons.go) — preferred over Icon
	Icon    string           // fallback glyph (monochrome arrows like ⇆/⇅) when SVG is ""
	Label   string           // tooltip (bar) and text (overflow menu)
	OnClick app.EventHandler // invoked on click
	Danger  bool             // destructive styling in the overflow menu
	// Custom replaces the icon+label row in the overflow menu with a whole rendered
	// row — for a set of choices that belongs on ONE line (the size fractions) rather
	// than as five near-identical menu entries. Ignored in the tile bar.
	Custom app.UI
}

// glyph renders a TileAction's icon: the SVG line icon when set, else the fallback
// glyph text.
func (a TileAction) glyph(size int) app.UI {
	if a.SVG != "" {
		return icon(a.SVG, size)
	}
	return app.Text(a.Icon)
}
