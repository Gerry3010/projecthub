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

// Command web is the ProjectHub frontend, compiled to WebAssembly
// (GOOS=js GOARCH=wasm) and run in the browser. It registers the component routes
// and hands control to the go-app runtime. Off-browser, RunWhenOnBrowser is a
// no-op, so this also compiles natively.
package main

import (
	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/webui"
)

func main() {
	app.Route("/", func() app.Composer { return &webui.Root{} })
	app.RunWhenOnBrowser()
}
