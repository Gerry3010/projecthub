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
	"fmt"
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
	remember bool   // "stay signed in on this device" — persist creds in localStorage
	status   string // error/info message

	// server is the Passbubble upstream URL, editable on the login screen (desktop
	// only). serverEditable gates the field; the value is device-local (persisted by
	// the sidecar), independent of any account, and survives logout.
	server         string
	serverEditable bool

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

	// account-level app accent (CSS hex); loaded at unlock, synced via the RootIndex
	accent string
	// homeView is the projects-home layout: "grid" (default) or "list". Synced via
	// the RootIndex.
	homeView string
	// theme is the account-level UI theme key ("deck-dark" default, "liquid-glass", …),
	// synced via the RootIndex; a project may override it (applied by the Workspace).
	theme string
	// accountBg is the account-level default wallpaper/glass background, loaded at
	// unlock and applied on the home view. A project may override it (r.selected.Background).
	accountBg *domain.Background
	// bgURL caches the resolved account wallpaper image URL for the home view.
	bgURL string

	// Global Claude chat sidebar (right dock, app-wide, toggleable). claudeOpener is
	// registered by the active Workspace so the sidebar's "start Claude" opens a
	// terminal tile there; nil on the home view (no workspace → starter disabled).
	chatOpen     bool
	claudeOpener func(ctx app.Context, cwd, prompt string)

	// showSettings overlays the global settings screen (left rail gear). Account-wide;
	// works over both the home view and an open workspace. settingsTab is the active
	// tab ("" ⇒ Themes).
	showSettings bool
	settingsTab  string
	// settingsThemeScope: "account" (default) or "project" — which scope a theme pick
	// in the Themes tab writes to (project option only offered when one is open).
	settingsThemeScope string
}

// accentColor returns the chosen app accent, or the default when none is set yet.
func (r *Root) accentColor() string {
	if r.accent == "" {
		return domain.DefaultAccent
	}
	return r.accent
}

func (r *Root) Render() app.UI {
	if !r.unlocked {
		return r.loginView()
	}
	var main app.UI
	if r.selected != nil {
		sel := r.selected
		main = &Workspace{Store: r.store, Ref: *sel, Back: r.closeProject, Native: r.native,
			RegisterClaudeOpener: func(open func(ctx app.Context, cwd, prompt string)) { r.claudeOpener = open },
			OnColor: func(color string) {
				sel.Color = color // update the open project's ref
				for i := range r.projects {
					if r.projects[i].ID == sel.ID {
						r.projects[i].Color = color // keep the rail/home list in sync
					}
				}
			}}
	} else {
		r.claudeOpener = nil // no workspace on the home view
		main = r.projectsView()
	}
	return app.Div().Class("ph-approot").Body(
		// Home wallpaper layer (account default). In a project the Workspace draws its
		// own wallpaper, so only render this on the home view to avoid a double layer.
		app.If(r.selected == nil, func() app.UI {
			return app.Div().Class("ph-app-wallpaper")
		}),
		r.railView(),
		app.Div().Class("ph-approot-main").Body(main),
		app.If(r.showSettings, r.settingsView),
		app.If(r.chatOpen, r.chatSidebar),
		// Floating toggle for the global Claude sidebar (desktop only).
		app.If(r.native.Available(), func() app.UI {
			cls := "ph-chat-fab"
			if r.chatOpen {
				cls += " ph-chat-fab-on"
			}
			return app.Button().Class(cls).Title("Claude-Chat").
				OnClick(func(ctx app.Context, _ app.Event) { r.chatOpen = !r.chatOpen }).
				Body(icon("chat", 20))
		}),
	)
}

// activeCwd is the working dir the global Claude sidebar targets: the open project's
// local path, or empty on the home view.
func (r *Root) activeCwd() string {
	if r.selected != nil {
		return r.selected.LocalPath
	}
	return ""
}

