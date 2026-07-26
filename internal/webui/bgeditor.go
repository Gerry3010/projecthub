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
	"github.com/Gerry3010/projecthub/internal/core/store"
	"github.com/Gerry3010/projecthub/internal/native/nativeclient"
)

// bgEditor is the shared background editor used by BOTH the global Settings
// "Erscheinungsbild" tab and the per-project appearance panel: it picks a wallpaper
// (bundled preset, local image path, or flat color), tunes panel glass
// (translucency/blur/dim) and the whole-window deck opacity, and toggles opt-in window
// transparency. Edits preview live (applyBackground) and persist E2E via the store.
//
// The two scopes' backgrounds are owned by the parent (its source of truth). The
// editor reads them via Account/Project and hands back a (possibly newly created)
// pointer through SetAccount/SetProject so the parent keeps it for its own re-render
// and applyBackground on other views.
type bgEditor struct {
	app.Compo

	Store  *store.Store
	Native *nativeclient.Client

	ProjectOpen bool   // whether the "project" scope is offered
	ProjectID   string // for SetProjectBackground

	Account    *domain.Background
	Project    *domain.Background
	SetAccount func(*domain.Background)
	SetProject func(*domain.Background)

	InitialScope string // seed the scope on first mount ("project" | "account")

	scope      string
	bgImageURL string
	bgTimer    *time.Timer
}

func (e *bgEditor) OnMount(ctx app.Context) {
	e.scope = e.InitialScope
	if e.scope != "project" || !e.ProjectOpen {
		if e.ProjectOpen {
			e.scope = "project"
		} else {
			e.scope = "account"
		}
	}
	e.refreshImage(ctx)
}

// curScope resolves the effective scope (falls back to account when no project).
func (e *bgEditor) curScope() string {
	if e.scope == "project" && e.ProjectOpen {
		return "project"
	}
	return "account"
}

// editable returns the current scope's background, creating one (and handing it to the
// parent) when the scope has none yet.
func (e *bgEditor) editable() *domain.Background {
	if e.curScope() == "project" {
		if e.Project == nil {
			e.Project = &domain.Background{Alpha: 1}
			if e.SetProject != nil {
				e.SetProject(e.Project)
			}
		}
		return e.Project
	}
	if e.Account == nil {
		e.Account = &domain.Background{Alpha: 1}
		if e.SetAccount != nil {
			e.SetAccount(e.Account)
		}
	}
	return e.Account
}

// preview is what the app currently shows for the active scope (a project with no
// override falls back to the account default).
func (e *bgEditor) preview() *domain.Background {
	if e.curScope() == "project" && e.Project != nil {
		return e.Project
	}
	return e.Account
}

// ─── edits ──────────────────────────────────────────────────────────────────

func (e *bgEditor) switchScope(ctx app.Context, scope string) {
	e.scope = scope
	e.refreshImage(ctx)
}

func (e *bgEditor) setColor(ctx app.Context, color string) {
	bg := e.editable()
	bg.Type, bg.Color, bg.Image = "color", color, ""
	e.bgImageURL = ""
	applyBackground(bg, "")
	e.persistSoon()
}

func (e *bgEditor) setPreset(ctx app.Context, file string) {
	bg := e.editable()
	bg.Type, bg.Image = "image", "preset:"+file
	e.refreshImage(ctx)
	e.persistSoon()
}

func (e *bgEditor) setImagePath(ctx app.Context, path string) {
	bg := e.editable()
	if path == "" {
		bg.Type, bg.Image = "", ""
	} else {
		bg.Type, bg.Image = "image", "file:"+path
	}
	e.refreshImage(ctx)
	e.persistSoon()
}

func (e *bgEditor) setNum(ctx app.Context, kind string, v float64) {
	bg := e.editable()
	switch kind {
	case "alpha":
		bg.Alpha = v
	case "blur":
		bg.Blur = int(v)
	case "dim":
		bg.Dim = v
	case "app":
		bg.AppAlpha = v
	}
	applyBackground(bg, e.bgImageURL)
	e.persistSoon()
}

func (e *bgEditor) clear(ctx app.Context) {
	if e.curScope() == "project" {
		e.Project = nil
		if e.SetProject != nil {
			e.SetProject(nil)
		}
	} else {
		e.Account = nil
		if e.SetAccount != nil {
			e.SetAccount(nil)
		}
	}
	e.bgImageURL = ""
	applyBackground(e.preview(), "")
	e.persistSoon()
}

// refreshImage re-resolves the wallpaper image (async fetch/decrypt for local/vault
// images; instant for presets) then applies it.
func (e *bgEditor) refreshImage(ctx app.Context) {
	bg := e.preview()
	st, nc := e.Store, e.Native
	ctx.Async(func() {
		url := resolveBgImageURL(st, nc, bg)
		ctx.Dispatch(func(ctx app.Context) {
			e.bgImageURL = url
			applyBackground(bg, url)
		})
	})
}

// persistSoon debounces the E2E save so slider drags don't hammer the vault.
func (e *bgEditor) persistSoon() {
	if e.bgTimer != nil {
		e.bgTimer.Stop()
	}
	scope, acc, prj, id, st := e.curScope(), e.Account, e.Project, e.ProjectID, e.Store
	e.bgTimer = time.AfterFunc(400*time.Millisecond, func() {
		if scope == "project" {
			_ = st.SetProjectBackground(context.Background(), id, prj)
		} else {
			_ = st.SetBackground(context.Background(), acc)
		}
	})
}

// ─── render ─────────────────────────────────────────────────────────────────

