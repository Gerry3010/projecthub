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
	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// appearancePanel is the per-project appearance popover (opened from the workspace
// toolbar's 🎨 button): a header plus the shared bgEditor, scoped to this project by
// default. The same bgEditor also powers the global Settings "Erscheinungsbild" tab.
func (w *Workspace) appearancePanel() app.UI {
	scope := w.apprScope
	if scope == "" {
		scope = "project"
	}
	return app.Div().Class("ph-appr").Body(
		app.Div().Class("ph-appr-head").Body(
			app.Strong().Text("Hintergrund"),
			app.Div().Class("ph-spacer"),
			app.Button().Class("ph-tile-btn").Text("✕").
				OnClick(func(ctx app.Context, _ app.Event) { w.apprOpen = false }),
		),
		&bgEditor{
			Store:        w.Store,
			Native:       w.Native,
			ProjectOpen:  true,
			ProjectID:    w.Ref.ID,
			Account:      w.accountBg,
			Project:      w.Ref.Background,
			SetAccount:   func(bg *domain.Background) { w.accountBg = bg },
			SetProject:   func(bg *domain.Background) { w.Ref.Background = bg },
			InitialScope: scope,
		},
	)
}
