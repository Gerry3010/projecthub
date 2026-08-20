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
import { EditorView, basicSetup } from "codemirror";
import { EditorState, Compartment, type Extension } from "@codemirror/state";
import { keymap } from "@codemirror/view";
import { StreamLanguage } from "@codemirror/language";
import { javascript } from "@codemirror/lang-javascript";
import { json } from "@codemirror/lang-json";
import { markdown } from "@codemirror/lang-markdown";
import { css } from "@codemirror/lang-css";
import { html } from "@codemirror/lang-html";
import { python } from "@codemirror/lang-python";
import { go } from "@codemirror/legacy-modes/mode/go";
import { shell as shellMode } from "@codemirror/legacy-modes/mode/shell";
import { oneDark } from "@codemirror/theme-one-dark";
import { dracula, cobalt, tomorrow, solarizedLight, ayuLight, espresso } from "thememirror";
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
  // go-app re-renders synchronously right after this call, so re-home on the following
  // frames. Two passes: the first catches the common case, the second covers a slot that
  // only appears once the new layout has settled.
  requestAnimationFrame(() => {
    rehomeIslands();
    requestAnimationFrame(rehomeIslands);
  });
}

/** Move every parked island back into its slot, for slots that exist right now.
 *
 *  This must not rely on the slot component's mount/update hooks: go-app skips OnUpdate
 *  entirely when no field changed (node.go updateComponent returns early on
 *  !modifiedFields). A tile that merely SURVIVES a layout mutation keeps the same
 *  PaneID/Type/Params, so its slot component is reused without any hook firing — and its
 *  island would stay in the pen, leaving a tile that is present but empty. */
function rehomeIslands(): void {
  registry.forEach((island, paneID) => {
    const slot = document.getElementById("ph-slot-" + paneID);
    if (!slot || island.el.parentElement === slot) return;
    slot.innerHTML = "";
    slot.appendChild(island.el);
    if (!island.attached) {
      island.attached = true;
      requestAnimationFrame(() => island.onAttached?.());
    }
    if (island.type === "terminal") queueMicrotask(() => (island.el as any)._fit?.());
  });
}

/** Put the keyboard into paneID's island. Called by go-app whenever a tile becomes the
 *  focused one (click, or the MCP tile_focus tool). Islands that own a real input —
 *  xterm, CodeMirror — publish a _focus hook on their element; everything else is a
 *  no-op, since go-app-rendered tiles take focus natively. */
function focusIsland(paneID: string): void {
  const island = registry.get(paneID);
  const focus = island && (island.el as any)._focus;
  if (typeof focus !== "function") return;
  try {
    focus();
  } catch {
    /* island not ready yet (not attached / disposed) */
  }
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
      return mountTerminal(el, params, paneID);
    case "markdown":
      return mountMarkdown(el, params);
    case "browser":
      return mountBrowser(el, params, paneID);
    case "editor":
      return mountEditor(el, params, paneID);
    default:
      el.textContent = `Unbekannter Tile-Typ: ${type}`;
      return { el, type, cleanup: () => {} };
  }
}

// ─── terminal ─────────────────────────────────────────────────────────────────

// Terminal word-navigation modifier (device-local, via phSecure). Picks which modifier
// + Arrow/Backspace/Delete triggers word-jump / word-delete in the terminal. Default
// "alt" mirrors xterm's historic behavior; Settings → Terminal drives it live via
// applyTerminalWordMod (no re-mount — every open terminal reads this at key time).
type TermWordMod = "alt" | "ctrl" | "meta";
let termWordMod: TermWordMod = "alt";
try {
  const saved = (window as any).phSecure?.get?.("ph.term.wordmod");
  if (saved === "alt" || saved === "ctrl" || saved === "meta") termWordMod = saved;
} catch {
  /* no phSecure bridge (hosted browser build) */
}

// applyTerminalWordMod is called BY go-app (Settings picker) to switch the modifier and
// persist it device-local. Runtime var is read live by every terminal's key handler.
function applyTerminalWordMod(key: string): void {
  if (key !== "alt" && key !== "ctrl" && key !== "meta") return;
  termWordMod = key;
  try {
    (window as any).phSecure?.set?.("ph.term.wordmod", key);
  } catch {
    /* no phSecure bridge */
  }
}

