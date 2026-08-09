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
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/store"
	"github.com/Gerry3010/projecthub/internal/native/nativeclient"
)

// The native (go-app) workspace tiles: Notizen, Dateien, Claude-Sessions. Each is a
// self-contained component that loads its own data from the shared Store, so any
// number of instances can coexist as tiles without shared state.

// ─── notes ─────────────────────────────────────────────────────────────────────

type notesTile struct {
	app.Compo
	Store    *store.Store
	FolderID string

	notes    []store.Note
	newTitle string
	newBody  string
	busy     bool
	loaded   bool
	status   string
}

func (t *notesTile) OnMount(ctx app.Context) { t.reload(ctx) }

func (t *notesTile) reload(ctx app.Context) {
	ctx.Async(func() {
		notes, err := t.Store.ListNotes(context.Background(), t.FolderID)
		ctx.Dispatch(func(ctx app.Context) {
			t.loaded = true
			t.busy = false
			if err != nil {
				t.status = err.Error()
				return
			}
			t.notes, t.status = notes, ""
		})
	})
}

func (t *notesTile) add(ctx app.Context, _ app.Event) {
	if t.busy || t.newTitle == "" {
		return
	}
	t.busy = true
	doc := domain.NoteDoc{Title: t.newTitle, Body: t.newBody, UpdatedAt: time.Now()}
	ctx.Async(func() {
		_, err := t.Store.CreateNote(context.Background(), t.FolderID, doc)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				t.busy, t.status = false, err.Error()
				return
			}
			t.newTitle, t.newBody = "", ""
			t.reload(ctx)
		})
	})
}

func (t *notesTile) Render() app.UI {
	return app.Div().Class("ph-tilecontent").Body(
		app.Div().Class("ph-noteform").Body(
			app.Input().Type("text").Placeholder("Titel").Value(t.newTitle).
				OnInput(func(ctx app.Context, e app.Event) { t.newTitle = ctx.JSSrc().Get("value").String() }),
			app.Textarea().Class("ph-textarea").Placeholder("Notiz…").Text(t.newBody).
				OnInput(func(ctx app.Context, e app.Event) { t.newBody = ctx.JSSrc().Get("value").String() }),
			app.Button().Class("ph-btn").Disabled(t.busy).Text("Speichern").OnClick(t.add),
		),
		app.If(t.status != "", func() app.UI { return app.P().Class("ph-err").Text(t.status) }),
		app.Ul().Class("ph-list").Body(
			app.Range(t.notes).Slice(func(i int) app.UI {
				n := t.notes[i]
				return app.Li().Class("ph-item ph-noteitem").Body(
					app.Div().Body(
						app.Strong().Text(orText(n.Doc.Title, "(ohne Titel)")),
						app.P().Class("ph-muted").Text(n.Doc.Body),
					),
				)
			}),
			app.If(t.loaded && len(t.notes) == 0, func() app.UI {
				return app.Li().Class("ph-muted").Text("Noch keine Notizen.")
			}),
		),
	)
}

// ─── todos ───────────────────────────────────────────────────────────────────────

type todoTile struct {
	app.Compo
	Store    *store.Store
	FolderID string

	todos   []store.Item[domain.TodoItem]
	newText string
	busy    bool
	loaded  bool
	status  string

	editing    string // todo id being edited ("" = none)
	editText   string
	editDue    string // datetime-local value (2006-01-02T15:04)
	editRemind string

	dragID string        // todo id currently being dragged ("" = none)
	stop   chan struct{} // reminder poller lifecycle
}

// dtLayout is the HTML datetime-local value format.
const dtLayout = "2006-01-02T15:04"

func fmtDT(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(dtLayout)
}

func parseDT(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if v, err := time.ParseInLocation(dtLayout, s, time.Local); err == nil {
		return &v
	}
	return nil
}

func (t *todoTile) OnMount(ctx app.Context) {
	t.reload(ctx)
	// Reminder poller: fires OS notifications for due reminders while a todo tile is
	// open. (A reminder only alerts when *some* todo tile for its project is mounted —
	// a fully background scheduler across all projects is future work.)
	t.stop = make(chan struct{})
	ctx.Async(func() {
		for {
			select {
			case <-t.stop:
				return
			case <-time.After(30 * time.Second):
			}
			ctx.Dispatch(func(ctx app.Context) { t.checkReminders(ctx) })
		}
	})
}

func (t *todoTile) OnDismount() {
	if t.stop != nil {
		close(t.stop)
		t.stop = nil
	}
}

// checkReminders alerts + clears any reminder whose time has passed.
func (t *todoTile) checkReminders(ctx app.Context) {
	now := time.Now()
	for i := range t.todos {
		it := &t.todos[i]
		if it.Val.Done || it.Val.RemindAt == nil || it.Val.RemindAt.After(now) {
			continue
		}
		notify("Erinnerung", it.Val.Text)
		id, v := it.ID, it.Val
		v.RemindAt = nil
		it.Val = v // clear locally so it won't re-fire next tick
		ctx.Async(func() { _ = t.Store.UpdateTodo(context.Background(), id, t.FolderID, v) })
	}
}

func (t *todoTile) reload(ctx app.Context) {
	ctx.Async(func() {
		todos, err := t.Store.ListTodos(context.Background(), t.FolderID)
		ctx.Dispatch(func(ctx app.Context) {
			t.loaded, t.busy = true, false
			if err != nil {
				t.status = err.Error()
				return
			}
			t.todos, t.status = todos, ""
		})
	})
}

func (t *todoTile) add(ctx app.Context, _ app.Event) {
	if t.busy || t.newText == "" {
		return
	}
	t.busy = true
	item := domain.TodoItem{Text: t.newText, CreatedAt: time.Now()}
	ctx.Async(func() {
		_, err := t.Store.CreateTodo(context.Background(), t.FolderID, item)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				t.busy, t.status = false, err.Error()
				return
			}
			t.newText = ""
			t.reload(ctx)
		})
	})
}

// mutate applies fn to the todo with the given id, updates it locally (optimistic)
// and persists it; on error it surfaces the message and reloads.
func (t *todoTile) mutate(ctx app.Context, id string, fn func(v *domain.TodoItem)) {
	var out *domain.TodoItem
	for i := range t.todos {
		if t.todos[i].ID == id {
			fn(&t.todos[i].Val)
			v := t.todos[i].Val
			out = &v
		}
	}
	if out == nil {
		return
	}
	v := *out
	ctx.Async(func() {
		if err := t.Store.UpdateTodo(context.Background(), id, t.FolderID, v); err != nil {
			ctx.Dispatch(func(ctx app.Context) { t.status = err.Error(); t.reload(ctx) })
		}
	})
}

func (t *todoTile) toggle(id string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		t.mutate(ctx, id, func(v *domain.TodoItem) {
			v.Done = !v.Done
			if v.Done {
				v.DoneAt = time.Now()
			} else {
				v.DoneAt = time.Time{}
			}
		})
	}
}

