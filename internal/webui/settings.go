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

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// appVersion is shown on the settings "Über" tab.
const appVersion = "0.1.0"

// uiTheme is one selectable UI theme (a token-override set in theme.css, keyed by
// [data-theme]). prevClass drives the small live-ish preview tile in the picker.
type uiTheme struct {
	Key, Label, Desc, PrevClass string
}

var uiThemes = []uiTheme{
	{"deck-dark", "Deck Dark", "Graphit-Deck mit opaken Instrument-Islands. Der Standard.", "ph-tpv-deck-dark"},
	{"liquid-glass", "Liquid Glass", "Transparente, geblurrte Glas-Panels — am schönsten über einem Wallpaper.", "ph-tpv-liquid-glass"},
}

// editorThemeOption mirrors the CodeMirror theme keys defined in the shell (index.ts
// EDITOR_THEMES) so the Settings editor tab can drive them from go-app.
var editorThemeOptions = []struct{ Key, Label string }{
	{"default", "Hell (Standard)"},
	{"one-dark", "One Dark"},
	{"dracula", "Dracula"},
	{"cobalt", "Cobalt"},
	{"tomorrow", "Tomorrow"},
	{"solarized-light", "Solarized Light"},
	{"ayu-light", "Ayu Light"},
	{"espresso", "Espresso"},
}

// setDocTheme sets <html data-theme=…> so theme.css's token-override set takes effect
// app-wide. Empty ⇒ the built-in default (deck-dark). Shared by Root + Workspace.
func setDocTheme(key string) {
	if key == "" {
		key = "deck-dark"
	}
	if doc := app.Window().Get("document"); doc.Truthy() {
		doc.Get("documentElement").Call("setAttribute", "data-theme", key)
	}
}

// applyEditorThemeLive pushes a CodeMirror theme to every open editor via the shell.
func applyEditorThemeLive(key string) {
	if key == "" {
		return
	}
	if shell := app.Window().Get("phShell"); shell.Truthy() {
		shell.Call("applyEditorTheme", key)
	}
}

// resolvedThemeKey is the UI theme currently in effect: a project override when one is
// open, else the account default, else deck-dark.
func (r *Root) resolvedThemeKey() string {
	if r.selected != nil && r.selected.Theme != "" {
		return r.selected.Theme
	}
	if r.theme != "" {
		return r.theme
	}
	return "deck-dark"
}

func (r *Root) settingsTabKey() string {
	// "themes" merged into "appearance" — map any stale value to the combined tab.
	if r.settingsTab == "" || r.settingsTab == "themes" {
		return "appearance"
	}
	return r.settingsTab
}

// settingsView is the global, full-screen settings screen (opened from the rail gear):
// a classic left tab column + content pane.
func (r *Root) settingsView() app.UI {
	tabs := []struct{ Key, Label string }{
		{"appearance", "Erscheinungsbild"},
		{"editor", "Editor"},
		{"terminal", "Terminal"},
		{"browser", "Browser"},
		{"windows", "Fenster"},
		{"account", "Konto"},
		{"about", "Über"},
	}
	active := r.settingsTabKey()
	return app.Div().Class("ph-settings").Body(
		app.Div().Class("ph-settings-head").Body(
			app.Span().Class("ph-settings-title").Text("Einstellungen"),
			app.Button().Class("ph-tile-btn").Title("Schließen").Text("✕").
				OnClick(func(ctx app.Context, _ app.Event) { r.showSettings = false }),
		),
		app.Div().Class("ph-settings-body").Body(
			app.Nav().Class("ph-settings-tabs").Body(
				app.Range(tabs).Slice(func(i int) app.UI {
					t := tabs[i]
					cls := "ph-settings-tab"
					if t.Key == active {
						cls += " ph-settings-tab-active"
					}
					return app.Button().Class(cls).Text(t.Label).
						OnClick(func(ctx app.Context, _ app.Event) { r.settingsTab = t.Key })
				}),
			),
			app.Div().Class("ph-settings-pane").Body(r.settingsPane(active)),
			// Live wallpaper/glass preview fills the empty space right of the pane, but
			// only on the appearance tab (where there's something to preview).
			app.If(active == "appearance", r.settingsPreview),
		),
	)
}

