package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bimonitor/internal/calendar"
	"bimonitor/internal/model"
	"bimonitor/internal/schedule"
	"bimonitor/internal/teamstore"
)

// newUserApp starts an App for one simulated user/PC.
func newUserApp(t *testing.T, user string) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	a, err := New(&testPlatform{}, Options{
		SettingsPath: filepath.Join(dir, "settings.json"),
		HistoryPath:  filepath.Join(dir, "history.db"),
		LocalDir:     filepath.Join(dir, "local"),
		Identity:     teamstore.Identity{User: `EF\` + user, Display: user, Host: "PC-" + user},
	})
	if err != nil {
		t.Fatal(err)
	}
	a.Start()
	t.Cleanup(a.Close)
	return a, dir
}

func localItem(t *testing.T, a *App, name, path string) model.Item {
	t.Helper()
	it := model.NewItem()
	it.ID = ""
	it.Name = name
	it.Path = path
	it.Schedule = schedule.Spec{Type: schedule.Daily, Times: []string{"06:00"}}
	return call[SaveResult](t, a, "saveItem", it).View.Item
}

func rpcErr(a *App, method string, params any) error {
	b, _ := json.Marshal(params)
	_, err := a.Call(method, b)
	return err
}

type enableRes struct {
	Check     TeamCheck      `json:"check"`
	Migration []MigrationRow `json:"migration"`
}

func TestSharedModeTwoUsers(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "BI_Check_kozos")
	os.MkdirAll(shared, 0o755)

	anna, annaDir := newUserApp(t, "anna")
	bela, _ := newUserApp(t, "bela")
	pathA := filepath.Join(annaDir, "export.csv")
	pathB := filepath.Join(annaDir, "riport.csv")
	localItem(t, anna, "Export", pathA)
	localItem(t, anna, "Riport", pathB)
	localItem(t, bela, "Béla exportja", pathA) // same path as Anna's → duplicate

	// --- Anna enables shared mode and copies her items.
	tc := call[TeamCheck](t, anna, "teamCheck", `"`+shared+`"`)
	if !tc.OK || !tc.Probe.Writable || !tc.Probe.Empty || tc.Folder != shared {
		t.Fatalf("check: %+v", tc)
	}
	en := call[enableRes](t, anna, "teamEnable", map[string]string{"folder": shared})
	if len(en.Migration) != 2 || en.Migration[0].Action != "add" {
		t.Fatalf("migration preview: %+v", en.Migration)
	}
	counts := call[map[string]int](t, anna, "teamMigrate", en.Migration)
	if counts["added"] != 2 {
		t.Fatalf("%+v", counts)
	}
	snap := call[Snapshot](t, anna, "listItems", nil)
	if len(snap.Items) != 2 || snap.Team == nil || !snap.Team.Status.Online || snap.Items[0].Team == nil {
		t.Fatalf("anna snapshot: %+v", snap)
	}
	var exportID string
	for _, v := range snap.Items {
		if v.Item.Name == "Export" {
			exportID = v.Item.ID
		}
	}

	// --- Béla joins: his local duplicate is offered as "skip".
	en = call[enableRes](t, bela, "teamEnable", map[string]string{"folder": shared})
	if len(en.Migration) != 1 || en.Migration[0].Action != "skip" || en.Migration[0].Duplicate != "Export" {
		t.Fatalf("bela migration: %+v", en.Migration)
	}
	call[map[string]int](t, bela, "teamMigrate", en.Migration)
	snapB := call[Snapshot](t, bela, "listItems", nil)
	if len(snapB.Items) != 2 {
		t.Fatalf("bela sees %d items", len(snapB.Items))
	}

	// --- Anna opens Export for editing → Béla sees the lock and cannot edit.
	type lockRes struct {
		OK     bool       `json:"ok"`
		Lock   *LockView  `json:"lock"`
		Rev    int        `json:"rev"`
		Item   model.Item `json:"item"`
		Reason string     `json:"reason"`
	}
	la := call[lockRes](t, anna, "lockItem", exportID)
	if !la.OK || la.Rev != 1 {
		t.Fatalf("anna lock: %+v", la)
	}
	lb := call[lockRes](t, bela, "lockItem", exportID)
	if lb.OK || lb.Lock == nil || lb.Lock.Name != "anna" || !strings.Contains(lb.Reason, "anna (PC-anna)") {
		t.Fatalf("bela should see anna's lock: %+v", lb)
	}
	call[Snapshot](t, bela, "teamSyncNow", nil)
	for _, v := range call[Snapshot](t, bela, "listItems", nil).Items {
		if v.Item.ID == exportID && (v.Team.Lock == nil || v.Team.Lock.Mine) {
			t.Fatalf("lock not visible in bela's list: %+v", v.Team)
		}
	}
	edited := lb.Item
	edited.Name = "Béla próbálja"
	if err := rpcErr(bela, "saveItem", edited); err == nil || !strings.Contains(err.Error(), "anna") {
		t.Fatalf("bela must not save while anna edits: %v", err)
	}

	// --- Anna saves: lock released, revision 2, Béla gets a notice.
	mine := la.Item
	mine.GraceMinutes = 45
	res := call[SaveResult](t, anna, "saveItem", mine)
	if res.Conflict != nil || res.View.Team == nil || res.View.Team.Rev != 2 {
		t.Fatalf("anna save: %+v", res)
	}
	call[Snapshot](t, bela, "teamSyncNow", nil)
	var seen bool
	for _, v := range call[Snapshot](t, bela, "listItems", nil).Items {
		if v.Item.ID == exportID {
			seen = v.Item.GraceMinutes == 45 && v.Team.Recent != nil && v.Team.Recent.By == "anna" && v.Team.Lock == nil
		}
	}
	if !seen {
		t.Fatal("bela should see anna's change and the released lock")
	}

	// --- Conflict: Béla still has revision 1 in his editor.
	if lb2 := call[lockRes](t, bela, "lockItem", exportID); !lb2.OK {
		t.Fatalf("bela lock: %+v", lb2)
	}
	stale := lb.Item // rev 1
	stale.Name = "Béla neve"
	cres := call[SaveResult](t, bela, "saveItem", stale)
	if cres.Conflict == nil || cres.Conflict.ModifiedBy != "anna" || cres.Conflict.CurrentRev != 2 {
		t.Fatalf("expected conflict: %+v", cres)
	}
	fres := call[SaveResult](t, bela, "saveItemForce", map[string]any{"item": stale, "force": true})
	if fres.Conflict != nil || fres.View.Item.Name != "Béla neve" {
		t.Fatalf("force save: %+v", fres)
	}

	// --- Personal notification choice does not leak to the other user.
	if err := rpcErr(anna, "setMute", map[string]any{"id": exportID, "mute": true}); err != nil {
		t.Fatal(err)
	}
	call[Snapshot](t, bela, "teamSyncNow", nil)
	for _, v := range call[Snapshot](t, bela, "listItems", nil).Items {
		if v.Item.ID == exportID && (!v.Item.Notify || v.Team.Muted) {
			t.Fatal("anna's mute must not affect bela")
		}
	}
	for _, v := range call[Snapshot](t, anna, "listItems", nil).Items {
		if v.Item.ID == exportID && (v.Item.Notify || !v.Team.Muted) {
			t.Fatal("anna's own mute missing")
		}
	}

	// --- Trash: Béla deletes, Anna restores.
	if err := rpcErr(bela, "deleteItem", map[string]string{"id": exportID}); err != nil {
		t.Fatal(err)
	}
	call[Snapshot](t, anna, "teamSyncNow", nil)
	type trashRow struct {
		ID      string          `json:"id"`
		Deleted teamstore.Stamp `json:"deleted"`
	}
	tr := call[[]trashRow](t, anna, "trashList", nil)
	if len(tr) != 1 || tr[0].Deleted.Name() != "bela" {
		t.Fatalf("trash: %+v", tr)
	}
	if n := len(call[Snapshot](t, anna, "listItems", nil).Items); n != 1 {
		t.Fatalf("deleted item still listed: %d", n)
	}
	if err := rpcErr(anna, "restoreItem", exportID); err != nil {
		t.Fatal(err)
	}
	type changeView struct {
		Action string          `json:"action"`
		Stamp  teamstore.Stamp `json:"stamp"`
		Fields []string        `json:"fields"`
	}
	ch := call[[]changeView](t, anna, "itemChanges", exportID)
	if len(ch) < 5 || ch[0].Action != "restored" || ch[1].Action != "deleted" || ch[1].Stamp.Name() != "bela" {
		t.Fatalf("change log: %+v", ch)
	}

	// --- Shared calendar.
	if err := rpcErr(anna, "saveCalendar", calendar.Overrides{RestDays: []string{"2026-10-09"}}); err != nil {
		t.Fatal(err)
	}
	call[Snapshot](t, bela, "teamSyncNow", nil)
	if bela.Calendar().IsWorkday(mustDate("2026-10-09")) {
		t.Fatal("bela should use the shared calendar")
	}

	// --- Offline: the folder disappears.
	hidden := shared + "-x"
	os.Rename(shared, hidden)
	off := call[Snapshot](t, bela, "teamSyncNow", nil)
	if off.Team.Status.Online || len(off.Items) != 2 {
		t.Fatalf("offline snapshot: online=%v items=%d", off.Team.Status.Online, len(off.Items))
	}
	if err := rpcErr(bela, "saveItem", localItemDef("Offline új", pathB)); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("editing must be refused offline: %v", err)
	}
	lo := call[lockRes](t, bela, "lockItem", exportID)
	if lo.OK {
		t.Fatal("no lock offline")
	}
	os.Rename(hidden, shared)
	if back := call[Snapshot](t, bela, "teamSyncNow", nil); !back.Team.Status.Online {
		t.Fatal("should be online again")
	}

	// --- Leaving shared mode brings back the personal list.
	if err := rpcErr(bela, "teamDisable", nil); err != nil {
		t.Fatal(err)
	}
	if n := call[Snapshot](t, bela, "listItems", nil).Items; len(n) != 1 || n[0].Item.Name != "Béla exportja" {
		t.Fatalf("personal list after disable: %+v", n)
	}
}

func localItemDef(name, path string) model.Item {
	it := model.NewItem()
	it.ID = ""
	it.Name = name
	it.Path = path
	return it
}

func TestWindowCloseReleasesLocks(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "kozos")
	os.MkdirAll(shared, 0o755)
	a, dir := newUserApp(t, "anna")
	call[enableRes](t, a, "teamEnable", map[string]string{"folder": shared})
	it := call[SaveResult](t, a, "saveItem", localItemDef("X", filepath.Join(dir, "x.csv"))).View.Item
	if err := rpcErr(a, "lockItem", it.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(shared, "locks", it.ID+".lock")); err != nil {
		t.Fatal("lock file expected")
	}
	a.UIClosed()
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(filepath.Join(shared, "locks", it.ID+".lock")); os.IsNotExist(err) {
			return
		}
		sleepMs(20)
	}
	t.Fatal("lock not released on window close")
}
