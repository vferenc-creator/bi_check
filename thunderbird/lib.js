// Pure helpers of the BI Monitor add-on (no Thunderbird API calls), shared by
// the background page, the popup and the options page – and the unit tests.
(function (root) {
  "use strict";

  const normalize = s => String(s || "").replace(/\s+/g, " ").trim();

  // Unique, stable key of a folder (survives Thunderbird restarts).
  const folderKey = f => (f.accountId || "") + ":" + (f.path || "");

  // Flattens accounts → folders, with a readable label ("Fiók / Elküldött / Riportok").
  function flattenFolders(accounts) {
    const out = [];
    const walk = (acc, folders, parents) => {
      for (const f of folders || []) {
        const names = parents.concat(f.name || f.path);
        const special = [].concat(f.specialUse || [], f.type ? [f.type] : []);
        out.push({ key: folderKey(f), folder: f, account: acc.name, label: names.join(" / "), sent: special.includes("sent"), inbox: special.includes("inbox"), special });
        walk(acc, f.subFolders, names);
      }
    };
    for (const a of accounts || []) walk(a, a.folders, []);
    return out;
  }

  // Default selection: every Inbox ("Beérkezett üzenetek").
  function watchedFolders(all, keys) {
    if (!keys) return all.filter(f => f.inbox);
    const set = new Set(keys);
    return all.filter(f => set.has(f.key));
  }

  function compileRules(rules) {
    return (rules || []).map(r => {
      let re = null;
      try { re = new RegExp(r.regex, "i"); } catch (_) { /* invalid → never matches */ }
      return Object.assign({}, r, { re, folderLc: normalize(r.folder).toLowerCase(), partyLc: normalize(r.party).toLowerCase() });
    });
  }

  const recipients = m => [].concat(m.recipients || [], m.ccList || [], m.bccList || []).join(", ");

  // Does message header m (in folder entry f) match compiled rule r?
  function matches(r, m, f) {
    if (!r.re || !r.re.test(normalize(m.subject))) return false;
    if (r.folderLc && !(f.account + " / " + f.label).toLowerCase().includes(r.folderLc)) return false;
    if (r.partyLc && !((m.author || "") + " " + recipients(m)).toLowerCase().includes(r.partyLc)) return false;
    return true;
  }

  // What the application receives about a message.
  function toMsg(m, f) {
    const d = m.date instanceof Date ? m.date : new Date(m.date);
    return { id: m.id, headerId: m.headerMessageId || "", subject: normalize(m.subject), date: d.toISOString(), folder: f.label, account: f.account, from: m.author || "", to: recipients(m) };
  }

  // Keep the newest n messages per rule.
  function trim(matchesById, n) {
    for (const id of Object.keys(matchesById)) {
      matchesById[id].sort((a, b) => (a.date < b.date ? 1 : -1));
      matchesById[id] = matchesById[id].slice(0, n);
    }
    return matchesById;
  }

  // Newest n messages (ISO dates sort as text).
  function newest(list, n) {
    return list.slice().sort((a, b) => (a.date < b.date ? 1 : -1)).slice(0, n);
  }

  // Badge: number of problems, "!" when the application is not reachable.
  function badge(connected, status) {
    if (!connected) return { text: "!", color: "#7C5CC4", title: "BI Monitor – nincs kapcsolat az alkalmazással" };
    const p = status ? status.problems : 0;
    if (p > 0) return { text: String(p), color: "#D64545", title: "BI Monitor – " + p + " e-mail probléma" };
    return { text: "", color: "#2E9E5B", title: "BI Monitor – minden e-mail rendben" };
  }

  // Items that became problems since the previous status (for notifications).
  function newProblems(prev, cur) {
    const was = new Map(((prev && prev.items) || []).map(i => [i.id, i.severity]));
    return ((cur && cur.items) || []).filter(i => i.severity >= 2 && !(was.get(i.id) >= 2) && prev);
  }

  const STATUS_COLOR = { ok: "#2E9E5B", waiting: "#5A8DB8", late: "#D99A00", missing: "#D64545", unreachable: "#7C5CC4", suspicious: "#E07400", disabled: "#9AA5B1", unknown: "#9AA5B1" };

  function fmtDate(iso) {
    if (!iso) return "–";
    const d = new Date(iso), now = new Date();
    const hm = d.toLocaleTimeString("hu-HU", { hour: "2-digit", minute: "2-digit" });
    const day = x => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
    const diff = Math.round((day(now) - day(d)) / 86400000);
    if (diff === 0) return "ma " + hm;
    if (diff === 1) return "tegnap " + hm;
    if (diff === -1) return "holnap " + hm;
    return d.toLocaleDateString("hu-HU", { month: "2-digit", day: "2-digit" }) + " " + hm;
  }

  const api = { normalize, folderKey, flattenFolders, watchedFolders, compileRules, matches, toMsg, trim, newest, badge, newProblems, STATUS_COLOR, fmtDate };
  root.BIM = api;
  if (typeof module !== "undefined") module.exports = api;
})(typeof globalThis !== "undefined" ? globalThis : this);