// settingsPreview is the live wallpaper/glass preview shown right of the appearance
// pane. It consumes the same :root CSS variables applyBackground sets live (--bg-image,
// --bg-dim, --app-alpha, --panel-*), so every edit in the bgEditor reflects instantly
// without a go-app re-render. The checkerboard behind it stands in for the desktop:
// when the window-opacity slider drops (or with window transparency on), the wallpaper
// fades and the "desktop" shows through — exactly what happens on screen.
func (r *Root) settingsPreview() app.UI {
	return app.Div().Class("ph-settings-preview").Body(
		app.P().Class("ph-eyebrow").Text("Vorschau"),
		app.Div().Class("ph-prev-stage").Body(
			app.Div().Class("ph-prev-wallpaper"),
			app.Div().Class("ph-prev-tiles").Body(
				r.previewTile("Notizen"),
				r.previewTile("Claude"),
				r.previewTile("Terminal"),
				r.previewTile("Dateien"),
			),
		),
		app.P().Class("ph-settings-note").Text("Live: Wallpaper, Glas und Fenster-Deckkraft. Das Schachbrett zeigt, wo bei aktivierter Fenster-Transparenz der Desktop durchscheint — die Tiles bleiben lesbar."),
	)
}

func (r *Root) previewTile(title string) app.UI {
	return app.Div().Class("ph-prev-tile").Body(
		app.Span().Class("ph-prev-tile-title").Text(title),
		app.Span().Class("ph-prev-tile-line"),
		app.Span().Class("ph-prev-tile-line ph-prev-tile-line-short"),
	)
}

func (r *Root) settingsPane(tab string) app.UI {
	switch tab {
	case "editor":
		return r.settingsEditor()
	case "terminal":
		return r.settingsTerminal()
	case "browser":
		return r.settingsBrowser()
	case "windows":
		return r.settingsWindows()
	case "account":
		return r.settingsAccount()
	case "about":
		return r.settingsAbout()
	default:
		return r.settingsAppearance()
	}
}

// ─── theme section (part of the Erscheinungsbild tab) ────────────────────────────

func (r *Root) themeSection() app.UI {
	cur := r.resolvedThemeKey()
	projectOpen := r.selected != nil
	scopeNote := "Gilt als Account-Standard."
	if projectOpen {
		scopeNote = "Wähle oben, ob die Theme-Auswahl für das ganze Konto oder nur dieses Projekt gilt."
	}
	return app.Div().Body(
		app.P().Class("ph-eyebrow").Text("UI-Theme"),
		app.If(projectOpen, func() app.UI {
			return app.Div().Class("ph-seg ph-settings-scope").Body(
				r.themeScopeButton("account", "Account-Standard"),
				r.themeScopeButton("project", "Dieses Projekt"),
			)
		}),
		app.Div().Class("ph-theme-cards").Body(
			app.Range(uiThemes).Slice(func(i int) app.UI {
				t := uiThemes[i]
				cls := "ph-theme-card"
				if t.Key == cur {
					cls += " ph-theme-card-active"
				}
				return app.Button().Class(cls).
					OnClick(func(ctx app.Context, _ app.Event) { r.chooseTheme(ctx, t.Key) }).
					Body(
						app.Div().Class("ph-theme-prev "+t.PrevClass).Body(
							app.Div().Class("ph-tpv-bar"),
							app.Div().Class("ph-tpv-islands").Body(
								app.Div().Class("ph-tpv-island"),
								app.Div().Class("ph-tpv-island"),
							),
						),
						app.Div().Class("ph-theme-meta").Body(
							app.Span().Class("ph-theme-name").Text(t.Label),
							app.Span().Class("ph-theme-desc").Text(t.Desc),
						),
					)
			}),
		),
		app.P().Class("ph-settings-note").Text(scopeNote),
	)
}

func (r *Root) themeScope() string {
	if r.settingsThemeScope == "project" && r.selected != nil {
		return "project"
	}
	return "account"
}

func (r *Root) themeScopeButton(scope, label string) app.UI {
	cls := "ph-seg-btn"
	if r.themeScope() == scope {
		cls += " ph-seg-on"
	}
	return app.Button().Class(cls).Text(label).
		OnClick(func(ctx app.Context, _ app.Event) { r.settingsThemeScope = scope })
}

