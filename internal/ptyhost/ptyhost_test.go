// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package ptyhost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestServeWSEchoesAndStaysOpen drives the real PTY→WS path: it runs `cat` (which
// echoes stdin and stays alive), streams a line in over the socket, and expects it
// back. This is the exact path the terminal tile uses; a session that closes
// immediately (the "[Sitzung beendet]" bug) would fail here.
func TestServeWSEchoesAndStaysOpen(t *testing.T) {
	h := New(4)
	id, err := h.Open(OpenRequest{Cmd: "/bin/cat", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = h.ServeWS(w, r, id)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// OP_DATA + payload → cat echoes it straight back out the PTY.
	if err := c.Write(ctx, websocket.MessageBinary, append([]byte{opData}, []byte("ping\n")...)); err != nil {
		t.Fatalf("ws write: %v", err)
	}

	// Read frames until we see our echo (a PTY may split/merge writes).
	var got strings.Builder
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("ws read (session closed too early?): %v — got so far %q", err, got.String())
		}
		got.Write(data)
		if strings.Contains(got.String(), "ping") {
			return // success
		}
	}
	t.Fatalf("did not receive echo; got %q", got.String())
}
