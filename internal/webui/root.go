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

// Package webui holds the go-app (WASM) frontend components for ProjectHub. All
// encryption happens here in the browser: the master password is turned into the
// master key via Argon2id and the private keys are decrypted locally — the
// ProjectHub server (reached via the same-origin /pb proxy) only ever sees
// ciphertext and the bearer token.
package webui

import (
	"context"
	"encoding/base64"
	"path"
	"strconv"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/core/crypto"
	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/pbclient"
	"github.com/Gerry3010/projecthub/internal/core/store"
	"github.com/Gerry3010/projecthub/internal/native/nativeclient"
)

// apiBase is the same-origin reverse-proxy prefix the chi server exposes for the
// Passbubble API.
const apiBase = "/pb"

// Root is the top-level component: it shows the login/unlock screen until the
// vault is unlocked, then the project list.
type Root struct {
	app.Compo

	// login form state
	email    string
	password string
	status   string // error/info message

	// session state (populated after unlock)
	busy     bool
	unlocked bool
	store    *store.Store
	projects []domain.ProjectRef
	selected *domain.ProjectRef // non-nil → showing a project's detail page

	// desktop (Electron) integration: nil in the hosted browser build
	native      *nativeclient.Client
	suggestions []nativeclient.ClaudeSuggestion // Claude Code dirs not yet added as projects

	// new-project form
	newTitle string
}

func (r *Root) Render() app.UI {
	switch {
	case !r.unlocked:
		return r.loginView()
	case r.selected != nil:
		return &ProjectPage{Store: r.store, Ref: *r.selected, Back: r.closeProject}
	default:
		return r.projectsView()
	}
}

// openProject shows a project's detail page.
func (r *Root) openProject(ref domain.ProjectRef) func(ctx app.Context, e app.Event) {
	return func(ctx app.Context, _ app.Event) {
		r.selected = &ref
	}
}

// closeProject returns to the project list, refreshing it.
func (r *Root) closeProject(ctx app.Context) {
	r.selected = nil
	r.busy, r.status = true, ""
	ctx.Async(func() { r.reload(ctx, nil) })
}

// ─── login / unlock ─────────────────────────────────────────────────────────

func (r *Root) loginView() app.UI {
	return app.Div().Class("ph-center").Body(
		app.Div().Class("ph-card").Body(
			app.H1().Text("ProjectHub"),
			app.P().Class("ph-muted").Text("Mit deinem Passbubble-Konto anmelden"),
			app.Input().Type("email").Placeholder("E-Mail").Value(r.email).
				Attr("autocomplete", "username").OnInput(r.bind(&r.email)),
			app.Input().Type("password").Placeholder("Master-Passwort").Value(r.password).
				Attr("autocomplete", "current-password").OnInput(r.bind(&r.password)),
			app.Button().Class("ph-btn").Disabled(r.busy).Text(btnLabel(r.busy, "Entsperren", "Entschlüssele…")).
				OnClick(r.login),
			app.If(r.status != "", func() app.UI { return app.P().Class("ph-err").Text(r.status) }),
		),
	)
}

