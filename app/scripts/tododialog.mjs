// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// E2E driver for two todo-tile complaints:
//
//   1. Reordering todos raised the TILE-rearrange overlay (the translucent half/edge
//      rectangle), as if the tile were about to move — it never did. A reorder must
//      instead show an insertion marker at the drop position.
//   2. Editing happened inline, so in a small tile the form had no room. Editing now
//      opens a dialog that is fixed to the viewport and therefore independent of the
//      tile's size and its overflow clipping.
//
// Run:  cd app && xvfb-run -a node scripts/tododialog.mjs
// Needs: local Passbubble on :8765 with the test account, npm run build:all + make build.

import { _electron as electron } from "playwright";
import { fileURLToPath } from "node:url";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

const appDir = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const configHome = fs.mkdtempSync(path.join(os.tmpdir(), "ph-e2e-"));
const tag = "T" + process.pid;

const log = (...a) => console.log("·", ...a);
const fails = [];
const check = (ok, msg) => (ok ? log("PASS", msg) : (fails.push(msg), log("FAIL", msg)));

/** Drive one HTML5 drag step with a DataTransfer shared across the events. */
const dragStep = (page, fromText, toText, phase, belowMidpoint) =>
  page.evaluate(
    ([from, to, ph, below]) => {
      const rows = [...document.querySelectorAll(".ph-todoitem")];
      const src = rows.find((r) => r.innerText.includes(from));
      const dst = rows.find((r) => r.innerText.includes(to));
      if (!src || !dst) return "row not found";
      window.__dt = window.__dt || new DataTransfer();
      const dt = window.__dt;
      const fire = (el, type, pt) => {
        const ev = new DragEvent(type, { bubbles: true, cancelable: true, dataTransfer: dt, ...pt });
        el.dispatchEvent(ev);
      };
      if (ph === "start") {
        fire(src, "dragstart", {});
        return "ok";
      }
      const r = dst.getBoundingClientRect();
      const pt = { clientX: r.left + r.width / 2, clientY: below ? r.bottom - 3 : r.top + 3 };
      fire(dst, ph === "over" ? "dragover" : "drop", pt);
      if (ph === "drop") fire(src, "dragend", {});
      return "ok";
    },
    [fromText, toText, phase, belowMidpoint],
  );

/** Visible text of the todo rows, top to bottom. */
const rowTexts = (page) =>
  page.evaluate(() => [...document.querySelectorAll(".ph-todoitem")].map((r) => r.innerText.trim().replace(/\s+/g, " ")));

const boxOf = (page, sel) =>
  page.evaluate((s) => {
    const el = document.querySelector(s);
    if (!el) return null;
    const r = el.getBoundingClientRect();
    const st = getComputedStyle(el);
    return { x: r.left, y: r.top, w: r.width, h: r.height, display: st.display, position: st.position };
  }, sel);

