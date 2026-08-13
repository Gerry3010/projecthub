// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// ProjectHub Electron main process. It spawns and supervises the Go sidecar (phd),
// reads its one-line handshake ({port, token, pid}) from stdout, then opens a window
// on the sidecar's loopback origin — which serves the existing go-app WASM UI. The
// port + token are injected into the renderer via the preload bridge (window.phNative)
// so the WASM UI can call the token-guarded /native API for local-machine actions.

import {
  app,
  BrowserWindow,
  Menu,
  MenuItemConstructorOptions,
  session,
  WebContents,
  ipcMain,
  safeStorage,
  Notification,
} from "electron";
import { spawn, ChildProcess } from "node:child_process";
import * as readline from "node:readline";
import * as path from "node:path";
import * as fs from "node:fs";
import * as http from "node:http";

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
// One window per project — keyed by project id; "" is the home/launcher window. A
// backgrounded project window keeps its Workspace mounted, so its terminals/Claude/
// webviews stay alive while you work in another window (the caching requirement).
const windows = new Map<string, BrowserWindow>();
let handshake: Handshake | null = null; // latest sidecar handshake (for new windows)
let restarts = 0;
const MAX_RESTARTS = 5;
let quitting = false;
// Local Passbubble backend auto-start. Dev finds it beside the repo; the packaged app reads
// the path from the device-local secure store (set in Settings). We stop on quit only what we
// started, and never touch Docker Desktop itself.
let startedBackend = false;
let stopping = false;
let backendStarting = false; // guards concurrent startBackend() (boot + on-demand)

/** The local Passbubble repo dir (its docker-compose.yml). Precedence: the device-local
 *  setting from the Settings UI (how the packaged app learns the path), then PROJECTHUB_PB_DIR,
 *  then the dev sibling ../Password-Manager (repo root is two levels up from app/dist,
 *  matching sidecarCwd()). */
function pbDir(): string {
  return (
    secureGet("backend.pbdir") ||
    process.env.PROJECTHUB_PB_DIR ||
    path.join(__dirname, "..", "..", "..", "Password-Manager")
  );
}

/** The URL phd proxies /pb to — used to detect an already-running backend. */
function pbURL(): string {
  return process.env.PROJECTHUB_PB_URL || "http://localhost:8080";
}

/** The docker CLI. Apps launched from Launchpad/Finder inherit a minimal PATH without Docker
 *  Desktop's bin, so resolve the common absolute locations before falling back to "docker". */
function dockerBin(): string {
  for (const c of [
    "/opt/homebrew/bin/docker",
    "/usr/local/bin/docker",
    path.join(app.getPath("home"), ".docker", "bin", "docker"),
  ]) {
    if (fs.existsSync(c)) return c;
  }
  return "docker";
}

/** Resolve true if the backend answers at all (any HTTP status), false on error/timeout. */
function backendReachable(): Promise<boolean> {
  return new Promise((resolve) => {
    const req = http.get(pbURL(), (res) => {
      res.resume();
      resolve(true);
    });
    req.setTimeout(1000, () => req.destroy());
    req.on("error", () => resolve(false));
  });
}

/** Resolve true if the docker daemon answers (`docker info` exits 0), with a hard timeout so a
 *  stopped daemon can't wedge startup. */
function dockerReady(): Promise<boolean> {
  return new Promise((resolve) => {
    const proc = spawn(dockerBin(), ["info"], { stdio: "ignore" });
    const t = setTimeout(() => {
      proc.kill("SIGKILL");
      resolve(false);
    }, 5000);
    proc.on("error", () => {
      clearTimeout(t);
      resolve(false);
    });
    proc.on("exit", (code) => {
      clearTimeout(t);
      resolve(code === 0);
    });
  });
}

/** Ensure the docker daemon is up, launching Docker Desktop (best-effort) if it isn't and
 *  waiting up to ~60s for it. macOS only (`open -a Docker`). */
async function ensureDockerRunning(): Promise<boolean> {
  if (await dockerReady()) return true;
  console.log("[backend] docker daemon down — launching Docker Desktop…");
  spawn("open", ["-a", "Docker"], { stdio: "ignore" }).on("error", () => {});
  for (let i = 0; i < 60; i++) {
    await new Promise((r) => setTimeout(r, 1000));
    if (await dockerReady()) {
      console.log("[backend] docker daemon is up");
      return true;
    }
  }
  console.log("[backend] docker daemon did not come up in time");
  return false;
}

