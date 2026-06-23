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

// Command tui is the local Bubble Tea companion for ProjectHub. It shares the
// internal/core packages with the web frontend and performs the local-machine
// actions a hosted browser app cannot: reading browser session files to save tab
// sets, opening pinned files/dirs via xdg-open, and restoring tabs. Implementation
// is planned for the next phase (see the project plan).
package main

import "fmt"

func main() {
	fmt.Println("ProjectHub TUI companion — not yet implemented (see plan: Phase 2).")
}
