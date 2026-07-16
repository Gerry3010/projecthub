// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// Dev driver: launches the Electron app via Playwright, logs into the local test
// account, opens a project, and exercises the workspace (swatch click, browser,
// drag). Prints a report + console messages so we can debug UI without a human.
//
// Run:  cd app && xvfb-run -a node scripts/drive.mjs
// Needs: local Passbubble on :8765, a built app (npm run build:all), web assets built.

import { _electron as electron } from "playwright";
import { fileURLToPath } from "node:url";
import * as path from "node:path";

const appDir = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const repo = path.join(appDir, "..");
const shot = (name) => path.join("/tmp", `ph-${name}.png`);

const log = (...a) => console.log("·", ...a);
const consoleMsgs = [];

async function main() {
  const app = await electron.launch({
    args: ["."],
    cwd: appDir,
    env: { ...process.env, PASSBUBBLE_URL: "http://localhost:8765" },
  });

  const page = await app.firstWindow();
  page.on("console", (m) => consoleMsgs.push(m.text()));
  page.on("pageerror", (e) => consoleMsgs.push("PAGEERROR: " + e.message));
  page.on("response", (r) => {
    if (r.status() >= 400) consoleMsgs.push(`HTTP ${r.status()} ${r.url()}`);
  });

  // ── login ────────────────────────────────────────────────────────────────
  await page.waitForSelector('input[type="email"]', { timeout: 20000 });
  // go-app controlled inputs reset value on re-render, so type slowly (per-char delay)
  // to let each OnInput → re-render cycle settle.
  await page.click('input[type="email"]');
  await page.type('input[type="email"]', "test@ph.local", { delay: 60 });
  await page.click('input[type="password"]');
  await page.type('input[type="password"]', "test1234", { delay: 60 });
  await page.waitForTimeout(300);
  const vals = await page.evaluate(() => ({
    email: document.querySelector('input[type="email"]')?.value,
    pw: (document.querySelector('input[type="password"]')?.value || "").length,
  }));
  log("field values:", JSON.stringify(vals));
  await page.click('button:has-text("Entsperren")');
  log("submitted login");
  await page.waitForTimeout(2500);
  const postLogin = await page.evaluate(() => ({
    view: document.querySelector(".ph-workspace") ? "workspace" : document.querySelector(".ph-app") ? "projects" : document.querySelector(".ph-center") ? "login" : "?",
    err: document.querySelector(".ph-err")?.textContent || "",
  }));
  log("post-login:", JSON.stringify(postLogin));
  await page.screenshot({ path: shot("postlogin") });

  // Projects list appears once unlocked.
  await page.waitForSelector(".ph-app .ph-titlebtn", { timeout: 20000 });
  const projects = await page.$$eval(".ph-titlebtn", (els) => els.map((e) => e.textContent));
  log("projects:", projects.slice(0, 6));
  await page.screenshot({ path: shot("projects") });

  // ── open first project → workspace ─────────────────────────────────────────
  await page.click(".ph-titlebtn");
  await page.waitForSelector(".ph-ws-toolbar", { timeout: 20000 });
  log("workspace open");
  await page.waitForTimeout(1500);
  await page.screenshot({ path: shot("workspace") });

  // ── swatch click test ──────────────────────────────────────────────────────
  const accentBefore = await page.evaluate(
    () => document.querySelector(".ph-workspace")?.style.getPropertyValue("--accent") || "",
  );
  const swatches = await page.$$(".ph-ws-toolbar .ph-swatch");
  log("toolbar swatches found:", swatches.length);
  // Click each preset in turn; --accent must follow the clicked swatch's color.
  const readAccent = () =>
    page.evaluate(() => document.querySelector(".ph-workspace")?.style.getPropertyValue("--accent") || "");
  const seen = [];
  for (let i = 0; i < Math.min(3, swatches.length); i++) {
    const bg = await swatches[i].evaluate((el) => getComputedStyle(el).backgroundColor);
    await swatches[i].click();
    await page.waitForTimeout(400);
    const acc = await readAccent();
    seen.push({ i, bg, acc });
  }
  log("swatch clicks:", JSON.stringify(seen));
  const accentAfter = await readAccent();
  const changed = seen.some((s) => s.acc !== accentBefore);
  log(`accent before=${accentBefore} after=${accentAfter}  changed=${changed}`);

  // ── terminal content check ─────────────────────────────────────────────────
  const termText = await page.evaluate(() => {
    const t = document.querySelector(".ph-island-inner .xterm-rows");
    return t ? t.textContent.trim().slice(0, 80) : "(no xterm-rows)";
  });
  log("terminal text:", JSON.stringify(termText));

  // ── browser tile ───────────────────────────────────────────────────────────
  // Add a Browser tile via the "+ Tile" menu and exercise the new chrome. Earlier
  // runs may have persisted browser tiles into this project's layout, so always work
  // with the LAST .ph-browser (the one we just added) for deterministic assertions.
  await page.click(".ph-ws-toolbar .ph-add .ph-btn"); // "+ Tile"
  await page.waitForSelector(".ph-menu", { timeout: 5000 });
  await page.click('.ph-menu-item:has-text("Browser")');
  await page.waitForSelector(".ph-browser .ph-browser-toolbar", { timeout: 10000 });
  await page.waitForTimeout(1200); // let the seed tab reach dom-ready
  const browser = page.locator(".ph-browser").last();
  const readBrowser = () =>
    browser.evaluate((b) => ({
      hasToolbar: !!b.querySelector(".ph-browser-toolbar"),
      hasUrl: !!b.querySelector(".ph-browser-url"),
      hasStatus: !!b.querySelector(".ph-browser-status"),
      hasView: !!b.querySelector(".ph-browser-view webview"),
      tabsSingle: b.querySelector(".ph-browser-tabs")?.classList.contains("single"),
      tabCount: b.querySelectorAll(".ph-browser-tab").length,
    }));
  const browserChrome = await readBrowser();
  log("browser chrome:", JSON.stringify(browserChrome));

  // Toolbar "+" opens a new tab; the tab strip becomes visible (loses .single).
  await browser.locator('.ph-browser-toolbar .ph-browser-btn[title="Neuer Tab"]').click();
  await page.waitForTimeout(600);
  const afterNewTab = await readBrowser();
  log("after +tab:", JSON.stringify(afterNewTab));

  // DevTools toggle flips the data-devtools attribute.
  await browser.locator('.ph-browser-btn[title="DevTools"]').click();
  await page.waitForTimeout(700);
  const devAttr = await browser.evaluate((b) => b.dataset.devtools || "(unset)");
  log("devtools attr:", devAttr);

  // ── close-correctness + island survival ────────────────────────────────────
  // Add a Notizen tile next to the Browser, then close Notizen. Regression checks:
  //  (1) exactly the Notizen tile vanishes (not a neighbor — the reported bug),
  //  (2) the Browser tile stays under the SAME data-pane, and
  //  (3) its live <webview> survives the split collapse (park-and-rehome, not torn out).
  const bPane = await browser.evaluate((b) => b.closest(".ph-tile")?.getAttribute("data-pane"));
  await page.click(".ph-ws-toolbar .ph-add .ph-btn");
  await page.waitForSelector(".ph-menu", { timeout: 5000 });
  await page.click('.ph-menu-item:has-text("Notizen")');
  await page.waitForTimeout(800);
  const readTiles = () =>
    page.$$eval(".ph-tile", (els) =>
      els.map((e) => ({ pane: e.getAttribute("data-pane"), label: e.querySelector(".ph-tile-title")?.textContent })),
    );
  const tilesBefore = await readTiles();
  const notes = tilesBefore.find((t) => t.label === "Notizen");
  log("tiles before close:", JSON.stringify(tilesBefore.map((t) => t.label)));
  await page.click(`.ph-tile[data-pane="${notes.pane}"] .ph-tile-btn[title="schließen"]`);
  await page.waitForTimeout(1000);
  const tilesAfter = await readTiles();
  log("tiles after close:", JSON.stringify(tilesAfter.map((t) => t.label)));
  const closedCorrectTile = !tilesAfter.some((t) => t.pane === notes.pane) && tilesAfter.length === tilesBefore.length - 1;
  const survivorsIntact = tilesBefore
    .filter((t) => t.pane !== notes.pane)
    .every((t) => tilesAfter.some((a) => a.pane === t.pane));
  const browserSurvived = await page.evaluate(
    (pane) => !!document.querySelector(`.ph-tile[data-pane="${pane}"] .ph-browser-view webview`),
    bPane,
  );
  const closeCorrectnessOK = closedCorrectTile && survivorsIntact && browserSurvived;
  log("close-correctness:", JSON.stringify({ closedCorrectTile, survivorsIntact, browserSurvived }));
  const browserOK =
    browserChrome.hasToolbar &&
    browserChrome.hasUrl &&
    browserChrome.hasStatus &&
    browserChrome.hasView &&
    browserChrome.tabsSingle === true &&
    afterNewTab.tabCount === 2 &&
    afterNewTab.tabsSingle === false;
  await page.screenshot({ path: shot("browser") });

  // Clean up: close every non-terminal tile (incl. any left by earlier runs) so the
  // persisted project layout stays idempotent across e2e runs. Terminals (.xterm) are
  // kept; browser/markdown/notes tiles are closed.
  const junkTiles = () => page.locator(".ph-tile:not(:has(.xterm))");
  const before = await junkTiles().count();
  log("non-terminal tiles before cleanup:", before);
  for (let i = 0; i < 24 && (await junkTiles().count()) > 0; i++) {
    await junkTiles()
      .first()
      .locator('.ph-tile-btn[title="schließen"]')
      .click({ force: true, timeout: 3000 })
      .catch(() => {});
    await page.waitForTimeout(500);
  }
  await page.waitForTimeout(700); // let the debounced closeTile persist flush before quit
  const remainingJunk = await junkTiles().count();
  log("non-terminal tiles after cleanup:", remainingJunk);

  // ── drop-hint element present? ─────────────────────────────────────────────
  const hasHint = await page.evaluate(() => !!document.querySelector(".ph-drop-hint"));
  log("drop-hint element exists:", hasHint);

  await page.screenshot({ path: shot("final") });

  // ── report ─────────────────────────────────────────────────────────────────
  console.log("\n=== CONSOLE (renderer) ===");
  for (const m of consoleMsgs.slice(-40)) console.log("  ", m);
  console.log("\n=== RESULT ===");
  console.log("  swatchClickChangedAccent:", changed, `(start ${accentBefore}, end ${accentAfter})`);
  console.log("  terminalHasContent:", termText !== "(no xterm-rows)" && termText.length > 0);
  console.log("  browserChromeOK:", browserOK, `(devtools=${devAttr})`);
  console.log("  closeCorrectnessOK:", closeCorrectnessOK);
  console.log("  dropHintPresent:", hasHint);
  console.log("  screenshots: /tmp/ph-projects.png /tmp/ph-workspace.png /tmp/ph-browser.png /tmp/ph-final.png");

  await app.close();
}

main().catch(async (e) => {
  console.error("DRIVE FAILED:", e.message);
  console.log("=== CONSOLE tail ===");
  for (const m of consoleMsgs.slice(-40)) console.log("  ", m);
  process.exit(1);
});
