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
  snap: null,
  selected: null,      // item id shown in the drawer
  sort: { key: "status", dir: 1 },
  groups: [],
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
  }, ic ? icon(ic) : h("span", { class: "sev st-" + (extra.sev || "unknown") }), h("span", {}, label), count !== undefined ? h("span", { class: "count" }, count) : null);

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

// ---------------------------------------------------------------------------
// Items view
// ---------------------------------------------------------------------------
const RANK = Object.fromEntries(STATUS_ORDER.map((s, i) => [s, i]));
const DAY_SHORT = ["", "H", "K", "Sze", "Cs", "P", "Szo", "V"];

function visibleItems() {
  const q = S.search;
  let rows = S.items.filter(r => {
    if (S.group && (r.item.group || "Csoport nélkül") !== S.group) return false;
    if (S.statusFilter && r.state.status !== S.statusFilter) return false;
    if (q) {
      const hay = [r.item.name, r.item.path, r.item.owner, r.item.group, r.item.note, r.state.resolved].join(" ").toLowerCase();
      if (!hay.includes(q)) return false;
    }
    return true;
  });
  const { key, dir } = S.sort;
  const val = r => {
    switch (key) {
      case "name": return r.item.name.toLowerCase();
      case "modified": return r.state.file ? new Date(r.state.file.modTime).getTime() : 0;
      case "next": return parseT(r.state.next) ? new Date(r.state.next).getTime() : Infinity;
      case "group": return (r.item.group || "").toLowerCase();
      default: return RANK[r.state.status] ?? 99;
    }
  };
  rows.sort((a, b) => {
    const va = val(a), vb = val(b);
    if (va < vb) return -dir;
    if (va > vb) return dir;
    return a.item.name.localeCompare(b.item.name, "hu");
  });
  return rows;
}

views.items = main => {
  const rows = visibleItems();
  const title = S.group || (S.statusFilter ? STATUS[S.statusFilter] + " elemek" : "Figyelt elemek");
  const last = S.items.map(r => parseT(r.state.lastCheck)).filter(Boolean).sort((a, b) => b - a)[0];
  main.append(h("div", { class: "page-head" },
    h("div", {}, h("h1", {}, title),
      h("div", { class: "sub" }, rows.length + " / " + S.items.length + " elem" + (last ? " · utolsó ellenőrzés: " + fmtDateTime(last, true) : ""))),
    h("div", { class: "grow" }),
    S.statusFilter || S.search ? h("button", { class: "btn sm ghost", onclick: () => { S.statusFilter = ""; S.search = ""; $("#search").value = ""; render(); } }, icon("close"), "Szűrés törlése") : null,
  ));
  if (S.snap && S.snap.pausedUntil && parseT(S.snap.pausedUntil) > new Date()) {
    main.append(h("div", { class: "banner" }, icon("bellOff"), h("span", { class: "grow" }, "Az értesítések szünetelnek eddig: " + fmtDateTime(S.snap.pausedUntil)),
      h("button", { class: "btn sm", onclick: () => api("pauseNotifications", "").then(refresh).catch(fail) }, "Visszakapcsolás")));
  }
  const down = (S.snap && S.snap.servers || []).filter(s => s.down);
  for (const sv of down) {
    main.append(h("div", { class: "banner", style: { background: "color-mix(in srgb, var(--unreachable) 14%, transparent)" } },
      h("span", { class: "dot st-unreachable", style: { width: "10px", height: "10px", borderRadius: "50%", background: "var(--unreachable)" } }),
      h("span", { class: "grow" }, h("b", {}, sv.server), " nem érhető el – " + (sv.lastError || "") + " Újrapróbálás: " + fmtDateTime(sv.retryAt, true))));
  }

  if (!S.items.length) {
    main.append(h("div", { class: "card" }, h("div", { class: "empty" },
      h("img", { src: window.__ICON, alt: "" }),
      h("h2", {}, "Még nincs figyelt elem"),
      h("p", {}, "Vegye fel az első outputot: egy konkrét fájlt (pl. \\\\EFS-FSRHQ\\Groups\\BI\\export.xlsx) vagy egy mintát (pl. sales_*.parquet, riport_{yyyyMMdd}.xlsx), és adja meg, mikor kell megérkeznie."),
      h("div", { class: "row", style: { justifyContent: "center" } },
        h("button", { class: "btn primary", onclick: () => openEditor() }, icon("plus"), "Új figyelt elem"),
        typeof importItems === "function" ? h("button", { class: "btn", onclick: importItems }, icon("upload"), "Importálás") : null),
    )));
    return;
  }
  if (!rows.length) {
    main.append(h("div", { class: "card" }, h("div", { class: "empty" }, h("h2", {}, "Nincs találat"), h("p", {}, "A szűrésnek egy elem sem felel meg."))));
    return;
  }

  const th = (key, label, cls) => h("th", {
    class: cls || "", onclick: key ? () => { S.sort = { key, dir: S.sort.key === key ? -S.sort.dir : 1 }; render(); } : null,
  }, label, key && S.sort.key === key ? h("span", { class: "arrow" }, S.sort.dir > 0 ? " ▲" : " ▼") : null);

  const tbody = h("tbody");
  for (const r of rows) tbody.append(itemRow(r));
  main.append(h("table", { class: "table" },
    h("colgroup", {}, ["c-status", "c-name", "c-path", "c-sched", "c-when", "c-when", "c-acts"].map(c => h("col", { class: c }))),
    h("thead", {}, h("tr", {}, th("status", "Állapot"), th("name", "Név"), th(null, "Útvonal", "nosort"), th(null, "Ütemezés", "nosort"),
      th("modified", "Utolsó módosítás"), th("next", "Következő elvárt"), th(null, "", "nosort"))),
    tbody));
};

