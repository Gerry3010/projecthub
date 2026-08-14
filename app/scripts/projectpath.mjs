// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// E2E driver for per-project settings, specifically the working directory:
// a hand-made project used to be stuck without one (the home form always passed
// localPath ""), and nothing in the UI could add it afterwards.
//
// Checks the whole loop: create → card offers the path → Projekt tab saves it →
// a bad path is refused → the value survives an app restart (i.e. it really went
// into the manifest + RootIndex, not just component state) → clearing it works.
//
// Run:  cd app && xvfb-run -a node scripts/projectpath.mjs
// Needs: local Passbubble on :8765 with the test account, npm run build:all + make build.

import { _electron as electron } from "playwright";
import { fileURLToPath } from "node:url";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

const appDir = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const configHome = fs.mkdtempSync(path.join(os.tmpdir(), "ph-e2e-"));
const workdir = fs.mkdtempSync(path.join(os.tmpdir(), "ph-proj-"));
const workdir2 = fs.mkdtempSync(path.join(os.tmpdir(), "ph-proj2-"));
const title = "E2E Pfad " + process.pid;
// A file only this run knows about, so seeing it proves the tree really reads workdir.
const marker = "ph-marker-" + process.pid + ".txt";
fs.writeFileSync(path.join(workdir, marker), "e2e\n");

const log = (...a) => console.log("·", ...a);
const fails = [];
const check = (ok, msg) => (ok ? log("PASS", msg) : (fails.push(msg), log("FAIL", msg)));

const launch = () =>
  electron.launch({
    args: ["."],
    cwd: appDir,
    env: { ...process.env, PASSBUBBLE_URL: "http://localhost:8765", XDG_CONFIG_HOME: configHome },
  });

async function unlock(app) {
  const page = await app.firstWindow();
  page.on("pageerror", (e) => log("PAGEERROR:", e.message));
  // A relaunch may restore the remembered credentials and unlock on its own, so only
  // drive the form when it is actually there.
  const email = page.locator('input[type="email"]');
  await page.waitForSelector('input[type="email"], .ph-home, .ph-workspace', { timeout: 20000 });
  if (await email.count()) {
    // Typing (not fill) — go-app binds on the input event stream, and a programmatic
    // value set does not reach its handler, so the store would see empty credentials.
    await email.click();
    await page.keyboard.press("Control+A");
    await page.type('input[type="email"]', "test@ph.local", { delay: 50 });
    await page.click('input[type="password"]');
    await page.keyboard.press("Control+A");
    await page.type('input[type="password"]', "test1234", { delay: 50 });
    await page.click('button:has-text("Entsperren")');
  }
  // After a restart the app may come back straight into the last workspace, so wait for
  // either view and then take the rail home.
  await page.waitForSelector(".ph-home, .ph-workspace", { timeout: 20000 }).catch(async (e) => {
    log("unlock stuck — status:", await page.locator(".ph-err, .ph-status").allInnerTexts().catch(() => []));
    throw e;
  });
  await goHome(page);
  return page;
}

/** Return to the projects list from wherever we are (rail home button). */
async function goHome(page) {
  if (await page.locator(".ph-home").count()) return;
  await page.locator('.ph-rail button[title="Projekte (Home)"]').click();
  await page.waitForSelector(".ph-home", { timeout: 10000 });
}

/** The project card created by this run. */
const card = (page) => page.locator(".ph-home .ph-proj", { hasText: title }).first();

/** Open the Projekt settings tab for our project via the card's path button. */
async function openProjectTab(page) {
  await card(page).locator(".ph-proj-meta").click();
  await page.waitForSelector(".ph-settings", { timeout: 10000 });
  await page.waitForSelector('.ph-settings-tab-active:has-text("Projekt")', { timeout: 10000 });
}

async function closeSettings(page) {
  await page.locator('.ph-settings-head button[title="Schließen"]').click();
  await page.waitForTimeout(400);
  await goHome(page);
}

async function setPath(page, value) {
  const input = page.locator(".ph-path-row .ph-set-input");
  await input.click();
  await input.fill(value);
  await page.locator('.ph-path-row button:has-text("Speichern")').click();
  await page.waitForTimeout(1800); // vault round-trip
  return (await page.locator(".ph-settings-note").first().innerText()).trim();
}

