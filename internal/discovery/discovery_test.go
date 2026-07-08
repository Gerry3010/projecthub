// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package discovery

import (
	"os"
	"testing"
)

// TestWriteReadRemove exercises the discovery-file lifecycle against an isolated
// config dir (XDG_CONFIG_HOME / os.UserConfigDir points there on Linux).
func TestWriteReadRemove(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// os.UserConfigDir on macOS ignores XDG; skip there so CI on a Mac still passes.
	if _, err := os.UserConfigDir(); err != nil {
		t.Skipf("no user config dir: %v", err)
	}

	want := Endpoint{Base: "http://127.0.0.1:54123", Token: "deadbeef", PID: 4242}
	if err := Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}

	// file must be 0600 (contains a bearer token)
	path, _ := Path()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("want 0600, got %o", perm)
	}

	got, err := Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != want {
		t.Fatalf("roundtrip mismatch: got %+v want %+v", got, want)
	}

	if err := Remove(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := Read(); err == nil {
		t.Fatal("read after remove should fail")
	}
	// removing a missing file is not an error
	if err := Remove(); err != nil {
		t.Fatalf("remove missing: %v", err)
	}
}
