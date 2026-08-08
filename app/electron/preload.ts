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

// phWindow controls the native window. Transparency is opt-in and device-local: it
// can only be applied when the window is created, so setTransparent persists the flag
// (secure store) and relaunches the app. getTransparent reads the current flag so the
// Settings toggle can reflect it. Synchronous (sendSync) like phSecure.

// The menu bar's "open in new window" is answered by the renderer (only it knows which
// project is on screen), so it registers a callback here that the main process triggers.
let newWindowCB: (() => void) | null = null;
ipcRenderer.on("ph-menu", (_e, req: { op?: string }) => {
  if (req?.op === "new-window") newWindowCB?.();
});

contextBridge.exposeInMainWorld("phWindow", {
  getTransparent: (): boolean => ipcRenderer.sendSync("ph-window", { op: "get-transparent" }) === true,
  setTransparent: (on: boolean): void => {
    ipcRenderer.sendSync("ph-window", { op: "set-transparent", on });
  },
  // Multi-window: the project this window was opened for ("" = the home window).
  projectId: argValue("--ph-project="),
  // Open (or focus) a dedicated window per project; "" targets home. Only called from
  // the explicit "in neuem Fenster öffnen" action — clicking a project stays in place.
  openProject: (id: string): void => {
    ipcRenderer.send("ph-window", { op: "open-project", id });
  },
  // Tell the shell this window now shows another project (in-place navigation), so the
  // per-project window bookkeeping follows along.
  setProject: (id: string): void => {
    ipcRenderer.sendSync("ph-window", { op: "set-project", id });
  },
  // Close the calling window (a dedicated project window's "back to projects").
  close: (): void => {
    ipcRenderer.send("ph-window", { op: "close" });
  },
  // Register the renderer's handler for the menu action / keyboard shortcut.
  onNewWindow: (cb: () => void): void => {
    newWindowCB = cb;
  },
  // Device-local modifier of the "open in new window" shortcut (…+Shift+N).
  getNewWindowMod: (): string => ipcRenderer.sendSync("ph-window", { op: "get-newwindow-mod" }) || "",
  setNewWindowMod: (mod: string): string =>
    ipcRenderer.sendSync("ph-window", { op: "set-newwindow-mod", mod }) || "",
});

// phNotify shows a native OS desktop notification (Electron Notification API in the
// main process). Used for todo deadline/reminder alerts, so they fire even when the
// window is backgrounded. Fire-and-forget — no return value needed.
contextBridge.exposeInMainWorld("phNotify", (title: string, body: string): void => {
  ipcRenderer.send("ph-notify", { title, body });
});
