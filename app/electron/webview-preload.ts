// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// Guest preload for the browser tile's <webview>. Runs inside every guest page
// (sandboxed, no node integration) and is injected by main.ts via
// will-attach-webview. Its only job is to forward a few interactions the host
// chrome can't observe from outside the guest frame — mouse back/forward
// buttons, keyboard shortcuts, and popup/target=_blank navigations — up to the
// host renderer through ipcRenderer.sendToHost, where the TabManager reacts.
//
// tsconfig (include: electron/**/*.ts) compiles this to dist/webview-preload.js
// next to main.js automatically — no extra build step.

import { ipcRenderer } from "electron";

// ─── mouse buttons 4/5 (back/forward) ────────────────────────────────────────
// MouseEvent.button: 3 = X1 (back), 4 = X2 (forward). preventDefault stops
// Chromium's own built-in X1/X2 navigation so we don't navigate twice.
window.addEventListener(
  "mouseup",
  (e) => {
    if (e.button === 3) {
      e.preventDefault();
      ipcRenderer.sendToHost("nav", "back");
    } else if (e.button === 4) {
      e.preventDefault();
      ipcRenderer.sendToHost("nav", "forward");
    }
  },
  true,
);
// Some builds fire navigation on the auxiliary mousedown/auxclick too; swallow
// them for the same buttons to avoid a double step.
for (const type of ["mousedown", "auxclick"] as const) {
  window.addEventListener(
    type,
    (e) => {
      if ((e as MouseEvent).button === 3 || (e as MouseEvent).button === 4) {
        e.preventDefault();
      }
    },
    true,
  );
}

// ─── keyboard shortcuts ──────────────────────────────────────────────────────
// The host toolbar can't see keydowns that land inside the guest frame, so
// forward the browser-navigation subset. The host decides what each does.
window.addEventListener(
  "keydown",
  (e) => {
    const send = (action: string) => {
      e.preventDefault();
      ipcRenderer.sendToHost("key", action);
    };
    if (e.altKey && e.key === "ArrowLeft") return send("back");
    if (e.altKey && e.key === "ArrowRight") return send("forward");
    if ((e.ctrlKey || e.metaKey) && !e.shiftKey) {
      switch (e.key.toLowerCase()) {
        case "l":
          return send("focus-address");
        case "f":
          return send("find");
        case "r":
          return send("reload");
      }
      // Zoom shortcuts route to the host, which drives view.setZoomLevel.
      if (e.key === "+" || e.key === "=") {
        e.preventDefault();
        return ipcRenderer.sendToHost("zoom", "in");
      }
      if (e.key === "-") {
        e.preventDefault();
        return ipcRenderer.sendToHost("zoom", "out");
      }
      if (e.key === "0") {
        e.preventDefault();
        return ipcRenderer.sendToHost("zoom", "reset");
      }
    }
    if (e.key === "Escape") return send("stop");
  },
  true,
);

// Ctrl+wheel inside the guest frame can't reach the host window listener, so
// forward it here and let the host adjust the zoom level.
window.addEventListener(
  "wheel",
  (e) => {
    if (!e.ctrlKey) return;
    e.preventDefault();
    ipcRenderer.sendToHost("zoom", e.deltaY < 0 ? "in" : "out");
  },
  { passive: false, capture: true },
);

// ─── popup / target=_blank interception → new tab ────────────────────────────
// window.open and <a target="_blank"> would otherwise spawn a native popup
// window (or be denied by setWindowOpenHandler). Route them to the host so the
// TabManager opens an in-tile tab instead.
const openTab = (url: string) => {
  if (url) ipcRenderer.sendToHost("open-tab", url);
};

const nativeOpen = window.open.bind(window);
window.open = ((url?: string | URL, ...rest: unknown[]) => {
  const href = url ? String(url) : "";
  if (href && href !== "about:blank") {
    openTab(href);
    return null;
  }
  return (nativeOpen as (...a: unknown[]) => Window | null)(url as never, ...(rest as never[]));
}) as typeof window.open;

document.addEventListener(
  "click",
  (e) => {
    const a = (e.target as HTMLElement | null)?.closest?.("a") as HTMLAnchorElement | null;
    if (!a) return;
    const targetsNewWindow = a.target === "_blank" || a.target === "_new";
    if (targetsNewWindow && a.href) {
      e.preventDefault();
      openTab(a.href);
    }
  },
  true,
);
