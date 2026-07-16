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

// Package server hosts the ProjectHub web app: it serves the go-app WASM frontend
// and reverse-proxies /pb/* to a Passbubble backend. The proxy is an OPAQUE
// pass-through — request and response bodies are never inspected or logged — so
// the ProjectHub server stays zero-knowledge: it only ever relays ciphertext and
// bearer tokens between the browser's WASM crypto and Passbubble.
package server

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// PBTarget holds the current Passbubble upstream, swappable at runtime (the desktop
// shell lets the user point the app at a different server without a restart). Reads
// are lock-free; the /pb proxy consults it on every request.
type PBTarget struct {
	v atomic.Pointer[url.URL]
}

// NewPBTarget parses raw and returns a target, or an error if it isn't a valid
// http/https origin.
func NewPBTarget(raw string) (*PBTarget, error) {
	t := &PBTarget{}
	if err := t.Set(raw); err != nil {
		return nil, err
	}
	return t, nil
}

// Set validates raw and atomically replaces the upstream. Rejects anything that
// isn't an absolute http(s) URL so a typo can't silently break all vault traffic.
func (t *PBTarget) Set(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("server URL must be http or https: %q", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("server URL is missing a host: %q", raw)
	}
	t.v.Store(u)
	return nil
}

// Get returns the current upstream (never nil once constructed via NewPBTarget/Set).
func (t *PBTarget) Get() *url.URL { return t.v.Load() }

// String returns the current upstream URL, or "" if unset.
func (t *PBTarget) String() string {
	if u := t.v.Load(); u != nil {
		return u.String()
	}
	return ""
}

// Config configures the HTTP server.
type Config struct {
	// PassbubbleURL is the upstream Passbubble origin (e.g. http://localhost:8080).
	// Used only when PBTarget is nil (New then builds a fixed target from it).
	PassbubbleURL string
	// PBTarget, when set, is the runtime-swappable upstream the /pb proxy consults;
	// the caller keeps a reference to change it live. Nil ⇒ built from PassbubbleURL.
	PBTarget *PBTarget
	// WebHandler serves the frontend (the go-app handler, or a static file server
	// during development). Mounted at "/".
	WebHandler http.Handler
	// NativeHandler, when set, is mounted at /native/* — the Electron desktop app's
	// local-machine API (filesystem, PTY, Claude scans). It MUST carry its own auth;
	// this package does not add it. Nil for the plain hosted web deployment.
	NativeHandler http.Handler
	// Embedded loosens the CSP for the Electron shell: the renderer is served from
	// this same loopback origin and must open WebSockets to it (the PTY stream). It
	// stays strict otherwise. Leave false for the hosted browser deployment.
	Embedded bool
}

// New builds the chi router: security headers, the /pb reverse proxy, a health
// check, the optional native API, and the web frontend.
func New(cfg Config) (http.Handler, error) {
	target := cfg.PBTarget
	if target == nil {
		var err error
		if target, err = NewPBTarget(cfg.PassbubbleURL); err != nil {
			return nil, err
		}
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	// NOTE: deliberately no body logging anywhere — bodies carry ciphertext +
	// bearer tokens. middleware.Logger only records method/path/status/latency.
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	// Compress responses — app.wasm is ~13 MB uncompressed but gzips to ~¼ of
	// that, and the browser transparently decodes Content-Encoding before
	// WebAssembly.instantiateStreaming sees it. NOTE: this only shrinks transfer
	// size; the on-disk/in-memory wasm stays large. Further size wins (TinyGo,
	// brotli, code-splitting) are tracked separately.
	r.Use(middleware.Compress(5, "application/wasm", "application/javascript", "text/css", "text/html", "image/svg+xml"))
	r.Use(securityHeaders(cfg.Embedded))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	r.Handle("/pb/*", pbProxy(target))

	if cfg.NativeHandler != nil {
		// Mount strips the /native prefix so the subrouter registers clean paths.
		r.Mount("/native", cfg.NativeHandler)
	}

	if cfg.WebHandler != nil {
		r.Handle("/*", cfg.WebHandler)
	}
	return r, nil
}

// pbProxy returns a reverse proxy mounted at /pb that forwards to the current
// Passbubble upstream (read from target on every request, so it can change at
// runtime), stripping the /pb prefix. It rewrites the Host header so the upstream
// accepts the request and never logs or buffers bodies.
func pbProxy(target *PBTarget) http.Handler {
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			u := target.Get()
			req.URL.Scheme = u.Scheme
			req.URL.Host = u.Host
			req.Host = u.Host
			path := "/" + strings.TrimPrefix(strings.TrimPrefix(req.URL.Path, "/pb"), "/")
			if base := strings.TrimRight(u.Path, "/"); base != "" {
				path = base + path // honour an upstream that lives under a base path
			}
			req.URL.Path = path
			req.URL.RawPath = ""
			// Strip any forwarded cookies/identity the upstream shouldn't see; the
			// only credential Passbubble needs is the Authorization bearer token,
			// which the browser sets explicitly.
		},
	}
}

// securityHeaders returns middleware applying a strict, E2E-appropriate security
// policy. Because all secrets live in the browser's WASM heap, an XSS would be
// catastrophic — hence a tight CSP. 'wasm-unsafe-eval' is required to instantiate
// the WebAssembly module. When embedded (Electron shell), connect-src also allows
// WebSockets to the loopback origin so the renderer can attach to the PTY stream;
// nothing else is loosened.
func securityHeaders(embedded bool) func(http.Handler) http.Handler {
	connectSrc := "connect-src 'self'"
	frameSrc := "frame-src 'none'"
	imgSrc := "img-src 'self' data:"
	if embedded {
		connectSrc += " ws://127.0.0.1:* wss://127.0.0.1:*"
		// The browser tile is an Electron <webview> loading arbitrary sites; allow it
		// (its guest content runs in its own process, not with vault access).
		frameSrc = "frame-src https: http:"
		// The live-tabs tile shows each open tab's favicon, served from the tab's own
		// (already-visited) origin. Images can't execute, so this stays safe.
		imgSrc = "img-src 'self' data: https: http:"
	}
	csp := "default-src 'self'; " +
		"script-src 'self' 'wasm-unsafe-eval'; " +
		"style-src 'self' 'unsafe-inline'; " +
		imgSrc + "; " +
		connectSrc + "; " +
		frameSrc + "; " +
		"object-src 'none'; " +
		"base-uri 'none'; " +
		"frame-ancestors 'none'"
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			next.ServeHTTP(w, r)
		})
	}
}