function itemRow(r) {
  const it = r.item, st = r.state;
  const tr = h("tr", {
    class: (S.selected === it.id ? "sel" : "") + (!it.enabled ? " off" : ""),
    onclick: () => openDrawer(it.id),
    ondblclick: () => { if (!it.source) openEditor(it); },
  },
    h("td", {}, st.checking ? h("span", { class: "pill st-unknown" }, icon("refresh", "spin"), "Ellenőrzés…") : pill(st.status), st.acked ? h("span", { class: "group-tag", title: "Nyugtázva" }, "nyugtázva") : null),
    h("td", {}, h("span", { class: "name" }, it.name), it.group ? h("span", { class: "group-tag" }, it.group) : null,
      it.source ? h("span", { class: "shared-tag", title: "Közös listából: " + it.source }, "közös") : null,
      !it.notify ? icon("bellOff", "mute-ic") : null,
      it.owner ? h("div", { class: "small muted" }, it.owner) : null),
    h("td", {}, h("div", { class: "path", title: st.file ? st.file.path : it.path }, "‎" + (st.file ? st.file.path : (st.resolved || it.path)))),
    h("td", { class: "small" }, st.scheduleText || ""),
    h("td", { class: "when", title: st.file ? fmtFull(st.file.modTime) : "" }, st.file ? fmtDateTime(st.file.modTime) : h("span", { class: "muted" }, "–"),
      st.file ? h("div", { class: "small muted" }, fmtSize(st.file.size)) : null),
    h("td", { class: "when" }, parseT(st.next) ? fmtDateTime(st.next) : "–"),
    h("td", { class: "acts" },
      h("button", { class: "btn sm icon ghost", title: "Ellenőrzés most", onclick: e => { e.stopPropagation(); checkNow(it.id); } }, icon("refresh")),
      h("button", { class: "btn sm icon ghost", title: "Mappa megnyitása", onclick: e => { e.stopPropagation(); openPath(it.id, "folder"); } }, icon("folder")),
      h("button", { class: "btn sm icon ghost", title: "Továbbiak", onclick: e => { e.stopPropagation(); itemMenu(e.currentTarget, r); } }, icon("dots"))),
  );
  return tr;
}

function closeMenus() { $$(".menu").forEach(m => m.remove()); }
document.addEventListener("click", closeMenus);
function showMenu(anchor, entries) {
  closeMenus();
  const m = h("div", { class: "menu", onclick: e => e.stopPropagation() });
  for (const e of entries) {
    if (!e) { m.append(h("hr")); continue; }
    m.append(h("button", { class: e.danger ? "danger" : "", onclick: () => { closeMenus(); e.run(); } }, e.icon ? icon(e.icon) : null, e.label));
  }
  document.body.append(m);
  const r = anchor.getBoundingClientRect();
  const mw = m.offsetWidth, mh = m.offsetHeight;
  m.style.left = Math.max(8, Math.min(window.innerWidth - mw - 8, r.right - mw)) + "px";
  m.style.top = (r.bottom + mh + 8 > window.innerHeight ? r.top - mh - 4 : r.bottom + 4) + "px";
}

