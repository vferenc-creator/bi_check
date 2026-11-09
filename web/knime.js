// "KNIME" menu: running KNIME workflows headless with NodePit Batch –
// scheduled or on demand. Independent of the monitoring views; it uses only
// the shared helpers (h, icon, api, toast, timeField, …) of app.js.

Object.assign(ICONS, {
  knime: '<path d="M12 3 21 8v8l-9 5-9-5V8z"/><path d="M12 3v18M3 8l9 5 9-5"/>',
  play: '<path d="M7 5v14l12-7z"/>',
  stopSq: '<rect x="6" y="6" width="12" height="12" rx="1.5"/>',
  logTxt: '<path d="M6 3h9l4 4v14H6z"/><path d="M15 3v4h4M9 12h7M9 16h7"/>',
});

const KN = { view: null, poll: null, result: null, busy: false };

const KN_STATUS = {
  ok: ["ok", "Sikeres"], error: ["bad", "Hiba"], timeout: ["bad", "Időtúllépés"], canceled: ["mute", "Megszakítva"],
  missed: ["warn", "Kimaradt"], skipped: ["mute", "Kihagyva"], running: ["run", "Fut"], queued: ["run", "Sorban áll"],
  retry: ["warn", "Újrapróbál"],
};
// "2/3. próbálkozás" when the job has retries
function knTry(r) { return r && r.attempts > 1 ? (r.attempt || 1) + "/" + r.attempts + ". próbálkozás" : ""; }
function knPill(status) {
  const [cls, txt] = KN_STATUS[status] || ["mute", "Még nem futott"];
  return h("span", { class: "kn-pill " + cls }, status === "running" ? h("span", { class: "kn-dot" }) : null, txt);
}

bridge.on("knime", () => { if (S.view === "knime" && !$(".menu")) knLoad(); });

// Esc closes the topmost KNIME window
function knEsc(close) {
  const onKey = e => { if (e.key === "Escape" && !$(".menu")) { e.preventDefault(); e.stopPropagation(); close(); } };
  document.addEventListener("keydown", onKey, true);
  return () => document.removeEventListener("keydown", onKey, true);
}

views.knime = main => {
  main.append(h("div", { id: "knRoot" }, h("div", { class: "small muted" }, "Betöltés…")));
  knLoad();
  clearInterval(KN.poll);
  KN.poll = setInterval(() => {
    if (S.view !== "knime" || !$("#knRoot")) { clearInterval(KN.poll); return; }
    if (document.visibilityState === "visible" && !$(".overlay") && !$(".menu")) knLoad();
  }, 5000);
};

async function knLoad() {
  try { KN.view = await api("knimeState"); } catch (e) { const r = $("#knRoot"); if (r) clear(r).append(h("div", { class: "errbox" }, e.message || String(e))); return; }
  knRender();
}

function knRender() {
  const root = $("#knRoot");
  if (!root) return;
  const v = KN.view;
  const keepScroll = $("#main").scrollTop;
  clear(root).append(
    h("div", { class: "page-head" },
      h("div", {}, h("h1", {}, "KNIME futtatás"), h("div", { class: "sub" }, "KNIME workflow-k futtatása a háttérben, ütemezve vagy kézzel (NodePit Batch)")),
      h("div", { class: "grow" }),
      h("button", { class: "btn primary", onclick: () => knEditor(null), disabled: !knReady(v) }, icon("plus"), "Új futtatás")),
    // on a computer without own jobs the other computers come first
    !v.jobs.length && (v.remote || []).length ? knRemoteCard(v) : null,
    knSetupCard(v),
    knJobsCard(v),
    v.jobs.length || !(v.remote || []).length ? knRemoteCard(v) : null,
    knRunsCard(v));
  $("#main").scrollTop = keepScroll;
}

function knReady(v) { return !!(v && v.install && v.install.exe && !v.install.error && v.install.nodepit); }

