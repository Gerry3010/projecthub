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
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// ─── Projekt tab (per-project settings; only offered when one is open) ──────────

// pickFolder opens the shell's native directory chooser and calls cb with the chosen
// path ("" on cancel). Reports false when the bridge is missing (hosted browser build),
// so callers can fall back to the plain text field.
func pickFolder(current string, cb func(dir string)) bool {
	pw := app.Window().Get("phWindow")
	if !pw.Truthy() || !pw.Get("pickFolder").Truthy() {
		return false
	}
	var fn app.Func
	fn = app.FuncOf(func(_ app.Value, args []app.Value) any {
		fn.Release() // one-shot: the dialog answers exactly once
		dir := ""
		if len(args) > 0 {
			dir = args[0].String()
		}
		cb(dir)
		return nil
	})
	pw.Call("pickFolder", current, fn)
	return true
}

// folderPickerAvailable reports whether the native directory chooser exists.
func folderPickerAvailable() bool {
	pw := app.Window().Get("phWindow")
	return pw.Truthy() && pw.Get("pickFolder").Truthy()
}

// projectPathBuffer returns the edit buffer for the open project's local path,
// (re)filling it from the project when the settings screen shows another project
// than the buffer was last loaded for.
func (r *Root) projectPathBuffer() string {
	if r.selected == nil {
		return ""
	}
	if r.projPathFor != r.selected.ID {
		r.projPathFor = r.selected.ID
		r.projPath = r.selected.LocalPath
		r.projPathMsg = ""
	}
	return r.projPath
}

func (r *Root) settingsProject() app.UI {
	if r.selected == nil {
		return app.Div().Body(
			app.P().Class("ph-eyebrow").Text("Projekt"),
			app.P().Class("ph-settings-note").
				Text("Öffne zuerst ein Projekt — diese Einstellungen gelten jeweils für das offene Projekt."),
		)
	}
	p := r.selected
	cur := r.projectPathBuffer()
	return app.Div().Body(
		app.P().Class("ph-eyebrow").Text("Projekt"),
		app.Div().Class("ph-set-row").Body(
			app.Span().Class("ph-muted").Text("Name"),
			app.Span().Class("ph-set-val").Text(p.Title),
		),

		app.P().Class("ph-eyebrow ph-settings-gap").Text("Lokaler Pfad"),
		app.Div().Class("ph-path-row").Body(
			app.Input().Type("text").Class("ph-set-input").
				Placeholder("/home/gerry/Sync-Projekte/…").
				Attr("spellcheck", "false").
				Value(cur).
				OnInput(r.bind(&r.projPath)).
				OnKeyDown(func(ctx app.Context, e app.Event) {
					if e.Get("key").String() == "Enter" {
						r.saveProjectPath(ctx, r.projPath)
					}
				}),
			app.If(folderPickerAvailable(), func() app.UI {
				return app.Button().Class("ph-btn ph-btn-ghost").Text("Ordner wählen…").
					OnClick(func(ctx app.Context, _ app.Event) {
						pickFolder(r.projPath, func(dir string) {
							if dir == "" {
								return // cancelled
							}
							r.saveProjectPath(ctx, dir)
						})
					})
			}),
			app.Button().Class("ph-btn").Text("Speichern").
				OnClick(func(ctx app.Context, _ app.Event) { r.saveProjectPath(ctx, r.projPath) }),
		),
		app.If(r.projPathMsg != "", func() app.UI {
			return app.P().Class("ph-settings-note").Text(r.projPathMsg)
		}),
		app.P().Class("ph-settings-note").Text("Das echte Arbeitsverzeichnis dieses Projekts auf diesem Gerät: "+
			"Terminal, Claude, Dateien und Sessions starten dort. Leer lassen ⇒ Rückfall auf den Ablage-Ordner "+
			"des Projekts. Der Pfad liegt verschlüsselt im Tresor und gilt derzeit für alle Geräte — auf einem "+
			"anderen Rechner kann er also ins Leere zeigen."),
		app.P().Class("ph-settings-note").Text("Bereits offene Terminals behalten ihr altes Verzeichnis; "+
			"neue Tiles verwenden sofort den neuen Pfad."),
	)
}

