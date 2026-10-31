/* global messenger */
"use strict";
const $ = id => document.getElementById(id);
const DEFAULTS = { port: 47231, key: "", folders: null, intervalMin: 2, notify: false };
let folders = [];

function drawFolders(selected) {
  const box = $("folders");
  box.replaceChildren();
  const def = $("defFolders").checked;
  box.style.opacity = def ? ".5" : "1";
  let acc = null;
  for (const f of folders) {
    if (f.account !== acc) {
      acc = f.account;
      const d = document.createElement("div");
      d.className = "acc";
      d.textContent = acc;
      box.append(d);
    }
    const l = document.createElement("label");
    l.className = "check";
    const c = document.createElement("input");
    c.type = "checkbox";
    c.value = f.key;
    c.disabled = def;
    c.checked = def ? f.sent : selected.includes(f.key);
    l.append(c, document.createTextNode(" " + f.label + (f.sent ? "  (elküldött)" : "")));
    box.append(l);
  }
}

async function load() {
  const cfg = Object.assign({}, DEFAULTS, await messenger.storage.local.get(Object.keys(DEFAULTS)));
  $("key").value = cfg.key;
  $("port").value = cfg.port;
  $("interval").value = cfg.intervalMin;
  $("notify").checked = !!cfg.notify;
  $("defFolders").checked = !cfg.folders;
  folders = await messenger.runtime.sendMessage({ type: "folders" });
  drawFolders(cfg.folders || []);
}

$("defFolders").addEventListener("change", () => {
  const sel = [...document.querySelectorAll("#folders input:checked")].map(c => c.value);
  drawFolders(sel.length ? sel : folders.filter(f => f.sent).map(f => f.key));
});

$("test").addEventListener("click", async () => {
  const out = $("testOut");
  out.className = "small muted";
  out.textContent = "Kapcsolódás…";
  try {
    const r = await messenger.runtime.sendMessage({ type: "test", key: $("key").value.trim(), port: parseInt($("port").value, 10) || 47231 });
    out.className = "small ok";
    out.textContent = "✔ Kapcsolódva: " + r.app + " " + r.version + (r.user ? " – " + r.user : "");
  } catch (e) {
    out.className = "small bad";
    out.textContent = "✖ " + e.message;
  }
});

$("save").addEventListener("click", async () => {
  const port = parseInt($("port").value, 10);
  const interval = parseInt($("interval").value, 10);
  const out = $("saveOut");
  if (!(port >= 1024 && port <= 65535)) { out.className = "small bad"; out.textContent = "A port 1024 és 65535 között lehet."; return; }
  const def = $("defFolders").checked;
  const sel = [...document.querySelectorAll("#folders input:checked")].map(c => c.value);
  if (!def && !sel.length) { out.className = "small bad"; out.textContent = "Válasszon legalább egy mappát."; return; }
  await messenger.storage.local.set({
    key: $("key").value.trim(), port, intervalMin: Math.min(60, Math.max(1, interval || 2)),
    notify: $("notify").checked, folders: def ? null : sel,
  });
  out.className = "small ok";
  out.textContent = "✔ Mentve – a keresés újraindul.";
});

load();