// ---- KNIME + NodePit Batch ----------------------------------------------------
function knSetupCard(v) {
  const inst = v.install || {};
  const has = inst.exe && !inst.error;
  const body = h("div", { class: "card-b kn-setup" });
  body.append(h("div", { class: "kn-row" },
    h("div", { class: "kn-lbl" }, "KNIME"),
    h("div", { class: "kn-val" },
      has ? h("div", {}, h("b", {}, "KNIME " + (inst.version || "?")), h("div", { class: "mono small muted selectable", style: { wordBreak: "break-all" } }, inst.exe))
        : h("div", { class: "muted" }, inst.error || "Még nincs megadva, hol van a KNIME ezen a gépen.")),
    h("div", { class: "kn-act" },
      h("button", { class: "btn sm", onclick: knDetect }, icon("refresh"), "KNIME keresése"),
      h("button", { class: "btn sm", onclick: knBrowseExe }, icon("folder"), "Tallózás…"))));
  if (has) {
    body.append(h("div", { class: "kn-row" },
      h("div", { class: "kn-lbl" }, "NodePit Batch"),
      h("div", { class: "kn-val" },
        inst.nodepit ? h("div", {}, h("span", { class: "kn-pill ok" }, "telepítve"), " ", h("b", {}, inst.nodepit), h("div", { class: "small muted" }, "A futtatások ezzel indulnak. Új KNIME-verziónál telepítse újra az ahhoz valót."))
          : h("div", {}, h("span", { class: "kn-pill warn" }, "nincs telepítve"), h("div", { class: "small muted", style: { marginTop: "4px" } }, "Ez futtatja a workflow-kat a háttérben (ingyenes, a KNIME 5.9-től a gyári futtató helyett).")),
        v.installing ? h("div", { class: "kn-busy" }, icon("refresh", "spin-ic"), "Telepítés folyamatban… (pár perc is lehet; a KNIME legyen bezárva)") : null,
        !inst.writable ? h("div", { class: "small", style: { color: "var(--late)", marginTop: "6px", lineHeight: 1.45 } },
          "A KNIME mappájába ez a felhasználó nem írhat – a telepítéshez indítsa a BI Monitort rendszergazdaként, vagy tegye a KNIME-ot egy saját mappába (pl. C:\\KNIME).") : null),
      h("div", { class: "kn-act" },
        h("button", { class: "btn sm primary", disabled: v.installing || !v.zipUrl, title: v.zipUrl || "A KNIME verziója nem ismert", onclick: () => knInstall({ download: true }) },
          icon("download"), "Letöltés és telepítés" + (inst.version ? " (" + inst.version.split(".").slice(0, 2).join(".") + ")" : "")),
        h("button", { class: "btn sm", disabled: v.installing, onclick: knInstallZip }, icon("upload"), "Zip a gépről…"))));
    if (v.zipUrl) body.append(h("div", { class: "small muted kn-url" }, "Letöltési cím: ", h("span", { class: "mono selectable" }, v.zipUrl)));
  }
  if (KN.result) {
    const r = KN.result;
    body.append(h("div", { class: "kn-res " + (r.ok ? "ok" : "bad") }, h("b", {}, r.ok ? "Kész. " : "Nem sikerült. "), r.message,
      r.output ? h("details", { style: { marginTop: "6px" }, open: !!KN.detailsOpen, ontoggle: e => { KN.detailsOpen = e.currentTarget.open; } },
        h("summary", {}, "Részletek"), h("pre", { class: "kn-pre" }, r.output)) : null));
  }
  const c = v.config || {};
  body.append(h("div", { class: "kn-row kn-opts" },
    h("div", { class: "kn-lbl" }, "Beállítások"),
    h("div", { class: "kn-val row wrap", style: { gap: "10px 22px" } },
      h("label", { class: "row", style: { gap: "8px" } }, "Egyszerre futhat",
        h("select", { class: "input", style: { width: "auto" }, onchange: e => knSaveConfig({ parallel: +e.target.value }) },
          [1, 2, 3, 4].map(n => h("option", { value: n, selected: c.parallel === n }, n + " workflow")))),
      h("label", { class: "switch", title: "A KNIME kimenete a naplóba (ha a gépen ez felugró ablakot nyit, kapcsolja ki)" },
        h("input", { type: "checkbox", checked: !!c.consoleLog, onchange: e => knSaveConfig({ consoleLog: e.target.checked }) }), "Konzolkimenet a naplóba (-consoleLog)"),
      h("div", { class: "row", style: { gap: "8px", flexBasis: "100%" } }, h("span", { title: "A KNIME ablakban beállított dolgok (pl. adatbázis-meghajtók) a háttérfuttatásnál nincsenek meg. Exportálja őket: KNIME → File → Export Preferences…" }, "KNIME beállításfájl (.epf):"),
        c.preferences ? h("span", { class: "mono small selectable", style: { wordBreak: "break-all" } }, c.preferences) : h("span", { class: "small muted" }, "nincs (ha a workflow adatbázis-meghajtót vagy más KNIME-beállítást használ, adja meg)"),
        h("button", { class: "btn sm", onclick: knBrowsePrefs }, icon("folder"), "Tallózás…"),
        c.preferences ? h("button", { class: "btn sm ghost", onclick: () => knSaveConfig({ preferences: "" }) }, "Törlés") : null),
      knShareOpts(v)),
    h("div", { class: "kn-act" })));
  return h("div", { class: "card kn-card" }, h("div", { class: "card-h" }, icon("knime"), h("h3", {}, "KNIME és NodePit Batch")), body);
}

// ---- shared folder: our state for the others, the others' runs ------------------
function knShareOpts(v) {
  const sh = v.share || {}, c = v.config || {};
  if (!sh.team) return h("div", { class: "small muted", style: { flexBasis: "100%", lineHeight: 1.45 } },
    "Közös mappa nincs beállítva: a futtatások állapota csak ezen a gépen látszik. (Közös mód: Beállítások → Közös mód.)");
  const status = c.noShare ? "kikapcsolva" : !v.jobs.length ? "nincs mit megosztani (ezen a gépen nincs futtatás)"
    : sh.published ? "megosztva, utoljára " + fmtDateTime(sh.published, true) : "hamarosan…";
  return h("div", { class: "kn-share", style: { flexBasis: "100%" } },
    h("label", { class: "switch", title: "A többi gép a közös mappában látja ennek a gépnek a futtatásait (jelszó, változó, napló nélkül)" },
      h("input", { type: "checkbox", checked: !c.noShare, onchange: e => knSaveConfig({ noShare: !e.target.checked }) }), "Állapot megosztása a közös mappában"),
    h("span", { class: "small muted" }, status, " · ", h("span", { class: "mono selectable" }, sh.folder)),
    h("label", { class: "switch", title: "Windows-értesítés, ha egy másik gépen egy futás végleg hibás lett, vagy a gép nem jelentkezik" },
      h("input", { type: "checkbox", checked: !c.muteRemote, onchange: e => knSaveConfig({ muteRemote: !e.target.checked }) }), "Értesítés a többi gép KNIME-hibáiról"),
    sh.error ? h("div", { class: "small", style: { color: "var(--missing)", flexBasis: "100%" } }, sh.error) : null);
}

