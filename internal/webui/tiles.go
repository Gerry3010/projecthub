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
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/store"
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
}

func (t *todoTile) OnMount(ctx app.Context) { t.reload(ctx) }

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

func (t *todoTile) toggle(id string, it domain.TodoItem) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		it.Done = !it.Done
		if it.Done {
			it.DoneAt = time.Now()
		} else {
			it.DoneAt = time.Time{}
		}
		// optimistic local update, then persist
		for i := range t.todos {
			if t.todos[i].ID == id {
				t.todos[i].Val = it
			}
		}
		ctx.Async(func() {
			err := t.Store.UpdateTodo(context.Background(), id, t.FolderID, it)
			if err != nil {
				ctx.Dispatch(func(ctx app.Context) { t.status = err.Error(); t.reload(ctx) })
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
			app.Button().Class("ph-btn").Disabled(t.busy).Text("+").OnClick(t.add),
		),
		app.If(t.status != "", func() app.UI { return app.P().Class("ph-err").Text(t.status) }),
		app.Ul().Class("ph-list ph-todolist").Body(
			app.Range(t.todos).Slice(func(i int) app.UI {
				td := t.todos[i]
				cls := "ph-item ph-todoitem"
				if td.Val.Done {
					cls += " ph-todo-done"
				}
				return app.Li().Class(cls).Body(
					app.Input().Type("checkbox").Class("ph-todocheck").Checked(td.Val.Done).
						OnChange(t.toggle(td.ID, td.Val)),
					app.Span().Class("ph-todotext").Text(td.Val.Text),
					app.Button().Class("ph-tile-btn ph-todo-del").Title("löschen").Text("✕").
						OnClick(t.remove(td.ID)),
				)
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

// ─── files ─────────────────────────────────────────────────────────────────────

type filesTile struct {
	app.Compo
	Store    *store.Store
	FolderID string

	files  []store.Item[domain.FileBlob]
	loaded bool
	status string
}

func (t *filesTile) OnMount(ctx app.Context) {
	ctx.Async(func() {
		files, err := t.Store.ListFiles(context.Background(), t.FolderID)
		ctx.Dispatch(func(ctx app.Context) {
			t.loaded = true
			if err != nil {
				t.status = err.Error()
				return
			}
			t.files = files
		})
	})
}

func (t *filesTile) Render() app.UI {
	return app.Div().Class("ph-tilecontent").Body(
		app.If(t.status != "", func() app.UI { return app.P().Class("ph-err").Text(t.status) }),
		app.Ul().Class("ph-list").Body(
			app.Range(t.files).Slice(func(i int) app.UI {
				f := t.files[i]
				return app.Li().Class("ph-item").Body(
					app.Span().Class("ph-title").Text(f.Val.Filename),
					app.Span().Class("ph-muted").Text(humanSize(f.Val.Size)),
				)
			}),
			app.If(t.loaded && len(t.files) == 0, func() app.UI {
				return app.Li().Class("ph-muted").Text("Keine Dateien.")
			}),
		),
	)
}

// ─── Claude sessions ────────────────────────────────────────────────────────────

type sessionsTile struct {
	app.Compo
	Store        *store.Store
	FolderID     string
	Cwd          string
	OpenTerminal func(ctx app.Context, cwd, sessionID string)

	sessions []store.Item[domain.CodeSession]
	loaded   bool
	status   string
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
}

func (t *sessionsTile) Render() app.UI {
	return app.Div().Class("ph-tilecontent").Body(
		app.If(t.status != "", func() app.UI { return app.P().Class("ph-err").Text(t.status) }),
		app.Ul().Class("ph-list").Body(
			app.Range(t.sessions).Slice(func(i int) app.UI {
				s := t.sessions[i]
				cwd := s.Val.Cwd
				if cwd == "" {
					cwd = t.Cwd
				}
				return app.Li().Class("ph-item").Body(
					app.Div().Class("ph-suggest-info").Body(
						app.Span().Class("ph-title").Text(orText(s.Val.Title, s.Val.SessionID)),
						app.Span().Class("ph-muted").Text(s.Val.LastActive.Format("2006-01-02 15:04")),
					),
					app.Button().Class("ph-btn").Text("Resume").OnClick(func(ctx app.Context, _ app.Event) {
						if t.OpenTerminal != nil {
							t.OpenTerminal(ctx, cwd, s.Val.SessionID)
						}
					}),
				)
			}),
			app.If(t.loaded && len(t.sessions) == 0, func() app.UI {
				return app.Li().Class("ph-muted").Text("Keine gespeicherten Sessions.")
			}),
		),
	)
}

// ─── helpers ───────────────────────────────────────────────────────────────────

func orText(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