func (t *todoTile) remove(id string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		ctx.Async(func() {
			err := t.Store.DeleteTodo(context.Background(), id)
			ctx.Dispatch(func(ctx app.Context) {
				if err != nil {
					t.status = err.Error()
					return
				}
				t.reload(ctx)
			})
		})
	}
}

// ─ edit (text + deadline + reminder) ─

func (t *todoTile) startEdit(it store.Item[domain.TodoItem]) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		t.editing = it.ID
		t.editText = it.Val.Text
		t.editDue = fmtDT(it.Val.DueAt)
		t.editRemind = fmtDT(it.Val.RemindAt)
	}
}

func (t *todoTile) cancelEdit(ctx app.Context, _ app.Event) { t.editing = "" }

func (t *todoTile) saveEdit(id string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		text := strings.TrimSpace(t.editText)
		if text == "" {
			return
		}
		due, remind := parseDT(t.editDue), parseDT(t.editRemind)
		t.editing = ""
		t.mutate(ctx, id, func(v *domain.TodoItem) {
			v.Text, v.DueAt, v.RemindAt = text, due, remind
		})
	}
}

// ─ drag reorder ─

func (t *todoTile) reorder(ctx app.Context, fromID, toID string) {
	if fromID == "" || fromID == toID {
		return
	}
	fromIdx, toIdx := -1, -1
	for i, it := range t.todos {
		switch it.ID {
		case fromID:
			fromIdx = i
		case toID:
			toIdx = i
		}
	}
	if fromIdx < 0 || toIdx < 0 {
		return
	}
	moved := t.todos[fromIdx]
	t.todos = append(t.todos[:fromIdx], t.todos[fromIdx+1:]...)
	if fromIdx < toIdx {
		toIdx--
	}
	rest := append([]store.Item[domain.TodoItem]{moved}, t.todos[toIdx:]...)
	t.todos = append(t.todos[:toIdx], rest...)
	// Reassign 1-based positions and persist the ones that changed. New todos keep
	// Order 0 so they sort above any manually-ordered items (newest-first default).
	for i := range t.todos {
		if t.todos[i].Val.Order == i+1 {
			continue
		}
		t.todos[i].Val.Order = i + 1
		id, v := t.todos[i].ID, t.todos[i].Val
		ctx.Async(func() {
			if err := t.Store.UpdateTodo(context.Background(), id, t.FolderID, v); err != nil {
				ctx.Dispatch(func(ctx app.Context) { t.status = err.Error() })
			}
		})
	}
}

func (t *todoTile) openCount() int {
	n := 0
	for _, it := range t.todos {
		if !it.Val.Done {
			n++
		}
	}
	return n
}

func (t *todoTile) Render() app.UI {
	return app.Div().Class("ph-tilecontent").Body(
		app.Div().Class("ph-todoform").Body(
			app.Input().Class("ph-todoinput").Type("text").Placeholder("Neue Aufgabe…").Value(t.newText).
				OnInput(func(ctx app.Context, e app.Event) { t.newText = ctx.JSSrc().Get("value").String() }).
				OnKeyDown(func(ctx app.Context, e app.Event) {
					if e.Get("key").String() == "Enter" {
						t.add(ctx, e)
					}
				}),
			app.If(strings.TrimSpace(t.newText) != "", func() app.UI {
				return app.Button().Class("ph-tile-btn ph-clear").Title("Feld leeren").Text("✕").
					OnClick(func(ctx app.Context, _ app.Event) { t.newText = "" })
			}),
			app.Button().Class("ph-btn").Disabled(t.busy).Text("+").OnClick(t.add),
		),
		app.If(t.status != "", func() app.UI { return app.P().Class("ph-err").Text(t.status) }),
		app.Ul().Class("ph-list ph-todolist").Body(
			app.Range(t.todos).Slice(func(i int) app.UI {
				return &todoRow{t: t, item: t.todos[i]}
			}),
			app.If(t.loaded && len(t.todos) == 0, func() app.UI {
				return app.Li().Class("ph-muted").Text("Keine Aufgaben — oben eine hinzufügen.")
			}),
		),
		app.If(t.loaded && len(t.todos) > 0, func() app.UI {
			return app.Div().Class("ph-todofoot ph-muted").Text(fmt.Sprintf("%d offen · %d gesamt", t.openCount(), len(t.todos)))
		}),
	)
}

// todoRow is a keyed wrapper (CompoID = todo id) so reorder/edit reconcile cleanly
// instead of positionally recycling <li>s (see projectItem for the rationale).
type todoRow struct {
	app.Compo
	t    *todoTile
	item store.Item[domain.TodoItem]
}

func (r *todoRow) CompoID() string { return r.item.ID }

func (r *todoRow) Render() app.UI {
	t, it := r.t, r.item
	if t.editing == it.ID {
		return app.Li().Class("ph-item ph-todoitem ph-todo-editing").Body(
			app.Input().Class("ph-todoinput").Type("text").Value(t.editText).
				OnInput(func(ctx app.Context, e app.Event) { t.editText = ctx.JSSrc().Get("value").String() }).
				OnKeyDown(func(ctx app.Context, e app.Event) {
					if e.Get("key").String() == "Enter" {
						t.saveEdit(it.ID)(ctx, e)
					}
				}),
			app.Label().Class("ph-muted").Text("Fällig").Body(
				app.Input().Type("datetime-local").Value(t.editDue).
					OnInput(func(ctx app.Context, e app.Event) { t.editDue = ctx.JSSrc().Get("value").String() }),
			),
			app.Label().Class("ph-muted").Text("Erinnerung").Body(
				app.Input().Type("datetime-local").Value(t.editRemind).
					OnInput(func(ctx app.Context, e app.Event) { t.editRemind = ctx.JSSrc().Get("value").String() }),
			),
			app.Button().Class("ph-tile-btn").Title("Speichern").Text("✓").OnClick(t.saveEdit(it.ID)),
			app.Button().Class("ph-tile-btn").Title("Abbrechen").Text("✕").OnClick(t.cancelEdit),
		)
	}
	cls := "ph-item ph-todoitem"
	if it.Val.Done {
		cls += " ph-todo-done"
	}
	overdue := !it.Val.Done && it.Val.DueAt != nil && it.Val.DueAt.Before(time.Now())
	if overdue {
		cls += " ph-todo-overdue"
	}
	return app.Li().Class(cls).Draggable(true).
		OnDragStart(func(ctx app.Context, _ app.Event) { t.dragID = it.ID }).
		OnDragOver(func(ctx app.Context, e app.Event) { e.PreventDefault() }).
		OnDrop(func(ctx app.Context, e app.Event) {
			e.PreventDefault()
			t.reorder(ctx, t.dragID, it.ID)
			t.dragID = ""
		}).
		Body(
			app.Span().Class("ph-todo-drag").Title("Ziehen zum Umsortieren").Text("⠿"),
			app.Input().Type("checkbox").Class("ph-todocheck").Checked(it.Val.Done).
				OnChange(t.toggle(it.ID)),
			app.Span().Class("ph-todotext").Text(it.Val.Text),
			app.If(it.Val.DueAt != nil, func() app.UI {
				return app.Span().Class("ph-todo-due").Text("⏰ " + it.Val.DueAt.Format("02.01. 15:04"))
			}),
			app.Button().Class("ph-tile-btn ph-todo-edit").Title("Bearbeiten").Text("✎").
				OnClick(t.startEdit(it)),
			app.Button().Class("ph-tile-btn ph-todo-del").Title("löschen").Text("✕").
				OnClick(t.remove(it.ID)),
		)
}