// ─── terminal bell sound ──────────────────────────────────────────────────────
//
// The bell (BEL / \a) makes a SOUND — it does not raise a toast. A bell means "look
// here now", and a notification card that needs reading (and dismissing) is the wrong
// answer to that; OSC 9 / OSC 777 still toast, because those carry an actual message.
//
// Tones are synthesised with WebAudio instead of shipping audio files: no assets, no
// packaging story, no decode latency. Device-local via phSecure (Settings → Terminal),
// same rationale as the word modifier — what sounds right depends on the machine.
type BellVoice = { at: number; freq: number; dur: number; type: OscillatorType; gain: number };

const bellVoices: Record<string, BellVoice[]> = {
  beep: [{ at: 0, freq: 880, dur: 0.12, type: "square", gain: 0.25 }],
  ping: [{ at: 0, freq: 1568, dur: 0.35, type: "sine", gain: 0.5 }],
  chime: [
    { at: 0, freq: 880, dur: 0.5, type: "sine", gain: 0.45 },
    { at: 0.11, freq: 1318.5, dur: 0.55, type: "sine", gain: 0.35 },
  ],
  knock: [
    { at: 0, freq: 180, dur: 0.09, type: "triangle", gain: 0.6 },
    { at: 0.12, freq: 150, dur: 0.11, type: "triangle", gain: 0.5 },
  ],
};

let bellSound = "ping"; // "off" or a key of bellVoices
let bellVolume = 0.6; // 0…1
try {
  const s = (window as any).phSecure?.get?.("ph.term.bellsound");
  if (s === "off" || (s && bellVoices[s])) bellSound = s;
  const v = Number((window as any).phSecure?.get?.("ph.term.bellvol"));
  if (Number.isFinite(v) && v > 0 && v <= 1) bellVolume = v;
} catch {
  /* no phSecure bridge (hosted browser build) */
}

let audioCtx: AudioContext | null = null;

// playTerminalBell renders one bell. Called with no argument by the bell handler (uses
// the configured sound); Settings passes a key to preview a sound before saving it.
function playTerminalBell(kind?: string): void {
  const voices = bellVoices[kind || bellSound];
  if (!voices) return; // "off" or unknown key → silence
  try {
    const Ctor = (window as any).AudioContext || (window as any).webkitAudioContext;
    if (!Ctor) return;
    if (!audioCtx) audioCtx = new Ctor();
    const ctx = audioCtx!;
    // Chromium suspends the context until a gesture; a bell can arrive before the user
    // ever clicked, so nudge it — worst case the resume is a no-op and this bell is mute.
    if (ctx.state === "suspended") void ctx.resume();
    const t0 = ctx.currentTime + 0.01;
    for (const v of voices) {
      const osc = ctx.createOscillator();
      const amp = ctx.createGain();
      osc.type = v.type;
      osc.frequency.value = v.freq;
      // Exponential decay to near-zero: a linear ramp to 0 clicks audibly at the tail.
      amp.gain.setValueAtTime(Math.max(0.0001, v.gain * bellVolume), t0 + v.at);
      amp.gain.exponentialRampToValueAtTime(0.0001, t0 + v.at + v.dur);
      osc.connect(amp).connect(ctx.destination);
      osc.start(t0 + v.at);
      osc.stop(t0 + v.at + v.dur + 0.02);
    }
  } catch {
    /* no WebAudio (or blocked) — a missing bell must never break the terminal */
  }
}

// applyTerminalBell is called BY go-app (Settings) to pick the sound; "" volume keeps
// the current one. Persists device-local and takes effect for every open terminal.
function applyTerminalBell(kind: string, volume: number): void {
  if (kind === "off" || bellVoices[kind]) bellSound = kind;
  if (Number.isFinite(volume) && volume > 0 && volume <= 1) bellVolume = volume;
  try {
    (window as any).phSecure?.set?.("ph.term.bellsound", bellSound);
    (window as any).phSecure?.set?.("ph.term.bellvol", String(bellVolume));
  } catch {
    /* no phSecure bridge */
  }
}

// Which pane currently owns which PTY session (ptyId → paneID). The sidecar serves a
// single subscriber per session: a second pane attaching to the same id takes it over
// and the first goes deaf, so you would end up typing into the wrong terminal. The Go
// side no longer copies pty_id when a tile is split, but a layout saved before that —
// or any future path that duplicates params — must not be able to reintroduce it, so
// reattach is refused here for an id another live pane already holds.
const ptyOwners = new Map<string, string>();

