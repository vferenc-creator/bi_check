/* global messenger, BIM */
// BI Output Monitor – Thunderbird add-on, background page.
// Talks to the BI Monitor application on this computer (http://127.0.0.1,
// pairing key), searches the watched folders for the messages of the e-mail
// rules and reports the matches. The application decides OK / late / missing.
"use strict";

const DEFAULTS = { port: 47231, key: "", folders: null, intervalMin: 2, notify: false };
const EXT_VERSION = messenger.runtime.getManifest().version;

const state = { connected: false, error: "", lastScan: null, scanning: false, folders: [], status: null, ruleCount: 0, scanError: "" };
let cfg = Object.assign({}, DEFAULTS);
let timer = null;
let tbVersion = "";

const sleep = ms => new Promise(r => setTimeout(r, ms));

// Installed from the BI Monitor ("Telepítés a Thunderbirdbe"): the package
// carries config.json with this computer's port and pairing key. It is used
// when the add-on has no key yet, or when the BI Monitor issued a new key.
async function applyBuiltInConfig(stored) {
  let pre;
  try {
    const r = await fetch(messenger.runtime.getURL("config.json"));
    if (!r.ok) return false;
    pre = await r.json();
  } catch (_) {
    return false; // installed by hand: no built-in config
  }
  if (!pre || !pre.key) return false;
  if (stored.key && (!pre.issued || pre.issued === stored.configIssued)) return false;
  await messenger.storage.local.set({ key: pre.key, port: pre.port || DEFAULTS.port, configIssued: pre.issued || "" });
  return true;
}

async function loadCfg() {
  const stored = await messenger.storage.local.get(Object.keys(DEFAULTS).concat(["configIssued"]));
  if (await applyBuiltInConfig(stored)) Object.assign(stored, await messenger.storage.local.get(["key", "port", "configIssued"]));
  cfg = Object.assign({}, DEFAULTS, stored);
}

async function api(path, opts = {}, override) {
  const c = override || cfg;
  if (!c.key) throw new Error("Nincs megadva párosító kulcs (Beállítások).");
  const ctrl = new AbortController();
  const t = setTimeout(() => ctrl.abort(), opts.timeout || 15000);
  try {
    const r = await fetch(`http://127.0.0.1:${c.port}/api/tb/v1${path}`, {
      method: opts.method || "GET",
      headers: { "Authorization": "Bearer " + c.key, "Content-Type": "application/json" },
      body: opts.body ? JSON.stringify(opts.body) : undefined,
      signal: ctrl.signal,
    });
    const data = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(data.error || "HTTP " + r.status);
    return data;
  } catch (e) {
    if (e.name === "AbortError") throw new Error("A BI Monitor nem válaszolt időben.");
    if (e instanceof TypeError) throw new Error("A BI Monitor nem érhető el – fut az alkalmazás, és be van kapcsolva a Thunderbird-kapcsolat (Beállítások → Thunderbird)?");
    throw e;
  } finally {
    clearTimeout(t);
  }
}

async function allFolders() {
  const accounts = await messenger.accounts.list(true);
  return BIM.flattenFolders(accounts);
}

// Thunderbird 121+ identifies folders by id; 115 needs the folder object.
const folderQuery = f => (f.folder.id ? { folderId: f.folder.id } : { folder: f.folder });

async function scan() {
  if (state.scanning || !cfg.key) return;
  state.scanning = true;
  try {
    const rr = await api("/rules");
    const rules = BIM.compileRules(rr.rules);
    state.ruleCount = rules.length;
    const folders = BIM.watchedFolders(await allFolders(), cfg.folders);
    state.folders = folders.map(f => f.account + " / " + f.label);
    const matches = {};
    for (const r of rules) matches[r.id] = [];
    const errors = [];
    if (rules.length) {
      const fromDate = new Date(Date.now() - (rr.lookbackDays || 40) * 86400000);
      for (const f of folders) {
        try {
          let page = await messenger.messages.query(Object.assign({ fromDate }, folderQuery(f)));
          for (;;) {
            for (const m of page.messages) for (const r of rules) if (BIM.matches(r, m, f)) matches[r.id].push(BIM.toMsg(m, f));
            if (!page.id) break;
            page = await messenger.messages.continueList(page.id);
          }
        } catch (e) {
          errors.push(f.account + " / " + f.label + ": " + e.message);
        }
      }
    }
    BIM.trim(matches, 50);
    await api("/report", { method: "POST", body: { extVersion: EXT_VERSION, tbVersion, folders: state.folders, matches, errors } });
    state.lastScan = new Date().toISOString();
    state.scanError = errors.join("; ");
    state.connected = true;
    state.error = "";
    await refreshStatus();
  } catch (e) {
    state.connected = false;
    state.error = e.message;
    updateBadge();
  } finally {
    state.scanning = false;
  }
}

// The newest messages of the watched folders, for the e-mail picker of the
// BI Monitor editor ("which report do you want to watch?").
async function sendRecent() {
  let days = 45, limit = 500;
  try {
    const rr = await api("/rules");
    days = rr.recentDays || days;
    limit = rr.recentLimit || limit;
  } catch (_) { /* defaults */ }
  const folders = BIM.watchedFolders(await allFolders(), cfg.folders);
  const fromDate = new Date(Date.now() - days * 86400000);
  const all = [];
  for (const f of folders) {
    try {
      let page = await messenger.messages.query(Object.assign({ fromDate }, folderQuery(f)));
      for (;;) {
        for (const m of page.messages) all.push(BIM.toMsg(m, f));
        if (!page.id) break;
        page = await messenger.messages.continueList(page.id);
      }
    } catch (e) {
      console.warn("BI Monitor: recent", f.label, e);
    }
  }
  await api("/recent", { method: "POST", body: { messages: BIM.newest(all, limit) } });
}