// notify shows an OS desktop notification via the Electron bridge (no-op in the
// hosted browser build where window.phNotify is absent).
func notify(title, body string) {
	if fn := app.Window().Get("phNotify"); fn.Truthy() {
		fn.Invoke(title, body)
	}
}

// ─── files ─────────────────────────────────────────────────────────────────────

// filesTile is the Passbubble ("Tresor") files tile: E2E-encrypted ph-file blobs.
// Supports upload, an empty "+ New File", download, delete, a per-file "→ Lokal"
// (write the blob to disk), and double-click-to-edit (sync to disk, then open in the
// editor). It integrates with the local file-tree tile via these copy actions.
type filesTile struct {
	app.Compo
	Store      *store.Store
	Native     *nativeclient.Client // nil in hosted build → sync/open disabled
	FolderID   string
	LocalRoot  string // default target folder for "→ Lokal" / open
	PaneID     string
	OpenEditor func(ctx app.Context, sourcePaneID, path string)

	files   []store.Item[domain.FileBlob]
	loaded  bool
	status  string
	newName string // "+ New File" name
	syncDir string // target folder for → Lokal / open (defaults to LocalRoot)

	// manual double-click detection (native OnDblClick is unreliable under go-app's
	// re-render cycle — same reason as the file tree).
	lastClickID string
	lastClickAt time.Time
}

// clickFile detects a double-click on a vault file (two clicks within 400ms) and, on
// the second, syncs it to disk and opens it in the editor.
func (t *filesTile) clickFile(ctx app.Context, f store.Item[domain.FileBlob]) {
	if t.Native == nil || t.OpenEditor == nil {
		return
	}
	now := time.Now()
	if t.lastClickID == f.ID && now.Sub(t.lastClickAt) < 400*time.Millisecond {
		t.lastClickID, t.lastClickAt = "", time.Time{}
		t.syncToLocal(ctx, f, func(ctx app.Context, p string) { t.OpenEditor(ctx, t.PaneID, p) })
		return
	}
	t.lastClickID, t.lastClickAt = f.ID, now
}

func (t *filesTile) OnMount(ctx app.Context) {
	t.syncDir = t.LocalRoot
	t.reload(ctx)
}

func (t *filesTile) reload(ctx app.Context) {
	ctx.Async(func() {
		files, err := t.Store.ListFiles(context.Background(), t.FolderID)
		ctx.Dispatch(func(ctx app.Context) {
			t.loaded = true
			if err != nil {
				t.status = err.Error()
				return
			}
			t.files, t.status = files, ""
		})
	})
}

// uploadFile reads the selected file via FileReader, seals + stores it as a ph-file.
func (t *filesTile) uploadFile(ctx app.Context, _ app.Event) {
	files := ctx.JSSrc().Get("files")
	if !files.Truthy() || files.Length() == 0 {
		return
	}
	f := files.Index(0)
	name, mime := f.Get("name").String(), f.Get("type").String()
	reader := app.Window().Get("FileReader").New()
	var onload app.Func
	onload = app.FuncOf(func(this app.Value, args []app.Value) any {
		onload.Release()
		raw, err := decodeDataURL(reader.Get("result").String())
		if err != nil {
			ctx.Dispatch(func(ctx app.Context) { t.status = "Upload: " + err.Error() })
			return nil
		}
		blob := domain.FileBlob{Filename: name, MIME: mime, Size: int64(len(raw)), Bytes: raw}
		ctx.Async(func() {
			_, err := t.Store.CreateFile(context.Background(), t.FolderID, blob)
			ctx.Dispatch(func(ctx app.Context) {
				if err != nil {
					t.status = "Upload: " + err.Error()
					return
				}
				t.reload(ctx)
			})
		})
		return nil
	})
	reader.Set("onload", onload)
	reader.Call("readAsDataURL", f)
}

// newFile creates an empty named ph-file blob (editable after syncing to disk).
func (t *filesTile) newFile(ctx app.Context, _ app.Event) {
	name := strings.TrimSpace(t.newName)
	if name == "" {
		return
	}
	t.newName = ""
	ctx.Async(func() {
		_, err := t.Store.CreateFile(context.Background(), t.FolderID, domain.FileBlob{Filename: name, MIME: "text/plain"})
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				t.status = err.Error()
				return
			}
			t.reload(ctx)
		})
	})
}

func (t *filesTile) remove(id string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		ctx.Async(func() {
			err := t.Store.DeleteItem(context.Background(), id)
			ctx.Dispatch(func(ctx app.Context) {
				if err != nil {
					t.status = err.Error()
					return
				}
				t.reload(ctx)
			})
		})
	}
}

// syncToLocal writes a vault blob to disk under syncDir; on success calls then(path).
func (t *filesTile) syncToLocal(ctx app.Context, f store.Item[domain.FileBlob], then func(ctx app.Context, path string)) {
	if t.Native == nil {
		return
	}
	dir := strings.TrimSpace(t.syncDir)
	if dir == "" {
		t.status = "Zielordner (unten) angeben"
		return
	}
	target := path.Join(dir, f.Val.Filename)
	ctx.Async(func() {
		blob, err := t.Store.GetFile(context.Background(), f.ID)
		if err == nil {
			err = t.Native.WriteFileBytes(context.Background(), target, blob.Bytes)
		}
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				t.status = "→ Lokal: " + err.Error()
				return
			}
			t.status = f.Val.Filename + " → " + target
			if then != nil {
				then(ctx, target)
			}
		})
	})
}

// toLocal is the button handler wrapping syncToLocal.
func (t *filesTile) toLocal(f store.Item[domain.FileBlob], then func(ctx app.Context, path string)) app.EventHandler {
	return func(ctx app.Context, _ app.Event) { t.syncToLocal(ctx, f, then) }
}

