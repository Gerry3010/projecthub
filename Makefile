# Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

PASSBUBBLE_URL ?= http://localhost:8080
PORT           ?= 8090
# where `make install` puts the desktop app (per-user by default, no sudo)
PREFIX         ?= $(HOME)/.local

.PHONY: all wasm server sidecar tabhost phmcp pack pack-mac install install-bundle uninstall icons run tui test vet build clean

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

## phmcp: build the MCP stdio bridge Claude Code launches → build/phmcp
phmcp:
	go build -o build/phmcp ./cmd/phmcp

## shell: bundle the renderer island layer (xterm/markdown/webview) → web/shell.js
shell:
	cd app && npm run build:shell

## tui: build the TUI companion → build/tui (placeholder until implemented)
tui:
	go build -o build/tui ./cmd/tui

## build: wasm frontend + server + sidecar + native-messaging host + MCP bridge
build: wasm server sidecar tabhost phmcp

## pack: bundle the desktop app → app/release (AppImage + .deb). Builds the Go
## pieces first because electron-builder ships them as extraResources next to the
## app (see app/electron-builder.yml).
pack: wasm sidecar tabhost phmcp
	cd app && npm run pack

## pack-mac: bundle the macOS desktop app → app/release (.dmg + .zip, arm64). Same
## as pack but runs electron-builder --mac; the Go pieces are built for the host
## (darwin/arm64) and shipped as extraResources. Icon comes from the committed
## build-resources/icon.icns — run `make icons` after changing web/icon.svg.
## Needs Node >= 20.19 (electron-builder 26 require()s ESM @noble/hashes; Node 18
## fails with ERR_REQUIRE_ESM). With nvm: `nvm use` (see app/.nvmrc).
pack-mac: wasm sidecar tabhost phmcp
	cd app && npm run pack:mac

## install: pack the desktop bundle and install it for the current user — the
## AppImage under $(PREFIX)/lib/projecthub, a launcher in $(PREFIX)/bin and a
## .desktop entry so it shows up in the app grid. No sudo, nothing outside $(PREFIX).
install: pack install-bundle

## install-bundle: the install steps without rebuilding — use after `make pack`.
install-bundle:
	@set -eu; \
	img=$$(ls -t app/release/ProjectHub-*.AppImage 2>/dev/null | head -1); \
	if [ -z "$$img" ]; then echo "make install: no bundle in app/release — run 'make pack' first" >&2; exit 1; fi; \
	dest="$(PREFIX)/lib/projecthub/ProjectHub.AppImage"; \
	bin="$(PREFIX)/bin/projecthub"; \
	mkdir -p "$(PREFIX)/lib/projecthub" "$(PREFIX)/bin" "$(PREFIX)/share/applications" \
	         "$(PREFIX)/share/icons/hicolor/scalable/apps" "$(PREFIX)/share/icons/hicolor/512x512/apps"; \
	install -m755 "$$img" "$$dest.new" && mv -f "$$dest.new" "$$dest"; \
	sed -e 's|@APPIMAGE@|'"$$dest"'|g' -e 's|@REPO@|$(CURDIR)|g' packaging/linux/projecthub.in > "$$bin.new"; \
	chmod 755 "$$bin.new" && mv -f "$$bin.new" "$$bin"; \
	sed -e 's|@BIN@|'"$$bin"'|g' packaging/linux/projecthub.desktop.in > "$(PREFIX)/share/applications/projecthub.desktop.new"; \
	mv -f "$(PREFIX)/share/applications/projecthub.desktop.new" "$(PREFIX)/share/applications/projecthub.desktop"; \
	cp -f web/icon.svg "$(PREFIX)/share/icons/hicolor/scalable/apps/projecthub.svg"; \
	cp -f app/build-resources/icon.png "$(PREFIX)/share/icons/hicolor/512x512/apps/projecthub.png"; \
	command -v update-desktop-database >/dev/null && update-desktop-database "$(PREFIX)/share/applications" || true; \
	command -v gtk-update-icon-cache >/dev/null && gtk-update-icon-cache -qtf "$(PREFIX)/share/icons/hicolor" || true; \
	echo "installed $$(basename $$img) → $$dest"; \
	echo "launcher  $$bin"; \
	echo "app grid  $(PREFIX)/share/applications/projecthub.desktop"; \
	echo "note: a running ProjectHub keeps the old bundle until you restart it."

## uninstall: remove what install put under $(PREFIX). Leaves your data alone —
## the vault lives in Passbubble, the device-local config in ~/.config/projecthub.
uninstall:
	rm -rf "$(PREFIX)/lib/projecthub"
	rm -f "$(PREFIX)/bin/projecthub" \
	      "$(PREFIX)/share/applications/projecthub.desktop" \
	      "$(PREFIX)/share/icons/hicolor/scalable/apps/projecthub.svg" \
	      "$(PREFIX)/share/icons/hicolor/512x512/apps/projecthub.png"
	@echo "removed. ~/.config/projecthub (device-local settings) was left in place."

## icons: regenerate the macOS icon.icns (+ refresh icon.png) from web/icon.svg.
## Only needed after the logo changes; the generated icns is committed.
icons:
	bash app/scripts/make-icons.sh

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