/** Run `docker compose <args>` in pbDir(); resolve with the exit code, or null if docker
 *  could not be spawned. Output is echoed under [backend]. */
function dockerCompose(args: string[]): Promise<number | null> {
  return new Promise((resolve) => {
    const proc = spawn(dockerBin(), ["compose", ...args], {
      cwd: pbDir(),
      stdio: ["ignore", "pipe", "pipe"],
    });
    const echo = (d: Buffer) => console.log("[backend]", d.toString().trimEnd());
    proc.stdout!.on("data", echo);
    proc.stderr!.on("data", echo);
    proc.on("error", () => resolve(null));
    proc.on("exit", (code) => resolve(code));
  });
}

/** Bring up the local Passbubble stack (dev sibling or the configured dir) so login works out
 *  of the box, launching Docker Desktop if needed. Silently skips (never fatal) when opted out,
 *  no compose dir/docker is found, or the backend is already up. Sets startedBackend only when
 *  THIS process actually started it, so stopBackend() on quit touches nothing we didn't start.
 *  Safe to call again at runtime (e.g. right after the path is set in Settings). */
async function startBackend(): Promise<void> {
  if (backendStarting) return;
  backendStarting = true;
  try {
    if (process.env.PROJECTHUB_NO_AUTOSTACK) return; // explicit opt-out
    if (secureGet("backend.autostack") === "0") return; // toggled off in Settings
    const dir = pbDir();
    if (!fs.existsSync(path.join(dir, "docker-compose.yml"))) {
      console.log(`[backend] skip: no docker-compose.yml at ${dir}`);
      return;
    }
    if (await backendReachable()) {
      console.log(`[backend] skip: already reachable at ${pbURL()}`);
      return; // someone else started it — don't adopt (and thus don't stop) it
    }
    if (!(await ensureDockerRunning())) {
      console.log("[backend] skip: docker daemon not available");
      return;
    }
    console.log("[backend] starting local Passbubble stack…");
    const code = await dockerCompose(["up", "-d", "postgres", "redis", "mailpit", "backend"]);
    if (code === null) {
      console.log("[backend] skip: docker not available");
      return;
    }
    if (code !== 0) {
      console.error(`[backend] 'docker compose up' exited ${code} — continuing without it`);
      return;
    }
    startedBackend = true;
    // Best-effort: wait up to ~20s for the backend to answer (purely for a clear log line).
    for (let i = 0; i < 20; i++) {
      if (await backendReachable()) {
        console.log(`[backend] up at ${pbURL()}`);
        return;
      }
      await new Promise((r) => setTimeout(r, 1000));
    }
    console.log("[backend] started; not answering yet — login may need a moment");
  } finally {
    backendStarting = false;
  }
}

/** Stop the stack this process started, best-effort. Docker Desktop itself is left running. */
async function stopBackend(): Promise<void> {
  if (!startedBackend) return;
  console.log("[backend] stopping local Passbubble stack…");
  await dockerCompose(["stop"]);
}

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
    for (const w of windows.values()) {
      w.webContents.executeJavaScript(`console.error('ProjectHub sidecar failed to stay up.')`).catch(() => {});
    }
    return;
  }
  restarts++;
  const backoff = Math.min(1000 * 2 ** (restarts - 1), 8000);
  console.error(`sidecar exited (code ${code}); restart ${restarts}/${MAX_RESTARTS} in ${backoff}ms`);
  setTimeout(() => {
    startSidecar().then((hs) => loadRenderer(hs)).catch((e) => console.error(e));
  }, backoff);
}

/** Create a window for a project ("" = home) on the current sidecar origin, injecting
 *  the per-launch bridge args and loading the project via a "#/p/<id>" hash the WASM
 *  Root reads to pin the window to that project. */
