// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// E2E driver for two behaviours that are only observable in the running app:
//
//   1. The terminal BELL makes a SOUND and raises no notification card. OSC 9 (a bell
//      that carries a message) must still toast — that is the line between the two.
//      Settings → Terminal picks the tone; "Aus" silences it.
//   2. The browser tile's HTTP cache is OFF by default, and the Settings toggle
//      actually reaches the main process (which owns the guests' session).
//
// Run:  cd app && xvfb-run -a node scripts/bellcache.mjs
// Needs: local Passbubble on :8765 with the test account, npm run build:all + make wasm.
//
// Sound can't be heard here, so it is counted: OscillatorNode.prototype.start is wrapped
// before the bell rings. That is the same signal the speaker gets.

import { _electron as electron } from "playwright";
import { fileURLToPath } from "node:url";
import * as fs from "node:fs";
import * as http from "node:http";
import * as os from "node:os";
import * as path from "node:path";

const appDir = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const configHome = fs.mkdtempSync(path.join(os.tmpdir(), "ph-e2e-"));
const log = (...a) => console.log("·", ...a);
const fails = [];
const check = (ok, msg) => (ok ? log("PASS", msg) : (fails.push(msg), log("FAIL", msg)));

const bells = (page) => page.evaluate(() => window.__phBells || 0);

/** Put the keyboard into the first terminal. A plain click can be swallowed by the
 *  tile chrome, so drive the shell's own focus path and confirm it took. */
const focusTerminal = async (page) => {
  const pane = await page.evaluate(() => {
    const tile = [...document.querySelectorAll(".ph-tile")].find((t) => t.querySelector(".xterm"));
    if (!tile) return "";
    window.phShell?.focusIsland?.(tile.dataset.pane);
    return tile.dataset.pane || "";
  });
  await page.waitForTimeout(300);
  return pane;
};
const toasts = (page) => page.evaluate(() => [...document.querySelectorAll(".ph-toast")].map((t) => t.innerText));

/** A tiny origin whose asset.js is aggressively cacheable (max-age=600). Counting how
 *  often it is actually fetched is the only honest test of "cache off": with caching on,
 *  the second page must NOT re-request it; with caching off, it must.
 *
 *  Every page carries a generation ?v=…, and its asset URL inherits it. Each phase of
 *  the test uses a fresh generation, so the cache-on phase starts from a genuinely cold
 *  entry instead of reusing what the cache-off phase already pulled into memory. */
function startProbeServer() {
  const state = { hits: {}, assetHeaders: [], paths: [] };
  const srv = http.createServer((req, res) => {
    state.paths.push(req.url);
    const v = new URL(req.url, "http://probe").searchParams.get("v") || "";
    if (req.url.startsWith("/asset.js")) {
      state.hits[v] = (state.hits[v] || 0) + 1;
      state.assetHeaders.push(req.headers);
      res.writeHead(200, { "Content-Type": "application/javascript", "Cache-Control": "public, max-age=600" });
      res.end("window.__probe = 1;\n");
      return;
    }
    res.writeHead(200, { "Content-Type": "text/html", "Cache-Control": "no-store" });
    res.end(`<!doctype html><title>probe</title><script src="/asset.js?v=${v}"></script><h1>${req.url}</h1>`);
  });
  return new Promise((resolve) => srv.listen(0, "127.0.0.1", () => resolve({ srv, state, port: srv.address().port })));
}

