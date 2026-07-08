// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// web/shell.js — the ProjectHub renderer "island" layer. go-app owns the page and
// the tiling split-tree; this JS owns the FOREIGN-DOM tiles (terminal, markdown,
// browser) that go-app's virtual DOM can't safely host. Each island element lives
// in a registry keyed by paneID and is re-parented (appendChild) into its current
// slot on every (re)mount, so a running terminal/webview SURVIVES splits, resizes,
// and drag-moves. Also hosts the divider-resize gesture. Exposed as window.phShell.

import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import MarkdownIt from "markdown-it";
import DOMPurify from "dompurify";
import "@xterm/xterm/css/xterm.css";
import "./styles.css";

interface PhNative {
  base: string;
  token: string;
  wsBearer: string;
}
function phNative(): PhNative | null {
  const n = (window as any).phNative;
  return n && n.base ? (n as PhNative) : null;
}

// PTY frame opcodes (client→server); must match internal/ptyhost.
const OP_DATA = 0x00;
const OP_RESIZE = 0x01;

interface Island {
  el: HTMLElement;
  type: string;
  cleanup: () => void;
  attached: boolean;
  onAttached?: () => void; // one-time init that needs the element to be in the DOM (xterm)
}
const registry = new Map<string, Island>();

/** Ensure the island for paneID exists and lives inside slot #slotId. Creating it on
 *  first call, otherwise just moving the existing (live) element. Idempotent. */
function attachIsland(paneID: string, type: string, paramsJSON: string, slotId: string): void {
  const slot = document.getElementById(slotId);
  if (!slot) return;
  let island = registry.get(paneID);
  if (!island) {
    const params = safeParse(paramsJSON);
    island = createIsland(paneID, type, params);
    registry.set(paneID, island);
  }
  if (island.el.parentElement !== slot) {
    slot.innerHTML = "";
    slot.appendChild(island.el);
  }
  // Run element-in-DOM init exactly once (e.g. open xterm at the right size).
  if (!island.attached) {
    island.attached = true;
    // Defer so layout has settled and the slot has a real size.
    requestAnimationFrame(() => island!.onAttached?.());
  }
  if (island.type === "terminal") queueMicrotask(() => (island!.el as any)._fit?.());
}

function destroyIsland(paneID: string): void {
  const island = registry.get(paneID);
  if (!island) return;
  try {
    island.cleanup();
  } catch {
    /* ignore */
  }
  island.el.remove();
  registry.delete(paneID);
}

function createIsland(paneID: string, type: string, params: Record<string, string>): Island {
  const el = document.createElement("div");
  el.className = "ph-island-inner";
  el.dataset.pane = paneID;
  switch (type) {
    case "terminal":
      return mountTerminal(el, params);
    case "markdown":
      return mountMarkdown(el, params);
    case "browser":
      return mountBrowser(el, params);
    default:
      el.textContent = `Unbekannter Tile-Typ: ${type}`;
      return { el, type, cleanup: () => {} };
  }
}

// ─── terminal ─────────────────────────────────────────────────────────────────

