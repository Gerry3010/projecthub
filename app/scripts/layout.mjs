// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// E2E driver for the layout work:
//
//   1. A divider snaps magnetically to 1/4, 1/3, 1/2, 2/3, 3/4 while dragging,
//      shows the rast lines while you drag, and lets Alt through for a free ratio.
//   2. A tile's ⋯ menu sets that tile's share directly.
//   3. The layout manager applies a built-in arrangement and saves/restores a named
//      layout — which has to survive a restart, since it lives in the vault.
//
// Run:  cd app && xvfb-run -a node scripts/layout.mjs
// Needs: local Passbubble on :8765 with the test account, npm run build:all + make build.
//
// Throwaway XDG_CONFIG_HOME isolates the sidecar's device-local server override and
// Electron's userData; the layout itself lives in the account, so runs stack — hence
// trimTiles() and the unique preset name.

import { _electron as electron } from "playwright";
import { fileURLToPath } from "node:url";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

const appDir = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const configHome = fs.mkdtempSync(path.join(os.tmpdir(), "ph-e2e-"));
const log = (...a) => console.log("·", ...a);
const fails = [];
const check = (ok, msg) => (ok ? log("PASS", msg) : (fails.push(msg), log("FAIL", msg)));
const near = (a, b, eps = 0.01) => Math.abs(a - b) <= eps;

async function launch() {
  const app = await electron.launch({
    args: ["."],
    cwd: appDir,
    env: { ...process.env, PASSBUBBLE_URL: "http://localhost:8765", XDG_CONFIG_HOME: configHome },
  });
  const page = await app.firstWindow();
  page.on("pageerror", (e) => log("PAGEERROR:", e.message));
  await page.waitForSelector('input[type="email"], .ph-home, .ph-workspace', { timeout: 25000 });
  if (await page.locator('input[type="email"]').count()) {
    await page.click('input[type="email"]');
    await page.keyboard.press("Control+A");
    await page.type('input[type="email"]', "test@ph.local", { delay: 60 });
    await page.click('input[type="password"]');
    await page.keyboard.press("Control+A");
    await page.type('input[type="password"]', "test1234", { delay: 60 });
    await page.click('button:has-text("Entsperren")');
  }
  await page.waitForSelector(".ph-home, .ph-workspace", { timeout: 25000 });
  if (await page.locator(".ph-home .ph-proj").count()) {
    await page.locator(".ph-home .ph-proj").first().click();
  }
  await page.waitForSelector(".ph-workspace .ph-tile", { timeout: 20000 });
  await page.waitForTimeout(1500);
  return { app, page };
}

/** Close tiles until only `keep` remain (the layout is account-scoped, so runs stack). */
async function trimTiles(page, keep) {
  for (let guard = 0; guard < 12; guard++) {
    if ((await page.locator(".ph-workspace .ph-tile").count()) <= keep) break;
    await page.locator('.ph-workspace .ph-tile .ph-tile-bar button[title="schließen"]').last().click();
    await page.waitForTimeout(600);
  }
}

/** Open a tile's ⋯ overflow menu. Waits for any open popover to be gone first: its
 *  backdrop covers the whole window, so a click that lands while it is still up only
 *  dismisses it. */
async function openTileMenu(page, index = 0) {
  await page.waitForFunction(() => document.querySelectorAll(".ph-backdrop").length === 0, null, {
    timeout: 5000,
  });
  await page.locator('.ph-workspace .ph-tile .ph-tile-bar button[title="mehr"]').nth(index).click();
  await page.waitForSelector(".ph-tile-menu", { timeout: 5000 });
}

/** Open the layout manager, waiting out any popover backdrop first. */
async function openLayoutPanel(page) {
  await page.waitForFunction(() => document.querySelectorAll(".ph-backdrop").length === 0, null, {
    timeout: 5000,
  });
  await page.locator('.ph-ws-toolbar button[title="Layout"]').click();
  await page.waitForSelector(".ph-layout-panel", { timeout: 5000 });
}

async function splitOnce(page) {
  await openTileMenu(page, 0);
  await page.locator('.ph-tile-menu .ph-menu-item:has-text("Horizontal teilen")').click();
  await page.waitForTimeout(900);
}

/** The outermost split's current --r, as the DOM has it. */
const rootRatio = (page) =>
  page.evaluate(() => {
    const s = document.querySelector(".ph-ws-body .ph-split");
    return s ? Number(s.style.getPropertyValue("--r")) : NaN;
  });

