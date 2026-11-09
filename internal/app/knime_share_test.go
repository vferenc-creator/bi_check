package app

import (
	"os"
	"path/filepath"
	"strings"
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
	// the server stops reporting → once; reporting again → once
	srv.Updated = now.Add(-30 * time.Minute)
	write()
	a.knimeShareStep()
	a.knimeShareStep()
	if n := p.notes(); len(n) != 2 || !strings.Contains(n[1], "nem jelentkezik") {
		t.Fatalf("%v", n)
	}
	v = call[knimeView](t, a, "knimeState", nil)
	if !v.Remote[0].Stale {
		t.Fatal("stale in the view")
	}
	srv.Updated = time.Now()
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
	// shared mode off: nothing shown
	a.Settings.Update(func(s *model.Settings) error { s.Team.Enabled = false; return nil })
	a.knimeShareStep()
	if v := call[knimeView](t, a, "knimeState", nil); len(v.Remote) != 0 || v.Share.Team {
		t.Fatalf("%+v", v.Share)
	}
}
