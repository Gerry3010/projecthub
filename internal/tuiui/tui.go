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

// Package tuiui is the Bubble Tea companion UI for ProjectHub. It runs locally and
// performs the actions a hosted browser app cannot: capturing the current Firefox
// tabs into an encrypted tab set, restoring tab sets, and opening pinned files via
// the local index root. It shares internal/core with the web frontend, so all
// data is the same E2E-encrypted Passbubble entries.
package tuiui

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Gerry3010/projecthub/internal/core/crypto"
	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/pbclient"
	"github.com/Gerry3010/projecthub/internal/core/store"
	"github.com/Gerry3010/projecthub/internal/local"
	"github.com/Gerry3010/projecthub/internal/pipepush"
	"github.com/Gerry3010/projecthub/internal/tabsession"
)

type screen int

const (
	screenLogin screen = iota
	screenProjects
	screenProject
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#6d8bff"))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#6d8bff"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#8b93a1"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff6b6b"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#4ade80"))
)

// Config seeds the companion with environment defaults.
type Config struct {
	ServerURL string // Passbubble server (the TUI talks to it directly, not via the web proxy)
	IndexRoot string // local base dir mirrored to <IndexRoot>/<project-slug>/
}

// Model is the Bubble Tea model.
type Model struct {
	cfg    Config
	screen screen
	status string
	err    string
	busy   bool

	// login
	inputs []textinput.Model
	focus  int

	// session
	store    *store.Store
	email    string
	projects []domain.ProjectRef
	pcursor  int

	// project detail
	current domain.ProjectRef
	items   []detailItem
	dcursor int
}

// detailItem is a unified row in the project detail view: a tab set (restorable),
// a pinned path (openable), or a Claude Code session (resumable).
type detailItem struct {
	kind    domain.Kind
	id      string
	label   string
	tabs    []domain.Tab       // for tab sets
	pin     domain.PinnedItem  // for pins
	session domain.CodeSession // for Claude Code sessions
}

// New builds the initial model on the login screen.
func New(cfg Config) Model {
	server := textinput.New()
	server.Placeholder = "https://passbubble.example.com"
	server.SetValue(cfg.ServerURL)
	server.Focus()

	email := textinput.New()
	email.Placeholder = "you@example.com"

	pass := textinput.New()
	pass.Placeholder = "Master-Passwort"
	pass.EchoMode = textinput.EchoPassword

	return Model{
		cfg:    cfg,
		screen: screenLogin,
		inputs: []textinput.Model{server, email, pass},
	}
}

func (m Model) Init() tea.Cmd { return textinput.Blink }

// ─── messages ───────────────────────────────────────────────────────────────

type errMsg struct{ err error }
type loggedInMsg struct {
	store    *store.Store
	projects []domain.ProjectRef
	email    string
}
type projectsMsg struct{ projects []domain.ProjectRef }
type detailMsg struct{ items []detailItem }
type statusMsg struct{ text string }

// ─── update ─────────────────────────────────────────────────────────────────

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case errMsg:
		m.busy = false
		m.err = msg.err.Error()
		return m, nil
	case statusMsg:
		m.busy = false
		m.status, m.err = msg.text, ""
		return m, nil
	case loggedInMsg:
		m.busy, m.err = false, ""
		m.store, m.projects, m.email = msg.store, msg.projects, msg.email
		m.screen = screenProjects
		return m, nil
	case projectsMsg:
		m.busy = false
		m.projects = msg.projects
		if m.pcursor >= len(m.projects) {
			m.pcursor = max0(len(m.projects) - 1)
		}
		return m, nil
	case detailMsg:
		m.busy, m.err = false, ""
		m.items = msg.items
		m.dcursor = 0
		m.screen = screenProject
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// forward to login inputs
	if m.screen == screenLogin {
		var cmd tea.Cmd
		m.inputs[m.focus], cmd = m.inputs[m.focus].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.screen {
	case screenLogin:
		return m.keyLogin(msg)
	case screenProjects:
		return m.keyProjects(msg)
	case screenProject:
		return m.keyProject(msg)
	}
	return m, nil
}