function createWindow(projectId: string): BrowserWindow {
  const hs = handshake!;
  const base = `http://127.0.0.1:${hs.port}`;
  // Reuse the geometry of the window this one was spawned from, offset a little, so a
  // handed-over project doesn't land in a default-sized window in a corner.
  const from = BrowserWindow.getFocusedWindow();
  const bounds = from && !from.isDestroyed() ? from.getBounds() : null;
  // Opt-in window transparency (device-local, from the secure store). A transparent
  // window must be created transparent — it can't be toggled at runtime — so the toggle
  // in Settings writes the flag and relaunches. When on, the deck/wallpaper fade with
  // --app-alpha so the desktop shows through behind ProjectHub.
  const transparent = secureGet("window.transparent") === "1";
  const w = new BrowserWindow({
    width: bounds ? bounds.width : 1280,
    height: bounds ? bounds.height : 820,
    x: bounds ? bounds.x + 32 : undefined,
    y: bounds ? bounds.y + 32 : undefined,
    transparent,
    backgroundColor: transparent ? "#00000000" : "#0f1115",
    webPreferences: {
      preload: path.join(__dirname, "preload.js"),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      webviewTag: true, // browser-panel tiles use <webview>
      // The project id rides in as a launch argument (not just the URL hash): the WASM
      // Root reads it from the preload bridge, which survives any client-side routing.
      additionalArguments: [
        `--ph-port=${hs.port}`,
        `--ph-token=${hs.token}`,
        `--ph-project=${projectId}`,
      ],
    },
  });
  windows.set(projectId, w);
  // Drop by identity, not by the id it was created with — a window can be re-keyed
  // when it navigates to another project in place (see rekeyWindow).
  w.on("closed", () => {
    for (const [id, other] of windows) {
      if (other === w) windows.delete(id);
    }
  });
  // Surface renderer console + errors to the main-process stdout for debugging.
  w.webContents.on("console-message", (_e, _lvl, message) => console.log("[renderer]", message));
  w.webContents.on("render-process-gone", (_e, d) => console.error("[renderer gone]", d.reason));
  // Browser-tile <webview> guests: inject the guest preload and force a hardened
  // sandbox. Runs for every <webview> the renderer attaches.
  w.webContents.on("will-attach-webview", (_e, webPreferences) => {
    webPreferences.preload = path.join(__dirname, "webview-preload.js");
    webPreferences.nodeIntegration = false;
    webPreferences.contextIsolation = true;
  });
  w.loadURL(projectId ? `${base}#/p/${encodeURIComponent(projectId)}` : base);
  return w;
}

/** Focus the project's window if it exists, else create it. */
function openWindow(projectId: string): void {
  const existing = windows.get(projectId);
  if (existing && !existing.isDestroyed()) {
    if (existing.isMinimized()) existing.restore();
    existing.focus();
    return;
  }
  createWindow(projectId);
}

/** Open an ADDITIONAL empty (home/launcher) window — ⌘N/Ctrl+N means "new window", not
 *  "focus the existing one", so this deliberately bypasses openWindow(""). Home windows
 *  are interchangeable, so the map just keeps the newest one under "" — that is the one
 *  focus-home targets; older ones stay usable and clean themselves out of the map when
 *  closed. (A sidecar restart rebuilds one window per KEY, so extra empty windows are not
 *  restored — they hold no state, so nothing is lost.) */
function newEmptyWindow(): void {
  if (!handshake) return; // sidecar not up yet — nothing to point a window at
  createWindow("");
}

/** Re-key a window when it navigates to another project in place, so "open in new
 *  window" and focus-the-existing-window keep pointing at the right window. */
function rekeyWindow(w: BrowserWindow, projectId: string): void {
  for (const [id, other] of windows) {
    if (other === w) windows.delete(id);
  }
  windows.set(projectId, w);
}

/** Called on sidecar (re)start. On first start opens the home window; on a restart the
 *  port/token changed (existing windows point at a dead origin with stale bridge args),
 *  so recreate every window fresh, preserving which project each showed. */
function loadRenderer(hs: Handshake): void {
  handshake = hs;
  const keys = windows.size > 0 ? [...windows.keys()] : [""];
  for (const w of windows.values()) {
    if (!w.isDestroyed()) w.destroy();
  }
  windows.clear();
  for (const id of keys) createWindow(id);
}

// ─── secure key/value store (origin-independent "stay signed in") ────────────
// The sidecar binds a random loopback port each launch, so the renderer origin — and
// thus its localStorage — changes on every start. The remembered login creds instead
// live here: a small JSON file in userData whose values are encrypted with the OS
// keychain via safeStorage (falling back to a plain-but-still-persistent encoding when
// no keychain backend is available). This both survives restarts AND keeps the master
// password off disk in plaintext, unlike the previous localStorage approach.

