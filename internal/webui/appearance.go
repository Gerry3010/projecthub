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
	"context"
	"strconv"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// appearancePanel is the background/glass editor: pick a wallpaper color or image,
// tune panel translucency/blur/dim, and choose whether it applies to just this
// project or as the account-wide default. Changes preview live and persist E2E.
func (w *Workspace) appearancePanel() app.UI {
	bg := w.previewBg()
	scopeBtn := func(label, scope string) app.UI {
		cls := "ph-scope-btn"
		if w.apprScope == scope {
			cls += " ph-scope-on"
		}
		return app.Button().Class(cls).Text(label).OnClick(func(ctx app.Context, _ app.Event) {
			w.apprScope = scope
			w.refreshBgImage(ctx)
		})
	}
	return app.Div().Class("ph-appr").Body(
		app.Div().Class("ph-appr-head").Body(
			app.Strong().Text("Hintergrund"),
			app.Div().Class("ph-spacer"),
			app.Button().Class("ph-tile-btn").Text("✕").OnClick(func(ctx app.Context, _ app.Event) { w.apprOpen = false }),
		),
		app.Div().Class("ph-row").Body(scopeBtn("Dieses Projekt", "project"), scopeBtn("Account-Default", "account")),

		app.Label().Class("ph-appr-lbl").Text("Farbe"),
		app.Div().Class("ph-row").Body(
			app.Input().Type("color").Value(bgColor(bg)).
				OnInput(func(ctx app.Context, e app.Event) { w.setBgColor(ctx, ctx.JSSrc().Get("value").String()) }),
			app.Button().Class("ph-link").Text("zurücksetzen").OnClick(func(ctx app.Context, _ app.Event) { w.clearBg(ctx) }),
		),

		app.Label().Class("ph-appr-lbl").Text("Bild (lokaler Pfad)"),
		app.Input().Type("text").Placeholder("/pfad/zum/wallpaper.jpg").Value(bgLocalPath(bg)).
			OnChange(func(ctx app.Context, e app.Event) { w.setBgImagePath(ctx, ctx.JSSrc().Get("value").String()) }),

		w.slider("Transparenz der Panels", "alpha", 0.3, 1, 0.02, bgAlpha(bg)),
		w.slider("Blur (px)", "blur", 0, 30, 1, float64(bgBlur(bg))),
		w.slider("Abdunkeln", "dim", 0, 0.8, 0.02, bgDim(bg)),

		app.P().Class("ph-muted ph-appr-hint").Text("Panels sind durchscheinend & geblurrt — das Bild scheint dahinter durch."),
	)
}

func (w *Workspace) slider(label, kind string, min, max, step, val float64) app.UI {
	return app.Div().Class("ph-appr-slider").Body(
		app.Label().Class("ph-appr-lbl").Text(label),
		app.Input().Type("range").
			Attr("min", min).Attr("max", max).Attr("step", step).Value(val).
			OnInput(func(ctx app.Context, e app.Event) {
				f, _ := strconv.ParseFloat(ctx.JSSrc().Get("value").String(), 64)
				w.setBgNum(ctx, kind, f)
			}),
	)
}

// ─── editing ──────────────────────────────────────────────────────────────────

// scopeBg returns the editable background for the active scope, creating one if the
// scope has none yet.
func (w *Workspace) scopeBg() *domain.Background {
	if w.apprScope == "account" {
		if w.accountBg == nil {
			w.accountBg = &domain.Background{Alpha: 1}
		}
		return w.accountBg
	}
	if w.Ref.Background == nil {
		w.Ref.Background = &domain.Background{Alpha: 1}
	}
	return w.Ref.Background
}

// previewBg is what the workspace currently shows: the scope being edited, or the
// account default when a project has no override.
func (w *Workspace) previewBg() *domain.Background {
	if w.apprScope == "account" {
		return w.accountBg
	}
	if w.Ref.Background != nil {
		return w.Ref.Background
	}
	return w.accountBg
}

func (w *Workspace) setBgColor(ctx app.Context, color string) {
	bg := w.scopeBg()
	bg.Type, bg.Color = "color", color
	applyBackground(bg, "")
	w.persistBgSoon()
}

func (w *Workspace) setBgImagePath(ctx app.Context, path string) {
	bg := w.scopeBg()
	if path == "" {
		bg.Type, bg.Image = "", ""
	} else {
		bg.Type, bg.Image = "image", "file:"+path
	}
	w.refreshBgImage(ctx)
	w.persistBgSoon()
}

func (w *Workspace) setBgNum(ctx app.Context, kind string, v float64) {
	bg := w.scopeBg()
	switch kind {
	case "alpha":
		bg.Alpha = v
	case "blur":
		bg.Blur = int(v)
	case "dim":
		bg.Dim = v
	}
	applyBackground(bg, w.bgImageURL)
	w.persistBgSoon()
}

func (w *Workspace) clearBg(ctx app.Context) {
	if w.apprScope == "account" {
		w.accountBg = nil
	} else {
		w.Ref.Background = nil
	}
	w.bgImageURL = ""
	applyBackground(w.previewBg(), w.bgImageURL)
	w.persistBgSoon()
}

// refreshBgImage re-resolves the wallpaper image (async fetch/decrypt) then applies.
func (w *Workspace) refreshBgImage(ctx app.Context) {
	bg := w.previewBg()
	ctx.Async(func() {
		url := resolveBgImageURL(w.Store, w.Native, bg)
		ctx.Dispatch(func(ctx app.Context) {
			w.bgImageURL = url
			applyBackground(bg, url)
		})
	})
}

// ─── persistence (debounced) ──────────────────────────────────────────────────

func (w *Workspace) persistBgSoon() {
	if w.bgTimer != nil {
		w.bgTimer.Stop()
	}
	scope, projectBg, accountBg, id := w.apprScope, w.Ref.Background, w.accountBg, w.Ref.ID
	w.bgTimer = time.AfterFunc(400*time.Millisecond, func() {
		if scope == "account" {
			_ = w.Store.SetBackground(context.Background(), accountBg)
		} else {
			_ = w.Store.SetProjectBackground(context.Background(), id, projectBg)
		}
	})
}

// ─── accessors (nil-safe) ─────────────────────────────────────────────────────

func bgColor(bg *domain.Background) string {
	if bg != nil && bg.Color != "" {
		return bg.Color
	}
	return "#181b22"
}
func bgLocalPath(bg *domain.Background) string {
	if bg != nil && bg.Type == "image" {
		if p, ok := trimFilePrefix(bg.Image); ok {
			return p
		}
	}
	return ""
}
func bgAlpha(bg *domain.Background) float64 {
	if bg == nil || bg.Alpha == 0 {
		return 1
	}
	return bg.Alpha
}
func bgBlur(bg *domain.Background) int {
	if bg == nil {
		return 0
	}
	return bg.Blur
}
func bgDim(bg *domain.Background) float64 {
	if bg == nil {
		return 0
	}
	return bg.Dim
}
func trimFilePrefix(s string) (string, bool) {
	const p = "file:"
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):], true
	}
	return "", false
}