func (m Model) keyLogin(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab", "down":
		m.focus = (m.focus + 1) % len(m.inputs)
		return m, m.refocus()
	case "shift+tab", "up":
		m.focus = (m.focus - 1 + len(m.inputs)) % len(m.inputs)
		return m, m.refocus()
	case "enter":
		if m.busy {
			return m, nil
		}
		m.busy, m.err = true, ""
		return m, m.login()
	}
	var cmd tea.Cmd
	m.inputs[m.focus], cmd = m.inputs[m.focus].Update(msg)
	return m, cmd
}

func (m *Model) refocus() tea.Cmd {
	var cmd tea.Cmd
	for i := range m.inputs {
		if i == m.focus {
			cmd = m.inputs[i].Focus()
		} else {
			m.inputs[i].Blur()
		}
	}
	return cmd
}

func (m Model) keyProjects(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "up", "k":
		m.pcursor = max0(m.pcursor - 1)
	case "down", "j":
		if m.pcursor < len(m.projects)-1 {
			m.pcursor++
		}
	case "enter":
		if len(m.projects) == 0 || m.busy {
			return m, nil
		}
		m.busy = true
		m.current = m.projects[m.pcursor]
		return m, m.loadDetail(m.current)
	case "s":
		if len(m.projects) == 0 || m.busy {
			return m, nil
		}
		m.busy, m.err, m.status = true, "", ""
		return m, m.saveFirefoxTabs(m.projects[m.pcursor])
	case "c":
		if len(m.projects) == 0 || m.busy {
			return m, nil
		}
		m.busy, m.err, m.status = true, "", ""
		return m, m.saveClaudeSessions(m.projects[m.pcursor])
	case "p":
		if len(m.projects) == 0 || m.busy {
			return m, nil
		}
		m.busy, m.err, m.status = true, "", ""
		return m, m.pingPipepush(m.projects[m.pcursor])
	}
	return m, nil
}

func (m Model) keyProject(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.screen = screenProjects
		m.status, m.err = "", ""
		return m, nil
	case "up", "k":
		m.dcursor = max0(m.dcursor - 1)
	case "down", "j":
		if m.dcursor < len(m.items)-1 {
			m.dcursor++
		}
	case "enter", "r", "o":
		if len(m.items) == 0 {
			return m, nil
		}
		return m, m.activate(m.items[m.dcursor])
	}
	return m, nil
}

// ─── commands ───────────────────────────────────────────────────────────────

func (m Model) login() tea.Cmd {
	server := m.inputs[0].Value()
	email := m.inputs[1].Value()
	password := m.inputs[2].Value()
	return func() tea.Msg {
		if server == "" || email == "" || password == "" {
			return errMsg{errors.New("Server, E-Mail und Passwort erforderlich")}
		}
		ctx := context.Background()
		api := pbclient.New(server)
		resp, err := api.Login(ctx, pbclient.LoginRequest{Email: email, Password: password})
		if err != nil {
			return errMsg{err}
		}
		if resp.RequiresTOTP() {
			return errMsg{errors.New("2FA wird im TUI noch nicht unterstützt")}
		}
		salt, e1 := b64d(resp.KDFSalt)
		encX, e2 := b64d(resp.EncPrivX25519)
		encM, e3 := b64d(resp.EncPrivMLKEM768)
		pubX, e4 := b64d(resp.PubX25519)
		pubM, e5 := b64d(resp.PubMLKEM768)
		if err := firstErr(e1, e2, e3, e4, e5); err != nil {
			return errMsg{err}
		}
		keys, err := crypto.Unlock(password, salt, resp.KDFTime, resp.KDFMemory, resp.UserID, encX, encM, pubX, pubM)
		if err != nil {
			return errMsg{err}
		}
		api.SetSession(resp)
		st := store.New(api, keys)
		projects, err := st.ListProjects(ctx)
		if err != nil {
			return errMsg{err}
		}
		return loggedInMsg{store: st, projects: projects, email: email}
	}
}

