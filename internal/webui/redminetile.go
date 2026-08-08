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
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/store"
	"github.com/Gerry3010/projecthub/internal/native/nativeclient"
)

// redmineTile shows this project's open Redmine issues (newest-updated first) and, when
// unconfigured or edited, an inline config form for the instance's Base-URL, API key and
// optional project filter. It needs Store (to read/write the RedmineLink, E2E-encrypted
// in Passbubble) and Native (the sidecar's same-origin Redmine relay — Redmine has no
// CORS and the API key must not leak to arbitrary JS origins). The config lives in the
// tile itself (not the dead ProjectPage) so it's reachable in the live workspace.
type redmineTile struct {
	app.Compo
	Store    *store.Store
	Native   *nativeclient.Client
	FolderID string

	loaded     bool
	configured bool
	editing    bool // config form visible
	saving     bool
	status     string
	issues     []domain.RedmineIssue

	// config form (mirrors domain.RedmineLink)
	fBase    string
	fKey     string
	fProject string
}

func (t *redmineTile) OnMount(ctx app.Context) {
	if t.Native == nil || t.Store == nil {
		t.loaded = true
		return
	}
	native, st, folderID := t.Native, t.Store, t.FolderID
	ctx.Async(func() {
		bg := context.Background()
		link, err := st.GetRedmineLink(bg, folderID)
		if err != nil {
			ctx.Dispatch(func(ctx app.Context) { t.loaded, t.status = true, err.Error() })
			return
		}
		if link == nil || link.Val.BaseURL == "" {
			ctx.Dispatch(func(ctx app.Context) { t.loaded, t.editing = true, true })
			return
		}
		l := link.Val
		issues, ierr := native.RedmineIssues(bg, l.BaseURL, l.APIKey, l.ProjectID)
		ctx.Dispatch(func(ctx app.Context) {
			t.loaded, t.configured = true, true
			t.fBase, t.fKey, t.fProject = l.BaseURL, l.APIKey, l.ProjectID
			if ierr != nil {
				t.status = "Redmine laden fehlgeschlagen: " + ierr.Error()
				return
			}
			t.issues, t.status = issues, ""
		})
	})
}

// save persists the config to the vault and reloads the issues.
func (t *redmineTile) save(ctx app.Context, _ app.Event) {
	base := strings.TrimSpace(t.fBase)
	if base == "" {
		t.status = "Base-URL fehlt."
		return
	}
	link := domain.RedmineLink{
		BaseURL:   base,
		APIKey:    strings.TrimSpace(t.fKey),
		ProjectID: strings.TrimSpace(t.fProject),
		LinkedAt:  time.Now(),
	}
	t.saving, t.status = true, ""
	st, native, folderID := t.Store, t.Native, t.FolderID
	ctx.Async(func() {
		bg := context.Background()
		if _, err := st.SetRedmineLink(bg, folderID, link); err != nil {
			ctx.Dispatch(func(ctx app.Context) { t.saving, t.status = false, "Speichern fehlgeschlagen: "+err.Error() })
			return
		}
		issues, ierr := native.RedmineIssues(bg, link.BaseURL, link.APIKey, link.ProjectID)
		ctx.Dispatch(func(ctx app.Context) {
			t.saving, t.editing, t.configured = false, false, true
			if ierr != nil {
				t.issues, t.status = nil, "Gespeichert, aber Laden fehlgeschlagen: "+ierr.Error()
				return
			}
			t.issues, t.status = issues, ""
		})
	})
}

// openIssue opens the issue's Redmine page in the system browser.
func (t *redmineTile) openIssue(id int) app.EventHandler {
	base, native := strings.TrimRight(t.fBase, "/"), t.Native
	return func(ctx app.Context, _ app.Event) {
		if native == nil || base == "" {
			return
		}
		target := base + "/issues/" + strconv.Itoa(id)
		ctx.Async(func() { _ = native.OpenIn(context.Background(), "url", target) })
	}
}