function mountTerminal(el: HTMLElement, params: Record<string, string>): Island {
  const nat = phNative();
  const term = new Terminal({
    fontFamily: 'ui-monospace, "JetBrains Mono", "Cascadia Code", monospace',
    fontSize: 13,
    cursorBlink: true,
    theme: { background: "rgba(0,0,0,0)" },
    allowTransparency: true,
  });
  const fit = new FitAddon();
  term.loadAddon(fit);
  const doFit = () => {
    try {
      fit.fit();
    } catch {
      /* not yet sized */
    }
  };
  (el as any)._fit = doFit;

  let ws: WebSocket | null = null;
  let closed = false;
  const ro = new ResizeObserver(() => doFit());

  // xterm must be opened while the element is in the DOM and has a size, else it
  // renders blank — so all of this runs from onAttached, not at create time.
  const onAttached = () => {
    term.open(el);
    doFit();
    ro.observe(el);
    if (!nat) {
      term.writeln("\x1b[31mProjectHub-Sidecar nicht verfügbar (nur im Desktop-Build).\x1b[0m");
      return;
    }
    (async () => {
      const cols = term.cols || 80;
      const rows = term.rows || 24;
      const cwd = params.cwd || "";
      let ptyId = "";
      try {
        if (params.session_id) {
          ptyId = await postJSON(nat, "/native/claude/resume", { cwd, session_id: params.session_id, cols, rows }).then(
            (r) => r.pty_id,
          );
        } else {
          // A "prompt" param (set by the Claude tile's starter) is forwarded as the
          // sole CLI arg so `claude "<prompt>"` opens straight into that session.
          const args = params.cmd === "claude" && params.prompt ? [params.prompt] : [];
          ptyId = await postJSON(nat, "/native/pty", { cwd, cmd: params.cmd || "", args, cols, rows }).then(
            (r) => r.pty_id,
          );
        }
      } catch (e) {
        term.writeln(`\x1b[31mTerminal-Start fehlgeschlagen: ${e}\x1b[0m`);
        return;
      }

      const wsURL = nat.base.replace(/^http/, "ws") + `/native/pty/${ptyId}/ws`;
      ws = new WebSocket(wsURL, nat.wsBearer);
      ws.binaryType = "arraybuffer";
      ws.onmessage = (ev) => term.write(new Uint8Array(ev.data as ArrayBuffer));
      ws.onclose = () => {
        if (!closed) term.writeln("\r\n\x1b[90m[Sitzung beendet]\x1b[0m");
      };
      ws.onerror = () => term.writeln("\r\n\x1b[31m[Terminal-Verbindung fehlgeschlagen]\x1b[0m");

      term.onData((d) => {
        if (ws?.readyState === WebSocket.OPEN) ws.send(frame(OP_DATA, new TextEncoder().encode(d)));
      });
      const sendResize = () => {
        if (ws?.readyState !== WebSocket.OPEN) return;
        const buf = new Uint8Array(4);
        new DataView(buf.buffer).setUint16(0, term.cols);
        new DataView(buf.buffer).setUint16(2, term.rows);
        ws.send(frame(OP_RESIZE, buf));
      };
      term.onResize(sendResize);
      ws.onopen = () => sendResize();
    })();
  };

  return {
    el,
    type: "terminal",
    attached: false,
    onAttached,
    cleanup: () => {
      closed = true;
      ro.disconnect();
      ws?.close();
      term.dispose();
    },
  };
}

function frame(op: number, payload: Uint8Array): Uint8Array {
  const out = new Uint8Array(payload.length + 1);
  out[0] = op;
  out.set(payload, 1);
  return out;
}

// ─── markdown ─────────────────────────────────────────────────────────────────

function mountMarkdown(el: HTMLElement, params: Record<string, string>): Island {
  el.classList.add("ph-md");
  const md = new MarkdownIt({ html: false, linkify: true, breaks: false });
  const render = (src: string) => {
    el.innerHTML = DOMPurify.sanitize(md.render(src));
  };

  let timer: number | undefined;
  if (params.content !== undefined) {
    render(params.content);
  } else if (params.path) {
    const nat = phNative();
    let lastMtime = "";
    const poll = async () => {
      if (!nat) return;
      try {
        const url = nat.base + "/native/file?path=" + encodeURIComponent(params.path) + "&mtime=" + lastMtime;
        const resp = await fetch(url, { headers: { Authorization: "Bearer " + nat.token } });
        if (resp.status === 304) return;
        if (!resp.ok) {
          el.innerHTML = `<p class="ph-md-err">Kann Datei nicht lesen: ${params.path}</p>`;
          return;
        }
        lastMtime = resp.headers.get("X-Mtime") || lastMtime;
        render(await resp.text());
      } catch {
        /* transient */
      }
    };
    poll();
    timer = window.setInterval(poll, 1200);
  } else {
    el.innerHTML = `<p class="ph-md-err">Kein <code>path</code> oder <code>content</code>.</p>`;
  }

  return { el, type: "markdown", cleanup: () => timer !== undefined && clearInterval(timer) };
}

// ─── browser (Electron webview) ────────────────────────────────────────────────

function mountBrowser(el: HTMLElement, params: Record<string, string>): Island {
  el.classList.add("ph-browser");
  const view = document.createElement("webview") as any;
  view.setAttribute("partition", "persist:ph-browser");
  view.setAttribute("allowpopups", "");
  view.style.width = "100%";
  view.style.height = "100%";
  el.appendChild(view);
  // <webview> only navigates reliably once it is attached to the DOM.
  const onAttached = () => view.setAttribute("src", normalizeURL(params.url) || "about:blank");
  return { el, type: "browser", attached: false, onAttached, cleanup: () => view.remove() };
}

