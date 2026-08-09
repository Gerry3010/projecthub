// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package nmhost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallWritesManifestForPresentBrowsers(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("path layout asserted for linux")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Pretend Chromium is installed (config dir exists) but Vivaldi is not.
	chromium := filepath.Join(home, ".config", "chromium")
	if err := os.MkdirAll(chromium, 0o755); err != nil {
		t.Fatal(err)
	}

	written, err := Install("/opt/projecthub/tabhost")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("expected 1 manifest (only chromium present), got %d: %v", len(written), written)
	}

	want := filepath.Join(chromium, "NativeMessagingHosts", HostName+".json")
	if written[0] != want {
		t.Fatalf("manifest path: got %s want %s", written[0], want)
	}

	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if m.Name != HostName || m.Type != "stdio" {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	if m.Path != "/opt/projecthub/tabhost" {
		t.Fatalf("host path: %s", m.Path)
	}
	if len(m.AllowedOrigins) != 1 || m.AllowedOrigins[0] != "chrome-extension://"+ExtensionID+"/" {
		t.Fatalf("allowed_origins: %v", m.AllowedOrigins)
	}
}

func TestInstallSkipsAbsentBrowsers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // no browser config dirs created

	written, err := Install("/opt/projecthub/tabhost")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(written) != 0 {
		t.Fatalf("expected nothing written, got %v", written)
	}
}

func TestInstallMakesPathAbsolute(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("path layout asserted for linux")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".config", "google-chrome"), 0o755); err != nil {
		t.Fatal(err)
	}

	written, err := Install("relative/tabhost")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("expected 1 manifest, got %d", len(written))
	}
	data, _ := os.ReadFile(written[0])
	var m manifest
	_ = json.Unmarshal(data, &m)
	if !filepath.IsAbs(m.Path) {
		t.Fatalf("host path should be absolute, got %s", m.Path)
	}
}