function secureStorePath(): string {
  return path.join(app.getPath("userData"), "secure-store.json");
}
function readSecureStore(): Record<string, string> {
  try {
    return JSON.parse(fs.readFileSync(secureStorePath(), "utf8")) as Record<string, string>;
  } catch {
    return {};
  }
}
function writeSecureStore(store: Record<string, string>): void {
  try {
    fs.writeFileSync(secureStorePath(), JSON.stringify(store), { mode: 0o600 });
  } catch (e) {
    console.error("secure-store write failed:", e);
  }
}
function encodeSecure(val: string): string {
  if (safeStorage.isEncryptionAvailable()) {
    return "enc:" + safeStorage.encryptString(val).toString("base64");
  }
  return "raw:" + Buffer.from(val, "utf8").toString("base64");
}
function decodeSecure(stored: string): string {
  try {
    if (stored.startsWith("enc:")) return safeStorage.decryptString(Buffer.from(stored.slice(4), "base64"));
    if (stored.startsWith("raw:")) return Buffer.from(stored.slice(4), "base64").toString("utf8");
  } catch {
    /* corrupt / keychain changed → treat as absent */
  }
  return "";
}
/** Read a decoded secure-store value (main-process side), "" if absent/corrupt. */
function secureGet(key: string): string {
  const store = readSecureStore();
  return key in store ? decodeSecure(store[key]) : "";
}
/** Write a decoded value into the secure store (encrypted at rest). */
function secureSet(key: string, val: string): void {
  const store = readSecureStore();
  store[key] = encodeSecure(val);
  writeSecureStore(store);
}

function registerSecureStore(): void {
  ipcMain.on("ph-secure", (e, req: { op: string; key: string; val?: string }) => {
    const store = readSecureStore();
    switch (req.op) {
      case "get":
        e.returnValue = req.key in store ? decodeSecure(store[req.key]) : "";
        return;
      case "set":
        store[req.key] = encodeSecure(req.val ?? "");
        writeSecureStore(store);
        e.returnValue = true;
        return;
      case "del":
        delete store[req.key];
        writeSecureStore(store);
        e.returnValue = true;
        return;
      default:
        e.returnValue = null;
    }
  });
}

/** Window controls from the renderer. Currently: opt-in transparency, which needs a
 *  relaunch because a BrowserWindow's `transparent` can only be set at creation. */
function registerWindowControls(): void {
  ipcMain.on("ph-window", (e, req: { op: string; on?: boolean; id?: string; mod?: string }) => {
    switch (req.op) {
      case "get-transparent":
        e.returnValue = secureGet("window.transparent") === "1";
        return;
      case "set-transparent":
        secureSet("window.transparent", req.on ? "1" : "");
        e.returnValue = true;
        // Recreate the process so the window is (re)built with the new transparency.
        app.relaunch();
        app.exit(0);
        return;
      case "get-browser-cache":
        e.returnValue = browserCacheOn;
        return;
      case "set-browser-cache":
        setBrowserCache(!!req.on);
        e.returnValue = true;
        return;
      case "clear-browser-cache":
        clearBrowserCache();
        e.returnValue = true;
        return;
      case "ensure-backend":
        // The Settings UI just changed the backend path/toggle — (re)attempt the local stack
        // now instead of waiting for the next launch. Fire-and-forget (renderer used send()).
        startBackend().catch((err) => console.error("[backend] ensure failed:", err));
        return;
      case "open-project":
        // Open (or focus) a dedicated window for the project. "" reopens/focuses home.
        openWindow(req.id ?? "");
        e.returnValue = true;
        return;
      case "set-project": {
        // A window navigated to another project in place — follow it with the key.
        const w = BrowserWindow.fromWebContents(e.sender);
        if (w) rekeyWindow(w, req.id ?? "");
        e.returnValue = true;
        return;
      }
      case "get-newwindow-mod":
        e.returnValue = newWindowMod();
        return;
      case "set-newwindow-mod":
        secureSet("window.newmod", req.mod ?? "");
        buildMenu(); // rebind the accelerator immediately
        e.returnValue = newWindowMod();
        return;
      case "close": {
        // A dedicated project window's "← Projekte" closes the window — home lives in
        // its own. If this is the last window, bring home up first: "back to projects"
        // must never quit the app.
        const w = BrowserWindow.fromWebContents(e.sender);
        if (w && windows.get("") !== w && windows.size <= 1) openWindow("");
        w?.close();
        e.returnValue = true;
        return;
      }
      default:
        e.returnValue = null;
    }
  });
}

