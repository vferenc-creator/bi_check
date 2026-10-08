/* BI Output Monitor – UI logic (vanilla JS, no build step). */
"use strict";

// ---------------------------------------------------------------------------
// Bridge to the Go backend: WebView2 postMessage, or HTTP in the dev server.
// ---------------------------------------------------------------------------
const bridge = (() => {
  const wv = window.chrome && window.chrome.webview;
  const pending = new Map();
  const listeners = {};
  let seq = 0;
  window.__rpc = (id, ok, data) => {
    const p = pending.get(id);
    if (!p) return;
    pending.delete(id);
    ok ? p.resolve(data) : p.reject(new Error(data));
  };
  window.__push = (event, data) => (listeners[event] || []).forEach(fn => fn(data));
  if (!wv) {
    try {
      const es = new EventSource("/events");
      es.onmessage = e => { const m = JSON.parse(e.data); window.__push(m.event, m.data); };
    } catch (_) { /* ignore */ }
  }
  return {
    call(method, params) {
      if (wv) {
        return new Promise((resolve, reject) => {
          const id = ++seq;
          pending.set(id, { resolve, reject });
          wv.postMessage(JSON.stringify({ id, method, params: params === undefined ? null : params }));
        });
      }
      return fetch("/rpc", { method: "POST", body: JSON.stringify({ method, params: params === undefined ? null : params }) })
        .then(r => r.json())
        .then(r => { if (!r.ok) throw new Error(r.error); return r.data; });
    },
    on(event, fn) { (listeners[event] = listeners[event] || []).push(fn); },
  };
})();
const api = (m, p) => bridge.call(m, p);