async function main() {
  const app = await electron.launch({
    args: ["."],
    cwd: appDir,
    env: { ...process.env, PASSBUBBLE_URL: "http://localhost:8765", XDG_CONFIG_HOME: configHome },
  });
  const page = await app.firstWindow();
  page.on("pageerror", (e) => log("PAGEERROR:", e.message));

  await page.waitForSelector('input[type="email"]', { timeout: 20000 });
  await page.click('input[type="email"]');
  await page.type('input[type="email"]', "test@ph.local", { delay: 50 });
  await page.click('input[type="password"]');
  await page.type('input[type="password"]', "test1234", { delay: 50 });
  await page.click('button:has-text("Entsperren")');
  await page.waitForSelector(".ph-home .ph-proj", { timeout: 20000 });
  await page.locator(".ph-home .ph-proj").first().click();
  await page.waitForSelector(".ph-workspace", { timeout: 20000 });
  await page.waitForTimeout(2500);

  // ── a todo tile to work in ────────────────────────────────────────────────
  // The layout lives in the account, so a previous run's tiles are still here. Close
  // everything down to one, otherwise ".ph-todoinput" is ambiguous (and the Claude
  // session tile happens to reuse that class too).
  for (let guard = 0; guard < 10; guard++) {
    const tiles = await page.locator(".ph-workspace .ph-tile").count();
    if (tiles <= 1) break;
    await page.locator('.ph-workspace .ph-tile .ph-tile-bar button[title="schließen"]').last().click();
    await page.waitForTimeout(700);
  }
  await page.locator('.ph-workspace button:has-text("+ Tile")').click();
  await page.locator('.ph-menu-item:has-text("Todo")').click();
  await page.waitForSelector(".ph-todolist", { timeout: 15000 });
  await page.waitForTimeout(1500);
  // The LAST one is the tile just added — the leftover single tile may be a todo tile
  // too, and scoping to the wrong one makes every selector below lie.
  const pane = await page.evaluate(
    () => [...document.querySelectorAll(".ph-tile:has(.ph-todolist)")].pop()?.dataset.pane || "",
  );
  const T = `.ph-tile[data-pane="${pane}"]`; // scope every selector to THIS tile
  await page.locator(`${T} .ph-todoform button:has-text("+")`).waitFor({ state: "visible", timeout: 15000 });
  log("todo tile open, pane", pane);

  // Sweep todos left behind by earlier runs of this script (tag pattern only).
  for (let i = 0; i < 20; i++) {
    const stale = page.locator(`${T} .ph-todoitem`).filter({ hasText: /T\d{4,}-(eins|zwei|drei|geaendert|verworfen)/ }).first();
    if (!(await stale.count())) break;
    await stale.locator(".ph-todo-del").click();
    await page.waitForTimeout(900);
  }

  // three todos, so "between two rows" is a real position
  const names = [`${tag}-eins`, `${tag}-zwei`, `${tag}-drei`];
  const newInput = `${T} .ph-todoform .ph-todoinput`;
  for (const n of names) {
    await page.locator(newInput).click();
    await page.type(newInput, n, { delay: 40 });
    await page.locator(`${T} .ph-todoform button:has-text("+")`).click();
    await page.waitForTimeout(1800);
  }
  let rows = await rowTexts(page);
  if (rows.filter((r) => r.includes(tag)).length === 0) {
    log("  tile says:", (await page.locator(`${T} .ph-tilecontent`).innerText()).replace(/\s+/g, " ").slice(0, 200));
  }
  const mine = () => rows.filter((r) => r.includes(tag));
  const posOf = (list, name) => list.findIndex((r) => r.includes(`${tag}-${name}`));
  const short = (list) => list.map((r) => r.replace(new RegExp(`.*${tag}-(\\w+).*`), "$1")).join(",");
  check(mine().length === 3, `three todos created (got ${mine().length})`);
  log("order:", short(mine()));

  // ── 1. dragging a todo must NOT raise the tile-rearrange overlay ──────────
  await dragStep(page, `${tag}-eins`, `${tag}-drei`, "start");
  await page.waitForTimeout(200);
  await dragStep(page, `${tag}-eins`, `${tag}-drei`, "over", true);
  await page.waitForTimeout(200);

  const tileHint = await boxOf(page, ".ph-drop-hint");
  const insert = await boxOf(page, ".ph-insert-hint");
  check(tileHint?.display === "none", `tile-rearrange overlay stays hidden (display: ${tileHint?.display})`);
  check(insert?.display === "block", `insertion marker is shown (display: ${insert?.display})`);
  check(insert?.h <= 4 && insert?.w > 40, `the marker is a line, not a rectangle (${insert?.w}×${insert?.h})`);

  // it sits at the edge of the row it would drop onto
  const targetBox = await page.evaluate((t) => {
    const r = [...document.querySelectorAll(".ph-todoitem")].find((e) => e.innerText.includes(t));
    const b = r.getBoundingClientRect();
    return { top: b.top, bottom: b.bottom };
  }, `${tag}-drei`);
  check(
    Math.abs(insert.y - (targetBox.bottom - 1)) < 3,
    `marker sits on the lower edge of the target row (marker ${insert.y.toFixed(0)}, edge ${targetBox.bottom.toFixed(0)})`,
  );

  // dropping in the lower half puts it AFTER that row
  await dragStep(page, `${tag}-eins`, `${tag}-drei`, "drop", true);
  await page.waitForTimeout(2000);
  rows = await rowTexts(page);
  check(
    posOf(mine(), "eins") === posOf(mine(), "drei") + 1,
    `dropped below a row it lands directly after it (order: ${short(mine())})`,
  );
  const after = await boxOf(page, ".ph-insert-hint");
  check(after?.display === "none", "the marker disappears after the drop");

  // dropping in the upper half puts it BEFORE that row
  await dragStep(page, `${tag}-eins`, `${tag}-zwei`, "start");
  await page.waitForTimeout(150);
  await dragStep(page, `${tag}-eins`, `${tag}-zwei`, "over", false);
  await page.waitForTimeout(150);
  await dragStep(page, `${tag}-eins`, `${tag}-zwei`, "drop", false);
  await page.waitForTimeout(2000);
  rows = await rowTexts(page);
  check(
    posOf(mine(), "eins") === posOf(mine(), "zwei") - 1,
    `dropped above a row it lands directly before it (order: ${short(mine())})`,
  );

  // the order must survive leaving and re-entering the project (it is persisted in
  // the vault, not just in the tile's memory)
  const before = short(mine());
  await page.locator('.ph-rail button[title="Projekte (Home)"]').click();
  await page.waitForSelector(".ph-home", { timeout: 15000 });
  await page.waitForTimeout(1200);
  await page.locator(".ph-home .ph-proj").first().click();
  await page.waitForSelector(".ph-todolist", { timeout: 25000 });
  await page.waitForTimeout(3000);
  rows = await rowTexts(page);
  check(short(mine()) === before, `order survived leaving the project (was ${before}, now ${short(mine())})`);

  // ── 2. editing opens a dialog that is independent of the tile ─────────────
  await page.locator(`${T} .ph-todoitem:has-text("${tag}-eins") .ph-todo-edit`).click();
  await page.waitForSelector(".ph-dlg", { timeout: 10000 });
  const dlg = await boxOf(page, ".ph-dlg");
  const tile = await page.evaluate(() => {
    const el = document.querySelector(".ph-tile:has(.ph-todolist)") || document.querySelector(".ph-tile");
    const r = el.getBoundingClientRect();
    return { x: r.left, y: r.top, w: r.width, h: r.height };
  });
  check(dlg !== null, "the edit dialog opens");
  check(await page.locator(".ph-todo-editing").count().then((n) => n === 0), "no inline editor is left in the row");
  const clipped = dlg.x >= tile.x && dlg.y >= tile.y && dlg.x + dlg.w <= tile.x + tile.w && dlg.y + dlg.h <= tile.y + tile.h;
  log(`dialog ${dlg.w.toFixed(0)}×${dlg.h.toFixed(0)} @${dlg.x.toFixed(0)},${dlg.y.toFixed(0)} — tile ${tile.w.toFixed(0)}×${tile.h.toFixed(0)} @${tile.x.toFixed(0)},${tile.y.toFixed(0)}`);
  check(!clipped || dlg.w >= Math.min(400, tile.w), "the dialog is not squeezed into the tile's box");

  // the fields are prefilled and the caret is in the text field
  const val = await page.locator(".ph-dlg-focus").inputValue();
  check(val === `${tag}-eins`, `the dialog is prefilled with the todo's text (got "${val}")`);
  check(
    await page.evaluate(() => document.activeElement?.classList.contains("ph-dlg-focus")),
    "the text field is focused when the dialog opens",
  );

  // Escape closes WITHOUT saving
  await page.locator(".ph-dlg-focus").fill("");
  await page.type(".ph-dlg-focus", `${tag}-verworfen`, { delay: 20 });
  await page.keyboard.press("Escape");
  await page.waitForTimeout(800);
  check(await page.locator(".ph-dlg").count().then((n) => n === 0), "Escape closes the dialog");
  rows = await rowTexts(page);
  check(!mine().join(" ").includes("verworfen"), "a discarded edit is not saved");

  // a real edit: new text + a due date
  await page.locator(`${T} .ph-todoitem:has-text("${tag}-eins") .ph-todo-edit`).click();
  await page.waitForSelector(".ph-dlg", { timeout: 10000 });
  await page.locator(".ph-dlg-focus").fill("");
  await page.type(".ph-dlg-focus", `${tag}-geaendert`, { delay: 20 });
  await page.locator('.ph-dlg-input[type="datetime-local"]').first().fill("2027-03-04T09:30");
  await page.locator('.ph-dlg-foot button:has-text("Speichern")').click();
  await page.waitForTimeout(2200);
  rows = await rowTexts(page);
  check(mine().join(" ").includes(`${tag}-geaendert`), `the edit is applied (order: ${mine().join(" | ")})`);
  check(mine().join(" ").includes("04.03."), "the due date is shown on the row");
  check(await page.locator(".ph-dlg").count().then((n) => n === 0), "saving closes the dialog");

  // ── cleanup: remove the todos this run created ────────────────────────────
  for (let i = 0; i < 6; i++) {
    const row = page.locator(`${T} .ph-todoitem:has-text("${tag}")`).first();
    if (!(await row.count())) break;
    await row.locator(".ph-todo-del").click();
    await page.waitForTimeout(1200);
  }
  rows = await rowTexts(page);
  check(mine().length === 0, `test todos removed again (${mine().length} left)`);

  // and the tile itself
  await page.locator(`${T} .ph-tile-bar button[title="schließen"]`).click().catch(() => {});
  await page.waitForTimeout(1200);

  await app.close();
}

main()
  .then(() => {
    fs.rmSync(configHome, { recursive: true, force: true });
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
