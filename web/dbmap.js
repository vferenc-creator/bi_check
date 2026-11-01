// ---------------------------------------------------------------------------
// Adatbázis fül: read-only database map (structure on request, team-made
// descriptions, relations, groups and notes) and a row viewer.
// Nothing is queried automatically: only the buttons send queries.
// ---------------------------------------------------------------------------
Object.assign(ICONS, {
  db: '<ellipse cx="12" cy="5.5" rx="7.5" ry="2.8"/><path d="M4.5 5.5v13c0 1.5 3.4 2.8 7.5 2.8s7.5-1.3 7.5-2.8v-13"/><path d="M4.5 12c0 1.5 3.4 2.8 7.5 2.8s7.5-1.3 7.5-2.8"/>',
  key: '<circle cx="8" cy="15" r="4"/><path d="m11 12 9-9M17 6l3 3M15 8l2 2"/>',
  link: '<path d="M10 14a4.5 4.5 0 0 0 6.4 0l3-3a4.5 4.5 0 0 0-6.4-6.4l-1 1"/><path d="M14 10a4.5 4.5 0 0 0-6.4 0l-3 3a4.5 4.5 0 0 0 6.4 6.4l1-1"/>',
  note: '<path d="M5 3h14a2 2 0 0 1 2 2v9l-7 7H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z"/><path d="M14 21v-5a2 2 0 0 1 2-2h5"/>',
  layout: '<rect x="3" y="3" width="7" height="8" rx="1.5"/><rect x="14" y="3" width="7" height="5" rx="1.5"/><rect x="14" y="12" width="7" height="9" rx="1.5"/><rect x="3" y="15" width="7" height="6" rx="1.5"/>',
  undo: '<path d="M9 14 4 9l5-5"/><path d="M4 9h10a6 6 0 0 1 0 12h-3"/>',
  redo: '<path d="m15 14 5-5-5-5"/><path d="M20 9H10a6 6 0 0 0 0 12h3"/>',
  minus: '<path d="M5 12h14"/>',
  fit: '<path d="M4 9V5a1 1 0 0 1 1-1h4M15 4h4a1 1 0 0 1 1 1v4M20 15v4a1 1 0 0 1-1 1h-4M9 20H5a1 1 0 0 1-1-1v-4"/>',
  table: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 10h18M3 15h18M9 10v10"/>',
  eye: '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>',
  comment: '<path d="M21 12a8 8 0 0 1-11.8 7L4 20l1.2-4.6A8 8 0 1 1 21 12z"/>',
  collapse: '<path d="m7 15 5-5 5 5"/>',
  expand: '<path d="m7 10 5 5 5-5"/>',
  rows: '<path d="M4 6h16M4 12h16M4 18h16"/>',
  stop: '<rect x="6" y="6" width="12" height="12" rx="2"/>',
  tag: '<path d="M3 12V4a1 1 0 0 1 1-1h8l9 9-9 9z"/><circle cx="7.5" cy="7.5" r="1.3"/>',
});

const CARD = { W: 264, HEAD: 42, ROW: 24, FOOT: 10, COLLAPSED: 8, GROUP_PAD: 36, GROUP_TOP: 50 };
const GROUP_COLORS = ["#F2A531", "#4C8DF6", "#2E9E5B", "#8B5CF6", "#E5508A", "#14A3A3", "#E0702A", "#64748B"];
const NOTE_COLORS = ["#FFF3C4", "#DDF4E4", "#DCEBFF", "#FCE1EC", "#EDE6FF"];
const nk = k => String(k || "").trim().toLowerCase();

const DB = {
  view: null, doc: null, schema: null,
  sel: null, selRel: null, search: "",
  k: 1, tx: 40, ty: 40,
  undo: [], redo: [],
  busy: false, mounted: null, poll: null,
  detailOpen: false,
};

function dbMounted() { return DB.mounted && document.body.contains(DB.mounted); }

// ---- data helpers ------------------------------------------------------------
function dbTables() { return (DB.schema && DB.schema.tables) || []; }
function dbTable(key) { const k = nk(key); return dbTables().find(t => nk(t.schema + "." + t.name) === k) || null; }
function tkey(t) { return t.schema + "." + t.name; }
function meta(key) { return (DB.doc && DB.doc.tables[nk(key)]) || null; }
function colMeta(key, col) { const m = meta(key); return (m && m.columns && m.columns[nk(col)]) || null; }
function groupOf(key) { const m = meta(key); return m && m.group ? (DB.doc.groups || []).find(g => g.id === m.group) || null : null; }

// every relation as {id, from, fromCol, to, toCol, manual, fk}
function dbRelations() {
  const out = [];
  const hidden = new Set((DB.doc && DB.doc.hiddenFks) || []);
  for (const fk of (DB.schema && DB.schema.fks) || []) {
    if (hidden.has(fk.name)) continue;
    fk.fromCols.forEach((c, i) => out.push({ id: "fk:" + fk.name + ":" + i, from: fk.from, fromCol: c, to: fk.to, toCol: fk.toCols[i], manual: false, fk }));
  }
  for (const r of (DB.doc && DB.doc.relations) || []) out.push(Object.assign({ manual: true }, r));
  return out.filter(r => dbTable(r.from) && dbTable(r.to));
}
function relatedCols() {
  const s = new Set();
  for (const r of dbRelations()) { s.add(nk(r.from) + "|" + nk(r.fromCol)); s.add(nk(r.to) + "|" + nk(r.toCol)); }
  return s;
}

// visible rows of a card (collapsed: keys and related columns, max 8)
function cardRows(t, rel) {
  const m = meta(tkey(t));
  if (!m || !m.collapsed) return { cols: t.columns, more: 0 };
  const keyCols = t.columns.filter(c => c.pk || rel.has(nk(tkey(t)) + "|" + nk(c.name))).slice(0, CARD.COLLAPSED);
  return { cols: keyCols, more: t.columns.length - keyCols.length };
}
function cardHeight(t, rel) {
  const r = cardRows(t, rel);
  return CARD.HEAD + Math.max(1, r.cols.length + (r.more ? 1 : 0)) * CARD.ROW + CARD.FOOT;
}

function fmtRows(n) { return n < 0 ? "" : n.toLocaleString("hu-HU") + " sor"; }

// ---- API ---------------------------------------------------------------------
async function dbLoad(quiet) {
  try {
    const known = !!(DB.doc && quiet);
    const v = await api("dbLoad", known ? { known: true, rev: DB.doc.rev, fetch: DB.schema ? (DB.schema.changed || DB.schema.fetchedAt) : "0001-01-01T00:00:00Z" } : {});
    if (v.unchanged) return;
    DB.view = v;
    if (v.snap) {
      DB.doc = v.snap.doc;
      if (v.snap.schema || !known) DB.schema = v.snap.schema;
    } else { DB.doc = null; DB.schema = null; }
    dbRender(quiet);
  } catch (e) { if (!quiet) fail(e); }
}

// apply ops with undo information; label shown in the undo tooltip
async function dbDo(ops, undoOps, label) {
  try {
    DB.doc = await api("dbApply", ops);
    if (undoOps) { DB.undo.push({ ops, undo: undoOps, label }); if (DB.undo.length > 100) DB.undo.shift(); DB.redo = []; }
    dbRender(true);
    return true;
  } catch (e) { fail(e); dbLoad(true); return false; }
}
async function dbUndo(redo) {
  const st = redo ? DB.redo : DB.undo;
  const e = st.pop();
  if (!e) return;
  if (e.undoFn) { // description in the database: undo = write the old text back
    const ok = await (redo ? e.doFn() : e.undoFn());
    if (ok) { (redo ? DB.undo : DB.redo).push(e); toast((redo ? "Újra: " : "Visszavonva: ") + e.label); dbRender(true); } else st.push(e);
    return;
  }
  try {
    DB.doc = await api("dbApply", redo ? e.ops : e.undo);
    (redo ? DB.undo : DB.redo).push(e);
    dbRender(true);
    toast((redo ? "Újra: " : "Visszavonva: ") + e.label);
  } catch (err) { fail(err); }
}
const rid = () => Math.random().toString(16).slice(2, 14);

async function dbFetch() {
  if (DB.busy) return;
  DB.busy = true; dbRender(true);
  try {
    const r = await api("dbFetch");
    DB.view = r; DB.doc = r.snap.doc; DB.schema = r.snap.schema;
    const s = r.summary;
    let msg = "Szerkezet lekérve: " + s.tables + " tábla";
    if (s.new.length) msg += ", " + s.new.length + " új";
    if (s.gone.length) msg += ", " + s.gone.length + " eltűnt";
    if (s.newColumns) msg += ", " + s.newColumns + " új mező";
    toast(msg + ".");
    if (s.warnings && s.warnings.length) toast("Részben nem olvasható: " + s.warnings.join("; "), true);
    if (!DB.fitted) { DB.fitted = true; setTimeout(() => dbFit(), 30); }
  } catch (e) { fail(e); } finally { DB.busy = false; dbRender(true); }
}

// ---- view --------------------------------------------------------------------
views.db = main => {
  const root = h("div", { class: "dbv" });
  main.append(root);
  DB.mounted = root;
  dbRestoreViewport();
  dbRender(false);
  dbLoad(false);
  clearInterval(DB.poll);
  // the map can be edited from the other computer too: pick up changes
  DB.poll = setInterval(() => {
    if (S.view !== "db" || !dbMounted()) { clearInterval(DB.poll); return; }
    if (document.visibilityState === "visible" && !DB.drag && !$(".overlay")) dbLoad(true);
  }, 20000);
};

function dbRender(keepDetail) {
  const root = DB.mounted;
  if (!root || !document.body.contains(root)) return;
  const focusEl = document.activeElement;
  const editing = focusEl && root.contains(focusEl) && /INPUT|TEXTAREA|SELECT/.test(focusEl.tagName) && focusEl.closest(".dbv-detail, .dbv-note, .dbv-pop, .s-search");
  if (!root.firstChild) {
    root.append(h("aside", { class: "dbv-side" }), h("section", { class: "dbv-canvas" }));
  }
  dbRenderSide(root.firstChild, editing && focusEl.closest(".s-search"));
  dbRenderCanvas(root.children[1], editing);
}

// ---- left side: header + table list --------------------------------------------
function dbRenderSide(side, keepSearch) {
  const v = DB.view;
  const conn = v && v.conn;
  if (keepSearch) { // only refresh the list while typing
    const list = side.querySelector(".dbv-list");
    if (list) { list.replaceWith(dbList()); return; }
  }
  clear(side);
  const fetched = DB.schema && parseT(DB.schema.fetchedAt);
  side.append(h("div", { class: "s-head" },
    h("div", { class: "s-title" }, h("div", { class: "db-ic" }, icon("db")),
      h("div", { style: { minWidth: 0 } }, h("b", { title: conn ? conn.database : "" }, conn && conn.database ? conn.database : "Adatbázis"),
        h("span", { title: conn ? conn.server : "" }, conn && conn.server ? conn.server : "nincs beállítva"))),
    conn && v.configured ? h("div", { class: "row" },
      h("button", { class: "btn primary sm grow", disabled: DB.busy, onclick: dbFetch, title: "Lekéri az adatbázis összes tábláját és mezőjét (csak olvas). Automatikusan soha nem frissül." },
        icon("refresh", DB.busy ? "spin-ic" : ""), DB.busy ? "Lekérés…" : "Szerkezet lekérése"),
      h("button", { class: "btn sm icon", title: "Kapcsolat beállítása", onclick: () => dbConnModal() }, icon("gear"))) : null,
    fetched ? h("div", { class: "s-meta" },
      "Utolsó lekérés: " + fmtDateTime(DB.schema.fetchedAt) + (DB.schema.fetchedBy ? " · " + DB.schema.fetchedBy : ""),
      h("br"), dbTables().filter(t => !t.gone).length + " tábla/nézet" + (dbNewCount() ? " · " : ""),
      dbNewCount() ? h("span", { class: "badge new" }, dbNewCount() + " új") : null,
      v && v.snap && v.snap.shared ? h("div", {}, "Közös térkép (a közös mappában)") : null,
      v && v.snap && v.snap.warning ? h("div", { style: { color: "var(--missing)" } }, v.snap.warning) : null) : null,
    DB.schema ? h("label", { class: "s-search" }, icon("search"),
      h("input", { class: "input", placeholder: "Tábla vagy mező keresése…", value: DB.search, oninput: e => { DB.search = e.target.value; dbRender(true); } })) : null));
  side.append(DB.schema ? dbList() : h("div", { class: "dbv-list" }));
  if (DB.schema) side.append(h("div", { class: "s-foot" },
    h("button", { class: "btn sm", onclick: () => dbGroupModal() }, icon("plus"), "Csoport"),
    h("button", { class: "btn sm", onclick: () => dbAddNote() }, icon("note"), "Jegyzet")));
}
function dbNewCount() { return dbTables().filter(t => { const m = meta(tkey(t)); return m && m.new && !t.gone; }).length; }

