// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package webui

import (
	"strings"
	"testing"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/store"
)

func todos(ids ...string) []store.Item[domain.TodoItem] {
	out := make([]store.Item[domain.TodoItem], len(ids))
	for i, id := range ids {
		out[i] = store.Item[domain.TodoItem]{ID: id, Val: domain.TodoItem{Text: id}}
	}
	return out
}

func ids(items []store.Item[domain.TodoItem]) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = it.ID
	}
	return strings.Join(parts, ",")
}

// The dragged todo must land exactly where the insertion marker promised: above the
// target when the pointer was in its upper half, below it otherwise — regardless of
// whether it came from above or below.
func TestReorderTodosLandsOnTheMarkedSide(t *testing.T) {
	cases := []struct {
		name           string
		from, to       string
		after          bool
		want           string
		wantNilBecause string
	}{
		{name: "down, before target", from: "a", to: "d", want: "b,c,a,d"},
		{name: "down, after target", from: "a", to: "d", after: true, want: "b,c,d,a"},
		{name: "up, before target", from: "d", to: "b", want: "a,d,b,c"},
		{name: "up, after target", from: "d", to: "b", after: true, want: "a,b,d,c"},
		{name: "to the very top", from: "c", to: "a", want: "c,a,b,d"},
		{name: "to the very bottom", from: "a", to: "d", after: true, want: "b,c,d,a"},
		{name: "onto the neighbour above", from: "c", to: "b", want: "a,c,b,d"},
		{name: "onto the neighbour below", from: "b", to: "c", after: true, want: "a,c,b,d"},

		{name: "onto itself", from: "b", to: "b", wantNilBecause: "no-op"},
		{name: "unknown source", from: "zz", to: "b", wantNilBecause: "id not in list"},
		{name: "unknown target", from: "b", to: "zz", wantNilBecause: "id not in list"},
		{name: "empty source", from: "", to: "b", wantNilBecause: "nothing dragged"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := todos("a", "b", "c", "d")
			got := reorderTodos(in, c.from, c.to, c.after)
			if c.wantNilBecause != "" {
				if got != nil {
					t.Fatalf("got %q, want nil (%s)", ids(got), c.wantNilBecause)
				}
				return
			}
			if got == nil {
				t.Fatalf("got nil, want %q", c.want)
			}
			if ids(got) != c.want {
				t.Errorf("got %q, want %q", ids(got), c.want)
			}
			if len(got) != len(in) {
				t.Errorf("length changed: %d → %d", len(in), len(got))
			}
			// The input slice must not be aliased into the result: the caller keeps
			// rendering from it until the assignment lands.
			if ids(in) != "a,b,c,d" {
				t.Errorf("input was mutated: %q", ids(in))
			}
		})
	}
}

// Moving to the same place must still produce a valid list (idempotent), not drop or
// duplicate the item.
func TestReorderTodosIdempotentNeighbourMove(t *testing.T) {
	in := todos("a", "b", "c")
	once := reorderTodos(in, "a", "b", true)
	if ids(once) != "b,a,c" {
		t.Fatalf("first move = %q", ids(once))
	}
	twice := reorderTodos(once, "a", "b", true)
	if ids(twice) != "b,a,c" {
		t.Errorf("repeating the same move = %q, want it to stay b,a,c", ids(twice))
	}
}

// A two-item list is the edge case where "before" and "after" are the only outcomes.
func TestReorderTodosTwoItems(t *testing.T) {
	in := todos("a", "b")
	if got := reorderTodos(in, "b", "a", false); ids(got) != "b,a" {
		t.Errorf("b before a = %q", ids(got))
	}
	if got := reorderTodos(in, "a", "b", true); ids(got) != "b,a" {
		t.Errorf("a after b = %q", ids(got))
	}
}