// chooseTheme applies + persists a UI theme at the selected scope (account default or
// the open project's override) and updates the live document immediately.
func (r *Root) chooseTheme(ctx app.Context, key string) {
	setDocTheme(key)
	st := r.store
	if r.themeScope() == "project" && r.selected != nil {
		id := r.selected.ID
		r.selected.Theme = key
		ctx.Async(func() {
			if err := st.SetProjectTheme(context.Background(), id, key); err != nil {
				ctx.Dispatch(func(ctx app.Context) { r.status = "Projekt-Theme speichern fehlgeschlagen: " + err.Error() })
			}
		})
		return
	}
	r.theme = key
	ctx.Async(func() {
		if err := st.SetTheme(context.Background(), key); err != nil {
			ctx.Dispatch(func(ctx app.Context) { r.status = "Theme speichern fehlgeschlagen: " + err.Error() })
		}
	})
}

// ─── Erscheinungsbild tab ───────────────────────────────────────────────────────

func (r *Root) settingsAppearance() app.UI {
	return app.Div().Body(
		// UI-Theme (cards + optional per-project scope)
		r.themeSection(),

		// Accent default
		app.P().Class("ph-eyebrow ph-settings-gap").Text("Akzentfarbe (Standard)"),
		swatchBar(r.accentColor(), r.pickAccent, r.customAccent),
		app.P().Class("ph-settings-note").Text("Der Akzent ist pro Projekt frei wählbar; das hier ist der Account-Standard."),

		// Home view
		app.P().Class("ph-eyebrow ph-settings-gap").Text("Home-Ansicht"),
		app.Div().Class("ph-seg").Body(
			r.homeViewButton("grid", "▦ Grid"),
			r.homeViewButton("list", "☰ Liste"),
		),

		// Background: wallpaper picker + glass + window opacity + transparency toggle
		app.P().Class("ph-eyebrow ph-settings-gap").Text("Hintergrund"),
		&bgEditor{
			Store:        r.store,
			Native:       r.native,
			ProjectOpen:  r.selected != nil,
			ProjectID:    r.selectedID(),
			Account:      r.accountBg,
			Project:      r.selectedBg(),
			SetAccount:   func(bg *domain.Background) { r.accountBg = bg },
			SetProject:   r.setSelectedBg,
			InitialScope: r.themeScope(),
		},
	)
}

// selectedID is the open project's id, or "" on the home view.
func (r *Root) selectedID() string {
	if r.selected != nil {
		return r.selected.ID
	}
	return ""
}

// selectedBg is the open project's background override, or nil on the home view.
func (r *Root) selectedBg() *domain.Background {
	if r.selected != nil {
		return r.selected.Background
	}
	return nil
}

// setSelectedBg stores a background pointer onto the open project's ref (no-op on home).
func (r *Root) setSelectedBg(bg *domain.Background) {
	if r.selected != nil {
		r.selected.Background = bg
	}
}

func (r *Root) homeViewButton(view, label string) app.UI {
	cls := "ph-seg-btn"
	if r.homeListClass() == "ph-home-"+view {
		cls += " ph-seg-on"
	}
	return app.Button().Class(cls).Text(label).OnClick(func(ctx app.Context, e app.Event) {
		if r.homeView != view {
			r.toggleHomeView(ctx, e)
		}
	})
}

// ─── Editor tab ─────────────────────────────────────────────────────────────────

func (r *Root) settingsEditor() app.UI {
	cur, _ := r.store.EditorTheme(context.Background())
	if cur == "" {
		cur = "one-dark"
	}
	return app.Div().Body(
		app.P().Class("ph-eyebrow").Text("Editor-Theme (Standard)"),
		app.Select().Class("ph-select").
			OnChange(func(ctx app.Context, e app.Event) { r.chooseEditorTheme(ctx, ctx.JSSrc().Get("value").String()) }).
			Body(
				app.Range(editorThemeOptions).Slice(func(i int) app.UI {
					o := editorThemeOptions[i]
					return app.Option().Value(o.Key).Text(o.Label).Selected(o.Key == cur)
				}),
			),
		app.P().Class("ph-settings-note").Text("Gilt für alle Code-Editor-Tiles. Speichern und „In VS Code öffnen“ sitzen jetzt in der Tile-Leiste jedes Editors."),
	)
}

func (r *Root) chooseEditorTheme(ctx app.Context, key string) {
	applyEditorThemeLive(key)
	st := r.store
	ctx.Async(func() {
		if err := st.SetEditorTheme(context.Background(), key); err != nil {
			ctx.Dispatch(func(ctx app.Context) { r.status = "Editor-Theme speichern fehlgeschlagen: " + err.Error() })
		}
	})
}

