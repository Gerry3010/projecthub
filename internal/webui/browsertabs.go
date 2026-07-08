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
	"github.com/Gerry3010/projecthub/internal/native/nativeclient"
)

// tabsTile shows this project's *coupled* tab groups (the extension only reports
// groups the user explicitly coupled in its popup — not every open tab), fed live via
// the sidecar's /native/tabs?project=. It polls a few times a minute (the extension
// pushes on every group change; the poll just refreshes the view). Clicking a tab
// focuses it if it's still open (falling back to opening the URL); a group's "öffnen"
// button focuses or reopens the whole group. The tile also lets the user manage
// groups directly — create/rename/recolor/delete a group, add/remove a tab — without
// going through the browser or the extension popup; every mutation is a TabCommand
// sent the same way focus/openGroup already are, no optimistic UI needed since the
// extension's own sync + this tile's poll pick the result up within ~1-2.5s.
type tabsTile struct {
	app.Compo
	Native    *nativeclient.Client // nil in the hosted (non-Electron) build
	ProjectID string

	groups   []domain.LiveTabGroup
	loaded   bool
	status   string
	stop     chan struct{}
	browsers []string // live browsers, for the "+ Neue Gruppe" target picker

	// "+ Neue Gruppe" inline form
	newGroupOpen bool
	newTitle     string
	newColor     string
	newBrowser   string
	newURL       string

	// per-group inline rename editor; renaming == "" means none open
	renaming    string
	renameTitle string

	// per-group inline "+ Tab" input; addingTab == "" means none open
	addingTab string
	addTabURL string
}

const tabsPollInterval = 2500 * time.Millisecond

// chromeGroupColors are every Chrome tab-group color name, offered in the color
// pickers (create/recolor); see groupColorCSS for their swatch mapping.
var chromeGroupColors = []string{"grey", "blue", "red", "yellow", "green", "pink", "purple", "cyan", "orange"}

func (t *tabsTile) OnMount(ctx app.Context) {
	if t.Native == nil {
		t.loaded = true
		return
	}
	t.newColor = "grey"
	ctx.Async(func() {
		browsers, _ := t.Native.Browsers(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			t.browsers = browsers
			if t.newBrowser == "" && len(browsers) > 0 {
				t.newBrowser = browsers[0]
			}
		})
	})
	t.stop = make(chan struct{})
	ctx.Async(func() {
		for {
			groups, err := t.Native.LiveGroups(context.Background(), t.ProjectID)
			ctx.Dispatch(func(ctx app.Context) {
				t.loaded = true
				if err != nil {
					t.status = err.Error()
					return
				}
				t.groups, t.status = groups, ""
			})
			select {
			case <-t.stop:
				return
			case <-time.After(tabsPollInterval):
			}
		}
	})
}

func (t *tabsTile) OnDismount() {
	if t.stop != nil {
		close(t.stop)
		t.stop = nil
	}
}

// focusTab asks the extension to focus this tab if it's still open, falling back to
// opening the URL in the default handler (SendCommand is best-effort: a closed
// tab/dead port just means the extension silently can't act on it).
func (t *tabsTile) focusTab(browser, url string, tabID, windowID int) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		if t.Native == nil {
			return
		}
		ctx.Async(func() {
			bg := context.Background()
			err := t.Native.SendCommand(bg, domain.TabCommand{
				Browser: browser, Action: "focusTab", TabID: tabID, WindowID: windowID,
			})
			if err != nil && url != "" {
				_ = t.Native.OpenIn(bg, "url", url)
			}
		})
	}
}

// openGroup asks the extension to focus the group if it's still open, or reopen its
// URLs otherwise (extension-side fallback); if the extension is unreachable at all,
// fall back to opening every URL directly.
func (t *tabsTile) openGroup(g domain.LiveTabGroup) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		if t.Native == nil {
			return
		}
		urls := make([]string, len(g.Tabs))
		for i, tab := range g.Tabs {
			urls[i] = tab.URL
		}
		ctx.Async(func() {
			bg := context.Background()
			err := t.Native.SendCommand(bg, domain.TabCommand{
				Browser: g.Browser, Action: "openGroup", GroupID: g.GroupID, GroupKey: g.GroupKey, URLs: urls,
			})
			if err != nil {
				for _, u := range urls {
					_ = t.Native.OpenIn(bg, "url", u)
				}
			}
		})
	}
}