// ─── application menu ────────────────────────────────────────────────────────
// A project opens in the window you are already in. A window of its own is only ever
// created on demand — from the menu item below or its shortcut, whose modifier is
// device-local and configurable (Einstellungen → Fenster).

const NEW_WINDOW_MODS = ["CommandOrControl", "Alt", "Super", "CommandOrControl+Alt"] as const;

/** The configured modifier for the "open in new window" accelerator. */
function newWindowMod(): string {
  const v = secureGet("window.newmod");
  return (NEW_WINDOW_MODS as readonly string[]).includes(v) ? v : "CommandOrControl";
}

/** Ask the focused renderer to hand its open project to a window of its own. The
 *  renderer answers, because only it knows which project is on screen — it calls back
 *  through phWindow.openProject and then lets the project go (see openInNewWindow). */
function requestNewWindow(from?: { id: number }): void {
  sendMenuOp("new-window", from);
}

/** Deliver a menu/accelerator action to the renderer that has to answer it. Prefers the
 *  window the menu was invoked from, falling back to the focused one (the accelerator
 *  path, where the menu doesn't hand us a window on every platform). */
function sendMenuOp(op: string, from?: { id: number }): void {
  const w = (from ? BrowserWindow.fromId(from.id) : null) ?? BrowserWindow.getFocusedWindow();
  if (!w || w.isDestroyed()) return;
  w.webContents.send("ph-menu", { op });
}

function buildMenu(): void {
  const isMac = process.platform === "darwin";
  // Settings open with the platform-standard ⌘,/Ctrl+, — which is also where each
  // platform expects the entry to live: in the app menu on macOS, under "Datei"
  // elsewhere. The accelerator fires either way, the placement is just convention.
  const settingsItem: MenuItemConstructorOptions = {
    label: "Einstellungen…",
    accelerator: "CommandOrControl+,",
    click: (_item, w) => sendMenuOp("settings", w),
  };
  const template: MenuItemConstructorOptions[] = [
    ...(isMac
      ? ([
          {
            label: app.name,
            submenu: [
              { role: "about" },
              { type: "separator" },
              settingsItem,
              { type: "separator" },
              { role: "services" },
              { type: "separator" },
              { role: "hide" },
              { role: "hideOthers" },
              { role: "unhide" },
              { type: "separator" },
              { role: "quit" },
            ],
          },
        ] as MenuItemConstructorOptions[])
      : []),
    {
      label: "Datei",
      submenu: [
        {
          // Platform-standard "new window": a fresh, empty launcher. Fixed accelerator —
          // unlike the project hand-over below, which uses the configurable modifier.
          label: "Neues Fenster",
          accelerator: "CommandOrControl+N",
          click: () => newEmptyWindow(),
        },
        {
          label: "Projekt in neuem Fenster öffnen",
          accelerator: `${newWindowMod()}+Shift+N`,
          click: (_item, w) => requestNewWindow(w),
        },
        ...(isMac ? [] : [{ type: "separator" } as MenuItemConstructorOptions, settingsItem]),
        { type: "separator" },
        { role: "close", label: "Fenster schließen" },
        ...(isMac ? [] : ([{ role: "quit", label: "Beenden" }] as MenuItemConstructorOptions[])),
      ],
    },
    { role: "editMenu", label: "Bearbeiten" },
    { role: "viewMenu", label: "Ansicht" },
    { role: "windowMenu", label: "Fenster" },
  ];
  Menu.setApplicationMenu(Menu.buildFromTemplate(template));
}

/** Show native OS notifications on request from the renderer (todo reminders). */
function registerNotifications(): void {
  ipcMain.on("ph-notify", (_e, req: { title?: string; body?: string }) => {
    if (!Notification.isSupported()) return;
    new Notification({ title: req.title || "ProjectHub", body: req.body || "" }).show();
  });
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
    guests.add(contents);
    contents.once("destroyed", () => guests.delete(contents));
    applyGuestCache(contents); // before the first navigation, so page 1 is uncached too
  });
  // Geolocation / media / notifications off for the shared browser partition (MVP).
  session.fromPartition(BROWSER_PARTITION).setPermissionRequestHandler((_wc, _perm, cb) => cb(false));
}

