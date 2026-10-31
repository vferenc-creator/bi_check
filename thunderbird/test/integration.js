// Runs the real background.js against a running BI Monitor (devserver) with a
// simulated Thunderbird: accounts, folders, messages, menus, notifications.
//   node thunderbird/test/integration.js http://127.0.0.1:8787   (devserver RPC base)
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");
const assert = require("assert");

const RPC = process.argv[2] || "http://127.0.0.1:8787";
const rpc = async (method, params) => {
  const r = await fetch(RPC + "/rpc", { method: "POST", body: JSON.stringify({ method, params }) });
  const d = await r.json();
  if (!d.ok) throw new Error(method + ": " + d.error);
  return d.data;
};
const sleep = ms => new Promise(r => setTimeout(r, ms));
const until = async (what, fn, ms = 15000) => {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error("timeout: " + what);
    await sleep(200);
  }
};

(async () => {
  // 1. Application side: connection on, one e-mail item.
  const port = 47000 + Math.floor(Math.random() * 900);
  const tb = await rpc("tbSave", { enabled: true, port, staleMin: 15 });
  assert.ok(tb.listening, "local API listening");
  const it = await rpc("newItem", null);
  Object.assign(it, { name: "Napi értékesítési e-mail", kind: "email", mail: { subject: "Napi értékesítés {yyyy.MM.dd}", folder: "Elküldött" },
    schedule: { type: "daily", times: ["00:00"] }, graceMinutes: 0, path: "" });
  const saved = await rpc("saveItem", it);
  const id = saved.view.item.id;

  // 2. Simulated Thunderbird.
  const today = new Date();
  const ymd = d => d.getFullYear() + "." + String(d.getMonth() + 1).padStart(2, "0") + "." + String(d.getDate()).padStart(2, "0");
  const sentFolder = { id: "f-sent", accountId: "a1", path: "/Sent", name: "Elküldött", specialUse: ["sent"] };
  const inbox = { id: "f-in", accountId: "a1", path: "/INBOX", name: "Beérkezett", specialUse: ["inbox"] };
  const messages = {
    "f-sent": [
      { id: 101, headerMessageId: "m101@bi", subject: "Napi értékesítés " + ymd(new Date(Date.now() - 86400000)), author: "bi@energofish.hu", recipients: ["vezetes@energofish.hu"], date: new Date(Date.now() - 86400000), folder: sentFolder },
      { id: 102, headerMessageId: "m102@bi", subject: "Más riport", author: "bi@energofish.hu", recipients: [], date: today, folder: sentFolder },
    ],
    "f-in": [{ id: 201, subject: "Napi értékesítés " + ymd(today), author: "x@y.hu", recipients: [], date: today, folder: inbox }],
  };
  const storage = { key: tb.key, port };
  const calls = { opened: [], notes: [], badge: [] };
  const listeners = {};
  const ev = name => ({ addListener: fn => { listeners[name] = fn; } });
  const messenger = {
    runtime: {
      getManifest: () => JSON.parse(fs.readFileSync(path.join(__dirname, "..", "manifest.json"), "utf8")),
      getBrowserInfo: async () => ({ name: "Thunderbird", version: "128.4.0esr" }),
      onMessage: ev("message"), openOptionsPage() {},
    },
    storage: { local: { get: async () => Object.assign({}, storage), set: async v => Object.assign(storage, v) }, onChanged: ev("storage") },
    accounts: { list: async () => [{ id: "a1", name: "BI riport", folders: [inbox, sentFolder] }] },
    messages: {
      query: async q => {
        if (q.headerMessageId) return { messages: Object.values(messages).flat().filter(m => m.headerMessageId === q.headerMessageId) };
        const list = (messages[q.folderId] || []).filter(m => m.date >= q.fromDate);
        return { id: null, messages: list };
      },
      continueList: async () => ({ id: null, messages: [] }),
    },
    messageDisplay: { open: async o => calls.opened.push(o) },
    menus: { create() {}, onClicked: ev("menu") },
    folders: { onFolderInfoChanged: ev("folderInfo") },
    notifications: { create: (a, b) => calls.notes.push(b || a) },
    browserAction: { setBadgeText: o => calls.badge.push(o.text), setBadgeBackgroundColor() {}, setTitle() {} },
  };
  const ctx = vm.createContext({ messenger, fetch, AbortController, setTimeout, clearTimeout, setInterval, clearInterval, console, Date, URL });
  for (const f of ["lib.js", "background.js"]) vm.runInContext(fs.readFileSync(path.join(__dirname, "..", f), "utf8"), ctx, { filename: f });

  // 3. First scan: yesterday's message only (today's is in the inbox, not watched) → missing.
  const st1 = await until("missing", async () => {
    const v = (await rpc("listItems", null)).items.find(x => x.item.id === id);
    return v && v.state.status === "missing" && v;
  });
  console.log("1. első jelentés →", st1.state.status, "|", st1.state.reason);
  assert.ok(calls.badge.some(t => +t >= 1), "badge shows the problem: " + calls.badge.join(","));

  // 4. Today's message appears in Sent; the application asks for a scan (check now).
  messages["f-sent"].push({ id: 103, headerMessageId: "m103@bi", subject: "Napi értékesítés " + ymd(today), author: "bi@energofish.hu", recipients: ["vezetes@energofish.hu"], date: new Date(), folder: sentFolder });
  rpc("checkNow", id).catch(() => {});
  const st2 = await until("ok", async () => {
    const v = (await rpc("listItems", null)).items.find(x => x.item.id === id);
    return v && v.state.status === "ok" && v;
  });
  console.log("2. kiment a mai e-mail →", st2.state.status, "|", st2.state.file.name);

  // 5. "Open in Thunderbird" from the application.
  await rpc("openPath", { id, target: "file" });
  await until("open", async () => calls.opened.length > 0);
  console.log("3. megnyitás Thunderbirdben →", JSON.stringify(calls.opened[0]));
  assert.strictEqual(calls.opened[0].messageId, 103);

  // 6. Context menu on a message → draft for the editor.
  await listeners.menu({ menuItemId: "bim-watch", selectedMessages: { messages: [messages["f-sent"][1]] } });
  const draft = await rpc("takeMailDraft", null);
  console.log("4. figyelés levélből →", draft.subject, "|", draft.account, "/", draft.folder);
  assert.strictEqual(draft.subject, "Más riport");

  // 7. Popup data.
  const state = await listeners.message({ type: "getState" });
  console.log("5. popup →", state.state.connected, state.state.status.items.map(i => i.name + ": " + i.label).join(", "));
  assert.ok(state.state.connected);

  // 8. Wrong key → clear error in the popup.
  storage.key = "rossz";
  await listeners.storage();
  await sleep(500);
  const bad = await listeners.message({ type: "getState" });
  console.log("6. rossz kulcs →", bad.state.connected, "|", bad.state.error);
  assert.ok(!bad.state.connected && /kulcs/.test(bad.state.error));
  await rpc("deleteItem", { id });
  await rpc("tbSave", { enabled: false, port, staleMin: 15 });
  console.log("OK");
  process.exit(0);
})().catch(e => { console.error("HIBA:", e); process.exit(1); });
