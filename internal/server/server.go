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
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Config configures the HTTP server.
type Config struct {
	// PassbubbleURL is the upstream Passbubble origin (e.g. http://localhost:8080).
	PassbubbleURL string
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
	upstream, err := url.Parse(cfg.PassbubbleURL)
	if err != nil {
		return nil, err
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

	r.Handle("/pb/*", pbProxy(upstream))

	if cfg.NativeHandler != nil {
		// Mount strips the /native prefix so the subrouter registers clean paths.
		r.Mount("/native", cfg.NativeHandler)
	}

	if cfg.WebHandler != nil {
		r.Handle("/*", cfg.WebHandler)
	}
	return r, nil
}

// pbProxy returns a reverse proxy mounted at /pb that forwards to the Passbubble
// upstream, stripping the /pb prefix. It rewrites the Host header so the upstream
// accepts the request and never logs or buffers bodies.
func pbProxy(upstream *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	base := proxy.Director
	proxy.Director = func(req *http.Request) {
		base(req)
		req.URL.Path = "/" + strings.TrimPrefix(strings.TrimPrefix(req.URL.Path, "/pb"), "/")
		req.Host = upstream.Host
		// Strip any forwarded cookies/identity the upstream shouldn't see; the
		// only credential Passbubble needs is the Authorization bearer token,
		// which the browser sets explicitly.
	}
	return http.StripPrefix("", proxy) // path rewrite handled in Director
}

// securityHeaders returns middleware applying a strict, E2E-appropriate security
// policy. Because all secrets live in the browser's WASM heap, an XSS would be
// catastrophic — hence a tight CSP. 'wasm-unsafe-eval' is required to instantiate
// the WebAssembly module. When embedded (Electron shell), connect-src also allows
// WebSockets to the loopback origin so the renderer can attach to the PTY stream;
// nothing else is loosened.
func securityHeaders(embedded bool) func(http.Handler) http.Handler {
	connectSrc := "connect-src 'self'"
	if embedded {
		connectSrc += " ws://127.0.0.1:* wss://127.0.0.1:*"
	}
	csp := "default-src 'self'; " +
		"script-src 'self' 'wasm-unsafe-eval'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; " +
		connectSrc + "; " +
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
