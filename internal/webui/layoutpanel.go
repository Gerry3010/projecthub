// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// The layout manager: the toolbar popover that arranges the workspace, plus the
// little diagrams it shares with the tile menu. One picture vocabulary throughout —
// a rounded rectangle cut the way the panes are cut — so the same shape means the
// same thing whether you meet it in the manager, in a tile's ⋯ menu, or as a snap
// guide while dragging a divider.

package webui

import (
	"fmt"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// diagram renders cells (x, y, w, h in 0..1) as an SVG plan of a workspace. The
// outline is the window; the filled cells are the panes.
func diagram(w, h int, cells [][4]float64) app.UI {
	const pad = 1.0 // keeps strokes inside the viewBox
	var b strings.Builder
	fmt.Fprintf(&b, `<svg width="%d" height="%d" viewBox="0 0 %d %d" aria-hidden="true" focusable="false" class="ph-diag">`, w, h, w, h)
	iw, ih := float64(w)-2*pad, float64(h)-2*pad
	fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="2.5" class="ph-diag-frame"/>`, pad, pad, iw, ih)
	for _, c := range cells {
		x := pad + c[0]*iw + 1
		y := pad + c[1]*ih + 1
		cw := c[2]*iw - 2
		ch := c[3]*ih - 2
		if cw <= 0 || ch <= 0 {
			continue
		}
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="1.5" class="ph-diag-cell"/>`, x, y, cw, ch)
	}
	b.WriteString(`</svg>`)
	return app.Raw(b.String())
}

// fracGlyph is the same picture at chip size: this pane's share of its split,
// highlighted, with the neighbour left as outline. dir "col" cuts horizontally.
func fracGlyph(frac float64, dir string) app.UI {
	cells := [][4]float64{{0, 0, frac, 1}}
	if dir == "col" {
		cells = [][4]float64{{0, 0, 1, frac}}
	}
	return diagram(22, 15, cells)
}

// tplGlyph draws a built-in arrangement for the picker.
func tplGlyph(t layoutTemplate) app.UI { return diagram(46, 32, t.Cells) }

// ─── the panel ────────────────────────────────────────────────────────────────

// layoutPanel is the workspace-arrangement popover: apply a built-in arrangement,
// even out the splits, or save/restore your own. Nothing here closes a tile except
// restoring a saved layout, which by definition replaces what is open.
func (w *Workspace) layoutPanel() app.UI {
	tiles := len(leaves(w.layout.Root))
	tpls := layoutTemplates()
	return app.Div().Class("ph-appr ph-layout-panel").Body(
		app.Div().Class("ph-appr-head").Body(
			app.Strong().Text("Layout"),
			app.Div().Class("ph-spacer"),
			app.Button().Class("ph-tile-btn").Text("✕").
				OnClick(func(ctx app.Context, _ app.Event) { w.layoutOpen = false }),
		),

		app.Span().Class("ph-eyebrow").Text("Anordnung"),
		app.Div().Class("ph-tpl-grid").Body(
			app.Range(tpls).Slice(func(i int) app.UI {
				t := tpls[i]
				cls := "ph-tpl"
				if t.Slots > tiles {
					cls += " is-dim" // more places than tiles: it will be trimmed to fit
				}
				return app.Button().Class(cls).Title(t.Label).
					OnClick(func(ctx app.Context, _ app.Event) { w.applyLayoutTemplate(t) }).
					Body(tplGlyph(t), app.Span().Class("ph-tpl-label").Text(t.Label))
			}),
		),

		app.Div().Class("ph-layout-acts").Body(
			app.Button().Class("ph-scope-btn").Text("Ausgleichen").
				Title("Alle Teiler auf die Hälfte setzen").
				OnClick(func(ctx app.Context, _ app.Event) { w.balanceLayout() }),
			app.Button().Class("ph-scope-btn").Text("Zurücksetzen").
				Title("Alle Tiles gleichmäßig untereinander — schließt nichts").
				OnClick(func(ctx app.Context, _ app.Event) { w.resetLayout() }),
		),

		app.Span().Class("ph-eyebrow").Text("Eigene Layouts"),
		app.Div().Class("ph-layout-save").Body(
			app.Input().Type("text").Placeholder("Name").
				Value(w.presetName).
				OnInput(func(ctx app.Context, e app.Event) { w.presetName = ctx.JSSrc().Get("value").String() }),
			app.Button().Class("ph-btn").Text("Sichern").
				OnClick(func(ctx app.Context, _ app.Event) { w.saveLayoutPreset() }),
		),
		app.If(len(w.layout.Presets) == 0, func() app.UI {
			return app.P().Class("ph-appr-hint ph-muted").
				Text("Noch keins gesichert. Ein gesichertes Layout merkt sich, welche Tiles wie angeordnet waren.")
		}).Else(func() app.UI {
			return app.Div().Class("ph-preset-list").Body(
				app.Range(w.layout.Presets).Slice(func(i int) app.UI {
					p := w.layout.Presets[i]
					return app.Div().Class("ph-preset").Body(
						app.Button().Class("ph-preset-apply").Text(p.Name).
							Title("Dieses Layout herstellen — ersetzt die offenen Tiles").
							OnClick(func(ctx app.Context, _ app.Event) { w.applyLayoutPreset(p.ID) }),
						app.Button().Class("ph-tile-btn").Text("✕").Title("Layout löschen").
							OnClick(func(ctx app.Context, _ app.Event) { w.deleteLayoutPreset(p.ID) }),
					)
				}),
			)
		}),
	)
}

// ─── actions ──────────────────────────────────────────────────────────────────

// applyLayoutTemplate rearranges the open tiles into a built-in arrangement.
func (w *Workspace) applyLayoutTemplate(t layoutTemplate) {
	root := applyTemplate(w.layout.Root, t)
	if root == nil {
		return
	}
	w.layout.Root = root
	w.layoutOpen = false
	w.persistSoon()
}

// balanceLayout evens out every divider. The splits keep their identity here, so the
// CSS variable has to be written by hand — a re-render alone would not move them.
func (w *Workspace) balanceLayout() {
	balanceRatios(w.layout.Root)
	for _, id := range splitIDs(w.layout.Root) {
		setSplitRatioJS(id, 0.5)
	}
	w.persistSoon()
}

// resetLayout stacks every tile evenly — the arrangement a fresh workspace grows
// into. It closes nothing.
func (w *Workspace) resetLayout() {
	tiles := leaves(w.layout.Root)
	if len(tiles) == 0 {
		return
	}
	w.layout.Root = stack(tiles)
	w.layoutOpen = false
	w.persistSoon()
}

func (w *Workspace) saveLayoutPreset() {
	name := strings.TrimSpace(w.presetName)
	if name == "" {
		w.status = "Gib dem Layout einen Namen"
		return
	}
	if w.layout.Root == nil {
		return
	}
	w.layout.Presets = savePreset(w.layout.Presets, name, w.layout.Root)
	w.presetName = ""
	w.status = "Layout „" + name + "“ gesichert"
	w.persistSoon()
}

// applyLayoutPreset restores a saved arrangement. It replaces what is open — a saved
// layout describes a whole workspace, not just its proportions.
func (w *Workspace) applyLayoutPreset(id string) {
	for _, p := range w.layout.Presets {
		if p.ID != id || p.Root == nil {
			continue
		}
		w.layout.Root = snapshotTree(p.Root)
		w.layoutOpen = false
		w.persistSoon()
		return
	}
}

func (w *Workspace) deleteLayoutPreset(id string) {
	w.layout.Presets = deletePreset(w.layout.Presets, id)
	w.persistSoon()
}