func (t *filesTile) Render() app.UI {
	return app.Div().Class("ph-tilecontent").Body(
		app.If(t.status != "", func() app.UI { return app.P().Class("ph-muted").Text(t.status) }),
		app.Div().Class("ph-todoform").Body(
			app.Input().Class("ph-todoinput").Type("text").Placeholder("Neue Datei…").Value(t.newName).
				OnInput(func(ctx app.Context, e app.Event) { t.newName = ctx.JSSrc().Get("value").String() }).
				OnKeyDown(func(ctx app.Context, e app.Event) {
					if e.Get("key").String() == "Enter" {
						t.newFile(ctx, e)
					}
				}),
			app.Button().Class("ph-btn").Text("+ Neu").Disabled(strings.TrimSpace(t.newName) == "").OnClick(t.newFile),
		),
		app.Input().Type("file").OnChange(t.uploadFile),
		app.Ul().Class("ph-list").Body(
			app.Range(t.files).Slice(func(i int) app.UI {
				return &fileRow{t: t, item: t.files[i]}
			}),
			app.If(!t.loaded, func() app.UI {
				return app.Li().Class("ph-muted").Text("Lädt…")
			}).ElseIf(len(t.files) == 0, func() app.UI {
				return app.Li().Class("ph-muted").Text("Keine Dateien — oben hochladen oder + Neu.")
			}),
		),
		app.If(t.Native != nil, func() app.UI {
			return app.Div().Class("ph-ftree-bar").Body(
				app.Span().Class("ph-muted").Text("Ziel:"),
				app.Input().Class("ph-island-input").Type("text").Placeholder("/ziel/ordner").Value(t.syncDir).
					OnInput(func(ctx app.Context, e app.Event) { t.syncDir = ctx.JSSrc().Get("value").String() }),
			)
		}),
	)
}

// fileRow is a keyed row for one vault file (CompoID = entry id).
type fileRow struct {
	app.Compo
	t    *filesTile
	item store.Item[domain.FileBlob]
}

func (r *fileRow) CompoID() string { return r.item.ID }

func (r *fileRow) Render() app.UI {
	t, f := r.t, r.item
	return app.Li().Class("ph-item ph-fileitem").Body(
		app.Div().Body(
			app.Span().Class("ph-title ph-file-open").Title("Doppelklick: im Editor öffnen").Text(f.Val.Filename).
				OnClick(func(ctx app.Context, _ app.Event) { t.clickFile(ctx, f) }),
			app.Span().Class("ph-muted").Text("  "+humanSize(f.Val.Size)),
		),
		app.Div().Body(
			app.A().Class("ph-link").Href(dataURL(f.Val)).Download(f.Val.Filename).Text("↓"),
			app.If(t.Native != nil, func() app.UI {
				return app.Button().Class("ph-link ph-icon-btn").Title("Auf Disk schreiben").OnClick(t.toLocal(f, nil)).Body(icon("save", 14))
			}),
			app.Button().Class("ph-link").Text("✕").OnClick(t.remove(f.ID)),
		),
	)
}

// ─── local file tree ─────────────────────────────────────────────────────────

// fileTreeTile is a local (on-disk) file browser: a lazily-expanded tree rooted at a
// folder it remembers. Double-clicking a file opens it in the editor (find-or-open a
// TileEditor next to this tile). "+ New File/Folder" per directory; a per-file
// "→ Tresor" copies the file into the encrypted Passbubble vault.
type fileTreeTile struct {
	app.Compo
	Native     *nativeclient.Client
	Store      *store.Store
	FolderID   string
	PaneID     string
	Root       string // initial root folder (Params["path"] or project cwd)
	OpenEditor func(ctx app.Context, sourcePaneID, path string)
	OpenTile   func(ctx app.Context, sourcePaneID string, t domain.TileType, params map[string]string)

	rootPath string
	rootEdit string                       // path-bar input
	expanded map[string]bool              // dir path → expanded
	children map[string][]domain.DirEntry // dir path → cached entries
	status   string

	creatingIn string // dir path whose new-entry form is open ("" = none)
	newName    string

	// manual double-click detection (go-app re-renders break native dblclick)
	lastClickPath string
	lastClickAt   time.Time

	// right-click context menu
	menuPath     string // target path ("" = closed)
	menuIsDir    bool
	menuX, menuY float64 // viewport coords to anchor the menu

	dragPath string // path being dragged ("" = none)
}

func (t *fileTreeTile) OnMount(ctx app.Context) {
	t.expanded = map[string]bool{}
	t.children = map[string][]domain.DirEntry{}
	t.rootPath = t.Root
	t.rootEdit = t.Root
	if t.rootPath != "" {
		t.loadDir(ctx, t.rootPath)
	}
}

// loadDir fetches a directory's entries into the cache and marks it expanded.
func (t *fileTreeTile) loadDir(ctx app.Context, dir string) {
	if t.Native == nil || dir == "" {
		return
	}
	ctx.Async(func() {
		entries, err := t.Native.ListDir(context.Background(), dir)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				t.status = err.Error()
				return
			}
			t.status = ""
			t.children[dir] = entries
			t.expanded[dir] = true
		})
	})
}

func (t *fileTreeTile) toggleDir(ctx app.Context, dir string) {
	if t.expanded[dir] {
		t.expanded[dir] = false
		return
	}
	if _, ok := t.children[dir]; ok {
		t.expanded[dir] = true
		return
	}
	t.loadDir(ctx, dir)
}

func (t *fileTreeTile) setRoot(ctx app.Context, dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}
	t.rootPath, t.rootEdit = dir, dir
	t.expanded = map[string]bool{}
	t.children = map[string][]domain.DirEntry{}
	t.loadDir(ctx, dir)
}

// openNew opens the new-entry form for a directory (auto-expanding it).
func (t *fileTreeTile) openNew(dir string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		t.creatingIn, t.newName = dir, ""
		if !t.expanded[dir] {
			t.loadDir(ctx, dir)
		}
	}
}

func (t *fileTreeTile) createEntry(dir string, isDir bool) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		name := strings.TrimSpace(t.newName)
		if name == "" || t.Native == nil {
			return
		}
		target := path.Join(dir, name)
		t.creatingIn, t.newName = "", ""
		ctx.Async(func() {
			var err error
			if isDir {
				err = t.Native.MakeDir(context.Background(), target)
			} else {
				err = t.Native.WriteFile(context.Background(), target, "")
			}
			ctx.Dispatch(func(ctx app.Context) {
				if err != nil {
					t.status = err.Error()
					return
				}
				t.loadDir(ctx, dir) // refresh the folder
			})
		})
	}
}

// syncToVault copies a local file into the encrypted Passbubble vault (bytes fetched
// via the sidecar, sealed in-WASM by the Store).
func (t *fileTreeTile) syncToVault(filePath string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		if t.Native == nil || t.Store == nil {
			return
		}
		ctx.Async(func() {
			data, ctype, err := t.Native.FetchFile(context.Background(), filePath)
			if err != nil {
				ctx.Dispatch(func(ctx app.Context) { t.status = "Lesen: " + err.Error() })
				return
			}
			blob := domain.FileBlob{Filename: path.Base(filePath), MIME: ctype, Size: int64(len(data)), Bytes: data}
			_, err = t.Store.CreateFile(context.Background(), t.FolderID, blob)
			ctx.Dispatch(func(ctx app.Context) {
				if err != nil {
					t.status = "In Tresor: " + err.Error()
					return
				}
				t.status = blob.Filename + " → Tresor ✓"
			})
		})
	}
}