function knRemoteCard(v) {
  const sh = v.share || {};
  if (!sh.team) return null;
  const card = h("div", { class: "card kn-card" }, h("div", { class: "card-h" }, icon("users"),
    h("h3", { class: "grow" }, "Más gépek KNIME-futtatásai"), h("span", { class: "small muted" }, "a közös mappából · csak olvasható")));
  const remote = v.remote || [];
  if (!remote.length) {
    card.append(h("div", { class: "card-b" }, h("div", { class: "kn-empty" }, "Egyetlen más gép sem osztja meg a KNIME-futtatásait a közös mappában.")));
    return card;
  }
  for (const rm of remote) {
    const who = rm.host || rm.file;
    const head = h("div", { class: "kn-rhead" },
      h("b", {}, who), rm.user ? h("span", { class: "muted" }, rm.user) : null,
      rm.error ? h("span", { class: "kn-pill bad" }, "hiba")
        : rm.stale ? h("span", { class: "kn-pill bad", title: "A BI Monitor azon a gépen nem fut, vagy nem éri el a közös mappát" }, "nem jelentkezik")
        : h("span", { class: "kn-pill ok" }, h("span", { class: "kn-dot" }), "él"),
      h("span", { class: "small muted" }, "utoljára " + fmtDateTime(rm.updated, true)),
      h("div", { class: "grow" }),
      h("span", { class: "small muted" }, [rm.knime ? "KNIME " + rm.knime : "", rm.nodepit ? "NodePit " + rm.nodepit : "", rm.app ? "BI Monitor " + rm.app : ""].filter(Boolean).join(" · ")));
    const box = h("div", { class: "kn-remote" + (rm.stale ? " stale" : "") }, head);
    if (rm.error) { box.append(h("div", { class: "errbox" }, rm.error)); card.append(box); continue; }
    if (rm.stale) box.append(h("div", { class: "kn-res bad", style: { margin: "0 0 8px" } },
      "Ez a gép " + fmtDateTime(rm.updated) + " óta nem írt a közös mappába: a BI Monitor ott nem fut, vagy nem éri el a mappát. Az alábbi állapot ekkori, és az ütemezett futtatások most nem biztos, hogy elindulnak."));
    const tb = h("tbody");
    for (const j of rm.jobs) {
      const st = j.live ? j.live.status : j.retry ? "retry" : j.last ? j.last.status : "";
      const last = j.last;
      tb.append(h("tr", { class: j.scheduled && !j.enabled ? "off" : "" },
        h("td", {}, knPill(st)),
        h("td", {}, h("div", { class: "kn-name" }, j.name), h("div", { class: "mono small muted kn-ell kn-path", title: j.workflow }, h("bdi", {}, j.workflow))),
        h("td", { class: "small" }, h("div", {}, j.schedule + (j.scheduled && !j.enabled ? " (szünetel)" : "")), j.next ? h("div", { class: "muted" }, "Következő: " + fmtDateTime(j.next)) : null),
        h("td", { class: "small" }, j.live ? (j.live.status === "running" ? "Fut, indult: " + fmtDateTime(j.live.started) : "Sorban áll")
          : j.retry ? h("div", {}, "Újrapróbálja: " + fmtDateTime(j.retry.at), last ? h("div", { class: "kn-msg", title: last.message }, knFirst(last.message)) : null)
          : last ? h("div", {}, fmtDateTime(last.ended || last.queued), last.durationMs ? h("span", { class: "muted" }, " · " + fmtDur(last.durationMs / 1000)) : null,
            last.status !== "ok" ? h("div", { class: "kn-msg " + knMsgCls(last.status), title: last.message }, knFirst(last.message)) : null) : h("span", { class: "muted" }, "–"))));
    }
    if (rm.jobs.length) box.append(h("table", { class: "table kn-table kn-rtable" },
      h("colgroup", {}, h("col", { style: { width: "120px" } }), h("col"), h("col", { style: { width: "28%" } }), h("col", { style: { width: "28%" } })),
      h("thead", {}, h("tr", {}, ["Állapot", "Név / workflow", "Ütemezés", "Utolsó futás"].map(t => h("th", { class: "nosort" }, t)))), tb));
    else box.append(h("div", { class: "kn-empty" }, "Nincs futtatás ezen a gépen."));
    if (rm.runs.length) {
      const list = h("div", { class: "kn-runs" });
      for (const r of rm.runs) list.append(h("div", { class: "kn-run" },
        h("span", { class: "t" }, fmtDateTime(r.started || r.queued || r.ended)), knPill(r.status),
        h("span", { class: "n" }, r.jobName),
        h("span", { class: "muted small" }, r.trigger + (knTry(r) ? " · " + knTry(r) : "") + (r.durationMs ? " · " + fmtDur(r.durationMs / 1000) : "")),
        h("span", { class: "m " + knMsgCls(r.status), title: r.message }, r.status === "ok" ? "" : knFirst(r.message)), h("span")));
      const key = "kn-open-" + rm.file;
      box.append(h("details", { class: "kn-rruns", open: !!KN[key], ontoggle: e => { KN[key] = e.currentTarget.open; } },
        h("summary", {}, "Utolsó futások (" + rm.runs.length + ")"), list));
    }
    card.append(box);
  }
  return card;
}

async function knSaveConfig(patch) {
  try { KN.view = await api("knimeSetConfig", Object.assign({}, KN.view.config, patch)); knRender(); } catch (e) { fail(e); }
}
async function knBrowsePrefs() {
  try { const r = await api("knimeBrowsePrefs"); if (r.path) knSaveConfig({ preferences: r.path }); } catch (e) { fail(e); }
}
async function knDetect() {
  try { KN.view = await api("knimeDetect"); KN.result = null; knRender(); toast("KNIME megtalálva: " + KN.view.install.exe); } catch (e) { fail(e); }
}
async function knBrowseExe() {
  try {
    const r = await api("knimeBrowseExe", (KN.view.install || {}).exe || "");
    if (!r.path) return;
    KN.view = await api("knimeSetConfig", Object.assign({}, KN.view.config, { exe: r.path }));
    KN.result = null;
    knRender();
  } catch (e) { fail(e); }
}
async function knInstallZip() {
  try {
    const z = await api("knimeBrowseZip");
    if (!z.path) return;
    if (z.error) { KN.result = { ok: false, message: z.error }; knRender(); return; }
    knInstall({ zip: z.path });
  } catch (e) { fail(e); }
}
async function knInstall(p) {
  KN.result = null;
  KN.view.installing = true;
  knRender();
  try {
    let r = await api("knimeInstall", p);
    if (r.mismatch) {
      const go = await confirmBox("Másik KNIME-verzióhoz készült zip", r.mismatch + "\n\nMégis telepíti? (Ha nem működik, töltse le a KNIME verziójához valót.)", "Telepítés");
      if (!go) { KN.view.installing = false; knRender(); return; }
      r = await api("knimeInstall", Object.assign({}, p, { force: true }));
    }
    KN.result = r;
    if (r.ok) toast("NodePit Batch telepítve.");
  } catch (e) { KN.result = { ok: false, message: e.message || String(e) }; }
  await knLoad();
}