// ─── Terminal tab ─────────────────────────────────────────────────────────────

// termWordModOptions mirrors the TermWordMod union in the shell (index.ts).
var termWordModOptions = []struct{ Key, Label string }{
	{"alt", "Alt / Option"},
	{"ctrl", "Strg / Ctrl"},
	{"meta", "Win / Cmd (Meta)"},
}

// terminalWordMod reads the device-local terminal word-nav modifier from the phSecure
// bridge (alt|ctrl|meta). Default "alt". available=false in the hosted browser build.
func terminalWordMod() (mod string, available bool) {
	ps := app.Window().Get("phSecure")
	if !ps.Truthy() {
		return "alt", false
	}
	v := ps.Call("get", "ph.term.wordmod").String()
	if v != "alt" && v != "ctrl" && v != "meta" {
		v = "alt"
	}
	return v, true
}

// setTerminalWordMod applies the modifier live to every open terminal via the shell,
// which also persists it device-local (phSecure). No-op without the bridge.
func setTerminalWordMod(mod string) {
	if shell := app.Window().Get("phShell"); shell.Truthy() {
		shell.Call("applyTerminalWordMod", mod)
	}
}

// termBellOptions mirrors bellVoices in the shell (index.ts). The bell rings — it does
// not raise a notification card — so this picks WHICH tone, or silences it.
var termBellOptions = []struct{ Key, Label string }{
	{"ping", "Ping (hell)"},
	{"beep", "Beep (klassisch)"},
	{"chime", "Chime (zwei Töne)"},
	{"knock", "Knock (dumpf)"},
	{"off", "Aus (stumm)"},
}

// terminalBell reads the device-local bell sound + volume from phSecure. Defaults
// "ping" at 0.6 (mirrors the shell). available=false in the hosted browser build.
func terminalBell() (kind string, vol float64, available bool) {
	ps := app.Window().Get("phSecure")
	if !ps.Truthy() {
		return "ping", 0.6, false
	}
	kind = ps.Call("get", "ph.term.bellsound").String()
	if kind != "off" {
		if _, ok := bellOption(kind); !ok {
			kind = "ping"
		}
	}
	vol = 0.6
	if v, err := strconv.ParseFloat(ps.Call("get", "ph.term.bellvol").String(), 64); err == nil && v > 0 && v <= 1 {
		vol = v
	}
	return kind, vol, true
}

func bellOption(key string) (string, bool) {
	for _, o := range termBellOptions {
		if o.Key == key {
			return o.Label, true
		}
	}
	return "", false
}

// setTerminalBell persists sound + volume (device-local) via the shell and previews the
// new setting right away, so picking a tone is audible instead of a guess.
func setTerminalBell(kind string, vol float64) {
	shell := app.Window().Get("phShell")
	if !shell.Truthy() {
		return
	}
	shell.Call("applyTerminalBell", kind, vol)
	if kind != "off" {
		shell.Call("playTerminalBell", kind)
	}
}

// ─── Passbubble backend auto-start (desktop, device-local) ───────────────────────
//
// The desktop shell can bring the local Passbubble stack up on launch and stop it on quit.
// It needs to know where the docker-compose.yml lives; that path is device-local (and private),
// so it is stored via the phSecure bridge, never in the account/vault. available=false in the
// hosted browser build (no bridge).

// passbubbleDir reads the configured compose-dir path from phSecure.
func passbubbleDir() (dir string, available bool) {
	ps := app.Window().Get("phSecure")
	if !ps.Truthy() {
		return "", false
	}
	return ps.Call("get", "backend.pbdir").String(), true
}

// setPassbubbleDir persists the path and asks the main process to (re)start the backend now,
// so setting it takes effect without a relaunch. No-op without the bridge.
func setPassbubbleDir(dir string) {
	if ps := app.Window().Get("phSecure"); ps.Truthy() {
		ps.Call("set", "backend.pbdir", dir)
	}
	ensureBackend()
}

// autostackOn reports whether backend auto-start is enabled — default on, "0" = off.
func autostackOn() bool {
	ps := app.Window().Get("phSecure")
	if !ps.Truthy() {
		return false
	}
	return ps.Call("get", "backend.autostack").String() != "0"
}

// setAutostack persists the toggle and, when enabling, kicks off a start attempt now.
func setAutostack(on bool) {
	ps := app.Window().Get("phSecure")
	if !ps.Truthy() {
		return
	}
	if on {
		ps.Call("set", "backend.autostack", "1")
		ensureBackend()
	} else {
		ps.Call("set", "backend.autostack", "0")
	}
}

