// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// E2E driver for two bugs that both came down to state not reaching the DOM:
//
//   1. Split terminal → the two panes must be SEPARATE shells. The split used to
//      clone pty_id into the new pane, and the sidecar serves one subscriber per
//      PTY session, so the second pane took the session over: you typed into one
//      terminal and it showed up in the other.
//   2. Keyed wrappers must reconcile. go-app skips the update entirely when
//      reflection finds no settable field that differs (node.go updateComponent),
//      so a wrapper with only unexported fields froze at its first render — the
//      session-rename editor never appeared, the focus ring never moved.
//
// Run:  cd app && xvfb-run -a node scripts/tilefocus.mjs
// Needs: local Passbubble on :8765 with the test account, npm run build:all + make build.
//
// Throwaway XDG_CONFIG_HOME, same rationale as multiwindow.mjs: it isolates the
// sidecar's device-local server override AND Electron's userData, so the run talks
// to the local backend and never touches the real profile.

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

/** Pane ids of the live terminal islands, in DOM order. */
const terminalPanes = (page) =>
  page.evaluate(() =>
    [...document.querySelectorAll(".ph-tile")]
      .filter((t) => t.querySelector(".xterm"))
      .map((t) => t.dataset.pane),
  );

/** Visible text of one pane's terminal screen. */
const screenText = (page, pane) =>
  page.evaluate(
    (p) => document.querySelector(`.ph-tile[data-pane="${p}"] .xterm-rows`)?.innerText.replace(/\s+/g, " ").trim() || "",
    pane,
  );

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
  await page.locator(".ph-home .ph-proj").first().click();
  await page.waitForSelector(".ph-workspace .ph-tile", { timeout: 20000 });
  await page.waitForTimeout(2500); // let the default terminal boot its PTY
  log("workspace open");

  // The layout lives in the ACCOUNT (Passbubble), not in the throwaway config dir, so
  // it carries over between runs. Close everything down to one tile, otherwise the
  // split check below would start from whatever the last run left behind.
  for (let guard = 0; guard < 8; guard++) {
    const tiles = await page.locator(".ph-workspace .ph-tile").count();
    if (tiles <= 1) break;
    await page.locator('.ph-workspace .ph-tile .ph-tile-bar button[title="schließen"]').last().click();
    await page.waitForTimeout(700);
  }
  await page.waitForTimeout(1500);
  log("reset to", await page.locator(".ph-workspace .ph-tile").count(), "tile(s)");

  // ── 1. split the terminal → two independent shells ───────────────────────
  const before = await terminalPanes(page);
  check(before.length === 1, `starting from a single terminal tile (${before.length})`);

  await page.locator(`.ph-tile[data-pane="${before[0]}"] .ph-tile-bar button[title="mehr"]`).click();
  await page.locator('.ph-tile-menu button:has-text("Horizontal teilen")').click();
  await page.waitForTimeout(3000); // second PTY spawns + attaches

  const panes = await terminalPanes(page);
  check(panes.length === 2, `split produced a second terminal (${panes.length})`);
  if (panes.length !== 2) {
    await app.close();
    console.log(`\n${fails.length} FAILED:\n- ${fails.join("\n- ")}`);
    process.exit(1);
  }

  // Distinct PTY sessions is the root invariant; prove it behaviourally by typing.
  const marker = "PH_MARKER_" + Date.now();
  await page.locator(`.ph-tile[data-pane="${panes[0]}"] .xterm-screen`).click();
  await page.waitForTimeout(300);
  await page.keyboard.type(`echo ${marker}`, { delay: 25 });
  await page.waitForTimeout(1200);

  const inFirst = await screenText(page, panes[0]);
  const inSecond = await screenText(page, panes[1]);
  check(inFirst.includes(marker), "typing lands in the terminal that was clicked");
  check(!inSecond.includes(marker), "it does NOT leak into the other terminal (separate PTYs)");
  if (!inFirst.includes(marker)) log("  first  screen:", inFirst.slice(-160));
  if (inSecond.includes(marker)) log("  second screen:", inSecond.slice(-160));

  // ── 2. clicking a tile moves the focus ring (nodeView reconciles) ─────────
  const ringOn = (p) =>
    page.evaluate(
      (pane) => document.querySelector(`.ph-tile[data-pane="${pane}"]`)?.classList.contains("ph-tile-focus") ?? false,
      p,
    );
  await page.locator(`.ph-tile[data-pane="${panes[1]}"] .xterm-screen`).click();
  await page.waitForTimeout(600);
  check(await ringOn(panes[1]), "clicked tile gets the focus ring");
  check(!(await ringOn(panes[0])), "the previously focused tile drops its ring");

  // …and the keyboard follows the ring.
  const marker2 = "PH_SECOND_" + Date.now();
  await page.keyboard.type(`echo ${marker2}`, { delay: 25 });
  await page.waitForTimeout(1200);
  check((await screenText(page, panes[1])).includes(marker2), "keyboard follows the selected tile");
  check(!(await screenText(page, panes[0])).includes(marker2), "…and only there");

  // ── 3. session rename shows its editor live (sessionRow reconciles) ──────
  await page.locator(".ph-ws-toolbar button", { hasText: "+" }).first().click();
  await page.locator('.ph-menu button:has-text("Claude-Sessions")').click();
  await page.waitForTimeout(2000);

  const sessionTile = page.locator('.ph-tile:has(.ph-tilecontent input[placeholder*="Session-ID"])').first();
  const addable = sessionTile.locator('button:has-text("+ Hinzufügen")').first();
  if (await addable.count()) {
    await addable.click(); // adopt a discovered session so there is a row to rename
    await page.waitForTimeout(1500);
  }
  const renameBtn = sessionTile.locator('.ph-list button[title="Umbenennen"], .ph-list button:has-text("✎")').first();
  if (await renameBtn.count()) {
    await renameBtn.click();
    await page.waitForTimeout(600);
    const editor = sessionTile.locator(".ph-list input.ph-todoinput").first();
    check((await editor.count()) > 0, "rename click swaps the row into its edit field (live)");

    if (await editor.count()) {
      // The reported symptom was the SAVED name not showing up — the row kept
      // rendering its stale copy of the item. So type a name, save, and read the list.
      const newTitle = "Umbenannt " + Date.now();
      await editor.click();
      await page.keyboard.press("Control+A");
      await page.type(".ph-list input.ph-todoinput", newTitle, { delay: 40 });
      await sessionTile.locator('.ph-list button[title="Speichern"]').first().click();
      await page.waitForTimeout(1200);
      const listText = await sessionTile.locator(".ph-list").first().innerText();
      check(listText.includes(newTitle), `the renamed session shows its new title live ("${newTitle}")`);
      if (!listText.includes(newTitle)) log("  list shows:", listText.replace(/\s+/g, " ").slice(0, 200));
    }
  } else {
    log("SKIP rename check — no saved session row available in this profile");
  }

  await app.close();
  console.log(fails.length ? `\n${fails.length} FAILED:\n- ${fails.join("\n- ")}` : "\nall checks passed");
  process.exit(fails.length ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
