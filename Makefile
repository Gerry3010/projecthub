# Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

PASSBUBBLE_URL ?= http://localhost:8080
PORT           ?= 8090

.PHONY: all wasm server sidecar tabhost run tui test vet build clean

all: build

## wasm: build the go-app frontend → web/app.wasm
wasm:
	GOOS=js GOARCH=wasm go build -o web/app.wasm ./cmd/web

## server: build the web server binary → build/server
server:
	go build -o build/server ./cmd/server

## sidecar: build the Electron desktop sidecar daemon → build/phd
sidecar:
	go build -o build/phd ./cmd/phd

## tabhost: build the browser-extension native-messaging host → build/tabhost
tabhost:
	go build -o build/tabhost ./cmd/tabhost

## shell: bundle the renderer island layer (xterm/markdown/webview) → web/shell.js
shell:
	cd app && npm run build:shell

## tui: build the TUI companion → build/tui (placeholder until implemented)
tui:
	go build -o build/tui ./cmd/tui

## build: wasm frontend + server + sidecar + native-messaging host
build: wasm server sidecar tabhost

## run: build the wasm frontend, then run the server (serves on $(PORT))
run: wasm
	PORT=$(PORT) PASSBUBBLE_URL=$(PASSBUBBLE_URL) go run ./cmd/server

## test: native unit tests (crypto interop, store round-trip, server proxy/CSP)
test:
	go test ./...

## vet: static checks for native + wasm targets. The wasm pass only covers the
## packages that compile into the frontend; the TUI/tabsession/local/server are
## native-only (bubbletea, os/exec, net/http server) and never built for wasm.
vet:
	go vet ./...
	GOOS=js GOARCH=wasm go vet ./cmd/web ./internal/webui ./internal/core/...

clean:
	rm -f web/app.wasm
	rm -rf build