// ---- jobs -------------------------------------------------------------------
function knJobsCard(v) {
  const card = h("div", { class: "card kn-card" }, h("div", { class: "card-h" }, icon("play"), h("h3", { class: "grow" }, "Futtatások (" + v.jobs.length + ")")));
  if (!v.jobs.length) {
    card.append(h("div", { class: "card-b" }, h("div", { class: "kn-empty" },
      knReady(v) ? "Még nincs futtatás. Az „Új futtatás” gombbal adhat meg egy workflow-t, a paramétereit és az ütemezését."
        : "Előbb adja meg a KNIME helyét, és telepítse a NodePit Batch-et – utána itt vehet fel futtatásokat.")));
    return card;
  }
  const tb = h("tbody");
  for (const jv of v.jobs) {
    const j = jv.job, live = jv.live, last = jv.last, retry = !live && jv.retry;
    const st = live ? live.status : retry ? "retry" : last ? last.status : "";
    tb.append(h("tr", { class: j.scheduled && !j.enabled ? "off" : "" },
      h("td", {}, knPill(st)),
      h("td", {}, h("div", { class: "kn-name" }, j.name), h("div", { class: "mono small muted kn-ell kn-path", title: j.workflow }, h("bdi", {}, j.workflow))),
      h("td", { class: "small" }, h("div", {}, jv.scheduleText), jv.next ? h("div", { class: "muted" }, "Következő: " + fmtDateTime(jv.next)) : null),
      h("td", { class: "small" }, live ? h("div", {}, live.status === "running" ? "Fut, indult: " + fmtDateTime(live.started) : "Sorban áll",
          knTry(live) ? h("span", { class: "muted" }, " · " + knTry(live)) : null)
        : retry ? h("div", {}, "Hiba után újrapróbálja: " + fmtDateTime(retry.at), h("span", { class: "muted" }, " · " + retry.attempt + "/" + retry.of + ". próbálkozás"),
          last ? h("div", { class: "kn-msg " + knMsgCls(last.status), title: last.message }, knFirst(last.message)) : null,
          h("button", { class: "btn sm ghost kn-link", onclick: () => knCancelRetry(j.id) }, "Újrapróbálás törlése"))
        : last ? h("div", {}, fmtDateTime(last.ended || last.queued), last.durationMs ? h("span", { class: "muted" }, " · " + fmtDur(last.durationMs / 1000)) : null,
          last.status !== "ok" ? h("div", { class: "kn-msg " + knMsgCls(last.status), title: last.message }, knFirst(last.message)) : null) : h("span", { class: "muted" }, "–")),
      h("td", { class: "kn-btns" },
        live ? h("button", { class: "btn sm", onclick: () => knCancel(live.id) }, icon("stopSq"), "Leállítás")
          : h("button", { class: "btn sm primary", disabled: !knReady(v), onclick: () => knRunNow(j.id) }, icon("play"), "Futtatás most"),
        h("button", { class: "btn sm icon ghost", title: "Szerkesztés", onclick: () => knEditor(j) }, icon("edit")),
        // stopPropagation: the page-wide click handler closes menus
        h("button", { class: "btn sm icon ghost", title: "Továbbiak", onclick: e => { e.stopPropagation(); showMenu(e.currentTarget, [
          last && last.log ? { label: "Utolsó futás naplója", icon: "logTxt", run: () => knShowLog(last) } : false,
          { label: "Duplikálás", icon: "copy", run: () => knEditor(Object.assign(JSON.parse(JSON.stringify(j)), { id: "", name: j.name + " (másolat)", credentials: (j.credentials || []).map(c => Object.assign({}, c, { hasPassword: false })) })) },
          j.scheduled ? { label: j.enabled ? "Ütemezés szüneteltetése" : "Ütemezés folytatása", icon: "power", run: () => knToggle(j) } : false,
          null,
          { label: "Törlés", icon: "trash", danger: true, run: () => knDelete(j) },
        ].filter(x => x !== false)); } }, icon("dots")))));
  }
  card.append(h("table", { class: "table kn-table" },
    h("colgroup", {}, h("col", { style: { width: "120px" } }), h("col"), h("col", { style: { width: "24%" } }), h("col", { style: { width: "22%" } }), h("col", { style: { width: "232px" } })),
    h("thead", {}, h("tr", {}, ["Állapot", "Név / workflow", "Ütemezés", "Utolsó futás", ""].map(t => h("th", { class: "nosort" }, t)))), tb));
  return card;
}
function knFirst(s) { return String(s || "").split("\n")[0]; }
// red only for real failures; a stop by hand or a skip is not an error
function knMsgCls(status) { return status === "canceled" || status === "skipped" ? "quiet" : status === "missed" ? "warn" : ""; }

