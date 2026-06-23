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

## Status

Phase 0 + Foundation: geteilter Kern (Krypto WASM-kompatibel + wire-kompatibel,
REST-Client, Domain, Store), chi-Server mit Proxy/CSP und go-app-Skelett
(Login/Unlock → Projektliste, Projekte anlegen/löschen). Offen: Live-Test gegen
laufendes Passbubble, Notizen/Tabs/Dateien-UI, TUI-Companion, Browser-Extension.
Siehe Plan in `~/.claude/plans/`.