function itemMenu(anchor, r) {
  const it = r.item;
  const shared = !!it.source;
  showMenu(anchor, [
    !shared && { label: "Szerkesztés", icon: "edit", run: () => openEditor(it) },
    { label: "Duplikálás", icon: "copy", run: () => duplicateItem(it.id) },
    !shared && { label: it.enabled ? "Kikapcsolás" : "Bekapcsolás", icon: "power", run: () => setEnabled(it.id, !it.enabled) },
    typeof ackItem === "function" && r.state.status && ["missing", "unreachable", "suspicious", "late"].includes(r.state.status) && !r.state.acked
      ? { label: "Nyugtázás (ne szóljon újra)", icon: "ack", run: () => ackItem(it.id, true) } : null,
    null,
    { label: "Fájl megnyitása", icon: "open", run: () => openPath(it.id, "file") },
    { label: "Mappa megnyitása", icon: "folder", run: () => openPath(it.id, "folder") },
    !shared && null,
    !shared && { label: "Törlés", icon: "trash", danger: true, run: () => deleteItem(it) },
  ].filter(x => x !== false));
}

async function refresh() {
  try { applySnapshot(await api("listItems")); } catch (e) { fail(e); }
}
function applySnapshot(snap) {
  S.snap = snap;
  S.items = snap.items || [];
  const scroll = $("#main").scrollTop;
  if (!$(".overlay")) {
    render();
    $("#main").scrollTop = scroll;
  } else {
    renderSidebar(); renderSummary();
  }
  if (S.selected) renderDrawer();
}
bridge.on("state", applySnapshot);
bridge.on("settings", s => { S.settings = s; });

async function checkNow(id) {
  try {
    if (!id) { $("#btnCheckAll").classList.add("spin"); $("#btnCheckAll").disabled = true; }
    applySnapshot(await api("checkNow", id || ""));
    if (!id) toast("Ellenőrzés kész.");
  } catch (e) { fail(e); } finally {
    $("#btnCheckAll").classList.remove("spin"); $("#btnCheckAll").disabled = false;
  }
}
async function openPath(id, target) { try { await api("openPath", { id, target }); } catch (e) { fail(e); } }
async function duplicateItem(id) {
  try { const v = await api("duplicateItem", { id }); await refresh(); openEditor(v.item); } catch (e) { fail(e); }
}
async function setEnabled(id, enabled) {
  try { await api("setEnabled", { id, enabled }); await refresh(); toast(enabled ? "Elem bekapcsolva." : "Elem kikapcsolva."); } catch (e) { fail(e); }
}
async function deleteItem(it) {
  if (!(await confirmBox("Elem törlése", "Biztosan törli ezt az elemet?\n\n" + it.name, "Törlés", true))) return;
  try {
    await api("deleteItem", { id: it.id });
    if (S.selected === it.id) closeDrawer();
    await refresh();
    toast("Elem törölve.");
  } catch (e) { fail(e); }
}

function confirmBox(title, text, okLabel, danger) {
  return new Promise(resolve => {
    const close = v => { ov.remove(); resolve(v); };
    const ov = h("div", { class: "overlay", onclick: e => { if (e.target === ov) close(false); } },
      h("div", { class: "modal small" },
        h("div", { class: "m-head" }, h("h2", {}, title)),
        h("div", { class: "m-body", style: { whiteSpace: "pre-line" } }, text),
        h("div", { class: "m-foot" },
          h("button", { class: "btn", onclick: () => close(false) }, "Mégse"),
          h("button", { class: "btn " + (danger ? "danger solid" : "primary"), onclick: () => close(true) }, okLabel || "OK"))));
    document.body.append(ov);
  });
}

// ---------------------------------------------------------------------------
// Detail drawer
// ---------------------------------------------------------------------------
function openDrawer(id) { S.selected = id; renderDrawer(); $$("#main tr").forEach(tr => tr.classList.remove("sel")); render(); }
function closeDrawer() { S.selected = null; $("#drawer").classList.remove("open"); render(); }