// isCopyChord reports whether a keydown means "copy the terminal selection":
// Ctrl+Shift+C everywhere, plus Cmd+C on macOS (where Ctrl+C is SIGINT and Cmd+C is
// the system-wide copy). Kept separate from the key handler so it stays readable.
function isCopyChord(e: KeyboardEvent): boolean {
  if ((e.key || "").toLowerCase() !== "c") return false;
  if (e.ctrlKey && e.shiftKey && !e.altKey) return true;
  const mac = /mac/i.test(navigator.platform || "") || /Mac OS X/.test(navigator.userAgent || "");
  return mac && e.metaKey && !e.ctrlKey && !e.altKey;
}

function mountTerminal(el: HTMLElement, params: Record<string, string>, paneID = ""): Island {
  const nat = phNative();
  const term = new Terminal({
    // "PH Nerd Symbols" is a bundled per-glyph fallback (theme.css) so Starship/
    // powerline/git glyphs in prompts render instead of tofu; JetBrains Mono still
    // supplies the base latin because it comes first.
    fontFamily: '"JetBrains Mono", "PH Nerd Symbols", ui-monospace, "Cascadia Code", monospace',
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
  (el as any)._focus = () => term.focus();
  // The xterm instance, next to the other island hooks: the E2E driver needs a way to
  // make a selection that does not depend on pixel geometry (scripts/claudeterm.mjs).
  (el as any)._term = term;

  let ws: WebSocket | null = null;
  let closed = false;
  let ptyId = ""; // function-scoped so cleanup can DELETE (kill) the session on tile close
  const ro = new ResizeObserver(() => doFit());

  // setTileParam writes one instance-scoped param (pty_id, session_id, the spent
  // prompt) both to the live params and to the persisted layout, so a restart restores
  // this very session instead of an equivalent-looking new one. "" clears the key.
  const setTileParam = (key: string, value: string) => {
    if (value) params[key] = value;
    else delete params[key];
    try {
      (window as any).phTileParam?.(paneID, key, value);
    } catch {
      /* no go-app bridge (hosted browser build) */
    }
  };

  // Word-navigation: for the configured modifier + Arrow/Backspace/Delete, send the
  // classic readline ESC sequences so word-jump/-delete works predictably no matter
  // which modifier the user picked (Settings → Terminal). Everything else falls through
  // to xterm's default handling.
  term.attachCustomKeyEventHandler((e: KeyboardEvent) => {
    if (e.type !== "keydown") return true;
    // Copy: Ctrl+Shift+C (Cmd+C on macOS). Plain Ctrl+C must stay SIGINT, so the
    // terminal needs its own chord — and nothing else provides one: pasting works
    // because Chromium turns Ctrl+Shift+V into a paste event on xterm's textarea, but
    // there is no matching native copy for a canvas-rendered selection.
    if (isCopyChord(e)) {
      const sel = term.getSelection();
      if (sel) void navigator.clipboard?.writeText(sel).catch(() => {});
      e.preventDefault(); // also keeps Ctrl+Shift+C from opening DevTools' inspector
      return false;
    }
    const mod = termWordMod === "alt" ? e.altKey : termWordMod === "ctrl" ? e.ctrlKey : e.metaKey;
    if (!mod) return true;
    let seq = "";
    switch (e.key) {
      case "ArrowLeft":
        seq = "\x1bb"; // backward-word
        break;
      case "ArrowRight":
        seq = "\x1bf"; // forward-word
        break;
      case "Backspace":
        seq = "\x1b\x7f"; // backward-kill-word
        break;
      case "Delete":
        seq = "\x1bd"; // kill-word (forward)
        break;
      default:
        return true;
    }
    e.preventDefault();
    if (ws?.readyState === WebSocket.OPEN) ws.send(frame(OP_DATA, new TextEncoder().encode(seq)));
    return false; // consumed — don't let xterm process it too
  });

  // Notifications: OSC 9 / OSC 777 and the PTY-idle "task done" hint route through
  // emitNotify (in-app toast + OS desktop). The bell only makes a sound — see
  // playTerminalBell. Rate-limited so a script spamming BEL can't machine-gun it.
  const notifyLabel = params.cmd === "claude" || params.session_id ? "Claude" : "Terminal";
  const isClaude = params.cmd === "claude" || !!params.session_id;
  let lastBell = 0;
  term.onBell(() => {
    const now = Date.now();
    if (now - lastBell > 700) {
      lastBell = now;
      playTerminalBell();
    }
  });
  term.parser.registerOscHandler(9, (data: string) => {
    emitNotify(notifyLabel, data); // iTerm-style: ESC ] 9 ; <message> BEL
    return true;
  });
  term.parser.registerOscHandler(777, (data: string) => {
    const p = data.split(";"); // ESC ] 777 ; notify ; <title> ; <body> BEL
    if (p[0] === "notify") emitNotify(p[1] || notifyLabel, p[2] || "");
    return true;
  });
  // PTY-idle heuristic: once a burst of Claude output goes quiet for a few seconds,
  // surface "Antwort fertig" once (re-arms on the next output).
  let lastData = 0;
  let idlePending = false;
  const idleTimer = isClaude
    ? setInterval(() => {
        if (idlePending && lastData && Date.now() - lastData >= 4000) {
          idlePending = false;
          emitNotify("Claude", "Antwort fertig");
        }
      }, 1000)
    : null;

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
      try {
        // Reattach to a still-running session after a renderer reload, if its PTY is
        // alive (the sidecar replays the scrollback on connect).
        const ownedElsewhere = params.pty_id ? (ptyOwners.get(params.pty_id) ?? paneID) !== paneID : false;
        if (params.pty_id && !ownedElsewhere) {
          const alive = await fetch(nat.base + "/native/pty/" + encodeURIComponent(params.pty_id), {
            headers: { Authorization: "Bearer " + nat.token },
          })
            .then((r) => r.status === 204)
            .catch(() => false);
          if (alive) ptyId = params.pty_id;
        }
        if (!ptyId) {
          // A Claude terminal PINS its session id: without one, every start (and every
          // restart of the app, where the PTY is gone) opened a brand-new Claude session
          // right next to the one this tile had been talking to — the duplicate,
          // prompt-less twin in `claude --resume`'s list. With the id minted up front the
          // sidecar starts `claude --session-id <id>` once and resumes exactly that
          // conversation afterwards.
          if (params.cmd === "claude" && !params.session_id) {
            setTileParam("session_id", crypto.randomUUID());
          }
          if (params.session_id) {
            // The prompt is a one-shot start argument (the Claude tile hands one over);
            // replaying it on every restore would re-ask the same question forever.
            const prompt = params.prompt || "";
            ptyId = await postJSON(nat, "/native/claude/resume", {
              cwd,
              session_id: params.session_id,
              prompt,
              cols,
              rows,
            }).then((r) => r.pty_id);
            if (prompt) setTileParam("prompt", "");
          } else {
            ptyId = await postJSON(nat, "/native/pty", { cwd, cmd: params.cmd || "", args: [], cols, rows }).then(
              (r) => r.pty_id,
            );
          }
          // Persist the new id so a later reload can reattach to this session.
          if (ptyId && ptyId !== params.pty_id) setTileParam("pty_id", ptyId);
        }
      } catch (e) {
        term.writeln(`\x1b[31mTerminal-Start fehlgeschlagen: ${e}\x1b[0m`);
        return;
      }
      if (ptyId) ptyOwners.set(ptyId, paneID);

      const wsURL = nat.base.replace(/^http/, "ws") + `/native/pty/${ptyId}/ws`;
      ws = new WebSocket(wsURL, nat.wsBearer);
      ws.binaryType = "arraybuffer";
      ws.onmessage = (ev) => {
        term.write(new Uint8Array(ev.data as ArrayBuffer));
        if (isClaude) {
          lastData = Date.now();
          idlePending = true;
        }
      };
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
      if (idleTimer) clearInterval(idleTimer);
      ro.disconnect();
      ws?.close();
      term.dispose();
      // The tile was intentionally destroyed (closed, or its workspace unmounted) → kill
      // the PTY. A renderer reload does NOT run cleanup, so that path keeps the session
      // alive for reattach-by-id instead.
      if (ptyId && nat) {
        if (ptyOwners.get(ptyId) === paneID) ptyOwners.delete(ptyId);
        fetch(nat.base + "/native/pty/" + encodeURIComponent(ptyId), {
          method: "DELETE",
          headers: { Authorization: "Bearer " + nat.token },
        }).catch(() => {});
      }
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

// ─── editor (CodeMirror) ──────────────────────────────────────────────────────

// langForPath picks a CodeMirror language by file extension (plain text otherwise).
function langForPath(path: string): Extension {
  const ext = (path.split(".").pop() || "").toLowerCase();
  switch (ext) {
    case "js":
    case "jsx":
    case "cjs":
    case "mjs":
      return javascript({ jsx: true });
    case "ts":
    case "tsx":
      return javascript({ jsx: true, typescript: true });
    case "json":
      return json();
    case "md":
    case "markdown":
      return markdown();
    case "css":
      return css();
    case "html":
    case "htm":
      return html();
    case "py":
      return python();
    case "go":
      return StreamLanguage.define(go);
    case "sh":
    case "bash":
    case "zsh":
      return StreamLanguage.define(shellMode);
    default:
      return [];
  }
}

// ─── editor themes ────────────────────────────────────────────────────────────

// Curated CodeMirror theme set for the editor tile. "default" = light baseline (no
// theme extension). Keys are what go-app persists (account- or project-level).
const EDITOR_THEMES: Record<string, { label: string; ext: Extension }> = {
  default: { label: "Hell (Standard)", ext: [] },
  "one-dark": { label: "One Dark", ext: oneDark },
  dracula: { label: "Dracula", ext: dracula },
  cobalt: { label: "Cobalt", ext: cobalt },
  tomorrow: { label: "Tomorrow", ext: tomorrow },
  "solarized-light": { label: "Solarized Light", ext: solarizedLight },
  "ayu-light": { label: "Ayu Light", ext: ayuLight },
  espresso: { label: "Espresso", ext: espresso },
};
const DEFAULT_EDITOR_THEME = "one-dark";
const EDITOR_THEME_EVENT = "ph-editor-theme-changed";
let currentEditorTheme = DEFAULT_EDITOR_THEME;

// Each mounted editor registers its tile-level actions here, keyed by paneID, so the
// standardized Go tile chrome can trigger them: window.phEditorSave(paneID) etc.
const editorActions: Record<string, { save: () => void; openInCode: () => void }> = {};
(window as any).phEditorSave = (id: string) => editorActions[id]?.save();
(window as any).phEditorOpenInCode = (id: string) => editorActions[id]?.openInCode();

function editorThemeExt(key: string): Extension {
  return (EDITOR_THEMES[key] || EDITOR_THEMES[DEFAULT_EDITOR_THEME]).ext;
}

// applyEditorTheme is called BY go-app after loading the resolved (project|account)
// theme, and by the in-editor picker: adopt it and live-reconfigure every open editor.
function applyEditorTheme(key: string): void {
  if (!EDITOR_THEMES[key]) return;
  currentEditorTheme = key;
  window.dispatchEvent(new CustomEvent(EDITOR_THEME_EVENT, { detail: key }));
}

// mountEditor is an in-tile CodeMirror file editor. It renders its own chrome (path
// input + Save + "VS Code"), loads/saves via the sidecar's /native/file, and polls
// for external changes — but never clobbers unsaved edits (dirty guard). The open
// path is pushed back to go-app (phEditorState) for layout restore + the tile label.
function mountEditor(el: HTMLElement, params: Record<string, string>, paneID: string): Island {
  el.classList.add("ph-editor");
  const nat = phNative();

  // Slim navigation strip: which file + dirty state. Tile-level ACTIONS (Save, VS
  // Code) live in the standardized tile chrome (Go), not here; the editor theme now
  // lives in the global Settings screen. This bar only shows/edits the open path.
  const bar = document.createElement("div");
  bar.className = "ph-editor-bar";
  const pathInput = document.createElement("input");
  pathInput.className = "ph-island-input ph-editor-path";
  pathInput.type = "text";
  pathInput.placeholder = "/pfad/zur/datei";
  pathInput.value = params.path || "";
  const dirtyDot = document.createElement("span");
  dirtyDot.className = "ph-editor-dirty";
  bar.append(pathInput, dirtyDot);

  const host = document.createElement("div");
  host.className = "ph-editor-host";
  el.append(bar, host);

  let curPath = params.path || "";
  let dirty = false;
  let lastMtime = "";
  const setDirty = (d: boolean) => {
    dirty = d;
    dirtyDot.textContent = d ? "●" : "";
  };

  const langComp = new Compartment();
  const themeComp = new Compartment();
  const view = new EditorView({
    parent: host,
    state: EditorState.create({
      doc: "",
      extensions: [
        basicSetup,
        langComp.of(langForPath(curPath)),
        themeComp.of(editorThemeExt(currentEditorTheme)),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) setDirty(true);
        }),
        keymap.of([{ key: "Mod-s", preventDefault: true, run: () => (void save(), true) }]),
      ],
    }),
  });
  (el as any)._focus = () => view.focus();

  // Live-reconfigure this editor when the theme changes (from the Settings screen's
  // editor-theme picker or go-app's initial push).
  const onThemeChange = (e: Event) => {
    const key = (e as CustomEvent).detail as string;
    view.dispatch({ effects: themeComp.reconfigure(editorThemeExt(key)) });
  };
  window.addEventListener(EDITOR_THEME_EVENT, onThemeChange);

  const setDoc = (text: string) => {
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } });
    setDirty(false);
  };

  const load = async (path: string) => {
    if (!nat || !path) return;
    try {
      const resp = await fetch(nat.base + "/native/file?path=" + encodeURIComponent(path), {
        headers: { Authorization: "Bearer " + nat.token },
      });
      view.dispatch({ effects: langComp.reconfigure(langForPath(path)) });
      if (!resp.ok) {
        setDoc(`Kann Datei nicht lesen: ${path}`);
        return;
      }
      lastMtime = resp.headers.get("X-Mtime") || "";
      setDoc(await resp.text());
    } catch {
      /* transient */
    }
  };

  const save = async () => {
    if (!nat || !curPath) return;
    try {
      const r = await postJSON(nat, "/native/file", { path: curPath, content: view.state.doc.toString() });
      lastMtime = r.mtime || lastMtime;
      setDirty(false);
    } catch {
      /* keep dirty so the user can retry */
    }
  };

  // Poll for external edits, but skip while the buffer is dirty so we never discard
  // unsaved work.
  const watch = async () => {
    if (!nat || !curPath || dirty) return;
    try {
      const url = nat.base + "/native/file?path=" + encodeURIComponent(curPath) + "&mtime=" + lastMtime;
      const resp = await fetch(url, { headers: { Authorization: "Bearer " + nat.token } });
      if (resp.status === 304 || !resp.ok) return;
      lastMtime = resp.headers.get("X-Mtime") || lastMtime;
      setDoc(await resp.text());
    } catch {
      /* transient */
    }
  };

  const openPath = (path: string) => {
    curPath = path.trim();
    (window as any).phEditorState?.(paneID, curPath);
    void load(curPath);
  };

  // Accept a file dragged from a file-tree tile onto the editor body (capture phase,
  // so we handle it before CodeMirror's own drop handling).
  const onDragOver = (e: DragEvent) => {
    if (e.dataTransfer?.types.includes("application/x-ph-path")) e.preventDefault();
  };
  const onDrop = (e: DragEvent) => {
    const p = e.dataTransfer?.getData("application/x-ph-path");
    if (p) {
      e.preventDefault();
      e.stopPropagation();
      pathInput.value = p;
      openPath(p);
    }
  };
  el.addEventListener("dragover", onDragOver, true);
  el.addEventListener("drop", onDrop, true);

  pathInput.addEventListener("change", () => openPath(pathInput.value));

  // Register this editor's tile-level actions for the Go tile chrome (Save, VS Code).
  const openInCode = () => {
    if (nat && curPath) postJSON(nat, "/native/openin", { type: "path", target: curPath, with: "code" }).catch(() => {});
  };
  editorActions[paneID] = { save: () => void save(), openInCode };

  if (curPath) void load(curPath);
  const watchTimer = window.setInterval(watch, 1500);

  return {
    el,
    type: "editor",
    cleanup: () => {
      clearInterval(watchTimer);
      window.removeEventListener(EDITOR_THEME_EVENT, onThemeChange);
      el.removeEventListener("dragover", onDragOver, true);
      el.removeEventListener("drop", onDrop, true);
      delete editorActions[paneID];
      view.destroy();
    },
  };
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