// clickEntry handles a row click: a folder toggles; a file opens in the editor on a
// (manually detected) double-click — go-app's re-render cycle breaks native dblclick,
// so we time two clicks on the same path ourselves.
func (t *fileTreeTile) clickEntry(ctx app.Context, it treeItem) {
	if it.isDir {
		t.toggleDir(ctx, it.path)
		return
	}
	now := time.Now()
	if t.lastClickPath == it.path && now.Sub(t.lastClickAt) < 400*time.Millisecond {
		t.lastClickPath, t.lastClickAt = "", time.Time{}
		if t.OpenEditor != nil {
			t.OpenEditor(ctx, t.PaneID, it.path)
		}
		return
	}
	t.lastClickPath, t.lastClickAt = it.path, now
}

// ─ context menu ─

func (t *fileTreeTile) openMenu(p string, isDir bool) app.EventHandler {
	return func(ctx app.Context, e app.Event) {
		e.PreventDefault()
		t.menuPath, t.menuIsDir = p, isDir
		t.menuX, t.menuY = e.Get("clientX").Float(), e.Get("clientY").Float()
	}
}

func (t *fileTreeTile) closeMenu(ctx app.Context, _ app.Event) { t.menuPath = "" }

// menuOpenIn opens the menu's target path in a tile of type tt (editor/markdown/browser).
func (t *fileTreeTile) menuOpenIn(tt domain.TileType, key, val string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		p := t.menuPath
		t.menuPath = ""
		if p == "" || t.OpenTile == nil {
			return
		}
		params := map[string]string{key: val}
		if val == "" {
			params[key] = p
		}
		t.OpenTile(ctx, t.PaneID, tt, params)
	}
}

// menuExternal opens the target in the OS default handler (external app/browser).
func (t *fileTreeTile) menuExternal(ctx app.Context, _ app.Event) {
	p := t.menuPath
	t.menuPath = ""
	if p == "" || t.Native == nil {
		return
	}
	ctx.Async(func() { _ = t.Native.OpenIn(context.Background(), "path", p) })
}

// ─ drag & drop (move within the tree) ─

func (t *fileTreeTile) dragStart(p string) app.EventHandler {
	return func(ctx app.Context, e app.Event) {
		t.dragPath = p // intra-tree move (drop on a folder)
		// Also carry the path in dataTransfer so it survives a drop onto ANOTHER tile
		// (editor/markdown/browser) — see Workspace.dropPathInTile.
		if dt := e.Get("dataTransfer"); dt.Truthy() {
			dt.Call("setData", phPathMime, p)
			dt.Set("effectAllowed", "copyMove")
		}
	}
}

// phPathMime is the dataTransfer type carrying a local file path across tiles.
const phPathMime = "application/x-ph-path"

// dropInto moves the dragged entry into dir.
func (t *fileTreeTile) dropInto(dir string) app.EventHandler {
	return func(ctx app.Context, e app.Event) {
		e.PreventDefault()
		e.Call("stopPropagation")
		src := t.dragPath
		t.dragPath = ""
		if src == "" || t.Native == nil {
			return
		}
		dst := path.Join(dir, path.Base(src))
		if dst == src || dir == src {
			return
		}
		ctx.Async(func() {
			err := t.Native.Move(context.Background(), src, dst)
			ctx.Dispatch(func(ctx app.Context) {
				if err != nil {
					t.status = "Verschieben: " + err.Error()
					return
				}
				t.loadDir(ctx, path.Dir(src)) // refresh source folder
				t.loadDir(ctx, dir)           // refresh destination folder
			})
		})
	}
}

// treeItem is one visible row (post-DFS over expanded dirs).
type treeItem struct {
	path  string
	name  string
	isDir bool
	depth int
}

// visible flattens the expanded tree into an ordered, indented row list.
func (t *fileTreeTile) visible() []treeItem {
	var out []treeItem
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		for _, e := range t.children[dir] {
			p := path.Join(dir, e.Name)
			out = append(out, treeItem{path: p, name: e.Name, isDir: e.IsDir, depth: depth})
			if e.IsDir && t.expanded[p] {
				walk(p, depth+1)
			}
		}
	}
	walk(t.rootPath, 0)
	return out
}

func (t *fileTreeTile) Render() app.UI {
	if t.Native == nil {
		return app.Div().Class("ph-tilecontent").Body(
			app.P().Class("ph-muted").Text("Lokale Dateien nur in der Desktop-App."),
		)
	}
	return app.Div().Class("ph-tilecontent ph-ftree").Body(
		app.Div().Class("ph-ftree-bar").Body(
			app.Input().Class("ph-island-input").Type("text").Placeholder("/pfad/zum/ordner").
				Value(t.rootEdit).
				OnInput(func(ctx app.Context, e app.Event) { t.rootEdit = ctx.JSSrc().Get("value").String() }).
				OnKeyDown(func(ctx app.Context, e app.Event) {
					if e.Get("key").String() == "Enter" {
						t.setRoot(ctx, t.rootEdit)
					}
				}),
			app.Button().Class("ph-tile-btn").Title("Neue Datei/Ordner hier").Text("+").
				OnClick(t.openNew(t.rootPath)),
		),
		app.If(t.status != "", func() app.UI { return app.P().Class("ph-muted ph-ftree-status").Text(t.status) }),
		app.If(t.creatingIn == t.rootPath, t.newForm),
		// The list area doubles as the root drop target (drop here = move to root).
		app.Ul().Class("ph-list ph-ftree-list").
			OnDragOver(func(ctx app.Context, e app.Event) { e.PreventDefault() }).
			OnDrop(t.dropInto(t.rootPath)).
			Body(
				app.Range(t.visible()).Slice(func(i int) app.UI {
					return &treeRow{t: t, item: t.visible()[i]}
				}),
				app.If(len(t.children[t.rootPath]) == 0 && t.rootPath != "", func() app.UI {
					return app.Li().Class("ph-muted").Text("Leerer Ordner.")
				}),
			),
		app.If(t.menuPath != "", t.contextMenu),
	)
}

