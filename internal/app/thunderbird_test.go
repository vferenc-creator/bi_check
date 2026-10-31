package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bimonitor/internal/mailwatch"
	"bimonitor/internal/model"
	"bimonitor/internal/schedule"
	"bimonitor/thunderbird"
)

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func tbReq(t *testing.T, h http.Handler, method, path, key string, body any, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, rd)
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestThunderbirdEmailItem(t *testing.T) {
	a, _, _ := newTestApp(t)
	port := freePort(t)
	st := call[TBStatus](t, a, "tbSave", map[string]any{"enabled": true, "port": port, "staleMin": 15})
	if !st.Listening || len(st.Key) < 20 {
		t.Fatalf("server not started: %+v", st)
	}
	key := st.Key

	// An e-mail item expected every day at 06:00 with today's date in the subject.
	it := model.NewItem()
	it.Name = "Napi értékesítési e-mail"
	it.Kind = model.KindEmail
	it.Mail = model.MailRule{Subject: "Napi értékesítés {yyyy.MM.dd}", Folder: "Elküldött"}
	it.Schedule = schedule.Spec{Type: schedule.Daily, Times: []string{"00:00"}}
	it.GraceMinutes = 0
	sv := call[SaveResult](t, a, "saveItem", it)
	id := sv.View.Item.ID
	if sv.View.Item.Path != "e-mail: Napi értékesítés {yyyy.MM.dd}" {
		t.Fatalf("label path: %q", sv.View.Item.Path)
	}

	h := a.tbHandler()
	// Wrong key, foreign web origin.
	if w := tbReq(t, h, "GET", "/api/tb/v1/rules", "rossz", nil, nil); w.Code != 401 {
		t.Fatalf("wrong key: %d", w.Code)
	}
	if w := tbReq(t, h, "GET", "/api/tb/v1/rules", key, nil, map[string]string{"Origin": "https://evil.example"}); w.Code != 403 {
		t.Fatalf("web origin: %d", w.Code)
	}
	// Rules for the add-on.
	w := tbReq(t, h, "GET", "/api/tb/v1/rules", key, nil, map[string]string{"Origin": "moz-extension://abc"})
	var rules struct{ Rules []mailwatch.Rule }
	json.Unmarshal(w.Body.Bytes(), &rules)
	if w.Code != 200 || len(rules.Rules) != 1 || rules.Rules[0].ID != id || rules.Rules[0].Folder != "Elküldött" {
		t.Fatalf("rules: %d %s", w.Code, w.Body.String())
	}

	wait := func(want model.Status) ItemView {
		t.Helper()
		var v ItemView
		for i := 0; i < 100; i++ {
			v = a.view(id)
			if v.State.Status == want && !v.State.Checking {
				return v
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("want %s, have %s (%s)", want, v.State.Status, v.State.Reason)
		return v
	}

	// Report without the expected message → missing.
	now := time.Now().In(a.Loc)
	yesterday := now.AddDate(0, 0, -1)
	rep := mailwatch.Report{ExtVersion: "1.0", TBVersion: "128.3", Folders: []string{"BI / Elküldött"}, Matches: map[string][]mailwatch.Msg{
		id: {{ID: 7, Subject: "Napi értékesítés " + yesterday.Format("2006.01.02"), Date: yesterday, Folder: "Elküldött", Account: "BI riport"}},
	}}
	if w := tbReq(t, h, "POST", "/api/tb/v1/report", key, rep, nil); w.Code != 200 {
		t.Fatalf("report: %d %s", w.Code, w.Body.String())
	}
	wait(model.StatusMissing)

	// Today's message arrives (in another folder first: filtered out).
	today := mailwatch.Msg{ID: 9, HeaderID: "<abc@x>", Subject: "Napi értékesítés " + now.Format("2006.01.02"), Date: now.Add(-time.Minute), Folder: "Elküldött", Account: "BI riport"}
	other := today
	other.ID, other.Folder = 10, "Beérkezett"
	rep.Matches[id] = append(rep.Matches[id], other)
	tbReq(t, h, "POST", "/api/tb/v1/report", key, rep, nil)
	wait(model.StatusMissing)
	rep.Matches[id] = append(rep.Matches[id], today)
	tbReq(t, h, "POST", "/api/tb/v1/report", key, rep, nil)
	v := wait(model.StatusOK)
	if v.State.File == nil || v.State.File.Name != today.Subject {
		t.Fatalf("found: %+v", v.State.File)
	}

	// Popup status and "open in Thunderbird" command.
	w = tbReq(t, h, "GET", "/api/tb/v1/status", key, nil, nil)
	if !strings.Contains(w.Body.String(), `"messageId":9`) {
		t.Fatalf("status: %s", w.Body.String())
	}
	if err := rpcErr(a, "openPath", map[string]string{"id": id, "target": "file"}); err != nil {
		t.Fatal(err)
	}
	w = tbReq(t, h, "GET", "/api/tb/v1/poll?wait=1", key, nil, nil)
	if !strings.Contains(w.Body.String(), `"type":"open"`) || !strings.Contains(w.Body.String(), `"messageId":9`) {
		t.Fatalf("poll: %s", w.Body.String())
	}

	// "Watch e-mails like this" from Thunderbird → draft for the editor.
	tbReq(t, h, "POST", "/api/tb/v1/draft", key, MailDraft{Subject: "Heti KPI 2026.10.26", Folder: "Elküldött"}, nil)
	d := call[*MailDraft](t, a, "takeMailDraft", nil)
	if d == nil || d.Subject != "Heti KPI 2026.10.26" {
		t.Fatalf("draft: %+v", d)
	}

	// Switching the connection off: e-mail items are "unknown", not problems.
	call[TBStatus](t, a, "tbSave", map[string]any{"enabled": false, "port": port, "staleMin": 15})
	a.Engine.CheckNow(id)
	wait(model.StatusUnknown)
}

func TestMailHubStale(t *testing.T) {
	clock := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	h := mailwatch.New(func() time.Time { return clock })
	it := model.Item{ID: "x", Kind: model.KindEmail, Mail: model.MailRule{Subject: "Riport"}}
	if r := h.Check(it, clock, false, true, 15*time.Minute); r.Kind != "noData" {
		t.Fatalf("first minutes after start: %+v", r)
	}
	clock = clock.Add(5 * time.Minute)
	if r := h.Check(it, clock, false, true, 15*time.Minute); r.Kind != "unreachable" || !r.ServerProblem {
		t.Fatalf("add-on never reported: %+v", r)
	}
	h.Update(mailwatch.Report{Folders: []string{"BI / Elküldött"}, Matches: map[string][]mailwatch.Msg{"x": {{Subject: "Riport ma", Date: clock}}}})
	if r := h.Check(it, clock, false, true, 15*time.Minute); r.Kind != "found" {
		t.Fatalf("%+v", r)
	}
	clock = clock.Add(20 * time.Minute)
	if r := h.Check(it, clock, false, true, 15*time.Minute); r.Kind != "unreachable" || !strings.Contains(r.Reason, fmt.Sprint(20)) {
		t.Fatalf("stale: %+v", r)
	}
}

func TestThunderbirdOneClickInstall(t *testing.T) {
	root := t.TempDir()
	// Two profiles; Thunderbird starts with "work" ([Install…] section).
	ini := "[Install4F96D1932A9F858E]\nDefault=Profiles/abcd.work\nLocked=1\n\n" +
		"[Profile1]\nName=default\nIsRelative=1\nPath=Profiles/x1.default\nDefault=1\n\n" +
		"[Profile0]\nName=work\nIsRelative=1\nPath=Profiles/abcd.work\n\n[General]\nStartWithLastProfile=1\nVersion=2\n"
	os.MkdirAll(filepath.Join(root, "Profiles", "abcd.work"), 0o755)
	os.MkdirAll(filepath.Join(root, "Profiles", "x1.default"), 0o755)
	os.WriteFile(filepath.Join(root, "profiles.ini"), []byte(ini), 0o644)

	dir := t.TempDir()
	a, err := New(&testPlatform{}, Options{SettingsPath: filepath.Join(dir, "s.json"), ThunderbirdRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	port := freePort(t)
	call[TBStatus](t, a, "tbSave", map[string]any{"enabled": false, "port": port, "staleMin": 15})

	res := call[TBInstallResult](t, a, "tbInstall", nil)
	work := filepath.Join(root, "Profiles", "abcd.work", "extensions", "bimonitor@energofish.hu.xpi")
	if _, err := os.Stat(work); err != nil {
		t.Fatalf("not installed into the default profile: %v (%+v)", err, res)
	}
	if _, err := os.Stat(filepath.Join(root, "Profiles", "x1.default", "extensions", "bimonitor@energofish.hu.xpi")); err == nil {
		t.Fatal("must not touch the other profile")
	}
	st := call[TBStatus](t, a, "tbStatus", nil)
	if !st.Enabled || !st.Listening || st.Key == "" {
		t.Fatalf("install switches the connection on: %+v", st)
	}
	if len(st.Profiles) != 2 || !st.Profiles[0].Default || !st.Profiles[0].Installed || st.Profiles[0].Name != "work" {
		t.Fatalf("profiles: %+v", st.Profiles)
	}
	cfg := readXPIConfig(t, work)
	if cfg.Key != st.Key || cfg.Port != port || cfg.Issued == "" {
		t.Fatalf("built-in config: %+v", cfg)
	}
	// Thunderbird running → the message says to restart it.
	holdProfileLock(t, filepath.Join(root, "Profiles", "abcd.work"))
	if r := call[TBInstallResult](t, a, "tbInstall", nil); !r.Running || !strings.Contains(r.Message, "indítsa újra") {
		t.Fatalf("running: %+v", r)
	}
	// New key → the installed package is rewritten with it.
	st2 := call[TBStatus](t, a, "tbNewKey", nil)
	if c := readXPIConfig(t, work); c.Key != st2.Key || c.Issued == cfg.Issued {
		t.Fatalf("new key not in the package: %+v", c)
	}
}

func readXPIConfig(t *testing.T, path string) thunderbird.Config {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var cfg thunderbird.Config
	seen := map[string]bool{}
	for _, f := range zr.File {
		seen[f.Name] = true
		if f.Name == "config.json" {
			rc, _ := f.Open()
			json.NewDecoder(rc).Decode(&cfg)
			rc.Close()
		}
	}
	for _, n := range []string{"manifest.json", "background.js", "lib.js", "popup.html", "options.html", "icons/icon-64.png"} {
		if !seen[n] {
			t.Fatalf("%s missing from the package", n)
		}
	}
	return cfg
}

// Shared mode: both computers watch the same e-mail item from the shared
// folder, each through its own Thunderbird. A computer whose Thunderbird
// does not have the sending account must not report "missing".
func TestEmailItemsInSharedMode(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "BI_Check_kozos")
	os.MkdirAll(shared, 0o755)
	anna, _ := newUserApp(t, "anna")
	bela, _ := newUserApp(t, "bela")
	for _, a := range []*App{anna, bela} {
		call[enableRes](t, a, "teamEnable", map[string]string{"folder": shared})
	}
	keyA := call[TBStatus](t, anna, "tbSave", map[string]any{"enabled": true, "port": freePort(t), "staleMin": 15}).Key
	keyB := call[TBStatus](t, bela, "tbSave", map[string]any{"enabled": true, "port": freePort(t), "staleMin": 15}).Key

	// Anna adds the e-mail item (as from Thunderbird: account / folder).
	it := model.NewItem()
	it.ID = ""
	it.Name = "Napi értékesítési e-mail"
	it.Kind = model.KindEmail
	it.Mail = model.MailRule{Subject: "Napi értékesítés {yyyy.MM.dd}", Folder: "BI riport / Elküldött"}
	it.Schedule = schedule.Spec{Type: schedule.Daily, Times: []string{"00:00"}}
	it.GraceMinutes = 0
	sv := call[SaveResult](t, anna, "saveItem", it)
	id := sv.View.Item.ID

	// Béla sees it from the shared folder, with the e-mail settings intact.
	call[Snapshot](t, bela, "teamSyncNow", nil)
	bv := bela.view(id)
	if !bv.Item.IsEmail() || bv.Item.Mail.Folder != "BI riport / Elküldött" {
		t.Fatalf("shared definition: %+v", bv.Item)
	}
	// Both Thunderbirds get the rule.
	for _, c := range []struct {
		a   *App
		key string
	}{{anna, keyA}, {bela, keyB}} {
		w := tbReq(t, c.a.tbHandler(), "GET", "/api/tb/v1/rules", c.key, nil, nil)
		if !strings.Contains(w.Body.String(), id) {
			t.Fatalf("rules: %s", w.Body.String())
		}
	}

	wait := func(a *App, want model.Status) ItemView {
		t.Helper()
		var v ItemView
		for i := 0; i < 150; i++ {
			v = a.view(id)
			if v.State.Status == want && !v.State.Checking {
				return v
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("%s: want %s, have %s (%s)", a.opt.Identity.Display, want, v.State.Status, v.State.Reason)
		return v
	}
	now := time.Now().In(anna.Loc)
	sent := mailwatch.Msg{ID: 5, Subject: "Napi értékesítés " + now.Format("2006.01.02"), Date: now.Add(-time.Minute), Folder: "Elküldött", Account: "BI riport"}

	// Anna's Thunderbird has the BI account: OK.
	tbReq(t, anna.tbHandler(), "POST", "/api/tb/v1/report", keyA, mailwatch.Report{Folders: []string{"BI riport / Elküldött", "Anna / Elküldött"},
		Matches: map[string][]mailwatch.Msg{id: {sent}}}, nil)
	wait(anna, model.StatusOK)

	// Béla's Thunderbird only has his own mailbox: not "missing", just unknown here.
	tbReq(t, bela.tbHandler(), "POST", "/api/tb/v1/report", keyB, mailwatch.Report{Folders: []string{"Béla / Elküldött"}, Matches: map[string][]mailwatch.Msg{id: {}}}, nil)
	v := wait(bela, model.StatusUnknown)
	if !strings.Contains(v.State.Reason, "BI riport / Elküldött") {
		t.Fatalf("reason: %s", v.State.Reason)
	}
	// Once Béla's Thunderbird also has the account, he checks it too.
	tbReq(t, bela.tbHandler(), "POST", "/api/tb/v1/report", keyB, mailwatch.Report{Folders: []string{"Béla / Elküldött", "BI riport / Elküldött"},
		Matches: map[string][]mailwatch.Msg{id: {sent}}}, nil)
	wait(bela, model.StatusOK)

	// Béla edits the e-mail item; Anna's application and Thunderbird follow.
	lock := call[map[string]any](t, bela, "lockItem", id)
	if lock["ok"] != true {
		t.Fatalf("lock: %v", lock)
	}
	edit := bela.view(id).Item
	edit.Mail.Subject = "Napi értékesítési riport {yyyy.MM.dd}"
	call[SaveResult](t, bela, "saveItem", edit)
	call[Snapshot](t, anna, "teamSyncNow", nil)
	if got := anna.view(id).Item.Mail.Subject; got != edit.Mail.Subject {
		t.Fatalf("anna sees %q", got)
	}
	w := tbReq(t, anna.tbHandler(), "GET", "/api/tb/v1/rules", keyA, nil, nil)
	if !strings.Contains(w.Body.String(), `riport \\d{4}`) {
		t.Fatalf("anna's Thunderbird gets the new rule: %s", w.Body.String())
	}
}

func TestMailHubRecent(t *testing.T) {
	h := mailwatch.New(nil)
	go func() {
		// the add-on picks up the "recent" command and answers
		cmds := h.Poll(context.Background(), time.Second)
		if len(cmds) != 1 || cmds[0].Type != "recent" {
			t.Errorf("command: %+v", cmds)
		}
		h.SetRecent([]mailwatch.Msg{{Subject: "régi", Date: time.Now().Add(-time.Hour)}, {Subject: "új", Date: time.Now()}})
	}()
	list, at, fresh := h.Recent(context.Background(), 2*time.Second)
	if !fresh || at.IsZero() || len(list) != 2 || list[0].Subject != "új" {
		t.Fatalf("%v %v %+v", fresh, at, list)
	}
	// No answer: the cached list comes back, marked not fresh.
	list, _, fresh = h.Recent(context.Background(), 50*time.Millisecond)
	if fresh || len(list) != 2 {
		t.Fatalf("cached: %v %+v", fresh, list)
	}
}
