package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bimonitor/internal/model"
	"bimonitor/internal/schedule"
)

type testPlatform struct {
	NullPlatform
	toasts []string
}

func (p *testPlatform) Notify(title, text string, _ NotifyKind) {
	p.toasts = append(p.toasts, title+": "+text)
}

func newTestApp(t *testing.T) (*App, *testPlatform, string) {
	t.Helper()
	dir := t.TempDir()
	p := &testPlatform{}
	a, err := New(p, Options{SettingsPath: filepath.Join(dir, "settings.json"), HistoryPath: filepath.Join(dir, "history.db")})
	if err != nil {
		t.Fatal(err)
	}
	a.Start()
	t.Cleanup(a.Close)
	return a, p, dir
}

func call[T any](t *testing.T, a *App, method string, params any) T {
	t.Helper()
	b, _ := json.Marshal(params)
	res, err := a.Call(method, b)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	var out T
	rb, _ := json.Marshal(res)
	if err := json.Unmarshal(rb, &out); err != nil {
		t.Fatalf("%s result: %v", method, err)
	}
	return out
}

func TestItemCRUDAndCheck(t *testing.T) {
	a, _, dir := newTestApp(t)
	file := filepath.Join(dir, "export.csv")
	os.WriteFile(file, []byte("x"), 0o644)

	it := call[model.Item](t, a, "newItem", nil)
	it.Name = "  Export  "
	it.Path = file
	it.Schedule = schedule.Spec{Type: schedule.Hourly}
	it.EarlyMinutes = 59
	saved := call[SaveResult](t, a, "saveItem", it)
	if saved.View.Item.ID == "" || saved.View.Item.Name != "Export" {
		t.Fatalf("%+v", saved)
	}
	id := saved.View.Item.ID

	snap := call[Snapshot](t, a, "checkNow", id)
	if len(snap.Items) != 1 || snap.Items[0].State.Status != model.StatusOK {
		t.Fatalf("after check: %+v", snap.Items)
	}

	dup := call[ItemView](t, a, "duplicateItem", map[string]string{"id": id})
	if dup.Item.ID == id || !strings.Contains(dup.Item.Name, "másolat") {
		t.Fatalf("dup %+v", dup.Item)
	}
	off := call[ItemView](t, a, "setEnabled", map[string]any{"id": dup.Item.ID, "enabled": false})
	if off.Item.Enabled || off.State.Status != model.StatusDisabled {
		t.Fatalf("disable: %+v", off)
	}
	if _, err := a.Call("deleteItem", json.RawMessage(`{"id":"`+dup.Item.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if n := len(call[Snapshot](t, a, "listItems", nil).Items); n != 1 {
		t.Fatalf("items after delete: %d", n)
	}
}

func TestValidation(t *testing.T) {
	a, _, _ := newTestApp(t)
	bad := []model.Item{
		{Name: "", Path: `\\s\x\a.csv`, Schedule: schedule.Spec{Type: schedule.Hourly}},
		{Name: "x", Path: "", Schedule: schedule.Spec{Type: schedule.Hourly}},
		{Name: "x", Path: `\\s\*\a.csv`, Schedule: schedule.Spec{Type: schedule.Hourly}},
		{Name: "x", Path: `\\s\x\a.csv`, Schedule: schedule.Spec{Type: schedule.Daily}},
		{Name: "x", Path: `\\s\x\a.csv`, Schedule: schedule.Spec{Type: schedule.Hourly}, GraceMinutes: -1},
	}
	for _, it := range bad {
		b, _ := json.Marshal(it)
		if _, err := a.Call("saveItem", b); err == nil {
			t.Errorf("expected error for %+v", it)
		}
	}
}

func TestPreviewAndTestPath(t *testing.T) {
	a, _, dir := newTestApp(t)
	pv := call[Preview](t, a, "previewSchedule", PreviewParams{Schedule: schedule.Spec{Type: schedule.Daily, Times: []string{"06:00"}}, Count: 5})
	if pv.Error != "" || len(pv.Next) != 5 || pv.Prev == nil || pv.Text != "Naponta 06:00" {
		t.Fatalf("%+v", pv)
	}
	pv = call[Preview](t, a, "previewSchedule", PreviewParams{Schedule: schedule.Spec{Type: schedule.Cron, Cron: "x"}})
	if pv.Error == "" {
		t.Fatal("expected error")
	}
	f := filepath.Join(dir, "r_"+time.Now().Format("20060102")+".csv")
	os.WriteFile(f, []byte("abc"), 0o644)
	it := model.NewItem()
	it.Name = "R"
	it.Path = filepath.Join(dir, "r_{yyyyMMdd}.csv")
	it.Schedule = schedule.Spec{Type: schedule.Hourly}
	it.EarlyMinutes = 59
	tr := call[TestResult](t, a, "testPath", it)
	if tr.Error != "" || tr.Status != model.StatusOK {
		t.Fatalf("%+v", tr)
	}
}

func TestUnknownMethod(t *testing.T) {
	a, _, _ := newTestApp(t)
	if _, err := a.Call("nope", nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestCSVRoundTrip(t *testing.T) {
	it := model.NewItem()
	it.Name = "Értékesítés; \"napi\""
	it.Path = `\\EFS-FSRHQ\Groups\BI\sales_{yyyyMMdd}.csv`
	it.Group = "KNIME"
	it.Schedule = schedule.Spec{Type: schedule.Weekly, Weekdays: []int{1, 3}, Times: []string{"07:00"}, HolidayRule: schedule.HolidayNext}
	it.Suspicious.MinBytes = 2048
	data := EncodeCSV([]model.Item{it})
	back, err := DecodeItems(data)
	if err != nil || len(back) != 1 {
		t.Fatal(err, back)
	}
	b := back[0]
	if b.Name != it.Name || b.Path != it.Path || b.Schedule.HolidayRule != schedule.HolidayNext || len(b.Schedule.Weekdays) != 2 || b.Suspicious.MinBytes != 2048 || b.ID != it.ID {
		t.Fatalf("%+v", b)
	}
}

func TestImportMerge(t *testing.T) {
	a, _, dir := newTestApp(t)
	it := model.NewItem()
	it.Name = "A"
	it.Path = filepath.Join(dir, "a.csv")
	saved := call[SaveResult](t, a, "saveItem", it)

	upd := saved.View.Item
	upd.Name = "A frissítve"
	other := model.NewItem()
	other.Name = "B"
	other.Path = filepath.Join(dir, "b.csv")
	samePath := model.NewItem()
	samePath.Name = "A más ID-vel"
	samePath.Path = filepath.Join(dir, "a.csv")
	bad := model.NewItem()
	bad.Name = ""
	bad.Path = "x"
	rows := a.previewImport([]model.Item{upd, other, samePath, bad})
	if rows[0].Action != "update" || rows[1].Action != "add" || rows[2].Action != "update" || rows[3].Action != "invalid" {
		t.Fatalf("%+v", rows)
	}
	counts, err := a.applyImport(rows[:2])
	if err != nil || counts["added"] != 1 || counts["updated"] != 1 {
		t.Fatal(counts, err)
	}
	items := a.Settings.Get().Items
	if len(items) != 2 || items[0].Name != "A frissítve" {
		t.Fatalf("%+v", items)
	}
}

func TestSharedList(t *testing.T) {
	a, _, dir := newTestApp(t)
	it := model.NewItem()
	it.Name = "Csapat export"
	it.Path = filepath.Join(dir, "team.csv")
	doc := ExportFile{Format: ExportFormat, Version: 1, Items: []model.Item{it}}
	b, _ := json.Marshal(doc)
	listPath := filepath.Join(dir, "csapat.json")
	os.WriteFile(listPath, b, 0o644)

	st := call[[]SharedStatus](t, a, "saveSharedLists", []model.SharedList{{Name: "BI csapat", Path: listPath, Enabled: true}})
	if len(st) != 1 || st[0].Count != 1 || st[0].Error != "" {
		t.Fatalf("%+v", st)
	}
	snap := call[Snapshot](t, a, "listItems", nil)
	if len(snap.Items) != 1 || snap.Items[0].Item.Source != "BI csapat" || !strings.HasPrefix(snap.Items[0].Item.ID, sharedPrefix) {
		t.Fatalf("%+v", snap.Items)
	}
	id := snap.Items[0].Item.ID
	// Shared items cannot be edited or deleted, but can be switched off locally.
	sh := snap.Items[0].Item
	sb, _ := json.Marshal(sh)
	if _, err := a.Call("saveItem", sb); err == nil {
		t.Fatal("editing a shared item must fail")
	}
	v := call[ItemView](t, a, "setEnabled", map[string]any{"id": id, "enabled": false})
	if v.Item.Enabled || v.State.Status != model.StatusDisabled {
		t.Fatalf("override: %+v", v)
	}
	// File changes are picked up.
	it2 := model.NewItem()
	it2.Name = "Második"
	it2.Path = filepath.Join(dir, "b.csv")
	doc.Items = append(doc.Items, it2)
	b, _ = json.Marshal(doc)
	os.WriteFile(listPath, b, 0o644)
	future := time.Now().Add(time.Minute)
	os.Chtimes(listPath, future, future)
	st = call[[]SharedStatus](t, a, "reloadSharedLists", nil)
	if st[0].Count != 2 {
		t.Fatalf("reload: %+v", st)
	}
	// Broken file: keep the last good items, report the error.
	os.WriteFile(listPath, []byte("{broken"), 0o644)
	os.Chtimes(listPath, future.Add(time.Minute), future.Add(time.Minute))
	st = call[[]SharedStatus](t, a, "reloadSharedLists", nil)
	if st[0].Error == "" || st[0].Count != 2 {
		t.Fatalf("broken: %+v", st)
	}
}

// The sample list attached to every release must stay importable.
func TestSampleListIsValid(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "BIMonitor_minta_elemek.json"))
	if err != nil {
		t.Fatal(err)
	}
	items, err := DecodeItems(data)
	if err != nil || len(items) == 0 {
		t.Fatal(err, len(items))
	}
	a, _, _ := newTestApp(t)
	for _, r := range a.previewImport(items) {
		if r.Action == "invalid" {
			t.Errorf("%s: %s", r.Item.Name, r.Error)
		}
	}
}

func sleepMs(n int) { time.Sleep(time.Duration(n) * time.Millisecond) }