// The fractions a divider snaps to. Go owns the list (webui.SnapPoints) and hands it
// over on mount via setSnapPoints; this default only covers the moment before that.
let snapPoints: number[] = [0.25, 1 / 3, 0.5, 2 / 3, 0.75];

// snapThreshold: how far from a snap point the divider still gets pulled in. It is a
// fraction of the split, capped in pixels so a very wide split doesn't snap across
// half a centimetre of travel.
function snapThreshold(extent: number): number {
  return Math.min(0.03, 24 / Math.max(extent, 1));
}

// snapRatio pulls r onto the nearest snap point within thresh (mirrors webui.snapRatio).
function snapRatio(r: number, thresh: number): number {
  let best = r;
  let bestDist = thresh;
  for (const p of snapPoints) {
    const d = Math.abs(p - r);
    if (d <= bestDist) {
      best = p;
      bestDist = d;
    }
  }
  return best;
}

// showGuides draws one hairline per snap point inside the split being dragged, so the
// magnet is visible instead of merely felt. Removed again when the drag ends.
function showGuides(split: HTMLElement, dir: string): HTMLElement {
  const box = document.createElement("div");
  box.className = "ph-snapguides";
  for (const p of snapPoints) {
    const line = document.createElement("i");
    line.className = "ph-snapguide";
    line.dataset.p = String(p);
    if (dir === "col") line.style.top = p * 100 + "%";
    else line.style.left = p * 100 + "%";
    box.appendChild(line);
  }
  split.appendChild(box);
  return box;
}