function renderDrawer() {
  const d = $("#drawer");
  const r = S.items.find(x => x.item.id === S.selected);
  if (!r) { d.classList.remove("open"); return; }
  const it = r.item, st = r.state;
  const keep = d.querySelector(".d-body") ? d.querySelector(".d-body").scrollTop : 0;
  clear(d);
  const shared = !!it.source;
  d.append(h("div", { class: "d-head" },
    h("div", { class: "row" }, pill(st.status), h("div", { class: "grow" }),
      h("button", { class: "btn sm icon ghost", title: "Bezárás (Esc)", onclick: closeDrawer }, icon("close"))),
    h("h2", { style: { marginTop: "10px" } }, it.name),
    h("div", { class: "small muted" }, [it.group, it.owner].filter(Boolean).join(" · ") || " "),
    h("div", { class: "d-actions" },
      h("button", { class: "btn sm primary", onclick: () => checkNow(it.id) }, icon("refresh"), "Ellenőrzés most"),
      !shared ? h("button", { class: "btn sm", onclick: () => openEditor(it) }, icon("edit"), "Szerkesztés") : null,
      h("button", { class: "btn sm", onclick: () => openPath(it.id, "file"), disabled: !st.file }, icon("open"), "Fájl"),
      h("button", { class: "btn sm", onclick: () => openPath(it.id, "folder") }, icon("folder"), "Mappa"),
      h("button", { class: "btn sm icon", title: "Továbbiak", onclick: e => { e.stopPropagation(); itemMenu(e.currentTarget, r); } }, icon("dots")),
    )));
  const body = h("div", { class: "d-body" });
  d.append(body);
  body.append(h("div", { class: "reason st-" + st.status }, st.reason || "Még nem volt ellenőrzés."));
  const kv = h("dl", { class: "kv" });
  const add = (k, v, cls) => { if (v === null || v === undefined || v === "") return; kv.append(h("dt", {}, k), h("dd", { class: cls || "" }, v)); };
  add("Figyelt útvonal", it.path, "mono selectable");
  if (st.resolved && st.resolved !== it.path) add("Vizsgált", st.resolved, "mono selectable");
  if (st.file) add("Talált fájl", st.file.path, "mono selectable");
  add("Ütemezés", st.scheduleText);
  if (st.scheduleError) add("Ütemezési hiba", st.scheduleError);
  add("Elvárt időpont", parseT(st.expected) ? fmtDateTime(st.expected) : null);
  add("Határidő", parseT(st.deadline) ? fmtDateTime(st.deadline) + " (türelmi idő: " + it.graceMinutes + " perc)" : null);
  add("Következő elvárt", parseT(st.next) ? fmtDateTime(st.next) : null);
  if (st.file) {
    add("Utolsó módosítás", fmtFull(st.file.modTime) + " (" + relTime(st.file.modTime) + ")");
    add("Méret", fmtSize(st.file.size) + " (" + st.file.size.toLocaleString("hu-HU") + " bájt)");
  }
  if (st.matches > 1) add("Illeszkedő fájlok", st.matches + " db");
  add("Utolsó ellenőrzés", parseT(st.lastCheck) ? fmtDateTime(st.lastCheck, true) + " · " + st.tookMs + " ms" : "még nem volt");
  add("Állapot óta", parseT(st.since) ? fmtDateTime(st.since) : null);
  add("Értesítés", it.notify ? "bekapcsolva" : "kikapcsolva");
  if (it.source) add("Forrás", "Közös lista: " + it.source);
  add("Megjegyzés", it.note);
  body.append(kv);
  if (st.candidates && st.candidates.length > 1) {
    body.append(h("div", { class: "section-t" }, "Legfrissebb illeszkedő fájlok"));
    const ev = h("div", { class: "events" });
    for (const c of st.candidates) ev.append(h("div", { class: "event" }, h("span", { class: "t" }, fmtDateTime(c.modTime)), h("span", { class: "mono grow selectable" }, c.name), h("span", { class: "muted" }, fmtSize(c.size))));
    body.append(ev);
  }
  if (typeof renderHistory === "function") renderHistory(body, it, st);
  d.classList.add("open");
  body.scrollTop = keep;
}

// ---------------------------------------------------------------------------
// Editor
// ---------------------------------------------------------------------------
const SCHED_TYPES = [
  ["hourly", "Óránként"], ["daily", "Naponta"], ["weekly", "Hetente"],
  ["workdays", "Munkanapokon"], ["monthly", "Havonta"], ["cron", "Cron"],
];