func (m Model) saveFirefoxTabs(p domain.ProjectRef) tea.Cmd {
	st := m.store
	return func() tea.Msg {
		tabs, err := tabsession.CaptureFirefox()
		if err != nil {
			return errMsg{err}
		}
		if len(tabs) == 0 {
			return errMsg{errors.New("keine offenen Firefox-Tabs gefunden")}
		}
		ts := domain.TabSet{
			Title:   "Firefox " + time.Now().Format("2006-01-02 15:04"),
			Browser: "firefox",
			Tabs:    tabs,
			SavedAt: time.Now(),
		}
		if _, err := st.CreateTabSet(context.Background(), p.FolderID, ts); err != nil {
			return errMsg{err}
		}
		return statusMsg{fmt.Sprintf("%d Tabs in %q gespeichert", len(tabs), p.Title)}
	}
}

// saveClaudeSessions scans the project's local dir for Claude Code sessions and
// stores a reference for each one not already saved (dedup by session id).
func (m Model) saveClaudeSessions(p domain.ProjectRef) tea.Cmd {
	st := m.store
	cwd := p.Cwd(m.cfg.IndexRoot)
	return func() tea.Msg {
		ctx := context.Background()
		found, err := tabsession.ScanClaudeSessions(cwd)
		if err != nil {
			return errMsg{err}
		}
		if len(found) == 0 {
			return errMsg{fmt.Errorf("keine Claude-Code-Sessions für %s gefunden", cwd)}
		}
		existing, err := st.ListCodeSessions(ctx, p.FolderID)
		if err != nil {
			return errMsg{err}
		}
		have := make(map[string]bool, len(existing))
		for _, e := range existing {
			have[e.Val.SessionID] = true
		}
		saved := 0
		for _, cs := range found {
			if have[cs.SessionID] {
				continue
			}
			if _, err := st.CreateCodeSession(ctx, p.FolderID, cs); err != nil {
				return errMsg{err}
			}
			saved++
		}
		return statusMsg{fmt.Sprintf("%d neue Claude-Session(s) gespeichert (%d insgesamt)", saved, len(found))}
	}
}

// pingPipepush sends a status webhook to the project's linked pipepush project,
// proving the integration end-to-end. Does nothing if no link is set.
func (m Model) pingPipepush(p domain.ProjectRef) tea.Cmd {
	st := m.store
	return func() tea.Msg {
		ctx := context.Background()
		link, err := st.GetPipepushLink(ctx, p.FolderID)
		if err != nil {
			return errMsg{err}
		}
		if link == nil {
			return errMsg{fmt.Errorf("kein pipepush-Link für %q (im Web verknüpfen)", p.Title)}
		}
		if link.Val.Token == "" {
			return errMsg{fmt.Errorf("pipepush-Link ohne Token — Webhook nicht möglich")}
		}
		req := pipepush.WebhookRequest{
			Token:    link.Val.Token,
			Status:   pipepush.StatusRunning,
			Pipeline: link.Val.Pipeline,
			Message:  "Ping von ProjectHub: " + p.Title,
		}
		if err := pipepush.SendWebhook(ctx, link.Val.BaseURL, req); err != nil {
			return errMsg{err}
		}
		return statusMsg{"pipepush-Webhook gesendet an " + link.Val.BaseURL}
	}
}

func (m Model) loadDetail(p domain.ProjectRef) tea.Cmd {
	st := m.store
	return func() tea.Msg {
		ctx := context.Background()
		tabsets, err := st.ListTabSets(ctx, p.FolderID)
		if err != nil {
			return errMsg{err}
		}
		pins, err := st.ListPins(ctx, p.FolderID)
		if err != nil {
			return errMsg{err}
		}
		sessions, err := st.ListCodeSessions(ctx, p.FolderID)
		if err != nil {
			return errMsg{err}
		}
		var items []detailItem
		for _, ts := range tabsets {
			items = append(items, detailItem{
				kind:  domain.KindTabSet,
				id:    ts.ID,
				label: fmt.Sprintf("[Tab-Set] %s (%d Tabs)", orDash(ts.Val.Title), len(ts.Val.Tabs)),
				tabs:  ts.Val.Tabs,
			})
		}
		for _, cs := range sessions {
			items = append(items, detailItem{
				kind:    domain.KindCodeSession,
				id:      cs.ID,
				label:   fmt.Sprintf("[Claude]  %s (%s)", orDash(cs.Val.Title), cs.Val.LastActive.Format("2006-01-02 15:04")),
				session: cs.Val,
			})
		}
		for _, pin := range pins {
			items = append(items, detailItem{
				kind:  domain.KindPin,
				id:    pin.ID,
				label: fmt.Sprintf("[Pfad]    %s → %s", orDash(pin.Val.Label), pin.Val.RelPath),
				pin:   pin.Val,
			})
		}
		return detailMsg{items: items}
	}
}

