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

package tuiui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

func TestLoginRequiresAllFields(t *testing.T) {
	m := New(Config{ServerURL: ""}) // all inputs empty
	msg := m.login()()              // run the command
	e, ok := msg.(errMsg)
	if !ok {
		t.Fatalf("expected errMsg for empty login, got %T", msg)
	}
	if e.err == nil {
		t.Fatal("expected an error")
	}
}

func TestLoggedInMsgSwitchesToProjects(t *testing.T) {
	m := New(Config{})
	refs := []domain.ProjectRef{{ID: "1", Title: "A"}, {ID: "2", Title: "B"}}
	next, _ := m.Update(loggedInMsg{projects: refs, email: "me@x"})
	mm := next.(Model)
	if mm.screen != screenProjects {
		t.Fatalf("expected screenProjects, got %d", mm.screen)
	}
	if len(mm.projects) != 2 || mm.email != "me@x" {
		t.Fatalf("session state not applied: %+v", mm)
	}
}

func TestProjectCursorNavigation(t *testing.T) {
	m := New(Config{})
	m.screen = screenProjects
	m.projects = []domain.ProjectRef{{Title: "A"}, {Title: "B"}, {Title: "C"}}

	down := tea.KeyMsg{Type: tea.KeyDown}
	step := func(mod tea.Model) tea.Model { n, _ := mod.Update(down); return n }

	cur := step(step(step(m))).(Model) // 3 downs on a 3-item list → clamps at 2
	if cur.pcursor != 2 {
		t.Fatalf("expected cursor clamped at 2, got %d", cur.pcursor)
	}

	up := tea.KeyMsg{Type: tea.KeyUp}
	n, _ := cur.Update(up)
	if n.(Model).pcursor != 1 {
		t.Fatalf("expected cursor 1 after up, got %d", n.(Model).pcursor)
	}
}

func TestErrMsgSetsError(t *testing.T) {
	m := New(Config{})
	m.busy = true
	next, _ := m.Update(errMsg{err: errTest("boom")})
	mm := next.(Model)
	if mm.err != "boom" || mm.busy {
		t.Fatalf("errMsg not handled: err=%q busy=%v", mm.err, mm.busy)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