function dbMatches(t) {
  const q = DB.search.trim().toLowerCase();
  if (!q) return true;
  const m = meta(tkey(t));
  if (tkey(t).toLowerCase().includes(q) || (m && m.desc && m.desc.toLowerCase().includes(q)) || (t.desc || "").toLowerCase().includes(q)) return true;
  return t.columns.some(c => c.name.toLowerCase().includes(q) || (c.desc || "").toLowerCase().includes(q) || ((colMeta(tkey(t), c.name) || {}).desc || "").toLowerCase().includes(q));
}

function dbList() {
  const list = h("div", { class: "dbv-list" });
  const groups = (DB.doc.groups || []).map(g => ({ g, tables: [] }));
  const none = { g: null, tables: [] };
  for (const t of dbTables()) {
    if (!dbMatches(t)) continue;
    const g = groupOf(tkey(t));
    (g ? groups.find(x => x.g.id === g.id) : none).tables.push(t);
  }
  for (const b of [...groups, none]) {
    if (!b.tables.length && !b.g) continue;
    if (!b.tables.length && DB.search) continue;
    if (b.g || groups.length) list.append(h("div", { class: "g-head", style: { "--gc": b.g ? b.g.color : "var(--border)" } },
      h("span", { class: "dot" }), b.g ? b.g.name : "Csoport nélkül", h("span", { class: "n" }, b.tables.length),
      b.g ? h("button", { class: "btn sm icon ghost", title: "Csoport szerkesztése", onclick: () => dbGroupModal(b.g) }, icon("edit")) : null));
    b.tables.sort((x, y) => tkey(x).localeCompare(tkey(y), "hu"));
    for (const t of b.tables) {
      const m = meta(tkey(t)) || {};
      list.append(h("div", { class: "dbv-item" + (nk(DB.sel) === nk(tkey(t)) ? " on" : "") + (t.gone ? " gone" : ""),
        style: b.g ? { "--gc": b.g.color } : {}, title: (dbDescInDb() ? t.desc || m.desc : m.desc || t.desc) || "",
        onclick: () => dbSelect(tkey(t), true), ondblclick: () => dbOpenDetail(tkey(t)) },
        h("div", { class: "nm" }, h("b", {}, t.name), h("span", {}, t.schema + ((dbDescInDb() ? t.desc || m.desc : m.desc || t.desc) ? " · " + (dbDescInDb() ? t.desc || m.desc : m.desc || t.desc) : ""))),
        t.view ? h("span", { class: "badge view" }, "nézet") : null,
        m.new && !t.gone ? h("span", { class: "badge new" }, "új") : null,
        t.gone ? h("span", { class: "badge gone" }, "eltűnt") : null,
        !t.view && !t.gone && t.rows >= 0 ? h("span", { class: "rc", title: fmtRows(t.rows) + " (becslés)" }, shortNum(t.rows)) : null));
    }
  }
  if (!list.children.length) list.append(h("div", { class: "small muted", style: { padding: "14px 8px" } }, DB.search ? "Nincs találat." : "Még nincs tábla."));
  return list;
}
function shortNum(n) {
  if (n >= 1e9) return (n / 1e9).toFixed(1).replace(".", ",") + " Mrd";
  if (n >= 1e6) return (n / 1e6).toFixed(1).replace(".", ",") + " M";
  if (n >= 1e4) return Math.round(n / 1e3) + " e";
  return String(n);
}

// ---- canvas ------------------------------------------------------------------
function dbRenderCanvas(cv, editing) {
  if (!cv._init) dbInitCanvas(cv);
  const v = DB.view;
  $$(".dbv-empty", cv).forEach(e => e.remove());
  const world = cv.querySelector(".dbv-world");
  clear(world);
  if (!v) return;
  if (!v.configured || !DB.schema) {
    cv.querySelector(".dbv-tools.tl").style.display = cv.querySelector(".dbv-tools.br").style.display = cv.querySelector(".dbv-hint").style.display = "none";
    cv.append(dbEmptyState(v));
    dbRenderDetail(cv, editing);
    return;
  }
  cv.querySelector(".dbv-tools.tl").style.display = cv.querySelector(".dbv-tools.br").style.display = cv.querySelector(".dbv-hint").style.display = "";
  const rel = relatedCols();
  DB.rel = rel;
  const q = DB.search.trim();
  // groups (behind everything)
  const gl = h("div", { class: "dbv-groups" });
  world.append(gl);
  // relation lines
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "dbv-svg");
  svg.setAttribute("width", "1"); svg.setAttribute("height", "1");
  world.append(svg);
  // cards
  for (const t of dbTables()) {
    const m = meta(tkey(t));
    if (!m) continue;
    world.append(dbCard(t, m, rel, q && dbMatches(t), q && !dbMatches(t)));
  }
  for (const n of DB.doc.notes || []) world.append(dbNote(n));
  dbDrawGroups();
  dbDrawLines();
  dbApplyViewport();
  dbUpdateTools();
  dbRenderDetail(cv, editing);
}

let dbMainGuard = false;
function dbInitCanvas(cv) {
  cv._init = true;
  cv.append(h("div", { class: "dbv-world" }));
  cv.append(h("div", { class: "dbv-tools tl" },
    h("button", { class: "btn sm", title: "Jegyzet hozzáadása a térkép közepére", onclick: () => dbAddNote() }, icon("note"), "Jegyzet"),
    h("button", { class: "btn sm", title: "Új csoport (pl. Önköltség, Értékesítés)", onclick: () => dbGroupModal() }, icon("tag"), "Csoport"),
    h("span", { class: "sep" }),
    h("button", { class: "btn sm", title: "Az összes tábla automatikus elrendezése csoportonként (visszavonható)", onclick: dbAutoLayout }, icon("layout"), "Elrendezés")));
  cv.append(h("div", { class: "dbv-tools br" },
    h("button", { class: "btn sm icon", id: "dbUndo", title: "Visszavonás (Ctrl+Z)", onclick: () => dbUndo(false) }, icon("undo")),
    h("button", { class: "btn sm icon", id: "dbRedo", title: "Újra (Ctrl+Y)", onclick: () => dbUndo(true) }, icon("redo")),
    h("span", { class: "sep" }),
    h("button", { class: "btn sm icon", title: "Kicsinyítés", onclick: () => dbZoomBy(1 / 1.2) }, icon("minus")),
    h("span", { class: "zoom", id: "dbZoom", title: "100%", onclick: () => dbZoomTo(1) }, "100%"),
    h("button", { class: "btn sm icon", title: "Nagyítás", onclick: () => dbZoomBy(1.2) }, icon("plus")),
    h("span", { class: "sep" }),
    h("button", { class: "btn sm icon", title: "Minden látszódjon", onclick: () => dbFit() }, icon("fit"))));
  cv.append(h("div", { class: "dbv-hint" }, "Görgő: nagyítás · üres terület húzása: mozgatás · mező szélén lévő pont húzása: kapcsolat"));
  cv.append(h("div", { class: "dbv-detail" }));

  cv.addEventListener("wheel", e => {
    if (e.target.closest(".dbv-detail, .dbv-pop, .dbv-note textarea, .dbv-empty")) return;
    e.preventDefault();
    const r = cv.getBoundingClientRect();
    const f = Math.exp(-Math.max(-60, Math.min(60, e.deltaY)) * 0.0022);
    dbZoomAt(DB.k * f, e.clientX - r.left, e.clientY - r.top);
  }, { passive: false });
  cv.addEventListener("mousedown", e => dbMouseDown(e, cv));
  // the canvas is moved only by its transform: undo any scrolling the browser
  // does on its own (focusing a field, find-in-page, scrollIntoView)
  const noScroll = el => el.addEventListener("scroll", () => { if (el.scrollLeft || el.scrollTop) { el.scrollLeft = 0; el.scrollTop = 0; } });
  noScroll(cv);
  if (DB.mounted) noScroll(DB.mounted);
  if (!dbMainGuard) {
    dbMainGuard = true; // #main scrolls normally in every other view
    const main = $("#main");
    main.addEventListener("scroll", () => { if (S.view === "db" && (main.scrollLeft || main.scrollTop)) { main.scrollLeft = 0; main.scrollTop = 0; } });
  }
  cv.addEventListener("contextmenu", e => {
    const card = e.target.closest(".dbv-card");
    if (!card) return;
    e.preventDefault();
    dbCardMenu(card.dataset.key, e.clientX, e.clientY);
  });
}

function dbEmptyState(v) {
  const box = h("div", { class: "dbv-empty" });
  if (!v.configured) {
    box.append(h("div", { class: "card" },
      h("div", { class: "hero" }, h("div", { class: "db-ic" }, icon("db")), h("h2", {}, "Adatbázis-térkép"),
        h("p", {}, "Adja meg a BI adatbázis elérését. A program lekéri a táblákat és mezőket a leírásukkal, és – ha kéri – egy tábla sorait. Az adatokba nem ír, és magától nem frissít; a leírásokat a csapat saját eljárásával (meta.leiras_beallit) menti.")),
      h("div", { class: "card-b", style: { borderTop: "1px solid var(--border)" } }, dbConnForm(true))));
  } else {
    box.append(h("div", { class: "card" },
      h("div", { class: "hero" }, h("div", { class: "db-ic" }, icon("db")), h("h2", {}, v.conn.database),
        h("p", {}, "Még nincs lekért szerkezet. A gomb lekéri az összes táblát, nézetet, mezőt, kulcsot és a sorok becsült számát (csak olvasás)."),
        h("div", { class: "row", style: { justifyContent: "center" } },
          h("button", { class: "btn primary", disabled: DB.busy, onclick: dbFetch }, icon("refresh", DB.busy ? "spin-ic" : ""), DB.busy ? "Lekérés…" : "Szerkezet lekérése"),
          h("button", { class: "btn", onclick: () => dbConnModal() }, icon("gear"), "Kapcsolat")),
        v.error ? h("p", { style: { color: "var(--missing)", marginTop: "12px" } }, v.error) : null)));
  }
  return box;
}

function dbCard(t, m, rel, match, faded) {
  const key = tkey(t);
  const g = groupOf(key);
  const rows = cardRows(t, rel);
  const fkCols = new Set(dbRelations().filter(r => nk(r.from) === nk(key)).map(r => nk(r.fromCol)));
  const card = h("div", {
    class: "dbv-card" + (nk(DB.sel) === nk(key) ? " sel" : "") + (match ? " match" : "") + (faded ? " faded" : "") + (t.gone ? " gone" : ""),
    style: Object.assign({ left: m.x + "px", top: m.y + "px" }, g ? { "--gc": g.color } : {}),
    "data-key": key,
  });
  const desc = dbDescInDb() ? t.desc || m.desc : m.desc || t.desc;
  card.append(h("div", { class: "ch", title: (desc ? desc + "\n\n" : "") + key + (t.rows >= 0 ? " · " + fmtRows(t.rows) : "") },
    t.view ? icon("eye") : icon("table"),
    h("div", { class: "tn" }, h("small", {}, t.schema + "."), t.name),
    desc ? icon("comment", "desc-ic") : null,
    m.new && !t.gone ? h("span", { class: "badge new" }, "új") : null,
    t.gone ? h("span", { class: "badge gone" }, "eltűnt") : null,
    h("button", { class: "btn sm icon ghost", style: { width: "24px", height: "24px" }, title: m.collapsed ? "Összes mező mutatása" : "Csak a kulcsok és kapcsolt mezők",
      onmousedown: e => e.stopPropagation(),
      onclick: e => { e.stopPropagation(); dbDo([{ op: "collapse", table: key, bool: !m.collapsed }], [{ op: "collapse", table: key, bool: !!m.collapsed }], "összecsukás"); } },
      icon(m.collapsed ? "expand" : "collapse"))));
  for (const c of rows.cols) {
    const cm = colMeta(key, c.name) || {};
    const isFk = fkCols.has(nk(c.name));
    card.append(h("div", { class: "cr", "data-col": c.name, title: c.name + "  " + c.type + (c.nullable ? " (NULL lehet)" : "") + ((dbDescInDb() ? c.desc || cm.desc : cm.desc || c.desc) ? "\n" + (dbDescInDb() ? c.desc || cm.desc : cm.desc || c.desc) : "") },
      h("span", { class: "port l", "data-side": "l" }), h("span", { class: "port r", "data-side": "r" }),
      h("span", { class: "k" + (c.pk ? " pk" : isFk ? " fk" : "") }, c.pk ? icon("key") : isFk ? icon("link") : ""),
      h("span", { class: "cn" + (c.pk ? " pk" : "") }, c.name),
      cm.new ? h("span", { class: "newc" }, "ÚJ") : null,
      cm.desc || c.desc ? h("span", { class: "cd" }) : null,
      h("span", { class: "ct" }, c.type + (c.nullable ? "?" : ""))));
  }
  if (rows.more) card.append(h("div", { class: "more", onmousedown: e => e.stopPropagation(),
    onclick: e => { e.stopPropagation(); dbDo([{ op: "collapse", table: key, bool: false }], [{ op: "collapse", table: key, bool: true }], "kinyitás"); } },
    "+ " + rows.more + " további mező"));
  if (!rows.cols.length && !rows.more) card.append(h("div", { class: "more" }, "nincs mező"));
  card.append(h("div", { class: "cf" }));
  return card;
}