func (t *redmineTile) Render() app.UI {
	if t.Native == nil || t.Store == nil {
		return app.Div().Class("ph-tilecontent").Body(
			app.P().Class("ph-muted").Text("Redmine ist nur in der ProjectHub-Desktop-App verfügbar."),
		)
	}
	if !t.loaded {
		return app.Div().Class("ph-tilecontent").Body(app.P().Class("ph-muted").Text("Lädt…"))
	}
	if t.editing {
		return t.configForm()
	}
	return app.Div().Class("ph-tilecontent ph-redmine").Body(
		app.Div().Class("ph-redmine-head").Body(
			app.Span().Class("ph-muted").Text(strings.TrimPrefix(strings.TrimPrefix(t.fBase, "https://"), "http://")),
			app.Button().Class("ph-tile-btn").Title("Einstellungen").Text("⚙").
				OnClick(func(ctx app.Context, _ app.Event) { t.editing = true }),
		),
		app.If(t.status != "", func() app.UI { return app.P().Class("ph-err").Text(t.status) }),
		app.Ul().Class("ph-list").Body(
			app.Range(t.issues).Slice(func(i int) app.UI {
				is := t.issues[i]
				return app.Li().Class("ph-item ph-redmine-issue").OnClick(t.openIssue(is.ID)).Body(
					app.Span().Class("ph-redmine-id").Text("#"+strconv.Itoa(is.ID)),
					app.Div().Class("ph-suggest-info").Body(
						app.Span().Class("ph-title").Text(is.Subject),
						app.Span().Class("ph-muted").Text(redmineMeta(is)),
					),
				)
			}),
			app.If(len(t.issues) == 0, func() app.UI {
				return app.Li().Class("ph-muted").Text("Keine offenen Tickets.")
			}),
		),
	)
}

func (t *redmineTile) configForm() app.UI {
	saveLabel := "Speichern"
	if t.saving {
		saveLabel = "Speichert…"
	}
	return app.Div().Class("ph-tilecontent ph-redmine ph-redmine-config").Body(
		app.P().Class("ph-eyebrow").Text("Redmine verbinden"),
		app.If(t.status != "", func() app.UI { return app.P().Class("ph-err").Text(t.status) }),
		app.Input().Class("ph-input").Type("text").Placeholder("Base-URL (https://redmine.example.com)").
			Value(t.fBase).OnInput(bindInput(&t.fBase)),
		app.Input().Class("ph-input").Type("password").Placeholder("API-Key (Redmine-Kontoeinstellung)").
			Value(t.fKey).OnInput(bindInput(&t.fKey)),
		app.Input().Class("ph-input").Type("text").Placeholder("Projekt-ID / Identifier (optional)").
			Value(t.fProject).OnInput(bindInput(&t.fProject)),
		app.Div().Class("ph-redmine-actions").Body(
			app.Button().Class("ph-btn").Disabled(t.saving || strings.TrimSpace(t.fBase) == "").
				Text(saveLabel).OnClick(t.save),
			app.If(t.configured, func() app.UI {
				return app.Button().Class("ph-link").Text("Abbrechen").
					OnClick(func(ctx app.Context, _ app.Event) { t.editing, t.status = false, "" })
			}),
		),
		app.P().Class("ph-settings-note").Text("Der API-Key wird E2E-verschlüsselt in Passbubble gespeichert und nur über den lokalen Sidecar an Redmine gesendet."),
	)
}

// redmineMeta builds the issue row's subtitle: "<tracker> · <status> · <priority>".
func redmineMeta(is domain.RedmineIssue) string {
	parts := make([]string, 0, 3)
	if is.Tracker.Name != "" {
		parts = append(parts, is.Tracker.Name)
	}
	if is.Status.Name != "" {
		parts = append(parts, is.Status.Name)
	}
	if is.Priority.Name != "" {
		parts = append(parts, is.Priority.Name)
	}
	return strings.Join(parts, " · ")
}
