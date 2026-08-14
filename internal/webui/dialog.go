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

// ─── tile dialogs ──────────────────────────────────────────────────────────────
//
// A tile is a small, clipped box (.ph-tile has overflow:hidden), so anything that
// needs room — an edit form, a confirmation, a picker — used to have to squeeze
// itself inline and was cut off in a narrow pane. tileDialog gives tiles a way out:
// the markup still lives inside the tile's tree (so it keeps the tile's state and
// event handlers), but it is positioned fixed to the VIEWPORT, which takes it out of
// the clipping box and lets it be as large as it needs to be. .ph-tile creates no
// containing block (no transform/filter on the element itself — the glass sits on
// ::before precisely so this keeps working), so "fixed" really means the viewport.
//
// Usage from a tile: render tileDialog(...) at the end of Render() while some
// "which dialog is open" field is set, and clear that field in onClose.

// dialogAction is one button in the dialog's footer.
type dialogAction struct {
	Label   string
	OnClick app.EventHandler
	Primary bool // accent-filled (the confirming action)
	Danger  bool // destructive (delete)
}

// tileDialog renders a modal dialog for a tile: a dimmed backdrop that closes on
// click, a titled card, the caller's body, and a footer of actions. onClose is also
// wired to the ✕ and to Escape anywhere in the card.
func tileDialog(title string, onClose app.EventHandler, actions []dialogAction, body ...app.UI) app.UI {
	closeOnEsc := func(ctx app.Context, e app.Event) {
		if e.Get("key").String() == "Escape" {
			onClose(ctx, e)
		}
	}
	card := []app.UI{
		app.Div().Class("ph-dlg-head").Body(
			app.Span().Class("ph-dlg-title").Text(title),
			app.Button().Class("ph-tile-btn").Title("Schließen").Text("✕").OnClick(onClose),
		),
	}
	card = append(card, app.Div().Class("ph-dlg-body").Body(body...))
	if len(actions) > 0 {
		card = append(card, app.Div().Class("ph-dlg-foot").Body(
			app.Range(actions).Slice(func(i int) app.UI {
				a := actions[i]
				cls := "ph-btn ph-btn-ghost"
				switch {
				case a.Primary:
					cls = "ph-btn"
				case a.Danger:
					cls = "ph-btn ph-btn-ghost ph-btn-danger"
				}
				return app.Button().Class(cls).Text(a.Label).OnClick(a.OnClick)
			}),
		))
	}
	return app.Div().Class("ph-dlg-host").Body(
		app.Div().Class("ph-dlg-backdrop").OnClick(onClose),
		// The card takes focus itself (tabindex) so Escape works even before the user
		// touches a field, and stops clicks from reaching the backdrop behind it.
		app.Div().Class("ph-dlg").Attr("tabindex", "-1").
			OnKeyDown(closeOnEsc).
			OnClick(func(ctx app.Context, e app.Event) { e.Call("stopPropagation") }).
			Body(card...),
	)
}

// setInputValue writes v straight into an input's DOM value.
//
// Rendering Value("") does NOT clear a field: go-app's attributes.Set drops empty
// attribute values, so an input the user has typed into keeps showing the old text
// even though the Go state is already "". That is why adding a todo used to leave the
// text in the field and the next one came out as "erstes" + "zweites" concatenated.
// Pass the element (captured before the async work), not a selector — several tiles of
// the same kind can be on screen at once.
func setInputValue(el app.Value, v string) {
	if el.Truthy() {
		el.Set("value", v)
	}
}

// inputIn returns the first input inside the ancestor matching sel, starting from the
// element an event came from ("" ⇒ the source itself).
func inputIn(src app.Value, sel string) app.Value {
	if !src.Truthy() {
		return app.Null()
	}
	host := src.Call("closest", sel)
	if !host.Truthy() {
		return app.Null()
	}
	return host.Call("querySelector", "input")
}

// focusDialogField moves the caret into the dialog's first input once it exists. Call
// it from the handler that opens the dialog (via ctx.Defer, after the render).
func focusDialogField(ctx app.Context) {
	ctx.Defer(func(app.Context) {
		doc := app.Window().Get("document")
		if !doc.Truthy() {
			return
		}
		if el := doc.Call("querySelector", ".ph-dlg .ph-dlg-focus"); el.Truthy() {
			el.Call("focus")
			el.Call("select")
		}
	})
}