function dbNote(n) {
  const el = h("div", { class: "dbv-note", "data-note": n.id, style: { left: n.x + "px", top: n.y + "px", width: n.w + "px", minHeight: n.h + "px", "--nc": n.color || NOTE_COLORS[0] },
    ondblclick: e => { e.stopPropagation(); dbEditNote(el, n); } },
    h("div", { class: "nb" },
      h("button", { title: "Szín", onmousedown: e => e.stopPropagation(), onclick: e => { e.stopPropagation(); const i = (NOTE_COLORS.indexOf(n.color) + 1) % NOTE_COLORS.length; dbDo([{ op: "noteSave", note: Object.assign({}, n, { color: NOTE_COLORS[i] }) }], [{ op: "noteSave", note: n }], "jegyzet színe"); } }, icon("tag")),
      h("button", { title: "Szerkesztés", onmousedown: e => e.stopPropagation(), onclick: e => { e.stopPropagation(); dbEditNote(el, n); } }, icon("edit")),
      h("button", { title: "Törlés", onmousedown: e => e.stopPropagation(), onclick: e => { e.stopPropagation(); dbDo([{ op: "noteDelete", id: n.id }], [{ op: "noteSave", note: n }], "jegyzet törlése"); } }, icon("trash"))),
    n.text || h("span", { class: "muted" }, "Üres jegyzet – dupla kattintás a szerkesztéshez"),
    n.by ? h("span", { class: "by" }, n.by) : null);
  return el;
}
function dbEditNote(el, n) {
  const ta = h("textarea", { placeholder: "Jegyzet…", onmousedown: e => e.stopPropagation() });
  ta.value = n.text || "";
  clear(el).append(ta);
  ta.focus({ preventScroll: true });
  ta.addEventListener("keydown", e => { if (e.key === "Escape") { ta.value = n.text || ""; ta.blur(); } e.stopPropagation(); });
  ta.addEventListener("blur", () => {
    const text = ta.value.trim();
    if (!text && !n.text) { dbDo([{ op: "noteDelete", id: n.id }], null, ""); return; } // an empty new note is dropped
    if (text === (n.text || "")) { dbRender(true); return; }
    dbDo([{ op: "noteSave", note: Object.assign({}, n, { text }) }], [{ op: "noteSave", note: n }], "jegyzet");
  });
}
function dbAddNote() {
  const cv = DB.mounted && DB.mounted.querySelector(".dbv-canvas");
  if (!cv || !DB.schema) return;
  const r = cv.getBoundingClientRect();
  const note = { id: rid(), text: "", color: NOTE_COLORS[0], x: Math.round((r.width / 2 - DB.tx) / DB.k - 110), y: Math.round((r.height / 2 - DB.ty) / DB.k - 60), w: 220, h: 120 };
  dbDo([{ op: "noteSave", note }], [{ op: "noteDelete", id: note.id }], "új jegyzet").then(ok => {
    if (!ok) return;
    const el = DB.mounted.querySelector(`.dbv-note[data-note="${note.id}"]`);
    const n = (DB.doc.notes || []).find(x => x.id === note.id);
    if (el && n) dbEditNote(el, n);
  });
}

// group areas: bounding box of the member cards
function dbGroupBoxes() {
  const out = [];
  for (const g of DB.doc.groups || []) {
    let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity, n = 0;
    for (const t of dbTables()) {
      const m = meta(tkey(t));
      if (!m || m.group !== g.id) continue;
      n++;
      x0 = Math.min(x0, m.x); y0 = Math.min(y0, m.y);
      x1 = Math.max(x1, m.x + CARD.W); y1 = Math.max(y1, m.y + cardHeight(t, DB.rel || relatedCols()));
    }
    if (!n) continue;
    out.push({ g, n, x: x0 - CARD.GROUP_PAD + 8, y: y0 - CARD.GROUP_TOP, w: x1 - x0 + 2 * (CARD.GROUP_PAD - 8), h: y1 - y0 + CARD.GROUP_TOP + CARD.GROUP_PAD - 8 });
  }
  return out;
}
function dbDrawGroups() {
  const gl = DB.mounted.querySelector(".dbv-groups");
  if (!gl) return;
  clear(gl);
  for (const b of dbGroupBoxes()) {
    gl.append(h("div", { class: "dbv-group", "data-group": b.g.id, style: { left: b.x + "px", top: b.y + "px", width: b.w + "px", height: b.h + "px", "--gc": b.g.color } },
      h("div", { class: "gt", title: "Húzással a csoport összes táblája mozog · dupla kattintás: szerkesztés", ondblclick: () => dbGroupModal(b.g) },
        h("span", { class: "dot" }), b.g.name, h("span", { style: { opacity: .6, fontWeight: 600 } }, b.n))));
  }
}

