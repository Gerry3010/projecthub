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

// Package discovery brokers the sidecar's per-launch loopback endpoint (base URL +
// bearer token) to sibling processes that the browser — not Electron — spawns, above
// all the native-messaging host cmd/tabhost. Electron learns the endpoint from phd's
// stdout handshake, but a native-messaging host is started by the browser and shares
// no pipe with phd, so phd also drops the same facts into a 0600 file in the user
// config dir. The host reads it to know where to POST the tabs. The file is rewritten
// each launch and removed on shutdown, so a stale token can't linger.
package discovery

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Endpoint is the loopback sidecar coordinate a spawned helper needs to reach the
// /native API. Token is the per-launch bearer; it is a secret, hence the 0600 file.
type Endpoint struct {
	Base  string `json:"base"`  // e.g. http://127.0.0.1:54123
	Token string `json:"token"` // per-launch bearer token
	PID   int    `json:"pid"`   // sidecar pid, for staleness checks / debugging
}

// Path returns the discovery file location: <user-config-dir>/projecthub/endpoint.json.
// os.UserConfigDir is OS-aware (Linux: $XDG_CONFIG_HOME or ~/.config; macOS:
// ~/Library/Application Support; Windows: %AppData%), so the Mac path comes for free.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "projecthub", "endpoint.json"), nil
}

// Write atomically persists e to the discovery file (0600), creating the parent dir.
func Write(e Endpoint) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Read loads the discovery file written by the running sidecar.
func Read() (Endpoint, error) {
	var e Endpoint
	path, err := Path()
	if err != nil {
		return e, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return e, err
	}
	err = json.Unmarshal(data, &e)
	return e, err
}

// Remove deletes the discovery file. A missing file is not an error.
func Remove() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
