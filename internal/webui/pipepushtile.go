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
	"crypto/ecdh"
	"encoding/json"
	"sort"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/core/store"
	"github.com/Gerry3010/projecthub/internal/native/nativeclient"
	"github.com/Gerry3010/projecthub/internal/pipepush"
	"github.com/Gerry3010/projecthub/internal/pipepush/ppcrypto"
)

// pipepushRunsLimit caps how many runs are fetched per pipeline; the overview
// only needs recent history, not the full archive.
const pipepushRunsLimit = 25

// ppRun bundles one pipepush run with its eagerly-decrypted payload.
// Decryption happens once at load time (the run count is bounded by
// pipepushRunsLimit × pipeline count, so this stays cheap) rather than per
// click, keeping the detail view's render trivial.
type ppRun struct {
	Run       pipepush.PPRun
	Payload   pipepush.RunPayload
	DecodeErr bool
}

// pipepushTile shows this project's linked pipepush CI runs, newest first, with
// a detail view of the selected run's decrypted commit/branch/message/logs. It
// needs both Store (to read the PipepushLink — base URL, project id, and the
// account email/password saved alongside it) and Native (the sidecar's
// same-origin pipepush proxy, since pipepush has no CORS for direct WASM
// fetches). All decryption (KDF, private-key unwrap, payload decrypt) happens
// here in WASM via internal/pipepush/ppcrypto — the sidecar only relays
// ciphertext.
type pipepushTile struct {
	app.Compo
	Store    *store.Store
	Native   *nativeclient.Client
	FolderID string

	loaded     bool
	configured bool // a link with base URL + project id + email + password exists
	status     string

	runs        []ppRun
	selectedRun string // Run.ID ("" = none)
}

func (t *pipepushTile) OnMount(ctx app.Context) {
	if t.Native == nil || t.Store == nil {
		t.loaded = true
		return
	}
	native, st, folderID := t.Native, t.Store, t.FolderID
	ctx.Async(func() {
		bg := context.Background()
		link, err := st.GetPipepushLink(bg, folderID)
		if err != nil {
			ctx.Dispatch(func(ctx app.Context) { t.loaded, t.status = true, err.Error() })
			return
		}
		if link == nil || link.Val.BaseURL == "" || link.Val.ProjectID == "" ||
			link.Val.Email == "" || link.Val.Password == "" {
			ctx.Dispatch(func(ctx app.Context) { t.loaded = true })
			return
		}
		l := link.Val

		loginResp, err := native.PipepushLogin(bg, l.BaseURL, l.Email, l.Password)
		if err != nil {
			ctx.Dispatch(func(ctx app.Context) {
				t.loaded, t.configured, t.status = true, true, "pipepush-Login fehlgeschlagen: "+err.Error()
			})
			return
		}
		privBytes, err := ppcrypto.DecryptPrivateKey(loginResp.EncryptedPrivateKey, loginResp.KDFSalt, l.Password)
		if err != nil {
			ctx.Dispatch(func(ctx app.Context) {
				t.loaded, t.configured, t.status = true, true, "Schlüssel entsperren fehlgeschlagen: "+err.Error()
			})
			return
		}
		priv, err := ppcrypto.PrivateKeyFromBytes(privBytes)
		if err != nil {
			ctx.Dispatch(func(ctx app.Context) {
				t.loaded, t.configured, t.status = true, true, "ungültiger Schlüssel: "+err.Error()
			})
			return
		}

		pipelines, err := native.PipepushPipelines(bg, l.BaseURL, loginResp.JWT, l.ProjectID)
		if err != nil {
			ctx.Dispatch(func(ctx app.Context) {
				t.loaded, t.configured, t.status = true, true, "Pipelines laden fehlgeschlagen: "+err.Error()
			})
			return
		}

		var runs []ppRun
		for _, pl := range pipelines {
			prs, err := native.PipepushRuns(bg, l.BaseURL, loginResp.JWT, pl.ID, pipepushRunsLimit)
			if err != nil {
				continue // one bad pipeline shouldn't blank the whole tile
			}
			for _, r := range prs {
				runs = append(runs, decryptRun(priv, r))
			}
		}
		sort.Slice(runs, func(i, j int) bool { return runs[i].Run.ReceivedAt.After(runs[j].Run.ReceivedAt) })

		ctx.Dispatch(func(ctx app.Context) {
			t.loaded, t.configured, t.status = true, true, ""
			t.runs = runs
		})
	})
}

// decryptRun decrypts one run's payload; a failure is shown in the UI rather
// than dropping the run (status is still meaningful even undecrypted).
func decryptRun(priv *ecdh.PrivateKey, r pipepush.PPRun) ppRun {
	out := ppRun{Run: r}
	plain, err := ppcrypto.DecryptString(priv, r.EncryptedPayload)
	if err != nil {
		out.DecodeErr = true
		return out
	}
	if err := json.Unmarshal([]byte(plain), &out.Payload); err != nil {
		out.DecodeErr = true
	}
	return out
}

func (t *pipepushTile) selectRun(runID string) app.EventHandler {
	return func(ctx app.Context, _ app.Event) { t.selectedRun = runID }
}

