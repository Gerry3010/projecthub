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

import (
	"fmt"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// nexusSVG returns ProjectHub's "Nexus" hub mark as an inline SVG string: a central
// node with six spokes to outer nodes ("everything converges here"). It is tinted
// with color (any CSS color) and drawn at size×size pixels on a 48-unit grid.
//
// color is always either a built-in palette hex or the value of an <input
// type="color"> (always "#rrggbb"), so it is safe to interpolate into the markup
// that app.Raw injects verbatim.
func nexusSVG(color string, size int) string {
	return fmt.Sprintf(
		`<svg width="%[1]d" height="%[1]d" viewBox="0 0 48 48" fill="none" aria-hidden="true" focusable="false">`+
			`<g stroke="%[2]s" stroke-width="2.4" stroke-linecap="round">`+
			`<line x1="24" y1="24" x2="39" y2="24"/><line x1="24" y1="24" x2="31.5" y2="11"/>`+
			`<line x1="24" y1="24" x2="16.5" y2="11"/><line x1="24" y1="24" x2="9" y2="24"/>`+
			`<line x1="24" y1="24" x2="16.5" y2="37"/><line x1="24" y1="24" x2="31.5" y2="37"/></g>`+
			`<g fill="%[2]s">`+
			`<circle cx="39" cy="24" r="3"/><circle cx="31.5" cy="11" r="3"/><circle cx="16.5" cy="11" r="3"/>`+
			`<circle cx="9" cy="24" r="3"/><circle cx="16.5" cy="37" r="3"/><circle cx="31.5" cy="37" r="3"/>`+
			`<circle cx="24" cy="24" r="5.6"/></g></svg>`,
		size, color)
}

// nexusIcon wraps nexusSVG as a go-app node. Changing color re-renders it, since
// app.Raw diffs on the raw string.
func nexusIcon(color string, size int) app.UI {
	return app.Raw(nexusSVG(color, size))
}

// swatchBar renders the preset palette as one-click swatches plus a native color
// well for a custom pick. current is the active color (highlighted); onPick fires
// for a preset; onCustom handles the <input type="color"> change event.
func swatchBar(current string, onPick func(string) app.EventHandler, onCustom app.EventHandler) app.UI {
	return app.Div().Class("ph-theme").Body(
		app.Range(domain.DefaultPalette).Slice(func(i int) app.UI {
			c := domain.DefaultPalette[i]
			return app.Button().Class("ph-swatch").
				Style("background", c).
				Attr("title", c).
				Attr("type", "button").
				Aria("pressed", eqColor(current, c)).
				Aria("label", "Farbe "+c).
				OnClick(onPick(c))
		}),
		app.Label().Class("ph-swatch ph-swatch-custom").Attr("title", "Eigene Farbe…").
			Style("background", current).Body(
			app.Input().Type("color").Value(current).OnChange(onCustom),
		),
	)
}

// eqColor compares two CSS hex colors case-insensitively.
func eqColor(a, b string) bool { return strings.EqualFold(a, b) }
