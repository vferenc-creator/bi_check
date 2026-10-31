package app

import (
	"archive/zip"
	"bytes"
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
	h.Update(mailwatch.Report{Matches: map[string][]mailwatch.Msg{"x": {{Subject: "Riport ma", Date: clock}}}})
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
	os.Symlink("x", filepath.Join(root, "Profiles", "abcd.work", "lock"))
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
