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
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/store"
)

// ProjectPage shows one project's contents: notes, files, tab sets and pinned
// references. All decryption happens here in the browser via the shared store.
type ProjectPage struct {
	app.Compo

	Store *store.Store
	Ref   domain.ProjectRef
	Back  func(ctx app.Context)

	loaded bool
	busy   bool
	status string

	notes   []store.Note
	tabsets []store.Item[domain.TabSet]
	pins    []store.Item[domain.PinnedItem]
	files   []store.Item[domain.FileBlob]

	newNoteTitle string
	newNoteBody  string
}

// OnMount loads the project's contents once the component is in the DOM.
func (p *ProjectPage) OnMount(ctx app.Context) { p.reload(ctx, nil) }

func (p *ProjectPage) Render() app.UI {
	return app.Div().Class("ph-app").Body(
		app.Header().Class("ph-header").Body(
			app.Div().Body(
				app.Button().Class("ph-link").Text("← Projekte").OnClick(func(ctx app.Context, _ app.Event) {
					if p.Back != nil {
						p.Back(ctx)
					}
				}),
				app.H1().Text(p.Ref.Title),
			),
			app.Span().Class("ph-muted").Text("/"+p.Ref.Slug),
		),
		app.If(p.status != "", func() app.UI { return app.P().Class("ph-err").Text(p.status) }),
		app.If(!p.loaded, func() app.UI { return app.P().Class("ph-muted").Text("Lädt…") }).
			Else(func() app.UI {
				return app.Div().Body(
					p.notesSection(),
					p.filesSection(),
					p.tabsetsSection(),
					p.pinsSection(),
				)
			}),
	)
}

// ─── notes ──────────────────────────────────────────────────────────────────

func (p *ProjectPage) notesSection() app.UI {
	return app.Section().Class("ph-section").Body(
		app.H2().Text("Notizen"),
		app.Div().Class("ph-noteform").Body(
			app.Input().Type("text").Placeholder("Titel").Value(p.newNoteTitle).OnInput(bindInput(&p.newNoteTitle)),
			app.Textarea().Class("ph-textarea").Placeholder("Notiz…").Text(p.newNoteBody).OnInput(bindInput(&p.newNoteBody)),
			app.Button().Class("ph-btn").Disabled(p.busy).Text("Notiz speichern").OnClick(p.addNote),
		),
		app.Ul().Class("ph-list").Body(
			app.Range(p.notes).Slice(func(i int) app.UI {
				n := p.notes[i]
				return app.Li().Class("ph-item ph-noteitem").Body(
					app.Div().Body(
						app.Strong().Text(orDash(n.Doc.Title)),
						app.P().Class("ph-muted").Text(n.Doc.Body),
					),
					app.Button().Class("ph-link").Text("löschen").OnClick(func(ctx app.Context, _ app.Event) {
						p.run(ctx, func() error { return p.Store.DeleteNote(context.Background(), n.ID) })
					}),
				)
			}),
			app.If(len(p.notes) == 0, func() app.UI { return app.Li().Class("ph-muted").Text("Keine Notizen.") }),
		),
	)
}

func (p *ProjectPage) addNote(ctx app.Context, _ app.Event) {
	if p.busy || (p.newNoteTitle == "" && p.newNoteBody == "") {
		return
	}
	doc := domain.NoteDoc{Title: p.newNoteTitle, Body: p.newNoteBody, UpdatedAt: time.Now()}
	p.runThen(ctx, func() error {
		_, err := p.Store.CreateNote(context.Background(), p.Ref.FolderID, doc)
		return err
	}, func() { p.newNoteTitle, p.newNoteBody = "", "" })
}

// ─── files ──────────────────────────────────────────────────────────────────

func (p *ProjectPage) filesSection() app.UI {
	return app.Section().Class("ph-section").Body(
		app.H2().Text("Dateien"),
		app.Input().Type("file").OnChange(p.uploadFile),
		app.Ul().Class("ph-list").Body(
			app.Range(p.files).Slice(func(i int) app.UI {
				f := p.files[i]
				return app.Li().Class("ph-item").Body(
					app.Div().Body(
						app.Strong().Text(f.Val.Filename),
						app.Span().Class("ph-muted").Text(fmt.Sprintf("  %s · %s", humanSize(f.Val.Size), orDash(f.Val.MIME))),
					),
					app.Div().Body(
						app.A().Class("ph-link").Href(dataURL(f.Val)).Download(f.Val.Filename).Text("herunterladen"),
						app.Button().Class("ph-link").Text("löschen").OnClick(func(ctx app.Context, _ app.Event) {
							p.run(ctx, func() error { return p.Store.DeleteItem(context.Background(), f.ID) })
						}),
					),
				)
			}),
			app.If(len(p.files) == 0, func() app.UI { return app.Li().Class("ph-muted").Text("Keine Dateien.") }),
		),
	)
}

// uploadFile reads the selected file in the browser via FileReader, then seals and
// stores it. The bytes are read as a data URL to avoid Uint8Array copying.
func (p *ProjectPage) uploadFile(ctx app.Context, _ app.Event) {
	files := ctx.JSSrc().Get("files")
	if !files.Truthy() || files.Length() == 0 {
		return
	}
	f := files.Index(0)
	name := f.Get("name").String()
	mime := f.Get("type").String()

	reader := app.Window().Get("FileReader").New()
	var onload app.Func
	onload = app.FuncOf(func(this app.Value, args []app.Value) any {
		onload.Release()
		raw, err := decodeDataURL(reader.Get("result").String())
		if err != nil {
			p.fail(ctx, "Upload fehlgeschlagen: "+err.Error())
			return nil
		}
		blob := domain.FileBlob{Filename: name, MIME: mime, Size: int64(len(raw)), Bytes: raw}
		p.run(ctx, func() error {
			_, err := p.Store.CreateFile(context.Background(), p.Ref.FolderID, blob)
			return err
		})
		return nil
	})
	reader.Set("onload", onload)
	reader.Call("readAsDataURL", f)
}