// activate performs an item's default action: restore a tab set, or open a pin.
func (m Model) activate(it detailItem) tea.Cmd {
	projectCwd := m.current.Cwd(m.cfg.IndexRoot)
	return func() tea.Msg {
		switch it.kind {
		case domain.KindTabSet:
			if err := local.RestoreTabs(it.tabs); err != nil {
				return errMsg{err}
			}
			return statusMsg{fmt.Sprintf("%d Tabs geöffnet", len(it.tabs))}
		case domain.KindCodeSession:
			cwd := it.session.Cwd
			if cwd == "" {
				cwd = projectCwd
			}
			if err := local.ResumeClaudeSession(cwd, it.session.SessionID); err != nil {
				return errMsg{err}
			}
			return statusMsg{"Claude-Session fortgesetzt: " + orDash(it.session.Title)}
		case domain.KindPin:
			abs := filepath.Join(projectCwd, it.pin.RelPath)
			if err := local.OpenPath(abs); err != nil {
				return errMsg{err}
			}
			return statusMsg{"geöffnet: " + abs}
		}
		return nil
	}
}

// ─── view ───────────────────────────────────────────────────────────────────

func (m Model) View() string {
	switch m.screen {
	case screenLogin:
		return m.viewLogin()
	case screenProjects:
		return m.viewProjects()
	case screenProject:
		return m.viewProject()
	}
	return ""
}

func (m Model) viewLogin() string {
	s := titleStyle.Render("ProjectHub — Companion") + "\n\n"
	labels := []string{"Server", "E-Mail", "Passwort"}
	for i, in := range m.inputs {
		s += labels[i] + "\n" + in.View() + "\n\n"
	}
	if m.busy {
		s += mutedStyle.Render("Entsperre…") + "\n"
	}
	s += m.footer("tab: Feld · enter: anmelden · ctrl+c: beenden")
	return s
}

func (m Model) viewProjects() string {
	s := titleStyle.Render("Projekte") + mutedStyle.Render("  ("+m.email+")") + "\n\n"
	if len(m.projects) == 0 {
		s += mutedStyle.Render("Keine Projekte. Lege welche im Web an.") + "\n"
	}
	for i, p := range m.projects {
		s += renderRow(i == m.pcursor, p.Title) + "\n"
	}
	s += "\n" + m.footer("↑/↓: wählen · enter: öffnen · s: Firefox-Tabs · c: Claude-Sessions · p: pipepush-Ping · q: beenden")
	return s
}

func (m Model) viewProject() string {
	s := titleStyle.Render(m.current.Title) + mutedStyle.Render("  /"+m.current.Slug) + "\n\n"
	if len(m.items) == 0 {
		s += mutedStyle.Render("Nichts gespeichert. Auf der Projektliste: 's' Firefox-Tabs, 'c' Claude-Sessions.") + "\n"
	}
	for i, it := range m.items {
		s += renderRow(i == m.dcursor, it.label) + "\n"
	}
	s += "\n" + m.footer("↑/↓: wählen · enter: Tab-Set / Claude-Session / Pfad aktivieren · esc: zurück")
	return s
}

func (m Model) footer(help string) string {
	var lines string
	if m.err != "" {
		lines += errStyle.Render("✗ "+m.err) + "\n"
	}
	if m.status != "" {
		lines += okStyle.Render("✓ "+m.status) + "\n"
	}
	return lines + mutedStyle.Render(help)
}

func renderRow(selected bool, text string) string {
	if selected {
		return cursorStyle.Render("› " + text)
	}
	return "  " + text
}

// ─── helpers ────────────────────────────────────────────────────────────────

func b64d(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