// ---------------------------------------------------------------------------
// Tiny DOM helpers
// ---------------------------------------------------------------------------
function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k === "style" && typeof v === "object") Object.assign(el.style, v);
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "html") el.innerHTML = v;
    else if (k in el && k !== "list" && typeof v !== "string") el[k] = v;
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat(Infinity)) {
    if (c === undefined || c === null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}
const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
function clear(el) { while (el.firstChild) el.removeChild(el.firstChild); return el; }

// Inline SVG icons (stroke style, 24×24).
const ICONS = {
  refresh: '<path d="M20 11a8 8 0 1 0-2.3 5.7"/><path d="M20 4v7h-7"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  list: '<path d="M8 6h13M8 12h13M8 18h13"/><circle cx="3.5" cy="6" r="1"/><circle cx="3.5" cy="12" r="1"/><circle cx="3.5" cy="18" r="1"/>',
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  file: '<path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><path d="M14 3v6h6"/>',
  gear: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 8h.01M11 12h1v5h1"/>',
  edit: '<path d="M4 20h4L19 9l-4-4L4 16z"/><path d="m13.5 6.5 4 4"/>',
  copy: '<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h8"/>',
  trash: '<path d="M4 7h16M10 11v6M14 11v6M5 7l1 12a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2l1-12M9 7V4h6v3"/>',
  power: '<path d="M12 3v9"/><path d="M6.3 7.5a8 8 0 1 0 11.4 0"/>',
  dots: '<circle cx="5" cy="12" r="1.3"/><circle cx="12" cy="12" r="1.3"/><circle cx="19" cy="12" r="1.3"/>',
  close: '<path d="M6 6l12 12M18 6 6 18"/>',
  bell: '<path d="M6 8a6 6 0 1 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10 21h4"/>',
  bellOff: '<path d="M6 8a6 6 0 0 1 9.3-5M18 8c0 7 3 9 3 9H9"/><path d="M10 21h4M3 3l18 18"/>',
  check: '<path d="m5 12 5 5 9-10"/>',
  upload: '<path d="M12 16V4M7 9l5-5 5 5"/><path d="M4 16v3a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3"/>',
  download: '<path d="M12 4v12M7 11l5 5 5-5"/><path d="M4 16v3a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3"/>',
  calendar: '<rect x="3" y="5" width="18" height="16" rx="2"/><path d="M3 10h18M8 3v4M16 3v4"/>',
  users: '<circle cx="9" cy="8" r="3.5"/><path d="M2.5 20a6.5 6.5 0 0 1 13 0"/><path d="M16 4.5a3.5 3.5 0 0 1 0 7M18 14a6.5 6.5 0 0 1 3.5 6"/>',
  mail: '<rect x="3" y="5" width="18" height="14" rx="2"/><path d="m3 7 9 6 9-6"/>',
  ack: '<path d="M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z"/><path d="m8 12 3 3 5-6"/>',
  open: '<path d="M14 4h6v6M20 4l-9 9"/><path d="M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5"/>',
  test: '<path d="M9 3h6M10 3v6l-5 9a2 2 0 0 0 1.7 3h10.6a2 2 0 0 0 1.7-3l-5-9V3"/>',
};
function icon(name, cls) {
  const s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  s.setAttribute("viewBox", "0 0 24 24");
  s.setAttribute("width", "18");
  s.setAttribute("height", "18");
  s.setAttribute("fill", "none");
  s.setAttribute("stroke", "currentColor");
  s.setAttribute("stroke-width", "1.8");
  s.setAttribute("stroke-linecap", "round");
  s.setAttribute("stroke-linejoin", "round");
  if (cls) s.setAttribute("class", cls);
  s.innerHTML = ICONS[name] || "";
  return s;
}

function toast(msg, isErr) {
  const t = h("div", { class: "toast" + (isErr ? " err" : "") }, msg);
  $("#toasts").append(t);
  setTimeout(() => t.remove(), isErr ? 7000 : 3500);
}
function fail(e) { console.error(e); toast(e && e.message ? e.message : String(e), true); }

// ---------------------------------------------------------------------------
// Formatting
// ---------------------------------------------------------------------------
const STATUS = {
  ok: "OK", waiting: "Várakozik", late: "Késik", missing: "Hiányzik",
  unreachable: "Elérhetetlen", suspicious: "Gyanús", disabled: "Kikapcsolva", unknown: "Ismeretlen",
};
const STATUS_ORDER = ["missing", "unreachable", "suspicious", "late", "unknown", "waiting", "ok", "disabled"];
function pill(status, text) {
  return h("span", { class: "pill st-" + status }, h("span", { class: "dot" }), text || STATUS[status] || status);
}
const pad = n => String(n).padStart(2, "0");
function parseT(s) { if (!s || s.startsWith("0001-")) return null; const d = new Date(s); return isNaN(d) ? null : d; }
function fmtDateTime(s, withSec) {
  const d = typeof s === "string" ? parseT(s) : s;
  if (!d) return "–";
  const now = new Date();
  const time = pad(d.getHours()) + ":" + pad(d.getMinutes()) + (withSec ? ":" + pad(d.getSeconds()) : "");
  const day = new Date(d); day.setHours(0, 0, 0, 0);
  const today = new Date(now); today.setHours(0, 0, 0, 0);
  const diff = Math.round((day - today) / 86400000);
  if (diff === 0) return "ma " + time;
  if (diff === -1) return "tegnap " + time;
  if (diff === 1) return "holnap " + time;
  return d.getFullYear() + "." + pad(d.getMonth() + 1) + "." + pad(d.getDate()) + ". " + time;
}
function fmtFull(s) {
  const d = typeof s === "string" ? parseT(s) : s;
  if (!d) return "–";
  return d.getFullYear() + "." + pad(d.getMonth() + 1) + "." + pad(d.getDate()) + ". " + pad(d.getHours()) + ":" + pad(d.getMinutes()) + ":" + pad(d.getSeconds());
}
function fmtDur(sec) {
  if (sec === null || sec === undefined) return "–";
  const neg = sec < 0; sec = Math.abs(Math.round(sec));
  let out;
  if (sec < 60) out = sec + " mp";
  else if (sec < 3600) out = Math.round(sec / 60) + " perc";
  else if (sec < 86400) { const hh = Math.floor(sec / 3600), mm = Math.round((sec % 3600) / 60); out = hh + " ó" + (mm ? " " + mm + " p" : ""); }
  else { const dd = Math.floor(sec / 86400), hh = Math.round((sec % 86400) / 3600); out = dd + " nap" + (hh ? " " + hh + " ó" : ""); }
  return (neg ? "-" : "") + out;
}
function fmtSize(b) {
  if (b === null || b === undefined || b < 0) return "–";
  if (b < 1024) return b + " B";
  const u = ["KB", "MB", "GB", "TB"]; let i = -1;
  do { b /= 1024; i++; } while (b >= 1024 && i < u.length - 1);
  return b.toFixed(b < 10 ? 1 : 0).replace(".", ",") + " " + u[i];
}
function relTime(s) {
  const d = parseT(s); if (!d) return "–";
  const sec = (Date.now() - d.getTime()) / 1000;
  if (sec < 0) return "ennyi múlva: " + fmtDur(-sec);
  if (sec < 45) return "épp most";
  return fmtDur(sec) + " ezelőtt";
}

// ---------------------------------------------------------------------------
// State & views
// ---------------------------------------------------------------------------
const S = {
  boot: null,
  settings: null,
  view: "items",       // items | settings | about
  group: "",           // "" = all
  statusFilter: "",    // status key or ""
  search: "",
  items: [],           // [{item, state}]
  summary: {},
  selected: null,
};
const views = {};

function applyTheme() {
  const t = (S.settings && S.settings.theme) || "system";
  const dark = t === "dark" || (t === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
}
window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", applyTheme);

function go(view, opts) {
  S.view = view;
  Object.assign(S, opts || {});
  render();
}

function render() {
  renderSidebar();
  renderSummary();
  const main = clear($("#main"));
  (views[S.view] || views.items)(main);
}

function renderSidebar() {
  const sb = clear($("#sidebar"));
  const nav = (key, label, ic, count, extra) => h("div", {
    class: "nav" + (extra.active ? " active" : ""), onclick: extra.onclick,
  }, ic ? icon(ic) : h("span", { class: "sev dot st-" + (extra.sev || "unknown") }), h("span", {}, label), count !== undefined ? h("span", { class: "count" }, count) : null);

  const total = S.items.length;
  sb.append(nav("all", "Összes elem", "list", total, {
    active: S.view === "items" && !S.group, onclick: () => go("items", { group: "" }),
  }));
  const groups = groupList();
  if (groups.length) {
    sb.append(h("h4", {}, "Csoportok"));
    for (const g of groups) {
      sb.append(nav("g:" + g.name, g.name, null, g.count, {
        active: S.view === "items" && S.group === g.name, sev: g.worst, onclick: () => go("items", { group: g.name }),
      }));
    }
  }
  sb.append(h("div", { class: "spacer" }));
  sb.append(nav("settings", "Beállítások", "gear", undefined, { active: S.view === "settings", onclick: () => go("settings") }));
  sb.append(nav("about", "Névjegy", "info", undefined, { active: S.view === "about", onclick: () => go("about") }));
}

function groupList() {
  const m = new Map();
  for (const r of S.items) {
    const g = r.item.group || "Csoport nélkül";
    const e = m.get(g) || { name: g, count: 0, worst: "ok" };
    e.count++;
    const st = r.state ? r.state.status : "unknown";
    if (STATUS_ORDER.indexOf(st) < STATUS_ORDER.indexOf(e.worst)) e.worst = st;
    m.set(g, e);
  }
  return Array.from(m.values()).sort((a, b) => a.name.localeCompare(b.name, "hu"));
}

function renderSummary() {
  const box = clear($("#summary"));
  const counts = {};
  for (const r of S.items) { const st = r.state ? r.state.status : "unknown"; counts[st] = (counts[st] || 0) + 1; }
  for (const st of ["ok", "late", "missing", "unreachable", "suspicious"]) {
    const n = counts[st] || 0;
    if (!n && st !== "ok") continue;
    box.append(h("span", {
      class: "chip st-" + st + (S.statusFilter === st ? " active" : ""), title: "Szűrés: " + STATUS[st],
      onclick: () => go("items", { statusFilter: S.statusFilter === st ? "" : st }),
    }, h("span", { class: "dot", style: { background: "var(--c)" } }), STATUS[st], h("span", { class: "n" }, n)));
  }
}

// ---- Items view (F1: empty state; filled in by later phases) -------------
views.items = main => {
  main.append(h("div", { class: "page-head" }, h("h1", {}, "Figyelt elemek")));
  main.append(h("div", { class: "card" }, h("div", { class: "empty" },
    h("img", { src: window.__ICON, alt: "" }),
    h("h2", {}, "Még nincs figyelt elem"),
    h("p", {}, "A BI Output Monitor a háttérben fut és figyeli, hogy a KNIME, DyntellBI és egyéb automatizált folyamatok kimenetei időben megérkeztek-e a hálózati meghajtókra."),
  )));
};

// ---- Settings view -------------------------------------------------------
views.settings = main => {
  const s = JSON.parse(JSON.stringify(S.settings));
  main.append(h("div", { class: "page-head" }, h("h1", {}, "Beállítások"), h("div", { class: "grow" }),
    h("button", { class: "btn primary", onclick: save }, icon("check"), "Mentés")));
  const grid = h("div", { class: "settings-grid" });
  main.append(grid);

  const sw = (obj, key) => h("label", { class: "switch" }, h("input", { type: "checkbox", checked: !!obj[key], onchange: e => { obj[key] = e.target.checked; } }));
  const num = (obj, key, min, max) => h("input", { class: "input narrow", type: "number", min, max, value: obj[key], oninput: e => { obj[key] = parseInt(e.target.value, 10) || 0; } });
  const timeIn = (obj, key) => h("input", { class: "input narrow", type: "time", value: obj[key], oninput: e => { obj[key] = e.target.value; } });
  const row = (title, hint, ctrl) => h("div", { class: "setting-row" }, h("div", { class: "lbl" }, h("b", {}, title), hint ? h("span", {}, hint) : null), ctrl);

  grid.append(h("div", { class: "card" },
    h("div", { class: "card-h" }, icon("gear"), h("h3", {}, "Általános")),
    h("div", { class: "card-b" },
      row("Indítás a Windows-zal", "Bejelentkezéskor automatikusan elindul a tálcán (admin jog nem kell).", sw(s, "autostart")),
      row("Ellenőrzési gyakoriság", "Másodperc. Az elvárt időpontokban ezen felül is ellenőriz.", num(s, "checkIntervalSec", 10, 3600)),
      row("Hálózati időkorlát", "Másodperc. Ennyi után az elem „Elérhetetlen” lesz.", num(s, "timeoutSec", 1, 120)),
      row("Párhuzamos ellenőrzések", "Egyszerre ennyi fájlt vizsgál.", num(s, "parallelism", 1, 32)),
      row("Előzmények megőrzése", "Nap.", num(s, "historyDays", 7, 3650)),
      row("Megjelenés", null, h("select", { class: "input", onchange: e => { s.theme = e.target.value; } },
        [["system", "Rendszer szerint"], ["light", "Világos"], ["dark", "Sötét"]].map(([v, l]) => h("option", { value: v, selected: s.theme === v }, l)))),
    )));

  const n = s.notifications;
  grid.append(h("div", { class: "card" },
    h("div", { class: "card-h" }, icon("bell"), h("h3", {}, "Értesítések")),
    h("div", { class: "card-b" },
      row("Windows értesítések", "Globális kapcsoló; elemenként is kikapcsolható.", sw(n, "enabled")),
      row("Késésről is szóljon", "Már a türelmi időn belül is (alapból csak a hibákról).", sw(n, "onLate")),
      row("Helyreállás jelzése", "Szól, ha egy hibás elem rendbe jött.", sw(n, "onRecovery")),
      row("Csendes időszak", "Ilyenkor nem jelenik meg értesítés; a végén összefoglalót kap.", sw(n.quiet, "enabled")),
      row("Csendes időszak kezdete / vége", null, h("div", { class: "row" }, timeIn(n.quiet, "from"), "–", timeIn(n.quiet, "to"))),
      row("Hétvégén is csendes", "Szombaton és vasárnap egész nap.", sw(n.quiet, "weekends")),
    )));

  async function save() {
    try {
      S.settings = await api("saveSettings", s);
      applyTheme();
      toast("Beállítások mentve.");
      render();
    } catch (e) { fail(e); }
  }
};

// ---- About view ------------------------------------------------------------
views.about = main => {
  const b = S.boot;
  main.append(h("div", { class: "page-head" }, h("h1", {}, "Névjegy")));
  main.append(h("div", { class: "card", style: { maxWidth: "640px" } }, h("div", { class: "card-b" },
    h("div", { class: "row", style: { gap: "16px", marginBottom: "14px" } },
      h("img", { src: window.__ICON, style: { width: "64px", height: "64px" } }),
      h("div", {}, h("div", { style: { fontSize: "18px", fontWeight: 650 } }, b.appName), h("div", { class: "muted" }, b.company + " · verzió " + b.version))),
    h("dl", { class: "kv" },
      h("dt", {}, "Beállítások"), h("dd", { class: "mono selectable" }, b.settingsPath),
    ),
    h("div", { class: "row", style: { marginTop: "14px" } },
      h("button", { class: "btn", onclick: () => api("openDataFolder").catch(fail) }, icon("folder"), "Adatmappa megnyitása")),
  )));
};

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------
async function boot() {
  $("#btnCheckAll").append(icon("refresh"), "Ellenőrzés most");
  $("#btnNew").append(icon("plus"), "Új elem");
  $("#search").addEventListener("input", e => { S.search = e.target.value.trim().toLowerCase(); if (S.view !== "items") S.view = "items"; render(); });
  try {
    S.boot = await api("bootstrap");
    S.settings = S.boot.settings;
    applyTheme();
    if (S.boot.warning) toast(S.boot.warning, true);
    if (typeof bootExtra === "function") await bootExtra();
    render();
  } catch (e) { fail(e); }
}
document.addEventListener("DOMContentLoaded", boot);
