// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// ProjectHub extension popup: couple Chrome tab groups to a ProjectHub project (a
// per-group project <select>), and show/act on the already-coupled groups as a
// mini-dashboard below. Reads/writes chrome.storage and chrome.tabGroups/chrome.tabs
// directly — the only round-trip through the background worker is fetching the
// (background-cached) project roster, since that's what talks to the sidecar.

const groupsEl = document.getElementById("groups");
const dashEl = document.getElementById("dashboard");
const statusEl = document.getElementById("status");

function getRoster() {
  return new Promise((resolve) => {
    chrome.runtime.sendMessage({ type: "getRoster" }, (roster) => resolve(roster || []));
  });
}

async function getCouplings() {
  const { couplings = {} } = await chrome.storage.local.get("couplings");
  return couplings;
}

async function setCoupling(groupKey, projectId) {
  const couplings = await getCouplings();
  if (projectId) {
    couplings[groupKey] = projectId;
  } else {
    delete couplings[groupKey];
  }
  await chrome.storage.local.set({ couplings });
}

async function loadGroups() {
  const groups = await chrome.tabGroups.query({});
  return Promise.all(groups.map(async (g) => ({ ...g, tabs: await chrome.tabs.query({ groupId: g.id }) })));
}

function projectTitle(roster, id) {
  const p = roster.find((r) => r.id === id);
  return p ? p.title : "";
}

function chromeGroupColor(name) {
  const map = {
    grey: "#5f6368", blue: "#1a73e8", red: "#d93025", yellow: "#f9ab00",
    green: "#188038", pink: "#d01884", purple: "#9334e6", cyan: "#007b83", orange: "#e8710a",
  };
  return map[name] || "#888";
}

function chip(color) {
  const el = document.createElement("span");
  el.className = "chip";
  el.style.background = chromeGroupColor(color);
  return el;
}

async function render() {
  const [groups, roster] = await Promise.all([loadGroups(), getRoster()]);
  const couplings = await getCouplings();
  renderCoupling(groups, roster, couplings);
  renderDashboard(groups, roster, couplings);
}

function renderCoupling(groups, roster, couplings) {
  if (!groups.length) {
    groupsEl.innerHTML = '<p class="muted">Keine Tab-Gruppen im Browser offen.</p>';
    return;
  }
  groupsEl.innerHTML = "";
  for (const g of groups) {
    const row = document.createElement("div");
    row.className = "group-row";
    row.appendChild(chip(g.color));

    const title = document.createElement("span");
    title.className = "gtitle";
    title.textContent = g.title || "(ohne Titel)";
    row.appendChild(title);

    const count = document.createElement("span");
    count.className = "gcount";
    count.textContent = String(g.tabs.length);
    row.appendChild(count);

    const select = document.createElement("select");
    const noneOpt = document.createElement("option");
    noneOpt.value = "";
    noneOpt.textContent = "— keine Kopplung —";
    select.appendChild(noneOpt);
    for (const p of roster) {
      const opt = document.createElement("option");
      opt.value = p.id;
      opt.textContent = p.title;
      if (couplings[g.title] === p.id) opt.selected = true;
      select.appendChild(opt);
    }
    select.addEventListener("change", async () => {
      await setCoupling(g.title, select.value || null);
      render();
    });
    row.appendChild(select);

    groupsEl.appendChild(row);
  }
}

function renderDashboard(groups, roster, couplings) {
  const coupled = groups.filter((g) => couplings[g.title]);
  if (!coupled.length) {
    dashEl.innerHTML = '<p class="muted">Noch keine Gruppe gekoppelt.</p>';
    return;
  }
  dashEl.innerHTML = "";
  for (const g of coupled) {
    const box = document.createElement("div");
    box.className = "dash-group";

    const head = document.createElement("div");
    head.className = "dash-head";
    head.appendChild(chip(g.color));

    const title = document.createElement("span");
    title.className = "gtitle";
    title.textContent = g.title || "(ohne Titel)";
    head.appendChild(title);

    const proj = document.createElement("span");
    proj.className = "proj";
    proj.textContent = projectTitle(roster, couplings[g.title]);
    head.appendChild(proj);

    const openBtn = document.createElement("button");
    openBtn.className = "dash-open";
    openBtn.textContent = "öffnen";
    openBtn.addEventListener("click", () => openGroup(g));
    head.appendChild(openBtn);

    box.appendChild(head);

    for (const t of g.tabs) {
      const row = document.createElement("div");
      row.className = "tab-row";
      const icon = document.createElement("img");
      icon.src = t.favIconUrl || "icons/icon16.png";
      icon.alt = "";
      row.appendChild(icon);
      const label = document.createElement("span");
      label.textContent = t.title || t.url;
      row.appendChild(label);
      row.addEventListener("click", () => focusTab(t));
      box.appendChild(row);
    }

    dashEl.appendChild(box);
  }
}

async function focusTab(tab) {
  await chrome.tabs.update(tab.id, { active: true });
  await chrome.windows.update(tab.windowId, { focused: true });
  window.close();
}

async function openGroup(g) {
  await chrome.tabGroups.update(g.id, { collapsed: false });
  await chrome.windows.update(g.windowId, { focused: true });
  if (g.tabs.length) await chrome.tabs.update(g.tabs[0].id, { active: true });
  window.close();
}

render();

chrome.storage.local.get("status", (data) => {
  if (!data.status) {
    statusEl.textContent = "Noch keine Verbindung zur App.";
    return;
  }
  const when = new Date(data.status.at).toLocaleTimeString();
  statusEl.textContent = data.status.ok
    ? `Verbunden · ${when}`
    : `Nicht verbunden (${data.status.error || "unbekannt"}) · ${when}`;
});
