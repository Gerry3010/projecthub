// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// E2E driver for the app_info MCP tool — the answer to "is the update actually
// running?". It launches the app, finds the sidecar the way cmd/phmcp does (the
// discovery file), calls app_info over the real HTTP API and checks that every
// section is filled in: the sidecar's own commit, the shipped app.wasm, what the
// Electron shell reported about itself (including its windows), and the live PTYs.
//
// Run:  cd app && xvfb-run -a node scripts/appinfo.mjs
// Needs: local Passbubble on :8765 with the test account, npm run build:all + make build.

import { _electron as electron } from "playwright";
import { fileURLToPath } from "node:url";
import { execSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

const appDir = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const repoDir = path.join(appDir, "..");
const configHome = fs.mkdtempSync(path.join(os.tmpdir(), "ph-e2e-"));
const log = (...a) => console.log("·", ...a);
const fails = [];
const check = (ok, msg) => (ok ? log("PASS", msg) : (fails.push(msg), log("FAIL", msg)));

/** The endpoint file phd writes — the same discovery path cmd/phmcp reads. */
const endpointPath = () => path.join(configHome, "projecthub", "endpoint.json");

async function waitForEndpoint(timeoutMs = 25000) {
  const until = Date.now() + timeoutMs;
  while (Date.now() < until) {
    try {
      return JSON.parse(fs.readFileSync(endpointPath(), "utf8"));
    } catch {
      await new Promise((r) => setTimeout(r, 250));
    }
  }
  throw new Error("sidecar never wrote " + endpointPath());
}

async function callTool(ep, tool, args = {}) {
  const res = await fetch(ep.base + "/native/mcp/call", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: "Bearer " + ep.token,
      // phmcp stamps itself on every call; fake a stamp to prove it is echoed back.
      "X-ProjectHub-Client": JSON.stringify({ version: "e2e", commit: "cafebabe" }),
    },
    body: JSON.stringify({ tool, args }),
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${tool}: ${res.status} ${text}`);
  return JSON.parse(text);
}

async function main() {
  const app = await electron.launch({
    args: ["."],
    cwd: appDir,
    env: { ...process.env, PASSBUBBLE_URL: "http://localhost:8765", XDG_CONFIG_HOME: configHome },
  });
  const page = await app.firstWindow();
  page.on("pageerror", (e) => log("PAGEERROR:", e.message));
  const ep = await waitForEndpoint();
  log("sidecar at", ep.base);
  await page.waitForTimeout(3000); // let the shell post its self-report

  const info = await callTool(ep, "app_info");

  // ── build identity ───────────────────────────────────────────────────────
  const head = execSync("git rev-parse HEAD", { cwd: repoDir, encoding: "utf8" }).trim();
  check(!!info.build?.phd?.version, `sidecar reports a version (${info.build?.phd?.version})`);
  check(
    info.build?.phd?.commit === head,
    `sidecar commit matches the checkout it was built from (${info.build?.phd?.commit?.slice(0, 7)} vs ${head.slice(0, 7)})`,
  );
  check(typeof info.build?.phd?.dirty === "boolean", "the dirty flag is reported");
  check(info.build?.phmcp?.commit === "cafebabe", "the calling bridge's own build is echoed back");
  check(
    !!info.build?.wasm?.sha256 && info.build.wasm.size > 0,
    `the shipped app.wasm is fingerprinted (${info.build?.wasm?.sha256}, ${info.build?.wasm?.size} B)`,
  );

  // ── what the Electron shell knows ────────────────────────────────────────
  check(!!info.app?.electron, `the shell reported its Electron version (${info.app?.electron})`);
  check(Array.isArray(info.app?.windows) && info.app.windows.length >= 1, "open windows are listed");

  // ── runtime + paths ──────────────────────────────────────────────────────
  check(info.runtime?.pid > 0 && info.runtime?.port > 0, `runtime pid/port present (${info.runtime?.pid}/${info.runtime?.port})`);
  check(Array.isArray(info.runtime?.ptys), `live PTYs listed (${info.runtime?.ptys?.length})`);
  check(
    info.runtime?.passbubble_url === "http://localhost:8765",
    `the Passbubble upstream is reported (${info.runtime?.passbubble_url})`,
  );
  check(info.paths?.endpoint_file === endpointPath(), "the discovery path is reported");
  check(!!info.paths?.web_dir, `the served asset dir is reported (${info.paths?.web_dir})`);

  // ── layout_get sees the tree, not just a flat list ───────────────────────
  // The throwaway profile starts logged out, and layout_get needs the vault open.
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
  await page.waitForSelector(".ph-workspace .ph-tile, .ph-home", { timeout: 25000 });
  if (await page.locator(".ph-home .ph-proj").count()) {
    await page.locator(".ph-home .ph-proj").first().click();
    await page.waitForSelector(".ph-workspace .ph-tile", { timeout: 20000 });
    await page.waitForTimeout(1200);
  }
  const layout = await callTool(ep, "layout_get");
  check(!!layout.root, "layout_get returns a tree");
  const node = layout.root;
  check(
    node.pane_id !== undefined || (node.dir && typeof node.ratio === "number"),
    `the root is a tile or a split with a ratio (${node.dir || node.type})`,
  );

  await app.close();
  console.log(fails.length ? `\n${fails.length} FAILED:\n- ${fails.join("\n- ")}` : "\nall checks passed");
  process.exit(fails.length ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
