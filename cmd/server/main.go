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

// Command server runs the ProjectHub web server: it serves the WASM frontend and
// reverse-proxies /pb/* to a Passbubble backend.
//
// Env:
//
//	PORT            listen port (default 8090)
//	PASSBUBBLE_URL  upstream Passbubble origin (default http://localhost:8080)
//	WEB_DIR         directory of frontend assets to serve (default ./web)
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/server"
	"github.com/Gerry3010/projecthub/internal/webui"
)

func main() {
	port := env("PORT", "8090")
	pbURL := env("PASSBUBBLE_URL", "http://localhost:8080")

	// go-app only serves the HTML shell for *registered* routes, so the server
	// binary must declare the same routes as the WASM binary (cmd/web).
	app.Route("/", func() app.Composer { return &webui.Root{} })

	// go-app serves the frontend: it generates index.html, serves /app.js and
	// /wasm_exec.js from its embedded runtime, and resolves /web/app.wasm from the
	// local ./web directory (LocalDir default). app.wasm is produced by building
	// ./cmd/web for GOOS=js GOARCH=wasm into ./web/app.wasm.
	webHandler := &app.Handler{
		Name:        "ProjectHub",
		ShortName:   "ProjectHub",
		Description: "Persönlicher Projekt-Manager mit E2E-Verschlüsselung über Passbubble.",
		Title:       "ProjectHub",
		Lang:        "de",
		Icon:        app.Icon{SVG: "/web/icon.svg", Default: "/web/icon.svg", Large: "/web/icon.svg"},
		Styles:      []string{"/web/theme.css", "/web/app.css", "/web/shell.css"},
		Scripts:     []string{"/web/shell.js"},
	}
	// Real byte length for the WASM loader's progress bar (Compress strips the gzipped
	// app.wasm's Content-Length → loader would otherwise show "NaN%").
	if fi, err := os.Stat(filepath.Join(env("WEB_DIR", "web"), "app.wasm")); err == nil {
		webHandler.WasmContentLength = fmt.Sprintf("%d", fi.Size())
	}

	handler, err := server.New(server.Config{
		PassbubbleURL: pbURL,
		WebHandler:    webHandler,
	})
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("ProjectHub listening on :%s (proxying /pb → %s)", port, pbURL)
	log.Fatal(srv.ListenAndServe())
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