func (e *bgEditor) Render() app.UI {
	bg := e.preview()
	return app.Div().Class("ph-bgedit").Body(
		app.If(e.ProjectOpen, func() app.UI {
			return app.Div().Class("ph-seg ph-bgedit-scope").Body(
				e.scopeBtn("account", "Account-Standard"),
				e.scopeBtn("project", "Dieses Projekt"),
			)
		}),
		e.wallpaperPicker(bg),

		app.P().Class("ph-eyebrow ph-settings-gap").Text("Eigenes Bild / Farbe"),
		app.Div().Class("ph-bgedit-row").Body(
			app.Input().Type("text").Class("ph-set-input ph-bgedit-path").Placeholder("/pfad/zum/bild.jpg").
				Value(bgLocalPath(bg)).
				OnChange(func(ctx app.Context, _ app.Event) { e.setImagePath(ctx, ctx.JSSrc().Get("value").String()) }),
			app.Input().Type("color").Class("ph-bgedit-color").Value(bgColor(bg)).
				OnInput(func(ctx app.Context, _ app.Event) { e.setColor(ctx, ctx.JSSrc().Get("value").String()) }),
			app.Button().Class("ph-link").Text("zurücksetzen").
				OnClick(func(ctx app.Context, _ app.Event) { e.clear(ctx) }),
		),

		app.P().Class("ph-eyebrow ph-settings-gap").Text("Glas & Fenster"),
		e.slider("Transparenz der Panels", "alpha", 0.3, 1, 0.02, bgAlpha(bg)),
		e.slider("Blur (px)", "blur", 0, 30, 1, float64(bgBlur(bg))),
		e.slider("Abdunkeln", "dim", 0, 0.8, 0.02, bgDim(bg)),
		e.slider("Fenster-Deckkraft", "app", 0.2, 1, 0.02, bgAppAlpha(bg)),

		e.transparencyToggle(),
	)
}

func (e *bgEditor) scopeBtn(scope, label string) app.UI {
	cls := "ph-seg-btn"
	if e.curScope() == scope {
		cls += " ph-seg-on"
	}
	return app.Button().Class(cls).Text(label).
		OnClick(func(ctx app.Context, _ app.Event) { e.switchScope(ctx, scope) })
}

func (e *bgEditor) wallpaperPicker(bg *domain.Background) app.UI {
	curImg := bgImage(bg)
	return app.Div().Class("ph-wp").Body(
		app.P().Class("ph-eyebrow").Text("Wallpaper"),
		app.Div().Class("ph-wp-grid").Body(
			app.Button().Class(wpTileCls(curImg == "")).Title("Kein Wallpaper").
				OnClick(func(ctx app.Context, _ app.Event) { e.setImagePath(ctx, "") }).
				Body(app.Span().Class("ph-wp-none").Text("Kein")),
		),
		app.Range(wallpaperCategories()).Slice(func(i int) app.UI {
			cat := wallpaperCategories()[i]
			items := wallpapersIn(cat)
			return app.Div().Class("ph-wp-cat").Body(
				app.P().Class("ph-wp-cat-title").Text(cat),
				app.Div().Class("ph-wp-grid").Body(
					app.Range(items).Slice(func(j int) app.UI {
						wp := items[j]
						active := curImg == "preset:"+wp.File
						return app.Button().Class(wpTileCls(active)).Title(wp.Label).
							OnClick(func(ctx app.Context, _ app.Event) { e.setPreset(ctx, wp.File) }).
							Body(
								app.Img().Class("ph-wp-thumb").Src(presetThumbURL(wp.File)).
									Alt(wp.Label).Attr("loading", "lazy"),
								app.Span().Class("ph-wp-label").Text(wp.Label),
							)
					}),
				),
			)
		}),
	)
}

func (e *bgEditor) slider(label, kind string, min, max, step, val float64) app.UI {
	return app.Div().Class("ph-appr-slider").Body(
		app.Label().Class("ph-appr-lbl").Text(label),
		app.Input().Type("range").
			Attr("min", min).Attr("max", max).Attr("step", step).Value(val).
			OnInput(func(ctx app.Context, _ app.Event) {
				f, _ := strconv.ParseFloat(ctx.JSSrc().Get("value").String(), 64)
				e.setNum(ctx, kind, f)
			}),
	)
}

// transparencyToggle is the opt-in window-transparency switch (desktop only; hidden in
// the hosted browser build). Flipping it relaunches the app — see setWindowTransparency.
func (e *bgEditor) transparencyToggle() app.UI {
	on, available := windowTransparency()
	if !available {
		return app.Div()
	}
	return app.Div().Class("ph-bgedit-transp").Body(
		app.P().Class("ph-eyebrow ph-settings-gap").Text("Fenster-Transparenz"),
		app.Label().Class("ph-check").Body(
			app.Input().Type("checkbox").Checked(on).
				OnChange(func(ctx app.Context, _ app.Event) { setWindowTransparency(ctx.JSSrc().Get("checked").Bool()) }),
			app.Text("Desktop durchscheinen lassen (Neustart)"),
		),
		app.P().Class("ph-settings-note").Text("Lässt bei „Fenster-Deckkraft“ < 100 % den Desktop hinter ProjectHub durchscheinen. Die App startet dafür neu."),
	)
}

// wpTileCls is the wallpaper-tile class with an active modifier.
func wpTileCls(active bool) string {
	if active {
		return "ph-wp-tile ph-wp-tile-active"
	}
	return "ph-wp-tile"
}

// ─── nil-safe accessors (shared with the workspace appearance panel) ──────────

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
func bgImage(bg *domain.Background) string {
	if bg == nil {
		return ""
	}
	return bg.Image
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
func bgAppAlpha(bg *domain.Background) float64 {
	if bg == nil || bg.AppAlpha == 0 {
		return 1
	}
	return bg.AppAlpha
}
func trimFilePrefix(s string) (string, bool) {
	const p = "file:"
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):], true
	}
	return "", false
}
