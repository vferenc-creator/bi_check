package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bimonitor/internal/knime"
	"bimonitor/internal/schedule"
)

// The KNIME menu keeps its own files in the machine-local folder, stores
// credential passwords apart (encrypted) and leaves the monitoring alone.
func TestKnimeAPI(t *testing.T) {
	a, _, dir := newTestApp(t)
	v := call[knimeView](t, a, "knimeState", nil)
	if v.Config.Exe != "" || len(v.Jobs) != 0 || !strings.HasPrefix(v.Dir, filepath.Join(dir, "local")) {
		t.Fatalf("%+v", v)
	}
	if _, err := a.Call("knimeSetConfig", mustJSON(knime.Config{Exe: filepath.Join(dir, "nincs", "knime.exe")})); err == nil {
		t.Fatal("a missing KNIME must be refused")
	}
	kd := filepath.Join(dir, "KNIME")
	os.MkdirAll(filepath.Join(kd, "plugins", "org.knime.core_5.12.0.v202604161011"), 0o755)
	os.WriteFile(filepath.Join(kd, knime.ExeName()), []byte("x"), 0o755)
	v = call[knimeView](t, a, "knimeSetConfig", knime.Config{Exe: kd, Parallel: 9})
	if v.Install.Version != "5.12.0" || v.Install.NodePit != "" || v.Config.Parallel != 4 || v.ZipURL != "https://download.nodepit.com/batch/5.12.zip" {
		t.Fatalf("%+v", v)
	}
	if _, err := a.Call("knimeRunNow", mustJSON(idParam{ID: "x"})); err == nil {
		t.Fatal("unknown job")
	}

	job := knime.Job{Name: "Napi export", Workflow: `C:\wf`, Scheduled: true, Enabled: true, Reset: true,
		Schedule:    schedule.Spec{Type: schedule.Workdays, Times: []string{"06:00"}, UseHolidays: true},
		Credentials: []knime.Credential{{Name: "bi_db", Login: "d.admin"}}}
	saved := call[knime.Job](t, a, "knimeSaveJob", knimeJobSave{Job: job, Passwords: map[string]string{"bi_db": "Titok123"}})
	if saved.ID == "" || !saved.Credentials[0].HasPassword {
		t.Fatalf("%+v", saved)
	}
	st, _, _ := a.knimeInit()
	pwFile := st.SecretPath(saved.ID, "bi_db")
	if b, err := os.ReadFile(pwFile); err != nil || strings.Contains(string(b), "Titok123") {
		t.Fatalf("password file: %v", err)
	}
	// saved again without a new password: the stored one stays
	saved.Name = "Napi export 2"
	saved = call[knime.Job](t, a, "knimeSaveJob", knimeJobSave{Job: saved})
	if !saved.Credentials[0].HasPassword {
		t.Fatal("password must be kept")
	}
	b, _ := os.ReadFile(filepath.Join(st.Dir(), "jobs.json"))
	if strings.Contains(string(b), "Titok123") {
		t.Fatal("no password in jobs.json")
	}
	v = call[knimeView](t, a, "knimeState", nil)
	if len(v.Jobs) != 1 || v.Jobs[0].Next == nil || !strings.Contains(v.Jobs[0].Text, "Munkanapokon 06:00") {
		t.Fatalf("%+v", v.Jobs)
	}
	// installing needs the zip (or a download); a non-NodePit zip is refused
	bad := filepath.Join(dir, "valami.zip")
	os.WriteFile(bad, []byte("nem zip"), 0o644)
	if r := call[knimeInstallResult](t, a, "knimeInstall", knimeInstallParam{Zip: bad}); r.OK || r.Message == "" {
		t.Fatalf("%+v", r)
	}
	// the monitoring settings are untouched
	if s, _ := os.ReadFile(filepath.Join(dir, "settings.json")); strings.Contains(string(s), "knime") || strings.Contains(string(s), "Napi export") {
		t.Fatal("the KNIME data must not be in settings.json")
	}
	call[any](t, a, "knimeDeleteJob", idParam{ID: saved.ID})
	if _, err := os.Stat(pwFile); !os.IsNotExist(err) {
		t.Fatal("the password must be removed with the job")
	}
}
