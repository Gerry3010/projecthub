// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// E2E for the in-app Passbubble server field (login screen). The server is a
// device-local, account-independent override the sidecar persists to
// <config>/projecthub/server.url; it must prefill the field, take effect on login,
// and survive a restart (new random port). Both backends here are the same local
// Passbubble (localhost vs 127.0.0.1) so the login still succeeds after the swap.
//
// Run:  cd app && xvfb-run -a node scripts/server.mjs

import { _electron as electron } from "playwright";
import { fileURLToPath } from "node:url";
import * as path from "node:path";
import * as fs from "node:fs";
import * as os from "node:os";

const appDir = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
// phd persists via os.UserConfigDir() → ~/.config/projecthub/server.url (Linux).
const serverFile = path.join(os.homedir(), ".config", "projecthub", "server.url");
// Electron userData holds the "stay signed in" secure store; clear it so a leftover
// remembered login can't auto-unlock past the screen we're testing.
const secureFile = path.join(os.homedir(), ".config", "projecthub-desktop", "secure-store.json");
const log = (...a) => console.log("·", ...a);

function launch() {
  return electron.launch({
    args: ["."],
    cwd: appDir,
    env: { ...process.env, PASSBUBBLE_URL: "http://localhost:8765" },
  });
}

async function readServerField(page) {
  await page.waitForSelector("#ph-email", { timeout: 20000 });
  // Open the "Server" disclosure and read the field value.
  await page.click(".ph-server summary").catch(() => {});
  await page.waitForTimeout(300);
  return page.inputValue("#ph-server").catch(() => "(no field)");
}

async function main() {
  for (const f of [serverFile, secureFile]) {
    try {
      fs.rmSync(f);
    } catch {
      /* absent is fine */
    }
  }

  // ── Phase A: field prefilled from env; change + login persists it ────────────
  let app = await launch();
  let page = await app.firstWindow();
  const prefilled = await readServerField(page);
  log("Phase A: server field prefilled =", prefilled);

  // Swap to an equivalent host and log in (same backend → still unlocks).
  await page.fill("#ph-server", "http://127.0.0.1:8765");
  await page.click("#ph-email");
  await page.type("#ph-email", "test@ph.local", { delay: 60 });
  await page.click("#ph-pw");
  await page.type("#ph-pw", "test1234", { delay: 60 });
  await page.click('button:has-text("Entsperren")');
  const loggedIn = await page
    .waitForSelector(".ph-app .ph-titlebtn", { timeout: 20000 })
    .then(() => true)
    .catch(() => false);
  await page.waitForTimeout(500);
  const persisted = fs.existsSync(serverFile) ? fs.readFileSync(serverFile, "utf8").trim() : "(none)";
  log("Phase A: loggedIn =", loggedIn, "| persisted server.url =", persisted);
  await app.close();

  // ── Phase B: relaunch (new random port) → field shows the persisted value ────
  app = await launch();
  page = await app.firstWindow();
  const afterRestart = await readServerField(page);
  log("Phase B: server field after restart =", afterRestart);
  await app.close();

  console.log("\n=== RESULT ===");
  console.log("  prefilledFromEnv:", prefilled === "http://localhost:8765");
  console.log("  loginAppliedAndPersisted:", loggedIn && persisted === "http://127.0.0.1:8765");
  console.log("  survivesRestart:", afterRestart === "http://127.0.0.1:8765");
  const ok =
    prefilled === "http://localhost:8765" &&
    loggedIn &&
    persisted === "http://127.0.0.1:8765" &&
    afterRestart === "http://127.0.0.1:8765";
  console.log("  SERVER_OK:", ok);
  process.exit(ok ? 0 : 1);
}

main().catch((e) => {
  console.error("SERVER TEST FAILED:", e.message);
  process.exit(1);
});
