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

	"github.com/google/uuid"
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
	Native *nativeclient.Client // nil in the hosted (non-Electron) build
	Cwd    string
	// OpenClaude starts a Claude terminal tile: prompt is sent on open, sessionID (the
	// chat selected in the list, "" for a new one) decides continue-vs-start.
	OpenClaude func(ctx app.Context, cwd, prompt, sessionID string)
	// OnActiveChat reports the currently open chat's title up to the tile chrome so the
	// tile toolbar can show which Claude chat is active (nil for the embedded sidebar).
	OnActiveChat func(title string)

	// Embedded turns the tile into the global sidebar chat: submitting a prompt runs a
	// headless Claude turn (Native.StartChat) and streams the reply in-place by polling
	// the transcript — no terminal tile. SystemPrompt is the manager persona + project
	// context baked in (see buildManagerSystemPrompt). The per-project Claude tile keeps
	// Embedded=false and starts a terminal via OpenClaude.
	Embedded     bool
	SystemPrompt string

	sessions []domain.CodeSession
	loaded   bool
	status   string

	selected  string // selected session id ("" = none)
	entries   []domain.TranscriptEntry
	tasks     []domain.ClaudeTask // the selected session's live task list
	loadingT  bool
	tStatus   string
	collapsed map[string]bool // thinking-block keys ("entryIdx-blockIdx") currently expanded

	// embedded-chat run state
	chatCwd string // effective cwd the sidecar used (for transcript polling)
	sending bool   // a headless turn is in flight (poll loop running)

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

// chatTitle returns a display title for a session id: its recorded title, else a short
// id, else "Claude". Used to surface the active chat in the tile toolbar.
func (t *claudeTile) chatTitle(sessionID string) string {
	if sessionID == "" {
		return "Claude"
	}
	for _, s := range t.sessions {
		if s.SessionID == sessionID && s.Title != "" {
			return s.Title
		}
	}
	if len(sessionID) > 8 {
		return "Chat " + sessionID[:8]
	}
	return "Chat " + sessionID
}

// notifyActiveChat pushes the active chat's title to the tile chrome (no-op in the
// embedded sidebar, which has no tile toolbar).
func (t *claudeTile) notifyActiveChat(sessionID string) {
	if t.OnActiveChat != nil {
		t.OnActiveChat(t.chatTitle(sessionID))
	}
}

