package knime

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bimonitor/internal/schedule"
)

// TestHelperProcess plays KNIME + NodePit Batch for the runner tests: the
// workflow folder name decides what happens ("ok", "fail30", "slow", "locked").
func TestHelperProcess(t *testing.T) {
	if os.Getenv("BIM_KNIME_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	data := ""
	for i, a := range args {
		if a == "-data" && i+1 < len(args) {
			data = args[i+1]
		}
	}
	wf := filepath.Base(args[len(args)-1])
	fmt.Println("ARGS:", strings.Join(args, " "))
	logDir := filepath.Join(data, ".metadata", "knime")
	_ = os.MkdirAll(logDir, 0o755)
	logLine := func(s string) {
		f, _ := os.OpenFile(filepath.Join(logDir, "knime.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		fmt.Fprintln(f, s)
		f.Close()
	}
	switch wf {
	case "ok":
		logLine("2026-11-06 08:00:00,000 : INFO : main : Arguments: " + strings.Join(args, " "))
		logLine("2026-11-06 08:00:01,000 : INFO : main : Workflow executed sucessfully")
		os.Exit(0)
	case "fail30":
		logLine("2026-11-06 08:00:01,000 : ERROR : KNIME-Worker-1 : DB Reader : 3:12 : Execute failed: Login failed for user 'd.admin'")
		fmt.Println("========= Workflow did not execute sucessfully ============")
		os.Exit(30)
	case "locked":
		fmt.Println("Workflow is locked by another KNIME instance")
		os.Exit(20)
	case "slow":
		time.Sleep(20 * time.Second)
		os.Exit(0)
	}
	os.Exit(50)
}

func helperCommand(ctx context.Context, exe string, args []string) *exec.Cmd {
	cmd := defaultCommand(ctx, os.Args[0], append([]string{"-test.run=TestHelperProcess", "--"}, args...))
	cmd.Env = append(os.Environ(), "BIM_KNIME_HELPER=1")
	return cmd
}

func fakeKnime(t *testing.T, nodepit bool) string {
	t.Helper()
	dir := t.TempDir()
	plugins := filepath.Join(dir, "plugins")
	must(t, os.MkdirAll(filepath.Join(plugins, "org.knime.core_5.12.0.v202604161011"), 0o755))
	must(t, os.WriteFile(filepath.Join(plugins, "org.knime.core_5.11.2.v202601010000.jar"), nil, 0o644))
	if nodepit {
		must(t, os.WriteFile(filepath.Join(plugins, "com.nodepit.batch.application_1.1.0.202604181052.jar"), nil, 0o644))
	}
	exe := filepath.Join(dir, ExeName())
	must(t, os.WriteFile(exe, []byte("x"), 0o755))
	return exe
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func workflow(t *testing.T, root, name string) string {
	t.Helper()
	d := filepath.Join(root, name)
	must(t, os.MkdirAll(d, 0o755))
	must(t, os.WriteFile(filepath.Join(d, "workflow.knime"), []byte("<config/>"), 0o644))
	return d
}

func TestInspect(t *testing.T) {
	exe := fakeKnime(t, true)
	in := Inspect(exe)
	if in.Error != "" || in.Version != "5.12.0" || in.Minor() != "5.12" || in.NodePit != "1.1.0" || !in.Writable {
		t.Fatalf("%+v", in)
	}
	if in2 := Inspect(filepath.Dir(exe)); in2.Exe != exe || in2.Version != "5.12.0" {
		t.Fatalf("folder: %+v", in2)
	}
	if in3 := Inspect(fakeKnime(t, false)); in3.NodePit != "" {
		t.Fatalf("no nodepit: %+v", in3)
	}
	if Inspect(t.TempDir()).Error == "" || Inspect("").Error == "" {
		t.Fatal("not KNIME")
	}
	if ZipURL("5.12") != "https://download.nodepit.com/batch/5.12.zip" {
		t.Fatal(ZipURL("5.12"))
	}
}

func writeZip(t *testing.T, path string, files ...string) {
	t.Helper()
	f, err := os.Create(path)
	must(t, err)
	w := zip.NewWriter(f)
	for _, n := range files {
		e, _ := w.Create(n)
		e.Write([]byte("x"))
	}
	must(t, w.Close())
	must(t, f.Close())
}

func TestInspectZip(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "5.12.zip")
	writeZip(t, good, "content.jar", "artifacts.jar", "features/com.nodepit.batch.feature_1.1.0.202604181052.jar", "plugins/com.nodepit.batch.application_1.1.0.202604181052.jar")
	zi, err := InspectZip(good)
	if err != nil || zi.Version != "1.1.0" || zi.ForKNIME != "5.12" {
		t.Fatalf("%+v %v", zi, err)
	}
	other := filepath.Join(dir, "valami.zip")
	writeZip(t, other, "content.jar", "artifacts.jar", "features/org.other_1.0.jar")
	if _, err := InspectZip(other); err == nil {
		t.Fatal("not NodePit")
	}
	if _, err := InspectZip(filepath.Join(dir, "nincs.zip")); err == nil {
		t.Fatal("missing file")
	}
	if u := RepoURL(good); !strings.HasPrefix(u, "jar:file:/") || !strings.HasSuffix(u, "5.12.zip!/") || strings.Contains(u, `\`) {
		t.Fatal(u)
	}
	a := DirectorArgs("REPO", false)
	if strings.Join(a, " ") != "-nosplash --launcher.suppressErrors -consoleLog -application org.eclipse.equinox.p2.director -installIU com.nodepit.batch.feature.feature.group -repository REPO" {
		t.Fatal(a)
	}
}

func TestBuildArgs(t *testing.T) {
	j := Job{Reset: true, Variables: []Variable{{"datum", "2026-11-06", "String"}, {"n", "5", "int"}},
		Credentials: []Credential{{Name: "bi", Login: "d.admin", HasPassword: true}}, ExtraArgs: `--preferences "C:\a b\p.epf"`}
	got := strings.Join(BuildArgs(j, "WS", "WF", map[string]string{"bi": "titok"}, false), " | ")
	want := "-nosplash | --launcher.suppressErrors | -application | " + AppID + " | -data | WS | --reset | --no-save | --variable=datum;2026-11-06;String | --variable=n;5;int | --credential=bi;d.admin;titok | --preferences | C:\\a b\\p.epf | WF"
	if got != want {
		t.Fatalf("\n%s\n%s", got, want)
	}
}

func TestValidate(t *testing.T) {
	j := Job{Name: " A ", Workflow: ` "C:\wf" `, Variables: []Variable{{Name: "x", Value: "1,5", Type: "double"}, {Name: ""}},
		Credentials: []Credential{{Name: "bi", Login: "a"}}}
	if err := Validate(&j); err != nil || j.Name != "A" || j.Workflow != `C:\wf` || len(j.Variables) != 1 || j.Variables[0].Value != "1.5" {
		t.Fatalf("%+v %v", j, err)
	}
	for _, bad := range []Job{
		{Name: "", Workflow: "x"},
		{Name: "a", Workflow: ""},
		{Name: "a", Workflow: "x", Variables: []Variable{{Name: "n", Value: "abc", Type: "int"}}},
		{Name: "a", Workflow: "x", Variables: []Variable{{Name: "n", Value: "1", Type: "date"}}},
		{Name: "a", Workflow: "x", Credentials: []Credential{{Name: "a;b"}}},
		{Name: "a", Workflow: "x", Scheduled: true, Schedule: schedule.Spec{Type: schedule.Daily}},
	} {
		b := bad
		if Validate(&b) == nil {
			t.Fatalf("expected error: %+v", bad)
		}
	}
}

type harness struct {
	st     *Store
	r      *Runner
	root   string
	mu     sync.Mutex
	done   []Run
	doneCh chan Run
}

// clock is a settable time for the scheduler tests.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

func newHarness(t *testing.T, setup ...func(*Runner)) *harness {
	t.Helper()
	root := t.TempDir()
	st, err := OpenStore(filepath.Join(root, "knime"))
	must(t, err)
	_, err = st.SetConfig(Config{Exe: fakeKnime(t, true), Parallel: 2})
	must(t, err)
	h := &harness{st: st, root: root, doneCh: make(chan Run, 20)}
	h.r = &Runner{Store: st, Command: helperCommand, unit: 100 * time.Millisecond,
		Password: func(jobID, cred string) (string, error) { return "Titok123", nil },
		OnFinish: func(r Run, j Job) {
			h.mu.Lock()
			h.done = append(h.done, r)
			h.mu.Unlock()
			h.doneCh <- r
		}}
	for _, f := range setup {
		f(h.r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.r.Start(ctx)
	return h
}

func (h *harness) job(t *testing.T, name string, mod func(*Job)) Job {
	t.Helper()
	j := Job{Name: name, Workflow: workflow(t, h.root, name), Reset: true}
	if mod != nil {
		mod(&j)
	}
	j, err := h.st.SaveJob(j, time.Now())
	must(t, err)
	return j
}

func (h *harness) wait(t *testing.T) Run {
	t.Helper()
	select {
	case r := <-h.doneCh:
		return r
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for a run")
	}
	return Run{}
}

func TestRunnerOutcomes(t *testing.T) {
	h := newHarness(t)
	ok := h.job(t, "ok", func(j *Job) {
		j.Variables = []Variable{{"datum", "2026-11-06", "String"}}
		j.Credentials = []Credential{{Name: "bi", Login: "d.admin", HasPassword: true}}
	})
	if _, err := h.r.Enqueue(ok, "kézi", time.Time{}); err != nil {
		t.Fatal(err)
	}
	r := h.wait(t)
	if r.Status != StatusOK || r.ExitCode == nil || *r.ExitCode != 0 || r.DurationMs < 0 || r.Log == "" {
		t.Fatalf("%+v", r)
	}
	logText, _ := os.ReadFile(h.st.LogPath(r.Log))
	if !strings.Contains(string(logText), "--variable=datum;2026-11-06;String") || strings.Contains(string(logText), "Titok123") ||
		!strings.Contains(string(logText), "--credential=bi;d.admin;••••") || !strings.Contains(string(logText), "knime.log") {
		t.Fatalf("log:\n%s", logText)
	}
	if _, err := os.Stat(filepath.Join(h.st.Dir(), "munka", r.ID)); !os.IsNotExist(err) {
		t.Fatal("the temporary workspace must be removed")
	}

	fail := h.job(t, "fail30", nil)
	h.r.Enqueue(fail, "kézi", time.Time{})
	r = h.wait(t)
	if r.Status != StatusError || *r.ExitCode != 30 || !strings.Contains(r.Message, "30-as kód") || !strings.Contains(r.Message, "Login failed") {
		t.Fatalf("%+v", r)
	}

	locked := h.job(t, "locked", nil)
	h.r.Enqueue(locked, "kézi", time.Time{})
	r = h.wait(t)
	if r.Status != StatusError || !strings.Contains(r.Message, "Futtatás másolatból") {
		t.Fatalf("%+v", r)
	}
	// a copy runs even while the original is open in KNIME (the helper
	// still sees the folder name "locked" → same exit, but from the copy)
	cp := h.job(t, "ok-copy", func(j *Job) { j.Copy = true; j.Workflow = workflow(t, h.root, "ok") })
	h.r.Enqueue(cp, "kézi", time.Time{})
	r = h.wait(t)
	logText, _ = os.ReadFile(h.st.LogPath(r.Log))
	if r.Status != StatusOK || !strings.Contains(string(logText), filepath.Join("munka", r.ID, "workflow", "ok")) {
		t.Fatalf("%+v\n%s", r, logText)
	}

	// timeout (1 unit = 100 ms here) and cancel
	slow := h.job(t, "slow", func(j *Job) { j.TimeoutMin = 3 })
	h.r.Enqueue(slow, "kézi", time.Time{})
	r = h.wait(t)
	if r.Status != StatusTimeout {
		t.Fatalf("%+v", r)
	}
	slow2 := h.job(t, "slow2", func(j *Job) { j.Workflow = workflow(t, h.root, "slow") })
	run, _ := h.r.Enqueue(slow2, "kézi", time.Time{})
	if _, err := h.r.Enqueue(slow2, "kézi", time.Time{}); err == nil {
		t.Fatal("the same job must not be queued twice")
	}
	time.Sleep(300 * time.Millisecond)
	if !h.r.Cancel(run.ID) {
		t.Fatal("cancel")
	}
	r = h.wait(t)
	if r.Status != StatusCanceled {
		t.Fatalf("%+v", r)
	}
	// missing workflow
	gone := h.job(t, "gone", func(j *Job) { j.Workflow = filepath.Join(h.root, "nincs") })
	h.r.Enqueue(gone, "kézi", time.Time{})
	if r = h.wait(t); r.Status != StatusError || !strings.Contains(r.Message, "nem található") {
		t.Fatalf("%+v", r)
	}
	if runs := h.st.Runs("", 0); len(runs) != 7 || runs[0].JobName != "gone" {
		t.Fatalf("history: %d", len(runs))
	}
}

func TestSchedulerTick(t *testing.T) {
	loc := time.UTC
	clk := &clock{t: time.Date(2026, 11, 6, 7, 0, 0, 0, loc)} // Friday
	h := newHarness(t, func(r *Runner) { r.Now, r.Location = clk.Now, loc })
	j := h.job(t, "ok", func(j *Job) {
		j.Scheduled, j.Enabled = true, true
		j.Schedule = schedule.Spec{Type: schedule.Daily, Times: []string{"07:50", "10:35"}}
	})
	h.r.Tick() // first tick: remembers yesterday's 10:35, runs nothing
	if len(h.st.Runs(j.ID, 0)) != 0 || h.st.LastDue(j.ID).IsZero() {
		t.Fatal("first tick must not run")
	}
	if n := h.r.Next(j); !n.Equal(time.Date(2026, 11, 6, 7, 50, 0, 0, loc)) {
		t.Fatalf("next %v", n)
	}
	clk.Set(time.Date(2026, 11, 6, 7, 50, 30, 0, loc))
	h.r.Tick()
	r := h.wait(t)
	if r.Trigger != "ütemezett" || !r.Due.Equal(time.Date(2026, 11, 6, 7, 50, 0, 0, loc)) || r.Status != StatusOK {
		t.Fatalf("%+v", r)
	}
	h.r.Tick() // same instant: nothing new
	// the program was off at 10:35: missed, not run late
	clk.Set(time.Date(2026, 11, 6, 11, 0, 0, 0, loc))
	h.r.Tick()
	r = h.wait(t)
	if r.Status != StatusMissed || !strings.Contains(r.Message, "10:35") {
		t.Fatalf("%+v", r)
	}
	// paused job
	j.Enabled = false
	h.st.SaveJob(j, clk.Now())
	clk.Set(time.Date(2026, 11, 7, 7, 51, 0, 0, loc))
	h.r.Tick()
	if runs := h.st.Runs(j.ID, 0); len(runs) != 2 {
		t.Fatalf("paused job ran: %d", len(runs))
	}
}

func TestStorePersistsAndTrims(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStore(dir)
	must(t, err)
	j, err := st.SaveJob(Job{Name: "A", Workflow: "x"}, time.Now())
	must(t, err)
	must(t, st.PutRun(Run{ID: "r1", JobID: j.ID, Status: StatusRunning}))
	st2, err := OpenStore(dir)
	must(t, err)
	if len(st2.Jobs()) != 1 {
		t.Fatal("jobs not kept")
	}
	if r, _ := st2.Run("r1"); r.Status != StatusCanceled {
		t.Fatalf("a run going at exit becomes canceled: %+v", r)
	}
	_, err = st2.SetConfig(Config{KeepRuns: 50})
	must(t, err)
	for i := 0; i < 60; i++ {
		must(t, st2.PutRun(Run{ID: fmt.Sprint("x", i), JobID: j.ID, Status: StatusOK}))
	}
	if n := len(st2.Runs("", 0)); n != 50 {
		t.Fatalf("trim: %d", n)
	}
	must(t, st2.DeleteJob(j.ID))
	if len(st2.Jobs()) != 0 {
		t.Fatal("delete")
	}
}

// KNIME runs in its own folder: a relative data folder must still give the
// run an absolute workspace (and so its knime.log).
func TestRelativeStoreDir(t *testing.T) {
	wd, _ := os.Getwd()
	tmp := t.TempDir()
	must(t, os.Chdir(tmp))
	defer os.Chdir(wd)
	st, err := OpenStore("rel")
	must(t, err)
	if !filepath.IsAbs(st.Dir()) {
		t.Fatal(st.Dir())
	}
}