async function main() {
  const app = await electron.launch({
    args: ["."],
    cwd: appDir,
    env: { ...process.env, PASSBUBBLE_URL: "http://localhost:8765", XDG_CONFIG_HOME: configHome },
  });
  const page = await app.firstWindow();
  page.on("pageerror", (e) => log("PAGEERROR:", e.message));

  // ── login + open a project ───────────────────────────────────────────────
  await page.waitForSelector('input[type="email"]', { timeout: 20000 });
  await page.click('input[type="email"]');
  await page.type('input[type="email"]', "test@ph.local", { delay: 60 });
  await page.click('input[type="password"]');
  await page.type('input[type="password"]', "test1234", { delay: 60 });
  await page.click('button:has-text("Entsperren")');
  await page.waitForSelector(".ph-home .ph-proj", { timeout: 20000 });

  // ── 2a. cache default (checked before the project opens — it is app-global) ──
  const cacheDefault = await page.evaluate(() => window.phWindow?.getBrowserCache?.() ?? null);
  check(cacheDefault === false, `browser-tile cache is OFF by default (got ${cacheDefault})`);

  await page.locator(".ph-home .ph-proj").first().click();
  await page.waitForSelector(".ph-workspace .ph-tile", { timeout: 20000 });
  await page.waitForTimeout(2500); // let the default terminal boot its PTY
  // The layout lives in the ACCOUNT, not in the throwaway config dir, so it carries over
  // between runs — collapse to a single tile before touching anything.
  for (let guard = 0; guard < 8; guard++) {
    if ((await page.locator(".ph-workspace .ph-tile").count()) <= 1) break;
    await page.locator('.ph-workspace .ph-tile .ph-tile-bar button[title="schließen"]').last().click();
    await page.waitForTimeout(700);
  }
  await page.waitForTimeout(1200);
  log("workspace open,", await page.locator(".ph-workspace .ph-tile").count(), "tile(s)");

  const termTile = page.locator(".ph-tile:has(.xterm)").first();
  if (!(await termTile.count())) {
    log("no terminal tile — cannot test the bell");
    fails.push("terminal tile present");
    await app.close();
    console.log(`\n${fails.length} FAILED:\n- ${fails.join("\n- ")}`);
    process.exit(1);
  }

  // Count oscillators instead of listening. Wrapping the prototype covers the shell's
  // AudioContext whether or not it has been created yet.
  await page.evaluate(() => {
    window.__phBells = 0;
    const start = OscillatorNode.prototype.start;
    OscillatorNode.prototype.start = function (...a) {
      window.__phBells++;
      return start.apply(this, a);
    };
  });

  // ── 1a. BEL → sound, no card ────────────────────────────────────────────
  await focusTerminal(page);
  await page.keyboard.type("printf '\\a'", { delay: 25 });
  await page.keyboard.press("Enter");
  await page.waitForTimeout(1500);

  const rang = await bells(page);
  check(rang > 0, `the bell plays a sound (${rang} oscillator(s) started)`);
  const cards = await toasts(page);
  check(cards.length === 0, `the bell shows no notification card (got ${JSON.stringify(cards)})`);

  // ── 1b. OSC 9 still toasts (it carries a message) ───────────────────────
  await page.keyboard.type("printf '\\033]9;E2E-Meldung\\a'", { delay: 25 });
  await page.keyboard.press("Enter");
  await page.waitForTimeout(1500);
  const osc = await toasts(page);
  check(
    osc.some((t) => t.includes("E2E-Meldung")),
    `OSC 9 still raises a notification (got ${JSON.stringify(osc)})`,
  );

  // ── 1c. Settings → Terminal → "Aus" silences the bell ───────────────────
  await page.evaluate(() => document.querySelectorAll(".ph-toast").forEach((t) => t.remove()));
  await page.locator(".ph-rail button[title='Einstellungen']").click();
  await page.waitForSelector(".ph-settings", { timeout: 10000 });
  await page.locator(".ph-settings-tab", { hasText: "Terminal" }).click();
  await page.waitForTimeout(500);
  // Identify the bell picker by its options, not by position: the word-modifier select
  // sits on the same tab, and "the last select" would happily match that one instead.
  const bellSel = page.locator('.ph-settings-pane select:has(option[value="off"])').first();
  check((await bellSel.count()) > 0, "Settings → Terminal has the bell picker");
  await bellSel.selectOption("off");
  await page.waitForTimeout(800);
  check(
    (await page.evaluate(() => window.phSecure?.get?.("ph.term.bellsound"))) === "off",
    "the picked bell sound is persisted device-local",
  );
  await page.locator(".ph-settings-head button[title='Schließen']").click();
  await page.waitForTimeout(800);

  const before = await bells(page);
  await focusTerminal(page);
  await page.keyboard.type("printf '\\a'", { delay: 25 });
  await page.keyboard.press("Enter");
  await page.waitForTimeout(1500);
  check((await bells(page)) === before, "bell set to «Aus» stays silent");

  // Put it back so the next run starts from a ringing bell.
  await page.evaluate(() => window.phShell?.applyTerminalBell?.("ping", 0.6));

  // ── 2b. the cache toggle reaches the main process ───────────────────────
  await page.locator(".ph-rail button[title='Einstellungen']").click();
  await page.waitForSelector(".ph-settings", { timeout: 10000 });
  await page.locator(".ph-settings-tab", { hasText: "Browser" }).click();
  await page.waitForTimeout(500);
  const cacheBox = page.locator(".ph-settings-pane .ph-check input[type=checkbox]").first();
  check((await cacheBox.count()) > 0, "Settings → Browser has the cache toggle");
  check((await cacheBox.isChecked()) === false, "the toggle reflects the OFF default");
  await cacheBox.check();
  await page.waitForTimeout(600);
  check((await page.evaluate(() => window.phWindow.getBrowserCache())) === true, "turning the cache on reaches main");
  await cacheBox.uncheck();
  await page.waitForTimeout(600);
  check((await page.evaluate(() => window.phWindow.getBrowserCache())) === false, "…and turning it off again does too");
  await page.locator(".ph-settings-head button[title='Schließen']").click();
  await page.waitForTimeout(600);

  // ── 2c. …and the guest really bypasses the cache ─────────────────────────
  const probe = await startProbeServer();
  log("probe server on", probe.port);
  await page.locator(".ph-ws-toolbar button", { hasText: "+" }).first().click();
  await page.locator(".ph-menu button", { hasText: /^Browser$/ }).click();
  await page.waitForTimeout(1500);

  const go = async (p, v) => {
    const url = page.locator(".ph-browser-url").last();
    await url.click();
    await url.fill(`http://127.0.0.1:${probe.port}${p}?v=${v}`);
    await page.keyboard.press("Enter");
    await page.waitForTimeout(2500);
  };
  await go("/p1", "off");
  await go("/p2", "off");
  check(
    probe.state.hits.off === 2,
    `cache off: the max-age=600 asset is re-fetched on every page (${probe.state.hits.off} hits, want 2)`,
  );
  check(
    probe.state.assetHeaders.every((h) => (h["cache-control"] || "").includes("no-cache")),
    `…and its requests carry no-cache (${JSON.stringify(probe.state.assetHeaders.map((h) => h["cache-control"]))})`,
  );

  // With caching on, the same asset must be served from disk the second time.
  await page.evaluate(() => window.phWindow.setBrowserCache(true));
  await go("/p3", "on");
  await go("/p4", "on");
  check(
    probe.state.hits.on === 1,
    `cache on: the asset is fetched once and reused (${probe.state.hits.on} hits, want 1)`,
  );
  await page.evaluate(() => window.phWindow.setBrowserCache(false)); // leave the default in place
  log("probe requests:", JSON.stringify(probe.state.paths));
  probe.srv.close();

  await app.close();
  console.log(fails.length ? `\n${fails.length} FAILED:\n- ${fails.join("\n- ")}` : "\nall checks passed");
  process.exit(fails.length ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