/** Drag the outermost divider so the pointer lands at `frac` of the split's width.
 *  offsetPx nudges the landing point by that many pixels, which is how the tests aim
 *  just inside or just outside the magnet's reach (see snapThreshold in index.ts:
 *  3% of the split, capped at 24px). */
async function dragDivider(page, frac, { alt = false, probe, offsetPx = 0 } = {}) {
  const box = await page.evaluate(() => {
    const s = document.querySelector(".ph-ws-body .ph-split");
    const d = s?.querySelector(":scope > .ph-divider");
    if (!s || !d) return null;
    const sr = s.getBoundingClientRect();
    const dr = d.getBoundingClientRect();
    return { x: sr.x, y: sr.y, w: sr.width, h: sr.height, dx: dr.x + dr.width / 2, dy: dr.y + dr.height / 2 };
  });
  if (!box) throw new Error("no split/divider on screen");
  await page.mouse.move(box.dx, box.dy);
  await page.mouse.down();
  if (alt) await page.keyboard.down("Alt");
  // Two moves: the first gets the gesture going, the second lands on the target.
  await page.mouse.move(box.x + box.w * 0.5, box.y + box.h / 2, { steps: 4 });
  await page.mouse.move(box.x + box.w * frac + offsetPx, box.y + box.h / 2, { steps: 8 });
  let probed;
  if (probe) probed = await probe();
  await page.mouse.up();
  if (alt) await page.keyboard.up("Alt");
  await page.waitForTimeout(300);
  return probed;
}

