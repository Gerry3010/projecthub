// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// ProjectHub Electron main process. It spawns and supervises the Go sidecar (phd),
// reads its one-line handshake ({port, token, pid}) from stdout, then opens a window
// on the sidecar's loopback origin — which serves the existing go-app WASM UI. The
// port + token are injected into the renderer via the preload bridge (window.phNative)
// so the WASM UI can call the token-guarded /native API for local-machine actions.

import { app, BrowserWindow, session, WebContents } from "electron";
import { spawn, ChildProcess } from "node:child_process";
import * as readline from "node:readline";
import * as path from "node:path";

interface Handshake {
  port: number;
  token: string;
  pid: number;
}

/** Where the phd binary lives: packaged resources in prod, ../build in dev. */
function sidecarPath(): string {
  const name = process.platform === "win32" ? "phd.exe" : "phd";
  if (app.isPackaged) {
    return path.join(process.resourcesPath, name);
  }
  return path.join(__dirname, "..", "..", "build", name);
}

/** The sidecar serves web/* relative to its working directory, so it must run from
 *  the directory that contains web/app.wasm: the repo root in dev, resources in prod. */
function sidecarCwd(): string {
  return app.isPackaged ? process.resourcesPath : path.join(__dirname, "..", "..");
}

let child: ChildProcess | null = null;
let win: BrowserWindow | null = null;
let restarts = 0;
const MAX_RESTARTS = 5;
let quitting = false;

/** Spawn phd and resolve with its handshake (rejects if it dies before announcing). */
function startSidecar(): Promise<Handshake> {
  return new Promise((resolve, reject) => {
    const bin = sidecarPath();
    const proc = spawn(bin, [], {
      cwd: sidecarCwd(),
      env: { ...process.env },
      stdio: ["ignore", "pipe", "pipe"],
    });
    child = proc;

    let settled = false;
    const rl = readline.createInterface({ input: proc.stdout! });
    rl.once("line", (line: string) => {
      try {
        const hs = JSON.parse(line) as Handshake;
        settled = true;
        resolve(hs);
      } catch {
        // Not the handshake — treat as a log line and keep waiting.
        console.log("[phd]", line);
      }
    });
    // Subsequent stdout lines are logs.
    rl.on("line", (line: string) => {
      if (settled) console.log("[phd]", line);
    });
    proc.stderr!.on("data", (d: Buffer) => console.error("[phd]", d.toString().trimEnd()));

    proc.on("exit", (code) => {
      if (!settled) reject(new Error(`sidecar exited before handshake (code ${code})`));
      onSidecarExit(code);
    });
    proc.on("error", (err) => {
      if (!settled) reject(err);
    });
  });
}

/** Restart the sidecar with backoff on unexpected exit; give up after MAX_RESTARTS. */
function onSidecarExit(code: number | null): void {
  child = null;
  if (quitting) return;
  if (restarts >= MAX_RESTARTS) {
    console.error(`sidecar died ${restarts} times — giving up`);
    win?.webContents.executeJavaScript(
      `console.error('ProjectHub sidecar failed to stay up.')`
    ).catch(() => {});
    return;
  }
  restarts++;
  const backoff = Math.min(1000 * 2 ** (restarts - 1), 8000);
  console.error(`sidecar exited (code ${code}); restart ${restarts}/${MAX_RESTARTS} in ${backoff}ms`);
  setTimeout(() => {
    startSidecar().then((hs) => loadRenderer(hs)).catch((e) => console.error(e));
  }, backoff);
}

/** Point the window at the sidecar origin and (re)inject the native bridge args. */
function loadRenderer(hs: Handshake): void {
  const base = `http://127.0.0.1:${hs.port}`;
  if (!win || win.isDestroyed()) {
    win = new BrowserWindow({
      width: 1280,
      height: 820,
      backgroundColor: "#0f1115",
      webPreferences: {
        preload: path.join(__dirname, "preload.js"),
        contextIsolation: true,
        nodeIntegration: false,
        sandbox: true,
        webviewTag: true, // browser-panel tiles use <webview>
        additionalArguments: [`--ph-port=${hs.port}`, `--ph-token=${hs.token}`],
      },
    });
    win.on("closed", () => (win = null));
    // Surface renderer console + errors to the main-process stdout for debugging.
    win.webContents.on("console-message", (_e, _lvl, message) => console.log("[renderer]", message));
    win.webContents.on("render-process-gone", (_e, d) => console.error("[renderer gone]", d.reason));
    // Browser-tile <webview> guests: inject the guest preload and force a hardened
    // sandbox. Runs for every <webview> the renderer attaches.
    win.webContents.on("will-attach-webview", (_e, webPreferences) => {
      webPreferences.preload = path.join(__dirname, "webview-preload.js");
      webPreferences.nodeIntegration = false;
      webPreferences.contextIsolation = true;
    });
  }
  win.loadURL(base);
}

/** Lock down every browser-tile guest: popups become in-tile tabs (handled in the
 *  guest preload), so deny native window opens, and deny permission prompts (MVP). */
function hardenWebviewGuests(): void {
  app.on("web-contents-created", (_e, contents: WebContents) => {
    if (contents.getType() !== "webview") return;
    // Popups are routed to the host as new tabs by the guest preload; anything that
    // still reaches here (e.g. a script-driven open) is denied rather than spawning
    // a native window.
    contents.setWindowOpenHandler(() => ({ action: "deny" }));
  });
  // Geolocation / media / notifications off for the shared browser partition (MVP).
  session
    .fromPartition("persist:ph-browser")
    .setPermissionRequestHandler((_wc, _perm, cb) => cb(false));
}

app.whenReady().then(async () => {
  hardenWebviewGuests();
  try {
    const hs = await startSidecar();
    loadRenderer(hs);
  } catch (e) {
    console.error("failed to start sidecar:", e);
    app.quit();
  }

  app.on("activate", () => {
    if (BrowserWindow.getAllWindows().length === 0 && child) {
      // Sidecar still up; a fresh window needs its handshake — simplest is a restart.
    }
  });
});

app.on("window-all-closed", () => {
  if (process.platform !== "darwin") app.quit();
});

/** Kill the sidecar cleanly on quit: SIGTERM, then SIGKILL after a grace period. */
app.on("before-quit", () => {
  quitting = true;
  if (child) {
    const proc = child;
    proc.kill("SIGTERM");
    setTimeout(() => {
      if (!proc.killed) proc.kill("SIGKILL");
    }, 2000);
  }
});
