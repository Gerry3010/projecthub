// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// Regression check for "stay signed in" persistence across restarts. The sidecar
// binds a RANDOM loopback port each launch, so the renderer origin (and its
// localStorage) changes every start — the remembered creds must instead survive via
// the origin-independent phSecure store (Electron safeStorage in userData).
//
// Phase A: launch, tick "Angemeldet bleiben", log in → expect workspace + a written
//          secure-store.json.
// Phase B: relaunch (fresh random port) → expect auto-unlock straight to the project
//          list, with NO manual credential entry.
//
// Run:  cd app && xvfb-run -a node scripts/remember.mjs

import { _electron as electron } from "playwright";
import { fileURLToPath } from "node:url";
import * as path from "node:path";
import * as fs from "node:fs";
import * as os from "node:os";

const appDir = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const securePath = path.join(os.homedir(), ".config", "projecthub-desktop", "secure-store.json");
const log = (...a) => console.log("·", ...a);

function launch() {
  return electron.launch({
    args: ["."],
    cwd: appDir,
    env: { ...process.env, PASSBUBBLE_URL: "http://localhost:8765" },
  });
}

async function main() {
  // Start clean so Phase A truly exercises a first-time remember.
  try {
    fs.rmSync(securePath);
  } catch {
    /* absent is fine */
  }

  // ── Phase A: log in with "remember" ticked ──────────────────────────────────
  let app = await launch();
  let page = await app.firstWindow();
  const errs = [];
  page.on("pageerror", (e) => errs.push("PAGEERROR: " + e.message));

  await page.waitForSelector('input[type="email"]', { timeout: 20000 });
  await page.click('input[type="email"]');
  await page.type('input[type="email"]', "test@ph.local", { delay: 60 });
  await page.click('input[type="password"]');
  await page.type('input[type="password"]', "test1234", { delay: 60 });
  // Tick the "Angemeldet bleiben" checkbox.
  await page.check(".ph-check input[type=checkbox]");
  await page.click('button:has-text("Entsperren")');
  await page.waitForSelector(".ph-app .ph-titlebtn", { timeout: 20000 });
  log("Phase A: logged in, project list shown");

  // The remembered creds must now be on disk (encrypted) in the secure store.
  await page.waitForTimeout(600);
  let secureExists = fs.existsSync(securePath);
  let secureKeys = [];
  let encrypted = false;
  if (secureExists) {
    const store = JSON.parse(fs.readFileSync(securePath, "utf8"));
    secureKeys = Object.keys(store);
    encrypted = Object.values(store).every((v) => typeof v === "string" && v.startsWith("enc:"));
    // Plaintext master password must never appear verbatim in the file.
    const raw = fs.readFileSync(securePath, "utf8");
    if (raw.includes("test1234")) log("WARNING: plaintext password found in secure store!");
  }
  log("secure store:", JSON.stringify({ secureExists, secureKeys, encrypted }));
  await app.close();

  // ── Phase B: relaunch → expect auto-unlock (new random port, no typing) ──────
  app = await launch();
  page = await app.firstWindow();
  page.on("pageerror", (e) => errs.push("PAGEERROR: " + e.message));
  // Auto-unlock should skip the login form and land on the project list directly.
  let autoUnlocked = false;
  try {
    await page.waitForSelector(".ph-app .ph-titlebtn", { timeout: 20000 });
    autoUnlocked = true;
  } catch {
    autoUnlocked = false;
  }
  const view = await page.evaluate(() =>
    document.querySelector(".ph-app") ? "projects" : document.querySelector(".ph-center") ? "login" : "?",
  );
  log("Phase B: view =", view, "autoUnlocked =", autoUnlocked);
  await app.close();

  console.log("\n=== RESULT ===");
  console.log("  secureStoreWritten:", secureExists, `(keys: ${secureKeys.join(",")})`);
  console.log("  autoUnlockAfterRestart:", autoUnlocked && view === "projects");
  console.log("  pageErrors:", errs.length ? errs.slice(-5) : "none");
  const ok = secureExists && secureKeys.length >= 2 && autoUnlocked && view === "projects";
  console.log("  REMEMBER_OK:", ok);
  process.exit(ok ? 0 : 1);
}

main().catch((e) => {
  console.error("REMEMBER TEST FAILED:", e.message);
  process.exit(1);
});