// send is the shared fire-and-forget path for every group-management command: unlike
// focusTab/openGroup there is no OpenIn fallback (creating/editing a group is
// meaningless without the extension), so a failure just surfaces via the next poll
// showing no change.
func (t *tabsTile) send(cmd domain.TabCommand) {
	if t.Native == nil {
		return
	}
	native := t.Native
	go func() { _ = native.SendCommand(context.Background(), cmd) }()
}

// ─── "+ Neue Gruppe" ──────────────────────────────────────────────────────────

func (t *tabsTile) toggleNewGroup(ctx app.Context, _ app.Event) {
	t.newGroupOpen = !t.newGroupOpen
}
func (t *tabsTile) setNewTitle(ctx app.Context, e app.Event) {
	t.newTitle = ctx.JSSrc().Get("value").String()
}
func (t *tabsTile) setNewColor(ctx app.Context, e app.Event) {
	t.newColor = ctx.JSSrc().Get("value").String()
}
func (t *tabsTile) setNewBrowser(ctx app.Context, e app.Event) {
	t.newBrowser = ctx.JSSrc().Get("value").String()
}
func (t *tabsTile) setNewURL(ctx app.Context, e app.Event) {
	t.newURL = ctx.JSSrc().Get("value").String()
}

func (t *tabsTile) submitNewGroup(ctx app.Context, _ app.Event) {
	if t.newTitle == "" || t.newBrowser == "" {
		return
	}
	cmd := domain.TabCommand{
		Browser: t.newBrowser, Action: "createGroup",
		Title: t.newTitle, Color: t.newColor, ProjectID: t.ProjectID,
	}
	if t.newURL != "" {
		cmd.URLs = []string{t.newURL}
	}
	t.send(cmd)
	t.newGroupOpen, t.newTitle, t.newURL = false, "", ""
}

// ─── per-group management (rename / recolor / delete / add-tab / remove-tab) ──

func (t *tabsTile) startRename(g domain.LiveTabGroup) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		t.renaming, t.renameTitle = g.GroupKey, g.Title
	}
}
func (t *tabsTile) setRenameTitle(ctx app.Context, e app.Event) {
	t.renameTitle = ctx.JSSrc().Get("value").String()
}
func (t *tabsTile) cancelRename(ctx app.Context, _ app.Event) { t.renaming = "" }
func (t *tabsTile) submitRename(g domain.LiveTabGroup) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		if t.renameTitle != "" {
			t.send(domain.TabCommand{
				Browser: g.Browser, Action: "renameGroup",
				GroupID: g.GroupID, GroupKey: g.GroupKey, Title: t.renameTitle,
			})
		}
		t.renaming = ""
	}
}

func (t *tabsTile) recolorGroup(g domain.LiveTabGroup) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		t.send(domain.TabCommand{
			Browser: g.Browser, Action: "recolorGroup",
			GroupID: g.GroupID, Color: ctx.JSSrc().Get("value").String(),
		})
	}
}

func (t *tabsTile) deleteGroup(g domain.LiveTabGroup) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		t.send(domain.TabCommand{
			Browser: g.Browser, Action: "deleteGroup", GroupID: g.GroupID, GroupKey: g.GroupKey,
		})
	}
}

func (t *tabsTile) startAddTab(g domain.LiveTabGroup) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		t.addingTab, t.addTabURL = g.GroupKey, ""
	}
}
func (t *tabsTile) setAddTabURL(ctx app.Context, e app.Event) {
	t.addTabURL = ctx.JSSrc().Get("value").String()
}
func (t *tabsTile) cancelAddTab(ctx app.Context, _ app.Event) { t.addingTab = "" }
func (t *tabsTile) submitAddTab(g domain.LiveTabGroup) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		t.send(domain.TabCommand{Browser: g.Browser, Action: "addTab", GroupID: g.GroupID, URL: t.addTabURL})
		t.addingTab = ""
	}
}

