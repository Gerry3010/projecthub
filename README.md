# ProjectHub

Persönlicher Projekt-Manager mit fünf Säulen — Browser-Tabs, Ordner/Dateien,
Passwörter/Keys, Notizen und Cloud-Sync — der **alle Daten end-to-end-verschlüsselt
in [Passbubble](../Password-Manager)** ablegt und nur über dessen REST-API spricht.

> Arbeitsname; AGPLv3 (von Passbubble geerbt).

## Architektur

```
Browser ── go-app (Go→WASM): UI + Krypto. Argon2id + X25519/ML-KEM-768 + AES-GCM,
   │            alles lokal. Master-Key & PrivKeys nur im WASM-Heap.
   │  HTTPS, same-origin /pb/* (nur Ciphertext + JWT)
   ▼
PM-Web-Server (Go, chi) ── serviert go-app (WASM/PWA) + Reverse-Proxy /pb/* → Passbubble.
   │                         Zero-knowledge: sieht nie Master-Key/Klartext.
   ▼
Passbubble-API ── Speicher + Sync + Konten + E2E.

Companion (Bubble-Tea-TUI, Go nativ) ── lokale Aktionen (Tabs lesen, xdg-open),
   die der Browser-Sandbox verwehrt sind. Teilt sich internal/core mit dem Web.
```

**Privacy:** Auch Titel/Namen werden verschlüsselt. Der Server sieht nur strukturelle
Marker (`ph-*`) + Ciphertext. Daten-Layout in Passbubble: Root-Folder `__PROJECT_HUB__`
→ Projekt-Subfolder → Entries je Säule + ein `ph-root`-Katalog.

## Layout

```
cmd/web      go-app Frontend (GOOS=js GOARCH=wasm)
cmd/server   chi-Server: serviert WASM + /pb-Proxy + CSP
cmd/tui      Bubble-Tea-Companion (geplant)
internal/core/{crypto,pbclient,domain,store}   geteilter Kern (Web + TUI)
internal/server   chi-Router, Reverse-Proxy, Security-Header
internal/webui    go-app-Komponenten
web/         index-Assets (app.css, icon.svg), app.wasm (gebaut)
```

## Entwicklung

Voraussetzung: ein lokales Passbubble nebenan (`../Password-Manager`) und dessen
Backend laufend (`cd ../Password-Manager && make up`).

```bash
make wasm        # Frontend → web/app.wasm
make run         # Server auf :8090, proxyt /pb → http://localhost:8080
make test        # Krypto-Interop, Store-Round-Trip, Proxy/CSP-Tests
make vet         # native + wasm vet
```

Dann http://localhost:8090 öffnen und mit dem Passbubble-Konto anmelden.

**Desktop-App (Electron):** `cd app && npm run dev` startet die Shell. Sie zieht den lokalen
Passbubble-Stack (`docker compose up -d …`) beim Start **automatisch** hoch (startet Docker
Desktop bei Bedarf), sofern er nicht schon läuft, und **stoppt** ihn beim Beenden. Im Dev
liegt der Ordner per Default nebenan (`../Password-Manager`); in der **gepackten App** trägt
man ihn einmal im Login-/Einstellungs-Feld „Backend" ein (geräte-lokal, nicht im Repo).
Abschalten mit `PROJECTHUB_NO_AUTOSTACK=1`; override via `PROJECTHUB_PB_DIR` /
`PROJECTHUB_PB_URL`. Packaging: `make pack-mac` (macOS, Node ≥20.19 → `nvm use` in `app/`).

**Installieren (Linux):** `make install` packt das Bundle und legt es unter
`~/.local/lib/projecthub/` ab, dazu einen Launcher (`~/.local/bin/projecthub`) und einen
Eintrag fürs App-Grid — kein sudo, nichts außerhalb von `$PREFIX` (Default `~/.local`).
Der Launcher startet immer das gepackte Bundle, `projecthub --dev` stattdessen den
Checkout. Ein laufendes ProjectHub überlebt die Installation und übernimmt das neue
Bundle beim nächsten Start. Ohne erneutes Packen: `make install-bundle`; wieder weg mit
`make uninstall` (geräte-lokale Einstellungen in `~/.config/projecthub` bleiben).

## MCP (Claude Code steuert den Workspace)

Der Sidecar stellt einen MCP-Server bereit, mit dem Claude Code — laufend im
eingebetteten Terminal eines Projekts — den Workspace treibt: Tiles auflisten/anlegen/
schließen/fokussieren, Todos lesen/anlegen, Projekte/Sessions lesen, Dateien lesen/
schreiben. Vault- und Tile-Tools laufen im WASM-Renderer (nur dort liegen die
Passbubble-Schlüssel und der Tiling-Baum); der Sidecar reicht sie über einen
Long-Poll-Steuerkanal dorthin weiter. Die Brücke ist `cmd/phmcp` (stdio-MCP-Server),
die den Sidecar per Discovery-Datei findet — kein Port/Token nötig.

In der Desktop-App ist keine Einrichtung nötig: startet ein Tile ein Claude (Terminal,
Resume oder Sidebar-Chat), hängt der Sidecar automatisch `--mcp-config` mit dem
`projecthub`-Server an (`phmcp` wird neben der Sidecar-Exe aufgelöst, kein `PATH` nötig;
`internal/local.DecorateClaude`). Für Claude außerhalb der App (eigenes Terminal, TUI):

```bash
make phmcp                          # baut build/phmcp (auf PATH legen, z.B. via `go install ./cmd/phmcp`)
claude mcp add projecthub -- phmcp  # oder projektweit eine .mcp.json:
# { "mcpServers": { "projecthub": { "command": "phmcp" } } }
```

Backlog (Folge-PR): `browser.*`-Tools (über die Extension-Command-Queue),
`layout.sort`, `project_create`.

## Status

Phase 0 + Foundation: geteilter Kern (Krypto WASM-kompatibel + wire-kompatibel,
REST-Client, Domain, Store), chi-Server mit Proxy/CSP und go-app-Skelett
(Login/Unlock → Projektliste, Projekte anlegen/löschen). Offen: Live-Test gegen
laufendes Passbubble, Notizen/Tabs/Dateien-UI, TUI-Companion, Browser-Extension.
Siehe Plan in `~/.claude/plans/`.