async function knRunNow(id) {
  try { await api("knimeRunNow", { id }); toast("Elindítva."); knLoad(); } catch (e) { fail(e); }
}
async function knCancelRetry(jobId) {
  try { await api("knimeCancelRetry", { id: jobId }); toast("Az újrapróbálás törölve."); knLoad(); } catch (e) { fail(e); }
}
async function knCancel(runId) {
  try { await api("knimeCancel", { id: runId }); knLoad(); } catch (e) { fail(e); }
}
async function knToggle(j) {
  try { await api("knimeSaveJob", { job: Object.assign({}, j, { enabled: !j.enabled }), passwords: {} }); knLoad(); } catch (e) { fail(e); }
}
async function knDelete(j) {
  if (!await confirmBox("Futtatás törlése", "Törli a(z) „" + j.name + "” futtatást? Az előzményei megmaradnak.", "Törlés", true)) return;
  try { await api("knimeDeleteJob", { id: j.id }); toast("Törölve."); knLoad(); } catch (e) { fail(e); }
}

// ---- history ------------------------------------------------------------------
function knRunsCard(v) {
  const card = h("div", { class: "card kn-card" }, h("div", { class: "card-h" }, icon("history"), h("h3", { class: "grow" }, "Előzmények")));
  const runs = (v.runs || []).slice(0, 60);
  if (!runs.length) { card.append(h("div", { class: "card-b" }, h("div", { class: "kn-empty" }, "Még nem volt futás."))); return card; }
  const list = h("div", { class: "kn-runs" });
  for (const r of runs) {
    list.append(h("div", { class: "kn-run" + (r.log ? " click" : ""), onclick: () => { if (r.log) knShowLog(r); }, title: r.log ? "Napló megnyitása" : "" },
      h("span", { class: "t" }, fmtDateTime(r.started || r.queued || r.ended)),
      knPill(r.status),
      h("span", { class: "n" }, r.jobName),
      h("span", { class: "muted small" }, r.trigger + (knTry(r) ? " · " + knTry(r) : "") + (r.durationMs ? " · " + fmtDur(r.durationMs / 1000) : "")),
      h("span", { class: "m " + knMsgCls(r.status), title: r.message }, r.status === "ok" ? "" : knFirst(r.message)),
      r.log ? icon("logTxt") : h("span")));
  }
  card.append(list);
  return card;
}

async function knShowLog(r) {
  const pre = h("pre", { class: "kn-pre kn-log" }, "Betöltés…");
  let unEsc = () => {};
  const close = () => { unEsc(); ov.remove(); };
  const ov = h("div", { class: "overlay", onclick: e => { if (e.target === ov) close(); } },
    h("div", { class: "modal", style: { width: "1000px" } },
      h("div", { class: "m-head" }, icon("logTxt"), h("h2", { class: "grow" }, r.jobName + " – " + fmtDateTime(r.started || r.queued)), knPill(r.status),
        h("button", { class: "btn sm icon ghost", onclick: close }, icon("close"))),
      h("div", { class: "m-body" }, r.message ? h("div", { class: "kn-res " + (r.status === "ok" ? "ok" : "bad"), style: { marginTop: 0, whiteSpace: "pre-line" } }, r.message) : null, pre),
      h("div", { class: "m-foot" },
        h("button", { class: "btn", onclick: () => api("knimeOpenLog", { id: r.id }).catch(fail) }, icon("open"), "Megnyitás szövegszerkesztőben"),
        h("div", { class: "grow" }),
        h("button", { class: "btn primary", onclick: close }, "Bezárás"))));
  document.body.append(ov);
  unEsc = knEsc(close);
  try { pre.textContent = await api("knimeLog", { id: r.id }); pre.scrollTop = pre.scrollHeight; } catch (e) { pre.textContent = e.message || String(e); }
}

// ---- editor -------------------------------------------------------------------
const KN_TYPES = [["daily", "Naponta"], ["workdays", "Munkanapokon"], ["weekly", "Hetente"], ["hourly", "Óránként"], ["monthly", "Havonta"], ["cron", "Cron"]];

