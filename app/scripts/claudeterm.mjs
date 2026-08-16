// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// E2E driver for the two terminal fixes:
//
//   1. Ctrl+Shift+C copies the selection. Ctrl+C has to stay SIGINT and Chromium
//      offers no native copy for xterm's rendered selection, so the terminal brings
//      its own chord. (Paste already worked — Ctrl+Shift+V arrives as a paste event.)
//   2. A Claude terminal pins its session id. Without one, every app start opened a
//      brand-new Claude session next to the one the tile had been talking to — the
//      duplicate, prompt-less twin in `claude --resume`'s list. The tile must come
//      back on the SAME session id after a restart.
//
// Run:  cd app && xvfb-run -a node scripts/claudeterm.mjs
// Needs: local Passbubble on :8765 with the test account, npm run build:all + make build,
//        and a logged-in `claude` CLI (the tile starts the real thing — no prompt is
//        ever sent, so this costs no tokens).
//
// Throwaway XDG_CONFIG_HOME (as in tilefocus.mjs) isolates the sidecar's device-local
// server override and Electron's userData; the layout itself lives in the account.

import { _electron as electron } from "playwright";
import { fileURLToPath } from "node:url";
import { execSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

const appDir = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const configHome = fs.mkdtempSync(path.join(os.tmpdir(), "ph-e2e-"));
const log = (...a) => console.log("·", ...a);
const fails = [];
const check = (ok, msg) => (ok ? log("PASS", msg) : (fails.push(msg), log("FAIL", msg)));

/** Session ids of every running `claude` process (--session-id or --resume). */
function claudeSessions() {
  let out = "";
  try {
    out = execSync("ps -eo args=", { encoding: "utf8" });
  } catch {
    return new Set();
  }
  const ids = new Set();
  for (const line of out.split("\n")) {
    if (!line.includes("claude")) continue;
    const m = line.match(/--(?:session-id|resume)[= ]([0-9a-f-]{36})/);
    if (m) ids.add(m[1]);
  }
  return ids;
}

const diff = (before, after) => [...after].filter((id) => !before.has(id));

async function launch() {
  const app = await electron.launch({
    args: ["."],
    cwd: appDir,
    env: {
      ...process.env,
      PASSBUBBLE_URL: "http://localhost:8765",
      XDG_CONFIG_HOME: configHome,
    },
  });
  const page = await app.firstWindow();
  page.on("pageerror", (e) => log("PAGEERROR:", e.message));
  await page.waitForSelector('input[type="email"], .ph-home, .ph-workspace', {
    timeout: 25000,
  });
  // The unlock screen only shows when the throwaway profile has no live session yet.
  if (await page.locator('input[type="email"]').count()) {
    // Select-all before typing: the second launch finds the email remembered, and
    // appending to it logs in as "test@ph.localtest@ph.local" (401). Typing (not
    // fill()) is required — go-app binds on input events.
    await page.click('input[type="email"]');
    await page.keyboard.press("Control+A");
    await page.type('input[type="email"]', "test@ph.local", { delay: 60 });
    await page.click('input[type="password"]');
    await page.keyboard.press("Control+A");
    await page.type('input[type="password"]', "test1234", { delay: 60 });
    await page.click('button:has-text("Entsperren")');
  }
  try {
    await page.waitForSelector(".ph-home, .ph-workspace", { timeout: 25000 });
  } catch (e) {
    log("unlock failed; screen says:", (await page.locator("body").innerText()).replace(/\s+/g, " ").slice(0, 200));
    throw e;
  }
  if (await page.locator(".ph-home .ph-proj").count()) {
    await page.locator(".ph-home .ph-proj").first().click();
  }
  await page.waitForSelector(".ph-workspace .ph-tile", { timeout: 20000 });
  return { app, page };
}

/** Close tiles until only `keep` remain (the layout is account-scoped, so runs stack). */
async function trimTiles(page, keep) {
  for (let guard = 0; guard < 10; guard++) {
    const tiles = await page.locator(".ph-workspace .ph-tile").count();
    if (tiles <= keep) break;
    await page.locator('.ph-workspace .ph-tile .ph-tile-bar button[title="schließen"]').last().click();
    await page.waitForTimeout(700);
  }
}

async function main() {
  let { app, page } = await launch();
  await page.waitForTimeout(2500); // let the default terminal boot its PTY
  await trimTiles(page, 1);
  await page.waitForTimeout(1200);
  log("workspace open with", await page.locator(".ph-workspace .ph-tile").count(), "tile(s)");

  // ── 1. Ctrl+Shift+C copies the selection ─────────────────────────────────
  const marker = "PH_COPY_" + Date.now();
  const shellPane = await page.evaluate(
    () => [...document.querySelectorAll(".ph-tile")].find((t) => t.querySelector(".xterm"))?.dataset.pane || "",
  );
  check(!!shellPane, "a terminal tile is on screen to copy from");
  // Focus the helper textarea directly: the tile chrome swallows a click on the
  // canvas in this headless layout, but the user's click ends up here either way.
  await page.evaluate(
    (p) => document.querySelector(`.ph-tile[data-pane="${p}"] .xterm-helper-textarea`)?.focus(),
    shellPane,
  );
  await page.waitForTimeout(300);
  await page.keyboard.type(marker, { delay: 40 }); // no Enter: keep it on the prompt line
  await page.waitForTimeout(600);

  // Select via xterm's own API (the _term island hook) — a pixel drag would depend on
  // the tile's geometry, which is a single row tall under xvfb.
  const selected = await page.evaluate((p) => {
    const host = [...document.querySelectorAll(`.ph-tile[data-pane="${p}"] *`)].find((e) => e._term);
    if (!host) return "";
    host._term.selectAll();
    return host._term.getSelection();
  }, shellPane);
  check(selected.includes(marker), "the typed marker is part of the terminal selection");

  await app.evaluate(({ clipboard }) => clipboard.writeText("PH_CLIPBOARD_UNTOUCHED"));
  await page.keyboard.press("Control+Shift+C");
  await page.waitForTimeout(500);
  const copied = await app.evaluate(({ clipboard }) => clipboard.readText());
  check(copied.includes(marker), "Ctrl+Shift+C copies the terminal selection");
  if (!copied.includes(marker)) log("  clipboard held:", JSON.stringify(copied.slice(0, 120)));

  // Ctrl+C must stay SIGINT: it may not copy the selection.
  await app.evaluate(({ clipboard }) => clipboard.writeText("PH_SIGINT_GUARD"));
  await page.keyboard.press("Control+C");
  await page.waitForTimeout(400);
  check(
    (await app.evaluate(({ clipboard }) => clipboard.readText())) === "PH_SIGINT_GUARD",
    "plain Ctrl+C stays SIGINT (does not copy)",
  );

  // ── 2. a Claude terminal pins its session id ─────────────────────────────
  const before = claudeSessions();
  await page.locator(".ph-ws-toolbar button", { hasText: "+" }).first().click();
  await page.locator('.ph-menu button:has-text("Terminal (Claude)")').click();
  await page.waitForTimeout(6000); // claude is a heavy start

  const started = diff(before, claudeSessions());
  check(started.length === 1, `starting a Claude terminal pins exactly one session id (${started.length})`);
  if (started.length !== 1) {
    log("  new claude sessions:", started.join(", ") || "(none — is the claude CLI logged in?)");
    await app.close();
    console.log(`\n${fails.length} FAILED:\n- ${fails.join("\n- ")}`);
    process.exit(1);
  }
  const pinned = started[0];
  log("pinned session", pinned);

  // ── 3. …and comes back on the SAME session after a restart ───────────────
  await app.close();
  await new Promise((r) => setTimeout(r, 2000));
  const beforeRestart = claudeSessions();
  check(!beforeRestart.has(pinned), "closing the app takes its Claude session down with it");

  ({ app, page } = await launch());
  await page.waitForTimeout(9000); // restore + claude start

  const afterRestart = claudeSessions();
  check(afterRestart.has(pinned), "the restored tile continues the SAME Claude session");
  const extras = diff(beforeRestart, afterRestart).filter((id) => id !== pinned);
  check(extras.length === 0, `no second Claude session appears next to it (${extras.length})`);
  if (extras.length) log("  unexpected extra sessions:", extras.join(", "));

  // Leave the account layout as we found it (and kill the pinned PTY).
  await trimTiles(page, 1);
  await app.close();
  console.log(fails.length ? `\n${fails.length} FAILED:\n- ${fails.join("\n- ")}` : "\nall checks passed");
  process.exit(fails.length ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