// removeTab stops the click from bubbling to the tab row's own focusTab handler.
func (t *tabsTile) removeTab(browser string, tabID int) app.EventHandler {
	return func(ctx app.Context, e app.Event) {
		e.StopImmediatePropagation()
		t.send(domain.TabCommand{Browser: browser, Action: "removeTab", TabID: tabID})
	}
}

func (t *tabsTile) Render() app.UI {
	if t.Native == nil {
		return app.Div().Class("ph-tilecontent").Body(
			app.P().Class("ph-muted").Text("Live-Tabs sind nur in der ProjectHub-Desktop-App verfügbar."),
		)
	}
	return app.Div().Class("ph-tilecontent").Body(
		app.If(t.status != "", func() app.UI { return app.P().Class("ph-err").Text(t.status) }),
		t.tabsHeader(),
		app.If(t.newGroupOpen, t.newGroupForm),
		app.Range(t.groups).Slice(func(i int) app.UI { return t.renderGroup(t.groups[i]) }),
		app.If(t.loaded && len(t.groups) == 0 && t.status == "", func() app.UI {
			return app.P().Class("ph-muted").Text("Noch keine Tab-Gruppe gekoppelt — im Extension-Popup zuordnen, oder oben eine neue anlegen.")
		}),
	)
}

// tabsHeader is the tile-wide "+ Neue Gruppe" toggle.
func (t *tabsTile) tabsHeader() app.UI {
	label := "+ Neue Gruppe"
	if t.newGroupOpen {
		label = "✕ Abbrechen"
	}
	return app.Div().Class("ph-tabs-head").Body(
		app.Button().Class("ph-tile-btn").Title("Neue Tab-Gruppe anlegen").Text(label).
			OnClick(t.toggleNewGroup),
	)
}

// newGroupForm is the inline "+ Neue Gruppe" form: title, color, target browser (only
// shown when more than one browser is live — otherwise it's implied), optional first URL.
func (t *tabsTile) newGroupForm() app.UI {
	return app.Div().Class("ph-newgroup-form").Body(
		app.Input().Class("ph-island-input").Type("text").Placeholder("Gruppenname").
			Value(t.newTitle).OnChange(t.setNewTitle),
		colorSelect(t.newColor, t.setNewColor),
		app.If(len(t.browsers) > 1, func() app.UI {
			return app.Select().Class("ph-island-input").OnChange(t.setNewBrowser).Body(
				app.Range(t.browsers).Slice(func(i int) app.UI {
					b := t.browsers[i]
					return app.Option().Value(b).Selected(b == t.newBrowser).Text(browserLabel(b))
				}),
			)
		}),
		app.Input().Class("ph-island-input").Type("text").Placeholder("erste URL (optional)").
			Value(t.newURL).OnChange(t.setNewURL),
		app.Button().Class("ph-btn").Text("Anlegen").
			Disabled(t.newTitle == "" || t.newBrowser == "").
			OnClick(t.submitNewGroup),
	)
}

// colorSelect renders a Chrome tab-group color <select>, shared by the create form and
// each group's recolor control.
func colorSelect(current string, onChange app.EventHandler) app.UI {
	return app.Select().Class("ph-island-input ph-color-select").OnChange(onChange).Body(
		app.Range(chromeGroupColors).Slice(func(i int) app.UI {
			c := chromeGroupColors[i]
			return app.Option().Value(c).Selected(c == current).Text(colorLabel(c))
		}),
	)
}

// colorLabel is the German label for a Chrome tab-group color name.
func colorLabel(name string) string {
	switch name {
	case "grey":
		return "Grau"
	case "blue":
		return "Blau"
	case "red":
		return "Rot"
	case "yellow":
		return "Gelb"
	case "green":
		return "Grün"
	case "pink":
		return "Pink"
	case "purple":
		return "Violett"
	case "cyan":
		return "Cyan"
	case "orange":
		return "Orange"
	default:
		return name
	}
}

