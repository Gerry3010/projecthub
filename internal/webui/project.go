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

	notes    []store.Note
	tabsets  []store.Item[domain.TabSet]
	pins     []store.Item[domain.PinnedItem]
	files    []store.Item[domain.FileBlob]
	sessions []store.Item[domain.CodeSession]
	ppLink   *store.Item[domain.PipepushLink]

	// pipepush link form
	ppBaseURL   string
	ppProjectID string
	ppLabel     string
	ppToken     string
	ppPipeline  string

	// note form (doubles as editor when editNoteID != "")
	newNoteTitle string
	newNoteBody  string
	editNoteID   string

	// pin form
	pinLabel   string
	pinRelPath string
	pinIsDir   bool

	// manual tab-set form
	tsTitle string
	tsURLs  string

	// id of the file currently previewed inline ("" = none)
	previewID string
}

// defaultPipepushBaseURL pre-fills the link form with the production pipepush
// server; it stays editable for other deployments.
const defaultPipepushBaseURL = "https://pipepush.geraldhofbauer.net"

// OnMount loads the project's contents once the component is in the DOM.
func (p *ProjectPage) OnMount(ctx app.Context) {
	if p.ppBaseURL == "" {
		p.ppBaseURL = defaultPipepushBaseURL
	}
	p.reload(ctx, nil)
}

func (p *ProjectPage) Render() app.UI {
	return app.Div().Class("ph-app").Style("--accent", p.Ref.AccentColor()).Body(
		app.Header().Class("ph-header").Body(
			app.Div().Class("ph-headleft").Body(
				app.Button().Class("ph-link").Text("← Projekte").OnClick(func(ctx app.Context, _ app.Event) {
					if p.Back != nil {
						p.Back(ctx)
					}
				}),
				app.Div().Class("ph-brand").Body(
					nexusIcon(p.Ref.AccentColor(), 26),
					app.H1().Text(p.Ref.Title),
				),
			),
			app.Div().Class("ph-headright").Body(
				app.Span().Class("ph-muted").Text("/"+p.Ref.Slug),
				swatchBar(p.Ref.AccentColor(), p.pickColor, p.customColor),
			),
		),
		app.If(p.status != "", func() app.UI { return app.P().Class("ph-err").Text(p.status) }),
		app.If(!p.loaded, func() app.UI { return app.P().Class("ph-muted").Text("Lädt…") }).
			Else(func() app.UI {
				return app.Div().Body(
					p.notesSection(),
					p.filesSection(),
					p.tabsetsSection(),
					p.codeSessionsSection(),
					p.pinsSection(),
					p.pipepushSection(),
				)
			}),
	)
}

// ─── notes ──────────────────────────────────────────────────────────────────

func (p *ProjectPage) notesSection() app.UI {
	saveLabel := "Notiz speichern"
	if p.editNoteID != "" {
		saveLabel = "Notiz aktualisieren"
	}
	return app.Section().Class("ph-section").Body(
		app.H2().Text("Notizen"),
		app.Div().Class("ph-noteform").Body(
			app.Input().Type("text").Placeholder("Titel").Value(p.newNoteTitle).OnInput(bindInput(&p.newNoteTitle)),
			app.Textarea().Class("ph-textarea").Placeholder("Notiz…").Text(p.newNoteBody).OnInput(bindInput(&p.newNoteBody)),
			app.Div().Class("ph-row").Body(
				app.Button().Class("ph-btn").Disabled(p.busy).Text(saveLabel).OnClick(p.saveNote),
				app.If(p.editNoteID != "", func() app.UI {
					return app.Button().Class("ph-link").Text("abbrechen").OnClick(p.cancelEditNote)
				}),
			),
		),
		app.Ul().Class("ph-list").Body(
			app.Range(p.notes).Slice(func(i int) app.UI {
				n := p.notes[i]
				return app.Li().Class("ph-item ph-noteitem").Body(
					app.Div().Body(
						app.Strong().Text(orDash(n.Doc.Title)),
						app.P().Class("ph-muted").Text(n.Doc.Body),
					),
					app.Div().Body(
						app.Button().Class("ph-link").Text("bearbeiten").OnClick(func(ctx app.Context, _ app.Event) {
							p.editNoteID = n.ID
							p.newNoteTitle, p.newNoteBody = n.Doc.Title, n.Doc.Body
						}),
						app.Button().Class("ph-link").Text("löschen").OnClick(func(ctx app.Context, _ app.Event) {
							p.run(ctx, func() error { return p.Store.DeleteNote(context.Background(), n.ID) })
						}),
					),
				)
			}),
			app.If(len(p.notes) == 0, func() app.UI { return app.Li().Class("ph-muted").Text("Keine Notizen.") }),
		),
	)
}