async function main() {
  let app = await launch();
  let page = await unlock(app);

  // ── create a project the way a human does: title only, no path ────────────
  await page.locator(".ph-newprj input").fill(title);
  await page.locator('.ph-newprj button:has-text("Anlegen")').click();
  await page.waitForSelector(`.ph-home .ph-proj:has-text("${title}")`, { timeout: 20000 });
  log("project created:", title);

  const meta = (await card(page).locator(".ph-proj-meta").innerText()).trim();
  check(/kein lokaler Pfad/.test(meta), `fresh project shows it has no path (got: "${meta}")`);
  check(
    await card(page).locator(".ph-proj-meta-unset").count(),
    "the empty path reads as a placeholder, not as a real path",
  );

  // ── the card's path opens the project's own settings tab ──────────────────
  await openProjectTab(page);
  check(true, "clicking the path lands on the Projekt tab");
  const pre = await page.locator(".ph-path-row .ph-set-input").inputValue();
  check(pre === "", `path field starts empty (got "${pre}")`);

  // ── a relative path is refused, and nothing is written ────────────────────
  const badMsg = await setPath(page, "relativ/kein/pfad");
  check(/absoluten Pfad/.test(badMsg), `relative path refused (got: "${badMsg}")`);

  // ── a path that does not exist is refused by the sidecar check ────────────
  const goneMsg = await setPath(page, "/nonexistent-" + process.pid + "/nope");
  check(/nicht lesbar/.test(goneMsg), `missing directory refused (got: "${goneMsg}")`);

  // ── a real directory saves ────────────────────────────────────────────────
  const okMsg = await setPath(page, workdir);
  check(okMsg.includes(workdir), `real directory saved (got: "${okMsg}")`);

  // a trailing separator must normalize to the same value, not a second variant
  const trailMsg = await setPath(page, workdir + "/");
  check(trailMsg.includes(workdir) && !trailMsg.includes(workdir + "/"), "trailing slash normalized away");

  // ── the home card reflects it immediately, without a reload ───────────────
  await closeSettings(page);
  const meta2 = (await card(page).locator(".ph-proj-meta").innerText()).trim();
  check(meta2 === workdir, `home card shows the new path at once (got: "${meta2}")`);

  // ── it survives a restart ⇒ it was persisted, not just held in memory ─────
  await app.close();
  app = await launch();
  page = await unlock(app);
  await page.waitForSelector(`.ph-home .ph-proj:has-text("${title}")`, { timeout: 20000 });
  const meta3 = (await card(page).locator(".ph-proj-meta").innerText()).trim();
  check(meta3 === workdir, `path survived a restart (got: "${meta3}")`);

  // ── the open project actually WORKS in that directory ─────────────────────
  // A local file tree rooted at the project's path is the cheapest end-to-end proof:
  // the tile takes its root from LocalPath and lists it through the sidecar, so seeing
  // the marker file means the saved path reached the real filesystem layer.
  await card(page).click();
  await page.waitForSelector(".ph-workspace", { timeout: 20000 });
  await page.waitForTimeout(2000);
  await page.locator('.ph-workspace button:has-text("+ Tile")').click();
  await page.locator('.ph-menu-item:has-text("Dateien (lokal)")').click();
  await page.waitForTimeout(2500);
  const treeText = (await page.locator(".ph-workspace").innerText()).replace(/\s+/g, " ");
  check(treeText.includes(marker), `the project's file tree lists ${marker} from the new path`);

  // ── switching projects must refill the buffer, not show the old path ──────
  await goHome(page);
  const other = page.locator(".ph-home .ph-proj").first();
  if ((await page.locator(".ph-home .ph-proj").count()) > 1) {
    const otherName = (await other.locator(".ph-title").innerText()).trim();
    if (otherName !== title) {
      await other.locator(".ph-proj-meta").click();
      await page.waitForSelector(".ph-settings", { timeout: 10000 });
      const buf = await page.locator(".ph-path-row .ph-set-input").inputValue();
      check(buf !== workdir, `switching to "${otherName}" refills the field (got "${buf}")`);
      await closeSettings(page);
    }
  }

  // ── re-editing to a second directory, then clearing ───────────────────────
  await openProjectTab(page);
  const second = await setPath(page, workdir2);
  check(second.includes(workdir2), `path can be changed again (got: "${second}")`);
  const cleared = await setPath(page, "");
  check(/entfernt/.test(cleared), `path can be cleared (got: "${cleared}")`);
  await closeSettings(page);
  const meta4 = (await card(page).locator(".ph-proj-meta").innerText()).trim();
  check(/kein lokaler Pfad/.test(meta4), `cleared path shows the placeholder again (got: "${meta4}")`);

  // ── cleanup: drop the test project again ──────────────────────────────────
  // Wait out the in-flight reload first — while it runs the delete is a no-op, and the
  // link is disabled for exactly that reason (Playwright's actionability check waits).
  const del = page.locator(`.ph-home .ph-proj:has-text("${title}") .ph-proj-del`);
  await del.click({ timeout: 20000 });
  await page
    .waitForSelector(`.ph-home .ph-proj:has-text("${title}")`, { state: "detached", timeout: 20000 })
    .catch(() => {});
  const left = await page.locator(`.ph-home .ph-proj:has-text("${title}")`).count();
  if (left) log("  delete status:", await page.locator(".ph-err").allInnerTexts().catch(() => []));
  check(left === 0, "test project removed again");

  await app.close();
}

main()
  .then(() => {
    fs.rmSync(configHome, { recursive: true, force: true });
    fs.rmSync(workdir, { recursive: true, force: true });
    fs.rmSync(workdir2, { recursive: true, force: true });
    if (fails.length) {
      console.log("\n" + fails.length + " FAIL(s):\n  " + fails.join("\n  "));
      process.exit(1);
    }
    console.log("\nall checks passed");
  })
  .catch((e) => {
    console.error(e);
    process.exit(1);
  });