// ensureBackend asks the main process to (re)attempt the local backend now. No-op without the
// desktop bridge.
func ensureBackend() {
	if pw := app.Window().Get("phWindow"); pw.Truthy() && pw.Get("ensureBackend").Truthy() {
		pw.Call("ensureBackend")
	}
}

// backendSettings renders the device-local Passbubble backend controls (path + auto-start
// toggle). Shared by the Settings "Fenster" tab and the login-screen disclosure, so it is
// reachable before the first login (when the backend isn't up yet). Empty without the bridge.
func (r *Root) backendSettings() app.UI {
	dir, ok := passbubbleDir()
	if !ok {
		return app.Div()
	}
	return app.Div().Body(
		app.P().Class("ph-eyebrow ph-settings-gap").Text("Passbubble-Backend (Docker)"),
		app.Label().Class("ph-check").Body(
			app.Input().Type("checkbox").Checked(autostackOn()).
				OnChange(func(ctx app.Context, e app.Event) { setAutostack(ctx.JSSrc().Get("checked").Bool()) }),
			app.Text("Backend automatisch starten"),
		),
		app.Input().Type("text").Class("ph-set-input ph-settings-gap").Value(dir).
			Attr("spellcheck", "false").Attr("placeholder", "/Pfad/zu/Password-Manager").
			OnChange(func(ctx app.Context, e app.Event) { setPassbubbleDir(ctx.JSSrc().Get("value").String()) }),
		app.P().Class("ph-settings-note").Text("Ordner mit Passbubbles docker-compose.yml. Geräte-lokal. "+
			"Beim Start zieht die App den Stack hoch (startet Docker Desktop bei Bedarf) und stoppt ihn beim Beenden."),
	)
}

func (r *Root) settingsTerminal() app.UI {
	cur, ok := terminalWordMod()
	if !ok {
		return app.Div().Body(
			app.P().Class("ph-eyebrow").Text("Terminal"),
			app.P().Class("ph-settings-note").Text("Terminal-Einstellungen sind nur im Desktop-Build verfügbar."),
		)
	}
	return app.Div().Body(
		app.P().Class("ph-eyebrow").Text("Wort-Navigation"),
		app.Select().Class("ph-select").
			OnChange(func(ctx app.Context, e app.Event) { setTerminalWordMod(ctx.JSSrc().Get("value").String()) }).
			Body(
				app.Range(termWordModOptions).Slice(func(i int) app.UI {
					o := termWordModOptions[i]
					return app.Option().Value(o.Key).Text(o.Label).Selected(o.Key == cur)
				}),
			),
		app.P().Class("ph-settings-note").Text("Diese Taste springt/löscht im Terminal wortweise: Taste+←/→ springt ein Wort, Taste+Backspace/Entf löscht ein Wort. Gilt nur auf diesem Gerät."),
		r.bellSettings(),
	)
}

// bellSettings renders the terminal-bell sound picker + volume. Every change previews
// the tone immediately (setTerminalBell), which is the only sane way to pick a sound.
func (r *Root) bellSettings() app.UI {
	kind, vol, ok := terminalBell()
	if !ok {
		return app.Div()
	}
	return app.Div().Body(
		app.P().Class("ph-eyebrow ph-settings-gap").Text("Glocke (Bell)"),
		app.Select().Class("ph-select").
			OnChange(func(ctx app.Context, e app.Event) {
				_, v, _ := terminalBell()
				setTerminalBell(ctx.JSSrc().Get("value").String(), v)
				ctx.Dispatch(func(app.Context) {}) // re-render so the volume row follows
			}).
			Body(
				app.Range(termBellOptions).Slice(func(i int) app.UI {
					o := termBellOptions[i]
					return app.Option().Value(o.Key).Text(o.Label).Selected(o.Key == kind)
				}),
			),
		app.If(kind != "off", func() app.UI {
			return app.Div().Class("ph-settings-gap").Body(
				app.Div().Class("ph-appr-slider").Body(
					app.Label().Class("ph-appr-lbl").Text("Lautstärke"),
					app.Input().Type("range").
						Attr("min", 0.05).Attr("max", 1).Attr("step", 0.05).Value(vol).
						// OnChange, not OnInput: dragging fires continuously and every
						// change previews the tone — that would be a machine gun.
						OnChange(func(ctx app.Context, e app.Event) {
							v, err := strconv.ParseFloat(ctx.JSSrc().Get("value").String(), 64)
							if err != nil {
								return
							}
							k, _, _ := terminalBell()
							setTerminalBell(k, v)
						}),
				),
				app.Button().Class("ph-btn").Text("Ton testen").
					OnClick(func(ctx app.Context, _ app.Event) {
						if shell := app.Window().Get("phShell"); shell.Truthy() {
							shell.Call("playTerminalBell", "")
						}
					}),
			)
		}),
		app.P().Class("ph-settings-note").Text("Das Terminal spielt bei der Glocke (BEL/\\a) diesen Ton statt eine Meldung anzuzeigen. "+
			"Meldungen mit Text (OSC 9 / OSC 777, z. B. „Claude fertig\") erscheinen weiterhin als Hinweis. Gilt nur auf diesem Gerät."),
	)
}

