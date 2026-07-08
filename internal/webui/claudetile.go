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

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/native/nativeclient"
)

// claudeTile is Phase 1 of the Claude widget: a live overview of this project's
// Claude Code chats (via Native.Sessions, a fresh disk scan — no TUI capture
// wait) and, for a selected chat, its full transcript rendered as structured
// entries (text/thinking/tool-calls/results). The prompt input at the bottom
// does not talk to Claude in-tile yet (that's Phase 2, headless streaming) — it
// starts a real `claude "<prompt>"` session in a new Terminal tile via
// OpenClaude, reusing the same PTY infrastructure the session list's "Resume"
// already relies on.
type claudeTile struct {
	app.Compo
	Native     *nativeclient.Client // nil in the hosted (non-Electron) build
	Cwd        string
	OpenClaude func(ctx app.Context, cwd, prompt string)

	sessions []domain.CodeSession
	loaded   bool
	status   string

	selected  string // selected session id ("" = none)
	entries   []domain.TranscriptEntry
	loadingT  bool
	tStatus   string
	collapsed map[string]bool // thinking-block keys ("entryIdx-blockIdx") currently expanded

	prompt string
}

func (t *claudeTile) OnMount(ctx app.Context) {
	if t.Native == nil {
		t.loaded = true
		return
	}
	t.collapsed = map[string]bool{}
	native := t.Native
	cwd := t.Cwd
	ctx.Async(func() {
		sessions, err := native.Sessions(context.Background(), cwd)
		ctx.Dispatch(func(ctx app.Context) {
			t.loaded = true
			if err != nil {
				t.status = err.Error()
				return
			}
			t.sessions, t.status = sessions, ""
		})
	})
}

// selectSession loads a chat's full transcript on click.
func (t *claudeTile) selectSession(sessionID string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		if t.Native == nil {
			return
		}
		t.selected, t.entries, t.loadingT, t.tStatus = sessionID, nil, true, ""
		native, cwd := t.Native, t.Cwd
		ctx.Async(func() {
			entries, err := native.Transcript(context.Background(), cwd, sessionID)
			ctx.Dispatch(func(ctx app.Context) {
				t.loadingT = false
				if err != nil {
					t.tStatus = err.Error()
					return
				}
				t.entries = entries
			})
		})
	}
}

// toggleBlock expands/collapses a single thinking block (collapsed by default
// to keep the transcript scannable).
func (t *claudeTile) toggleBlock(key string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		t.collapsed[key] = !t.collapsed[key]
	}
}

func (t *claudeTile) setPrompt(ctx app.Context, e app.Event) {
	t.prompt = ctx.JSSrc().Get("value").String()
}

// submitPrompt starts a real `claude "<prompt>"` session in a new Terminal tile
// (see OpenClaude at construction, workspace.go) — Phase 1 delegates actually
// talking to Claude to the existing PTY-backed terminal rather than a headless
// invocation (Phase 2's job).
func (t *claudeTile) submitPrompt(ctx app.Context, _ app.Event) {
	if t.prompt == "" || t.OpenClaude == nil {
		return
	}
	t.OpenClaude(ctx, t.Cwd, t.prompt)
	t.prompt = ""
}

func (t *claudeTile) Render() app.UI {
	if t.Native == nil {
		return app.Div().Class("ph-tilecontent").Body(
			app.P().Class("ph-muted").Text("Claude ist nur in der ProjectHub-Desktop-App verfügbar."),
		)
	}
	return app.Div().Class("ph-tilecontent ph-claude").Body(
		t.overview(),
		t.detail(),
		t.inputBar(),
	)
}