// contextMenu is the right-click menu for the current t.menuPath. A full-viewport
// backdrop closes it on an outside click (see the +Tile menu for the pattern).
func (t *fileTreeTile) contextMenu() app.UI {
	item := func(label string, h app.EventHandler) app.UI {
		return app.Button().Class("ph-menu-item").Text(label).OnClick(h)
	}
	var items []app.UI
	if t.menuIsDir {
		items = []app.UI{
			item("+ Neue Datei / Ordner", func(ctx app.Context, e app.Event) { p := t.menuPath; t.menuPath = ""; t.openNew(p)(ctx, e) }),
			item("Extern öffnen", t.menuExternal),
		}
	} else {
		items = []app.UI{
			item("Im Editor öffnen", t.menuOpenIn(domain.TileEditor, "path", "")),
			item("Markdown-Vorschau", t.menuOpenIn(domain.TileMarkdown, "path", "")),
			item("Im Browser öffnen", t.menuOpenIn(domain.TileBrowser, "url", "file://"+t.menuPath)),
			item("Extern öffnen", t.menuExternal),
			item("→ In Tresor kopieren", func(ctx app.Context, e app.Event) { p := t.menuPath; t.menuPath = ""; t.syncToVault(p)(ctx, e) }),
		}
	}
	return app.Div().Body(
		app.Div().Class("ph-backdrop").OnClick(t.closeMenu).OnContextMenu(func(ctx app.Context, e app.Event) { e.PreventDefault(); t.menuPath = "" }),
		app.Div().Class("ph-menu ph-ctxmenu").
			Style("left", fmt.Sprintf("%.0fpx", t.menuX)).
			Style("top", fmt.Sprintf("%.0fpx", t.menuY)).
			Body(items...),
	)
}

// newForm renders the inline "new file/folder" input for t.creatingIn.
func (t *fileTreeTile) newForm() app.UI {
	dir := t.creatingIn
	return app.Div().Class("ph-ftree-new").Body(
		app.Input().Class("ph-island-input").Type("text").Placeholder("Name…").Value(t.newName).
			OnInput(func(ctx app.Context, e app.Event) { t.newName = ctx.JSSrc().Get("value").String() }).
			OnKeyDown(func(ctx app.Context, e app.Event) {
				if e.Get("key").String() == "Enter" {
					t.createEntry(dir, false)(ctx, e)
				}
			}),
		app.Button().Class("ph-tile-btn").Title("Datei anlegen").OnClick(t.createEntry(dir, false)).Body(icon("file", 15)),
		app.Button().Class("ph-tile-btn").Title("Ordner anlegen").OnClick(t.createEntry(dir, true)).Body(icon("folder", 15)),
		app.Button().Class("ph-tile-btn").Title("Abbrechen").Text("✕").
			OnClick(func(ctx app.Context, _ app.Event) { t.creatingIn = "" }),
	)
}

// treeRow is a keyed row for one file/folder (CompoID = full path).
type treeRow struct {
	app.Compo
	t    *fileTreeTile
	item treeItem
}

func (r *treeRow) CompoID() string { return r.item.path }

func (r *treeRow) Render() app.UI {
	t, it := r.t, r.item
	iconName := "file"
	if it.isDir {
		if t.expanded[it.path] {
			iconName = "folder-open"
		} else {
			iconName = "folder"
		}
	}
	li := app.Li().Class("ph-item ph-ftree-item").
		Style("padding-left", fmt.Sprintf("%.1frem", 0.5+float64(it.depth)*0.9)).
		Draggable(true).
		OnDragStart(t.dragStart(it.path)).
		OnContextMenu(t.openMenu(it.path, it.isDir))
	// Folders are drop targets (move the dragged entry into them).
	if it.isDir {
		li = li.OnDragOver(func(ctx app.Context, e app.Event) { e.PreventDefault() }).
			OnDrop(t.dropInto(it.path))
	}
	row := li.Body(
		app.Span().Class("ph-ftree-name").
			OnClick(func(ctx app.Context, _ app.Event) { t.clickEntry(ctx, it) }).
			Body(
				icon(iconName, 15),
				app.Span().Class("ph-ftree-label").Text(it.name),
			),
		app.If(it.isDir, func() app.UI {
			return app.Button().Class("ph-tile-btn ph-ftree-act").Title("Neu hier").Text("+").OnClick(t.openNew(it.path))
		}).Else(func() app.UI {
			return app.Button().Class("ph-tile-btn ph-ftree-act").Title("In Tresor kopieren").OnClick(t.syncToVault(it.path)).Body(icon("lock", 13))
		}),
	)
	// inline new-entry form directly under the folder it targets
	if it.isDir && t.creatingIn == it.path {
		return app.Div().Body(row, t.newForm())
	}
	return row
}

// ─── Claude sessions ────────────────────────────────────────────────────────────

type sessionsTile struct {
	app.Compo
	Store        *store.Store
	Native       *nativeclient.Client // nil in the hosted build; scans on-disk sessions
	FolderID     string
	Cwd          string
	OpenTerminal func(ctx app.Context, cwd, sessionID string)

	sessions []store.Item[domain.CodeSession] // saved (persisted) sessions
	scanned  []domain.CodeSession             // discovered on disk (may include saved ones)
	loaded   bool
	status   string

	newID string // manual "add session id" input

	renaming    string // entry id currently being renamed ("" = none)
	renameTitle string
}

func (t *sessionsTile) setNewID(ctx app.Context, e app.Event) {
	t.newID = ctx.JSSrc().Get("value").String()
}

// addSession persists a manually entered Claude session id so it shows up in the
// list (and can be resumed) even if it wasn't auto-discovered.
func (t *sessionsTile) addSession(ctx app.Context, _ app.Event) {
	id := strings.TrimSpace(t.newID)
	if id == "" {
		return
	}
	t.newID = ""
	t.persist(ctx, domain.CodeSession{SessionID: id, Cwd: t.Cwd, LastActive: time.Now()})
}

// addScanned saves a discovered-but-unsaved session into the project.
func (t *sessionsTile) addScanned(cs domain.CodeSession) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		if cs.Cwd == "" {
			cs.Cwd = t.Cwd
		}
		t.persist(ctx, cs)
	}
}

// persist stores a CodeSession and prepends it to the saved list on success.
func (t *sessionsTile) persist(ctx app.Context, cs domain.CodeSession) {
	t.status = ""
	ctx.Async(func() {
		entryID, err := t.Store.CreateCodeSession(context.Background(), t.FolderID, cs)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				t.status = err.Error()
				return
			}
			t.sessions = append([]store.Item[domain.CodeSession]{{ID: entryID, Val: cs}}, t.sessions...)
		})
	})
}

// ─ rename ─

func (t *sessionsTile) startRename(id, title string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) { t.renaming, t.renameTitle = id, title }
}

func (t *sessionsTile) cancelRename(ctx app.Context, _ app.Event) { t.renaming = "" }

func (t *sessionsTile) saveRename(id, cwd, sessionID string, lastActive time.Time) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		cs := domain.CodeSession{SessionID: sessionID, Title: strings.TrimSpace(t.renameTitle), Cwd: cwd, LastActive: lastActive}
		t.renaming = ""
		for i := range t.sessions {
			if t.sessions[i].ID == id {
				t.sessions[i].Val = cs
			}
		}
		ctx.Async(func() {
			if err := t.Store.UpdateCodeSession(context.Background(), id, t.FolderID, cs); err != nil {
				ctx.Dispatch(func(ctx app.Context) { t.status = err.Error() })
			}
		})
	}
}