// chatSidebar is the app-wide, right-docked Claude chat (viewer + session browser +
// starter), reusing the same claudeTile as the Claude tile. Its "start" opens a
// terminal in the active workspace via the registered opener.
func (r *Root) chatSidebar() app.UI {
	return app.Aside().Class("ph-chat-sidebar").Body(
		app.Div().Class("ph-chat-head").Body(
			app.Span().Class("ph-chat-title").Text("Claude"),
			app.Button().Class("ph-tile-btn").Title("Schließen").Text("✕").
				OnClick(func(ctx app.Context, _ app.Event) { r.chatOpen = false }),
		),
		&claudeTile{
			Native:       r.native,
			Cwd:          r.activeCwd(),
			OpenClaude:   r.claudeOpener,
			Embedded:     true,
			SystemPrompt: buildManagerSystemPrompt(r.projects),
		},
	)
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
	setDocTheme(r.theme)                   // back to the account theme (drop any project override)
	applyBackground(r.accountBg, r.bgURL) // back to the account wallpaper (drop project override)
	r.busy, r.status = true, ""
	ctx.Async(func() { r.reload(ctx, nil) })
}

// ─── login / unlock ─────────────────────────────────────────────────────────

// OnMount restores a remembered login: the email is always prefilled; if the user
// chose "stay signed in" the password is prefilled too and we auto-unlock. Storage
// is localStorage on the loopback desktop origin — the user's explicit, local-only
// opt-in (a keychain-backed store is a later hardening step).
func (r *Root) OnMount(ctx app.Context) {
	if r.unlocked {
		return
	}
	r.email = lsGet("ph.email")
	// Under the desktop shell, expose + prefill the Passbubble server field from the
	// sidecar (device-local override; independent of the account and of logout).
	if base, token := readPhNative(); base != "" && token != "" {
		r.serverEditable = true
		nc := nativeclient.New(base, token)
		ctx.Async(func() {
			if url, err := nc.Server(context.Background()); err == nil && url != "" {
				ctx.Dispatch(func(ctx app.Context) { r.server = url })
			}
		})
	}
	if lsGet("ph.remember") == "1" {
		r.remember = true
		r.password = lsGet("ph.pw")
		if r.email != "" && r.password != "" {
			r.doLogin(ctx)
		}
	}
}

func (r *Root) loginView() app.UI {
	return app.Div().Class("ph-center").Style("--accent", r.accentColor()).Body(
		app.Div().Class("ph-card").Body(
			app.Div().Class("ph-brand ph-brand-lg").Body(
				nexusIcon(r.accentColor(), 34),
				app.H1().Text("ProjectHub"),
			),
			app.P().Class("ph-muted").Text("Mit deinem Passbubble-Konto anmelden"),
			app.Input().Type("email").Placeholder("E-Mail").Value(r.email).ID("ph-email").
				Attr("autocomplete", "username").OnInput(r.bind(&r.email)).
				OnKeyDown(focusOnEnter("ph-pw")),
			app.Input().Type("password").Placeholder("Master-Passwort").Value(r.password).ID("ph-pw").
				Attr("autocomplete", "current-password").OnInput(r.bind(&r.password)).
				OnKeyDown(func(ctx app.Context, e app.Event) {
					if e.Get("key").String() == "Enter" {
						r.doLogin(ctx)
					}
				}),
			app.Label().Class("ph-check").Body(
				app.Input().Type("checkbox").Checked(r.remember).
					OnChange(func(ctx app.Context, e app.Event) { r.remember = ctx.JSSrc().Get("checked").Bool() }),
				app.Text("Angemeldet bleiben (lokal auf diesem Gerät)"),
			),
			app.Button().Class("ph-btn").Disabled(r.busy).Text(btnLabel(r.busy, "Entsperren", "Entschlüssele…")).
				OnClick(r.login),
			app.If(r.serverEditable, func() app.UI {
				// Server field (desktop only): the Passbubble upstream. In a disclosure
				// so it stays out of the way; device-local + survives logout.
				return app.Details().Class("ph-server").Body(
					app.Summary().Text("Server"),
					app.Input().Type("text").Class("ph-server-input").
						Placeholder("https://passbubble.example.net").Value(r.server).ID("ph-server").
						Attr("autocomplete", "off").Attr("spellcheck", "false").
						OnInput(r.bind(&r.server)),
				)
			}),
			app.If(r.status != "", func() app.UI { return app.P().Class("ph-err").Text(r.status) }),
		),
	)
}

// login is the button/Enter handler; the real work is in doLogin so OnMount can
// reuse it for auto-unlock.
func (r *Root) login(ctx app.Context, _ app.Event) { r.doLogin(ctx) }