// saveNote creates a new note, or updates the one currently being edited.
func (p *ProjectPage) saveNote(ctx app.Context, _ app.Event) {
	if p.busy || (p.newNoteTitle == "" && p.newNoteBody == "") {
		return
	}
	doc := domain.NoteDoc{Title: p.newNoteTitle, Body: p.newNoteBody, UpdatedAt: time.Now()}
	editID := p.editNoteID
	p.runThen(ctx, func() error {
		if editID != "" {
			return p.Store.UpdateNote(context.Background(), editID, p.Ref.FolderID, doc)
		}
		_, err := p.Store.CreateNote(context.Background(), p.Ref.FolderID, doc)
		return err
	}, func() { p.newNoteTitle, p.newNoteBody, p.editNoteID = "", "", "" })
}

func (p *ProjectPage) cancelEditNote(ctx app.Context, _ app.Event) {
	p.editNoteID, p.newNoteTitle, p.newNoteBody = "", "", ""
}

// ─── files ──────────────────────────────────────────────────────────────────

func (p *ProjectPage) filesSection() app.UI {
	return app.Section().Class("ph-section").Body(
		app.H2().Text("Dateien"),
		app.Input().Type("file").OnChange(p.uploadFile),
		app.Ul().Class("ph-list").Body(
			app.Range(p.files).Slice(func(i int) app.UI {
				f := p.files[i]
				return app.Li().Class("ph-item ph-fileitem").Body(
					app.Div().Class("ph-filerow").Body(
						app.Div().Body(
							app.Strong().Text(f.Val.Filename),
							app.Span().Class("ph-muted").Text(fmt.Sprintf("  %s · %s", humanSize(f.Val.Size), orDash(f.Val.MIME))),
						),
						app.Div().Body(
							app.If(previewable(f.Val), func() app.UI {
								return app.Button().Class("ph-link").Text(toggleLabel(p.previewID == f.ID)).OnClick(func(ctx app.Context, _ app.Event) {
									p.previewID = toggle(p.previewID, f.ID)
								})
							}),
							app.A().Class("ph-link").Href(dataURL(f.Val)).Download(f.Val.Filename).Text("herunterladen"),
							app.Button().Class("ph-link").Text("löschen").OnClick(func(ctx app.Context, _ app.Event) {
								p.run(ctx, func() error { return p.Store.DeleteItem(context.Background(), f.ID) })
							}),
						),
					),
					app.If(p.previewID == f.ID, func() app.UI { return filePreview(f.Val) }),
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
		app.P().Class("ph-muted").Text("Der TUI-Companion speichert offene Browser-Tabs; hier kannst du auch manuell eines anlegen (eine URL pro Zeile)."),
		app.Div().Class("ph-noteform").Body(
			app.Input().Type("text").Placeholder("Titel").Value(p.tsTitle).OnInput(bindInput(&p.tsTitle)),
			app.Textarea().Class("ph-textarea").Placeholder("https://…\nhttps://…").Text(p.tsURLs).OnInput(bindInput(&p.tsURLs)),
			app.Button().Class("ph-btn").Disabled(p.busy).Text("Tab-Set anlegen").OnClick(p.addTabSet),
		),
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
		app.P().Class("ph-muted").Text("Relativ zum lokalen Index-Root; Öffnen nur über den TUI-Companion."),
		app.Div().Class("ph-noteform").Body(
			app.Input().Type("text").Placeholder("Label").Value(p.pinLabel).OnInput(bindInput(&p.pinLabel)),
			app.Input().Type("text").Placeholder("relativer Pfad (z.B. docs/spec.md)").Value(p.pinRelPath).OnInput(bindInput(&p.pinRelPath)),
			app.Label().Class("ph-check").Body(
				app.Input().Type("checkbox").Checked(p.pinIsDir).OnChange(bindCheck(&p.pinIsDir)),
				app.Text(" Ordner"),
			),
			app.Button().Class("ph-btn").Disabled(p.busy).Text("Pfad anheften").OnClick(p.addPin),
		),
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

// ─── Claude Code sessions (read-only) ─────────────────────────────────────────

// codeSessionsSection lists the Claude Code sessions captured for this project.
// Capture and resume are TUI-only (they need local file/process access), so the
// web view is read-only.
func (p *ProjectPage) codeSessionsSection() app.UI {
	return app.Section().Class("ph-section").Body(
		app.H2().Text("Claude-Code-Sessions"),
		app.P().Class("ph-muted").Text("Vom TUI-Companion erfasst; Fortsetzen ('claude --resume') nur dort möglich."),
		app.Ul().Class("ph-list").Body(
			app.Range(p.sessions).Slice(func(i int) app.UI {
				cs := p.sessions[i]
				return app.Li().Class("ph-item").Body(
					app.Div().Body(
						app.Strong().Text(orDash(cs.Val.Title)),
						app.Span().Class("ph-muted").Text(fmt.Sprintf("  %s · %s", cs.Val.LastActive.Format("2006-01-02 15:04"), orDash(cs.Val.Cwd))),
					),
					app.Button().Class("ph-link").Text("löschen").OnClick(func(ctx app.Context, _ app.Event) {
						p.run(ctx, func() error { return p.Store.DeleteItem(context.Background(), cs.ID) })
					}),
				)
			}),
			app.If(len(p.sessions) == 0, func() app.UI { return app.Li().Class("ph-muted").Text("Keine Sessions erfasst.") }),
		),
	)
}

// ─── pipepush link ────────────────────────────────────────────────────────────

// pipepushSection links this project to a pipepush project (base URL + project id
// + optional webhook token). Status webhooks are fired from the TUI companion.
func (p *ProjectPage) pipepushSection() app.UI {
	return app.Section().Class("ph-section").Body(
		app.H2().Text("pipepush"),
		app.P().Class("ph-muted").Text("Ordne dieses Projekt einem pipepush-Projekt zu. Der Token bleibt Ende-zu-Ende verschlüsselt gespeichert."),
		app.If(p.ppLink != nil, func() app.UI {
			l := p.ppLink.Val
			return app.Div().Class("ph-item").Body(
				app.Div().Body(
					app.Strong().Text(orDash(l.Label)),
					app.Span().Class("ph-muted").Text("  "+l.BaseURL+" · "+l.ProjectID),
				),
				app.Div().Body(
					app.A().Class("ph-link").Href(pipepushURL(l)).Target("_blank").Text("in pipepush öffnen"),
					app.Button().Class("ph-link").Text("entfernen").OnClick(func(ctx app.Context, _ app.Event) {
						p.run(ctx, func() error { return p.Store.DeleteItem(context.Background(), p.ppLink.ID) })
					}),
				),
			)
		}).Else(func() app.UI {
			return app.Div().Class("ph-noteform").Body(
				app.Input().Type("text").Placeholder("Base-URL (https://pipepush.…)").Value(p.ppBaseURL).OnInput(bindInput(&p.ppBaseURL)),
				app.Input().Type("text").Placeholder("pipepush-Projekt-UUID").Value(p.ppProjectID).OnInput(bindInput(&p.ppProjectID)),
				app.Input().Type("text").Placeholder("Label (optional)").Value(p.ppLabel).OnInput(bindInput(&p.ppLabel)),
				app.Input().Type("text").Placeholder("Pipeline-Name (optional)").Value(p.ppPipeline).OnInput(bindInput(&p.ppPipeline)),
				app.Input().Type("password").Placeholder("Webhook-Token pp_… (optional)").Value(p.ppToken).OnInput(bindInput(&p.ppToken)),
				app.Button().Class("ph-btn").Disabled(p.busy).Text("Verknüpfen").OnClick(p.savePipepushLink),
			)
		}),
	)
}

func (p *ProjectPage) savePipepushLink(ctx app.Context, _ app.Event) {
	if p.busy {
		return
	}
	if strings.TrimSpace(p.ppBaseURL) == "" || strings.TrimSpace(p.ppProjectID) == "" {
		p.fail(ctx, "Base-URL und Projekt-UUID sind erforderlich")
		return
	}
	link := domain.PipepushLink{
		BaseURL:   strings.TrimSpace(p.ppBaseURL),
		ProjectID: strings.TrimSpace(p.ppProjectID),
		Label:     p.ppLabel,
		Token:     strings.TrimSpace(p.ppToken),
		Pipeline:  strings.TrimSpace(p.ppPipeline),
		LinkedAt:  time.Now(),
	}
	p.runThen(ctx, func() error {
		_, err := p.Store.SetPipepushLink(context.Background(), p.Ref.FolderID, link)
		return err
	}, func() {
		p.ppProjectID, p.ppLabel, p.ppToken, p.ppPipeline = "", "", "", ""
		p.ppBaseURL = defaultPipepushBaseURL // keep the default ready if the link is later removed
	})
}

func (p *ProjectPage) addTabSet(ctx app.Context, _ app.Event) {
	if p.busy {
		return
	}
	var tabs []domain.Tab
	for _, line := range strings.Split(p.tsURLs, "\n") {
		if u := strings.TrimSpace(line); u != "" {
			tabs = append(tabs, domain.Tab{URL: u})
		}
	}
	if len(tabs) == 0 {
		p.fail(ctx, "Mindestens eine URL angeben")
		return
	}
	title := p.tsTitle
	if title == "" {
		title = "Manuell"
	}
	ts := domain.TabSet{Title: title, Browser: "manual", Tabs: tabs, SavedAt: time.Now()}
	p.runThen(ctx, func() error {
		_, err := p.Store.CreateTabSet(context.Background(), p.Ref.FolderID, ts)
		return err
	}, func() { p.tsTitle, p.tsURLs = "", "" })
}

func (p *ProjectPage) addPin(ctx app.Context, _ app.Event) {
	if p.busy || p.pinRelPath == "" {
		return
	}
	pin := domain.PinnedItem{Label: p.pinLabel, RelPath: p.pinRelPath, IsDir: p.pinIsDir}
	p.runThen(ctx, func() error {
		_, err := p.Store.CreatePin(context.Background(), p.Ref.FolderID, pin)
		return err
	}, func() { p.pinLabel, p.pinRelPath, p.pinIsDir = "", "", false })
}

// ─── project color ──────────────────────────────────────────────────────────

// pickColor sets this project's accent to a preset swatch and persists it.
func (p *ProjectPage) pickColor(color string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) { p.applyColor(ctx, color) }
}

// customColor handles the native color-well change event.
func (p *ProjectPage) customColor(ctx app.Context, _ app.Event) {
	p.applyColor(ctx, ctx.JSSrc().Get("value").String())
}

// applyColor recolors the page immediately and persists the choice to the manifest
// and RootIndex mirror in the background. The list view picks up the new color on
// its next reload (i.e. when navigating back).
func (p *ProjectPage) applyColor(ctx app.Context, color string) {
	if p.busy || color == "" || eqColor(color, p.Ref.AccentColor()) {
		return
	}
	p.Ref.Color = color // re-themes the header + icon on the next render
	id := p.Ref.ID
	p.busy = true
	st := p.Store
	ctx.Async(func() {
		err := st.SetProjectColor(context.Background(), id, color)
		ctx.Dispatch(func(ctx app.Context) {
			p.busy = false
			if err != nil {
				p.status = "Farbe speichern fehlgeschlagen: " + err.Error()
			}
		})
	})
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
	sessions, err := p.Store.ListCodeSessions(bg, folder)
	if err != nil {
		p.fail(ctx, "Sessions: "+err.Error())
		return
	}
	ppLink, err := p.Store.GetPipepushLink(bg, folder)
	if err != nil {
		p.fail(ctx, "pipepush: "+err.Error())
		return
	}

	ctx.Dispatch(func(ctx app.Context) {
		p.notes, p.files, p.tabsets, p.pins = notes, files, tabsets, pins
		p.sessions, p.ppLink = sessions, ppLink
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

// previewable reports whether a file can be shown inline (images and text).
func previewable(f domain.FileBlob) bool {
	return strings.HasPrefix(f.MIME, "image/") || isText(f.MIME)
}

func isText(mime string) bool {
	return strings.HasPrefix(mime, "text/") ||
		mime == "application/json" || mime == "application/xml"
}

// filePreview renders an inline preview: an <img> for images, a <pre> for text.
func filePreview(f domain.FileBlob) app.UI {
	if strings.HasPrefix(f.MIME, "image/") {
		return app.Img().Class("ph-preview-img").Src(dataURL(f))
	}
	return app.Pre().Class("ph-preview-text").Text(string(f.Bytes))
}

func toggle(cur, id string) string {
	if cur == id {
		return ""
	}
	return id
}

func toggleLabel(open bool) string {
	if open {
		return "Vorschau aus"
	}
	return "Vorschau"
}

// bindCheck returns a change handler that writes a checkbox's state into dst.
func bindCheck(dst *bool) app.EventHandler {
	return func(ctx app.Context, e app.Event) {
		*dst = ctx.JSSrc().Get("checked").Bool()
	}
}

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

// pipepushURL builds a best-effort deep link into pipepush. pipepush serves its
// web UI at the root; if it later exposes a per-project route this is the single
// place to refine.
func pipepushURL(l domain.PipepushLink) string {
	return strings.TrimRight(l.BaseURL, "/") + "/"
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