async function main() {
  let { app, page } = await launch();
  await trimTiles(page, 1);
  await splitOnce(page);
  check((await page.locator(".ph-workspace .ph-tile").count()) === 2, "two tiles side by side");

  // ── 1. magnetic snapping ─────────────────────────────────────────────────
  const guides = await dragDivider(page, 1 / 3, {
    offsetPx: -14, // inside the magnet's reach
    probe: () => page.locator(".ph-snapguide").count(),
  });
  check(guides === 5, `rast lines visible while dragging (${guides})`);
  let r = await rootRatio(page);
  check(near(r, 1 / 3), `a drag stopping 14px short of a third snaps onto it (${r.toFixed(4)})`);
  check((await page.locator(".ph-snapguide").count()) === 0, "rast lines disappear when the drag ends");

  await dragDivider(page, 0.75, { offsetPx: 14 });
  r = await rootRatio(page);
  check(near(r, 0.75), `and 14px past three quarters snaps back (${r.toFixed(4)})`);

  // Beyond the magnet's reach the divider must stay exactly where it was dropped —
  // that pixel cap is what keeps a wide split from snapping across visible distance.
  await dragDivider(page, 0.5, { offsetPx: 60 });
  r = await rootRatio(page);
  check(!near(r, 0.5, 0.01), `60px from the half is out of reach and stays free (${r.toFixed(4)})`);

  await dragDivider(page, 0.42);
  r = await rootRatio(page);
  check(near(r, 0.42, 0.02), `a drag to 42% stays free (${r.toFixed(4)})`);

  // ── 2. Alt suspends the magnet ───────────────────────────────────────────
  await dragDivider(page, 0.5, { offsetPx: 8, alt: true });
  r = await rootRatio(page);
  check(!near(r, 0.5, 0.002), `Alt drags right through the half without snapping (${r.toFixed(4)})`);

  // ── 3. the tile menu's fraction chips ────────────────────────────────────
  await openTileMenu(page, 0);
  const chips = await page.locator(".ph-tile-menu .ph-frac-btn").count();
  check(chips === 5, `the tile menu offers five fractions (${chips})`);
  await page.locator('.ph-tile-menu .ph-frac-btn[title="¼"]').click();
  await page.waitForTimeout(400);
  r = await rootRatio(page);
  check(near(r, 0.25), `¼ in the first tile's menu sets the split to a quarter (${r.toFixed(4)})`);

  // The SECOND tile's ¼ means "give ME a quarter" — the stored ratio is mirrored.
  await openTileMenu(page, 1);
  await page.locator('.ph-tile-menu .ph-frac-btn[title="¼"]').click();
  await page.waitForTimeout(400);
  r = await rootRatio(page);
  check(near(r, 0.75), `¼ in the second tile's menu mirrors to 0.75 (${r.toFixed(4)})`);

  // ── 4. the layout manager ────────────────────────────────────────────────
  await splitOnce(page); // three tiles, so "3 Spalten" fits exactly
  await openLayoutPanel(page);
  check((await page.locator(".ph-layout-panel .ph-tpl").count()) === 6, "six arrangements offered");
  await page.locator('.ph-layout-panel .ph-tpl[title="3 Spalten"]').click();
  await page.waitForTimeout(900);
  const shape = await page.evaluate(() => {
    const root = document.querySelector(".ph-ws-body .ph-split");
    const inner = root?.querySelector(":scope > .ph-split-b > * > .ph-split");
    return {
      tiles: document.querySelectorAll(".ph-workspace .ph-tile").length,
      dir: root?.dataset.dir,
      r: Number(root?.style.getPropertyValue("--r")),
      innerDir: inner?.dataset.dir,
    };
  });
  check(shape.tiles === 3, `all three tiles survive the arrangement (${shape.tiles})`);
  check(shape.dir === "row" && near(shape.r, 1 / 3), `"3 Spalten" puts the first column at a third (${shape.r})`);

  // Balance flattens whatever the arrangement set.
  await openLayoutPanel(page);
  await page.locator('.ph-layout-panel button:has-text("Ausgleichen")').click();
  await page.waitForTimeout(500);
  r = await rootRatio(page);
  check(near(r, 0.5), `"Ausgleichen" evens the splits out (${r.toFixed(4)})`);

  // ── 5. a saved layout survives a restart ─────────────────────────────────
  const name = "E2E " + Date.now();
  await page.locator(".ph-layout-panel .ph-layout-save input").fill(name);
  await page.locator('.ph-layout-panel .ph-layout-save button:has-text("Sichern")').click();
  await page.waitForTimeout(1200); // debounced persist (400ms) + vault round-trip
  check(
    (await page.locator(`.ph-preset-apply:has-text("${name}")`).count()) === 1,
    "the saved layout appears in the list",
  );

  await app.close();
  ({ app, page } = await launch());
  await openLayoutPanel(page);
  const saved = page.locator(`.ph-preset-apply:has-text("${name}")`);
  check((await saved.count()) === 1, "the saved layout is still there after a restart");

  if (await saved.count()) {
    await saved.click();
    await page.waitForTimeout(1200);
    check(
      (await page.locator(".ph-workspace .ph-tile").count()) === 3,
      "restoring it brings back the three tiles",
    );
    // Clean up: the vault is shared between runs.
    await openLayoutPanel(page);
    await page.locator(`.ph-preset:has-text("${name}") button[title="Layout löschen"]`).click();
    await page.waitForTimeout(900);
    check(
      (await page.locator(`.ph-preset-apply:has-text("${name}")`).count()) === 0,
      "deleting the saved layout removes it",
    );
  }

  // ── 6. the panels stay inside the window and scroll ──────────────────────
  if (await page.locator(".ph-backdrop").count()) await page.locator(".ph-backdrop").first().click();
  for (const [title, sel] of [
    ["Aussehen / Hintergrund", ".ph-appr:not(.ph-layout-panel)"],
    ["Layout", ".ph-layout-panel"],
  ]) {
    await page.waitForFunction(() => document.querySelectorAll(".ph-backdrop").length === 0, null, {
      timeout: 5000,
    });
    await page.locator(`.ph-ws-toolbar button[title="${title}"]`).click();
    await page.waitForSelector(sel, { timeout: 5000 });
    const box = await page.evaluate((s) => {
      const el = document.querySelector(s);
      const r = el.getBoundingClientRect();
      el.scrollTop = el.scrollHeight; // ask for the very bottom
      const head = el.querySelector(".ph-appr-head").getBoundingClientRect();
      return {
        bottom: r.bottom,
        view: window.innerHeight,
        overflows: el.scrollHeight > el.clientHeight + 1,
        scrolled: el.scrollTop,
        headTop: head.top,
        panelTop: r.top,
      };
    }, sel);
    check(box.bottom <= box.view - 1, `${title}: the panel ends inside the window (${Math.round(box.bottom)} ≤ ${box.view})`);
    if (box.overflows) {
      check(box.scrolled > 0, `${title}: the panel scrolls when it runs out of room (${Math.round(box.scrolled)}px)`);
      check(
        Math.abs(box.headTop - box.panelTop) < 3,
        `${title}: the title sticks to the top while scrolling (${Math.round(box.headTop - box.panelTop)}px off)`,
      );
    } else {
      log("skip", `${title}: fits without scrolling in this window`);
    }
    await page.locator(`${sel} .ph-appr-head button`).click();
  }

  await app.close();
  console.log(fails.length ? `\n${fails.length} FAILED:\n- ${fails.join("\n- ")}` : "\nall checks passed");
  process.exit(fails.length ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