// login authenticates against Passbubble and unlocks the vault entirely in the
// browser. Network + crypto run off the UI goroutine via ctx.Async.
func (r *Root) login(ctx app.Context, _ app.Event) {
	if r.busy {
		return
	}
	r.busy, r.status = true, ""
	email, password := r.email, r.password

	ctx.Async(func() {
		api := pbclient.New(apiBase)
		resp, err := api.Login(context.Background(), pbclient.LoginRequest{Email: email, Password: password})
		if err != nil {
			r.fail(ctx, "Anmeldung fehlgeschlagen: "+err.Error())
			return
		}
		if resp.RequiresTOTP() {
			r.fail(ctx, "2FA ist noch nicht implementiert.")
			return
		}

		salt, e1 := b64d(resp.KDFSalt)
		encX, e2 := b64d(resp.EncPrivX25519)
		encM, e3 := b64d(resp.EncPrivMLKEM768)
		pubX, e4 := b64d(resp.PubX25519)
		pubM, e5 := b64d(resp.PubMLKEM768)
		if err := firstErr(e1, e2, e3, e4, e5); err != nil {
			r.fail(ctx, "Schlüsselmaterial ungültig: "+err.Error())
			return
		}

		keys, err := crypto.Unlock(password, salt, resp.KDFTime, resp.KDFMemory, resp.UserID, encX, encM, pubX, pubM)
		if err != nil {
			r.fail(ctx, err.Error())
			return
		}
		api.SetToken(resp.AccessToken)
		st := store.New(api, keys)

		projects, err := st.ListProjects(context.Background())
		if err != nil {
			r.fail(ctx, "Projekte laden fehlgeschlagen: "+err.Error())
			return
		}

		// Under the Electron shell, offer recently-active Claude Code dirs that
		// aren't projects yet. In the hosted browser build phNative is undefined,
		// so nc is nil and no suggestions appear.
		base, token := readPhNative()
		nc := nativeclient.New(base, token)
		var suggestions []nativeclient.ClaudeSuggestion
		if nc.Available() {
			if sug, err := nc.Suggestions(context.Background()); err == nil {
				suggestions = filterAdded(sug, projects)
			}
		}

		ctx.Dispatch(func(ctx app.Context) {
			r.busy = false
			r.unlocked = true
			r.password = "" // drop the plaintext password from component state
			r.store = st
			r.projects = projects
			r.native = nc
			r.suggestions = suggestions
		})
	})
}

// ─── projects ───────────────────────────────────────────────────────────────

func (r *Root) projectsView() app.UI {
	return app.Div().Class("ph-app").Body(
		app.Header().Class("ph-header").Body(
			app.H1().Text("Projekte"),
			app.Span().Class("ph-muted").Text(r.email),
		),
		app.Div().Class("ph-newprj").Body(
			app.Input().Type("text").Placeholder("Neues Projekt…").Value(r.newTitle).OnInput(r.bind(&r.newTitle)),
			app.Button().Class("ph-btn").Disabled(r.busy).Text("Anlegen").OnClick(r.createProject),
		),
		app.If(r.status != "", func() app.UI { return app.P().Class("ph-err").Text(r.status) }),
		app.Ul().Class("ph-list").Body(
			app.Range(r.projects).Slice(func(i int) app.UI {
				p := r.projects[i]
				return app.Li().Class("ph-item").Body(
					app.Button().Class("ph-title ph-titlebtn").Text(p.Title).OnClick(r.openProject(p)),
					app.Button().Class("ph-link").Text("löschen").OnClick(func(ctx app.Context, _ app.Event) {
						r.deleteProject(ctx, p.ID)
					}),
				)
			}),
			app.If(len(r.projects) == 0, func() app.UI {
				return app.Li().Class("ph-muted").Text("Noch keine Projekte.")
			}),
		),
		r.suggestionsView(),
	)
}

// suggestionsView renders "add this project?" cards for recently-active Claude Code
// working dirs (desktop shell only; empty in the hosted browser build).
func (r *Root) suggestionsView() app.UI {
	if len(r.suggestions) == 0 {
		return app.Div()
	}
	return app.Div().Class("ph-suggest").Body(
		app.H2().Class("ph-suggest-h").Text("Aus Claude Code hinzufügen"),
		app.Ul().Class("ph-list").Body(
			app.Range(r.suggestions).Slice(func(i int) app.UI {
				s := r.suggestions[i]
				return app.Li().Class("ph-item ph-suggest-item").Body(
					app.Div().Class("ph-suggest-info").Body(
						app.Span().Class("ph-title").Text(suggestionTitle(s)),
						app.Span().Class("ph-muted ph-suggest-path").Text(s.Cwd),
						app.Span().Class("ph-muted").Text(sessionCountLabel(s.SessionCount)),
					),
					app.Button().Class("ph-btn").Disabled(r.busy).Text("+ Projekt").
						OnClick(r.addSuggestion(s)),
				)
			}),
		),
	)
}