function markGuide(box: HTMLElement | null, snapped: number | null): void {
  if (!box) return;
  for (const line of Array.from(box.children) as HTMLElement[]) {
    const hit = snapped !== null && Math.abs(Number(line.dataset.p) - snapped) < 1e-6;
    line.classList.toggle("is-active", hit);
  }
}

function initDividerResize(): void {
  let active: {
    split: HTMLElement;
    dir: string;
    node: string;
    guides: HTMLElement | null;
  } | null = null;

  document.addEventListener("pointerdown", (e) => {
    const div = (e.target as HTMLElement)?.closest?.(".ph-divider") as HTMLElement | null;
    if (!div) return;
    const split = div.parentElement as HTMLElement;
    if (!split?.classList.contains("ph-split")) return;
    const dir = split.dataset.dir || "row";
    active = { split, dir, node: div.dataset.node || "", guides: showGuides(split, dir) };
    div.setPointerCapture(e.pointerId);
    document.body.classList.add("ph-resizing");
    e.preventDefault();
  });

  document.addEventListener("pointermove", (e) => {
    if (!active) return;
    const rect = active.split.getBoundingClientRect();
    const vertical = active.dir === "col";
    const extent = vertical ? rect.height : rect.width;
    let r = (vertical ? e.clientY - rect.top : e.clientX - rect.left) / extent;
    r = Math.max(0.05, Math.min(0.95, r));
    // Alt turns the magnet off for the rest of the gesture — the escape hatch for a
    // proportion the snap table doesn't have.
    const snapped = e.altKey ? r : snapRatio(r, snapThreshold(extent));
    markGuide(active.guides, snapped === r ? null : snapped);
    active.split.style.setProperty("--r", String(snapped));
    (active.split as any)._r = snapped;
  });

  const end = () => {
    if (!active) return;
    const r = (active.split as any)._r;
    if (typeof r === "number") (window as any).phWsRatio?.(active.node, r);
    active.guides?.remove();
    document.body.classList.remove("ph-resizing");
    active = null;
  };
  document.addEventListener("pointerup", end);
  document.addEventListener("pointercancel", end);
}