// doLogin authenticates against Passbubble and unlocks the vault entirely in the
// browser. Network + crypto run off the UI goroutine via ctx.Async.
func (r *Root) doLogin(ctx app.Context) {
	if r.busy {
		return
	}
	r.busy, r.status = true, ""
	email, password, serverURL := r.email, r.password, r.server

	ctx.Async(func() {
		// Point the sidecar's /pb proxy at the chosen server first, so this login (and
		// everything after) talks to it. Takes effect immediately, no restart.
		if base, token := readPhNative(); base != "" && token != "" && serverURL != "" {
			if err := nativeclient.New(base, token).SetServer(context.Background(), serverURL); err != nil {
				r.fail(ctx, "Server ungültig: "+err.Error())
				return
			}
		}
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
		accent, err := st.AccentColor(context.Background())
		if err != nil {
			accent = domain.DefaultAccent // non-fatal: fall back rather than block unlock
		}
		homeView, err := st.HomeView(context.Background())
		if err != nil || homeView == "" {
			homeView = "grid"
		}
		theme, err := st.Theme(context.Background())
		if err != nil {
			theme = "" // non-fatal → built-in default
		}
		accountBg, _ := st.Background(context.Background()) // account-default wallpaper (nil ⇒ flat deck)

		// Under the Electron shell, offer recently-active Claude Code dirs that
		// aren't projects yet. In the hosted browser build phNative is undefined,
		// so nc is nil and no suggestions appear.
		base, token := readPhNative()
		nc := nativeclient.New(base, token)
		bgURL := resolveBgImageURL(st, nc, accountBg) // resolve the wallpaper image (needs nc for local/vault)
		var suggestions []nativeclient.ClaudeSuggestion
		if nc.Available() {
			if sug, err := nc.Suggestions(context.Background()); err == nil {
				suggestions = filterAdded(sug, projects)
			}
		}

		ctx.Dispatch(func(ctx app.Context) {
			r.busy = false
			r.unlocked = true
			r.store = st
			r.projects = projects
			r.accent = accent
			r.homeView = homeView
			r.theme = theme
			r.accountBg = accountBg
			r.bgURL = bgURL
			r.native = nc
			r.suggestions = suggestions
			setDocTheme(theme)                 // apply the account UI theme immediately
			applyBackground(accountBg, bgURL) // apply the account wallpaper on the home view

			// Persist per the "stay signed in" choice (local-only, this device).
			lsSet("ph.email", email)
			if r.remember {
				lsSet("ph.remember", "1")
				lsSet("ph.pw", password)
			} else {
				lsDel("ph.remember")
				lsDel("ph.pw")
			}
			r.password = "" // drop the plaintext password from component state
		})
		pushRoster(nc, projects)
	})
}

// ─── local (device) login persistence ────────────────────────────────────────

// The "stay signed in" creds prefer window.phSecure — the Electron desktop's
// origin-independent, keychain-encrypted store (localStorage would be wiped every
// launch because the sidecar's loopback port is random). In the plain web build
// phSecure is absent, so fall back to localStorage.

func lsGet(key string) string {
	if s := app.Window().Get("phSecure"); s.Truthy() {
		if v := s.Call("get", key); v.Truthy() {
			return v.String()
		}
		return ""
	}
	ls := app.Window().Get("localStorage")
	if !ls.Truthy() {
		return ""
	}
	if v := ls.Call("getItem", key); v.Truthy() {
		return v.String()
	}
	return ""
}

func lsSet(key, val string) {
	if s := app.Window().Get("phSecure"); s.Truthy() {
		s.Call("set", key, val)
		return
	}
	if ls := app.Window().Get("localStorage"); ls.Truthy() {
		ls.Call("setItem", key, val)
	}
}

func lsDel(key string) {
	if s := app.Window().Get("phSecure"); s.Truthy() {
		s.Call("del", key)
		return
	}
	if ls := app.Window().Get("localStorage"); ls.Truthy() {
		ls.Call("removeItem", key)
	}
}

// focusOnEnter moves focus to the element with id when Enter is pressed — so Enter
// on the email field jumps to the password field.
func focusOnEnter(id string) app.EventHandler {
	return func(ctx app.Context, e app.Event) {
		if e.Get("key").String() == "Enter" {
			app.Window().Get("document").Call("getElementById", id).Call("focus")
		}
	}
}

// ─── projects ───────────────────────────────────────────────────────────────

