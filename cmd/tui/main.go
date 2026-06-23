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

// Command tui is the local Bubble Tea companion for ProjectHub. It performs the
// local-machine actions a hosted browser app cannot: saving the current Firefox
// tabs as an encrypted tab set, restoring tab sets, and opening pinned files via
// the local index root. It talks directly to the Passbubble server and shares the
// internal/core packages with the web frontend.
//
// Env:
//
//	PROJECTHUB_SERVER  Passbubble server URL (default http://localhost:8080)
//	PROJECTHUB_INDEX   local index root mirrored per project (default ~/ProjectIndex)
package main

import (
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Gerry3010/projecthub/internal/tuiui"
)

func main() {
	cfg := tuiui.Config{
		ServerURL: env("PROJECTHUB_SERVER", "http://localhost:8080"),
		IndexRoot: env("PROJECTHUB_INDEX", defaultIndexRoot()),
	}
	if _, err := tea.NewProgram(tuiui.New(cfg), tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tui:", err)
		os.Exit(1)
	}
}

func defaultIndexRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "ProjectIndex"
	}
	return filepath.Join(home, "ProjectIndex")
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
