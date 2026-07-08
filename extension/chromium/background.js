// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// ProjectHub "Live Tabs" background service worker. Two jobs:
//   1. Report only the tab GROUPS the user has coupled to a project (via the popup) to
//      the ProjectHub sidecar, through the native-messaging host (cmd/tabhost) — never
//      the browser's full tab list, so a browser with hundreds of tabs stays cheap.
//   2. Execute commands ProjectHub sends back (focus a tab, focus/reopen a group) when
//      they arrive unsolicited over the same native port — this is how the "öffnen"
//      button and tab clicks in the ProjectHub tile reach into a running browser that
//      doesn't have the popup open.
//
// Coupling state (which group belongs to which project) lives in
// chrome.storage.local under "couplings": { [groupTitle]: projectId }. The popup reads
// and writes it directly; this worker just reacts to storage.onChanged to re-sync.
//
// Design notes for MV3: the service worker can be terminated when idle, so we (a) hold
// an open native port — a connected port keeps the worker alive and event listeners
// live — and (b) register a chrome.alarms heartbeat that both wakes the worker and
// refreshes the sidecar's TTL when the user is idle. Every relevant tab/group event
// triggers a debounced re-sync.

const HOST = "net.geraldhofbauer.projecthub.tabs";
const DEBOUNCE_MS = 300;
const HEARTBEAT = "ph-heartbeat";

let port = null;
let debounceTimer = null;
let myBrowser = null;

// ─── native port ──────────────────────────────────────────────────────────────

function connect() {
  if (port) return port;
  try {
    port = chrome.runtime.connectNative(HOST);
  } catch (e) {
    setStatus(false, String(e));
    port = null;
    return null;
  }
  port.onMessage.addListener(handleHostMessage);
  port.onDisconnect.addListener(() => {
    const err = chrome.runtime.lastError;
    setStatus(false, err ? err.message : "disconnected");
    port = null; // reconnect lazily on the next sync
  });
  return port;
}

function setStatus(ok, error) {
  chrome.storage.local.set({ status: { ok, error: error || "", at: Date.now() } });
}

// handleHostMessage dispatches tabhost's typed replies/pushes: "ack" for a tabs
// report, "projects" for a roster fetch, "command" for an unsolicited ProjectHub
// request to act on a tab/group.
function handleHostMessage(msg) {
  if (!msg || !msg.type) return;
  switch (msg.type) {
    case "ack":
      setStatus(!!msg.ok, msg.error || "");
      break;
    case "projects":
      if (msg.ok) chrome.storage.local.set({ roster: msg.data || [] });
      break;
    case "command":
      runCommand(msg);
      break;
  }
}

// ─── browser detection (best effort, cached) ────────────────────────────────────

async function detectBrowser() {
  try {
    if (navigator.brave && (await navigator.brave.isBrave())) return "brave";
  } catch (_) {}
  const ua = navigator.userAgent || "";
  if (ua.includes("Edg/")) return "edge";
  if (ua.includes("OPR/")) return "opera";
  if (ua.includes("Vivaldi")) return "vivaldi";
  if (ua.includes("Chrome")) return "chrome";
  return "chromium";
}

async function myBrowserID() {
  if (!myBrowser) myBrowser = await detectBrowser();
  return myBrowser;
}

// ─── report coupled groups ────────────────────────────────────────────────────

function scheduleSync() {
  if (debounceTimer) clearTimeout(debounceTimer);
  debounceTimer = setTimeout(syncTabs, DEBOUNCE_MS);
}