/** setSnapPoints receives the snap table from Go (window.phShell.setSnapPoints). */
function setSnapPoints(points: number[]): void {
  const clean = (points || []).map(Number).filter((p) => p > 0 && p < 1);
  if (clean.length) snapPoints = clean;
}

/** setSplitRatio moves a divider from Go without a re-render: go-app does not
 *  reliably re-apply an inline custom property, so the menu writes it here. */
function setSplitRatio(nodeID: string, r: number): void {
  const div = document.querySelector(`.ph-divider[data-node="${CSS.escape(nodeID)}"]`);
  const split = div?.parentElement as HTMLElement | null;
  if (split?.classList.contains("ph-split")) split.style.setProperty("--r", String(r));
}

// ─── drag drop-zone preview ─────────────────────────────────────────────────────

// Shows a translucent overlay on the half/edge a tile drop would land, matching the
// Go-side dropEdge thresholds (0.25 / 0.75). Purely visual; go-app performs the move.
function initDropHint(): void {
  const hint = document.createElement("div");
  hint.className = "ph-drop-hint";
  hint.style.display = "none";
  document.body.appendChild(hint);
  // Insertion line for list reorders (todos): a thin accent bar on the edge the item
  // would land on. A rectangle would read as "replace"; a line reads as "insert here".
  const line = document.createElement("div");
  line.className = "ph-insert-hint";
  line.style.display = "none";
  document.body.appendChild(line);

  const hide = () => {
    hint.style.display = "none";
    line.style.display = "none";
  };

  /** Draw the insertion line above or below the row under the pointer. */
  const showInsertLine = (row: HTMLElement, e: DragEvent): void => {
    const r = row.getBoundingClientRect();
    if (!r.height) return hide();
    const below = (e.clientY - r.top) / r.height > 0.5;
    hint.style.display = "none";
    line.style.display = "block";
    line.style.left = r.left + "px";
    line.style.width = r.width + "px";
    line.style.top = (below ? r.bottom : r.top) - 1 + "px";
  };

  // Capture phase on purpose: the todo rows stop propagation so the tile (a drop
  // target itself) stays out of a reorder — a bubbling listener here would never see
  // those drags, and the insertion marker would never appear.
  document.addEventListener("dragover", (e) => {
    const types = Array.from(e.dataTransfer?.types ?? []);
    // A todo reorder stays inside its list — never show the tile-rearrange overlay for
    // it, which is what used to make reordering look like it would move the tile.
    if (types.includes("application/x-ph-todo")) {
      const row = (e.target as HTMLElement)?.closest?.(".ph-todoitem") as HTMLElement | null;
      return row ? showInsertLine(row, e) : hide();
    }
    const tile = (e.target as HTMLElement)?.closest?.(".ph-tile") as HTMLElement | null;
    if (!tile) return hide();
    const r = tile.getBoundingClientRect();
    if (!r.width || !r.height) return hide();
    line.style.display = "none";
    // Dragging a file onto a tile opens it THERE — it never splits, so the half/edge
    // preview would promise a layout change that does not happen.
    if (types.includes("application/x-ph-path") && !types.includes("application/x-ph-tile")) {
      hint.style.display = "block";
      hint.style.left = r.left + "px";
      hint.style.top = r.top + "px";
      hint.style.width = r.width + "px";
      hint.style.height = r.height + "px";
      return;
    }
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
  }, true);
  document.addEventListener("drop", hide, true);
  document.addEventListener("dragend", hide, true);
}