function knEditor(job) {
  const isNew = !job || !job.id;
  const j = job ? JSON.parse(JSON.stringify(job)) : { name: "", workflow: "", variables: [], credentials: [], reset: true, save: false, copy: false, timeoutMin: 120, retryCount: 2, retryWaitMin: 5,
    scheduled: true, enabled: true, notify: true, note: "", extraArgs: "", schedule: { type: "workdays", times: ["06:00"], useHolidays: true } };
  if (!j.schedule || !j.schedule.type) j.schedule = { type: "workdays", times: ["06:00"], useHolidays: true };
  j.variables = j.variables || []; j.credentials = j.credentials || [];
  const passwords = {}, clearPw = new Set();
  const err = h("div");
  const preview = h("div", { class: "kn-preview" });
  const field = (label, ctrl, hint) => h("label", { class: "field" }, label, ctrl, hint ? h("span", { class: "hint" }, hint) : null);
  const txt = (key, attrs) => h("input", Object.assign({ class: "input", value: j[key] || "", oninput: e => { j[key] = e.target.value; } }, attrs || {}));
  const swIn = {};
  const sw = (key, label) => h("label", { class: "switch" }, swIn[key] = h("input", { type: "checkbox", checked: !!j[key], onchange: e => {
    j[key] = e.target.checked;
    if (key === "scheduled") drawSched();
    // a copy is deleted after the run: saving it would do nothing
    if (key === "copy" && j.copy && j.save) { j.save = false; swIn.save.checked = false; }
    if (key === "save" && j.save && j.copy) { j.copy = false; swIn.copy.checked = false; }
  } }), label);

  const wfIn = txt("workflow", { class: "input mono grow", placeholder: "C:\\knime-workspace\\Napi_export   (a workflow mappája)", spellcheck: false });
  const browseWf = async file => {
    try { const r = await api("knimeBrowseWorkflow", { file, current: j.workflow }); if (r.path) { j.workflow = r.path; wfIn.value = r.path; if (!j.name) { j.name = r.path.split(/[\\/]/).filter(Boolean).pop().replace(/\.(knwf|knar)$/i, ""); nameIn.value = j.name; } } } catch (e) { fail(e); }
  };
  const nameIn = txt("name", { placeholder: "pl. Napi értékesítési export" });
  const waitIn = h("input", { class: "input narrow", type: "number", min: 1, max: 240, value: j.retryWaitMin || 5, disabled: !j.retryCount, style: { width: "70px" } });

  // variables
  const varBox = h("div", { class: "kn-list" });
  const drawVars = () => {
    clear(varBox);
    j.variables.forEach((v, i) => varBox.append(h("div", { class: "kn-li" },
      h("input", { class: "input mono", placeholder: "név", value: v.name || "", oninput: e => { v.name = e.target.value; } }),
      h("input", { class: "input", placeholder: "érték (pl. 2026-11-06)", value: v.value || "", oninput: e => { v.value = e.target.value; } }),
      h("select", { class: "input", onchange: e => { v.type = e.target.value; } }, ["String", "int", "double"].map(t => h("option", { value: t, selected: (v.type || "String") === t }, t))),
      h("button", { class: "btn sm icon ghost", title: "Törlés", onclick: () => { j.variables.splice(i, 1); drawVars(); } }, icon("trash")))));
    varBox.append(h("button", { class: "btn sm", onclick: () => { j.variables.push({ name: "", value: "", type: "String" }); drawVars(); } }, icon("plus"), "Változó"));
  };
  drawVars();

  // credentials
  const credBox = h("div", { class: "kn-list" });
  const drawCreds = () => {
    clear(credBox);
    j.credentials.forEach((c, i) => {
      const stored = c.hasPassword && !clearPw.has(c.name.trim());
      const pw = h("input", { class: "input", type: "password", autocomplete: "new-password", placeholder: stored ? "•••••• (mentve – üresen megmarad)" : "jelszó", value: passwords[c.name] || "",
        oninput: e => { passwords[c.name] = e.target.value; } });
      credBox.append(h("div", { class: "kn-li" + (stored ? " pw" : "") },
        h("input", { class: "input mono", placeholder: "hitelesítés neve a workflow-ban", value: c.name || "", oninput: e => { const old = c.name; c.name = e.target.value; if (passwords[old] !== undefined) { passwords[c.name] = passwords[old]; delete passwords[old]; } } }),
        h("input", { class: "input", placeholder: "felhasználó", value: c.login || "", oninput: e => { c.login = e.target.value; } }),
        pw,
        stored ? h("button", { class: "btn sm ghost", title: "A mentett jelszó törlése", onclick: () => { clearPw.add(c.name.trim()); delete passwords[c.name]; drawCreds(); } }, "jelszó ×") : null,
        h("button", { class: "btn sm icon ghost", title: "Törlés", onclick: () => { if (c.name.trim()) clearPw.add(c.name.trim()); delete passwords[c.name]; j.credentials.splice(i, 1); drawCreds(); } }, icon("trash"))));
    });
    credBox.append(h("button", { class: "btn sm", onclick: () => { j.credentials.push({ name: "", login: "", hasPassword: false }); drawCreds(); } }, icon("plus"), "Hitelesítés"));
  };
  drawCreds();

  // schedule (the first spec + further ones in "also")
  const schedBox = h("div");
  let pvT = 0;
  const pv = () => { clearTimeout(pvT); pvT = setTimeout(updatePreview, 250); };
  const specEd = (sp, box, idx) => {
    if (idx !== null) box.append(h("div", { class: "kn-sh" }, h("b", {}, (idx + 1) + ". ütemezés"), h("div", { class: "grow" }),
      h("button", { class: "btn sm icon ghost", title: "Ütemezés törlése", onclick: () => removeSpec(idx) }, icon("trash"))));
    box.append(h("div", { class: "seg", style: { marginBottom: "10px" } }, KN_TYPES.map(([t, l]) => h("button", { type: "button", class: sp.type === t ? "on" : "", onclick: () => {
      sp.type = t;
      if (["daily", "weekly", "workdays", "monthly"].includes(t) && !(sp.times || []).length) sp.times = ["06:00"];
      if (t === "weekly" && !(sp.weekdays || []).length) sp.weekdays = [1, 2, 3, 4, 5];
      if (t === "workdays" && sp.useHolidays === undefined) sp.useHolidays = true;
      if (t === "monthly" && !sp.monthlyMode) { sp.monthlyMode = "day"; sp.monthDay = sp.monthDay || 1; }
      if (t === "hourly") { sp.intervalMinutes = sp.intervalMinutes || 60; sp.from = sp.from || "06:00"; sp.to = sp.to || "18:00"; }
      if (t === "cron" && !sp.cron) sp.cron = "0 6 * * 1-5";
      drawSched();
    } }, l))));
    const row = h("div", { class: "row wrap", style: { gap: "10px 18px", alignItems: "flex-start" } });
    box.append(row);
    const times = () => {
      const tb = h("div", { class: "row wrap", style: { gap: "6px" } });
      const draw = () => {
        clear(tb);
        for (const t of sp.times || []) tb.append(h("span", { class: "tag" }, t, h("button", { title: "Eltávolítás", onclick: () => { sp.times = sp.times.filter(x => x !== t); draw(); pv(); } }, "×")));
        const add = val => { if (val && !(sp.times || []).includes(val)) { sp.times = [...(sp.times || []), val].sort(); draw(); pv(); } };
        const tf = timeField("", add, { live: false, placeholder: "+ ÓÓ:PP" });
        tb.append(h("span", { class: "time-add" }, tf, h("button", { class: "btn icon", title: "Időpont hozzáadása (Enter)", onclick: () => { const val = parseClock(tf.input.value); if (val) { tf.input.value = ""; add(val); } } }, icon("plus"))));
      };
      draw();
      return h("div", { class: "field" }, "Időpont(ok)", tb);
    };
    const days = () => {
      const d = h("div", { class: "daypick" });
      const draw = () => { clear(d); for (let i = 1; i <= 7; i++) d.append(h("button", { type: "button", class: (sp.weekdays || []).includes(i) ? "on" : "", onclick: () => {
        sp.weekdays = (sp.weekdays || []).includes(i) ? sp.weekdays.filter(x => x !== i) : [...(sp.weekdays || []), i].sort(); draw(); pv(); } }, DAY_SHORT[i])); };
      draw();
      return h("div", { class: "field" }, "Napok", d);
    };
    switch (sp.type) {
      case "daily": row.append(times()); break;
      case "workdays": row.append(times(), h("div", { class: "field" }, "Munkaszüneti napok", h("label", { class: "switch" },
        h("input", { type: "checkbox", checked: !!sp.useHolidays, onchange: e => { sp.useHolidays = e.target.checked; pv(); } }), "magyar ünnepekkel"))); break;
      case "weekly": row.append(days(), times()); break;
      case "monthly": row.append(h("div", { class: "field" }, "Nap", h("select", { class: "input", onchange: e => { sp.monthlyMode = e.target.value; drawSched(); } },
        [["day", "a hónap N. napja"], ["firstWorkday", "első munkanap"], ["nthWorkday", "N. munkanap"], ["lastWorkday", "utolsó munkanap"], ["lastDay", "utolsó nap"]]
          .map(([k, l]) => h("option", { value: k, selected: (sp.monthlyMode || "day") === k }, l)))),
        (sp.monthlyMode || "day") === "day" || sp.monthlyMode === "nthWorkday" ? h("div", { class: "field" }, "N =", h("input", { class: "input narrow", type: "number", min: 1, max: 31, value: sp.monthDay || 1, oninput: e => { sp.monthDay = parseInt(e.target.value, 10) || 1; pv(); } })) : null,
        times()); break;
      case "hourly": row.append(
        h("div", { class: "field" }, "Gyakoriság", h("select", { class: "input", onchange: e => { sp.intervalMinutes = +e.target.value; pv(); } },
          [[15, "15 percenként"], [30, "30 percenként"], [60, "óránként"], [120, "2 óránként"], [180, "3 óránként"], [240, "4 óránként"], [360, "6 óránként"]].map(([n, l]) => h("option", { value: n, selected: sp.intervalMinutes === n }, l)))),
        h("div", { class: "field" }, "Kezdés", timeField(sp.from, val => { sp.from = val; pv(); })),
        h("div", { class: "field" }, "Vége", timeField(sp.to, val => { sp.to = val; pv(); })),
        h("div", { class: "field" }, "Napok", h("select", { class: "input", onchange: e => { sp.days = e.target.value; pv(); } },
          [["", "minden nap"], ["weekdays", "hétköznap"], ["workdays", "munkanapokon"]].map(([k, l]) => h("option", { value: k, selected: (sp.days || "") === k }, l))))); break;
      case "cron": row.append(h("div", { class: "field", style: { flex: 1 } }, "Cron kifejezés", h("input", { class: "input mono", value: sp.cron || "", oninput: e => { sp.cron = e.target.value; pv(); } }),
        h("span", { class: "hint" }, "perc óra nap hónap hét_napja – pl. 0 6 * * 1-5"))); break;
    }
  };
  const removeSpec = i => {
    const sp = j.schedule, rest = [...(sp.also || [])];
    if (i === 0) { const first = rest.shift(); j.schedule = Object.assign(first, rest.length ? { also: rest } : {}); }
    else { rest.splice(i - 1, 1); if (rest.length) sp.also = rest; else delete sp.also; }
    drawSched();
  };
  function drawSched() {
    clear(schedBox);
    schedBox.append(h("div", { class: "row wrap", style: { gap: "10px 24px", marginBottom: "10px" } },
      sw("scheduled", "Ütemezett futtatás"),
      j.scheduled ? h("label", { class: "switch" }, h("input", { type: "checkbox", checked: !!j.enabled, onchange: e => { j.enabled = e.target.checked; } }), "Ütemezés bekapcsolva") : null));
    if (!j.scheduled) { schedBox.append(h("div", { class: "small muted" }, "Csak kézzel indul (Futtatás most).")); clear(preview); return; }
    const all = [j.schedule, ...(j.schedule.also || [])];
    all.forEach((sp, i) => { const b = h("div", { class: all.length > 1 ? "kn-sblock" : "" }); specEd(sp, b, all.length > 1 ? i : null); schedBox.append(b); });
    if (all.length <= 20) schedBox.append(h("button", { class: "btn sm", style: { marginTop: "8px" }, onclick: () => {
      j.schedule.also = [...(j.schedule.also || []), { type: "weekly", weekdays: [1, 2, 3, 4, 5], times: ["12:00"] }]; drawSched(); } }, icon("plus"), "További ütemezés"));
    pv();
  }
  async function updatePreview() {
    if (!j.scheduled) { clear(preview); return; }
    try {
      const p = await api("previewSchedule", { schedule: j.schedule, count: 6 });
      clear(preview).append(h("div", { class: "section-t", style: { marginTop: 0 } }, "Következő futások"));
      if (p.error) { preview.append(h("div", { class: "errbox" }, p.error)); return; }
      preview.append(h("div", { style: { fontWeight: 600, marginBottom: "6px" } }, p.text));
      const ul = h("ul", { class: "preview-list" });
      const wd = ["V", "H", "K", "Sze", "Cs", "P", "Szo"];
      for (const t of p.next) { const d = new Date(t); ul.append(h("li", {}, h("span", {}, fmtDateTime(t)), h("span", { class: "muted" }, wd[d.getDay()]))); }
      preview.append(ul);
    } catch (_) { /* typing */ }
  }
  drawSched();

  let unEsc = () => {};
  const close = () => { unEsc(); ov.remove(); };
  const save = async () => {
    clear(err);
    j.retryWaitMin = parseInt(waitIn.value, 10) || 0;
    // names as the server sees them (trimmed); a removed or cleared name
    // never keeps its old password, even if added again
    const pws = {};
    for (const [k, v] of Object.entries(passwords)) if (v) pws[k.trim()] = v;
    try {
      await api("knimeSaveJob", { job: j, passwords: pws, clear: [...clearPw] });
      close();
      toast(isNew ? "Futtatás felvéve: " + j.name : "Mentve: " + j.name);
      knLoad();
    } catch (e) { err.append(h("div", { class: "errbox" }, e.message || String(e))); }
  };
  // no close on a click beside the dialog: the typed data would be lost
  const ov = h("div", { class: "overlay" },
    h("div", { class: "modal kn-modal" },
      h("div", { class: "m-head" }, icon("knime"), h("h2", { class: "grow" }, isNew ? "Új KNIME futtatás" : "Futtatás szerkesztése"), h("button", { class: "btn sm icon ghost", onclick: close }, icon("close"))),
      h("div", { class: "m-body kn-ed" },
        h("div", {},
          h("fieldset", {}, h("legend", {}, "Workflow"),
            field("Név *", nameIn),
            h("div", { class: "field" }, "Workflow *", h("div", { class: "row" }, wfIn,
              h("button", { class: "btn", type: "button", onclick: () => browseWf(false) }, icon("folder"), "Mappa…"),
              h("button", { class: "btn", type: "button", title: ".knwf vagy .knar fájl", onclick: () => browseWf(true) }, icon("file"), "Fájl…")),
              h("span", { class: "hint" }, "A workflow mappája (benne a workflow.knime), vagy egy exportált .knwf fájl. Hálózati mappa is lehet (\\\\szerver\\…).")),
            field("Megjegyzés", txt("note", { placeholder: "pl. mit állít elő, kinek szól" }))),
          h("fieldset", {}, h("legend", {}, "Ütemezés"), schedBox),
          h("fieldset", {}, h("legend", {}, "Paraméterek"),
            h("div", { class: "field" }, "Workflow-változók", h("span", { class: "hint", style: { marginBottom: "6px" } }, "A workflow saját változóinak felülírása (Workflow Variables…)."), varBox),
            h("div", { class: "field", style: { marginTop: "10px" } }, "Hitelesítések (pl. adatbázis)", h("span", { class: "hint", style: { marginBottom: "6px" } }, "A workflow-ban megadott Workflow Credentials név szerint. A jelszó titkosítva, csak ezen a gépen tárolódik."), credBox)),
          h("fieldset", {}, h("legend", {}, "Futtatás"),
            h("div", { class: "row wrap", style: { gap: "10px 24px" } },
              sw("reset", "Alaphelyzetbe állítás előtte (--reset)"),
              sw("copy", "Futtatás másolatból"),
              sw("save", "Mentés futás után"),
              sw("notify", "Értesítés hibánál")),
            h("div", { class: "small muted", style: { margin: "6px 0 10px", lineHeight: 1.45 } }, "„Futtatás másolatból”: akkor is fut, ha a workflow épp nyitva van a KNIME-ban (egy ideiglenes másolatot futtat; mentés ilyenkor nincs)."),
            h("div", { class: "row wrap", style: { gap: "10px 18px" } },
              field("Időkorlát (perc)", h("input", { class: "input narrow", type: "number", min: 0, max: 1440, value: j.timeoutMin, oninput: e => { j.timeoutMin = parseInt(e.target.value, 10) || 0; } }), "0 = nincs; utána leállítja"),
              h("div", { class: "field" }, "Újrapróbálás hibánál",
                h("div", { class: "row", style: { gap: "8px" } },
                  h("select", { class: "input", style: { width: "auto" }, onchange: e => { j.retryCount = +e.target.value; if (j.retryCount && !j.retryWaitMin) { j.retryWaitMin = 5; waitIn.value = 5; } waitIn.disabled = !j.retryCount; } },
                    [0, 1, 2, 3, 4, 5].map(n => h("option", { value: n, selected: (j.retryCount || 0) === n }, n ? n + "×" : "nincs"))),
                  h("span", { class: "small muted" }, "várakozás:"), waitIn, h("span", { class: "small muted" }, "perc")),
                h("span", { class: "hint" }, "hiba vagy időtúllépés után; értesítés csak az utolsó sikertelen próbálkozás után")),
              h("div", { style: { flex: 1, minWidth: "240px" } }, field("További NodePit kapcsolók (haladó)", txt("extraArgs", { class: "input mono", placeholder: "pl. --preferences \"C:\\knime\\prefs.epf\"", spellcheck: false })))),
            h("div", { class: "field", style: { marginTop: "10px" } }, "KNIME munkaterület (haladó)",
              h("div", { class: "row" }, txt("workspace", { class: "input mono grow kn-ws", placeholder: "üresen: minden futás új, ideiglenes munkaterületen (ajánlott)", spellcheck: false }),
                h("button", { class: "btn", type: "button", onclick: async () => { try { const r = await api("knimeBrowseWorkflow", { file: false, current: j.workspace || "" }); if (r.path) { j.workspace = r.path; $(".kn-ws", ov).value = r.path; } } catch (e) { fail(e); } } }, icon("folder"), "Mappa…")),
              h("span", { class: "hint" }, "Csak ha a workflow knime://LOCAL/ útvonalat vagy a munkaterület beállításait használja. Az ugyanilyen munkaterületű futások egymás után futnak, és a munkaterület közben nem lehet nyitva a KNIME ablakban."))),
          err),
        h("div", { class: "side" }, preview)),
      h("div", { class: "m-foot" },
        h("button", { class: "btn", onclick: close }, "Mégse"),
        h("button", { class: "btn primary", onclick: save }, icon("check"), isNew ? "Felvétel" : "Mentés"))));
  document.body.append(ov);
  unEsc = knEsc(close);
  nameIn.focus();
}
