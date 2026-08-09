// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package control

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestHubCallRoundTrip(t *testing.T) {
	h := New()

	// A "renderer" that picks up one command and answers it.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		cmd, ok := h.Next(ctx)
		if !ok {
			return
		}
		h.Complete(Result{ID: cmd.ID, Result: json.RawMessage(`{"echo":"` + cmd.Tool + `"}`)})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := h.Call(ctx, "tile_list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if string(res) != `{"echo":"tile_list"}` {
		t.Fatalf("unexpected result: %s", res)
	}
}

func TestHubCallErrorPropagates(t *testing.T) {
	h := New()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		cmd, ok := h.Next(ctx)
		if ok {
			h.Complete(Result{ID: cmd.ID, Error: "boom"})
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := h.Call(ctx, "todo_create", nil); err == nil || err.Error() != "boom" {
		t.Fatalf("expected boom error, got %v", err)
	}
}

func TestHubCallTimesOutWithoutRenderer(t *testing.T) {
	h := New()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := h.Call(ctx, "tile_list", nil); err != ErrNoRenderer {
		t.Fatalf("expected ErrNoRenderer, got %v", err)
	}
}
