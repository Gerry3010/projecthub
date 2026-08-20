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
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/discovery"
	"github.com/Gerry3010/projecthub/internal/nativeserver"
	"github.com/Gerry3010/projecthub/internal/nmhost"
	"github.com/Gerry3010/projecthub/internal/ptyhost"
	"github.com/Gerry3010/projecthub/internal/server"
	"github.com/Gerry3010/projecthub/internal/tabstate"
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

	// `phd --install-native-host` installs the browser-extension native-messaging host
	// manifest and exits — a standalone setup step (also runnable by hand). Normal
	// launches do the same install best-effort below, so this is rarely needed.
	if len(os.Args) > 1 && os.Args[1] == "--install-native-host" {
		written, err := nmhost.InstallDefault()
		for _, p := range written {
			log.Printf("installed native-host manifest: %s", p)
		}
		if err != nil {
			log.Fatalf("install native host: %v", err)
		}
		if len(written) == 0 {
			log.Print("no Chromium-family browser found — nothing installed")
		}
		return
	}

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
		// Default/Large drive the WASM-loading logo; without them go-app points the
		// loader <img> at an external github URL that our CSP blocks (broken image).
		Icon:    app.Icon{SVG: "/web/icon.svg", Default: "/web/icon.svg", Large: "/web/icon.svg"},
		Styles:  []string{"/web/theme.css", "/web/app.css", "/web/shell.css"},
		Scripts: []string{"/web/shell.js"}, // island layer (xterm/markdown/webview) + divider resize
	}
	// Give the WASM loader a real byte length for its 0→100% progress. The Compress
	// middleware strips Content-Length from the gzipped app.wasm, so the browser sees
	// none and the loader shows "NaN%". The on-disk size equals what the browser gets
	// after transparently decoding gzip, so the progress reads correctly.
	if fi, err := os.Stat(filepath.Join("web", "app.wasm")); err == nil {
		webHandler.WasmContentLength = fmt.Sprintf("%d", fi.Size())
	}

	ptys := ptyhost.New(32)
	tabs := tabstate.New()
	native := nativeserver.New(token, ptys, tabs)
	// Where we listen and which asset directory we serve — the app_info MCP tool
	// reports both, and only this process knows them (the port is random per launch,
	// and the web dir is relative to the cwd Electron started us in).
	native.SetRuntimeInfo(port, absWebDir())

	// The Passbubble upstream is runtime-swappable and device-local: a persisted
	// override (set in the login screen's Server field) wins over PASSBUBBLE_URL, so
	// the choice survives restarts and logouts without an env var.
	initialURL := pbURL
	if saved := loadServerURL(); saved != "" {
		initialURL = saved
	}
	target, err := server.NewPBTarget(initialURL)
	if err != nil {
		log.Fatalf("passbubble url %q: %v", initialURL, err)
	}
	native.SetServerHooks(
		func() string { return target.String() },
		func(raw string) error {
			if err := target.Set(raw); err != nil {
				return err
			}
			return saveServerURL(target.String())
		},
	)

	handler, err := server.New(server.Config{
		PBTarget:      target,
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
	// Also drop the endpoint into a discovery file so browser-spawned helpers (the
	// native-messaging host cmd/tabhost) can find this loopback API + token. Removed
	// again on shutdown so a stale token can't linger past this launch.
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := discovery.Write(discovery.Endpoint{Base: base, Token: token, PID: os.Getpid()}); err != nil {
		log.Printf("discovery file: %v", err) // non-fatal: only the tabs feature degrades
	}
	// Keep the browser-extension native-messaging host manifest current (path may move
	// between launches). Best-effort: a failure only disables the live-tabs feature.
	if written, err := nmhost.InstallDefault(); err != nil {
		log.Printf("native host: %v", err)
	} else if len(written) > 0 {
		log.Printf("native host manifest installed for %d browser(s)", len(written))
	}
	log.Printf("sidecar on 127.0.0.1:%d (proxying /pb → %s)", port, target.String())

	cleanup := func(reason string) {
		log.Printf("%s — shutting down", reason)
		_ = discovery.Remove()
		ptys.CloseAll()
		os.Exit(0)
	}
	go watchParent(func() { cleanup("parent gone") })

	// Graceful quit (Electron sends SIGTERM before SIGKILL): drop the discovery file
	// so no stale endpoint/token lingers.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() { cleanup((<-sig).String()) }()

	if err := srv.Serve(ln); err != nil {
		_ = discovery.Remove()
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

// absWebDir resolves the frontend asset directory the sidecar serves (relative to
// its working directory: the repo root in dev, the bundle's resources in prod).
func absWebDir() string {
	dir := env("WEB_DIR", "web")
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// serverURLPath is where the device-local Passbubble upstream override lives, next to
// the discovery file (<user-config-dir>/projecthub/server.url). It is account-
// independent — persists across logouts — and plain text (the URL isn't a secret).
func serverURLPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "projecthub", "server.url"), nil
}

func loadServerURL() string {
	p, err := serverURLPath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func saveServerURL(raw string) error {
	p, err := serverURLPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(raw+"\n"), 0o600)
}
