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

// Package nmhost installs the native-messaging host manifest that lets ProjectHub's
// browser extension launch the cmd/tabhost relay. A Chromium-family browser only runs
// a native-messaging host it finds a manifest for, in a per-browser NativeMessagingHosts
// directory, whose allowed_origins pins the calling extension's id. This package writes
// that manifest (pointing at the tabhost binary) into every installed Chromium browser
// it can find, so the tabs feature works without the user hand-editing files.
package nmhost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// HostName is the reverse-DNS native-messaging host id. The extension's
// chrome.runtime.connectNative(HostName) must match, as must the manifest filename.
const HostName = "net.geraldhofbauer.projecthub.tabs"

// ExtensionID is the ProjectHub Live Tabs extension's fixed id, derived from the
// public "key" pinned in extension/chromium/manifest.json. It must appear verbatim in
// the manifest's allowed_origins or the browser refuses the connection.
const ExtensionID = "pcknaffknemkpjmbngjfcklnjknlngmo"

// manifest is the on-disk native-messaging host manifest schema.
type manifest struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Path           string   `json:"path"`
	Type           string   `json:"type"`
	AllowedOrigins []string `json:"allowed_origins"`
}

// Install writes the host manifest (pointing at hostBin, which should be an absolute
// path to the tabhost binary) into every installed Chromium-family browser's
// NativeMessagingHosts directory. It returns the manifest paths it wrote. A browser
// is considered installed if its top-level config directory already exists; the
// NativeMessagingHosts subdirectory is created as needed. Browsers that aren't present
// are skipped silently, so this is safe to call unconditionally on every launch.
func Install(hostBin string) ([]string, error) {
	if !filepath.IsAbs(hostBin) {
		abs, err := filepath.Abs(hostBin)
		if err != nil {
			return nil, err
		}
		hostBin = abs
	}
	m := manifest{
		Name:           HostName,
		Description:    "ProjectHub live browser-tabs relay",
		Path:           hostBin,
		Type:           "stdio",
		AllowedOrigins: []string{"chrome-extension://" + ExtensionID + "/"},
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}

	var written []string
	for _, base := range browserConfigDirs() {
		if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
			continue // browser not installed
		}
		dir := filepath.Join(base, "NativeMessagingHosts")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		path := filepath.Join(dir, HostName+".json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			continue
		}
		written = append(written, path)
	}
	return written, nil
}

// browserConfigDirs returns the top-level per-browser config directories whose
// NativeMessagingHosts subdir a manifest belongs in, for the current OS. Windows uses
// the registry rather than files and is intentionally unsupported here.
func browserConfigDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	switch runtime.GOOS {
	case "linux":
		cfg := filepath.Join(home, ".config")
		return []string{
			filepath.Join(cfg, "google-chrome"),
			filepath.Join(cfg, "google-chrome-beta"),
			filepath.Join(cfg, "google-chrome-unstable"),
			filepath.Join(cfg, "chromium"),
			filepath.Join(cfg, "BraveSoftware", "Brave-Browser"),
			filepath.Join(cfg, "microsoft-edge"),
			filepath.Join(cfg, "vivaldi"),
			filepath.Join(cfg, "opera"),
		}
	case "darwin":
		// macOS paths are correct; only signing/notarising tabhost is Apple-specific
		// follow-up work. Kept here so the feature is one build step from working there.
		app := filepath.Join(home, "Library", "Application Support")
		return []string{
			filepath.Join(app, "Google", "Chrome"),
			filepath.Join(app, "Chromium"),
			filepath.Join(app, "BraveSoftware", "Brave-Browser"),
			filepath.Join(app, "Microsoft Edge"),
			filepath.Join(app, "Vivaldi"),
			filepath.Join(app, "com.operasoftware.Opera"),
		}
	default:
		return nil
	}
}

// DefaultHostBin returns the expected tabhost path: a sibling of the running
// executable (phd and tabhost ship side by side in build/ and in the packaged app).
func DefaultHostBin() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := "tabhost"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(filepath.Dir(exe), name), nil
}

// InstallDefault installs the manifest pointing at DefaultHostBin. It is best-effort:
// it returns a descriptive error for logging but callers treat failure as non-fatal
// (only the tabs feature degrades).
func InstallDefault() ([]string, error) {
	bin, err := DefaultHostBin()
	if err != nil {
		return nil, err
	}
	written, err := Install(bin)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(bin); err != nil {
		return written, fmt.Errorf("manifest written but tabhost binary missing at %s (run `make build`)", bin)
	}
	return written, nil
}
