package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bimonitor/internal/knime"
	"bimonitor/internal/model"
	"bimonitor/internal/schedule"
)

// The KNIME state in the shared folder: only its own sub-folder is used,
// no secrets are written, the others' final failures and a computer that
// stopped reporting are notified once.
func TestKnimeShare(t *testing.T) {
	a, p, dir := newTestApp(t)
	shared := filepath.Join(dir, "kozos")
	os.MkdirAll(shared, 0o755)
	os.WriteFile(filepath.Join(shared, "bicheck.json"), []byte(`{"format":"x"}`), 0o644) // the existing shared mode files
	if _, err := a.Settings.Update(func(s *model.Settings) error { s.Team.Folder, s.Team.Enabled = shared, true; return nil }); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(shared, knime.ShareFolder, a.knimeShareFile())

	// no jobs here: nothing is published
	a.knimeShareStep()
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Fatal("nothing to share without jobs")
	}
	job := knime.Job{Name: "Napi export", Workflow: `C:\wf\Napi`, Scheduled: true, Enabled: true,
		Schedule:    schedule.Spec{Type: schedule.Daily, Times: []string{"06:00"}},
		Variables:   []knime.Variable{{Name: "datum", Value: "TITKOSVALTOZO", Type: "String"}},
		Credentials: []knime.Credential{{Name: "bi_db", Login: "d.admin"}}}
	call[knime.Job](t, a, "knimeSaveJob", knimeJobSave{Job: job, Passwords: map[string]string{"bi_db": "Titok123"}})
	a.knimeShareStep()
	b, err := os.ReadFile(own)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"Titok123", "TITKOSVALTOZO", "d.admin", "bi_db"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("%q must not be shared:\n%s", secret, b)
		}
	}
	if !strings.Contains(string(b), "Napi export") || !strings.Contains(string(b), "Naponta 06:00") {
		t.Fatalf("%s", b)
	}
	// only our own sub-folder was added to the shared folder
	ents, _ := os.ReadDir(shared)
	if len(ents) != 2 || ents[0].Name() != "bicheck.json" || ents[1].Name() != knime.ShareFolder {
		t.Fatalf("shared folder touched: %v", ents)
	}

	// the server's file: the history found at the first look is not notified
	now := time.Now()
	srv := knime.Shared{Version: 1, Host: "EF-BI", User: "bi", Updated: now, IntervalSec: 60,
		Jobs: []knime.SharedJob{{ID: "j", Name: "Esti töltés"}},
		Runs: []knime.SharedRun{{ID: "r1", JobName: "Esti töltés", Status: knime.StatusError, Message: "régi hiba"}}}
	write := func() {
		t.Helper()
		if err := knime.WriteShared(shared, "EF-BI__bi.json", srv); err != nil {
			t.Fatal(err)
		}
	}
	write()
	a.knimeShareStep()
	if n := p.notes(); len(n) != 0 {
		t.Fatalf("no notification for the history: %v", n)
	}
	v := call[knimeView](t, a, "knimeState", nil)
	if len(v.Remote) != 1 || v.Remote[0].Host != "EF-BI" || v.Remote[0].Stale || !v.Share.Sharing || !strings.HasSuffix(v.Share.Folder, knime.ShareFolder) {
		t.Fatalf("%+v %+v", v.Remote, v.Share)
	}
	// a new final failure → one notification; a failure with a retry → none yet
	srv.Runs = append([]knime.SharedRun{
		{ID: "r3", JobName: "Esti töltés", Status: knime.StatusError, Message: "újrapróbálja", NextRetry: now.Add(time.Minute)},
		{ID: "r2", JobName: "Esti töltés", Status: knime.StatusError, Message: "Login failed\nrészletek",
			FailedNode: &knime.FailedNode{Name: "DB Reader", ID: "3:12", Message: "Execute failed: Login failed"}}}, srv.Runs...)
	write()
	a.knimeShareStep()
	a.knimeShareStep()
	if n := p.notes(); len(n) != 1 || !strings.Contains(n[0], "KNIME (EF-BI): Esti töltés") || !strings.Contains(n[0], "Hibás csomópont: DB Reader (#3:12) – Execute failed: Login failed") || strings.Contains(n[0], "részletek") {
		t.Fatalf("%v", n)
	}
	// the server stops reporting (its file does not change for 30 minutes
	// by our clock) → once; reporting again → once
	var clk struct {
		sync.Mutex
		off time.Duration
	}
	setShareClock(a, func() time.Time { clk.Lock(); defer clk.Unlock(); return time.Now().Add(clk.off) })
	clk.Lock()
	clk.off = 30 * time.Minute
	clk.Unlock()
	a.knimeShareStep()
	a.knimeShareStep()
	if n := p.notes(); len(n) != 2 || !strings.Contains(n[1], "nem jelentkezik") {
		t.Fatalf("%v", n)
	}
	v = call[knimeView](t, a, "knimeState", nil)
	if !v.Remote[0].Stale {
		t.Fatal("stale in the view")
	}
	srv.Updated = time.Now() // its clock is 30 minutes behind ours: alive all the same
	write()
	a.knimeShareStep()
	if n := p.notes(); len(n) != 3 || !strings.Contains(n[2], "ismét jelentkezik") {
		t.Fatalf("%v", n)
	}
	// muted: logged only
	st, _, _ := a.knimeInit()
	cfg := st.Config()
	cfg.MuteRemote = true
	st.SetConfig(cfg)
	srv.Runs = append([]knime.SharedRun{{ID: "r4", JobName: "Esti töltés", Status: knime.StatusTimeout}}, srv.Runs...)
	write()
	a.knimeShareStep()
	if n := p.notes(); len(n) != 3 {
		t.Fatalf("muted: %v", n)
	}
	// sharing switched off: our file goes, the others need not wait for us
	cfg.NoShare = true
	st.SetConfig(cfg)
	a.knimeShareStep()
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Fatal("our file must be removed")
	}
	if v := call[knimeView](t, a, "knimeState", nil); v.Remote[0].Stale {
		t.Fatal("a writer whose clock is behind is alive while its file changes")
	}
	// shared mode off: nothing shown, and our file does not stay behind
	cfg.NoShare = false
	st.SetConfig(cfg)
	a.knimeShareStep()
	if _, err := os.Stat(own); err != nil {
		t.Fatal("shared again")
	}
	a.Settings.Update(func(s *model.Settings) error { s.Team.Enabled = false; return nil })
	a.knimeShareStep()
	if v := call[knimeView](t, a, "knimeState", nil); len(v.Remote) != 0 || v.Share.Team {
		t.Fatalf("%+v", v.Share)
	}
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Fatal("shared mode off: our file must be removed")
	}
}

