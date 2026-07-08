// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Gerry3010/projecthub/internal/discovery"
)

func TestMessageRoundtrip(t *testing.T) {
	var buf bytes.Buffer
	want := map[string]any{"type": "tabs", "payload": map[string]any{"browser": "chrome"}}
	if err := writeMessage(&buf, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readMessage(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["type"] != "tabs" {
		t.Fatalf("unexpected: %+v", decoded)
	}
}

func TestReadMessageEOF(t *testing.T) {
	if _, err := readMessage(bytes.NewReader(nil)); err != io.EOF {
		t.Fatalf("want io.EOF on empty stream, got %v", err)
	}
}

func TestReadMessageTooLarge(t *testing.T) {
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], maxMessage+1)
	if _, err := readMessage(bytes.NewReader(lenBuf[:])); err == nil {
		t.Fatal("want error for oversized message")
	}
}

func setupDiscovery(t *testing.T, base string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := os.UserConfigDir(); err != nil {
		t.Skipf("no user config dir: %v", err)
	}
	if err := discovery.Write(discovery.Endpoint{Base: base, Token: "tok123", PID: 1}); err != nil {
		t.Fatalf("write discovery: %v", err)
	}
}

func TestHandleTabsForwardsAndSetsBrowser(t *testing.T) {
	var gotBody []byte
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/native/tabs/ingest" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	setupDiscovery(t, srv.URL)

	h := newHost(io.Discard, srv.Client())
	payload := []byte(`{"browser":"chrome","groups":[{"project_id":"p1","title":"Backend"}]}`)
	reply := h.handle([]byte(`{"type":"tabs","payload":` + string(payload) + `}`))

	ack, ok := reply.(ackReply)
	if !ok || !ack.OK {
		t.Fatalf("expected ok ack, got %+v", reply)
	}
	if gotAuth != "Bearer tok123" {
		t.Fatalf("bad auth header: %q", gotAuth)
	}
	if !bytes.Equal(gotBody, payload) {
		t.Fatalf("payload not passed through verbatim: %s", gotBody)
	}
	if h.getBrowser() != "chrome" {
		t.Fatalf("expected browser sniffed as chrome, got %q", h.getBrowser())
	}
}

func TestHandleTabsNoSidecar(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no discovery file written
	if _, err := os.UserConfigDir(); err != nil {
		t.Skipf("no user config dir: %v", err)
	}
	h := newHost(io.Discard, http.DefaultClient)
	reply := h.handle([]byte(`{"type":"tabs","payload":{"browser":"chrome"}}`))
	ack, ok := reply.(ackReply)
	if !ok || ack.OK {
		t.Fatalf("expected failing ack when sidecar is absent, got %+v", reply)
	}
}

func TestHandleUnknownType(t *testing.T) {
	h := newHost(io.Discard, http.DefaultClient)
	reply := h.handle([]byte(`{"type":"bogus"}`))
	ack, ok := reply.(ackReply)
	if !ok || ack.OK {
		t.Fatalf("expected failing ack for unknown type, got %+v", reply)
	}
}

func TestHandleGetProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/native/projects" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"p1","title":"ProjectHub"}]`))
	}))
	defer srv.Close()
	setupDiscovery(t, srv.URL)

	h := newHost(io.Discard, srv.Client())
	reply := h.handle([]byte(`{"type":"getProjects"}`))
	pr, ok := reply.(projectsReply)
	if !ok || !pr.OK || len(pr.Data) != 1 || pr.Data[0].Title != "ProjectHub" {
		t.Fatalf("unexpected projects reply: %+v", reply)
	}
}

func TestPollCommandsPushesFrames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/native/tabs/commands" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"browser":"chrome","action":"focusTab","tab_id":7}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	setupDiscovery(t, srv.URL)

	var out bytes.Buffer
	h := newHost(&out, srv.Client())
	h.setBrowser("chrome")

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { h.pollCommands(20*time.Millisecond, stop); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	bufLen := func() int {
		h.outMu.Lock()
		defer h.outMu.Unlock()
		return out.Len()
	}
	for bufLen() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	<-done // wait for the goroutine to fully exit before touching `out` unsynchronized

	if bufLen() == 0 {
		t.Fatal("expected at least one command frame to be written")
	}
	frame, err := readMessage(&out)
	if err != nil {
		t.Fatalf("read pushed frame: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(frame, &decoded); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	if decoded["type"] != "command" || decoded["action"] != "focusTab" {
		t.Fatalf("unexpected command frame: %+v", decoded)
	}
}

func TestPollCommandsIdleWithoutBrowser(t *testing.T) {
	var out bytes.Buffer
	h := newHost(&out, http.DefaultClient)
	// browser never set — poll should stay a no-op
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { h.pollCommands(10*time.Millisecond, stop); close(done) }()
	time.Sleep(50 * time.Millisecond)
	close(stop)
	<-done
	if out.Len() != 0 {
		t.Fatalf("expected no frames written without a known browser, got %d bytes", out.Len())
	}
}