// ─── Browser tab ──────────────────────────────────────────────────────────────

// browserCache reads the device-local browser-tile cache toggle from the shell (it lives
// in the main process, which owns the guests' session). available=false without the bridge.
func browserCache() (on bool, available bool) {
	pw := app.Window().Get("phWindow")
	if !pw.Truthy() || !pw.Get("getBrowserCache").Truthy() {
		return false, false
	}
	return pw.Call("getBrowserCache").Bool(), true
}

func setBrowserCache(on bool) {
	if pw := app.Window().Get("phWindow"); pw.Truthy() && pw.Get("setBrowserCache").Truthy() {
		pw.Call("setBrowserCache", on)
	}
}

func clearBrowserCache() {
	if pw := app.Window().Get("phWindow"); pw.Truthy() && pw.Get("clearBrowserCache").Truthy() {
		pw.Call("clearBrowserCache")
	}
}

func (r *Root) settingsBrowser() app.UI {
	on, ok := browserCache()
	if !ok {
		return app.Div().Body(
			app.P().Class("ph-eyebrow").Text("Browser"),
			app.P().Class("ph-settings-note").Text("Browser-Einstellungen sind nur im Desktop-Build verfügbar."),
		)
	}
	return app.Div().Body(
		app.P().Class("ph-eyebrow").Text("Cache"),
		app.Label().Class("ph-check").Body(
			app.Input().Type("checkbox").Checked(on).
				OnChange(func(ctx app.Context, e app.Event) {
					v := ctx.JSSrc().Get("checked").Bool()
					setBrowserCache(v)
					ctx.Dispatch(func(app.Context) { r.status = cacheStatus(v) })
				}),
			app.Text("Cache im Browser-Tile verwenden"),
		),
		app.Button().Class("ph-btn ph-settings-gap").Text("Cache jetzt leeren").
			OnClick(func(ctx app.Context, _ app.Event) {
				clearBrowserCache()
				ctx.Dispatch(func(app.Context) { r.status = "Browser-Cache geleert." })
			}),
		app.P().Class("ph-settings-note").Text("Standard: aus. Browser-Tiles zeigen meist etwas, an dem gerade gearbeitet wird "+
			"(lokaler Dev-Server, Staging, Dashboard) — dort ist eine veraltete Datei aus dem Cache teurer als ein erneuter Ladevorgang. "+
			"Cookies und Logins bleiben in jedem Fall erhalten. Gilt nur auf diesem Gerät."),
	)
}

func cacheStatus(on bool) string {
	if on {
		return "Browser-Cache aktiviert."
	}
	return "Browser-Cache deaktiviert und geleert."
}

// ─── Fenster tab ──────────────────────────────────────────────────────────────

// newWindowModOptions mirrors NEW_WINDOW_MODS in the Electron main process. The
// labels name both platforms' key because the setting is device-local.
var newWindowModOptions = []struct{ Key, Label string }{
	{"CommandOrControl", "Strg / Cmd"},
	{"Alt", "Alt / Option"},
	{"Super", "Super / Win"},
	{"CommandOrControl+Alt", "Strg+Alt / Cmd+Alt"},
}

// newWindowMod reads the device-local modifier of the "open project in a new window"
// shortcut from the shell. available=false in the hosted browser build.
func newWindowMod() (mod string, available bool) {
	pw := app.Window().Get("phWindow")
	if !pw.Truthy() || !pw.Get("getNewWindowMod").Truthy() {
		return "CommandOrControl", false
	}
	v := pw.Call("getNewWindowMod").String()
	if v == "" {
		v = "CommandOrControl"
	}
	return v, true
}

