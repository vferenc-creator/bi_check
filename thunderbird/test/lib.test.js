// node --test thunderbird/test — unit tests of the pure add-on helpers.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const BIM = require("../lib.js");

const accounts = [{
  id: "a1", name: "BI riport",
  folders: [
    { accountId: "a1", path: "/INBOX", name: "Beérkezett", type: "inbox" },
    { accountId: "a1", path: "/Sent", name: "Elküldött", specialUse: ["sent"], subFolders: [
      { accountId: "a1", path: "/Sent/Archiv", name: "Archív" }] },
  ],
}];

test("folders: flatten, labels, default = sent folders", () => {
  const all = BIM.flattenFolders(accounts);
  assert.deepStrictEqual(all.map(f => f.label), ["Beérkezett", "Elküldött", "Elküldött / Archív"]);
  assert.deepStrictEqual(BIM.watchedFolders(all, null).map(f => f.key), ["a1:/Sent"]);
  assert.deepStrictEqual(BIM.watchedFolders(all, ["a1:/INBOX"]).map(f => f.label), ["Beérkezett"]);
});

test("rules: Go regex runs in JS, folder and party filters", () => {
  // regex as produced by pathpattern.Text.LooseRegex for "Napi riport {yyyy.MM.dd}"
  const [r] = BIM.compileRules([{ id: "x", regex: ".*Napi riport \\d{4}\\.\\d{2}\\.\\d{2}.*", folder: "elküldött", party: "kontrolling" }]);
  const sent = BIM.flattenFolders(accounts)[1];
  const m = { id: 1, subject: "Napi  riport 2026.10.31", author: "bi@energofish.hu", recipients: ["Kontrolling <k@energofish.hu>"], date: new Date("2026-10-31T06:02:00Z") };
  assert.ok(BIM.matches(r, m, sent));
  assert.ok(!BIM.matches(r, Object.assign({}, m, { recipients: ["x@y.hu"] }), sent));
  assert.ok(!BIM.matches(r, m, BIM.flattenFolders(accounts)[0]));
  const out = BIM.toMsg(m, sent);
  assert.strictEqual(out.subject, "Napi riport 2026.10.31");
  assert.strictEqual(out.account, "BI riport");
  assert.strictEqual(out.date, "2026-10-31T06:02:00.000Z");
});

test("invalid regex never matches, badge and new problems", () => {
  const [r] = BIM.compileRules([{ id: "x", regex: "(" }]);
  assert.ok(!BIM.matches(r, { subject: "(" }, { account: "", label: "" }));
  assert.strictEqual(BIM.badge(false, null).text, "!");
  assert.strictEqual(BIM.badge(true, { problems: 2 }).text, "2");
  const prev = { items: [{ id: "a", severity: 1 }, { id: "b", severity: 3 }] };
  const cur = { items: [{ id: "a", severity: 3 }, { id: "b", severity: 3 }] };
  assert.deepStrictEqual(BIM.newProblems(prev, cur).map(i => i.id), ["a"]);
  assert.deepStrictEqual(BIM.newProblems(null, cur), []); // first status: no burst of notifications
});