// A file left behind (silent already at the first look) is shown as silent
// but not notified at every start; a reader whose clock is ahead does not
// see a live server as silent; a moved shared folder keeps no file of ours.
func TestKnimeShareClocksAndLeftovers(t *testing.T) {
	a, p, dir := newTestApp(t)
	// our clock is 11 minutes ahead (set before the program's own share
	// loop can look at the folder)
	setShareClock(a, func() time.Time { return time.Now().Add(11 * time.Minute) })
	shared := filepath.Join(dir, "kozos")
	os.MkdirAll(shared, 0o755)
	if _, err := a.Settings.Update(func(s *model.Settings) error { s.Team.Folder, s.Team.Enabled = shared, true; return nil }); err != nil {
		t.Fatal(err)
	}
	old := knime.Shared{Version: 1, Host: "REGI-PC", User: "x", Updated: time.Now().Add(-40 * 24 * time.Hour), IntervalSec: 60}
	srv := knime.Shared{Version: 1, Host: "EF-BI", User: "bi", Updated: time.Now(), IntervalSec: 60}
	knime.WriteShared(shared, "REGI-PC__x.json", old)
	knime.WriteShared(shared, "EF-BI__bi.json", srv)
	a.knimeShareStep()
	if n := p.notes(); len(n) != 0 {
		t.Fatalf("no notification at the first look: %v", n)
	}
	v := call[knimeView](t, a, "knimeState", nil)
	if len(v.Remote) != 2 || v.Remote[0].Host != "EF-BI" || v.Remote[1].Host != "REGI-PC" || !v.Remote[1].Stale {
		t.Fatalf("%+v", v.Remote)
	}
	// the server writes again: alive by our clock, whatever its clock says
	srv.Updated = time.Now().Add(time.Second)
	knime.WriteShared(shared, "EF-BI__bi.json", srv)
	a.knimeShareStep()
	a.knimeShareStep()
	if v := call[knimeView](t, a, "knimeState", nil); v.Remote[0].Stale {
		t.Fatal("clock difference counted as silence")
	}
	if n := p.notes(); len(n) != 0 {
		t.Fatalf("%v", n)
	}

	// we share, then the shared folder is changed: our file leaves the old one
	call[knime.Job](t, a, "knimeSaveJob", knimeJobSave{Job: knime.Job{Name: "x", Workflow: `C:\wf`}})
	a.knimeShareStep()
	own := filepath.Join(shared, knime.ShareFolder, a.knimeShareFile())
	if _, err := os.Stat(own); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "uj-kozos")
	os.MkdirAll(other, 0o755)
	a.Settings.Update(func(s *model.Settings) error { s.Team.Folder = other; return nil })
	a.knimeShareStep()
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Fatal("our file stayed in the old folder")
	}
	if _, err := os.Stat(filepath.Join(other, knime.ShareFolder, a.knimeShareFile())); err != nil {
		t.Fatal("not shared in the new folder")
	}

	// the shared folder disappears: it is not created again
	gone := filepath.Join(dir, "atnevezett")
	a.Settings.Update(func(s *model.Settings) error { s.Team.Folder = gone; return nil })
	a.knimeShareStep()
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatal("the missing shared folder was created")
	}
	if v := call[knimeView](t, a, "knimeState", nil); !strings.Contains(v.Share.Error, "nem írható") {
		t.Fatalf("error not shown: %+v", v.Share)
	}
}

// A hung shared folder: no new operation piles up behind the waiting one.
func TestKnimeShareHung(t *testing.T) {
	var k knimeShareState
	release := make(chan struct{})
	if err := k.timeout(50*time.Millisecond, func() error { <-release; return nil }); err != errShareTimeout {
		t.Fatal(err)
	}
	if k.pending.Load() != 1 {
		t.Fatal("the waiting operation is not counted")
	}
	close(release)
	for i := 0; i < 50 && k.pending.Load() != 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if k.pending.Load() != 0 {
		t.Fatal("not released")
	}
}

// setShareClock: the program's own share loop runs meanwhile.
func setShareClock(a *App, now func() time.Time) {
	k := &a.kn.share
	k.step.Lock()
	k.mu.Lock()
	k.now = now
	k.mu.Unlock()
	k.step.Unlock()
}
