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
		logLine("2026-11-06 08:00:01,000 : ERROR : KNIME-Worker-1-DB Reader 3:12 :  : Node : DB Reader : 3:12 : Execute failed: Login failed for user 'd.admin'")
		fmt.Println("========= Workflow did not execute sucessfully ============")
		os.Exit(30)
	case "locked":
		fmt.Println("Workflow is locked by another KNIME instance")
		os.Exit(20)
	case "slow":
		time.Sleep(20 * time.Second)
		os.Exit(0)
	case "badparam":
		os.Exit(10)
	case "flaky": // fails the first time, then succeeds
		mark := filepath.Join(args[len(args)-1], "tried")
		if _, err := os.Stat(mark); err != nil {
			os.WriteFile(mark, nil, 0o644)
			os.Exit(30)
		}
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
	if strings.Join(a, " ") != "-nosplash --launcher.suppressErrors -consoleLog -data @none -application org.eclipse.equinox.p2.director -installIU com.nodepit.batch.feature.feature.group -repository REPO" {
		t.Fatal(a)
	}
}

func TestBuildArgs(t *testing.T) {
	j := Job{Reset: true, Variables: []Variable{{"datum", "2026-11-06", "String"}, {"n", "5", "int"}},
		Credentials: []Credential{{Name: "bi", Login: "d.admin", HasPassword: true}}, ExtraArgs: `--preferences "C:\a b\p.epf"`}
	got := strings.Join(BuildArgs(j, "WS", "WF", map[string]string{"bi": "titok"}, false, "C:\\x.epf"), " | ")
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
	t.Cleanup(func() { h.r.Stop(10 * time.Second); cancel() }) // like the program exit
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
	if fn := r.FailedNode; fn == nil || fn.Name != "DB Reader" || fn.ID != "3:12" || !strings.Contains(r.Message, "Hibás csomópont: DB Reader (#3:12) – Execute failed: Login failed") {
		t.Fatalf("failed node: %+v %q", r.FailedNode, r.Message)
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
	// while it runs: its log and its KNIME workspace (live log)
	if name, data, ok := h.r.LiveLog(run.ID); !ok || name != run.ID+".log" || !strings.Contains(data, run.ID) {
		t.Fatalf("live log: %q %q %v", name, data, ok)
	}
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
	_, err := h.st.SaveJob(j, clk.Now()) // saved at 07:00
	must(t, err)
	h.r.Tick() // first tick: yesterday's 10:35 is before the save, nothing runs
	if len(h.st.Runs(j.ID, 0)) != 0 {
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

func TestRetries(t *testing.T) {
	h := newHarness(t) // 1 minute = 100 ms here
	// always failing: 1 + 2 tries, only the last one is final
	fail := h.job(t, "fail30", func(j *Job) { j.RetryCount, j.RetryWaitMin = 2, 1 })
	h.r.Enqueue(fail, "ütemezett", time.Time{})
	for i := 1; i <= 3; i++ {
		r := h.wait(t)
		if r.Status != StatusError || r.Attempt != i || r.Attempts != 3 || r.Trigger != "ütemezett" {
			t.Fatalf("try %d: %+v", i, r)
		}
		if final := i == 3; final != r.NextRetry.IsZero() || final == strings.Contains(r.Message, "Újrapróbálás") {
			t.Fatalf("try %d next retry: %v %q", i, r.NextRetry, r.Message)
		}
	}
	if _, ok := h.st.PendingRetry(fail.ID); ok {
		t.Fatal("no retry after the last try")
	}
	// fails once, then succeeds
	flaky := h.job(t, "flaky", func(j *Job) { j.RetryCount, j.RetryWaitMin = 3, 1 })
	h.r.Enqueue(flaky, "kézi", time.Time{})
	if r := h.wait(t); r.Status != StatusError || r.NextRetry.IsZero() {
		t.Fatalf("%+v", r)
	}
	if r := h.wait(t); r.Status != StatusOK || r.Attempt != 2 {
		t.Fatalf("%+v", r)
	}
	// wrong parameters (10) and runs stopped by hand are not retried
	bad := h.job(t, "badparam", func(j *Job) { j.RetryCount = 2 })
	h.r.Enqueue(bad, "kézi", time.Time{})
	if r := h.wait(t); r.Status != StatusError || !r.NextRetry.IsZero() {
		t.Fatalf("%+v", r)
	}
	slow := h.job(t, "slow", func(j *Job) { j.RetryCount = 2 })
	run, _ := h.r.Enqueue(slow, "kézi", time.Time{})
	time.Sleep(300 * time.Millisecond)
	h.r.Cancel(run.ID)
	if r := h.wait(t); r.Status != StatusCanceled || !r.NextRetry.IsZero() {
		t.Fatalf("%+v", r)
	}
	// a manual run replaces a pending retry; cancelling drops it
	h.st.SetRetry(fail.ID, Retry{At: time.Now().Add(time.Hour), Attempt: 2, Of: 3})
	if !h.r.CancelRetry(fail.ID) || h.r.CancelRetry(fail.ID) {
		t.Fatal("cancel retry")
	}
	h.st.SetRetry(fail.ID, Retry{At: time.Now().Add(time.Hour), Attempt: 2, Of: 3})
	h.r.Enqueue(fail, "kézi", time.Time{})
	if _, ok := h.st.PendingRetry(fail.ID); ok {
		t.Fatal("a new run must replace the pending retry")
	}
	h.wait(t)
	for len(h.doneCh) > 0 || func() bool { _, ok := h.st.PendingRetry(fail.ID); return ok }() {
		h.r.CancelRetry(fail.ID)
		select {
		case <-h.doneCh:
		case <-time.After(time.Second):
		}
	}
}

// A pending retry survives a restart; a stale one is recorded, not run.
func TestRetryAfterRestart(t *testing.T) {
	h := newHarness(t)
	j := h.job(t, "ok", func(j *Job) { j.RetryCount = 1 })
	must(t, h.st.SetRetry(j.ID, Retry{At: time.Now().Add(-time.Second), Attempt: 2, Of: 2, Trigger: "ütemezett"}))
	st2, err := OpenStore(h.st.Dir())
	must(t, err)
	if rt, ok := st2.PendingRetry(j.ID); !ok || rt.Attempt != 2 {
		t.Fatal("retry not kept")
	}
	h.r.Tick()
	if r := h.wait(t); r.Status != StatusOK || r.Attempt != 2 || r.Trigger != "ütemezett" {
		t.Fatalf("%+v", r)
	}
	must(t, h.st.SetRetry(j.ID, Retry{At: time.Now().Add(-13 * time.Hour), Attempt: 2, Of: 2}))
	h.r.Tick()
	if r := h.wait(t); r.Status != StatusMissed || !strings.Contains(r.Message, "elmaradt") {
		t.Fatalf("%+v", r)
	}
	// paused job: the retry is dropped
	j.Scheduled, j.Enabled = true, false
	j.Schedule = schedule.Spec{Type: schedule.Daily, Times: []string{"06:00"}}
	_, err = h.st.SaveJob(j, time.Now())
	must(t, err)
	must(t, h.st.SetRetry(j.ID, Retry{At: time.Now().Add(-time.Second), Attempt: 2, Of: 2}))
	h.r.Tick()
	if _, ok := h.st.PendingRetry(j.ID); ok || len(h.r.Live()) != 0 {
		t.Fatal("a paused job must not retry")
	}
	if Validate(&Job{Name: "a", Workflow: "x", RetryCount: 6}) == nil {
		t.Fatal("at most 5 retries")
	}
	v := Job{Name: "a", Workflow: "x", RetryCount: 2}
	if Validate(&v) != nil || v.RetryWaitMin != 5 {
		t.Fatalf("default wait: %+v", v)
	}
}

// Regression tests of the review findings.

// A double click (or a click at the scheduled time) must not start a job twice.
func TestEnqueueConcurrent(t *testing.T) {
	h := newHarness(t)
	j := h.job(t, "slow", nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.r.Enqueue(j, "kézi", time.Time{}); err == nil {
				mu.Lock()
				okCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if okCount != 1 || len(h.r.Live()) != 1 {
		t.Fatalf("started %d times, live %d", okCount, len(h.r.Live()))
	}
	time.Sleep(300 * time.Millisecond) // running now
	h.r.Cancel(h.r.Live()[0].ID)
	h.wait(t)
}

// A password split over two writes is still masked; Flush writes the rest.
func TestMaskSplitWrites(t *testing.T) {
	var b strings.Builder
	w := &tailWriter{w: &b, max: 1 << 10, masks: []string{"Titok123"}}
	w.Write([]byte("--credential=bi;d.admin;Tit"))
	w.Write([]byte("ok123\nkovetkezo sor Tito"))
	w.Write([]byte("k123 vege"))
	w.Flush()
	if strings.Contains(b.String(), "Titok123") || strings.Count(b.String(), "••••") != 2 || !strings.HasSuffix(b.String(), "vege") {
		t.Fatalf("%q", b.String())
	}
	if strings.Contains(w.String(), "Titok123") {
		t.Fatal("tail")
	}
}

// At program exit the running KNIME is stopped and waited for; nothing new
// starts afterwards.
func TestStopWaitsForRuns(t *testing.T) {
	h := newHarness(t)
	j := h.job(t, "slow", nil)
	h.r.Enqueue(j, "kézi", time.Time{})
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	h.r.Stop(10 * time.Second)
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("stop took %v", d)
	}
	if r := h.wait(t); r.Status != StatusCanceled {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(h.st.Dir(), "munka")); err == nil {
		if ents, _ := os.ReadDir(filepath.Join(h.st.Dir(), "munka")); len(ents) != 0 {
			t.Fatal("temporary workspace left behind")
		}
	}
	ok := h.job(t, "ok", nil)
	h.r.Enqueue(ok, "kézi", time.Time{})
	h.r.Tick()
	time.Sleep(300 * time.Millisecond)
	if live := h.r.Live(); len(live) != 1 || live[0].Status != StatusQueued {
		t.Fatalf("nothing may start after Stop: %+v", live)
	}
}

// While NodePit Batch is installed, queued runs wait.
func TestHold(t *testing.T) {
	h := newHarness(t)
	h.r.Hold(true)
	j := h.job(t, "ok", nil)
	h.r.Enqueue(j, "kézi", time.Time{})
	time.Sleep(400 * time.Millisecond)
	if live := h.r.Live(); len(live) != 1 || live[0].Status != StatusQueued {
		t.Fatalf("%+v", live)
	}
	h.r.Hold(false)
	if r := h.wait(t); r.Status != StatusOK {
		t.Fatalf("%+v", r)
	}
}

// Runs in the same own workspace never overlap (Parallel = 2 here).
func TestOwnWorkspaceSerialized(t *testing.T) {
	h := newHarness(t)
	ws := filepath.Join(h.root, "ws")
	must(t, os.MkdirAll(ws, 0o755))
	a := h.job(t, "slow", func(j *Job) { j.Workspace = ws })
	b := h.job(t, "ok", func(j *Job) { j.Workspace = ws + string(filepath.Separator) })
	c := h.job(t, "ok2", func(j *Job) { j.Workflow = workflow(t, h.root, "ok") })
	h.r.Enqueue(a, "kézi", time.Time{})
	time.Sleep(300 * time.Millisecond)
	h.r.Enqueue(b, "kézi", time.Time{})
	h.r.Enqueue(c, "kézi", time.Time{})
	if r := h.wait(t); r.JobID != c.ID || r.Status != StatusOK {
		t.Fatalf("the run with a temporary workspace goes ahead: %+v", r)
	}
	if live := h.r.Live(); len(live) != 2 {
		t.Fatalf("b must wait for a: %+v", live)
	}
	h.r.Cancel(h.r.Live()[0].ID)
	for i := 0; i < 2; i++ {
		r := h.wait(t)
		if r.JobID == b.ID {
			logText, _ := os.ReadFile(h.st.LogPath(r.Log))
			if r.Status != StatusOK || !strings.Contains(string(logText), "-data "+ws) {
				t.Fatalf("%+v\n%s", r, logText)
			}
		}
	}
	bad := h.job(t, "ok3", func(j *Job) { j.Workspace = filepath.Join(h.root, "nincs"); j.Workflow = workflow(t, h.root, "ok") })
	h.r.Enqueue(bad, "kézi", time.Time{})
	if r := h.wait(t); r.Status != StatusError || !strings.Contains(r.Message, "munkaterület") {
		t.Fatalf("%+v", r)
	}
}

// A schedule saved at 05:59:50 for 06:00 runs at 06:00 (the first look of
// the scheduler is after the time).
func TestFirstTickAfterSave(t *testing.T) {
	loc := time.UTC
	clk := &clock{t: time.Date(2026, 11, 9, 5, 59, 50, 0, loc)} // Monday
	h := newHarness(t, func(r *Runner) { r.Now, r.Location = clk.Now, loc })
	j := Job{Name: "x", Workflow: workflow(t, h.root, "ok"), Scheduled: true, Enabled: true,
		Schedule: schedule.Spec{Type: schedule.Daily, Times: []string{"06:00"}}}
	j, err := h.st.SaveJob(j, clk.Now())
	must(t, err)
	clk.Set(time.Date(2026, 11, 9, 6, 0, 5, 0, loc))
	h.r.Tick()
	if r := h.wait(t); r.Status != StatusOK || r.Trigger != "ütemezett" {
		t.Fatalf("%+v", r)
	}
}

func TestValidateCopyAndSave(t *testing.T) {
	if err := Validate(&Job{Name: "a", Workflow: "x", Copy: true, Save: true}); err == nil {
		t.Fatal("copy + save must be refused")
	}
}

// The preferences file of the settings goes to every run, unless the job sets one.
func TestPreferencesArg(t *testing.T) {
	a := strings.Join(BuildArgs(Job{}, "D", "W", nil, false, "p.epf"), " ")
	b := strings.Join(BuildArgs(Job{ExtraArgs: "--preferences=sajat.epf"}, "D", "W", nil, false, "p.epf"), " ")
	if !strings.Contains(a, "--preferences p.epf") || strings.Contains(b, "p.epf ") || !strings.Contains(b, "sajat.epf") {
		t.Fatalf("%s | %s", a, b)
	}
}

func TestShareFiles(t *testing.T) {
	if n := ShareFileName("EF-BI", `ENERGOFISH\d.admin`); n != "EF-BI__d.admin.json" {
		t.Fatal(n)
	}
	if n := ShareFileName("gép 1", ""); n != "g_p_1__ismeretlen.json" {
		t.Fatal(n)
	}
	root := t.TempDir()
	now := time.Now()
	mine := Shared{Version: ShareVersion, Host: "KLIENS", Updated: now, IntervalSec: 60}
	srv := Shared{Version: ShareVersion, Host: "EF-BI", Updated: now.Add(-2 * time.Minute), IntervalSec: 60,
		Jobs: []SharedJob{{ID: "j1", Name: "Napi"}}, Runs: []SharedRun{ShareRun(Run{ID: "r1", Message: "a\nb\nc\nd\ne"})}}
	must(t, WriteShared(root, "KLIENS__x.json", mine))
	must(t, WriteShared(root, "EF-BI__y.json", srv))
	must(t, os.WriteFile(filepath.Join(root, ShareFolder, "rossz.json"), []byte("{"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, ShareFolder, ".felkesz.json.tmp"), []byte("{"), 0o644))
	must(t, WriteShared(root, "UJ__z.json", Shared{Version: ShareVersion + 1, App: "9.9"}))
	rs, err := ReadShared(root, "kliens__X.json") // our own file is left out (case-insensitive)
	must(t, err)
	if len(rs) != 3 || rs[0].File != "EF-BI__y.json" || rs[0].Error != "" || rs[0].Data.Jobs[0].Name != "Napi" ||
		rs[0].Data.Runs[0].Message != "a\nb\nc" || rs[1].Error == "" || rs[2].Error == "" || !strings.Contains(rs[2].Error, "frissítse") {
		t.Fatalf("%+v", rs)
	}
	if rs[0].Data.Stale(now) || !rs[0].Data.Stale(now.Add(20*time.Minute)) {
		t.Fatal("stale")
	}
	must(t, RemoveShared(root, "EF-BI__y.json"))
	must(t, RemoveShared(root, "EF-BI__y.json")) // already gone: no error
	if rs, _ := ReadShared(t.TempDir(), ""); rs == nil || len(rs) != 0 {
		t.Fatal("no folder yet: empty, no error")
	}
}

func TestParseFailedNode(t *testing.T) {
	cases := []struct{ text, name, id, msg string }{
		// KNIME 5 knime.log layout
		{"2026-11-06 08:00:00,100 : WARN  : main : : Node : CSV Reader : 3:1 : file is empty\n" +
			"2026-11-06 08:00:01,000 : ERROR : KNIME-Worker-3-DB Reader 3:12 :  : Node : DB Reader : 3:12 : Execute failed: Login failed for user 'd.admin'\n" +
			"2026-11-06 08:00:01,100 : ERROR : main : : WorkflowManager : Workflow : 3 : Workflow did not execute",
			"DB Reader", "3:12", "Execute failed: Login failed for user 'd.admin'"},
		// a shorter layout (no MDC fields)
		{"2026-11-06 08:00:01,000 : ERROR : KNIME-Worker-1 : DB Reader : 0:5:12 : Execute failed: Connection timed out",
			"DB Reader", "0:5:12", "Execute failed: Connection timed out"},
		// no "Execute failed": the last error that names a node
		{"x : ERROR : main : : Node : Excel Writer : 2:7 : Could not write file\nx : ERROR : main : : Node : Table Writer : 2:9 : disk full",
			"Table Writer", "2:9", "disk full"},
	}
	for _, c := range cases {
		n := ParseFailedNode(c.text)
		if n == nil || n.Name != c.name || n.ID != c.id || n.Message != c.msg {
			t.Fatalf("%+v for %q", n, c.text[:40])
		}
	}
	if ParseFailedNode("x : ERROR : main : : Workflow did not execute") != nil || ParseFailedNode("") != nil {
		t.Fatal("no node")
	}
}

func TestParseNodeStates(t *testing.T) {
	text := `=================== Executing workflow ====================
Status of workflow before execution:
------------------------------------
  CSV Reader 3:1 (IDLE)
Status of workflow after execution:
------------------------------------
Napi_export 3 (start)
  "CSV Reader 3:1" (EXECUTED)
  "Row Filter 3:2" (EXECUTED)
  Metanode 3:5 - CONFIGURED (start)
    Column Filter 3:5:1 - EXECUTED
    DB Reader 3:5:12 - CONFIGURED
  Metanode 3:5 (end)
  Excel Writer 3:7: IDLE
Napi_export 3 (end)
========= Workflow did not execute sucessfully ============`
	if e, n := ParseNodeStates(text); e != 3 || n != 6 {
		t.Fatalf("%d/%d", e, n)
	}
	if e, n := ParseNodeStates("no summary"); e != 0 || n != 0 {
		t.Fatal("none")
	}
}
