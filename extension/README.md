# ProjectHub Browser-Extensions

Melden die **aktuell offenen Tabs** live an die ProjectHub-Desktop-App. Die App zeigt
sie in einem „Tabs"-Tile im Workspace — gruppiert nach Browser, mit Favicon je Tab und
einem Browser-Icon daneben.

## Architektur

```
Extension (MV3, background SW)  →  connectNative  →  tabhost (cmd/tabhost)
                                                        │  POST /native/tabs/ingest
                                                        ▼
                                                 ProjectHub-Sidecar (phd)
                                                        ▲  GET /native/tabs
                                                   WASM-UI „Tabs"-Tile
```

Kein offener Netzwerk-Port: die Extension spricht den Native-Messaging-Host `tabhost`
über stdio an; der findet den laufenden Sidecar über dessen Discovery-Datei
(`<config>/projecthub/endpoint.json`) und leitet die Tabs an die Loopback-API weiter.

## Chromium-Familie (`chromium/`)

Eine WebExtension für Chrome, Chromium, Brave, Edge, Vivaldi.

- **Feste Extension-ID:** `pcknaffknemkpjmbngjfcklnjknlngmo`
  (aus dem `key` in `manifest.json` abgeleitet — bleibt über alle Installationen gleich,
  damit sie im Host-Manifest `allowed_origins` gepinnt werden kann).
- **Signing-Key:** `../signing-key.b64` (gitignored) — nur nötig, um später ein `.crx`
  für die Verteilung zu packen. Zum Entwickeln als „entpackt laden" **nicht** nötig.

### Dev-Install

1. App bauen inkl. Host: `make build` (baut u.a. `build/tabhost`).
2. Host-Manifest installieren: `build/phd --install-native-host` (schreibt
   `net.geraldhofbauer.projecthub.tabs.json` in die NativeMessagingHosts-Verzeichnisse
   aller gefundenen Chromium-Browser, mit `path` → `build/tabhost` und der ID oben).
3. In Chrome/Chromium: `chrome://extensions` → Entwicklermodus → „Entpackte Erweiterung
   laden" → `extension/chromium/`.
4. ProjectHub-App starten, im Workspace ein **Tabs**-Tile hinzufügen.

## Firefox

Später (eigenes Manifest-Format + `~/.mozilla/native-messaging-hosts/`).
