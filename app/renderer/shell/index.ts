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

// Hidden off-tree holding pen for live islands during a structural re-render. Keeping
// islands here (still attached to document, just display:none) means go-app never sees
// them as children of a dismounting subtree, so <webview>/terminal guests survive.
function islandPen(): HTMLElement {
  let pen = document.getElementById("ph-island-pen");
  if (!pen) {
    pen = document.createElement("div");
    pen.id = "ph-island-pen";
    pen.style.display = "none";
    document.body.appendChild(pen);
  }
  return pen;
}

/** Move every live island into the holding pen ahead of a layout mutation. Each
 *  island is re-homed into its new slot by attachIsland when the slot (re)mounts.
 *  appendChild is an atomic move (not remove+add), so guests are never destroyed. */
function parkIslands(): void {
  const pen = islandPen();
  registry.forEach((island) => {
    if (island.el.parentElement !== pen) pen.appendChild(island.el);
  });
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
      return mountBrowser(el, params, paneID);
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

// ─── browser (Electron webview) — in-tile tab manager ─────────────────────────

// One in-tile browser tab. Its <webview> is created lazily the first time the tab
// is activated (inactive tabs cost nothing) and hidden via display:none otherwise.
interface BTab {
  url: string;
  title: string;
  favicon: string;
  view: any | null;
  loading: boolean;
  domReady: boolean;
  canBack: boolean;
  canForward: boolean;
}

// User-selectable search engines. Each drives the new-tab "home" page and the
// address-bar omnibox; the pick persists in localStorage (per machine) and every
// open browser tile's picker stays in sync via the 'ph-engine-changed' event.
interface SearchEngine {
  label: string;
  home: string;
  query: string;
}
const ENGINES: Record<string, SearchEngine> = {
  brave: { label: "Brave", home: "https://search.brave.com/", query: "https://search.brave.com/search?q=" },
  ddg: { label: "DuckDuckGo", home: "https://duckduckgo.com/", query: "https://duckduckgo.com/?q=" },
  google: { label: "Google", home: "https://www.google.com/", query: "https://www.google.com/search?q=" },
  startpage: { label: "Startpage", home: "https://www.startpage.com/", query: "https://www.startpage.com/sp/search?query=" },
};
const DEFAULT_ENGINE = "brave";
const ENGINE_KEY = "ph-search-engine";
const ENGINE_EVENT = "ph-engine-changed";

// Account-level engine pushed from go-app (Passbubble-backed, syncs across devices).
// Authoritative once loaded; localStorage is a per-machine cache/offline fallback.
let accountEngine: string | null = null;

function currentEngineKey(): string {
  let k = accountEngine || DEFAULT_ENGINE;
  if (!accountEngine) {
    try {
      k = localStorage.getItem(ENGINE_KEY) || DEFAULT_ENGINE;
    } catch {
      /* localStorage unavailable */
    }
  }
  return ENGINES[k] ? k : DEFAULT_ENGINE;
}
function engine(): SearchEngine {
  return ENGINES[currentEngineKey()];
}
// setEngine is the user picking an engine: cache locally, sync open pickers, and
// persist account-wide via go-app (→ Passbubble).
function setEngine(key: string): void {
  if (!ENGINES[key]) return;
  accountEngine = key;
  try {
    localStorage.setItem(ENGINE_KEY, key);
  } catch {
    /* ignore */
  }
  window.dispatchEvent(new CustomEvent(ENGINE_EVENT, { detail: key }));
  (window as any).phSetSearchEngine?.(key);
}
// applySearchEngine is called BY go-app after loading the account setting: adopt it
// and sync every open picker, without persisting back (avoids a feedback loop).
function applySearchEngine(key: string): void {
  if (!ENGINES[key]) return;
  accountEngine = key;
  try {
    localStorage.setItem(ENGINE_KEY, key);
  } catch {
    /* ignore */
  }
  window.dispatchEvent(new CustomEvent(ENGINE_EVENT, { detail: key }));
}

// isLikelyURL decides whether an omnibox entry is a navigable address (vs. a search
// query): a scheme, localhost/IP[:port], or a whitespace-free host with a dotted TLD.
function isLikelyURL(s: string): boolean {
  if (/\s/.test(s)) return false; // has whitespace → search
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(s)) return true; // scheme://…
  if (/^(localhost|[\d.]+)(:\d+)?(\/|$)/i.test(s)) return true; // localhost / IP
  return /^[^/\s]+\.[^/\s]{2,}([/:?#]|$)/.test(s); // host.tld[/…]
}

// omniboxURL turns whatever the user typed into a URL: pass through explicit schemes,
// treat address-looking input as a URL (https:// prepended), else run it as a search
// on the currently selected engine.
function omniboxURL(input: string): string {
  const s = (input || "").trim();
  if (!s) return engine().home;
  if (/^(about|data|file|chrome|view-source|blob):/i.test(s)) return s;
  if (isLikelyURL(s)) return normalizeURL(s);
  return engine().query + encodeURIComponent(s);
}

// mountBrowser builds the whole browser chrome (tab strip + toolbar + view stack +
// statusbar + find/error overlays) around a set of lazy <webview> tabs. All reactive
// state (loading, back/forward, title, favicon, hover-URL, zoom) flows JS-only via
// webview DOM events; only tab-list changes are pushed back to go-app (debounced,
// window.phBrowserState) for layout restore + the tile label.
function mountBrowser(el: HTMLElement, params: Record<string, string>, paneID: string): Island {
  el.classList.add("ph-browser");

  const div = (cls: string) => {
    const d = document.createElement("div");
    d.className = cls;
    return d;
  };
  const button = (label: string, title: string) => {
    const b = document.createElement("button");
    b.className = "ph-browser-btn";
    b.type = "button";
    b.textContent = label;
    b.title = title;
    return b;
  };

  // ── scaffold ──
  const tabsBar = div("ph-browser-tabs");
  const toolbar = div("ph-browser-toolbar");
  const findBar = div("ph-browser-find");
  const viewWrap = div("ph-browser-view");
  const status = div("ph-browser-status");
  const errorOverlay = div("ph-browser-error");
  errorOverlay.style.display = "none";
  findBar.style.display = "none";
  viewWrap.appendChild(errorOverlay); // overlays the webview (viewWrap is positioned)
  el.append(tabsBar, toolbar, findBar, viewWrap, status);

  // ── toolbar controls ──
  const btnBack = button("◀", "Zurück");
  const btnFwd = button("▶", "Vor");
  const btnReload = button("⟳", "Neu laden");
  const address = document.createElement("input");
  address.className = "ph-browser-url";
  address.type = "text";
  address.placeholder = "https://…";
  const btnGo = button("→", "Laden");
  const btnOpenIn = button("↗", "Im System-Browser öffnen");
  const btnCopy = button("⧉", "URL kopieren");
  const btnDev = button("⚙", "DevTools");
  // New-tab lives in the toolbar so it stays reachable even when the tab strip is
  // hidden (single-tab "light" mode); the strip has its own "+" for the multi-tab case.
  const btnNew = button("＋", "Neuer Tab");
  // Search-engine picker: sets the omnibox + new-tab home; persists across tiles.
  const engineSel = document.createElement("select");
  engineSel.className = "ph-browser-engine";
  engineSel.title = "Suchmaschine";
  for (const [key, e] of Object.entries(ENGINES)) {
    const opt = document.createElement("option");
    opt.value = key;
    opt.textContent = e.label;
    engineSel.appendChild(opt);
  }
  engineSel.value = currentEngineKey();
  engineSel.onchange = () => setEngine(engineSel.value);
  const onEngineChanged = (e: Event) => {
    engineSel.value = (e as CustomEvent).detail || currentEngineKey();
  };
  window.addEventListener(ENGINE_EVENT, onEngineChanged);
  toolbar.append(btnBack, btnFwd, btnReload, address, btnGo, btnNew, btnOpenIn, btnCopy, btnDev, engineSel);

  // ── find bar ──
  const findInput = document.createElement("input");
  findInput.className = "ph-browser-find-input";
  findInput.placeholder = "Auf Seite suchen…";
  const findCount = document.createElement("span");
  findCount.className = "ph-browser-find-count";
  const findClose = button("✕", "Suche schließen");
  findBar.append(findInput, findCount, findClose);

  const tabs: BTab[] = [];
  let active = 0;
  let devOpen = false;
  let saveTimer: number | undefined;

  const activeTab = (): BTab | undefined => tabs[active];

  // Debounced push of the tab list back to go-app for persistence + tile label.
  const pushState = () => {
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = window.setTimeout(() => {
      const payload = tabs.map((t) => ({ url: t.url, title: t.title }));
      (window as any).phBrowserState?.(paneID, JSON.stringify(payload), String(active));
    }, 400);
  };

  // ── toolbar / chrome refresh (active tab only) ──
  const updateChrome = () => {
    const t = activeTab();
    if (!t) return;
    if (document.activeElement !== address) address.value = t.url === "about:blank" ? "" : t.url;
    btnBack.disabled = !t.canBack;
    btnFwd.disabled = !t.canForward;
    btnReload.textContent = t.loading ? "✕" : "⟳";
    btnReload.title = t.loading ? "Stopp" : "Neu laden";
    if (!status.dataset.hover) status.textContent = t.loading ? "Lädt…" : "";
  };

  const refreshNav = (t: BTab) => {
    // canGoBack/canGoForward throw synchronously until the guest's dom-ready fires,
    // so gate on domReady and wrap the call itself (not just the promise). They are
    // sync in Electron 33 but Promise-returning in 34+; Promise.resolve absorbs both.
    if (!t.view || !t.domReady) return;
    try {
      Promise.resolve(t.view.canGoBack?.())
        .then((v: boolean) => {
          t.canBack = !!v;
          if (activeTab() === t) btnBack.disabled = !v;
        })
        .catch(() => {});
      Promise.resolve(t.view.canGoForward?.())
        .then((v: boolean) => {
          t.canForward = !!v;
          if (activeTab() === t) btnFwd.disabled = !v;
        })
        .catch(() => {});
    } catch {
      /* not ready yet */
    }
  };

  // ── tab strip render ──
  const renderTabs = () => {
    tabsBar.classList.toggle("single", tabs.length <= 1);
    tabsBar.innerHTML = "";
    tabs.forEach((t, i) => {
      const tab = div("ph-browser-tab");
      if (i === active) tab.classList.add("active");
      if (t.favicon) {
        const img = document.createElement("img");
        img.className = "ph-browser-fav";
        img.src = t.favicon;
        img.onerror = () => img.remove();
        tab.appendChild(img);
      }
      const label = document.createElement("span");
      label.className = "ph-browser-tab-title";
      label.textContent = t.title || t.url || "Neuer Tab";
      tab.appendChild(label);
      const close = button("✕", "Tab schließen");
      close.className = "ph-browser-tab-close";
      close.onclick = (e) => {
        e.stopPropagation();
        closeTab(i);
      };
      tab.appendChild(close);
      tab.onclick = () => activateTab(i);
      tabsBar.appendChild(tab);
    });
    const plus = button("+", "Neuer Tab");
    plus.className = "ph-browser-tab-new";
    plus.onclick = () => {
      const i = addTab(engine().home);
      activateTab(i);
      address.focus();
    };
    tabsBar.appendChild(plus);
  };

  // ── webview lifecycle ──
  const wireView = (t: BTab, view: any) => {
    const ifActive = (fn: () => void) => {
      if (activeTab() === t) fn();
    };
    view.addEventListener("did-start-loading", () => {
      t.loading = true;
      ifActive(updateChrome);
    });
    view.addEventListener("did-stop-loading", () => {
      t.loading = false;
      refreshNav(t);
      ifActive(updateChrome);
    });
    const onNav = () => {
      try {
        t.url = view.getURL();
      } catch {
        /* ignore */
      }
      errorOverlay.style.display = "none";
      refreshNav(t);
      renderTabs();
      ifActive(updateChrome);
      pushState();
    };
    view.addEventListener("did-navigate", onNav);
    view.addEventListener("did-navigate-in-page", onNav);
    view.addEventListener("page-title-updated", (e: any) => {
      t.title = e.title || t.url;
      renderTabs();
      pushState();
    });
    view.addEventListener("page-favicon-updated", (e: any) => {
      t.favicon = (e.favicons && e.favicons[0]) || "";
      renderTabs();
    });
    view.addEventListener("update-target-url", (e: any) => {
      ifActive(() => {
        if (e.url) {
          status.dataset.hover = "1";
          status.textContent = e.url;
        } else {
          delete status.dataset.hover;
          status.textContent = t.loading ? "Lädt…" : "";
        }
      });
    });
    view.addEventListener("did-fail-load", (e: any) => {
      if (e.errorCode === -3 || e.isMainFrame === false) return; // -3 = user-aborted
      errorOverlay.textContent = `Kann Seite nicht laden: ${e.validatedURL || t.url} — ${e.errorDescription || e.errorCode}`;
      errorOverlay.style.display = "";
    });
    view.addEventListener("dom-ready", () => {
      t.domReady = true;
      refreshNav(t);
    });
    view.addEventListener("found-in-page", (e: any) => {
      const r = e.result;
      findCount.textContent = r && r.matches ? `${r.activeMatchOrdinal}/${r.matches}` : "0";
    });
    view.addEventListener("ipc-message", (e: any) => {
      if (e.channel === "nav") {
        if (e.args[0] === "back") goBack();
        else if (e.args[0] === "forward") goForward();
      } else if (e.channel === "key") {
        handleKey(e.args[0]);
      } else if (e.channel === "open-tab") {
        const i = addTab(e.args[0]);
        activateTab(i);
      } else if (e.channel === "zoom") {
        if (e.args[0] === "in") adjustZoom(0.5);
        else if (e.args[0] === "out") adjustZoom(-0.5);
        else if (e.args[0] === "reset") resetZoom();
      }
    });
  };

  const ensureView = (t: BTab): any => {
    if (t.view) return t.view;
    const view = document.createElement("webview") as any;
    view.setAttribute("partition", "persist:ph-browser");
    view.setAttribute("allowpopups", "");
    view.style.display = "none";
    viewWrap.appendChild(view);
    t.view = view;
    wireView(t, view);
    view.setAttribute("src", normalizeURL(t.url) || "about:blank");
    return view;
  };

  const addTab = (url: string): number => {
    tabs.push({
      url: url || "about:blank",
      title: "",
      favicon: "",
      view: null,
      loading: false,
      domReady: false,
      canBack: false,
      canForward: false,
    });
    pushState();
    return tabs.length - 1;
  };

  const activateTab = (i: number) => {
    if (i < 0 || i >= tabs.length) return;
    active = i;
    tabs.forEach((t, j) => {
      if (t.view) t.view.style.display = j === i ? "" : "none";
    });
    const t = tabs[i];
    ensureView(t).style.display = "";
    errorOverlay.style.display = "none";
    renderTabs();
    updateChrome();
    refreshNav(t);
    pushState();
  };

  const closeTab = (i: number) => {
    const t = tabs[i];
    if (!t) return;
    if (tabs.length === 1) {
      // Never leave an empty tile: reset the sole tab to the home page instead of removing it.
      navigateTo(engine().home);
      t.title = "";
      renderTabs();
      pushState();
      return;
    }
    if (t.view) t.view.remove();
    tabs.splice(i, 1);
    if (i < active || active >= tabs.length) active = Math.max(0, active - 1);
    activateTab(active);
  };

  // ── navigation actions ──
  const navigateTo = (raw: string) => {
    const t = activeTab();
    if (!t) return;
    const url = omniboxURL(raw);
    const view = ensureView(t);
    errorOverlay.style.display = "none";
    t.url = url;
    if (t.domReady) {
      try {
        view.loadURL(url);
      } catch {
        view.setAttribute("src", url);
      }
    } else {
      view.setAttribute("src", url);
    }
  };

  const goBack = () => {
    const t = activeTab();
    if (t?.view && t.canBack) {
      try {
        t.view.goBack();
      } catch {
        /* ignore */
      }
    }
  };
  const goForward = () => {
    const t = activeTab();
    if (t?.view && t.canForward) {
      try {
        t.view.goForward();
      } catch {
        /* ignore */
      }
    }
  };
  const reloadOrStop = () => {
    const t = activeTab();
    if (!t?.view) return;
    try {
      if (t.loading) t.view.stop();
      else t.view.reload();
    } catch {
      /* ignore */
    }
  };

  // ── zoom ──
  const clampZoom = (z: number) => Math.max(-3, Math.min(3, z));
  const adjustZoom = (delta: number) => {
    const t = activeTab();
    if (!t?.view) return;
    Promise.resolve(t.view.getZoomLevel?.())
      .then((c: number) => {
        try {
          t.view.setZoomLevel(clampZoom((c || 0) + delta));
        } catch {
          /* ignore */
        }
      })
      .catch(() => {});
  };
  const resetZoom = () => {
    const t = activeTab();
    try {
      t?.view?.setZoomLevel(0);
    } catch {
      /* ignore */
    }
  };

  // ── find in page ──
  const openFind = () => {
    findBar.style.display = "";
    findInput.focus();
    findInput.select();
    if (findInput.value) activeTab()?.view?.findInPage?.(findInput.value);
  };
  const closeFind = () => {
    findBar.style.display = "none";
    findCount.textContent = "";
    try {
      activeTab()?.view?.stopFindInPage("clearSelection");
    } catch {
      /* ignore */
    }
  };
  findInput.addEventListener("input", () => {
    const t = activeTab();
    if (!t?.view) return;
    if (findInput.value) {
      try {
        t.view.findInPage(findInput.value);
      } catch {
        /* ignore */
      }
    } else {
      findCount.textContent = "";
      try {
        t.view.stopFindInPage("clearSelection");
      } catch {
        /* ignore */
      }
    }
  });
  findInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter") {
      const t = activeTab();
      if (t?.view && findInput.value) {
        try {
          t.view.findInPage(findInput.value, { findNext: true, forward: !e.shiftKey });
        } catch {
          /* ignore */
        }
      }
    } else if (e.key === "Escape") {
      closeFind();
    }
  });
  findClose.onclick = closeFind;

  // Guest-preload keyboard shortcuts routed here via ipc-message 'key'.
  const handleKey = (action: string) => {
    switch (action) {
      case "back":
        return goBack();
      case "forward":
        return goForward();
      case "focus-address":
        address.focus();
        return address.select();
      case "find":
        return openFind();
      case "reload":
        return reloadOrStop();
      case "stop":
        try {
          activeTab()?.view?.stop();
        } catch {
          /* ignore */
        }
        return;
    }
  };

  // ── wire toolbar ──
  btnBack.onclick = goBack;
  btnFwd.onclick = goForward;
  btnReload.onclick = reloadOrStop;
  btnGo.onclick = () => navigateTo(address.value);
  address.addEventListener("keydown", (e) => {
    if (e.key === "Enter") navigateTo(address.value);
  });
  btnOpenIn.onclick = () => {
    const t = activeTab();
    const nat = phNative();
    if (!t || !nat || !t.url || t.url === "about:blank") return;
    postJSON(nat, "/native/openin", { type: "url", target: t.url }).catch(() => {});
  };
  btnCopy.onclick = () => {
    const t = activeTab();
    if (!t?.url || t.url === "about:blank") return;
    navigator.clipboard?.writeText(t.url).catch(() => {});
    delete status.dataset.hover;
    status.textContent = "URL kopiert";
    window.setTimeout(() => {
      if (status.textContent === "URL kopiert") status.textContent = t.loading ? "Lädt…" : "";
    }, 1500);
  };
  btnNew.onclick = () => {
    const i = addTab(engine().home);
    activateTab(i);
    address.focus();
  };
  btnDev.onclick = () => {
    const t = activeTab();
    if (!t?.view || !t.domReady) return;
    try {
      if (devOpen) t.view.closeDevTools();
      else t.view.openDevTools();
    } catch {
      /* ignore */
    }
    devOpen = !devOpen;
    el.dataset.devtools = devOpen ? "open" : "closed";
  };
  // Ctrl+wheel over the chrome zooms; in-guest Ctrl+wheel arrives via ipc 'zoom'.
  viewWrap.addEventListener(
    "wheel",
    (e) => {
      if (!e.ctrlKey) return;
      e.preventDefault();
      adjustZoom(e.deltaY < 0 ? 0.5 : -0.5);
    },
    { passive: false },
  );

  // <webview> navigates reliably only once attached to the DOM, so seed the tabs
  // (migrating params.tabs JSON, else a single tab from params.url) from onAttached.
  const onAttached = () => {
    let seeded: { url?: string; title?: string }[] = [];
    try {
      if (params.tabs) seeded = JSON.parse(params.tabs);
    } catch {
      /* fall through to single-tab seed */
    }
    if (!Array.isArray(seeded) || seeded.length === 0) {
      // Fresh tile (no persisted tabs): open on the default search engine as home.
      const initial = params.url && params.url !== "about:blank" ? params.url : engine().home;
      seeded = [{ url: initial, title: "" }];
    }
    seeded.forEach((s) => {
      const i = addTab(s.url || "about:blank");
      tabs[i].title = s.title || "";
    });
    const want = parseInt(params.active || "0", 10);
    active = Number.isFinite(want) ? Math.min(Math.max(want, 0), tabs.length - 1) : 0;
    activateTab(active);
  };

  return {
    el,
    type: "browser",
    attached: false,
    onAttached,
    cleanup: () => {
      if (saveTimer) clearTimeout(saveTimer);
      window.removeEventListener(ENGINE_EVENT, onEngineChanged);
      tabs.forEach((t) => t.view?.remove());
    },
  };
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
// Browser navigation now lives entirely in the tile's own chrome (mountBrowser), so
// the old top-level navigate() is gone; go-app only attaches/destroys islands.
(window as any).phShell = { attachIsland, destroyIsland, parkIslands, applySearchEngine };

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