// selectSession loads a chat's full transcript on click.
func (t *claudeTile) selectSession(sessionID string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) {
		if t.Native == nil {
			return
		}
		t.selected, t.entries, t.tasks, t.loadingT, t.tStatus = sessionID, nil, nil, true, ""
		t.notifyActiveChat(sessionID)
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
		// Load the session's task list in parallel (best-effort: no tasks just hides
		// the checklist section).
		ctx.Async(func() {
			tasks, err := native.Tasks(context.Background(), sessionID)
			if err != nil {
				return
			}
			ctx.Dispatch(func(ctx app.Context) {
				if t.selected == sessionID {
					t.tasks = tasks
				}
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

// submitPrompt sends the prompt. In the embedded sidebar it runs a headless Claude
// turn and streams the reply in-place (no terminal); in the per-project tile it opens
// a real `claude "<prompt>"` terminal via OpenClaude.
func (t *claudeTile) submitPrompt(ctx app.Context, _ app.Event) {
	if t.prompt == "" {
		return
	}
	if !t.Embedded {
		if t.OpenClaude == nil {
			return
		}
		// Continue the chat that is open in the tile instead of starting a second one
		// beside it — picking a session and then typing means "go on here".
		t.OpenClaude(ctx, t.Cwd, t.prompt, t.selected)
		t.prompt = ""
		return
	}
	if t.Native == nil || t.sending {
		return
	}
	prompt := t.prompt
	t.prompt = ""
	// A selected session continues (resume); no selection starts a fresh chat with a
	// client-minted session id.
	resume := t.selected != ""
	sid := t.selected
	if !resume {
		sid = uuid.NewString()
		t.selected = sid
		t.entries = nil
		t.tasks = nil
	}
	t.sending, t.tStatus = true, ""
	native, cwd, sp := t.Native, t.Cwd, t.SystemPrompt
	ctx.Async(func() {
		retSid, effCwd, err := native.StartChat(context.Background(), cwd, prompt, sp, sid, resume)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				t.sending, t.tStatus = false, err.Error()
				return
			}
			t.chatCwd = effCwd
			t.pollTranscript(ctx, retSid)
		})
	})
}

// pollTranscript streams a running headless turn into the view by re-reading the
// session transcript (~1s) until it stops growing and an assistant reply is present
// (or a safety cap is hit). Runs in one goroutine; each read dispatches an update.
func (t *claudeTile) pollTranscript(ctx app.Context, sid string) {
	native, cwd := t.Native, t.chatCwd
	ctx.Async(func() {
		var lastSig string
		stable := 0
		for i := 0; i < 180; i++ {
			entries, err := native.Transcript(context.Background(), cwd, sid)
			if err == nil {
				entries := entries
				ctx.Dispatch(func(ctx app.Context) {
					if t.selected == sid {
						t.entries = entries
					}
				})
				sig := transcriptSig(entries)
				if sig == lastSig {
					stable++
				} else {
					stable, lastSig = 0, sig
				}
				// Done once the reply stopped changing for ~3s and an assistant message
				// is present.
				if stable >= 3 && lastEntryIsAssistant(entries) {
					break
				}
			}
			time.Sleep(time.Second)
		}
		ctx.Dispatch(func(ctx app.Context) {
			if t.selected == sid {
				t.sending = false
			}
		})
	})
}

// newChat clears the active session so the next prompt starts a fresh embedded chat.
func (t *claudeTile) newChat(ctx app.Context, _ app.Event) {
	if t.sending {
		return
	}
	t.selected, t.entries, t.tasks, t.tStatus = "", nil, nil, ""
	t.notifyActiveChat("")
}

// transcriptSig is a cheap change signature over a transcript (entry/block counts plus
// the last block's text length) so polling can detect when a reply stops growing.
func transcriptSig(entries []domain.TranscriptEntry) string {
	if len(entries) == 0 {
		return "0"
	}
	last := entries[len(entries)-1]
	tail := 0
	if len(last.Blocks) > 0 {
		tail = len(last.Blocks[len(last.Blocks)-1].Text)
	}
	return fmt.Sprintf("%d/%d/%d", len(entries), len(last.Blocks), tail)
}

// lastEntryIsAssistant reports whether the transcript's final entry is an assistant
// message (i.e. the reply to the just-sent prompt has landed).
func lastEntryIsAssistant(entries []domain.TranscriptEntry) bool {
	return len(entries) > 0 && entries[len(entries)-1].Role == "assistant"
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
		hint := "← einen Chat wählen."
		if t.Embedded {
			hint = "Frag den ProjectHub-Assistenten unten — er kennt deine Projekte und plant projektübergreifend."
		}
		return app.Div().Class("ph-cc-detail").Body(
			app.P().Class("ph-muted").Text(hint),
		)
	}
	return app.Div().Class("ph-cc-detail").Body(
		t.taskList(),
		app.If(t.tStatus != "", func() app.UI { return app.P().Class("ph-err").Text(t.tStatus) }),
		app.If(t.loadingT, func() app.UI { return app.P().Class("ph-muted").Text("Lädt…") }),
		app.Range(t.entries).Slice(func(i int) app.UI { return t.renderEntry(i, t.entries[i]) }),
		app.If(!t.loadingT && t.tStatus == "" && len(t.entries) == 0, func() app.UI {
			return app.P().Class("ph-muted").Text("Kein Transcript.")
		}),
	)
}

// taskList renders the selected session's live task plan as a checklist, with a
// progress count. Hidden when the session recorded no tasks.
func (t *claudeTile) taskList() app.UI {
	if len(t.tasks) == 0 {
		return app.Div()
	}
	done := 0
	for _, tk := range t.tasks {
		if tk.Status == "completed" {
			done++
		}
	}
	return app.Div().Class("ph-cc-tasks").Body(
		app.Div().Class("ph-cc-tasks-head").Body(
			icon("tasks", 14),
			app.Span().Text(fmt.Sprintf("Aufgaben · %d/%d erledigt", done, len(t.tasks))),
		),
		app.Ul().Class("ph-list ph-cc-tasklist").Body(
			app.Range(t.tasks).Slice(func(i int) app.UI {
				tk := t.tasks[i]
				return app.Li().Class("ph-cc-taskitem ph-cc-task-"+orText(tk.Status, "pending")).Body(
					app.Span().Class("ph-cc-task-icon").Text(taskIcon(tk.Status)),
					app.Span().Class("ph-cc-task-text").Text(orText(tk.Subject, tk.ActiveForm)),
				)
			}),
		),
	)
}

// taskIcon maps a task status to a glyph.
func taskIcon(status string) string {
	switch status {
	case "completed":
		return "✅"
	case "in_progress":
		return "🔄"
	default:
		return "⬜"
	}
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

// inputBar is the sticky-footer prompt input. Embedded: send a headless turn (streams
// in-place) with a "Neuer Chat" reset; per-project tile: start a Claude terminal.
func (t *claudeTile) inputBar() app.UI {
	if !t.Embedded {
		return app.Div().Class("ph-cc-input").Body(
			app.Textarea().Class("ph-island-input").Placeholder("Prompt…").Text(t.prompt).OnChange(t.setPrompt),
			app.Button().Class("ph-btn ph-btn-icon").Disabled(t.prompt == "").OnClick(t.submitPrompt).Body(
				icon("play", 15),
				app.Span().Text("In Claude starten"),
			),
		)
	}
	sendLabel := "Senden"
	if t.sending {
		sendLabel = "Antwortet…"
	}
	return app.Div().Class("ph-cc-input ph-cc-input-embedded").Body(
		app.Div().Class("ph-cc-input-actions").Body(
			app.Button().Class("ph-link").Disabled(t.sending || (t.selected == "" && len(t.entries) == 0)).
				Text("+ Neuer Chat").OnClick(t.newChat),
			app.If(t.sending, func() app.UI {
				return app.Span().Class("ph-muted ph-cc-running").Text("● Claude denkt nach…")
			}),
		),
		app.Textarea().Class("ph-island-input").Placeholder("Frag den ProjectHub-Assistenten…").
			Text(t.prompt).OnChange(t.setPrompt),
		app.Button().Class("ph-btn ph-btn-icon").Disabled(t.prompt == "" || t.sending).OnClick(t.submitPrompt).Body(
			icon("chat", 15),
			app.Span().Text(sendLabel),
		),
	)
}