// addSuggestion creates a project bound to the Claude Code working dir.
func (r *Root) addSuggestion(s nativeclient.ClaudeSuggestion) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		if r.busy {
			return
		}
		r.busy, r.status = true, ""
		ctx.Async(func() {
			if _, err := r.store.CreateProject(context.Background(), suggestionTitle(s), "", s.Cwd); err != nil {
				r.fail(ctx, "Anlegen fehlgeschlagen: "+err.Error())
				return
			}
			r.reload(ctx, nil)
		})
	}
}

func (r *Root) createProject(ctx app.Context, _ app.Event) {
	title := r.newTitle
	if title == "" || r.busy {
		return
	}
	r.busy, r.status = true, ""
	ctx.Async(func() {
		if _, err := r.store.CreateProject(context.Background(), title, "", ""); err != nil {
			r.fail(ctx, "Anlegen fehlgeschlagen: "+err.Error())
			return
		}
		r.reload(ctx, func() { r.newTitle = "" })
	})
}

func (r *Root) deleteProject(ctx app.Context, id string) {
	if r.busy {
		return
	}
	r.busy, r.status = true, ""
	ctx.Async(func() {
		if err := r.store.DeleteProject(context.Background(), id); err != nil {
			r.fail(ctx, "Löschen fehlgeschlagen: "+err.Error())
			return
		}
		r.reload(ctx, nil)
	})
}

// reload re-fetches the project list, runs an optional mutation, and clears busy.
func (r *Root) reload(ctx app.Context, mutate func()) {
	projects, err := r.store.ListProjects(context.Background())
	if err != nil {
		r.fail(ctx, "Neu laden fehlgeschlagen: "+err.Error())
		return
	}
	ctx.Dispatch(func(ctx app.Context) {
		if mutate != nil {
			mutate()
		}
		r.projects = projects
		// A project just added from a suggestion now has a LocalPath, so re-filter to
		// drop it from the offered list.
		r.suggestions = filterAdded(r.suggestions, projects)
		r.busy = false
	})
}

// ─── Claude Code suggestions (desktop shell) ─────────────────────────────────

// readPhNative reads the sidecar base URL + bearer token the Electron preload
// injects as window.phNative. Both are empty in the hosted browser build.
func readPhNative() (base, token string) {
	pn := app.Window().Get("phNative")
	if !pn.Truthy() {
		return "", ""
	}
	return pn.Get("base").String(), pn.Get("token").String()
}

// filterAdded drops suggestions whose cwd is already a project's LocalPath.
func filterAdded(sug []nativeclient.ClaudeSuggestion, projects []domain.ProjectRef) []nativeclient.ClaudeSuggestion {
	added := make(map[string]bool, len(projects))
	for _, p := range projects {
		if p.LocalPath != "" {
			added[p.LocalPath] = true
		}
	}
	out := make([]nativeclient.ClaudeSuggestion, 0, len(sug))
	for _, s := range sug {
		if !added[s.Cwd] {
			out = append(out, s)
		}
	}
	return out
}

// suggestionTitle derives a project title from a suggestion: the working dir's last
// path segment (the natural project name), falling back to the transcript title.
func suggestionTitle(s nativeclient.ClaudeSuggestion) string {
	if base := path.Base(s.Cwd); base != "" && base != "." && base != "/" {
		return base
	}
	return s.Title
}

// sessionCountLabel renders "N Session(s)".
func sessionCountLabel(n int) string {
	if n == 1 {
		return "1 Session"
	}
	return strconv.Itoa(n) + " Sessions"
}

// ─── helpers ────────────────────────────────────────────────────────────────

// bind returns an input handler that writes the element's value into dst.
func (r *Root) bind(dst *string) app.EventHandler { return bindInput(dst) }

// bindInput returns an input handler that writes the element's value into dst.
// Shared by the login/project-list and project-detail components.
func bindInput(dst *string) app.EventHandler {
	return func(ctx app.Context, e app.Event) {
		*dst = ctx.JSSrc().Get("value").String()
	}
}

// fail dispatches an error message and clears the busy flag.
func (r *Root) fail(ctx app.Context, msg string) {
	ctx.Dispatch(func(ctx app.Context) {
		r.status = msg
		r.busy = false
	})
}

func b64d(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func btnLabel(busy bool, idle, active string) string {
	if busy {
		return active
	}
	return idle
}