// renderGroup renders one coupled tab group: header (chip, browser icon, title/rename
// editor, count, öffnen/umfärben/umbenennen/löschen), then its tabs (each with a focus
// click + a remove button), then the inline "+ Tab" row when active.
func (t *tabsTile) renderGroup(g domain.LiveTabGroup) app.UI {
	return app.Div().Class("ph-tabgroup").Body(
		app.Div().Class("ph-tabgroup-head").Body(
			groupColorChip(g.Color),
			browserIcon(g.Browser, 14),
			app.If(t.renaming == g.GroupKey, func() app.UI { return t.renameEditor(g) }).
				Else(func() app.UI { return app.Span().Class("ph-title").Text(orText(g.Title, "(ohne Titel)")) }),
			app.Span().Class("ph-muted").Text(fmt.Sprintf("%d", len(g.Tabs))),
			app.Div().Class("ph-spacer"),
			app.If(t.renaming != g.GroupKey, func() app.UI {
				return app.Button().Class("ph-tile-btn").Title("umbenennen").Text("✎").OnClick(t.startRename(g))
			}),
			colorSelect(g.Color, t.recolorGroup(g)),
			app.Button().Class("ph-tile-btn").Title("Tab hinzufügen").Text("+").OnClick(t.startAddTab(g)),
			app.Button().Class("ph-tile-btn").Title("Gruppe öffnen/fokussieren").Text("↗").
				OnClick(t.openGroup(g)),
			app.Button().Class("ph-tile-btn ph-tile-btn-danger").Title("Gruppe löschen (schließt ihre Tabs)").Text("🗑").
				OnClick(t.deleteGroup(g)),
		),
		app.If(t.addingTab == g.GroupKey, func() app.UI { return t.addTabRow(g) }),
		app.Ul().Class("ph-list").Body(
			app.Range(g.Tabs).Slice(func(j int) app.UI { return t.renderTab(g, g.Tabs[j]) }),
		),
	)
}

// renameEditor replaces the group title with an inline text input + confirm/cancel.
func (t *tabsTile) renameEditor(g domain.LiveTabGroup) app.UI {
	return app.Div().Class("ph-inline-edit").Body(
		app.Input().Class("ph-island-input").Type("text").Value(t.renameTitle).
			OnChange(t.setRenameTitle).Attr("autofocus", true),
		app.Button().Class("ph-tile-btn").Title("übernehmen").Text("✓").OnClick(t.submitRename(g)),
		app.Button().Class("ph-tile-btn").Title("abbrechen").Text("✕").OnClick(t.cancelRename),
	)
}

// addTabRow is the inline "+ Tab" URL input shown below a group's header.
func (t *tabsTile) addTabRow(g domain.LiveTabGroup) app.UI {
	return app.Div().Class("ph-inline-edit").Body(
		app.Input().Class("ph-island-input").Type("text").Placeholder("https:// (leer = neuer Tab)").
			Value(t.addTabURL).OnChange(t.setAddTabURL).Attr("autofocus", true),
		app.Button().Class("ph-tile-btn").Title("Tab hinzufügen").Text("✓").OnClick(t.submitAddTab(g)),
		app.Button().Class("ph-tile-btn").Title("abbrechen").Text("✕").OnClick(t.cancelAddTab),
	)
}

// renderTab renders one tab row: click focuses it, the × button removes it from the
// group (closing the tab) without triggering the focus click.
func (t *tabsTile) renderTab(g domain.LiveTabGroup, tab domain.LiveTab) app.UI {
	cls := "ph-item ph-tabitem"
	if tab.Active {
		cls += " ph-tab-active"
	}
	return app.Li().Class(cls).Title(tab.URL).
		OnClick(t.focusTab(g.Browser, tab.URL, tab.TabID, tab.WindowID)).Body(
		tabFavicon(tab.FavIconURL),
		app.Div().Class("ph-suggest-info").Body(
			app.Span().Class("ph-title").Text(orText(tab.Title, tab.URL)),
			app.Span().Class("ph-muted ph-taburl").Text(shortURL(tab.URL)),
		),
		app.If(tab.Pinned, func() app.UI {
			return app.Span().Class("ph-tabpin").Title("angepinnt").Text("📌")
		}),
		app.Button().Class("ph-tile-btn ph-tab-remove").Title("Tab entfernen (schließt ihn)").Text("×").
			OnClick(t.removeTab(g.Browser, tab.TabID)),
	)
}