// ─── notifications ────────────────────────────────────────────────────────────────
// Unified notify path: an in-app toast overlay plus a native OS desktop notification
// (phNotify bridge). Fed by terminal bells/OSC sequences and the PTY-idle heuristic
// (mountTerminal), plus the sidecar's /native/notify queue (e.g. chattr messages).

let toastHost: HTMLElement | null = null;

function ensureToastHost(): HTMLElement {
  if (!toastHost) {
    toastHost = document.createElement("div");
    toastHost.className = "ph-toasts";
    document.body.appendChild(toastHost);
  }
  return toastHost;
}

function emitNotify(title: string, body: string): void {
  try {
    const host = ensureToastHost();
    const el = document.createElement("div");
    el.className = "ph-toast";
    const h = document.createElement("div");
    h.className = "ph-toast-title";
    h.textContent = title;
    el.appendChild(h);
    if (body) {
      const b = document.createElement("div");
      b.className = "ph-toast-body";
      b.textContent = body;
      el.appendChild(b);
    }
    el.addEventListener("click", () => el.remove());
    host.appendChild(el);
    setTimeout(() => {
      el.classList.add("ph-toast-out");
      setTimeout(() => el.remove(), 300);
    }, 6000);
  } catch {
    /* document.body not ready yet */
  }
  try {
    (window as any).phNotify?.(title, body);
  } catch {
    /* no bridge (hosted build) */
  }
}
// Exposed so go-app can raise a toast+desktop notification too.
(window as any).phEmitNotify = emitNotify;