// ─── browser-tile HTTP cache ──────────────────────────────────────────────────
//
// OFF by default. A browser tile mostly points at the thing you are working on right
// now — a local dev server, a staging deploy, a dashboard — where a stale asset silently
// showing yesterday's build costs far more than a re-fetch. Cookies/localStorage are
// untouched (the partition stays persist:), so logins survive. Settings → Browser.
//
// Electron exposes no per-session cache switch, and rewriting request headers is NOT
// enough: a resource served from Chromium's memory cache never issues a request, so
// webRequest never sees it (measured — scripts/bellcache.mjs). The switch that actually
// works is the one behind DevTools' "Disable cache": CDP Network.setCacheDisabled, per
// guest WebContents. The header rewrite stays as the upstream half of the same intent —
// it tells servers and proxies not to hand back a stale copy either.
const BROWSER_PARTITION = "persist:ph-browser";
let browserCacheOn = false;
const guests = new Set<WebContents>();

/** Apply the current cache setting to one browser-tile guest. Detaches again when
 *  caching is on, so the debugger port is free for DevTools. */
function applyGuestCache(c: WebContents): void {
  if (c.isDestroyed()) return;
  try {
    if (browserCacheOn) {
      if (c.debugger.isAttached()) c.debugger.detach();
      return;
    }
    if (!c.debugger.isAttached()) c.debugger.attach("1.3");
    void c.debugger.sendCommand("Network.enable");
    void c.debugger.sendCommand("Network.setCacheDisabled", { cacheDisabled: true });
  } catch (e) {
    // Someone else holds the debugger (guest DevTools open) — the header rewrite and the
    // cleared disk cache still apply, so log and carry on rather than failing the tile.
    console.error("[browser] cache control unavailable for this guest:", e);
  }
}

function registerBrowserCache(): void {
  browserCacheOn = secureGet("browser.cache") === "1"; // unset → off
  const sess = session.fromPartition(BROWSER_PARTITION);
  sess.webRequest.onBeforeSendHeaders((details, cb) => {
    if (browserCacheOn) return cb({ requestHeaders: details.requestHeaders });
    details.requestHeaders["Cache-Control"] = "no-cache, no-store, max-age=0";
    details.requestHeaders["Pragma"] = "no-cache";
    cb({ requestHeaders: details.requestHeaders });
  });
  if (!browserCacheOn) clearBrowserCache();
}

function setBrowserCache(on: boolean): void {
  secureSet("browser.cache", on ? "1" : "0");
  browserCacheOn = on;
  guests.forEach(applyGuestCache); // takes effect in open tiles, not just new ones
  if (!on) clearBrowserCache(); // anything cached while it was on would outlive the switch
}

function clearBrowserCache(): void {
  session
    .fromPartition(BROWSER_PARTITION)
    .clearCache()
    .catch((e) => console.error("[browser] clearCache failed:", e));
}

app.whenReady().then(async () => {
  registerSecureStore();
  registerWindowControls();
  registerNotifications();
  hardenWebviewGuests();
  registerBrowserCache();
  buildMenu();
  try {
    await startBackend();
  } catch (e) {
    console.error("[backend] auto-start failed (continuing):", e);
  }
  try {
    const hs = await startSidecar();
    loadRenderer(hs);
  } catch (e) {
    console.error("failed to start sidecar:", e);
    app.quit();
  }

  app.on("activate", () => {
    // macOS dock re-activate with no windows: reopen the home window (sidecar's still up).
    if (windows.size === 0 && handshake) openWindow("");
  });
});

app.on("window-all-closed", () => {
  if (process.platform !== "darwin") app.quit();
});

/** Kill the sidecar cleanly on quit: SIGTERM, then SIGKILL after a grace period. */
app.on("before-quit", (e) => {
  quitting = true;
  if (child) {
    const proc = child;
    proc.kill("SIGTERM");
    setTimeout(() => {
      if (!proc.killed) proc.kill("SIGKILL");
    }, 2000);
  }
  // Stop the local backend WE started before the process exits. Defer the quit until
  // `docker compose stop` returns; the `stopping` guard lets the re-quit pass straight
  // through, and the timeout is a dead-man's switch against a hung docker.
  if (startedBackend && !stopping) {
    stopping = true;
    e.preventDefault();
    setTimeout(() => app.exit(0), 8000);
    stopBackend().finally(() => app.quit());
  }
});
