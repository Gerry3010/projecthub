// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package tabstate

import (
	"testing"
	"time"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

func group(projectID, title string, tabs ...domain.LiveTab) domain.LiveTabGroup {
	return domain.LiveTabGroup{ProjectID: projectID, GroupKey: title, Title: title, Color: "blue", Tabs: tabs}
}

func TestSetAndGroupsForProject(t *testing.T) {
	s := New()
	s.Set(domain.LiveBrowserGroups{Browser: "chrome", Groups: []domain.LiveTabGroup{
		group("p1", "Backend", domain.LiveTab{URL: "https://a", Active: true}),
		group("p2", "Docs", domain.LiveTab{URL: "https://b"}),
	}})
	s.Set(domain.LiveBrowserGroups{Browser: "brave", Groups: []domain.LiveTabGroup{
		group("p1", "Design", domain.LiveTab{URL: "https://c"}),
	}})

	p1 := s.GroupsForProject("p1")
	if len(p1) != 2 {
		t.Fatalf("want 2 groups for p1, got %d", len(p1))
	}
	// sorted by browser then title → brave/Design before chrome/Backend
	if p1[0].Browser != "brave" || p1[0].Title != "Design" {
		t.Fatalf("unexpected first group: %+v", p1[0])
	}
	if p1[1].Browser != "chrome" || p1[1].Title != "Backend" {
		t.Fatalf("unexpected second group: %+v", p1[1])
	}
	if got := s.GroupsForProject("p2"); len(got) != 1 || got[0].Title != "Docs" {
		t.Fatalf("unexpected p2 groups: %+v", got)
	}
	if got := s.GroupsForProject("nope"); len(got) != 0 {
		t.Fatalf("expected no groups for unknown project, got %d", len(got))
	}
}

func TestSetReplacesSameBrowser(t *testing.T) {
	s := New()
	s.Set(domain.LiveBrowserGroups{Browser: "chrome", Groups: []domain.LiveTabGroup{group("p1", "Old")}})
	s.Set(domain.LiveBrowserGroups{Browser: "chrome", Groups: []domain.LiveTabGroup{
		group("p1", "New1"), group("p1", "New2"),
	}})
	if got := s.GroupsForProject("p1"); len(got) != 2 || got[0].Title != "New1" {
		t.Fatalf("expected replaced groups, got %+v", got)
	}
}

func TestGroupsForProjectEvictsStale(t *testing.T) {
	now := time.Unix(1000, 0)
	s := &Store{ttl: 30 * time.Second, now: func() time.Time { return now }, by: map[string]domain.LiveBrowserGroups{}, cmds: map[string][]domain.TabCommand{}}
	s.Set(domain.LiveBrowserGroups{Browser: "chrome", Groups: []domain.LiveTabGroup{group("p1", "G")}})
	now = now.Add(31 * time.Second)
	if got := s.GroupsForProject("p1"); len(got) != 0 {
		t.Fatalf("stale browser should be evicted, got %+v", got)
	}
}

func TestRosterRoundtrip(t *testing.T) {
	s := New()
	if len(s.Roster()) != 0 {
		t.Fatal("fresh roster should be empty")
	}
	s.SetRoster([]domain.RosterEntry{{ID: "p1", Title: "ProjectHub"}, {ID: "p2", Title: "Pipepush"}})
	r := s.Roster()
	if len(r) != 2 || r[0].Title != "ProjectHub" {
		t.Fatalf("unexpected roster: %+v", r)
	}
}

func TestCommandQueue(t *testing.T) {
	s := New()
	s.Enqueue(domain.TabCommand{Browser: "chrome", Action: "focusTab", TabID: 7})
	s.Enqueue(domain.TabCommand{Browser: "chrome", Action: "openGroup", GroupID: 3})
	s.Enqueue(domain.TabCommand{Browser: "brave", Action: "focusTab", TabID: 1})
	s.Enqueue(domain.TabCommand{Browser: "", Action: "focusTab"}) // ignored

	got := s.DrainCommands("chrome")
	if len(got) != 2 || got[0].Action != "focusTab" || got[1].Action != "openGroup" {
		t.Fatalf("unexpected chrome commands: %+v", got)
	}
	// draining again yields nothing (consumed)
	if len(s.DrainCommands("chrome")) != 0 {
		t.Fatal("commands should be consumed on drain")
	}
	if len(s.DrainCommands("brave")) != 1 {
		t.Fatal("brave should still have its command")
	}
}