// notifyLoop drains the sidecar's /native/notify queue (long-poll). Reconnects at once
// on 204/timeout; backs off on hard errors (sidecar restarting).
async function notifyLoop(): Promise<void> {
  const nat = phNative();
  if (!nat) return; // hosted build: no sidecar
  for (;;) {
    try {
      const resp = await fetch(nat.base + "/native/notify/next", {
        headers: { Authorization: "Bearer " + nat.token },
      });
      if (resp.status === 200) {
        const m = await resp.json();
        if (m && m.title) emitNotify(m.title, m.body || "");
      }
    } catch {
      await new Promise((r) => setTimeout(r, 5000));
    }
  }
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
(window as any).phShell = {
  attachIsland,
  destroyIsland,
  focusIsland,
  parkIslands,
  rehomeIslands,
  applySearchEngine,
  applyEditorTheme,
  applyTerminalWordMod,
  applyTerminalBell,
  playTerminalBell,
  setSnapPoints,
  setSplitRatio,
};

// The DOM-touching init (drop-hint appends to <body>) must wait: this script runs in
// <head>, where document.body is still null.
function boot(): void {
  initDividerResize();
  initDropHint();
  void notifyLoop(); // drain sidecar notifications (chattr etc.) into toasts + desktop
}
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot);
} else {
  boot();
}