func (r *Root) projectsView() app.UI {
	return app.Div().Class("ph-app ph-home").Style("--accent", r.accentColor()).Body(
		// hero band: brand lockup + live readout on the left, controls on the right
		app.Header().Class("ph-hero").Body(
			app.Div().Class("ph-hero-lead").Body(
				app.Div().Class("ph-hero-mark").Body(nexusIcon(r.accentColor(), 44)),
				app.Div().Class("ph-hero-text").Body(
					app.H1().Class("ph-hero-title").Text("ProjectHub"),
					app.P().Class("ph-hero-readout").Text(r.homeReadout()),
				),
			),
			app.Div().Class("ph-hero-actions").Body(
				app.Span().Class("ph-muted ph-hero-user").Text(r.email),
				app.Button().Class("ph-tile-btn").Title(r.homeToggleTitle()).Text(r.homeToggleIcon()).
					OnClick(r.toggleHomeView),
				&accentPicker{Current: r.accentColor(), OnPick: r.pickAccent, OnCustom: r.customAccent},
			),
		),
		app.Div().Class("ph-newprj").Body(
			app.Input().Type("text").Placeholder("Neues Projekt…").Value(r.newTitle).OnInput(r.bind(&r.newTitle)).
				OnKeyDown(func(ctx app.Context, e app.Event) {
					if e.Get("key").String() == "Enter" {
						r.createProject(ctx, e)
					}
				}),
			app.Button().Class("ph-btn").Disabled(r.busy).Text("Anlegen").OnClick(r.createProject),
		),
		app.If(r.status != "", func() app.UI { return app.P().Class("ph-err").Text(r.status) }),
		app.P().Class("ph-eyebrow").Text("Projekte"),
		app.Ul().Class("ph-list "+r.homeListClass()).Body(
			app.Range(r.projects).Slice(func(i int) app.UI {
				return &projectItem{r: r, p: r.projects[i]}
			}),
			app.If(len(r.projects) == 0, func() app.UI {
				return app.Li().Class("ph-empty-card").Text("Noch keine Projekte — oben eins anlegen oder links über ＋.")
			}),
		),
		r.suggestionsView(),
	)
}

// homeReadout is the hero's status line: project count plus, on the desktop shell,
// how many Claude Code sessions were discovered across the suggestion dirs.
func (r *Root) homeReadout() string {
	n := len(r.projects)
	word := "Projekte"
	if n == 1 {
		word = "Projekt"
	}
	out := fmt.Sprintf("%d %s", n, word)
	sessions := 0
	for _, s := range r.suggestions {
		sessions += s.SessionCount
	}
	if sessions > 0 {
		out += fmt.Sprintf(" · %d Claude-Sessions gefunden", sessions)
	}
	return out
}

// homeListClass maps the current home view to the projects-list CSS class.
func (r *Root) homeListClass() string {
	if r.homeView == "list" {
		return "ph-home-list"
	}
	return "ph-home-grid"
}

func (r *Root) homeToggleIcon() string {
	if r.homeView == "list" {
		return "▦" // offer switching to grid
	}
	return "☰" // offer switching to list
}

func (r *Root) homeToggleTitle() string {
	if r.homeView == "list" {
		return "Als Grid anzeigen"
	}
	return "Als Liste anzeigen"
}

// toggleHomeView flips grid⇄list, updates the UI immediately and persists it.
func (r *Root) toggleHomeView(ctx app.Context, _ app.Event) {
	if r.homeView == "list" {
		r.homeView = "grid"
	} else {
		r.homeView = "list"
	}
	view := r.homeView
	st := r.store
	ctx.Async(func() {
		if err := st.SetHomeView(context.Background(), view); err != nil {
			ctx.Dispatch(func(ctx app.Context) { r.status = "Ansicht speichern fehlgeschlagen: " + err.Error() })
		}
	})
}

// projectItem is the keyed wrapper for one project row. Keying reconciliation by
// the project ID (CompoID) makes go-app dismount+mount rows cleanly on reorder or
// delete, instead of positionally recycling <li>s — which otherwise leaves the
// per-project color/handlers stuck on the wrong row (same class of bug fixed for
// layout tiles via nodeView in workspace.go).
type projectItem struct {
	app.Compo
	r *Root
	p domain.ProjectRef
}