// coupledGroupPayloads reads the popup-maintained coupling map and returns wire-ready
// group payloads for only the groups that are coupled — an uncoupled group (the vast
// majority, for a heavy tab user) never leaves the browser.
async function coupledGroupPayloads() {
  const { couplings = {} } = await chrome.storage.local.get("couplings");
  const groups = await chrome.tabGroups.query({});
  const out = [];
  for (const g of groups) {
    const projectId = couplings[g.title];
    if (!projectId) continue;
    const tabs = await chrome.tabs.query({ groupId: g.id });
    out.push({
      project_id: projectId,
      group_key: g.title,
      title: g.title,
      color: g.color,
      group_id: g.id,
      tabs: tabs
        .filter((t) => t.url)
        .map((t) => ({
          url: t.url,
          title: t.title || "",
          fav_icon_url: t.favIconUrl || "",
          tab_id: t.id,
          window_id: t.windowId || 0,
          active: !!t.active,
          pinned: !!t.pinned,
        })),
    });
  }
  return out;
}

async function syncTabs() {
  debounceTimer = null;
  const p = connect();
  if (!p) return;
  const browser = await myBrowserID();
  const groups = await coupledGroupPayloads();
  try {
    p.postMessage({ type: "tabs", payload: { browser, groups } });
  } catch (e) {
    setStatus(false, String(e));
    port = null; // force reconnect next time
  }
}

function requestRoster() {
  const p = connect();
  if (!p) return;
  try {
    p.postMessage({ type: "getProjects" });
  } catch (e) {
    setStatus(false, String(e));
    port = null;
  }
}

// ─── commands pushed from ProjectHub ──────────────────────────────────────────

async function runCommand(cmd) {
  const browser = await myBrowserID();
  if (cmd.browser !== browser) return; // this command targets a different browser instance
  try {
    if (cmd.action === "focusTab") {
      await chrome.tabs.update(cmd.tab_id, { active: true });
      if (cmd.window_id) await chrome.windows.update(cmd.window_id, { focused: true });
    } else if (cmd.action === "focusGroup" || cmd.action === "openGroup") {
      await focusOrReopenGroup(cmd);
    } else if (cmd.action === "createGroup") {
      await createGroup(cmd);
    } else if (cmd.action === "deleteGroup") {
      await deleteGroup(cmd);
    } else if (cmd.action === "renameGroup") {
      await renameGroup(cmd);
    } else if (cmd.action === "recolorGroup") {
      await chrome.tabGroups.update(cmd.group_id, { color: cmd.color });
    } else if (cmd.action === "addTab") {
      await addTab(cmd);
    } else if (cmd.action === "removeTab") {
      await chrome.tabs.remove(cmd.tab_id);
    }
  } catch (e) {
    // best-effort — ProjectHub falls back to opening the URL directly if this fails
    console.warn("[ProjectHub] command failed:", cmd, e);
  }
}

// focusOrReopenGroup surfaces the group if it's still open (focus its window, expand
// it, activate a tab), or reopens it from the command's URLs if it's gone.
async function focusOrReopenGroup(cmd) {
  try {
    const group = await chrome.tabGroups.get(cmd.group_id);
    await chrome.tabGroups.update(group.id, { collapsed: false });
    await chrome.windows.update(group.windowId, { focused: true });
    const tabs = await chrome.tabs.query({ groupId: group.id });
    if (tabs.length) await chrome.tabs.update(tabs[0].id, { active: true });
    return;
  } catch (_) {
    // group id no longer exists — fall through to reopening from URLs
  }
  const urls = cmd.urls || [];
  if (!urls.length) return;
  const created = [];
  for (const url of urls) {
    const tab = await chrome.tabs.create({ url, active: false });
    created.push(tab.id);
  }
  const groupId = await chrome.tabs.group({ tabIds: created });
  await chrome.tabGroups.update(groupId, { title: cmd.group_key || "" });
  const tabs = await chrome.tabs.query({ groupId });
  if (tabs.length) await chrome.tabs.update(tabs[0].id, { active: true });
}

// ─── tab-group management (ProjectHub app → extension) ───────────────────────────

async function setCouplingRaw(groupTitle, projectId) {
  const { couplings = {} } = await chrome.storage.local.get("couplings");
  if (projectId) {
    couplings[groupTitle] = projectId;
  } else {
    delete couplings[groupTitle];
  }
  await chrome.storage.local.set({ couplings });
}