async function openEditor(item) {
  let it;
  if (item) it = JSON.parse(JSON.stringify(item));
  else {
    try { it = await api("newItem"); } catch (e) { return fail(e); }
    if (S.group && S.group !== "Csoport nélkül") it.group = S.group;
  }
  if (!S.groups.length) { try { S.groups = await api("groups"); } catch (_) { /* ignore */ } }
  const isNew = !it.id;
  const sp = it.schedule;
  let suggestions = [];
  let testOut = null;

  const ov = h("div", { class: "overlay" });
  const modal = h("div", { class: "modal" });
  ov.append(modal);
  document.body.append(ov);
  const close = () => { ov.remove(); document.removeEventListener("keydown", onKey); render(); };
  const onKey = e => { if (e.key === "Escape") close(); if (e.key === "Enter" && e.ctrlKey) save(); };
  document.addEventListener("keydown", onKey);

  const preview = h("div");
  const testBox = h("div");
  const pathHelp = h("div");
  const schedBox = h("div");
  const errBox = h("div");

  const field = (label, ctrl, hint) => h("label", { class: "field" }, label, ctrl, hint ? h("span", { class: "hint" }, hint) : null);
  const text = (obj, key, attrs) => h("input", Object.assign({ class: "input", value: obj[key] || "", oninput: e => { obj[key] = e.target.value; onChange(key); } }, attrs || {}));
  const number = (obj, key, attrs) => h("input", Object.assign({ class: "input narrow", type: "number", value: obj[key] ?? 0, oninput: e => { obj[key] = parseInt(e.target.value, 10) || 0; onChange(key); } }, attrs || {}));
  const sw = (obj, key, label) => h("label", { class: "switch" }, h("input", { type: "checkbox", checked: !!obj[key], onchange: e => { obj[key] = e.target.checked; onChange(key); } }), label);

  let pvTimer = null;
  function onChange(key) {
    if (key === "path") renderPathHelp();
    clearTimeout(pvTimer);
    pvTimer = setTimeout(updatePreview, 250);
  }

  const pathInput = text(it, "path", { class: "input mono grow", placeholder: "\\\\EFS-FSRHQ\\Groups\\BI\\export.xlsx", spellcheck: false });
  pathInput.addEventListener("blur", async () => {
    if (/^[a-zA-Z]:[\\/]/.test(it.path)) {
      try {
        const r = await api("toUNC", it.path);
        if (r.converted) { it.path = r.path; pathInput.value = r.path; toast("Csatolt meghajtó → UNC: " + r.path); }
        suggestions = r.suggestions || [];
        renderPathHelp();
      } catch (_) { /* ignore */ }
    }
  });

  async function browse() {
    try {
      const r = await api("browseFile", it.path);
      if (!r.path) return;
      it.path = r.path;
      pathInput.value = r.path;
      suggestions = r.suggestions || [];
      if (!it.name) {
        const base = r.path.split(/[\\/]/).pop().replace(/\.[^.]+$/, "");
        it.name = base; $("#ed-name").value = base;
      }
      if (r.converted) toast("A csatolt meghajtós útvonalat UNC-re alakítottam.");
      if (r.local) toast("Figyelem: helyi meghajtós útvonal – más gépen nem biztos, hogy elérhető.", true);
      onChange("path");
    } catch (e) { fail(e); }
  }

  function renderPathHelp() {
    clear(pathHelp);
    const hasTok = /\{[^}]+\}/.test(it.path || "");
    if (suggestions.length) {
      pathHelp.append(h("div", { class: "suggest" }, "A fájlnév dátumot tartalmaz. Mintaként figyelje? ",
        suggestions.map(sg => h("button", { class: "btn sm", style: { margin: "4px 4px 0 0" }, onclick: () => { it.path = sg; pathInput.value = sg; suggestions = []; onChange("path"); } }, h("code", {}, sg.split(/[\\/]/).pop())))));
    }
    if (hasTok) {
      pathHelp.append(h("div", { class: "row wrap", style: { marginTop: "8px" } },
        h("span", { class: "small muted" }, "Dátum token:"),
        h("div", { class: "seg" },
          h("button", { class: it.tokenMode !== "any" ? "on" : "", onclick: () => { it.tokenMode = ""; renderPathHelp(); onChange(); } }, "az elvárt nap dátuma"),
          h("button", { class: it.tokenMode === "any" ? "on" : "", onclick: () => { it.tokenMode = "any"; renderPathHelp(); onChange(); } }, "bármilyen dátum (legfrissebb)"))));
    }
    pathHelp.append(h("div", { class: "small muted", style: { marginTop: "6px", lineHeight: 1.5 } },
      "Minta: ", h("code", {}, "*"), " és ", h("code", {}, "?"), " a fájlnévben; dátum: ", h("code", {}, "{yyyyMMdd}"), ", ", h("code", {}, "{yyyy-MM-dd}"),
      ", eltolással ", h("code", {}, "{yyyyMMdd:-1d}"), ". Mintánál a legfrissebb illeszkedő fájl számít."));
  }

  function timesEditor() {
    if (!sp.times) sp.times = [];
    const box = h("div", { class: "row wrap" });
    const draw = () => {
      clear(box);
      for (const t of sp.times) box.append(h("span", { class: "tag" }, t, h("button", { title: "Eltávolítás", onclick: () => { sp.times = sp.times.filter(x => x !== t); draw(); onChange(); } }, "×")));
      const inp = h("input", { class: "input narrow", type: "time", value: "" });
      const addT = () => { const v = inp.value; if (v && !sp.times.includes(v)) { sp.times.push(v); sp.times.sort(); draw(); onChange(); } };
      inp.addEventListener("change", addT);
      box.append(inp, h("button", { class: "btn sm", onclick: addT }, icon("plus"), "Időpont"));
    };
    draw();
    return field("Időpont(ok)", box);
  }
  function dayPicker() {
    if (!sp.weekdays) sp.weekdays = [];
    const box = h("div", { class: "daypick" });
    const draw = () => {
      clear(box);
      for (let d = 1; d <= 7; d++) {
        box.append(h("button", { class: sp.weekdays.includes(d) ? "on" : "", onclick: () => {
          sp.weekdays = sp.weekdays.includes(d) ? sp.weekdays.filter(x => x !== d) : [...sp.weekdays, d].sort();
          draw(); onChange();
        } }, DAY_SHORT[d]));
      }
    };
    draw();
    return box;
  }
  function holidayRule() {
    return field("Munkaszüneti napon", h("select", { class: "input", onchange: e => { sp.holidayRule = e.target.value; onChange(); } },
      [["", "nem számít (ugyanúgy elvárt)"], ["skip", "nincs elvárás"], ["next", "a következő munkanapra tolódik"], ["prev", "az előző munkanapra kerül"]]
        .map(([v, l]) => h("option", { value: v, selected: (sp.holidayRule || "") === v }, l))));
  }

  function renderSchedule() {
    clear(schedBox);
    schedBox.append(h("div", { class: "seg", style: { marginBottom: "12px" } },
      SCHED_TYPES.map(([v, l]) => h("button", { class: sp.type === v ? "on" : "", onclick: () => {
        sp.type = v;
        if (["daily", "weekly", "workdays", "monthly"].includes(v) && (!sp.times || !sp.times.length)) sp.times = ["06:00"];
        if (v === "weekly" && (!sp.weekdays || !sp.weekdays.length)) sp.weekdays = [1];
        if (v === "workdays" && sp.useHolidays === undefined) sp.useHolidays = true;
        if (v === "monthly" && !sp.monthlyMode) { sp.monthlyMode = "day"; sp.monthDay = sp.monthDay || 1; }
        if (v === "cron" && !sp.cron) sp.cron = "0 6 * * 1-5";
        renderSchedule(); onChange();
      } }, l))));
    const g = h("div", { class: "grid2" });
    schedBox.append(g);
    switch (sp.type) {
      case "hourly": {
        if (!sp.intervalMinutes) sp.intervalMinutes = 60;
        g.append(field("Gyakoriság", h("select", { class: "input", onchange: e => { sp.intervalMinutes = parseInt(e.target.value, 10); onChange(); } },
          [[15, "15 percenként"], [30, "30 percenként"], [60, "óránként"], [120, "2 óránként"], [180, "3 óránként"], [240, "4 óránként"], [360, "6 óránként"], [720, "12 óránként"]]
            .map(([v, l]) => h("option", { value: v, selected: sp.intervalMinutes === v }, l)))));
        g.append(field("Napok", h("select", { class: "input", onchange: e => { sp.days = e.target.value; renderSchedule(); onChange(); } },
          [["", "minden nap"], ["weekdays", "hétköznap (H–P)"], ["workdays", "munkanapokon (ünnepekkel)"], ["custom", "kiválasztott napokon"]]
            .map(([v, l]) => h("option", { value: v, selected: (sp.days || "") === v }, l)))));
        g.append(field("Első időpont (kezdés)", h("input", { class: "input", type: "time", value: sp.from || "00:00", oninput: e => { sp.from = e.target.value; onChange(); } }), "pl. 06:00, vagy 00:15 = minden óra 15. percében"));
        g.append(field("Utolsó időpont (vége)", h("input", { class: "input", type: "time", value: sp.to || "23:59", oninput: e => { sp.to = e.target.value; onChange(); } }), "ha korábbi a kezdésnél, átnyúlik éjfélen"));
        if (sp.days === "custom") schedBox.append(h("div", { style: { marginTop: "12px" } }, field("Kiválasztott napok", dayPicker())));
        break;
      }
      case "daily":
        g.append(timesEditor(), holidayRule());
        break;
      case "weekly":
        g.append(field("Napok", dayPicker()), holidayRule(), timesEditor());
        break;
      case "workdays":
        g.append(timesEditor(), h("div", { class: "col", style: { justifyContent: "center" } },
          sw(sp, "useHolidays", "Magyar munkaszüneti napok és áthelyezett munkanapok figyelembevétele")));
        break;
      case "monthly": {
        const needDay = sp.monthlyMode === "day" || sp.monthlyMode === "nthWorkday" || !sp.monthlyMode;
        g.append(field("Melyik nap", h("select", { class: "input", onchange: e => { sp.monthlyMode = e.target.value; if (!sp.monthDay) sp.monthDay = 1; renderSchedule(); onChange(); } },
          [["day", "a hónap N. napja"], ["firstWorkday", "első munkanap"], ["nthWorkday", "N. munkanap"], ["lastWorkday", "utolsó munkanap"], ["lastDay", "a hónap utolsó napja"]]
            .map(([v, l]) => h("option", { value: v, selected: (sp.monthlyMode || "day") === v }, l)))));
        if (needDay) g.append(field("N =", number(sp, "monthDay", { min: 1, max: 31 }), sp.monthlyMode === "day" ? "rövidebb hónapban az utolsó nap" : null));
        g.append(timesEditor());
        if (sp.monthlyMode === "day" || sp.monthlyMode === "lastDay" || !sp.monthlyMode) g.append(holidayRule());
        break;
      }
      case "cron":
        g.append(field("Cron kifejezés", h("input", { class: "input mono", value: sp.cron || "", oninput: e => { sp.cron = e.target.value; onChange(); }, spellcheck: false }),
          "perc óra nap hónap hét_napja – pl. 0 6 * * 1-5 (hétköznap 6:00), */15 8-17 * * * , 30 7 1 * *"), holidayRule());
        break;
    }
  }

  async function updatePreview() {
    try {
      const pv = await api("previewSchedule", { schedule: sp, count: 8 });
      clear(preview);
      preview.append(h("div", { class: "section-t", style: { marginTop: 0 } }, "Ütemezés előnézet"));
      if (pv.error) { preview.append(h("div", { class: "errbox" }, pv.error)); return; }
      preview.append(h("div", { style: { fontWeight: 600, marginBottom: "8px" } }, pv.text));
      if (pv.prev) preview.append(h("div", { class: "small muted", style: { marginBottom: "6px" } }, "Legutóbbi elvárt: " + fmtDateTime(pv.prev)));
      const ul = h("ul", { class: "preview-list" });
      const wd = ["V", "H", "K", "Sze", "Cs", "P", "Szo"];
      for (const t of pv.next) { const d = new Date(t); ul.append(h("li", {}, h("span", {}, fmtDateTime(t)), h("span", { class: "muted" }, wd[d.getDay()]))); }
      preview.append(ul);
    } catch (e) { /* ignore while typing */ }
  }

  async function runTest() {
    clear(testBox);
    testBox.append(h("div", { class: "small muted" }, "Ellenőrzés folyamatban…"));
    try {
      const r = await api("testPath", it);
      clear(testBox);
      if (r.error) { testBox.append(h("div", { class: "errbox" }, r.error)); return; }
      testOut = r;
      const res = r.result || {};
      testBox.append(h("div", { class: "row", style: { marginBottom: "6px" } }, pill(r.status)));
      testBox.append(h("div", { class: "reason st-" + r.status, style: { margin: "6px 0", fontSize: "12.5px" } }, r.reason));
      if (res.file) testBox.append(h("div", { class: "small" }, h("b", {}, "Talált: "), h("span", { class: "mono selectable" }, res.file.name), " · ", fmtFull(res.file.modTime), " · ", fmtSize(res.file.size)));
      if (res.matches > 1) testBox.append(h("div", { class: "small muted" }, res.matches + " illeszkedő fájl, a legfrissebb számít."));
      if (res.resolved && res.resolved !== it.path) testBox.append(h("div", { class: "small muted mono", style: { marginTop: "4px", wordBreak: "break-all" } }, "Vizsgált: " + res.resolved));
      for (const w of r.warnings || []) testBox.append(h("div", { class: "small", style: { marginTop: "6px", color: "var(--late)" } }, w));
      if (r.normPath && r.normPath !== it.path) { it.path = r.normPath; pathInput.value = r.normPath; }
    } catch (e) { clear(testBox); testBox.append(h("div", { class: "errbox" }, e.message)); }
  }

  async function save() {
    clear(errBox);
    try {
      const r = await api("saveItem", it);
      close();
      await refresh();
      toast(isNew ? "Új elem felvéve: " + r.view.item.name : "Mentve: " + r.view.item.name);
      for (const w of r.warnings || []) toast(w);
      openDrawer(r.view.item.id);
    } catch (e) {
      errBox.append(h("div", { class: "errbox" }, e.message));
    }
  }

  if (!it.suspicious) it.suspicious = { zeroBytes: true };
  const sus = it.suspicious;
  const minKB = { v: sus.minBytes ? Math.round(sus.minBytes / 1024) : 0 };

  modal.append(
    h("div", { class: "m-head" }, h("h2", {}, isNew ? "Új figyelt elem" : "Elem szerkesztése"), h("div", { class: "grow" }),
      h("button", { class: "btn sm icon ghost", onclick: close, title: "Bezárás (Esc)" }, icon("close"))),
    h("div", { class: "m-body" }, h("div", { class: "editor" },
      h("div", {},
        h("fieldset", {}, h("legend", {}, "Alapadatok"),
          h("div", { class: "grid2" },
            field("Név *", text(it, "name", { id: "ed-name", placeholder: "pl. Napi értékesítési export" })),
            field("Csoport", text(it, "group", { list: "ed-groups", placeholder: "pl. KNIME" })),
            field("Felelős", text(it, "owner", { placeholder: "név vagy e-mail" })),
            field("Megjegyzés", text(it, "note", { placeholder: "pl. KNIME workflow neve, teendő hiba esetén" }))),
          h("datalist", { id: "ed-groups" }, S.groups.map(g => h("option", { value: g })))),
        h("fieldset", {}, h("legend", {}, "Útvonal"),
          h("div", { class: "row" }, pathInput, h("button", { class: "btn", onclick: browse }, icon("folder"), "Tallózás…")),
          pathHelp),
        h("fieldset", {}, h("legend", {}, "Ütemezés"), schedBox),
        h("fieldset", {}, h("legend", {}, "Tolerancia"),
          h("div", { class: "grid2" },
            field("Türelmi idő (perc)", number(it, "graceMinutes", { min: 0 }), "ennyi ideig „Késik”, utána „Hiányzik”"),
            field("Korai érkezés elfogadása (perc)", number(it, "earlyMinutes", { min: 0 }), "az elvárt időpont előtt ennyivel érkező fájl is jó"))),
        h("fieldset", {}, h("legend", {}, "Gyanús fájl"),
          h("div", { class: "grid3" },
            h("div", { class: "col", style: { justifyContent: "center" } }, sw(sus, "zeroBytes", "0 bájtos fájl gyanús")),
            field("Minimális méret (KB)", h("input", { class: "input narrow", type: "number", min: 0, value: minKB.v, oninput: e => { sus.minBytes = (parseInt(e.target.value, 10) || 0) * 1024; } })),
            field("Méretcsökkenés küszöb (%)", number(sus, "dropPercent", { min: 0, max: 99 }), "a szokásos mérethez képest; 0 = ki"))),
        h("fieldset", {}, h("legend", {}, "Működés"),
          h("div", { class: "row wrap", style: { gap: "24px" } }, sw(it, "enabled", "Figyelés bekapcsolva"), sw(it, "notify", "Értesítés erről az elemről"))),
        errBox,
      ),
      h("div", { class: "side" }, preview,
        h("div", { class: "section-t" }, "Útvonal teszt"),
        h("div", { class: "small muted", style: { marginBottom: "8px" } }, "Megnézi a fájlt most, és megmutatja, milyen állapot lenne."),
        h("button", { class: "btn sm", onclick: runTest }, icon("test"), "Útvonal tesztelése"),
        h("div", { style: { marginTop: "10px" } }, testBox)),
    )),
    h("div", { class: "m-foot" },
      h("span", { class: "small muted grow" }, "Ctrl+Enter: mentés · Esc: mégse"),
      h("button", { class: "btn", onclick: close }, "Mégse"),
      h("button", { class: "btn primary", onclick: save }, icon("check"), isNew ? "Felvétel" : "Mentés")));

  renderPathHelp();
  renderSchedule();
  updatePreview();
  setTimeout(() => (isNew ? pathInput : $("#ed-name")).focus(), 50);
}

document.addEventListener("keydown", e => {
  if (e.key === "Escape" && !$(".overlay")) { closeMenus(); if (S.selected) closeDrawer(); }
  if (e.key === "F5") { e.preventDefault(); checkNow(); }
  if ((e.key === "n" || e.key === "N") && e.ctrlKey && !$(".overlay")) { e.preventDefault(); openEditor(); }
  if (e.key === "f" && e.ctrlKey) { e.preventDefault(); $("#search").focus(); }
});

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
  $("#btnCheckAll").addEventListener("click", () => checkNow());
  $("#btnNew").addEventListener("click", () => openEditor());
  $("#search").addEventListener("input", e => { S.search = e.target.value.trim().toLowerCase(); if (S.view !== "items") S.view = "items"; render(); });
  try {
    S.boot = await api("bootstrap");
    S.settings = S.boot.settings;
    applyTheme();
    if (S.boot.warning) toast(S.boot.warning, true);
    applySnapshot(await api("listItems"));
  } catch (e) { fail(e); }
}
document.addEventListener("DOMContentLoaded", boot);