func (t *pipepushTile) Render() app.UI {
	if t.Native == nil || t.Store == nil {
		return app.Div().Class("ph-tilecontent").Body(
			app.P().Class("ph-muted").Text("Pipepush ist nur in der ProjectHub-Desktop-App verfügbar."),
		)
	}
	if !t.loaded {
		return app.Div().Class("ph-tilecontent").Body(app.P().Class("ph-muted").Text("Lädt…"))
	}
	if !t.configured {
		return app.Div().Class("ph-tilecontent").Body(
			app.P().Class("ph-muted").Text("Kein pipepush-Login hinterlegt — im Projekt unter „pipepush“ Base-URL, Projekt-UUID sowie Account-E-Mail und -Passwort eintragen."),
		)
	}
	return app.Div().Class("ph-tilecontent ph-pp").Body(
		app.If(t.status != "", func() app.UI { return app.P().Class("ph-err").Text(t.status) }),
		t.overview(),
		t.detail(),
	)
}

func (t *pipepushTile) overview() app.UI {
	return app.Div().Class("ph-pp-overview").Body(
		app.Ul().Class("ph-list").Body(
			app.Range(t.runs).Slice(func(i int) app.UI {
				r := t.runs[i]
				cls := "ph-item ph-pp-run"
				if r.Run.ID == t.selectedRun {
					cls += " ph-pp-run-active"
				}
				return app.Li().Class(cls).OnClick(t.selectRun(r.Run.ID)).Body(
					app.Span().Class("ph-pp-status").Text(statusIcon(r.Run.Status)),
					app.Div().Class("ph-suggest-info").Body(
						app.Span().Class("ph-title").Text(orText(runSummary(r), r.Run.ID)),
						app.Span().Class("ph-muted").Text(r.Run.ReceivedAt.Format("2006-01-02 15:04")),
					),
				)
			}),
			app.If(len(t.runs) == 0, func() app.UI {
				return app.Li().Class("ph-muted").Text("Keine Runs.")
			}),
		),
	)
}

// runSummary builds the overview row's title: "<branch> · <commit[:8]>" when
// decrypted, else empty (falls back to the run id).
func runSummary(r ppRun) string {
	if r.DecodeErr {
		return ""
	}
	commit := r.Payload.Commit
	if len(commit) > 8 {
		commit = commit[:8]
	}
	switch {
	case r.Payload.Branch != "" && commit != "":
		return r.Payload.Branch + " · " + commit
	case r.Payload.Branch != "":
		return r.Payload.Branch
	default:
		return commit
	}
}

func (t *pipepushTile) detail() app.UI {
	if t.selectedRun == "" {
		return app.Div().Class("ph-pp-detail").Body(app.P().Class("ph-muted").Text("← einen Run wählen."))
	}
	var r *ppRun
	for i := range t.runs {
		if t.runs[i].Run.ID == t.selectedRun {
			r = &t.runs[i]
			break
		}
	}
	if r == nil {
		return app.Div().Class("ph-pp-detail").Body(app.P().Class("ph-muted").Text("Run nicht mehr vorhanden."))
	}
	if r.DecodeErr {
		return app.Div().Class("ph-pp-detail").Body(
			app.P().Class("ph-err").Text("Entschlüsseln fehlgeschlagen."),
			app.P().Class("ph-muted").Text("Status: "+statusIcon(r.Run.Status)+" "+r.Run.Status),
		)
	}
	p := r.Payload
	return app.Div().Class("ph-pp-detail").Body(
		app.Div().Class("ph-pp-field").Body(app.Strong().Text("Status: "), app.Text(statusIcon(r.Run.Status)+" "+r.Run.Status)),
		app.If(p.Branch != "", func() app.UI {
			return app.Div().Class("ph-pp-field").Body(app.Strong().Text("Branch: "), app.Text(p.Branch))
		}),
		app.If(p.Commit != "", func() app.UI {
			return app.Div().Class("ph-pp-field").Body(app.Strong().Text("Commit: "), app.Text(p.Commit))
		}),
		app.If(p.Duration != "", func() app.UI {
			return app.Div().Class("ph-pp-field").Body(app.Strong().Text("Dauer: "), app.Text(p.Duration))
		}),
		app.If(p.Message != "", func() app.UI {
			return app.Div().Class("ph-pp-field").Body(app.Strong().Text("Nachricht: "), app.Text(p.Message))
		}),
		app.If(p.Logs != "", func() app.UI {
			return app.Div().Body(
				app.Div().Class("ph-pp-field").Body(app.Strong().Text("Logs:")),
				app.Pre().Class("ph-pp-logs").Text(p.Logs),
			)
		}),
	)
}

// statusIcon maps a pipepush run status to a small glyph for the overview/detail.
func statusIcon(status string) string {
	switch status {
	case pipepush.StatusSuccess:
		return "✅"
	case pipepush.StatusFailure:
		return "❌"
	case pipepush.StatusRunning:
		return "⏳"
	case pipepush.StatusCancelled:
		return "⊘"
	case pipepush.StatusSkipped:
		return "⏭"
	default:
		return "•"
	}
}