/** navigate points a browser tile's <webview> at a new url (called from the tile's
 *  address bar). Prepends https:// when the user omits a scheme. */
function navigate(paneID: string, url: string): void {
  const island = registry.get(paneID);
  if (!island || island.type !== "browser") return;
  const view = island.el.querySelector("webview") as any;
  if (view) view.setAttribute("src", normalizeURL(url) || "about:blank");
}

function normalizeURL(u: string): string {
  const s = (u || "").trim();
  if (!s || s === "about:blank") return s;
  if (/^[a-z]+:\/\//i.test(s)) return s;
  return "https://" + s;
}

// ─── divider resize ─────────────────────────────────────────────────────────────

function initDividerResize(): void {
  let active: { split: HTMLElement; dir: string; node: string } | null = null;

  document.addEventListener("pointerdown", (e) => {
    const div = (e.target as HTMLElement)?.closest?.(".ph-divider") as HTMLElement | null;
    if (!div) return;
    const split = div.parentElement as HTMLElement;
    if (!split?.classList.contains("ph-split")) return;
    active = { split, dir: split.dataset.dir || "row", node: div.dataset.node || "" };
    div.setPointerCapture(e.pointerId);
    document.body.classList.add("ph-resizing");
    e.preventDefault();
  });

  document.addEventListener("pointermove", (e) => {
    if (!active) return;
    const rect = active.split.getBoundingClientRect();
    let r =
      active.dir === "col"
        ? (e.clientY - rect.top) / rect.height
        : (e.clientX - rect.left) / rect.width;
    r = Math.max(0.05, Math.min(0.95, r));
    active.split.style.setProperty("--r", String(r));
    (active.split as any)._r = r;
  });

  const end = () => {
    if (!active) return;
    const r = (active.split as any)._r;
    if (typeof r === "number") (window as any).phWsRatio?.(active.node, r);
    document.body.classList.remove("ph-resizing");
    active = null;
  };
  document.addEventListener("pointerup", end);
  document.addEventListener("pointercancel", end);
}

// ─── drag drop-zone preview ─────────────────────────────────────────────────────

// Shows a translucent overlay on the half/edge a tile drop would land, matching the
// Go-side dropEdge thresholds (0.25 / 0.75). Purely visual; go-app performs the move.
function initDropHint(): void {
  const hint = document.createElement("div");
  hint.className = "ph-drop-hint";
  hint.style.display = "none";
  document.body.appendChild(hint);
  const hide = () => (hint.style.display = "none");

  document.addEventListener("dragover", (e) => {
    const tile = (e.target as HTMLElement)?.closest?.(".ph-tile") as HTMLElement | null;
    if (!tile) return hide();
    const r = tile.getBoundingClientRect();
    if (!r.width || !r.height) return hide();
    const fx = (e.clientX - r.left) / r.width;
    const fy = (e.clientY - r.top) / r.height;
    let x = r.left,
      y = r.top,
      w = r.width,
      h = r.height;
    if (fx < 0.25) w = r.width / 2;
    else if (fx > 0.75) {
      x = r.left + r.width / 2;
      w = r.width / 2;
    } else if (fy < 0.25) h = r.height / 2;
    else if (fy > 0.75) {
      y = r.top + r.height / 2;
      h = r.height / 2;
    }
    hint.style.display = "block";
    hint.style.left = x + "px";
    hint.style.top = y + "px";
    hint.style.width = w + "px";
    hint.style.height = h + "px";
  });
  document.addEventListener("drop", hide);
  document.addEventListener("dragend", hide);
}

// ─── plumbing ───────────────────────────────────────────────────────────────────

async function postJSON(nat: PhNative, path: string, body: unknown): Promise<any> {
  const resp = await fetch(nat.base + path, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + nat.token },
    body: JSON.stringify(body),
  });
  if (!resp.ok) throw new Error(`${path}: ${resp.status}`);
  return resp.json();
}

function safeParse(s: string): Record<string, string> {
  try {
    return s ? JSON.parse(s) : {};
  } catch {
    return {};
  }
}

// phShell is pure functions — expose immediately (go-app calls it long after load).
(window as any).phShell = { attachIsland, destroyIsland, navigate };

// The DOM-touching init (drop-hint appends to <body>) must wait: this script runs in
// <head>, where document.body is still null.
function boot(): void {
  initDividerResize();
  initDropHint();
}
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot);
} else {
  boot();
}
