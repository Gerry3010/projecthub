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
  console.log("  dropHintPresent:", hasHint);
  console.log("  screenshots: /tmp/ph-projects.png /tmp/ph-workspace.png /tmp/ph-final.png");

  await app.close();
}

main().catch(async (e) => {
  console.error("DRIVE FAILED:", e.message);
  console.log("=== CONSOLE tail ===");
  for (const m of consoleMsgs.slice(-40)) console.log("  ", m);
  process.exit(1);
});
