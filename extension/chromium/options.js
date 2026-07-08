// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

const el = document.getElementById("status");

function render(status) {
  if (!status) {
    el.className = "status muted";
    el.textContent = "Noch keine Verbindung — App gestartet und Host installiert?";
    return;
  }
  const when = new Date(status.at).toLocaleTimeString();
  if (status.ok) {
    el.className = "status ok";
    el.textContent = `Verbunden · zuletzt synchronisiert um ${when}`;
  } else {
    el.className = "status err";
    el.textContent = `Nicht verbunden (${status.error || "unbekannt"}) · Stand ${when}`;
  }
}

function refresh() {
  chrome.storage.local.get("status", (data) => render(data.status));
}

document.getElementById("refresh").addEventListener("click", () => {
  // Poke the worker; it re-syncs and writes a fresh status.
  chrome.runtime.sendMessage({ type: "ping" }).catch(() => {});
  setTimeout(refresh, 500);
});

chrome.storage.onChanged.addListener(refresh);
refresh();