// tabFavicon renders a tab's favicon, falling back to a neutral globe glyph when the
// browser reported no icon URL (e.g. new tab, local file).
func tabFavicon(url string) app.UI {
	if url == "" {
		return app.Span().Class("ph-tab-favicon ph-tab-favicon-fallback").Text("🌐")
	}
	return app.Img().Class("ph-tab-favicon").Src(url).Alt("").Attr("loading", "lazy")
}

// shortURL trims the scheme for a compact secondary line (full URL stays in the title).
func shortURL(u string) string {
	for _, p := range []string{"https://", "http://"} {
		if len(u) >= len(p) && u[:len(p)] == p {
			return u[len(p):]
		}
	}
	return u
}

// browserLabel is the human name for a reported browser id.
func browserLabel(name string) string {
	switch name {
	case "chrome":
		return "Chrome"
	case "chromium":
		return "Chromium"
	case "brave":
		return "Brave"
	case "edge":
		return "Edge"
	case "vivaldi":
		return "Vivaldi"
	case "opera":
		return "Opera"
	case "firefox":
		return "Firefox"
	default:
		return orText(name, "Browser")
	}
}

// browserColor maps a browser id to a representative brand color for its icon.
func browserColor(name string) string {
	switch name {
	case "chrome":
		return "#4285F4"
	case "brave":
		return "#FB542B"
	case "edge":
		return "#0E86D4"
	case "vivaldi":
		return "#EF3939"
	case "opera":
		return "#FF1B2D"
	case "firefox":
		return "#FF7139"
	default: // chromium + unknown → app accent
		return "#6d8bff"
	}
}

// browserIcon draws a small "browser window" glyph tinted with the browser's brand
// color: a rounded frame with a title bar and an address dot. Color distinguishes
// which browser at a glance; the shape reads as "a browser". Rendered via app.Raw —
// color comes from the fixed browserColor palette, so it is safe to interpolate.
func browserIcon(name string, size int) app.UI {
	c := browserColor(name)
	return app.Raw(fmt.Sprintf(
		`<svg width="%[1]d" height="%[1]d" viewBox="0 0 24 24" fill="none" aria-hidden="true" focusable="false" class="ph-browser-icon" role="img" aria-label="%[3]s">`+
			`<rect x="2.5" y="4" width="19" height="16" rx="3" fill="none" stroke="%[2]s" stroke-width="2"/>`+
			`<line x1="2.5" y1="9" x2="21.5" y2="9" stroke="%[2]s" stroke-width="2"/>`+
			`<circle cx="6" cy="6.5" r="1" fill="%[2]s"/><circle cx="9" cy="6.5" r="1" fill="%[2]s"/>`+
			`</svg>`,
		size, c, browserLabel(name)))
}

// groupColorCSS maps a Chrome tab-group color name to a CSS color. Chrome's palette:
// https://developer.chrome.com/docs/extensions/reference/api/tabGroups (Color enum).
func groupColorCSS(name string) string {
	switch name {
	case "grey", "gray":
		return "#9aa0a6"
	case "blue":
		return "#4285f4"
	case "red":
		return "#ea4335"
	case "yellow":
		return "#fbbc04"
	case "green":
		return "#34a853"
	case "pink":
		return "#ff8bcb"
	case "purple":
		return "#a142f4"
	case "cyan":
		return "#24c1e0"
	case "orange":
		return "#fa903e"
	default:
		return "#6d8bff"
	}
}

// groupColorChip renders a small dot in the tab group's Chrome color, giving the same
// at-a-glance grouping cue the browser's own tab strip uses.
func groupColorChip(color string) app.UI {
	return app.Span().Class("ph-group-chip").Style("background", groupColorCSS(color)).
		Attr("title", color)
}
