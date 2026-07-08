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
// button focuses or reopens the whole group.
type tabsTile struct {
	app.Compo
	Native    *nativeclient.Client // nil in the hosted (non-Electron) build
	ProjectID string

	groups []domain.LiveTabGroup
	loaded bool
	status string
	stop   chan struct{}
}

const tabsPollInterval = 2500 * time.Millisecond

func (t *tabsTile) OnMount(ctx app.Context) {
	if t.Native == nil {
		t.loaded = true
		return
	}
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

func (t *tabsTile) Render() app.UI {
	if t.Native == nil {
		return app.Div().Class("ph-tilecontent").Body(
			app.P().Class("ph-muted").Text("Live-Tabs sind nur in der ProjectHub-Desktop-App verfügbar."),
		)
	}
	return app.Div().Class("ph-tilecontent").Body(
		app.If(t.status != "", func() app.UI { return app.P().Class("ph-err").Text(t.status) }),
		app.Range(t.groups).Slice(func(i int) app.UI {
			g := t.groups[i]
			return app.Div().Class("ph-tabgroup").Body(
				app.Div().Class("ph-tabgroup-head").Body(
					groupColorChip(g.Color),
					browserIcon(g.Browser, 14),
					app.Span().Class("ph-title").Text(orText(g.Title, "(ohne Titel)")),
					app.Span().Class("ph-muted").Text(fmt.Sprintf("%d", len(g.Tabs))),
					app.Div().Class("ph-spacer"),
					app.Button().Class("ph-tile-btn").Title("Gruppe öffnen/fokussieren").Text("↗").
						OnClick(t.openGroup(g)),
				),
				app.Ul().Class("ph-list").Body(
					app.Range(g.Tabs).Slice(func(j int) app.UI {
						tab := g.Tabs[j]
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
						)
					}),
				),
			)
		}),
		app.If(t.loaded && len(t.groups) == 0 && t.status == "", func() app.UI {
			return app.P().Class("ph-muted").Text("Noch keine Tab-Gruppe gekoppelt — im Extension-Popup zuordnen.")
		}),
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