func (it *projectItem) CompoID() string { return it.p.ID }

func (it *projectItem) Render() app.UI {
	p, r := it.p, it.r
	meta := p.LocalPath
	if meta == "" {
		meta = "kein lokaler Pfad"
	}
	// The whole card opens the project; the delete link stops propagation so it
	// doesn't also trigger the open.
	return app.Li().Class("ph-item ph-proj").Style("--accent", p.AccentColor()).
		OnClick(r.openProject(p)).
		Body(
			app.Div().Class("ph-item-main").Body(
				nexusIcon(p.AccentColor(), 22),
				app.Span().Class("ph-title").Text(p.Title),
			),
			app.Span().Class("ph-proj-meta").Title(meta).Text(meta),
			app.Button().Class("ph-link ph-proj-del").Text("löschen").OnClick(func(ctx app.Context, e app.Event) {
				e.Call("stopPropagation")
				r.deleteProject(ctx, p.ID)
			}),
		)
}

// suggestionsView renders "add this project?" cards for recently-active Claude Code
// working dirs (desktop shell only; empty in the hosted browser build).
func (r *Root) suggestionsView() app.UI {
	if len(r.suggestions) == 0 {
		return app.Div()
	}
	return app.Div().Class("ph-suggest").Body(
		app.P().Class("ph-eyebrow").Text("Aus Claude Code"),
		app.Ul().Class("ph-list "+r.homeListClass()).Body(
			app.Range(r.suggestions).Slice(func(i int) app.UI {
				return &suggestItem{r: r, s: r.suggestions[i]}
			}),
		),
	)
}

// suggestItem is the keyed wrapper for one Claude-Code suggestion card, keyed by
// its working dir (Cwd) so filtering/reordering the suggestions list reconciles
// cleanly — see projectItem for the rationale.
type suggestItem struct {
	app.Compo
	r *Root
	s nativeclient.ClaudeSuggestion
}

func (it *suggestItem) CompoID() string { return it.s.Cwd }

func (it *suggestItem) Render() app.UI {
	s, r := it.s, it.r
	return app.Li().Class("ph-item ph-suggest-item").Body(
		app.Div().Class("ph-suggest-info").Body(
			app.Span().Class("ph-title").Text(suggestionTitle(s)),
			app.Span().Class("ph-muted ph-suggest-path").Text(s.Cwd),
			app.Span().Class("ph-muted").Text(sessionCountLabel(s.SessionCount)),
		),
		app.Button().Class("ph-btn").Disabled(r.busy).Text("+ Projekt").
			OnClick(r.addSuggestion(s)),
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

// pickAccent sets the app accent to a preset swatch and persists it.
func (r *Root) pickAccent(color string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) { r.applyAccent(ctx, color) }
}

// customAccent handles the native color-well change event.
func (r *Root) customAccent(ctx app.Context, _ app.Event) {
	r.applyAccent(ctx, ctx.JSSrc().Get("value").String())
}

// applyAccent updates the accent in the UI immediately and persists it to the
// RootIndex in the background (best-effort: a failed save is non-fatal — the local
// pick still holds for the session).
func (r *Root) applyAccent(ctx app.Context, color string) {
	if color == "" || color == r.accent {
		return
	}
	r.accent = color
	st := r.store
	ctx.Async(func() {
		if err := st.SetAccentColor(context.Background(), color); err != nil {
			ctx.Dispatch(func(ctx app.Context) { r.status = "Akzentfarbe speichern fehlgeschlagen: " + err.Error() })
		}
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
	pushRoster(r.native, projects)
}

// ─── Claude Code suggestions (desktop shell) ─────────────────────────────────

// pushRoster best-effort pushes the project id+title roster into the sidecar so the
// browser-extension popup can list projects to couple tab groups to. It is always
// called from a goroutine already off the UI thread (inside ctx.Async), so the
// blocking HTTP call here never stalls rendering; failures are silently ignored (the
// popup just shows a stale/empty roster until the next successful push).
func pushRoster(nc *nativeclient.Client, projects []domain.ProjectRef) {
	if !nc.Available() {
		return
	}
	roster := make([]domain.RosterEntry, len(projects))
	for i, p := range projects {
		roster[i] = domain.RosterEntry{ID: p.ID, Title: p.Title}
	}
	_ = nc.SetProjects(context.Background(), roster)
}

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