async function refreshStatus() {
  try {
    const st = await api("/status");
    const fresh = BIM.newProblems(state.status, st);
    state.status = st;
    state.connected = true;
    if (cfg.notify) {
      for (const it of fresh) {
        messenger.notifications.create("bim-" + it.id, {
          type: "basic", iconUrl: "icons/icon-64.png",
          title: "BI Monitor: " + it.label + " – " + it.name, message: it.reason,
        });
      }
    }
  } catch (e) {
    state.connected = false;
    state.error = e.message;
  }
  updateBadge();
}

function updateBadge() {
  const b = BIM.badge(state.connected, state.status);
  messenger.browserAction.setBadgeText({ text: b.text });
  messenger.browserAction.setBadgeBackgroundColor({ color: b.color });
  messenger.browserAction.setTitle({ title: b.title });
}

async function openMessage(c) {
  let id = c.messageId;
  if (c.headerId) {
    // ids change when Thunderbird restarts; the Message-ID header does not
    try {
      const page = await messenger.messages.query({ headerMessageId: c.headerId });
      if (page.messages.length) id = page.messages[0].id;
    } catch (_) { /* older Thunderbird: use the id */ }
  }
  if (!id) return;
  await messenger.messageDisplay.open({ messageId: id, location: "tab" });
}

async function handle(cmd) {
  if (cmd.type === "scan") await scan();
  else if (cmd.type === "recent") await sendRecent().catch(e => console.warn("BI Monitor: recent", e));
  else if (cmd.type === "open") await openMessage(cmd).catch(e => console.warn("BI Monitor: open", e));
}

// Long poll: the application pushes "scan now" / "open this message".
async function pollLoop() {
  for (;;) {
    if (!cfg.key) { await sleep(5000); continue; }
    try {
      const r = await api("/poll?wait=25", { timeout: 40000 });
      if (!state.connected) { state.connected = true; state.error = ""; refreshStatus(); }
      for (const c of r.commands || []) await handle(c);
    } catch (e) {
      state.connected = false;
      state.error = e.message;
      updateBadge();
      await sleep(15000);
    }
  }
}

function schedule() {
  clearInterval(timer);
  timer = setInterval(scan, Math.max(1, cfg.intervalMin) * 60000);
}

// "Watch e-mails like this" on a message.
async function watchMessage(m) {
  const accounts = await messenger.accounts.list(true);
  const all = BIM.flattenFolders(accounts);
  const entry = all.find(x => x.folder.accountId === m.folder.accountId && x.folder.path === m.folder.path);
  await api("/draft", { method: "POST", body: {
    subject: BIM.normalize(m.subject), folder: entry ? entry.label : (m.folder.name || ""), account: entry ? entry.account : "",
    from: m.author || "", to: [].concat(m.recipients || []).join(", "), date: new Date(m.date).toISOString(),
  } });
}

messenger.menus.create({ id: "bim-watch", title: "BI Monitor: figyelés erre az e-mailre…", contexts: ["message_list"] });
messenger.menus.onClicked.addListener(async info => {
  if (info.menuItemId !== "bim-watch") return;
  const m = info.selectedMessages && info.selectedMessages.messages && info.selectedMessages.messages[0];
  if (!m) return;
  try {
    await watchMessage(m);
  } catch (e) {
    messenger.notifications.create({ type: "basic", iconUrl: "icons/icon-64.png", title: "BI Monitor", message: e.message });
  }
});

// A new message in a watched folder → look again soon.
let soon = null;
messenger.folders.onFolderInfoChanged.addListener(folder => {
  const watched = cfg.folders
    ? cfg.folders.includes(BIM.folderKey(folder))
    : [].concat(folder.specialUse || [], folder.type || []).includes("sent");
  if (!watched) return;
  clearTimeout(soon);
  soon = setTimeout(scan, 20000);
});

messenger.storage.onChanged.addListener(async () => {
  await loadCfg();
  schedule();
  state.connected = false;
  scan();
});

// Popup / options requests.
messenger.runtime.onMessage.addListener(async msg => {
  switch (msg && msg.type) {
    case "getState":
      return { state, cfg: { port: cfg.port, hasKey: !!cfg.key, intervalMin: cfg.intervalMin, notify: cfg.notify, folders: cfg.folders } };
    case "scanNow":
      await scan();
      return { state };
    case "folders":
      return (await allFolders()).map(f => ({ key: f.key, label: f.label, account: f.account, sent: f.sent }));
    case "test":
      return api("/hello", {}, { port: msg.port, key: msg.key });
    case "openMessage":
      await openMessage(msg);
      return true;
    case "showInApp":
      await api("/show", { method: "POST", body: { id: msg.id || "" } });
      return true;
  }
  return undefined;
});

(async () => {
  tbVersion = (await messenger.runtime.getBrowserInfo()).version;
  await loadCfg();
  updateBadge();
  schedule();
  setTimeout(scan, 5000);
  pollLoop();
})();
