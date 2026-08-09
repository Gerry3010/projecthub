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

package pbclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// pbStub is a Passbubble stand-in that only accepts one "current" bearer token and
// mints a new one on every refresh — enough to observe the client keeping its session
// alive around an expiring access token.
type pbStub struct {
	mu       sync.Mutex
	token    string // the only access token /entries accepts
	refresh  string // the only refresh token /auth/refresh accepts ("" ⇒ reject all)
	refreshN int32  // refresh calls
	loginN   int32  // login calls
	entryN   int32  // /entries calls (both the failing and the retried one)
	seen     []string
}

func (s *pbStub) session(access, refresh string, expiresIn int) map[string]any {
	return map[string]any{"access_token": access, "refresh_token": refresh, "expires_in": expiresIn}
}

func (s *pbStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.refreshN, 1)
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.refresh == "" || body.RefreshToken != s.refresh {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "refresh token expired"})
			return
		}
		s.token, s.refresh = "access-2", "refresh-2"
		_ = json.NewEncoder(w).Encode(s.session(s.token, s.refresh, 3600))
	})
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.loginN, 1)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.token, s.refresh = "access-3", "refresh-3"
		_ = json.NewEncoder(w).Encode(s.session(s.token, s.refresh, 3600))
	})
	mux.HandleFunc("/api/v1/entries", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.entryN, 1)
		s.mu.Lock()
		want, got := "Bearer "+s.token, r.Header.Get("Authorization")
		s.seen = append(s.seen, got)
		s.mu.Unlock()
		if got != want {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "token expired"})
			return
		}
		_ = json.NewEncoder(w).Encode([]EntryResponse{{ID: "e1"}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// A request that fails on an expired access token refreshes and retries itself, so
// the caller never sees the 401 — the bug where every tile eventually showed
// "api error 401" until the app was restarted.
func TestDoRefreshesAndRetriesOn401(t *testing.T) {
	stub := &pbStub{token: "access-2", refresh: "refresh-1"} // server already rotated
	c := New(stub.server(t).URL)
	c.SetSession(&LoginResponse{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 3600})

	entries, err := c.ListEntries(context.Background())
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if got := atomic.LoadInt32(&stub.refreshN); got != 1 {
		t.Errorf("refresh calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&stub.entryN); got != 2 {
		t.Errorf("entry calls = %d, want 2 (401 + retry)", got)
	}
	if c.Token() != "access-2" {
		t.Errorf("token = %q, want the refreshed one", c.Token())
	}
}

// When the refresh token has expired too (window open for days), the Reauth hook —
// a full re-login — is the fallback that keeps the session alive.
func TestDoReauthsWhenRefreshRejected(t *testing.T) {
	stub := &pbStub{token: "access-2", refresh: ""} // refresh token no longer accepted
	c := New(stub.server(t).URL)
	c.SetSession(&LoginResponse{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 3600})
	c.Reauth = func(ctx context.Context) (*LoginResponse, error) {
		return c.Login(ctx, LoginRequest{Email: "a@b.c", Password: "pw"})
	}

	if _, err := c.ListEntries(context.Background()); err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if got := atomic.LoadInt32(&stub.loginN); got != 1 {
		t.Errorf("login calls = %d, want 1", got)
	}
	if c.Token() != "access-3" {
		t.Errorf("token = %q, want the re-logged-in one", c.Token())
	}
}

// Without any way to renew, the 401 must surface unchanged (and as a typed APIError).
func TestDoSurfaces401WithoutRenewal(t *testing.T) {
	stub := &pbStub{token: "access-2"}
	c := New(stub.server(t).URL)
	c.SetToken("access-1")

	_, err := c.ListEntries(context.Background())
	if !isUnauthorized(err) {
		t.Fatalf("err = %v, want a 401 APIError", err)
	}
	if got := atomic.LoadInt32(&stub.entryN); got != 1 {
		t.Errorf("entry calls = %d, want 1 (no retry without a renewal path)", got)
	}
}

// A burst of parallel calls (every tile refreshing at once) must renew exactly once.
func TestParallel401sRenewOnce(t *testing.T) {
	stub := &pbStub{token: "access-2", refresh: "refresh-1"}
	c := New(stub.server(t).URL)
	c.SetSession(&LoginResponse{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 3600})

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = c.ListEntries(context.Background())
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&stub.refreshN); got != 1 {
		t.Errorf("refresh calls = %d, want 1", got)
	}
}

// A token whose known lifetime is nearly over is renewed *before* the request goes
// out, so the request never fails in the first place.
func TestRenewsBeforeExpiry(t *testing.T) {
	stub := &pbStub{token: "access-2", refresh: "refresh-1"}
	c := New(stub.server(t).URL)
	c.SetSession(&LoginResponse{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 5})

	if _, err := c.ListEntries(context.Background()); err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if got := atomic.LoadInt32(&stub.entryN); got != 1 {
		t.Errorf("entry calls = %d, want 1 (renewed up front, no failed attempt)", got)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.seen) != 1 || stub.seen[0] != "Bearer access-2" {
		t.Errorf("authorization headers = %v, want the refreshed token on the only call", stub.seen)
	}
}