// ---- relation lines ------------------------------------------------------------
function dbAnchor(key, col) {
  const t = dbTable(key), m = meta(key);
  if (!t || !m) return null;
  const rows = cardRows(t, DB.rel || relatedCols());
  const i = rows.cols.findIndex(c => nk(c.name) === nk(col));
  return { x0: m.x, x1: m.x + CARD.W, y: m.y + (i >= 0 ? CARD.HEAD + i * CARD.ROW + CARD.ROW / 2 : CARD.HEAD / 2 + 2), cx: m.x + CARD.W / 2 };
}
function roundedPath(pts, r) {
  let d = "M" + pts[0][0] + " " + pts[0][1];
  for (let i = 1; i < pts.length - 1; i++) {
    const [px, py] = pts[i - 1], [x, y] = pts[i], [nx, ny] = pts[i + 1];
    const d1 = Math.hypot(x - px, y - py), d2 = Math.hypot(nx - x, ny - y);
    const rr = Math.min(r, d1 / 2, d2 / 2);
    if (rr < 0.5) { d += " L" + x + " " + y; continue; }
    const ax = x - (x - px) / d1 * rr, ay = y - (y - py) / d1 * rr;
    const bx = x + (nx - x) / d2 * rr, by = y + (ny - y) / d2 * rr;
    d += " L" + ax + " " + ay + " Q" + x + " " + y + " " + bx + " " + by;
  }
  const l = pts[pts.length - 1];
  return d + " L" + l[0] + " " + l[1];
}
function relPath(a, b, self) {
  let xa, sa, xb, sb, pts;
  if (!self && a.x1 + 40 <= b.x0) { xa = a.x1; sa = 1; xb = b.x0; sb = -1; }
  else if (!self && b.x1 + 40 <= a.x0) { xa = a.x0; sa = -1; xb = b.x1; sb = 1; }
  else { xa = a.x1; sa = 1; xb = b.x1; sb = 1; }
  const o = 22;
  if (sa !== sb) {
    const mid = Math.round(((xa + sa * o) + (xb + sb * o)) / 2);
    pts = [[xa, a.y], [mid, a.y], [mid, b.y], [xb, b.y]];
  } else {
    const mx = Math.max(xa, xb) + o + 14;
    pts = [[xa, a.y], [mx, a.y], [mx, b.y], [xb, b.y]];
  }
  // "many" end: crow's foot at the referencing column; "one" end: a bar
  const crow = `M${xa + sa * 13} ${a.y} L${xa} ${a.y - 6} M${xa + sa * 13} ${a.y} L${xa} ${a.y + 6}`;
  const bar = `M${xb + sb * 9} ${b.y - 6} L${xb + sb * 9} ${b.y + 6}`;
  return { line: roundedPath(pts, 9), marks: crow + " " + bar };
}
let dbLinesRaf = 0;
function dbDrawLinesSoon() { if (!dbLinesRaf) dbLinesRaf = requestAnimationFrame(() => { dbLinesRaf = 0; dbDrawLines(); dbDrawGroups(); }); }
function dbDrawLines() {
  const svg = DB.mounted && DB.mounted.querySelector(".dbv-svg");
  if (!svg) return;
  const hot = new Set([nk(DB.sel), nk(DB.hover)].filter(Boolean));
  let s = "";
  for (const r of dbRelations()) {
    const a = dbAnchor(r.from, r.fromCol), b = dbAnchor(r.to, r.toCol);
    if (!a || !b) continue;
    const p = relPath(a, b, nk(r.from) === nk(r.to));
    const cls = (hot.has(nk(r.from)) || hot.has(nk(r.to)) ? "hl " : "") + (DB.selRel === r.id ? "sel" : "");
    s += `<g class="${cls}" data-rel="${escAttr(r.id)}"><path class="rel${r.manual ? " manual" : ""}" d="${p.line} ${p.marks}"/><path class="hit" d="${p.line}"/></g>`;
  }
  svg.innerHTML = s;
  svg.classList.toggle("dim", hot.size > 0 || !!DB.selRel);
}
function escAttr(s) { return String(s).replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;"); }

// ---- viewport ----------------------------------------------------------------
function dbVpKey() { const c = DB.view && DB.view.conn; return "dbvp:" + (c ? nk(c.server) + "/" + nk(c.database) : ""); }
function dbRestoreViewport() {
  try {
    const v = JSON.parse(localStorage.getItem(dbVpKey()) || "null");
    if (v && isFinite(v.k)) { DB.k = v.k; DB.tx = v.tx; DB.ty = v.ty; DB.fitted = true; }
  } catch (e) { /* no storage */ }
}
let dbVpTimer = 0;
function dbApplyViewport() {
  const w = DB.mounted && DB.mounted.querySelector(".dbv-world");
  if (!w) return;
  w.style.transform = `translate(${DB.tx}px, ${DB.ty}px) scale(${DB.k})`;
  const cv = DB.mounted.querySelector(".dbv-canvas");
  cv.style.backgroundSize = (22 * DB.k) + "px " + (22 * DB.k) + "px";
  cv.style.backgroundPosition = DB.tx + "px " + DB.ty + "px";
  const z = $("#dbZoom");
  if (z) z.textContent = Math.round(DB.k * 100) + "%";
  clearTimeout(dbVpTimer);
  dbVpTimer = setTimeout(() => { try { localStorage.setItem(dbVpKey(), JSON.stringify({ k: DB.k, tx: DB.tx, ty: DB.ty })); } catch (e) { /* ignore */ } }, 400);
}
function dbZoomAt(k, sx, sy) {
  k = Math.max(0.1, Math.min(2.5, k));
  DB.tx = sx - (sx - DB.tx) * (k / DB.k);
  DB.ty = sy - (sy - DB.ty) * (k / DB.k);
  DB.k = k;
  dbApplyViewport();
}
function dbZoomBy(f) { const cv = DB.mounted.querySelector(".dbv-canvas"); dbZoomAt(DB.k * f, cv.clientWidth / 2, cv.clientHeight / 2); }
function dbZoomTo(k) { const cv = DB.mounted.querySelector(".dbv-canvas"); dbZoomAt(k, cv.clientWidth / 2, cv.clientHeight / 2); }
function dbBounds(keys) {
  let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  const rel = DB.rel || relatedCols();
  for (const t of dbTables()) {
    const m = meta(tkey(t));
    if (!m || (keys && !keys.has(nk(tkey(t))))) continue;
    x0 = Math.min(x0, m.x); y0 = Math.min(y0, m.y); x1 = Math.max(x1, m.x + CARD.W); y1 = Math.max(y1, m.y + cardHeight(t, rel));
  }
  if (!keys) for (const n of DB.doc.notes || []) { x0 = Math.min(x0, n.x); y0 = Math.min(y0, n.y); x1 = Math.max(x1, n.x + n.w); y1 = Math.max(y1, n.y + n.h); }
  return isFinite(x0) ? { x0, y0, x1, y1 } : null;
}
function dbFit() {
  const cv = DB.mounted && DB.mounted.querySelector(".dbv-canvas");
  if (!cv || !DB.doc) return;
  const b = dbBounds();
  if (!b) return;
  const pad = 70, w = cv.clientWidth - (DB.detailOpen ? 430 : 0);
  const k = Math.max(0.12, Math.min(1.1, Math.min((w - 2 * pad) / (b.x1 - b.x0 + 80), (cv.clientHeight - 2 * pad) / (b.y1 - b.y0 + 80))));
  DB.k = k;
  DB.tx = (w - (b.x1 - b.x0) * k) / 2 - b.x0 * k;
  DB.ty = (cv.clientHeight - (b.y1 - b.y0) * k) / 2 - b.y0 * k;
  dbApplyViewport();
}
function dbCenterOn(key) {
  const cv = DB.mounted.querySelector(".dbv-canvas");
  const m = meta(key), t = dbTable(key);
  if (!m || !t) return;
  const w = cv.clientWidth - (DB.detailOpen ? 430 : 0);
  if (DB.k < 0.6) DB.k = 0.8;
  DB.tx = w / 2 - (m.x + CARD.W / 2) * DB.k;
  DB.ty = cv.clientHeight / 2 - (m.y + Math.min(cardHeight(t, DB.rel || relatedCols()), 400) / 2) * DB.k;
  dbApplyViewport();
}
function dbUpdateTools() {
  const u = $("#dbUndo"), r = $("#dbRedo");
  if (u) { u.disabled = !DB.undo.length; u.title = DB.undo.length ? "Visszavonás: " + DB.undo[DB.undo.length - 1].label + " (Ctrl+Z)" : "Visszavonás (Ctrl+Z)"; }
  if (r) { r.disabled = !DB.redo.length; r.title = DB.redo.length ? "Újra: " + DB.redo[DB.redo.length - 1].label + " (Ctrl+Y)" : "Újra (Ctrl+Y)"; }
}

// ---- mouse: pan, drag, link -----------------------------------------------------
function dbMouseDown(e, cv) {
  if (e.button !== 0 && e.button !== 1) return;
  if (e.target.closest(".dbv-tools, .dbv-detail, .dbv-pop, .dbv-empty")) return;
  if (e.target.closest("button, textarea, input")) return;
  closeMenus();
  const toWorld = (cx, cy) => { const r = cv.getBoundingClientRect(); return { x: (cx - r.left - DB.tx) / DB.k, y: (cy - r.top - DB.ty) / DB.k }; };
  const sx = e.clientX, sy = e.clientY;
  let moved = false;
  const hit = e.target.closest("[data-rel]");
  const port = e.target.closest(".port");
  const card = e.target.closest(".dbv-card");
  const gt = e.target.closest(".gt");
  const note = e.target.closest(".dbv-note");
  let onMove, onUp;
  if (e.button === 0 && hit && e.target.classList.contains("hit")) {
    e.preventDefault();
    dbSelectRel(hit.dataset.rel, e.clientX, e.clientY);
    return;
  }
  if (e.button === 0 && port && card) {
    // draw a relation: from this column to another one
    e.preventDefault();
    const src = { key: card.dataset.key, col: port.closest(".cr").dataset.col };
    const a = dbAnchor(src.key, src.col);
    const start = { x: port.dataset.side === "l" ? a.x0 : a.x1, y: a.y };
    cv.classList.add("linking");
    const svg = cv.querySelector(".dbv-svg");
    const draft = document.createElementNS("http://www.w3.org/2000/svg", "path");
    draft.setAttribute("class", "draft");
    svg.append(draft);
    let target = null;
    onMove = ev => {
      const p = toWorld(ev.clientX, ev.clientY);
      draft.setAttribute("d", `M${start.x} ${start.y} L${p.x} ${p.y}`);
      $$(".cr.target", cv).forEach(x => x.classList.remove("target"));
      const el = document.elementFromPoint(ev.clientX, ev.clientY);
      const row = el && el.closest(".dbv-card .cr");
      target = row ? { key: row.closest(".dbv-card").dataset.key, col: row.dataset.col } : null;
      if (target && nk(target.key) === nk(src.key) && nk(target.col) === nk(src.col)) target = null;
      if (row && target) row.classList.add("target");
    };
    onUp = () => {
      cv.classList.remove("linking");
      draft.remove();
      $$(".cr.target", cv).forEach(x => x.classList.remove("target"));
      if (target) dbAddRelation(src, target, true);
    };
  } else if (e.button === 0 && (card || gt || note)) {
    e.preventDefault();
    let items = [];
    if (card) {
      const key = card.dataset.key;
      items = [{ key, m: meta(key), el: card }];
    } else if (gt) {
      const gid = gt.closest(".dbv-group").dataset.group;
      for (const t of dbTables()) {
        const m = meta(tkey(t));
        if (m && m.group === gid) items.push({ key: tkey(t), m, el: cv.querySelector(`.dbv-card[data-key="${CSS.escape(tkey(t))}"]`) });
      }
    } else {
      const n = DB.doc.notes.find(x => x.id === note.dataset.note);
      items = [{ key: "note:" + n.id, m: n, el: note }];
    }
    items.forEach(it => { it.ox = it.m.x; it.oy = it.m.y; });
    DB.drag = true;
    onMove = ev => {
      const dx = (ev.clientX - sx) / DB.k, dy = (ev.clientY - sy) / DB.k;
      if (!moved && Math.hypot(ev.clientX - sx, ev.clientY - sy) < 4) return;
      moved = true;
      for (const it of items) {
        it.m.x = Math.round(it.ox + dx); it.m.y = Math.round(it.oy + dy);
        if (it.el) { it.el.style.left = it.m.x + "px"; it.el.style.top = it.m.y + "px"; }
      }
      dbDrawLinesSoon();
    };
    onUp = () => {
      DB.drag = false;
      if (!moved) {
        if (card) dbSelect(card.dataset.key, false);
        return;
      }
      const pos = items.map(it => ({ table: it.key, x: it.m.x, y: it.m.y }));
      const old = items.map(it => ({ table: it.key, x: it.ox, y: it.oy }));
      dbDo([{ op: "move", pos }], [{ op: "move", pos: old }], items.length > 1 ? "csoport mozgatása" : "mozgatás");
    };
  } else {
    // pan
    e.preventDefault();
    const tx = DB.tx, ty = DB.ty;
    cv.classList.add("panning");
    onMove = ev => {
      if (!moved && Math.hypot(ev.clientX - sx, ev.clientY - sy) < 3) return;
      moved = true;
      DB.tx = tx + ev.clientX - sx; DB.ty = ty + ev.clientY - sy;
      dbApplyViewport();
    };
    onUp = () => {
      cv.classList.remove("panning");
      if (!moved) { // click on the empty map: deselect
        $$(".dbv-pop").forEach(p => p.remove());
        if (DB.sel || DB.selRel) { DB.sel = null; DB.selRel = null; DB.detailOpen = false; dbRender(true); }
      }
    };
  }
  const up = ev => { document.removeEventListener("mousemove", onMove); document.removeEventListener("mouseup", up); onUp(ev); };
  document.addEventListener("mousemove", onMove);
  document.addEventListener("mouseup", up);
}

// hover highlights the relations of a card
document.addEventListener("mouseover", e => {
  if (S.view !== "db" || DB.drag) return;
  const card = e.target.closest && e.target.closest(".dbv-card");
  const key = card ? card.dataset.key : null;
  if (key !== DB.hover) { DB.hover = key; if (dbMounted()) dbDrawLines(); }
});

function dbSelect(key, center) {
  DB.sel = key; DB.selRel = null; DB.detailOpen = true;
  $$(".dbv-pop").forEach(p => p.remove());
  dbRender(false);
  if (center) dbCenterOn(key);
}
function dbOpenDetail(key) { dbSelect(key, true); }

async function dbAddRelation(src, dst, fromDrag) {
  let a = src, b = dst;
  const ta = dbTable(a.key), tb = dbTable(b.key);
  const ca = ta && ta.columns.find(c => nk(c.name) === nk(a.col)), cb = tb && tb.columns.find(c => nk(c.name) === nk(b.col));
  // the key side is the "one" side: dragging from a key to a foreign key works too
  if (fromDrag && ca && cb && ca.pk && !cb.pk) { const x = a; a = b; b = x; }
  const id = rid();
  return dbDo([{ op: "relAdd", relation: { id, from: a.key, fromCol: a.col, to: b.key, toCol: b.col } }], [{ op: "relDelete", id }],
    "kapcsolat " + a.col + " → " + b.col);
}

function dbAutoLayout() {
  const old = dbTables().map(t => { const m = meta(tkey(t)); return m ? { table: tkey(t), x: m.x, y: m.y } : null; }).filter(Boolean);
  dbDo([{ op: "layout" }], [{ op: "move", pos: old }], "automatikus elrendezés").then(ok => { if (ok) setTimeout(dbFit, 30); });
}

document.addEventListener("keydown", e => {
  if (S.view !== "db" || $(".overlay")) return;
  const inField = /INPUT|TEXTAREA|SELECT/.test((document.activeElement || {}).tagName || "");
  if (e.ctrlKey && !inField && (e.key === "z" || e.key === "Z")) { e.preventDefault(); dbUndo(e.shiftKey); }
  else if (e.ctrlKey && !inField && (e.key === "y" || e.key === "Y")) { e.preventDefault(); dbUndo(true); }
  else if (e.key === "Escape" && !inField) {
    if ($(".dbv-pop")) { $$(".dbv-pop").forEach(p => p.remove()); DB.selRel = null; dbDrawLines(); }
    else if (DB.detailOpen) { DB.detailOpen = false; dbRender(true); }
  } else if (e.key === "Delete" && !inField && DB.selRel) {
    const r = (DB.doc.relations || []).find(x => x.id === DB.selRel);
    if (r) { $$(".dbv-pop").forEach(p => p.remove()); DB.selRel = null; dbDo([{ op: "relDelete", id: r.id }], [{ op: "relAdd", relation: r }], "kapcsolat törlése"); }
  }
});

// ---- relation popover ------------------------------------------------------------
function dbSelectRel(id, cx, cy) {
  DB.selRel = id;
  dbDrawLines();
  $$(".dbv-pop").forEach(p => p.remove());
  const r = dbRelations().find(x => x.id === id);
  if (!r) return;
  const cv = DB.mounted.querySelector(".dbv-canvas");
  const rc = cv.getBoundingClientRect();
  const close = () => { pop.remove(); DB.selRel = null; dbDrawLines(); };
  const pop = h("div", { class: "dbv-pop", onmousedown: e => e.stopPropagation() },
    h("div", { class: "row" }, h("b", { class: "grow" }, r.manual ? "Kapcsolat" : "Idegen kulcs (adatbázis)"),
      h("button", { class: "btn sm icon ghost", onclick: close }, icon("close"))),
    h("div", { class: "t mono" }, r.from + "." + r.fromCol), h("div", { class: "small muted" }, "↓ hivatkozik (sok → egy)"),
    h("div", { class: "t mono" }, r.to + "." + r.toCol));
  if (r.manual) {
    const note = h("input", { class: "input", placeholder: "Megjegyzés a kapcsolathoz (nem kötelező)", value: r.note || "" });
    note.addEventListener("change", () => dbDo([{ op: "relNote", id: r.id, text: note.value }], [{ op: "relNote", id: r.id, text: r.note || "" }], "kapcsolat megjegyzése"));
    pop.append(note, h("div", { class: "small muted" }, (r.by ? r.by + " · " : "") + (parseT(r.at) ? fmtDateTime(r.at) : "")),
      h("div", { class: "row" }, h("div", { class: "grow" }),
        h("button", { class: "btn sm danger", onclick: () => { pop.remove(); DB.selRel = null; dbDo([{ op: "relDelete", id: r.id }], [{ op: "relAdd", relation: r }], "kapcsolat törlése"); } }, icon("trash"), "Törlés")));
  } else {
    pop.append(h("div", { class: "small muted" }, r.fk.name + (r.fk.fromCols.length > 1 ? " · összetett kulcs: " + r.fk.fromCols.join(", ") : "")),
      h("div", { class: "row" }, h("div", { class: "grow" }),
        h("button", { class: "btn sm", onclick: () => { pop.remove(); DB.selRel = null; dbDo([{ op: "fkHide", id: r.fk.name, bool: true }], [{ op: "fkHide", id: r.fk.name, bool: false }], "idegen kulcs elrejtése"); } }, "Elrejtés a térképről")));
  }
  cv.append(pop);
  pop.style.left = Math.max(8, Math.min(rc.width - 310, cx - rc.left + 12)) + "px";
  pop.style.top = Math.max(8, Math.min(rc.height - pop.offsetHeight - 8, cy - rc.top + 12)) + "px";
}

// ---- card menu -------------------------------------------------------------------
function dbCardMenu(key, x, y) {
  const t = dbTable(key), m = meta(key);
  if (!t || !m) return;
  const anchor = { getBoundingClientRect: () => ({ left: x, right: x + 220, top: y, bottom: y }) };
  const groups = DB.doc.groups || [];
  showMenu(anchor, [
    { label: "Részletek és leírás", icon: "info", run: () => dbSelect(key, false) },
    !t.gone && { label: "Első 100 sor", icon: "rows", run: () => dbOpenGrid(key, false) },
    !t.gone && { label: "Teljes tábla", icon: "table", run: () => dbOpenGrid(key, true) },
    null,
    { label: m.collapsed ? "Összes mező mutatása" : "Csak kulcsok és kapcsolt mezők", icon: m.collapsed ? "expand" : "collapse",
      run: () => dbDo([{ op: "collapse", table: key, bool: !m.collapsed }], [{ op: "collapse", table: key, bool: !!m.collapsed }], "összecsukás") },
    ...groups.filter(g => g.id !== m.group).map(g => ({ label: "Csoportba: " + g.name, icon: "tag",
      run: () => dbDo([{ op: "setGroup", id: g.id, tables: [key] }], [{ op: "setGroup", id: m.group || "", tables: [key] }], "csoport") })),
    m.group && { label: "Kivétel a csoportból", icon: "close", run: () => dbDo([{ op: "setGroup", id: "", tables: [key] }], [{ op: "setGroup", id: m.group, tables: [key] }], "csoport") },
    { label: "Új csoport ezzel a táblával…", icon: "plus", run: () => dbGroupModal(null, [key]) },
    t.gone && null,
    t.gone && { label: "Eltávolítás a térképről (leírásaival együtt)", icon: "trash", danger: true, run: () => dbForget(key) },
  ].filter(x => x !== false && x !== undefined));
}
async function dbForget(key) {
  if (!await confirmBox("Eltávolítás a térképről", "A(z) " + key + " tábla már nincs az adatbázisban. Eltávolítja a térképről a leírásaival és kapcsolataival együtt?\n\nEz nem vonható vissza.", "Eltávolítás", true)) return;
  if (nk(DB.sel) === nk(key)) { DB.sel = null; DB.detailOpen = false; }
  dbDo([{ op: "forget", table: key }], null, "eltávolítás");
}

// ---- detail panel ------------------------------------------------------------------
function dbRenderDetail(cv, editing) {
  const p = cv.querySelector(".dbv-detail");
  const t = DB.sel && dbTable(DB.sel);
  if (!DB.detailOpen || !t) { p.classList.remove("open"); return; }
  if (editing && p.contains(document.activeElement)) return; // do not disturb typing
  const key = tkey(t), m = meta(key) || {};
  const scroll = p.querySelector(".d-body") ? p.querySelector(".d-body").scrollTop : 0;
  clear(p);
  const g = groupOf(key);
  p.append(h("div", { class: "d-top" },
    h("div", { class: "row" },
      h("span", { class: "badge " + (t.view ? "view" : "fk") }, t.view ? "nézet" : "tábla"),
      m.new && !t.gone ? h("span", { class: "badge new" }, "új") : null,
      t.gone ? h("span", { class: "badge gone" }, "eltűnt az adatbázisból") : null,
      g ? h("span", { class: "badge", style: { background: g.color + "26", color: g.color } }, g.name) : null,
      h("div", { class: "grow" }),
      h("button", { class: "btn sm icon ghost", title: "Bezárás (Esc)", onclick: () => { DB.detailOpen = false; dbRender(true); } }, icon("close"))),
    h("h2", { class: "selectable" }, h("small", {}, t.schema + "."), t.name),
    h("div", { class: "small muted" }, [t.rows >= 0 ? fmtRows(t.rows) + " (becslés)" : null, t.columns.length + " mező",
      parseT(t.modified) ? "szerkezet módosítva: " + fmtDateTime(t.modified) : null].filter(Boolean).join(" · ")),
    !t.gone ? h("div", { class: "row wrap" },
      h("button", { class: "btn sm primary", onclick: () => dbOpenGrid(key, false) }, icon("rows"), "Első 100 sor"),
      h("button", { class: "btn sm", onclick: () => dbOpenGrid(key, true), title: "Az egész tábla (legfeljebb a beállított sorszámig)" }, icon("table"), "Teljes tábla"),
      h("button", { class: "btn sm", onclick: () => dbCenterOn(key) }, icon("fit"), "Mutasd")) :
      h("button", { class: "btn sm danger", onclick: () => dbForget(key) }, icon("trash"), "Eltávolítás a térképről")));
  const body = h("div", { class: "d-body" });
  p.append(body);

  // description: in the database (team procedure) or – switched off – on the map only
  const inDb = dbDescInDb();
  body.append(h("div", { class: "section-t row" }, h("span", { class: "grow" }, "Leírás"),
    inDb ? h("span", { class: "badge fk", title: "Az adatbázisban tárolva (MS_Description); mentés: " + dbDescProc() }, "adatbázisban") : null,
    !t.gone ? h("button", { class: "btn sm ghost", style: { height: "22px", textTransform: "none", letterSpacing: 0 }, title: "A tábla és a mezők leírásának újraolvasása az adatbázisból", onclick: () => dbReadDesc(key) }, icon("refresh"), "Újraolvasás") : null));
  const ta = h("textarea", { class: "input", placeholder: "Mit tartalmaz, melyik cégre vonatkozik, és mi egy sor (pl. „cikk × üzletág × időszak szinten egy sor”)", disabled: inDb && t.gone });
  if (inDb) {
    ta.value = t.desc || "";
    ta.addEventListener("blur", () => { const v = ta.value.trim(), cur = dbCurDesc(key, ""); if (v !== cur) dbSaveDesc(key, "", v, cur, ta); });
    body.append(ta);
    if (m.desc && m.desc !== t.desc) body.append(dbOldDesc(m.desc, () => { ta.value = m.desc; dbSaveDesc(key, "", m.desc, t.desc || "", ta); }));
  } else {
    ta.value = m.desc || "";
    ta.addEventListener("blur", () => {
      const v = ta.value.trim();
      if (v !== (m.desc || "")) dbDo([{ op: "tableDesc", table: key, text: v }], [{ op: "tableDesc", table: key, text: m.desc || "" }], "táblaleírás");
    });
    body.append(ta);
    if (m.descBy) body.append(h("div", { class: "dbdesc" }, "Utoljára: " + m.descBy + (parseT(m.descAt) ? " · " + fmtDateTime(m.descAt) : "")));
    if (t.desc) body.append(h("div", { class: "dbdesc" }, "Az adatbázisban: " + t.desc));
  }

  // group
  const groups = DB.doc.groups || [];
  const sel = h("select", { class: "input", onchange: e => {
    const v = e.target.value;
    if (v === "__new") { e.target.value = m.group || ""; dbGroupModal(null, [key]); return; }
    dbDo([{ op: "setGroup", id: v, tables: [key] }], [{ op: "setGroup", id: m.group || "", tables: [key] }], "csoport");
  } }, h("option", { value: "" }, "– nincs csoportban –"), groups.map(gr => h("option", { value: gr.id, selected: gr.id === m.group }, gr.name)), h("option", { value: "__new" }, "+ Új csoport…"));
  body.append(h("div", { class: "section-t" }, "Csoport"), sel);

  // columns
  body.append(h("div", { class: "section-t row" }, h("span", { class: "grow" }, "Mezők (" + t.columns.length + ")"),
    h("span", { class: "muted", style: { cursor: "help", textTransform: "none", letterSpacing: 0, fontWeight: 400 }, title: DESC_TIPS }, "ⓘ leírási szabályok")));
  const rels = dbRelations();
  const fkCols = new Set(rels.filter(r => nk(r.from) === nk(key)).map(r => nk(r.fromCol)));
  const cols = h("div", { class: "dbv-cols" });
  for (const c of t.columns) {
    const cm = colMeta(key, c.name) || {};
    const inp = h("input", { placeholder: "Leírás hozzáadása…", value: (inDb ? c.desc : cm.desc) || "", disabled: inDb && t.gone,
      title: inDb ? "Az adatbázisban tárolt leírás – Enter vagy kilépés: mentés" : cm.descBy ? "Utoljára: " + cm.descBy : "" });
    inp.addEventListener("keydown", e => { if (e.key === "Enter") inp.blur(); if (e.key === "Escape") { inp.value = (inDb ? c.desc : cm.desc) || ""; inp.blur(); e.stopPropagation(); } });
    inp.addEventListener("blur", () => {
      const v = inp.value.trim();
      if (inDb) { const cur = dbCurDesc(key, c.name); if (v !== cur) dbSaveDesc(key, c.name, v, cur, inp); return; }
      if (v !== (cm.desc || "")) dbDo([{ op: "colDesc", table: key, column: c.name, text: v }], [{ op: "colDesc", table: key, column: c.name, text: cm.desc || "" }], "mezőleírás: " + c.name);
    });
    const flags = [c.nullable ? "NULL lehet" : null, c.identity ? "identity" : null, c.computed ? "számított" : null].filter(Boolean).join(", ");
    cols.append(h("div", { class: "dbv-col" },
      h("span", { class: "k" + (c.pk ? " pk" : fkCols.has(nk(c.name)) ? " fk" : "") }, c.pk ? icon("key") : fkCols.has(nk(c.name)) ? icon("link") : ""),
      h("span", { class: "n selectable" }, c.name, cm.new ? h("span", { class: "badge new", style: { marginLeft: "6px" } }, "új") : null),
      h("span", { class: "t", title: flags }, c.type + (c.nullable ? "?" : "")),
      h("div", { class: "ed" }, inp),
      !inDb && c.desc ? h("div", { class: "dbd" }, "Az adatbázisban: " + c.desc) : null,
      inDb && cm.desc && cm.desc !== c.desc ? h("div", { class: "dbd" }, dbOldDesc(cm.desc, () => { inp.value = cm.desc; dbSaveDesc(key, c.name, cm.desc, c.desc || "", inp); })) : null));
  }
  body.append(cols);

  // relations
  const mine = rels.filter(r => nk(r.from) === nk(key) || nk(r.to) === nk(key));
  body.append(h("div", { class: "section-t" }, "Kapcsolatok (" + mine.length + ")"));
  if (!mine.length) body.append(h("div", { class: "small muted" }, "Még nincs kapcsolat. Húzza egy mező szélén lévő pontot egy másik tábla mezőjére, vagy adja hozzá lent."));
  for (const r of mine) {
    const out = nk(r.from) === nk(key);
    body.append(h("div", { class: "dbv-rel", onclick: () => { DB.selRel = r.id; dbDrawLines(); } },
      icon(out ? "link" : "key"),
      h("div", { class: "grow mono" }, out ? r.fromCol : r.from + "." + r.fromCol, h("span", { class: "arrow" }, " → "), out ? r.to + "." + r.toCol : r.toCol,
        r.note ? h("div", { class: "small muted", style: { fontFamily: "var(--font)" } }, r.note) : null),
      r.manual ? null : h("span", { class: "badge fk", title: r.fk.name }, "adatbázis"),
      r.manual ? h("button", { class: "btn sm icon ghost", title: "Kapcsolat törlése", onclick: ev => { ev.stopPropagation(); dbDo([{ op: "relDelete", id: r.id }], [{ op: "relAdd", relation: r }], "kapcsolat törlése"); } }, icon("trash")) : null));
  }
  const hidden = ((DB.schema && DB.schema.fks) || []).filter(fk => (DB.doc.hiddenFks || []).includes(fk.name) && (nk(fk.from) === nk(key) || nk(fk.to) === nk(key)));
  for (const fk of hidden) body.append(h("div", { class: "dbv-rel" }, h("div", { class: "grow small muted" }, "Elrejtett idegen kulcs: " + fk.name),
    h("button", { class: "btn sm", onclick: () => dbDo([{ op: "fkHide", id: fk.name, bool: false }], [{ op: "fkHide", id: fk.name, bool: true }], "idegen kulcs megjelenítése") }, "Mutasd")));

  // suggestions: same column name as the single-column key of another table
  if (!t.gone) {
    const sugg = dbSuggestions(t, rels);
    if (sugg.length) {
      body.append(h("div", { class: "section-t" }, "Javasolt kapcsolatok"));
      const box = h("div", { class: "dbv-sugg" });
      for (const s of sugg) box.append(h("div", { class: "dbv-rel" }, icon("link"),
        h("div", { class: "grow mono" }, (nk(s.from.key) === nk(key) ? "" : s.from.key + ".") + s.from.col, h("span", { class: "arrow" }, " → "), (nk(s.to.key) === nk(key) ? "" : s.to.key + ".") + s.to.col),
        h("button", { class: "btn sm", onclick: () => dbAddRelation(s.from, s.to, false) }, icon("plus"), "Hozzáad")));
      body.append(box, h("div", { class: "small muted", style: { marginTop: "4px" } }, "Azonos nevű mező egy másik tábla elsődleges kulcsával."));
    }
    body.append(dbAddRelForm(t));
  }
  body.scrollTop = scroll;
  p.classList.add("open");
}

const DESC_TIPS = "Tábla: mit tartalmaz, melyik cégre vonatkozik, és mi egy sor (pl. „cikk × üzletág × időszak szinten egy sor”).\n" +
  "cikkszam: „Energofish cikkszám (a cikktörzs azonosítója)”.\n" +
  "idoszak: ÉÉÉÉ.HH formátum (pl. 2026.10) – hóvégi pillanatkép vagy havi összesítés?\n" +
  "Összeg: nettó/bruttó és pénznem. Mennyiség: mértékegység.\n" +
  "Kódolt mező: példa értékekkel (pl. „1_NAGYKER, 9_KISKER”).";

// the description as currently known (the panel may show an older copy)
function dbCurDesc(key, col) {
  const t = dbTable(key);
  if (!t) return "";
  if (!col) return t.desc || "";
  const c = t.columns.find(x => nk(x.name) === nk(col));
  return (c && c.desc) || "";
}
function dbDescInDb() { const c = DB.view && DB.view.conn; return !!c && c.descWrite !== "off"; }
function dbDescProc() { const c = DB.view && DB.view.conn; return (c && c.descProc) || "meta.leiras_beallit"; }

// a description that was written on the map before descriptions moved into the database
function dbOldDesc(text, move) {
  return h("div", { class: "dbdesc row", style: { gap: "6px", alignItems: "flex-start" } },
    h("span", { class: "grow" }, "Korábbi, csak a térképen tárolt leírás: " + text),
    h("button", { class: "btn sm", style: { height: "22px", flexShrink: 0 }, onclick: move }, "Átvitel"));
}

function dbApplyDescResult(r) {
  const k = nk(tkey(r.table));
  const i = DB.schema.tables.findIndex(t => nk(tkey(t)) === k);
  if (i >= 0) DB.schema.tables[i] = r.table;
  DB.schema.changed = r.changed;
  if (r.doc) DB.doc = r.doc;
}

// writes one description with the team's procedure (EXEC … @sema, @tabla, @mezo, @leiras)
async function dbSaveDesc(key, col, text, old, el, fromUndo) {
  const label = (col ? "mezőleírás: " + col : "táblaleírás");
  if (el) { el.classList.remove("saved"); el.classList.add("saving"); }
  try {
    const r = await api("dbSetDesc", { table: key, column: col, text });
    dbApplyDescResult(r);
    if (el) { el.classList.remove("saving"); el.classList.add("saved"); setTimeout(() => el.classList.remove("saved"), 1600); }
    if (r.warning) toast(r.warning, true);
    else if (!fromUndo) toast((col ? "Mezőleírás" : "Táblaleírás") + " mentve az adatbázisba.");
    if (!fromUndo) {
      DB.undo.push({ label, doFn: () => dbSaveDesc(key, col, text, old, null, true), undoFn: () => dbSaveDesc(key, col, old, text, null, true) });
      DB.redo = [];
    }
    const focusInPanel = document.activeElement && document.activeElement.closest(".dbv-detail");
    if (!focusInPanel) dbRender(true); else { dbDrawLines(); dbUpdateTools(); dbRenderSide(DB.mounted.firstChild, false); }
    return true;
  } catch (e) {
    if (el) { el.classList.remove("saving"); el.value = old; }
    fail(e);
    return false;
  }
}

async function dbReadDesc(key) {
  try {
    const r = await api("dbReadDesc", { table: key });
    dbApplyDescResult(r);
    dbRender(true);
    toast("Leírások újraolvasva az adatbázisból.");
  } catch (e) { fail(e); }
}

function dbSuggestions(t, rels) {
  const key = tkey(t);
  const has = new Set(rels.map(r => nk(r.from) + "|" + nk(r.fromCol) + "|" + nk(r.to) + "|" + nk(r.toCol)));
  const pkOf = {};
  for (const x of dbTables()) {
    if (x.gone) continue;
    const pks = x.columns.filter(c => c.pk);
    if (pks.length === 1) (pkOf[nk(pks[0].name)] = pkOf[nk(pks[0].name)] || []).push({ key: tkey(x), col: pks[0].name });
  }
  const out = [];
  const add = (from, to) => {
    if (nk(from.key) === nk(to.key)) return;
    const k = nk(from.key) + "|" + nk(from.col) + "|" + nk(to.key) + "|" + nk(to.col);
    if (!has.has(k) && !out.some(o => o.k === k)) out.push({ from, to, k });
  };
  for (const c of t.columns) {
    for (const target of pkOf[nk(c.name)] || []) if (!c.pk || t.columns.filter(x => x.pk).length > 1) add({ key, col: c.name }, target);
  }
  const myPk = t.columns.filter(c => c.pk);
  if (myPk.length === 1) for (const x of dbTables()) {
    if (x.gone) continue;
    const c = x.columns.find(cc => nk(cc.name) === nk(myPk[0].name));
    if (c && !(c.pk && x.columns.filter(y => y.pk).length === 1)) add({ key: tkey(x), col: c.name }, { key, col: myPk[0].name });
  }
  return out.slice(0, 10);
}

function dbAddRelForm(t) {
  const key = tkey(t);
  const others = dbTables().filter(x => !x.gone).sort((a, b) => tkey(a).localeCompare(tkey(b), "hu"));
  const fromSel = h("select", { class: "input" }, t.columns.map(c => h("option", { value: c.name }, c.name)));
  const toTab = h("select", { class: "input" }, h("option", { value: "" }, "– kapcsolódó tábla –"), others.map(x => h("option", { value: tkey(x) }, tkey(x))));
  const toCol = h("select", { class: "input", disabled: true }, h("option", {}, "– mező –"));
  const fillCols = () => {
    const x = dbTable(toTab.value);
    clear(toCol);
    toCol.disabled = !x;
    if (!x) { toCol.append(h("option", {}, "– mező –")); return; }
    const pref = x.columns.find(c => nk(c.name) === nk(fromSel.value)) || x.columns.find(c => c.pk) || x.columns[0];
    for (const c of x.columns) toCol.append(h("option", { value: c.name, selected: pref && c.name === pref.name }, c.name + (c.pk ? "  🔑" : "")));
  };
  toTab.addEventListener("change", fillCols);
  fromSel.addEventListener("change", () => { if (toTab.value) fillCols(); });
  return h("div", {},
    h("div", { class: "section-t" }, "Kapcsolat hozzáadása"),
    h("div", { class: "dbv-addrel" },
      h("label", { class: "small" }, "Ennek a táblának a mezője (pl. PartnerID)"), fromSel,
      h("label", { class: "small" }, "hivatkozik erre (pl. dim.Partner → PartnerID)"),
      h("div", { class: "row" }, toTab, toCol),
      h("div", { class: "row" }, h("div", { class: "grow" }),
        h("button", { class: "btn sm primary", onclick: () => {
          if (!toTab.value || toCol.disabled) { toast("Válassza ki a kapcsolódó táblát és mezőt.", true); return; }
          dbAddRelation({ key, col: fromSel.value }, { key: toTab.value, col: toCol.value }, false);
        } }, icon("link"), "Hozzáadás"))));
}

// ---- group editor ------------------------------------------------------------------
function dbGroupModal(g, preset) {
  const isNew = !g;
  const draft = g ? Object.assign({}, g) : { id: rid(), name: "", color: GROUP_COLORS[(DB.doc.groups || []).length % GROUP_COLORS.length] };
  const members = new Set(dbTables().filter(t => { const m = meta(tkey(t)); return g && m && m.group === g.id; }).map(t => nk(tkey(t))));
  for (const k of preset || []) members.add(nk(k));
  let q = "";
  const close = () => ov.remove();
  const name = h("input", { class: "input", placeholder: "pl. Önköltség, Értékesítés, Partnerek", value: draft.name, oninput: e => { draft.name = e.target.value; } });
  const sw = h("div", { class: "row wrap" });
  const drawSw = () => {
    clear(sw);
    for (const c of GROUP_COLORS) sw.append(h("button", { class: "btn icon", title: c, style: { background: c, borderColor: c === draft.color ? "var(--text)" : c, boxShadow: c === draft.color ? "0 0 0 2px var(--surface) inset" : "", width: "30px", height: "30px", borderRadius: "10px" },
      onclick: () => { draft.color = c; drawSw(); } }));
  };
  drawSw();
  const list = h("div", { style: { maxHeight: "300px", overflow: "auto", border: "1px solid var(--border)", borderRadius: "10px", padding: "4px" } });
  const drawList = () => {
    clear(list);
    for (const t of dbTables().filter(t => !t.gone && tkey(t).toLowerCase().includes(q.toLowerCase()))) {
      const k = nk(tkey(t)), og = groupOf(tkey(t));
      list.append(h("label", { class: "row", style: { padding: "5px 8px", borderRadius: "8px", cursor: "pointer" } },
        h("input", { type: "checkbox", checked: members.has(k), onchange: e => { e.target.checked ? members.add(k) : members.delete(k); } }),
        h("span", { class: "grow" }, tkey(t)),
        og && (!g || og.id !== g.id) ? h("span", { class: "small muted" }, og.name) : null));
    }
  };
  drawList();
  const save = () => {
    if (!draft.name.trim()) { toast("Adjon nevet a csoportnak.", true); name.focus(); return; }
    const ops = [{ op: "groupSave", group: draft }];
    const undo = [];
    const prevGroup = {};
    for (const t of dbTables()) { const m = meta(tkey(t)); if (m) prevGroup[nk(tkey(t))] = { key: tkey(t), group: m.group || "" }; }
    const add = [], rem = [];
    for (const k in prevGroup) {
      const inNow = members.has(k), wasIn = g && prevGroup[k].group === g.id;
      if (inNow && !wasIn) add.push(prevGroup[k]);
      if (!inNow && wasIn) rem.push(prevGroup[k]);
    }
    if (add.length) ops.push({ op: "setGroup", id: draft.id, tables: add.map(x => x.key) });
    if (rem.length) ops.push({ op: "setGroup", id: "", tables: rem.map(x => x.key) });
    if (isNew) undo.push({ op: "groupDelete", id: draft.id });
    else undo.push({ op: "groupSave", group: g });
    const byOld = {};
    for (const x of [...add, ...rem]) (byOld[x.group] = byOld[x.group] || []).push(x.key);
    for (const [gid, keys] of Object.entries(byOld)) if (!(isNew && gid === draft.id)) undo.push({ op: "setGroup", id: gid, tables: keys });
    close();
    dbDo(ops, undo, isNew ? "új csoport" : "csoport");
  };
  const del = async () => {
    if (!await confirmBox("Csoport törlése", "Törli a(z) „" + g.name + "” csoportot? A táblák a térképen maradnak, csak csoport nélkül.", "Törlés", true)) return;
    close();
    const keys = dbTables().filter(t => { const m = meta(tkey(t)); return m && m.group === g.id; }).map(tkey);
    dbDo([{ op: "groupDelete", id: g.id }], [{ op: "groupSave", group: g }, { op: "setGroup", id: g.id, tables: keys }], "csoport törlése");
  };
  const ov = h("div", { class: "overlay center", onclick: e => { if (e.target === ov) close(); } },
    h("div", { class: "modal small" },
      h("div", { class: "m-head" }, h("h2", {}, isNew ? "Új csoport" : "Csoport szerkesztése")),
      h("div", { class: "m-body col", style: { gap: "12px" } },
        h("label", { class: "col", style: { gap: "5px" } }, h("b", { class: "small" }, "Név"), name),
        h("div", { class: "col", style: { gap: "5px" } }, h("b", { class: "small" }, "Szín"), sw),
        h("div", { class: "col", style: { gap: "5px" } }, h("b", { class: "small" }, "Táblák"),
          h("input", { class: "input", placeholder: "Szűrés…", oninput: e => { q = e.target.value; drawList(); } }), list)),
      h("div", { class: "m-foot" },
        !isNew ? h("button", { class: "btn danger", onclick: del }, icon("trash"), "Törlés") : null,
        h("div", { class: "grow" }),
        h("button", { class: "btn", onclick: close }, "Mégse"),
        h("button", { class: "btn primary", onclick: save }, icon("check"), "Mentés"))));
  document.body.append(ov);
  name.focus();
}

// ---- connection settings -------------------------------------------------------------
// Used on the "Adatbázis" tab, in its settings dialog and on the Settings page.
function dbConnForm(inline, onSaved) {
  const cur = (DB.view && DB.view.conn) || {};
  const c = Object.assign({ server: "", database: "", auth: "windows", user: "", encrypt: "trust", timeoutSec: 30, maxRows: 100000, descWrite: "proc", descProc: "meta.leiras_beallit" }, cur, { password: "", clearPassword: false });
  const res = h("div");
  const field = (label, el, full, hint) => h("label", { class: "field" + (full ? " full" : "") }, label, el, hint ? h("span", { class: "small muted", style: { fontWeight: 400 } }, hint) : null);
  const inp = (key, ph, type) => h("input", { class: "input" + (key === "server" || key === "database" ? " mono" : ""), type: type || "text", placeholder: ph || "", value: c[key] == null ? "" : c[key],
    oninput: e => { c[key] = type === "number" ? parseInt(e.target.value, 10) || 0 : e.target.value; } });
  const userBox = h("div", { class: "dbc-grid full", style: { gridColumn: "1 / -1" } });
  const drawUser = () => {
    clear(userBox);
    if (c.auth !== "sql") return;
    const pw = h("input", { class: "input", type: "password", autocomplete: "new-password", placeholder: c.hasPassword && !c.clearPassword ? "•••••••• (mentve – üresen hagyva megmarad)" : "Jelszó",
      oninput: e => { c.password = e.target.value; } });
    userBox.append(field("Felhasználó", inp("user", "pl. bi_olvaso")), field("Jelszó", pw, false,
      "Titkosítva, csak ezen a gépen tárolódik (Windows DPAPI)."));
  };
  const auth = h("div", { class: "seg" }, [["windows", "Windows-bejelentkezés"], ["sql", "SQL Server-felhasználó"]].map(([v, l]) =>
    h("button", { type: "button", class: c.auth === v ? "on" : "", onclick: e => { c.auth = v; $$("button", auth).forEach(b => b.classList.remove("on")); e.currentTarget.classList.add("on"); drawUser(); } }, l)));
  drawUser();
  const enc = h("select", { class: "input", onchange: e => { c.encrypt = e.target.value; } },
    [["trust", "Titkosított, a szerver tanúsítványát elfogadja (belső hálózat)"], ["strict", "Titkosított, tanúsítvány-ellenőrzéssel"], ["off", "Csak a bejelentkezés titkosított (régi szerverek)"]]
      .map(([v, l]) => h("option", { value: v, selected: c.encrypt === v }, l)));
  const procBox = h("div", { class: "field full" });
  const drawProc = () => {
    clear(procBox);
    if (c.descWrite === "off") { procBox.append(h("span", { class: "small muted", style: { fontWeight: 400 } }, "A leírások csak olvashatók; a program semmit nem ír az adatbázisba.")); return; }
    procBox.append("Leírást író tárolt eljárás",
      h("input", { class: "input mono", value: c.descProc || "", placeholder: "meta.leiras_beallit", oninput: e => { c.descProc = e.target.value; } }),
      h("span", { class: "small muted", style: { fontWeight: 400, lineHeight: 1.5 } },
        "Mentéskor a program kizárólag ezt futtatja, paraméterezve: ", h("code", { class: "mono" }, "EXEC " + (c.descProc || "meta.leiras_beallit") + " @sema, @tabla, @mezo, @leiras"),
        " (a tábla leírásánál @mezo = NULL). Más írási művelet nem fut."));
  };
  const descSeg = h("div", { class: "seg" }, [["proc", "Szerkeszthetők az adatbázisban"], ["off", "Csak olvasás"]].map(([v, l]) =>
    h("button", { type: "button", class: c.descWrite === v ? "on" : "", onclick: e => { c.descWrite = v; $$("button", descSeg).forEach(b => b.classList.remove("on")); e.currentTarget.classList.add("on"); drawProc(); } }, l)));
  drawProc();
  const busy = (btn, on) => { btn.disabled = on; };
  const show = (cls, ...content) => { clear(res).append(h("div", { class: "dbc-res " + cls }, ...content)); };
  const test = async e => {
    const b = e.currentTarget; busy(b, true);
    show("", "Kapcsolódás…");
    try {
      const i = await api("dbTest", c);
      const lines = [h("b", {}, "Sikeres kapcsolat. "), i.server + " · " + i.database + " · " + i.login + " · SQL Server " + i.serverVersion + " · " + i.tables + " tábla/nézet"];
      let warn = false;
      if (c.descWrite !== "off") {
        lines.push(h("br"), h("br"), h("b", {}, "Leírások: "));
        if (!i.descProcOk) { warn = true; lines.push("a(z) " + i.descProc + " eljárás nem található (vagy nincs rá jog) – a leírások mentése így nem fog működni."); }
        else if (!i.descProcExec) { warn = true; lines.push("a(z) " + i.descProc + " eljárás megvan, de ennek a felhasználónak nincs EXECUTE joga rá – a leírások mentése így nem fog működni."); }
        else lines.push("a(z) " + i.descProc + " eljárás elérhető, a leírások szerkeszthetők.");
      }
      if (i.canWrite) lines.push(h("br"), h("br"), "Ennek a felhasználónak az adatokra is van írási joga. A program ettől még csak a leírást író eljárást futtatja, de biztonságosabb egy csak olvasási jogú (db_datareader) felhasználó" + (c.descWrite !== "off" ? " + EXECUTE jog az eljárásra." : "."));
      show(warn || i.canWrite ? "warn" : "ok", ...lines);
    } catch (err) { show("err", h("b", {}, "Nem sikerült: "), err.message || String(err)); } finally { busy(b, false); }
  };
  const save = async e => {
    const b = e.currentTarget; busy(b, true);
    try {
      const v = await api("dbSaveConn", c);
      const changed = !DB.view || !DB.view.conn || nk(DB.view.conn.server) !== nk(v.conn.server) || nk(DB.view.conn.database) !== nk(v.conn.database);
      DB.view = v;
      if (changed) { DB.doc = null; DB.schema = null; DB.sel = null; DB.detailOpen = false; DB.undo = []; DB.redo = []; DB.fitted = false; dbRestoreViewport(); }
      toast("Adatbázis-kapcsolat mentve.");
      if (onSaved) onSaved(v);
      await dbLoad(false);
      if (S.view === "db" && changed && DB.schema) setTimeout(dbFit, 30);
    } catch (err) { fail(err); } finally { busy(b, false); }
  };
  return h("div", {},
    h("div", { class: "dbc-grid" },
      field("Szerver", inp("server", "pl. EF-SQL01 vagy EF-SQL01\\BI"), false, "Példány: SZERVER\\PÉLDÁNY · port: SZERVER,1433. Kipróbáláshoz: (demo)"),
      field("Adatbázis", inp("database", "pl. BI")),
      h("div", { class: "field full" }, "Bejelentkezés", auth),
      userBox,
      field("Titkosítás", enc, true),
      field("Időkorlát (mp)", inp("timeoutSec", "30", "number"), false, "Szerkezet és első 100 sor."),
      field("„Teljes tábla” legfeljebb", inp("maxRows", "100000", "number"), false, "sor (a gép memóriája miatt)"),
      h("div", { class: "field full" }, "Tábla- és mezőleírások", descSeg),
      procBox),
    h("div", { class: "small muted", style: { marginTop: "12px", lineHeight: 1.5 } },
      "Az adatokat a program csak olvassa: a tábla- és mezőlistát a rendszerkatalógusból kéri le, sorokat pedig csak akkor, ha az „Első 100 sor” vagy „Teljes tábla” gombot megnyomja. Az egyetlen írás a leírások mentése a fenti eljárással (kikapcsolható)."),
    res,
    h("div", { class: "row", style: { marginTop: "14px", justifyContent: inline ? "center" : "flex-end" } },
      h("button", { class: "btn", onclick: test }, icon("test"), "Kapcsolat tesztelése"),
      h("button", { class: "btn primary", onclick: save }, icon("check"), "Mentés")));
}

function dbConnModal() {
  const close = () => ov.remove();
  const ov = h("div", { class: "overlay center", onclick: e => { if (e.target === ov) close(); } },
    h("div", { class: "modal", style: { width: "680px" } },
      h("div", { class: "m-head" }, icon("db"), h("h2", { class: "grow" }, "Adatbázis-kapcsolat"), h("button", { class: "btn sm icon ghost", onclick: close }, icon("close"))),
      h("div", { class: "m-body" }, dbConnForm(false, close))));
  document.body.append(ov);
}

// card on the Settings page
function dbSettingsCard() {
  const body = h("div", { class: "card-b" }, h("div", { class: "small muted" }, "Betöltés…"));
  const card = h("div", { class: "card" }, h("div", { class: "card-h" }, icon("db"), h("h3", {}, "Adatbázis")), body);
  api("dbLoad", {}).then(v => { DB.view = Object.assign({}, DB.view || {}, { conn: v.conn, configured: v.configured }); clear(body).append(dbConnForm(false)); }).catch(e => { clear(body).append(h("div", { class: "small", style: { color: "var(--missing)" } }, e.message || String(e))); });
  return card;
}

// ---- row viewer ------------------------------------------------------------------
const GRID_ROW = 28, GRID_PAGE = 200;
bridge.on("dbProgress", p => { const el = $("#dbgProgress"); if (el) el.textContent = p.rows.toLocaleString("hu-HU") + " sor betöltve…"; });

async function dbOpenGrid(key, all) {
  $$(".overlay.dbg").forEach(o => o.remove());
  const t = dbTable(key);
  const G = { key, all, res: null, q: { filters: {}, search: "", sort: "", desc: false }, pages: new Map(), filtered: 0, gen: 0, widths: [], stats: null, sel: null };
  const close = () => { ov.remove(); document.removeEventListener("keydown", onKey, true); if (G.res) api("dbCloseResult", { id: G.res.id }).catch(() => {}); };
  const onKey = e => {
    if (!document.body.contains(ov)) return;
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(); }
    if (e.ctrlKey && (e.key === "c" || e.key === "C") && G.sel && !/INPUT|TEXTAREA/.test(document.activeElement.tagName) && !String(window.getSelection())) { e.preventDefault(); copyText(G.sel.text); }
  };
  document.addEventListener("keydown", onKey, true);
  const info = h("span", { class: "info" });
  const bar = h("div", { class: "dbg-bar" });
  const wrap = h("div", { class: "dbg-wrap" });
  const foot = h("div", { class: "dbg-foot" });
  const loading = h("div", { class: "dbg-loading" }, h("div", { class: "box" }, icon("refresh", "spin-ic"),
    h("div", { id: "dbgProgress" }, all ? "Teljes tábla lekérése…" : "Első 100 sor lekérése…"),
    h("button", { class: "btn sm", onclick: () => api("dbCancel").catch(fail) }, icon("stop"), "Megszakítás")));
  const ov = h("div", { class: "overlay center dbg" },
    h("div", { class: "modal dbg-modal" },
      h("div", { class: "m-head" }, icon("table"), h("h2", { class: "grow selectable" }, key), info,
        h("button", { class: "btn sm icon ghost", title: "Bezárás (Esc)", onclick: close }, icon("close"))),
      h("div", { class: "m-body", style: { position: "relative" } }, bar, wrap, foot, loading)));
  document.body.append(ov);
  try {
    G.res = await api("dbQuery", { table: key, all });
  } catch (e) {
    clear(loading).append(h("div", { class: "box" }, h("b", { style: { color: "var(--missing)" } }, "A lekérdezés nem sikerült"), h("div", { class: "small", style: { maxWidth: "520px", textAlign: "center" } }, e.message || String(e)),
      h("button", { class: "btn sm", onclick: close }, "Bezárás")));
    return;
  }
  loading.remove();
  const r = G.res;
  const tookTxt = (r.tookMs / 1000).toFixed(1).replace(".", ",") + " mp";
  info.textContent = r.total.toLocaleString("hu-HU") + " sor · " + tookTxt;
  const cols = r.cols;
  const numeric = cols.map(c => c.kind === "number");
  const search = h("input", { class: "input", placeholder: "Keresés minden mezőben…" });
  bar.append(
    h("span", { class: "badge " + (r.all ? "new" : "fk") }, r.all ? (r.truncated ? "első " + r.total.toLocaleString("hu-HU") + " sor (korlát)" : "teljes tábla") : "első 100 sor"),
    t && t.rows >= 0 ? h("span", { class: "small muted" }, "a táblában kb. " + t.rows.toLocaleString("hu-HU") + " sor") : "",
    h("div", { class: "grow" }),
    h("label", { class: "dbg-search" }, icon("search"), search),
    h("button", { class: "btn sm", onclick: () => { G.q.filters = {}; G.q.search = ""; search.value = ""; $$(".dbg-frow input", wrap).forEach(i => { i.value = ""; i.classList.remove("on", "bad"); }); G.q.sort = ""; reload(); } }, "Szűrők törlése"),
    !r.all ? h("button", { class: "btn sm primary", onclick: () => { close(); dbOpenGrid(key, true); } }, icon("table"), "Teljes tábla") : "");
  foot.append(h("span", { id: "dbgCount" }),
    h("span", {}, "Szűrő: ", h("code", {}, "szöveg"), " tartalmazza · ", h("code", {}, ">100"), " ", h("code", {}, "<=5"), " ", h("code", {}, "=HU"), " ", h("code", {}, "!=0"),
      " · ", h("code", {}, "10..20"), " tartomány · ", h("code", {}, "üres"), " / ", h("code", {}, "!üres"), " · dátum: ", h("code", {}, "=2024-03")),
    h("span", { class: "grow" }), h("span", {}, "Kattintás: cella kijelölése · Ctrl+C / dupla kattintás: másolás"));

  // column widths from the names and the first rows
  const first = await api("dbPage", { id: r.id, offset: 0, limit: GRID_PAGE });
  G.pages.set(0, first.rows);
  G.filtered = first.filtered;
  G.widths = cols.map((c, i) => {
    let w = Math.max(c.name.length * 8 + 46, c.type.length * 6.5 + 24);
    for (const row of first.rows.slice(0, 100)) { const v = row[i]; if (v != null) w = Math.max(w, Math.min(String(v).length, 48) * (numeric[i] ? 7.4 : 7) + 22); }
    return Math.round(Math.max(72, Math.min(360, w)));
  });

  const table = h("div", { class: "dbg-table" });
  const head = h("div", { class: "dbg-head" });
  const hrow = h("div", { class: "dbg-hrow" }), frow = h("div", { class: "dbg-frow" });
  const body = h("div", { class: "dbg-body" });
  head.append(hrow, frow);
  table.append(head, body);
  wrap.append(table);
  const totalW = () => 64 + G.widths.reduce((a, b) => a + b, 0);
  const drawHead = () => {
    clear(hrow); clear(frow);
    hrow.append(h("div", { class: "rn" }, "#"));
    frow.append(h("div", { class: "rn" }));
    cols.forEach((c, i) => {
      const s = G.q.sort === c.name ? (G.q.desc ? " ▼" : " ▲") : "";
      const cell = h("div", { class: numeric[i] ? "num" : "", style: { width: G.widths[i] + "px" }, title: "Rendezés: kattintás",
        onclick: e => { if (e.target.classList.contains("rs")) return;
          if (G.q.sort !== c.name) { G.q.sort = c.name; G.q.desc = false; } else if (!G.q.desc) G.q.desc = true; else { G.q.sort = ""; G.q.desc = false; }
          reload(); } },
        h("div", { class: "hn" }, h("span", { style: { overflow: "hidden", textOverflow: "ellipsis" } }, c.name), s ? h("span", { class: "sort" }, s) : null,
          h("span", { class: "muted", title: "Oszlopstatisztika", style: { cursor: "help", fontWeight: 400 }, onclick: e => e.stopPropagation(), onmouseenter: e => showStats(e.currentTarget, i), onmouseleave: hideStats }, "ⓘ")),
        h("div", { class: "ht" }, c.type),
        h("div", { class: "rs", onmousedown: e => resize(e, i), onclick: e => e.stopPropagation() }));
      hrow.append(cell);
      const fi = h("input", { placeholder: numeric[i] ? ">0, 10..20" : c.kind === "date" ? "=2024-03" : "szűrő", value: G.q.filters[c.name] || "", class: G.q.filters[c.name] ? "on" : "" });
      let tm = 0;
      fi.addEventListener("input", () => {
        clearTimeout(tm);
        tm = setTimeout(() => { if (fi.value.trim()) G.q.filters[c.name] = fi.value; else delete G.q.filters[c.name]; fi.classList.toggle("on", !!fi.value.trim()); reload(); }, 280);
      });
      frow.append(h("div", { style: { width: G.widths[i] + "px" } }, fi));
    });
    table.style.width = totalW() + "px";
  };
  const resize = (e, i) => {
    e.preventDefault(); e.stopPropagation();
    const sx = e.clientX, w0 = G.widths[i];
    const mv = ev => { G.widths[i] = Math.max(50, Math.round(w0 + ev.clientX - sx)); applyWidths(); };
    const up = () => { document.removeEventListener("mousemove", mv); document.removeEventListener("mouseup", up); };
    document.addEventListener("mousemove", mv); document.addEventListener("mouseup", up);
  };
  const applyWidths = () => {
    const set = row => { for (let i = 0; i < cols.length; i++) if (row.children[i + 1]) row.children[i + 1].style.width = G.widths[i] + "px"; };
    set(hrow); set(frow); $$(".dbg-row", body).forEach(set);
    table.style.width = totalW() + "px";
  };
  let statsBox = null;
  const showStats = async (el, i) => {
    hideStats();
    statsBox = h("div", { class: "dbg-stats" }, "…");
    document.body.append(statsBox);
    const rc = el.getBoundingClientRect();
    statsBox.style.left = Math.min(innerWidth - 370, rc.left) + "px"; statsBox.style.top = (rc.bottom + 6) + "px";
    const box = statsBox;
    try {
      if (!G.stats || G.stats.gen !== G.gen) G.stats = { gen: G.gen, data: await api("dbStats", { id: r.id }) };
      const s = G.stats.data[i];
      if (box !== statsBox) return;
      clear(box).append(h("span", {}, "Üres (NULL)"), h("b", {}, s.nulls.toLocaleString("hu-HU")), h("span", {}, "Különböző érték"), h("b", {}, s.distinct >= 10000 ? "10 000+" : s.distinct.toLocaleString("hu-HU")),
        s.min !== undefined && s.min !== "" ? h("span", { style: { display: "contents" } }, h("span", {}, "Legkisebb"), h("b", {}, s.min), h("span", {}, "Legnagyobb"), h("b", {}, s.max)) : "",
        h("span", { class: "muted", style: { gridColumn: "1 / -1", marginTop: "4px" } }, "A szűrt sorokra, a betöltött adatokból."));
    } catch (e) { if (box === statsBox) clear(box).append(e.message || String(e)); }
  };
  const hideStats = () => { if (statsBox) statsBox.remove(); statsBox = null; };

  const renderRows = () => {
    const top = wrap.scrollTop, hgt = wrap.clientHeight;
    const headH = head.offsetHeight;
    const from = Math.max(0, Math.floor((top - headH) / GRID_ROW) - 10);
    const to = Math.min(G.filtered, Math.ceil((top + hgt) / GRID_ROW) + 10);
    body.style.height = (G.filtered * GRID_ROW) + "px";
    const need = new Set();
    for (let i = from; i < to; i++) if (!G.pages.has(Math.floor(i / GRID_PAGE))) need.add(Math.floor(i / GRID_PAGE));
    need.forEach(p => loadPage(p));
    clear(body);
    for (let i = from; i < to; i++) {
      const page = G.pages.get(Math.floor(i / GRID_PAGE));
      const row = page && page[i % GRID_PAGE];
      const el = h("div", { class: "dbg-row", style: { top: (i * GRID_ROW) + "px" } }, h("div", { class: "rn" }, (i + 1).toLocaleString("hu-HU")));
      cols.forEach((c, ci) => {
        const v = row ? row[ci] : undefined;
        const txt = v == null ? "" : String(v);
        const cell = h("div", { class: (numeric[ci] ? "num" : "") + (G.sel && G.sel.i === i && G.sel.c === ci ? " cellsel" : ""), style: { width: G.widths[ci] + "px" },
          title: txt.length > 18 ? txt.slice(0, 1500) : "" },
          row === undefined ? h("span", { class: "null" }, "…") : v === null ? h("span", { class: "null" }, "NULL") : txt);
        cell.addEventListener("click", () => { G.sel = { i, c: ci, text: v == null ? "" : txt }; $$(".cellsel", body).forEach(x => x.classList.remove("cellsel")); cell.classList.add("cellsel"); });
        cell.addEventListener("dblclick", () => copyText(v == null ? "" : txt));
        el.append(cell);
      });
      body.append(el);
    }
    if (!G.filtered) body.append(h("div", { class: "small muted", style: { position: "sticky", left: "0", padding: "28px 18px", width: "min(100%, 90vw)" } },
      r.total ? "Nincs a szűrőknek megfelelő sor." : "A tábla üres."));
    const cnt = $("#dbgCount");
    if (cnt) cnt.textContent = (G.filtered === r.total ? r.total.toLocaleString("hu-HU") + " sor" : G.filtered.toLocaleString("hu-HU") + " / " + r.total.toLocaleString("hu-HU") + " sor (szűrve)") +
      (r.truncated ? " · a táblában ennél több sor van" : "");
  };
  const loadPage = async p => {
    G.pages.set(p, null); // loading
    const gen = G.gen;
    try {
      const pg = await api("dbPage", Object.assign({ id: r.id, offset: p * GRID_PAGE, limit: GRID_PAGE }, G.q));
      if (gen !== G.gen) return;
      G.pages.set(p, pg.rows); G.filtered = pg.filtered;
      renderRows();
    } catch (e) { G.pages.delete(p); fail(e); }
  };
  const reload = async () => {
    G.gen++; G.pages = new Map(); G.sel = null;
    const gen = G.gen;
    try {
      const pg = await api("dbPage", Object.assign({ id: r.id, offset: 0, limit: GRID_PAGE }, G.q));
      if (gen !== G.gen) return;
      const bad = pg.error || "";
      $$(".dbg-frow input", wrap).forEach((inp, i) => inp.classList.toggle("bad", !!bad && bad.startsWith(cols[i].name + ":")));
      if (bad) toast("Szűrő: " + bad, true);
      G.pages.set(0, pg.rows); G.filtered = pg.filtered;
      drawHead();
      wrap.scrollTop = 0;
      renderRows();
    } catch (e) { fail(e); }
  };
  search.addEventListener("input", () => { clearTimeout(search._t); search._t = setTimeout(() => { G.q.search = search.value; reload(); }, 300); });
  let raf = 0;
  wrap.addEventListener("scroll", () => { if (!raf) raf = requestAnimationFrame(() => { raf = 0; renderRows(); }); });
  drawHead();
  renderRows();
}

function copyText(s) {
  const done = () => toast("Másolva a vágólapra.");
  if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(s).then(done, () => fallbackCopy(s, done));
  else fallbackCopy(s, done);
}
function fallbackCopy(s, done) {
  const ta = h("textarea", { style: { position: "fixed", left: "-9999px" } });
  ta.value = s;
  document.body.append(ta); ta.select();
  try { document.execCommand("copy"); done(); } catch (e) { /* ignore */ }
  ta.remove();
}
