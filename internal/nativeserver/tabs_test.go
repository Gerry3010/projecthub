// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package nativeserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/tabstate"
)

const testToken = "test-bearer-token"

func newTestServer() http.Handler {
	return New(testToken, nil, tabstate.New()).Handler()
}

func authedReq(method, path string, body []byte) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("Authorization", "Bearer "+testToken)
	return r
}

func TestTabsIngestThenListScopedToProject(t *testing.T) {
	h := newTestServer()

	body, _ := json.Marshal(domain.LiveBrowserGroups{
		Browser: "chrome",
		Groups: []domain.LiveTabGroup{
			{ProjectID: "p1", GroupKey: "Backend", Title: "Backend", Color: "blue",
				Tabs: []domain.LiveTab{{URL: "https://example.com", Title: "Example", Active: true}}},
			{ProjectID: "p2", GroupKey: "Docs", Title: "Docs", Color: "grey"},
		},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/tabs/ingest", body))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("ingest: want 204, got %d (%s)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodGet, "/tabs?project=p1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d", rec.Code)
	}
	var got []domain.LiveTabGroup
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Backend" || got[0].Browser != "chrome" {
		t.Fatalf("unexpected scoped list: %+v", got)
	}
	if len(got[0].Tabs) != 1 || got[0].Tabs[0].URL != "https://example.com" {
		t.Fatalf("unexpected tab: %+v", got[0].Tabs)
	}

	// unscoped list (debug) returns the whole browser snapshot
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodGet, "/tabs", nil))
	var snap []domain.LiveBrowserGroups
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snap) != 1 || len(snap[0].Groups) != 2 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}

func TestTabsRequireAuth(t *testing.T) {
	h := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/tabs", nil) // no bearer
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without token, got %d", rec.Code)
	}
}

func TestTabsIngestRejectsEmptyBrowser(t *testing.T) {
	h := newTestServer()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/tabs/ingest", []byte(`{"groups":[]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for missing browser, got %d", rec.Code)
	}
}

func TestProjectsRoundtrip(t *testing.T) {
	h := newTestServer()
	body, _ := json.Marshal([]domain.RosterEntry{{ID: "p1", Title: "ProjectHub"}, {ID: "p2", Title: "Pipepush"}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/projects", body))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("set roster: want 204, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodGet, "/projects", nil))
	var got []domain.RosterEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].Title != "ProjectHub" {
		t.Fatalf("unexpected roster: %+v", got)
	}
}

func TestTabsCommandEnqueueAndDrain(t *testing.T) {
	h := newTestServer()
	body, _ := json.Marshal(domain.TabCommand{Browser: "chrome", Action: "focusTab", TabID: 42})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/tabs/command", body))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("enqueue: want 204, got %d (%s)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodGet, "/tabs/commands?browser=chrome", nil))
	var got []domain.TabCommand
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].TabID != 42 {
		t.Fatalf("unexpected commands: %+v", got)
	}

	// drained — second read is empty
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodGet, "/tabs/commands?browser=chrome", nil))
	var got2 []domain.TabCommand
	_ = json.Unmarshal(rec.Body.Bytes(), &got2)
	if len(got2) != 0 {
		t.Fatalf("expected drained queue to be empty, got %+v", got2)
	}
}

func TestTabsCommandsRequiresBrowserParam(t *testing.T) {
	h := newTestServer()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodGet, "/tabs/commands", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 without browser param, got %d", rec.Code)
	}
}

func TestTabsBrowsersEndpoint(t *testing.T) {
	h := newTestServer()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodGet, "/tabs/browsers", nil))
	var got []string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("fresh server should report no browsers, got %v", got)
	}

	body, _ := json.Marshal(domain.LiveBrowserGroups{Browser: "brave", Groups: []domain.LiveTabGroup{
		{ProjectID: "p1", GroupKey: "G", Title: "G"},
	}})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/tabs/ingest", body))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("ingest: want 204, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodGet, "/tabs/browsers", nil))
	got = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0] != "brave" {
		t.Fatalf("expected [brave], got %v", got)
	}
}

// TestTabsCommandGroupManagementFieldsRoundtrip confirms the enqueue/drain handler is
// action-agnostic: the new group-management fields (Title/Color/ProjectID/URL) survive
// the JSON roundtrip through the queue untouched, same as the existing focus/openGroup
// fields — no server-side change was needed to support them.
func TestTabsCommandGroupManagementFieldsRoundtrip(t *testing.T) {
	h := newTestServer()
	body, _ := json.Marshal(domain.TabCommand{
		Browser: "brave", Action: "createGroup",
		Title: "Mission WS", Color: "blue", ProjectID: "p1",
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/tabs/command", body))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("enqueue: want 204, got %d (%s)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodGet, "/tabs/commands?browser=brave", nil))
	var got []domain.TabCommand
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 command, got %+v", got)
	}
	c := got[0]
	if c.Action != "createGroup" || c.Title != "Mission WS" || c.Color != "blue" || c.ProjectID != "p1" {
		t.Fatalf("unexpected roundtripped command: %+v", c)
	}
}
