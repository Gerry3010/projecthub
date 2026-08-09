// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// E2E driver for the multi-window behaviour:
//   1. clicking a project opens it IN PLACE — no second window appears,
//   2. the menu action / shortcut ("Projekt in neuem Fenster öffnen") hands the open
//      project to a new window, which actually SHOWS the project (not the home view),
//   3. the source window falls back to the project list.
//
// Run:  cd app && xvfb-run -a npm run e2e:multiwindow
// Needs: local Passbubble on :8765 with the test account, a built app
//        (npm run build:all) and built web assets (make build).
//
// The run gets a throwaway XDG_CONFIG_HOME: that isolates BOTH the sidecar's
// device-local server override (<config>/projecthub/server.url — which on a real
// machine points at the production Passbubble) and Electron's userData (remembered
// login), so the test talks to the local backend and never touches the real profile.

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

/** What a renderer currently shows: the workspace of a project, or the home list. */
const viewOf = (page) =>
  page.evaluate(() => ({
    view: document.querySelector(".ph-workspace")
      ? "workspace"
      : document.querySelector(".ph-home")
        ? "home"
        : document.querySelector(".ph-center")
          ? "login"
          : "?",
    title: document.querySelector(".ph-ws-title, .ph-tile-title")?.textContent || "",
    project: window.phWindow?.projectId || "",
  }));

async function main() {
  const app = await electron.launch({
    args: ["."],
    cwd: appDir,
    env: { ...process.env, PASSBUBBLE_URL: "http://localhost:8765", XDG_CONFIG_HOME: configHome },
  });

  const page = await app.firstWindow();
  page.on("pageerror", (e) => log("PAGEERROR:", e.message));

  // ── login ────────────────────────────────────────────────────────────────
  await page.waitForSelector('input[type="email"]', { timeout: 20000 });
  await page.click('input[type="email"]');
  await page.type('input[type="email"]', "test@ph.local", { delay: 60 });
  await page.click('input[type="password"]');
  await page.type('input[type="password"]', "test1234", { delay: 60 });
  // "Stay signed in" is how the desktop is actually used — and it is the path a new
  // window takes to its project: auto-unlock, then select the pending project.
  await page.check('.ph-check input[type="checkbox"]');
  await page.click('button:has-text("Entsperren")');
  await page.waitForSelector(".ph-home .ph-proj", { timeout: 20000 });
  const name = await page.locator(".ph-home .ph-proj .ph-title").first().textContent();
  log("logged in; first project:", name);

  // ── 1. click a project → opens in place, no new window ────────────────────
  await page.locator(".ph-home .ph-proj").first().click();
  await page.waitForTimeout(1500);
  check((await viewOf(page)).view === "workspace", "click opens the project in the same window");
  check(app.windows().length === 1, `no window spawned by the click (windows=${app.windows().length})`);

  // ── 2. menu action → the project moves into a window of its own ───────────
  await app.evaluate(({ BrowserWindow }) => {
    BrowserWindow.getAllWindows()[0].webContents.send("ph-menu", { op: "new-window" });
  });
  const second = await app.waitForEvent("window", { timeout: 15000 });
  await second.waitForLoadState("domcontentloaded");
  log("second window url:", second.url());

  // It has to reach the project on its own (fresh renderer → auto-unlock → project).
  await second.waitForSelector(".ph-workspace", { timeout: 30000 }).catch(() => {});
  const secondView = await viewOf(second);
  check(secondView.view === "workspace", `new window shows the project (view=${secondView.view})`);
  check(secondView.project !== "", `new window knows its project id (${secondView.project})`);

  // ── 3. the source window let the project go ───────────────────────────────
  await page.waitForTimeout(1500);
  const firstView = await viewOf(page);
  check(firstView.view === "home", `source window returned to the project list (view=${firstView.view})`);

  // ── 4. the menu item exists with an accelerator ───────────────────────────
  const item = await app.evaluate(({ Menu }) => {
    const file = Menu.getApplicationMenu()?.items.find((i) => i.label === "Datei");
    const entry = file?.submenu?.items.find((i) => i.label?.includes("neuem Fenster"));
    return entry ? { label: entry.label, accelerator: entry.accelerator } : null;
  });
  check(!!item, `menu item present: ${JSON.stringify(item)}`);
  check(item?.accelerator?.endsWith("+Shift+N") === true, `accelerator is <mod>+Shift+N (${item?.accelerator})`);

  await app.close();
  console.log(fails.length ? `\n${fails.length} FAILED:\n- ${fails.join("\n- ")}` : "\nall checks passed");
  process.exit(fails.length ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