// normalizeLocalPath cleans a hand-typed working directory and rejects what cannot
// be one. Returns the cleaned path and an empty reason on success; "" with a reason
// when the input is unusable. An empty input is valid — it clears the path.
func normalizeLocalPath(raw string) (dir, reason string) {
	dir = strings.TrimSpace(raw)
	if dir == "" {
		return "", ""
	}
	// Strip a trailing separator ("/home/x/" ⇒ "/home/x") so the value compares equal
	// to the sidecar's and to Claude Code cwds, but keep the root itself intact.
	if len(dir) > 1 {
		dir = strings.TrimRight(dir, "/")
	}
	if !strings.HasPrefix(dir, "/") {
		return "", "Bitte einen absoluten Pfad angeben (mit / beginnend)."
	}
	if strings.Contains(dir, "\x00") {
		return "", "Ungültiges Zeichen im Pfad."
	}
	return dir, ""
}

// saveProjectPath validates the directory (via the sidecar, when available) and
// persists it to the manifest + RootIndex, then updates the in-memory project list so
// the home cards and any new tile pick it up without a reload.
func (r *Root) saveProjectPath(ctx app.Context, raw string) {
	if r.selected == nil {
		return
	}
	dir, reason := normalizeLocalPath(raw)
	if reason != "" {
		r.projPath, r.projPathMsg = raw, reason
		return
	}
	id, st, nc := r.selected.ID, r.store, r.native
	// Push the cleaned value back into the field: rendering it is not enough once the
	// user has typed (see setInputValue), so a normalised or cleared path would keep
	// showing the raw input.
	setInputValue(inputIn(ctx.JSSrc(), ".ph-path-row"), dir)
	r.projPath, r.projPathMsg = dir, "Wird gespeichert…"
	ctx.Async(func() {
		// Verify the directory really exists before writing it — a typo'd path would
		// otherwise only surface later as a terminal that refuses to start.
		if dir != "" && nc.Available() {
			if _, err := nc.ListDir(context.Background(), dir); err != nil {
				ctx.Dispatch(func(ctx app.Context) {
					r.projPathMsg = "Ordner nicht lesbar: " + err.Error()
				})
				return
			}
		}
		if err := st.SetProjectLocalPath(context.Background(), id, dir); err != nil {
			ctx.Dispatch(func(ctx app.Context) { r.projPathMsg = "Speichern fehlgeschlagen: " + err.Error() })
			return
		}
		ctx.Dispatch(func(ctx app.Context) {
			r.applyLocalPath(id, dir)
			if dir == "" {
				r.projPathMsg = "Pfad entfernt — es gilt wieder der Ablage-Ordner."
			} else {
				r.projPathMsg = "Gespeichert: " + dir
			}
		})
	})
}

// applyLocalPath mirrors a saved path into the in-memory project state (open project
// + home list), so the UI reflects it without a round-trip to the vault.
func (r *Root) applyLocalPath(id, dir string) {
	if r.selected != nil && r.selected.ID == id {
		r.selected.LocalPath = dir
	}
	for i := range r.projects {
		if r.projects[i].ID == id {
			r.projects[i].LocalPath = dir
		}
	}
	r.projPathFor, r.projPath = id, dir
	// A project that just got a path may now shadow a Claude Code suggestion.
	r.suggestions = filterAdded(r.suggestions, r.projects)
}

// openProjectSettings opens a project and lands directly on its Projekt tab — the
// path shortcut from the home cards.
func (r *Root) openProjectSettings(ref domain.ProjectRef) app.EventHandler {
	return func(ctx app.Context, e app.Event) {
		e.Call("stopPropagation")
		r.navProject(ref)
		r.projPathFor = "" // reload the buffer for this project
		r.settingsTab = "project"
		r.showSettings = true
	}
}
