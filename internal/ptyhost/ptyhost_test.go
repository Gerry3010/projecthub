// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package ptyhost

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// readUntil reads WS frames until want appears or the deadline passes.
func readUntil(t *testing.T, ctx context.Context, c *websocket.Conn, want string) {
	t.Helper()
	var got strings.Builder
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("ws read: %v — got so far %q", err, got.String())
		}
		got.Write(data)
		if strings.Contains(got.String(), want) {
			return
		}
	}
	t.Fatalf("did not see %q; got %q", want, got.String())
}

// TestReattachReplaysScrollbackAndSurvivesDetach covers the PTY-hardening path: a
// dropped socket (renderer reload) must NOT kill the process, and reattaching by the
// same id must replay the scrollback. An explicit Close then reaps it.
func TestReattachReplaysScrollbackAndSurvivesDetach(t *testing.T) {
	h := New(4)
	id, err := h.Open(OpenRequest{Cmd: "/bin/cat", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = h.ServeWS(w, r, id)
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// First attach: cat echoes the marker, so it lands in the scrollback ring.
	c1, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	if err := c1.Write(ctx, websocket.MessageBinary, append([]byte{opData}, []byte("marker42\n")...)); err != nil {
		t.Fatalf("ws write: %v", err)
	}
	readUntil(t, ctx, c1, "marker42")

	// Drop the socket WITHOUT closing the session (simulates a renderer reload).
	_ = c1.Close(websocket.StatusNormalClosure, "reload")

	// The session must survive the detached socket.
	alive := false
	for i := 0; i < 50; i++ {
		if h.Has(id) {
			alive = true
		}
		time.Sleep(20 * time.Millisecond)
		if !h.Has(id) {
			alive = false
			break
		}
	}
	if !alive {
		t.Fatalf("session died after socket drop (should survive for reattach)")
	}

	// Reattach: the scrollback replay must contain the earlier marker.
	c2, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial 2 (reattach): %v", err)
	}
	defer c2.Close(websocket.StatusNormalClosure, "")
	readUntil(t, ctx, c2, "marker42")

	// Explicit Close reaps it.
	h.Close(id)
	if h.Has(id) {
		t.Fatalf("session still present after Close")
	}
}

// TestCloseLetsTheProcessSaveItsWork is the graceful-shutdown contract: closing a
// tile must give whatever runs in it a chance to finish writing. The stand-in for
// Claude Code's transcript here is a file the shell writes from its HUP trap — a
// SIGKILL teardown never produces it.
func TestCloseLetsTheProcessSaveItsWork(t *testing.T) {
	dir := t.TempDir()
	saved := filepath.Join(dir, "transcript")
	h := New(2)
	id, err := h.Open(OpenRequest{
		Cmd:  "/bin/sh",
		Args: []string{"-c", `trap 'printf saved > "$0"; exit 0' HUP; while :; do sleep 0.05; done`, saved},
		Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	// Let the shell install its trap before we pull the rug out.
	time.Sleep(400 * time.Millisecond)

	h.Close(id)
	if h.Has(id) {
		t.Error("Close must drop the session from the host immediately")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(saved); err == nil && string(b) == "saved" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the process never got to write its file — teardown was not graceful")
}

// TestCloseAllWaitsForEverySession: on sidecar shutdown we must not exit while the
// terminals are still saving, or they end up orphaned mid-write.
func TestCloseAllWaitsForEverySession(t *testing.T) {
	dir := t.TempDir()
	h := New(4)
	var files []string
	for i := range 2 {
		f := filepath.Join(dir, fmt.Sprintf("t%d", i))
		files = append(files, f)
		if _, err := h.Open(OpenRequest{
			Cmd:  "/bin/sh",
			Args: []string{"-c", `trap 'printf saved > "$0"; exit 0' HUP; while :; do sleep 0.05; done`, f},
			Cols: 80, Rows: 24,
		}); err != nil {
			t.Fatalf("open pty %d: %v", i, err)
		}
	}
	time.Sleep(400 * time.Millisecond)

	h.CloseAll()
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || string(b) != "saved" {
			t.Fatalf("%s not written when CloseAll returned (%v)", filepath.Base(f), err)
		}
	}
}

// TestShutdownKillsWhatIgnoresTheSignals — the grace period is a courtesy, not a
// hostage situation.
func TestShutdownKillsWhatIgnoresTheSignals(t *testing.T) {
	h := New(2)
	id, err := h.Open(OpenRequest{
		Cmd:  "/bin/sh",
		Args: []string{"-c", `trap '' HUP TERM; while :; do sleep 0.05; done`},
		Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	s := h.take(id)
	if s == nil {
		t.Fatal("session vanished before the test could take it")
	}
	start := time.Now()
	s.shutdown(600 * time.Millisecond)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("shutdown took %v — the kill fallback did not fire", d)
	}
}
