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

// Command phd is the ProjectHub sidecar daemon spawned by the Electron shell. It
// serves the go-app WASM frontend + the /pb Passbubble proxy (so the renderer's
// origin is this loopback server and the existing WASM crypto is untouched) AND a
// token-guarded local-machine API (/native/*) for the things a browser sandbox
// can't do: Claude Code scans, PTY terminals, and Open-In.
//
// It binds a random port on 127.0.0.1, mints a per-launch bearer token, and writes
// ONE JSON handshake line to stdout — {"port":…,"token":…,"pid":…} — which the
// Electron main process reads before loading the renderer. All later stdout/stderr
// is logs. It exits when its parent (Electron) dies, so no orphan survives a crash.
//
// Env:
//
//	PASSBUBBLE_URL  upstream Passbubble origin (default http://localhost:8080)
//	WEB_DIR         directory of frontend assets to serve (default ./web)
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/nativeserver"
	"github.com/Gerry3010/projecthub/internal/ptyhost"
	"github.com/Gerry3010/projecthub/internal/server"
	"github.com/Gerry3010/projecthub/internal/webui"
)

// handshake is the single JSON line printed to stdout for the Electron main process.
type handshake struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("phd: ")
	pbURL := env("PASSBUBBLE_URL", "http://localhost:8080")

	// Bind a random loopback port up front so we can announce it in the handshake.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	token, err := mintToken()
	if err != nil {
		log.Fatalf("token: %v", err)
	}

	// The renderer (WASM) must declare the same routes as cmd/web.
	app.Route("/", func() app.Composer { return &webui.Root{} })
	webHandler := &app.Handler{
		Name:        "ProjectHub",
		ShortName:   "ProjectHub",
		Description: "Persönlicher Projekt-Manager mit E2E-Verschlüsselung über Passbubble.",
		Title:       "ProjectHub",
		Lang:        "de",
		Icon:        app.Icon{SVG: "/web/icon.svg"},
		Styles:      []string{"/web/app.css"},
	}

	ptys := ptyhost.New(32)
	native := nativeserver.New(token, ptys)

	handler, err := server.New(server.Config{
		PassbubbleURL: pbURL,
		WebHandler:    webHandler,
		NativeHandler: native.Handler(),
		Embedded:      true,
	})
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Announce ourselves, then serve.
	emitHandshake(handshake{Port: port, Token: token, PID: os.Getpid()})
	log.Printf("sidecar on 127.0.0.1:%d (proxying /pb → %s)", port, pbURL)

	go watchParent(func() {
		log.Print("parent gone — shutting down")
		ptys.CloseAll()
		os.Exit(0)
	})

	if err := srv.Serve(ln); err != nil {
		ptys.CloseAll()
		log.Fatalf("serve: %v", err)
	}
}

// mintToken returns a 32-byte random hex bearer token, fresh per launch.
func mintToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// emitHandshake writes the single machine-readable line the Electron main process
// parses. It must be the FIRST thing on stdout and valid JSON on its own line.
func emitHandshake(h handshake) {
	line, _ := json.Marshal(h)
	_, _ = os.Stdout.Write(append(line, '\n'))
}

// watchParent polls for the Electron parent dying. On Linux a re-parented orphan's
// PPID becomes 1 (init); when we detect that, we run onExit so a hard Electron crash
// can't leave the sidecar (and its PTYs) running.
func watchParent(onExit func()) {
	startPPID := os.Getppid()
	if startPPID <= 1 {
		return // already orphaned or run standalone (dev) — don't self-terminate
	}
	for range time.Tick(2 * time.Second) {
		if os.Getppid() != startPPID {
			onExit()
			return
		}
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
