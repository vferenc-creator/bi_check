/* global messenger, BIM */
"use strict";
const $ = id => document.getElementById(id);
const h = (tag, attrs, ...kids) => {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "style") Object.assign(el.style, v);
    else el.setAttribute(k, v);
  }
  for (const c of kids.flat()) if (c != null && c !== false) el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  return el;
};
const send = msg => messenger.runtime.sendMessage(msg);

function pill(it) {
  const c = BIM.STATUS_COLOR[it.status] || "#9AA5B1";
  return h("span", { class: "pill", style: { background: c + "22", color: c } }, h("span", { class: "dot", style: { background: c } }), it.label);
}

async function render() {
  const { state, cfg } = await send({ type: "getState" });
  const conn = $("conn");
  conn.replaceChildren(h("span", { class: "dot", style: { background: state.connected ? "#2E9E5B" : "#7C5CC4" } }), state.connected ? "kapcsolódva" : "nincs kapcsolat");
  const body = $("body");
  body.replaceChildren();
  if (!cfg.hasKey) {
    body.append(h("div", { class: "empty" }, "A bővítmény még nincs összekapcsolva a BI Monitorral.", h("br"),
      "Nyissa meg a ", h("b", {}, "Beállításokat"), ", és másolja be a párosító kulcsot (BI Monitor → Beállítások → Thunderbird)."));
    return;
  }
  if (!state.connected) {
    body.append(h("div", { class: "box err" }, state.error || "Nincs kapcsolat a BI Monitorral."));
  }
  const st = state.status;
  body.append(h("div", { class: "meta" },
    (state.lastScan ? "Utolsó keresés: " + BIM.fmtDate(state.lastScan) : "Még nem volt keresés") +
    " · " + state.folders.length + " figyelt mappa" + (state.scanError ? " · hiba: " + state.scanError : "")));
  const items = (st && st.items) || [];
  if (!items.length) {
    body.append(h("div", { class: "empty" }, "Nincs e-mail figyelés.", h("br"),
      "Egy levélen jobb gomb → ", h("b", {}, "BI Monitor: figyelés erre az e-mailre…"), ", vagy a BI Monitorban: Új elem → E-mail."));
    return;
  }
  items.sort((a, b) => b.severity - a.severity || a.name.localeCompare(b.name, "hu"));
  for (const it of items) {
    body.append(h("div", { class: "item" },
      h("div", { class: "main" },
        h("div", { class: "name" }, it.name),
        h("div", { class: "subj" }, "Tárgy: ", it.subject, " · ", it.schedule),
        it.last ? h("div", { class: "last" }, "Utolsó: ", h("b", {}, BIM.fmtDate(it.last.date)), " – ", it.last.subject, h("span", { class: "muted" }, " (" + it.last.folder + ")")) : null,
        it.severity >= 2 ? h("div", { class: "reason" }, it.reason) : null),
      h("div", { class: "acts" }, pill(it),
        it.last && (it.last.messageId || it.last.headerId) ? h("button", { class: "btn", onclick: () => send({ type: "openMessage", messageId: it.last.messageId, headerId: it.last.headerId }).then(() => window.close()) }, "Megnyitás") : null,
        h("button", { class: "btn", title: "Megnyitás a BI Monitorban", onclick: () => send({ type: "showInApp", id: it.id }).then(() => window.close()) }, "Részletek"))));
  }
}

$("scan").addEventListener("click", async () => {
  $("scan").disabled = true;
  $("scan").textContent = "Keresés…";
  await send({ type: "scanNow" }).catch(() => {});
  $("scan").disabled = false;
  $("scan").textContent = "Keresés most";
  render();
});
$("app").addEventListener("click", () => send({ type: "showInApp" }).then(() => window.close()).catch(e => alert(e.message)));
$("opts").addEventListener("click", () => { messenger.runtime.openOptionsPage(); window.close(); });
render();