// ─── tab sets ───────────────────────────────────────────────────────────────

func (p *ProjectPage) tabsetsSection() app.UI {
	return app.Section().Class("ph-section").Body(
		app.H2().Text("Tab-Sets"),
		app.P().Class("ph-muted").Text("Tab-Sets werden vom TUI-Companion gespeichert."),
		app.Ul().Class("ph-list").Body(
			app.Range(p.tabsets).Slice(func(i int) app.UI {
				ts := p.tabsets[i]
				return app.Li().Class("ph-item").Body(
					app.Div().Body(
						app.Strong().Text(orDash(ts.Val.Title)),
						app.Span().Class("ph-muted").Text(fmt.Sprintf("  %d Tabs · %s", len(ts.Val.Tabs), orDash(ts.Val.Browser))),
					),
					app.Div().Body(
						app.Button().Class("ph-link").Text("alle öffnen").OnClick(func(ctx app.Context, _ app.Event) {
							openTabs(ts.Val.Tabs)
						}),
						app.Button().Class("ph-link").Text("löschen").OnClick(func(ctx app.Context, _ app.Event) {
							p.run(ctx, func() error { return p.Store.DeleteItem(context.Background(), ts.ID) })
						}),
					),
				)
			}),
			app.If(len(p.tabsets) == 0, func() app.UI { return app.Li().Class("ph-muted").Text("Keine Tab-Sets.") }),
		),
	)
}

// ─── pins ───────────────────────────────────────────────────────────────────

func (p *ProjectPage) pinsSection() app.UI {
	return app.Section().Class("ph-section").Body(
		app.H2().Text("Angeheftete Pfade"),
		app.P().Class("ph-muted").Text("Öffnen nur über den TUI-Companion (lokaler Index-Root)."),
		app.Ul().Class("ph-list").Body(
			app.Range(p.pins).Slice(func(i int) app.UI {
				pin := p.pins[i]
				return app.Li().Class("ph-item").Body(
					app.Div().Body(
						app.Strong().Text(orDash(pin.Val.Label)),
						app.Span().Class("ph-muted").Text("  "+pin.Val.RelPath+dirSuffix(pin.Val.IsDir)),
					),
					app.Button().Class("ph-link").Text("löschen").OnClick(func(ctx app.Context, _ app.Event) {
						p.run(ctx, func() error { return p.Store.DeleteItem(context.Background(), pin.ID) })
					}),
				)
			}),
			app.If(len(p.pins) == 0, func() app.UI { return app.Li().Class("ph-muted").Text("Keine angehefteten Pfade.") }),
		),
	)
}

// ─── data loading + mutation plumbing ───────────────────────────────────────

// run executes a mutating store action off the UI goroutine, then reloads.
func (p *ProjectPage) run(ctx app.Context, action func() error) {
	p.runThen(ctx, action, nil)
}

// runThen is run() with an extra UI mutation applied after a successful reload.
func (p *ProjectPage) runThen(ctx app.Context, action func() error, after func()) {
	if p.busy {
		return
	}
	p.busy, p.status = true, ""
	ctx.Async(func() {
		if err := action(); err != nil {
			p.fail(ctx, err.Error())
			return
		}
		p.reload(ctx, after)
	})
}

// reload re-fetches all sections, applies an optional UI mutation, and clears busy.
func (p *ProjectPage) reload(ctx app.Context, after func()) {
	folder := p.Ref.FolderID
	bg := context.Background()

	notes, err := p.Store.ListNotes(bg, folder)
	if err != nil {
		p.fail(ctx, "Notizen: "+err.Error())
		return
	}
	files, err := p.Store.ListFiles(bg, folder)
	if err != nil {
		p.fail(ctx, "Dateien: "+err.Error())
		return
	}
	tabsets, err := p.Store.ListTabSets(bg, folder)
	if err != nil {
		p.fail(ctx, "Tab-Sets: "+err.Error())
		return
	}
	pins, err := p.Store.ListPins(bg, folder)
	if err != nil {
		p.fail(ctx, "Pins: "+err.Error())
		return
	}

	ctx.Dispatch(func(ctx app.Context) {
		p.notes, p.files, p.tabsets, p.pins = notes, files, tabsets, pins
		p.loaded, p.busy = true, false
		if after != nil {
			after()
		}
	})
}

func (p *ProjectPage) fail(ctx app.Context, msg string) {
	ctx.Dispatch(func(ctx app.Context) {
		p.status = msg
		p.busy = false
		p.loaded = true
	})
}

// ─── small helpers ──────────────────────────────────────────────────────────

// openTabs reopens each URL in a new browser tab via window.open.
func openTabs(tabs []domain.Tab) {
	w := app.Window()
	for _, t := range tabs {
		w.Call("open", t.URL, "_blank")
	}
}

// dataURL builds a base64 data URL for downloading a decrypted file in-browser.
func dataURL(f domain.FileBlob) string {
	mime := f.MIME
	if mime == "" {
		mime = "application/octet-stream"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(f.Bytes)
}

// decodeDataURL extracts the bytes from a "data:...;base64,<data>" URL.
func decodeDataURL(u string) ([]byte, error) {
	i := strings.IndexByte(u, ',')
	if i < 0 {
		return nil, fmt.Errorf("invalid data URL")
	}
	return base64.StdEncoding.DecodeString(u[i+1:])
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func dirSuffix(isDir bool) string {
	if isDir {
		return "/"
	}
	return ""
}