func (t *sessionsTile) OnMount(ctx app.Context) {
	ctx.Async(func() {
		sessions, err := t.Store.ListCodeSessions(context.Background(), t.FolderID)
		ctx.Dispatch(func(ctx app.Context) {
			t.loaded = true
			if err != nil {
				t.status = err.Error()
				return
			}
			t.sessions = sessions
		})
	})
	// Scan the working dir for Claude sessions on disk (desktop shell only).
	if t.Native != nil && t.Cwd != "" {
		ctx.Async(func() {
			scanned, err := t.Native.Sessions(context.Background(), t.Cwd)
			if err != nil {
				return // best-effort: no scan just means no "discovered" section
			}
			ctx.Dispatch(func(ctx app.Context) { t.scanned = scanned })
		})
	}
}

// unsavedScanned returns discovered sessions not already in the saved list.
func (t *sessionsTile) unsavedScanned() []domain.CodeSession {
	saved := make(map[string]bool, len(t.sessions))
	for _, s := range t.sessions {
		saved[s.Val.SessionID] = true
	}
	var out []domain.CodeSession
	for _, cs := range t.scanned {
		if !saved[cs.SessionID] {
			out = append(out, cs)
		}
	}
	return out
}

func (t *sessionsTile) Render() app.UI {
	return app.Div().Class("ph-tilecontent").Body(
		app.If(t.status != "", func() app.UI { return app.P().Class("ph-err").Text(t.status) }),
		app.Div().Class("ph-todoform").Body(
			app.Input().Class("ph-todoinput").Type("text").Placeholder("Claude Session-ID…").
				Value(t.newID).OnInput(t.setNewID).
				OnKeyDown(func(ctx app.Context, e app.Event) {
					if e.Get("key").String() == "Enter" {
						t.addSession(ctx, e)
					}
				}),
			app.If(strings.TrimSpace(t.newID) != "", func() app.UI {
				return app.Button().Class("ph-tile-btn ph-clear").Title("Feld leeren").Text("✕").
					OnClick(func(ctx app.Context, _ app.Event) { t.newID = "" })
			}),
			app.Button().Class("ph-btn").Text("+ Add").Disabled(strings.TrimSpace(t.newID) == "").
				OnClick(t.addSession),
		),
		app.Ul().Class("ph-list").Body(
			app.Range(t.sessions).Slice(func(i int) app.UI {
				s := t.sessions[i]
				return &sessionRow{t: t, item: s}
			}),
			app.If(t.loaded && len(t.sessions) == 0, func() app.UI {
				return app.Li().Class("ph-muted").Text("Keine gespeicherten Sessions.")
			}),
		),
		// Discovered-but-unsaved sessions: greyed, one click to add.
		app.If(len(t.unsavedScanned()) > 0, func() app.UI {
			return app.Div().Body(
				app.Div().Class("ph-muted ph-scanned-h").Text("Gefundene Sessions"),
				app.Ul().Class("ph-list").Body(
					app.Range(t.unsavedScanned()).Slice(func(i int) app.UI {
						cs := t.unsavedScanned()[i]
						return &scannedRow{t: t, cs: cs}
					}),
				),
			)
		}),
	)
}

// sessionRow is a keyed wrapper for one saved session (see projectItem rationale).
type sessionRow struct {
	app.Compo
	t    *sessionsTile
	item store.Item[domain.CodeSession]
}

func (r *sessionRow) CompoID() string { return r.item.ID }

func (r *sessionRow) Render() app.UI {
	t, s := r.t, r.item
	cwd := s.Val.Cwd
	if cwd == "" {
		cwd = t.Cwd
	}
	if t.renaming == s.ID {
		return app.Li().Class("ph-item").Body(
			app.Input().Class("ph-todoinput").Type("text").Value(t.renameTitle).
				OnInput(func(ctx app.Context, e app.Event) { t.renameTitle = ctx.JSSrc().Get("value").String() }).
				OnKeyDown(func(ctx app.Context, e app.Event) {
					if e.Get("key").String() == "Enter" {
						t.saveRename(s.ID, cwd, s.Val.SessionID, s.Val.LastActive)(ctx, e)
					}
				}),
			app.Button().Class("ph-tile-btn").Title("Speichern").Text("✓").
				OnClick(t.saveRename(s.ID, cwd, s.Val.SessionID, s.Val.LastActive)),
			app.Button().Class("ph-tile-btn").Title("Abbrechen").Text("✕").OnClick(t.cancelRename),
		)
	}
	return app.Li().Class("ph-item").Body(
		app.Div().Class("ph-suggest-info").Body(
			app.Span().Class("ph-title").Text(orText(s.Val.Title, s.Val.SessionID)),
			app.Span().Class("ph-muted").Text(s.Val.LastActive.Format("2006-01-02 15:04")),
		),
		app.Button().Class("ph-tile-btn").Title("Umbenennen").Text("✎").
			OnClick(t.startRename(s.ID, s.Val.Title)),
		app.Button().Class("ph-btn").Text("Resume").OnClick(func(ctx app.Context, _ app.Event) {
			if t.OpenTerminal != nil {
				t.OpenTerminal(ctx, cwd, s.Val.SessionID)
			}
		}),
	)
}

// scannedRow is a keyed wrapper for one discovered-but-unsaved session.
type scannedRow struct {
	app.Compo
	t  *sessionsTile
	cs domain.CodeSession
}

func (r *scannedRow) CompoID() string { return r.cs.SessionID }

func (r *scannedRow) Render() app.UI {
	cs := r.cs
	return app.Li().Class("ph-item ph-scanned").Body(
		app.Div().Class("ph-suggest-info").Body(
			app.Span().Class("ph-title").Text(orText(cs.Title, cs.SessionID)),
			app.Span().Class("ph-muted").Text(cs.LastActive.Format("2006-01-02 15:04")),
		),
		app.Button().Class("ph-btn").Text("+ Hinzufügen").OnClick(r.t.addScanned(cs)),
	)
}

// ─── Passbubble entry links ───────────────────────────────────────────────────

// passbubbleTile links this project to the user's OTHER Passbubble vault entries
// (logins/notes made in the Passbubble app) on the same server. Only a reference is
// stored; secret content is decrypted on demand in the browser, never copied in.
type passbubbleTile struct {
	app.Compo
	Store    *store.Store
	FolderID string

	links  []store.Item[domain.PassbubbleLink]
	loaded bool
	status string

	pickerOpen bool
	foreign    []store.ForeignEntry
	foreignErr string
	filter     string
	reveal     map[string]map[string]string // entryID → decrypted fields (revealed on demand)
}

func (t *passbubbleTile) OnMount(ctx app.Context) {
	t.reveal = map[string]map[string]string{}
	t.reload(ctx)
}

