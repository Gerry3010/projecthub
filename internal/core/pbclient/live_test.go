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
	"os"
	"testing"
)

// Opt-in check against a real Passbubble: the unit tests above pin the client's
// behaviour against a stub, this one pins the *server contract* the renewal relies on
// (that /auth/refresh really returns a usable access token for a refresh token).
//
//	PB_LIVE=http://localhost:8765 PB_EMAIL=test@ph.local PB_PASSWORD=test1234 \
//	  go test ./internal/core/pbclient -run TestLiveRefresh -v
func TestLiveRefresh(t *testing.T) {
	base, email, password := os.Getenv("PB_LIVE"), os.Getenv("PB_EMAIL"), os.Getenv("PB_PASSWORD")
	if base == "" || email == "" || password == "" {
		t.Skip("set PB_LIVE/PB_EMAIL/PB_PASSWORD to run against a live Passbubble")
	}
	ctx := context.Background()

	c := New(base)
	resp, err := c.Login(ctx, LoginRequest{Email: email, Password: password})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if resp.RefreshToken == "" {
		t.Fatal("login returned no refresh token — the session cannot renew itself")
	}
	t.Logf("access token lives %ds", resp.ExpiresIn)
	c.SetSession(resp)
	if _, err := c.ListEntries(ctx); err != nil {
		t.Fatalf("ListEntries with a fresh token: %v", err)
	}

	// Stand in for an expired access token, keeping the (valid) refresh token: the
	// next call must renew and succeed instead of failing with 401.
	c.SetSession(&LoginResponse{AccessToken: "expired-token", RefreshToken: resp.RefreshToken, ExpiresIn: 3600})
	if _, err := c.ListEntries(ctx); err != nil {
		t.Fatalf("ListEntries after the access token died: %v", err)
	}
	if c.Token() == "expired-token" {
		t.Error("token was not renewed")
	}

	// Last line of defence: with the refresh token gone too, the Reauth hook (a full
	// re-login, what the desktop wires up) has to carry the session.
	reauths := 0
	c.Reauth = func(ctx context.Context) (*LoginResponse, error) {
		reauths++
		return c.Login(ctx, LoginRequest{Email: email, Password: password})
	}
	c.SetSession(&LoginResponse{AccessToken: "expired-token", RefreshToken: "dead-refresh-token", ExpiresIn: 3600})
	if _, err := c.ListEntries(ctx); err != nil {
		t.Fatalf("ListEntries with only the Reauth hook left: %v", err)
	}
	if reauths != 1 {
		t.Errorf("Reauth calls = %d, want 1", reauths)
	}
}
