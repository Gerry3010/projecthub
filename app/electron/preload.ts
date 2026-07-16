// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// Preload bridge. Runs in an isolated world before the renderer and exposes the
// sidecar's loopback origin + per-launch bearer token to the WASM UI as
// window.phNative. The token authenticates calls to the /native API and — via a
// WebSocket subprotocol — the PTY stream. Nothing else from Node is exposed.

import { contextBridge, ipcRenderer } from "electron";

function argValue(prefix: string): string {
  const hit = process.argv.find((a) => a.startsWith(prefix));
  return hit ? hit.slice(prefix.length) : "";
}

const port = argValue("--ph-port=");
const token = argValue("--ph-token=");

contextBridge.exposeInMainWorld("phNative", {
  port: Number(port),
  token,
  base: `http://127.0.0.1:${port}`,
  // The WebSocket handshake can't set an Authorization header, so the token rides
  // in the Sec-WebSocket-Protocol header; the sidecar's auth accepts this form.
  wsBearer: `ph-bearer.${token}`,
});

// phSecure is an origin-independent, OS-keychain-encrypted key/value store (Electron
// safeStorage, kept in userData by the main process). The "stay signed in" creds live
// here instead of localStorage, because the sidecar binds a RANDOM port each launch —
// so the loopback origin changes every start and localStorage would never persist.
// Synchronous (sendSync) so the WASM login flow can read it without async plumbing.
contextBridge.exposeInMainWorld("phSecure", {
  get: (key: string): string => ipcRenderer.sendSync("ph-secure", { op: "get", key }) || "",
  set: (key: string, val: string): void => {
    ipcRenderer.sendSync("ph-secure", { op: "set", key, val });
  },
  del: (key: string): void => {
    ipcRenderer.sendSync("ph-secure", { op: "del", key });
  },
});