// createGroup makes a brand-new tab group (one blank tab if no URLs were given) and
// immediately couples it to the requesting ProjectHub project, so it shows up in the
// tile on the very next sync.
async function createGroup(cmd) {
  const urls = cmd.urls && cmd.urls.length ? cmd.urls : [undefined];
  const created = [];
  for (const url of urls) {
    const tab = await chrome.tabs.create({ url, active: false });
    created.push(tab.id);
  }
  const groupId = await chrome.tabs.group({ tabIds: created });
  const title = cmd.title || "";
  await chrome.tabGroups.update(groupId, { title, color: cmd.color || "grey" });
  if (title && cmd.project_id) await setCouplingRaw(title, cmd.project_id);
}

// deleteGroup closes every tab in the group (real "gone", not just ungrouping) and
// drops its coupling.
async function deleteGroup(cmd) {
  const tabs = await chrome.tabs.query({ groupId: cmd.group_id });
  if (tabs.length) await chrome.tabs.remove(tabs.map((t) => t.id));
  if (cmd.group_key) await setCouplingRaw(cmd.group_key, null);
}

// renameGroup updates the group's title and migrates its coupling key (coupling is
// title-based), so the rename doesn't silently decouple the group.
async function renameGroup(cmd) {
  await chrome.tabGroups.update(cmd.group_id, { title: cmd.title || "" });
  if (!cmd.group_key || !cmd.title || cmd.group_key === cmd.title) return;
  const { couplings = {} } = await chrome.storage.local.get("couplings");
  if (!(cmd.group_key in couplings)) return;
  couplings[cmd.title] = couplings[cmd.group_key];
  delete couplings[cmd.group_key];
  await chrome.storage.local.set({ couplings });
}

// addTab opens a new tab (blank if no URL given) inside an existing group.
async function addTab(cmd) {
  const tab = await chrome.tabs.create({ url: cmd.url || undefined, active: false });
  await chrome.tabs.group({ groupId: cmd.group_id, tabIds: [tab.id] });
}

// ─── popup messaging ────────────────────────────────────────────────────────────

// The popup talks to Chrome's tab/group/storage APIs directly (same permissions as
// this worker); the only thing it needs from here is the background-cached roster.
chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (!msg || msg.type !== "getRoster") return false;
  chrome.storage.local.get("roster", (data) => sendResponse(data.roster || []));
  requestRoster(); // refresh in the background for next time
  return true; // sendResponse is async
});

// Re-sync the moment the popup couples/uncouples a group.
chrome.storage.onChanged.addListener((changes, area) => {
  if (area === "local" && changes.couplings) scheduleSync();
});

// ─── wiring ─────────────────────────────────────────────────────────────────────

const t = chrome.tabs;
for (const ev of [t.onCreated, t.onRemoved, t.onUpdated, t.onActivated, t.onMoved, t.onAttached, t.onDetached, t.onReplaced]) {
  if (ev) ev.addListener(scheduleSync);
}
for (const ev of [chrome.windows.onCreated, chrome.windows.onRemoved, chrome.windows.onFocusChanged]) {
  if (ev) ev.addListener(scheduleSync);
}
if (chrome.tabGroups) {
  for (const ev of [chrome.tabGroups.onUpdated, chrome.tabGroups.onRemoved, chrome.tabGroups.onMoved]) {
    if (ev) ev.addListener(scheduleSync);
  }
}

chrome.runtime.onStartup.addListener(() => {
  syncTabs();
  requestRoster();
});
chrome.runtime.onInstalled.addListener(() => {
  syncTabs();
  requestRoster();
});

// Heartbeat: keep the sidecar's TTL fresh and wake the worker if it was suspended.
chrome.alarms.create(HEARTBEAT, { periodInMinutes: 0.5 });
chrome.alarms.onAlarm.addListener((a) => {
  if (a.name === HEARTBEAT) {
    syncTabs();
    requestRoster();
  }
});

// Kick off immediately on worker start.
syncTabs();
requestRoster();
