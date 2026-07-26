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
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// railView is the permanent left quick-switcher spine: Nexus → home, one accent
// tile per project (click to open/switch), then + new and ⚙ settings. It frames both
// the home view and an open workspace, so ProjectHub reads as a suite, not a page.
func (r *Root) railView() app.UI {
	homeCls := "ph-rail-home"
	if r.selected == nil {
		homeCls += " ph-rail-active"
	}
	return app.Aside().Class("ph-rail").Body(
		app.Button().Class(homeCls).Title("Projekte (Home)").
			OnClick(func(ctx app.Context, _ app.Event) {
				if r.selected != nil {
					r.closeProject(ctx)
				}
			}).
			Body(nexusIcon(r.accentColor(), 26)),

		app.Div().Class("ph-rail-sep"),

		app.Div().Class("ph-rail-projects").Body(
			app.Range(r.projects).Slice(func(i int) app.UI {
				p := r.projects[i]
				cls := "ph-rail-dot"
				if r.selected != nil && r.selected.ID == p.ID {
					cls += " ph-rail-dot-active"
				}
				return app.Button().Class(cls).Title(p.Title).
					Style("--dot", p.AccentColor()).
					OnClick(func(ctx app.Context, _ app.Event) {
						ref := p
						r.selected = &ref
					}).
					Text(projectInitials(p.Title))
			}),
		),

		app.Div().Class("ph-rail-sep"),

		app.Button().Class("ph-rail-btn").Title("Neues Projekt").
			OnClick(func(ctx app.Context, _ app.Event) {
				if r.selected != nil {
					r.closeProject(ctx)
				}
			}).
			Text("+"),
		app.Button().Class("ph-rail-btn").Title("Einstellungen").
			OnClick(func(ctx app.Context, _ app.Event) { r.showSettings = true }).
			Body(icon("gear", 18)),
	)
}

// projectInitials returns up to two uppercase initials for a rail tile: first letters
// of the first two words, or the first two letters of a single word.
func projectInitials(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "•"
	}
	fields := strings.Fields(title)
	if len(fields) >= 2 {
		return strings.ToUpper(firstRune(fields[0]) + firstRune(fields[1]))
	}
	r := []rune(title)
	if len(r) == 1 {
		return strings.ToUpper(string(r[0]))
	}
	return strings.ToUpper(string(r[:2]))
}

func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}