// setNewWindowMod persists the modifier and rebinds the menu accelerator live.
func setNewWindowMod(mod string) {
	if pw := app.Window().Get("phWindow"); pw.Truthy() && pw.Get("setNewWindowMod").Truthy() {
		pw.Call("setNewWindowMod", mod)
	}
}

// shortcutLabel renders an accelerator modifier as the key combination it produces.
func shortcutLabel(mod string) string {
	return strings.ReplaceAll(mod, "CommandOrControl", "Strg/Cmd") + "+Shift+N"
}

func (r *Root) settingsWindows() app.UI {
	cur, ok := newWindowMod()
	if !ok {
		return app.Div().Body(
			app.P().Class("ph-eyebrow").Text("Fenster"),
			app.P().Class("ph-settings-note").Text("Fenster-Einstellungen sind nur im Desktop-Build verfügbar."),
		)
	}
	return app.Div().Body(
		app.P().Class("ph-eyebrow").Text("Projekt in neuem Fenster öffnen"),
		app.Select().Class("ph-select").
			OnChange(func(ctx app.Context, e app.Event) {
				mod := ctx.JSSrc().Get("value").String()
				setNewWindowMod(mod)
				r.status = "" // re-render so the note below shows the new combination
			}).
			Body(
				app.Range(newWindowModOptions).Slice(func(i int) app.UI {
					o := newWindowModOptions[i]
					return app.Option().Value(o.Key).Text(o.Label).Selected(o.Key == cur)
				}),
			),
		app.P().Class("ph-settings-note").Text("Ein Klick auf ein Projekt öffnet es immer im aktuellen Fenster. "+
			shortcutLabel(cur)+" (oder „Datei → Projekt in neuem Fenster öffnen“) übergibt das offene Projekt "+
			"an ein eigenes Fenster. Gilt nur auf diesem Gerät."),
		r.backendSettings(),
	)
}

// ─── Konto tab ──────────────────────────────────────────────────────────────────

func (r *Root) settingsAccount() app.UI {
	return app.Div().Body(
		app.P().Class("ph-eyebrow").Text("Konto"),
		app.Div().Class("ph-set-row").Body(
			app.Span().Class("ph-muted").Text("Angemeldet als"),
			app.Span().Class("ph-set-val").Text(r.email),
		),
		app.Label().Class("ph-check ph-settings-gap").Body(
			app.Input().Type("checkbox").Checked(r.remember).
				OnChange(func(ctx app.Context, e app.Event) {
					r.remember = ctx.JSSrc().Get("checked").Bool()
					if r.remember {
						lsSet("ph.remember", "1")
						lsSet("ph.pw", r.password)
					} else {
						lsSet("ph.remember", "")
						lsSet("ph.pw", "")
					}
				}),
			app.Text("Angemeldet bleiben (lokal auf diesem Gerät)"),
		),
		app.If(r.serverEditable, func() app.UI {
			return app.Div().Body(
				app.P().Class("ph-eyebrow ph-settings-gap").Text("Passbubble-Server"),
				app.Input().Type("text").Class("ph-set-input").Value(r.server).
					Attr("spellcheck", "false").OnInput(r.bind(&r.server)),
				app.P().Class("ph-settings-note").Text("Geräte-lokal; überlebt Logout. Wirkt beim nächsten Login."),
			)
		}),
		app.Button().Class("ph-btn ph-settings-gap").Text("Sperren").
			OnClick(func(ctx app.Context, _ app.Event) {
				if w := app.Window(); w.Truthy() {
					w.Get("location").Call("reload")
				}
			}),
	)
}

// ─── Über tab ───────────────────────────────────────────────────────────────────

func (r *Root) settingsAbout() app.UI {
	return app.Div().Class("ph-about").Body(
		app.Div().Class("ph-about-mark").Body(nexusIcon(r.accentColor(), 56)),
		app.H2().Class("ph-about-title").Text("ProjectHub"),
		app.P().Class("ph-muted").Text("Version "+appVersion),
		app.P().Class("ph-settings-note").Text("Ein Claude-Code-zentriertes Projekt-Cockpit mit E2E-verschlüsseltem Vault (Passbubble)."),
		app.P().Class("ph-settings-note").Text("© 2026 Gerald Hofbauer — AGPLv3."),
	)
}