func (t *passbubbleTile) reload(ctx app.Context) {
	ctx.Async(func() {
		links, err := t.Store.ListPassbubbleLinks(context.Background(), t.FolderID)
		ctx.Dispatch(func(ctx app.Context) {
			t.loaded = true
			if err != nil {
				t.status = err.Error()
				return
			}
			t.links, t.status = links, ""
		})
	})
}

func (t *passbubbleTile) togglePicker(ctx app.Context, _ app.Event) {
	t.pickerOpen = !t.pickerOpen
	if !t.pickerOpen || len(t.foreign) > 0 {
		return
	}
	t.foreignErr = ""
	ctx.Async(func() {
		entries, err := t.Store.ListForeignEntries(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				t.foreignErr = err.Error()
				return
			}
			t.foreign = entries
		})
	})
}

// filteredForeign returns foreign entries not already linked, matching the filter.
func (t *passbubbleTile) filteredForeign() []store.ForeignEntry {
	linked := make(map[string]bool, len(t.links))
	for _, l := range t.links {
		linked[l.Val.EntryID] = true
	}
	q := strings.ToLower(strings.TrimSpace(t.filter))
	var out []store.ForeignEntry
	for _, fe := range t.foreign {
		if linked[fe.ID] {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(fe.Title), q) && !strings.Contains(strings.ToLower(fe.Folder), q) {
			continue
		}
		out = append(out, fe)
	}
	return out
}

func (t *passbubbleTile) addLink(fe store.ForeignEntry) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		link := domain.PassbubbleLink{EntryID: fe.ID, Title: fe.Title, EntryType: fe.Type, Folder: fe.Folder, LinkedAt: time.Now()}
		ctx.Async(func() {
			_, err := t.Store.CreatePassbubbleLink(context.Background(), t.FolderID, link)
			ctx.Dispatch(func(ctx app.Context) {
				if err != nil {
					t.status = err.Error()
					return
				}
				t.pickerOpen = false
				t.reload(ctx)
			})
		})
	}
}

func (t *passbubbleTile) removeLink(id string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		ctx.Async(func() {
			err := t.Store.DeletePassbubbleLink(context.Background(), id)
			ctx.Dispatch(func(ctx app.Context) {
				if err != nil {
					t.status = err.Error()
					return
				}
				t.reload(ctx)
			})
		})
	}
}

// revealToggle decrypts a linked entry's fields on demand (or hides them again).
func (t *passbubbleTile) revealToggle(entryID string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		if _, ok := t.reveal[entryID]; ok {
			delete(t.reveal, entryID)
			return
		}
		ctx.Async(func() {
			fields, err := t.Store.GetForeignEntry(context.Background(), entryID)
			ctx.Dispatch(func(ctx app.Context) {
				if err != nil {
					t.status = "Entschlüsseln fehlgeschlagen: " + err.Error()
					return
				}
				t.reveal[entryID] = fields
			})
		})
	}
}

func (t *passbubbleTile) Render() app.UI {
	return app.Div().Class("ph-tilecontent").Body(
		app.If(t.status != "", func() app.UI { return app.P().Class("ph-err").Text(t.status) }),
		app.P().Class("ph-muted").Text("Logins/Notizen aus deinem Passbubble-Tresor. Inhalte werden erst beim Anzeigen entschlüsselt."),
		app.Ul().Class("ph-list").Body(
			app.Range(t.links).Slice(func(i int) app.UI {
				return &pbLinkRow{t: t, item: t.links[i]}
			}),
			app.If(t.loaded && len(t.links) == 0, func() app.UI {
				return app.Li().Class("ph-muted").Text("Keine verknüpften Einträge.")
			}),
		),
		app.Button().Class("ph-btn").Text("+ Eintrag verknüpfen").OnClick(t.togglePicker),
		app.If(t.pickerOpen, func() app.UI {
			filtered := t.filteredForeign()
			return app.Div().Class("ph-pbpicker").Body(
				app.Input().Type("text").Class("ph-todoinput").Placeholder("Einträge filtern…").
					Value(t.filter).OnInput(func(ctx app.Context, e app.Event) { t.filter = ctx.JSSrc().Get("value").String() }),
				app.If(t.foreignErr != "", func() app.UI { return app.P().Class("ph-err").Text(t.foreignErr) }),
				app.Ul().Class("ph-list").Body(
					app.Range(filtered).Slice(func(i int) app.UI {
						fe := filtered[i]
						return app.Li().Class("ph-item").Body(
							app.Div().Body(
								app.Strong().Text(orText(fe.Title, "(ohne Titel)")),
								app.Span().Class("ph-muted").Text("  "+orText(fe.Type, "eintrag")+foreignFolderSuffix(fe.Folder)),
							),
							app.If(!fe.Owned, func() app.UI {
								return app.Span().Class("ph-muted").Text("geteilt")
							}).Else(func() app.UI {
								return app.Button().Class("ph-link").Text("+ verknüpfen").OnClick(t.addLink(fe))
							}),
						)
					}),
					app.If(len(filtered) == 0, func() app.UI {
						return app.Li().Class("ph-muted").Text("Keine passenden Einträge.")
					}),
				),
			)
		}),
	)
}

// pbLinkRow is a keyed wrapper for one linked Passbubble entry (see projectItem).
type pbLinkRow struct {
	app.Compo
	t    *passbubbleTile
	item store.Item[domain.PassbubbleLink]
}

func (r *pbLinkRow) CompoID() string { return r.item.ID }

func (r *pbLinkRow) Render() app.UI {
	t, l := r.t, r.item
	revealed, isOpen := t.reveal[l.Val.EntryID]
	revealLabel := "anzeigen"
	if isOpen {
		revealLabel = "verbergen"
	}
	return app.Li().Class("ph-item ph-pblink").Body(
		app.Div().Class("ph-pblink-main").Body(
			app.Div().Body(
				app.Strong().Text(orText(l.Val.Title, "(ohne Titel)")),
				app.Span().Class("ph-muted").Text("  "+orText(l.Val.EntryType, "eintrag")+foreignFolderSuffix(l.Val.Folder)),
			),
			app.If(isOpen, func() app.UI {
				return app.Div().Class("ph-pblink-fields").Body(
					app.Range(revealed).Map(func(k string) app.UI {
						return app.Div().Class("ph-pblink-field").Body(
							app.Span().Class("ph-muted ph-pblink-key").Text(k+": "),
							app.Span().Class("ph-pblink-val").Text(revealed[k]),
						)
					}),
				)
			}),
		),
		app.Div().Body(
			app.Button().Class("ph-link").Text(revealLabel).OnClick(t.revealToggle(l.Val.EntryID)),
			app.Button().Class("ph-link").Text("entfernen").OnClick(t.removeLink(l.ID)),
		),
	)
}

// ─── helpers ───────────────────────────────────────────────────────────────────

func foreignFolderSuffix(folder string) string {
	if folder == "" {
		return ""
	}
	return " · " + folder
}

func orText(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