// overview lists this project's chats, newest-first (as reported by Sessions).
func (t *claudeTile) overview() app.UI {
	return app.Div().Class("ph-cc-overview").Body(
		app.If(t.status != "", func() app.UI { return app.P().Class("ph-err").Text(t.status) }),
		app.Ul().Class("ph-list").Body(
			app.Range(t.sessions).Slice(func(i int) app.UI {
				s := t.sessions[i]
				cls := "ph-item ph-cc-session"
				if s.SessionID == t.selected {
					cls += " ph-cc-session-active"
				}
				return app.Li().Class(cls).OnClick(t.selectSession(s.SessionID)).Body(
					app.Div().Class("ph-suggest-info").Body(
						app.Span().Class("ph-title").Text(orText(s.Title, s.SessionID)),
						app.Span().Class("ph-muted").Text(s.LastActive.Format("2006-01-02 15:04")),
					),
				)
			}),
			app.If(t.loaded && len(t.sessions) == 0, func() app.UI {
				return app.Li().Class("ph-muted").Text("Keine Chats gefunden.")
			}),
		),
	)
}

// detail renders the selected chat's transcript.
func (t *claudeTile) detail() app.UI {
	if t.selected == "" {
		return app.Div().Class("ph-cc-detail").Body(
			app.P().Class("ph-muted").Text("← einen Chat wählen."),
		)
	}
	return app.Div().Class("ph-cc-detail").Body(
		app.If(t.tStatus != "", func() app.UI { return app.P().Class("ph-err").Text(t.tStatus) }),
		app.If(t.loadingT, func() app.UI { return app.P().Class("ph-muted").Text("Lädt…") }),
		app.Range(t.entries).Slice(func(i int) app.UI { return t.renderEntry(i, t.entries[i]) }),
		app.If(!t.loadingT && t.tStatus == "" && len(t.entries) == 0, func() app.UI {
			return app.P().Class("ph-muted").Text("Kein Transcript.")
		}),
	)
}

func (t *claudeTile) renderEntry(i int, e domain.TranscriptEntry) app.UI {
	roleLabel := "Claude"
	if e.Role == "user" {
		roleLabel = "Du"
	}
	return app.Div().Class("ph-cc-msg ph-cc-role-"+e.Role).Body(
		app.Div().Class("ph-cc-msghead").Body(
			app.Span().Class("ph-cc-role").Text(roleLabel),
			app.Span().Class("ph-muted").Text(e.Timestamp.Format("15:04:05")),
		),
		app.Range(e.Blocks).Slice(func(j int) app.UI {
			return t.renderBlock(fmt.Sprintf("%d-%d", i, j), e.Blocks[j])
		}),
	)
}

func (t *claudeTile) renderBlock(key string, b domain.TranscriptBlock) app.UI {
	switch b.Kind {
	case "thinking":
		expanded := t.collapsed[key]
		icon := "▸"
		if expanded {
			icon = "▾"
		}
		return app.Div().Class("ph-cc-think").Body(
			app.Button().Class("ph-cc-think-head").OnClick(t.toggleBlock(key)).Body(
				app.Text(icon+" 🧠 Thinking"),
			),
			app.If(expanded, func() app.UI { return app.Div().Class("ph-cc-think-body").Text(b.Text) }),
		)
	case "tool_use":
		return app.Div().Class("ph-cc-tool").Body(
			app.Div().Class("ph-cc-tool-head").Text("🔧 "+orText(b.ToolName, "Tool")),
			app.Pre().Class("ph-cc-tool-input").Text(b.ToolInput),
		)
	case "tool_result":
		cls := "ph-cc-result"
		if b.IsError {
			cls += " ph-cc-result-err"
		}
		return app.Div().Class(cls).Body(
			app.Div().Class("ph-cc-result-head").Text("⤷ Ergebnis"),
			app.Pre().Class("ph-cc-result-body").Text(b.Result),
		)
	case "image":
		return app.Span().Class("ph-cc-image").Text("🖼 Bild (" + orText(b.ImageMIME, "unbekannt") + ")")
	default: // "text"
		return app.Div().Class("ph-cc-text").Text(b.Text)
	}
}

// inputBar is the sticky-footer prompt input; submitting starts a fresh Claude
// terminal session with this prompt (Phase 1 — see submitPrompt).
func (t *claudeTile) inputBar() app.UI {
	return app.Div().Class("ph-cc-input").Body(
		app.Textarea().Class("ph-island-input").Placeholder("Prompt…").Text(t.prompt).OnChange(t.setPrompt),
		app.Button().Class("ph-btn").Text("▶ In Claude starten").Disabled(t.prompt == "").OnClick(t.submitPrompt),
	)
}
