package knime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bimonitor/internal/calendar"
	"bimonitor/internal/schedule"
)

// CatchUp: a scheduled instant is still run this late (e.g. the computer
// woke up); older ones are recorded as missed.
const CatchUp = 15 * time.Minute

// ExitText explains a NodePit Batch exit code.
func ExitText(code int) string {
	switch code {
	case 0:
		return "Sikeres futás."
	case 10:
		return "Hibás paraméter (pl. egy változó vagy hitelesítés formátuma) – 10-es kód."
	case 20:
		return "A workflow nem tölthető be (rossz útvonal, sérült vagy másik KNIME-ban nyitott workflow) – 20-as kód."
	case 30:
		return "Hiba a workflow futtatása közben: valamelyik csomópont hibára futott – 30-as kód."
	case 40:
		return "A workflow mentése nem sikerült – 40-es kód."
	case 50:
		return "Ismeretlen hiba a futtatóban – 50-es kód."
	case 13:
		return "A KNIME nem tudta elindítani a futtatót – telepítve van a NodePit Batch ebben a KNIME-ban? (13-as kód)"
	}
	return fmt.Sprintf("A KNIME %d-es kóddal állt le.", code)
}

// BuildArgs is the command line of a run (passwords in pw by credential name;
// prefs: a KNIME preferences file for every run, unless the job sets one).
func BuildArgs(j Job, data, workflow string, pw map[string]string, consoleLog bool, prefs string) []string {
	a := []string{"-nosplash", "--launcher.suppressErrors"}
	if consoleLog {
		a = append(a, "-consoleLog")
	}
	a = append(a, "-application", AppID, "-data", data)
	if j.Reset {
		a = append(a, "--reset")
	}
	if j.Save {
		a = append(a, "--save")
	} else {
		a = append(a, "--no-save")
	}
	for _, v := range j.Variables {
		a = append(a, "--variable="+v.Name+";"+v.Value+";"+v.Type)
	}
	for _, c := range j.Credentials {
		s := "--credential=" + c.Name + ";" + c.Login
		if p := pw[c.Name]; p != "" {
			s += ";" + p
		}
		a = append(a, s)
	}
	extra := SplitArgs(j.ExtraArgs)
	if prefs != "" && !hasOpt(extra, "--preferences", "-ps") {
		a = append(a, "--preferences", prefs)
	}
	a = append(a, extra...)
	return append(a, workflow)
}

func hasOpt(args []string, names ...string) bool {
	for _, a := range args {
		for _, n := range names {
			if a == n || strings.HasPrefix(a, n+"=") {
				return true
			}
		}
	}
	return false
}

// SplitArgs splits extra options on spaces, keeping "quoted parts" together.
func SplitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	quote, has := false, false
	for _, r := range s {
		switch {
		case r == '"':
			quote, has = !quote, true
		case (r == ' ' || r == '\t') && !quote:
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}

// Runner starts the runs of the jobs: a queue with at most Config.Parallel
// runs at once, each in its own temporary KNIME workspace.
type Runner struct {
	Store *Store
	// Password returns a stored credential password.
	Password func(jobID, cred string) (string, error)
	// OnChange is called when a run changes (UI refresh).
	OnChange func()
	// OnFinish is called with every finished, missed or skipped run.
	OnFinish func(Run, Job)
	// Command builds the process (tests replace it).
	Command func(ctx context.Context, exe string, args []string) *exec.Cmd
	// Calendar and Location of the schedules.
	Calendar *calendar.Calendar
	Location *time.Location
	Now      func() time.Time

	unit time.Duration // of TimeoutMin (tests shorten it)

	tickMu  sync.Mutex  // one scheduler step at a time
	stopped atomic.Bool // the program is exiting: start nothing new

	mu       sync.Mutex
	queue    []*pending
	active   map[string]*active
	reserved map[string]bool // job IDs being queued (between the check and the queue)
	held     bool            // no new starts (e.g. while NodePit Batch is installed)
	running  sync.WaitGroup  // run goroutines (Stop waits for them)
	wake     chan struct{}
}

// errBusy: the job is already queued or running.
var errBusy = errors.New("ez a futtatás már fut vagy sorban áll")

type pending struct {
	run Run
	job Job
}

type active struct {
	run       Run
	cancel    context.CancelFunc
	canceled  bool
	workspace string   // own workspace in use ("" = temporary)
	data      string   // the run's KNIME workspace (its knime.log: live log)
	log       string   // the run's log file name
	logFrom   int64    // size of knime.log at the start (an own workspace keeps older runs in it)
	masks     []string // the run's passwords (hidden in the live log)
	shutdown  bool     // stopped by the program's stop, not by the user
}

// workspaceKey identifies a job's own workspace ("" = temporary).
func workspaceKey(j Job) string {
	if strings.TrimSpace(j.Workspace) == "" {
		return ""
	}
	p, err := filepath.Abs(strings.TrimSpace(j.Workspace))
	if err != nil {
		p = j.Workspace
	}
	return strings.ToLower(filepath.Clean(p))
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) changed() {
	if r.OnChange != nil {
		r.OnChange()
	}
}

// Start begins the dispatcher and the scheduler (every 20 s).
func (r *Runner) Start(ctx context.Context) {
	r.mu.Lock()
	if r.active == nil {
		r.active = map[string]*active{}
	}
	if r.wake == nil {
		r.wake = make(chan struct{}, 1)
	}
	r.mu.Unlock()
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		r.Tick()
		for {
			select {
			case <-ctx.Done():
				r.stopped.Store(true)
				r.CancelAll()
				return
			case <-r.wake:
				r.dispatch()
			case <-t.C:
				r.Tick()
				r.dispatch()
			}
		}
	}()
}

func (r *Runner) poke() {
	if r.wake == nil {
		r.dispatch()
		return
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Busy reports whether a job is queued or running.
func (r *Runner) Busy(jobID string) (Run, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.active {
		if a.run.JobID == jobID {
			return a.run, true
		}
	}
	for _, p := range r.queue {
		if p.run.JobID == jobID {
			return p.run, true
		}
	}
	if r.reserved[jobID] {
		return Run{JobID: jobID, Status: StatusQueued}, true
	}
	return Run{}, false
}

// Enqueue queues a (first) run of a job; it replaces a pending retry.
func (r *Runner) Enqueue(j Job, trigger string, due time.Time) (Run, error) {
	run, err := r.enqueue(j, trigger, due, 1)
	if err == nil && r.Store.ClearRetry(j.ID) {
		r.changed()
	}
	return run, err
}

func (r *Runner) enqueue(j Job, trigger string, due time.Time, attempt int) (Run, error) {
	// check and reserve in one step: a double click or a click at the
	// scheduled time must not start the job twice
	r.mu.Lock()
	for _, a := range r.active {
		if a.run.JobID == j.ID {
			cur := a.run // copied under the lock
			r.mu.Unlock()
			return cur, errBusy
		}
	}
	for _, p := range r.queue {
		if p.run.JobID == j.ID {
			cur := p.run
			r.mu.Unlock()
			return cur, errBusy
		}
	}
	if r.reserved[j.ID] {
		r.mu.Unlock()
		return Run{JobID: j.ID}, errBusy
	}
	if r.reserved == nil {
		r.reserved = map[string]bool{}
	}
	r.reserved[j.ID] = true
	r.mu.Unlock()
	unreserve := func() {
		r.mu.Lock()
		delete(r.reserved, j.ID)
		r.mu.Unlock()
	}
	run := Run{ID: NewID(), JobID: j.ID, JobName: j.Name, Trigger: trigger, Due: due, Queued: r.now(), Status: StatusQueued,
		Attempt: attempt, Attempts: max(j.RetryCount+1, attempt)}
	if err := r.Store.PutRun(run); err != nil {
		unreserve()
		return run, fmt.Errorf("a futás nem rögzíthető: %w", err)
	}
	r.mu.Lock()
	if r.active == nil {
		r.active = map[string]*active{}
	}
	r.queue = append(r.queue, &pending{run: run, job: j})
	delete(r.reserved, j.ID)
	r.mu.Unlock()
	r.changed()
	r.poke()
	return run, nil
}

// Cancel stops a queued or running run.
func (r *Runner) Cancel(runID string) bool {
	r.mu.Lock()
	for i, p := range r.queue {
		if p.run.ID == runID {
			r.queue = append(r.queue[:i], r.queue[i+1:]...)
			r.mu.Unlock()
			run := p.run
			run.Status, run.Message, run.Ended = StatusCanceled, "Megszakítva, mielőtt elindult.", r.now()
			_ = r.Store.PutRun(run)
			r.changed()
			return true
		}
	}
	a := r.active[runID]
	if a != nil {
		a.canceled = true
		a.cancel()
	}
	r.mu.Unlock()
	return a != nil
}

// Hold stops (true) or allows (false) starting queued runs; runs already
// going continue. Used while NodePit Batch is (re)installed.
func (r *Runner) Hold(on bool) {
	r.mu.Lock()
	r.held = on
	r.mu.Unlock()
	if !on {
		r.poke()
	}
}

// Stop ends the runner at program exit: nothing new starts, the running
// KNIME processes are stopped and waited for (at most timeout), so no
// KNIME is left running and the temporary workspaces are removed.
func (r *Runner) Stop(timeout time.Duration) {
	r.stopped.Store(true)
	r.CancelAll()
	done := make(chan struct{})
	go func() { r.running.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// CancelAll stops everything (program exit).
func (r *Runner) CancelAll() {
	r.mu.Lock()
	q := r.queue
	r.queue = nil
	for _, a := range r.active {
		a.canceled, a.shutdown = true, true
		a.cancel()
	}
	r.mu.Unlock()
	for _, p := range q {
		run := p.run
		run.Status, run.Message, run.Ended = StatusCanceled, MsgStoppedQueued, r.now()
		r.Store.ResumeLater(&run, false, r.now())
		_ = r.Store.PutRun(run)
	}
}

// LiveLog returns what a running run has written so far and the new part
// of its knime.log, passwords hidden; ok is false when it is not running.
func (r *Runner) LiveLog(runID string) (string, bool) {
	r.mu.Lock()
	a := r.active[runID]
	if a == nil || a.data == "" {
		r.mu.Unlock()
		return "", false
	}
	name, data, from, masks := a.log, a.data, a.logFrom, a.masks
	r.mu.Unlock()
	text := readFrom(r.Store.LogPath(name), 0, 400<<10, 0) +
		"\n===== knime.log (élő – a futás még tart) =====\n" + readFrom(knimeLogPath(data), from, 1<<20, 300)
	for _, m := range masks {
		text = strings.ReplaceAll(text, m, "••••")
	}
	return text, true
}

// Live returns the queued and running runs.
func (r *Runner) Live() []Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Run{}
	for _, a := range r.active {
		out = append(out, a.run)
	}
	for _, p := range r.queue {
		out = append(out, p.run)
	}
	return out
}

func (r *Runner) dispatch() {
	for {
		r.mu.Lock()
		if r.active == nil {
			r.active = map[string]*active{}
		}
		if len(r.queue) == 0 || r.held || r.stopped.Load() || len(r.active) >= r.Store.Config().Parallel {
			r.mu.Unlock()
			return
		}
		// the first queued run whose own workspace is free (a KNIME
		// workspace can be used by one KNIME at a time)
		pick := -1
		for i, q := range r.queue {
			ws := workspaceKey(q.job)
			free := true
			for _, a := range r.active {
				if ws != "" && a.workspace == ws {
					free = false
					break
				}
			}
			if free {
				pick = i
				break
			}
		}
		if pick < 0 {
			r.mu.Unlock()
			return
		}
		p := r.queue[pick]
		r.queue = append(r.queue[:pick:pick], r.queue[pick+1:]...)
		unit := r.unit
		if unit == 0 {
			unit = time.Minute
		}
		timeout := time.Duration(p.job.TimeoutMin) * unit
		var ctx context.Context
		var cancel context.CancelFunc
		if timeout > 0 {
			ctx, cancel = context.WithTimeout(context.Background(), timeout)
		} else {
			ctx, cancel = context.WithCancel(context.Background())
		}
		p.run.Status, p.run.Started = StatusRunning, r.now()
		a := &active{run: p.run, cancel: cancel, workspace: workspaceKey(p.job)}
		r.active[p.run.ID] = a
		r.running.Add(1)
		r.mu.Unlock()
		_ = r.Store.PutRun(p.run)
		r.changed()
		go func() {
			defer r.running.Done()
			defer cancel()
			run := r.execute(ctx, a, p.job)
			r.mu.Lock()
			shutdown := a.shutdown
			r.mu.Unlock()
			if shutdown && run.Status == StatusCanceled {
				// the program stops: a scheduled run goes on at the next start
				run.Message = MsgStoppedRunning
				r.Store.ResumeLater(&run, true, r.now())
			} else {
				r.planRetry(&run, p.job)
			}
			r.mu.Lock()
			delete(r.active, run.ID)
			r.mu.Unlock()
			_ = r.Store.PutRun(run)
			r.changed()
			if r.OnFinish != nil {
				r.OnFinish(run, p.job)
			}
			r.poke()
		}()
	}
}

// Retryable: failures and timeouts, but not wrong parameters (exit code 10:
// the same input fails again) and not runs stopped by hand or at exit.
func Retryable(run Run) bool {
	switch run.Status {
	case StatusError, StatusTimeout:
		return run.ExitCode == nil || *run.ExitCode != 10
	}
	return false
}

// planRetry schedules the next try of a failed run (if the job allows more).
func (r *Runner) planRetry(run *Run, j Job) {
	if !Retryable(*run) || run.Attempt < 1 || run.Attempt > j.RetryCount {
		return
	}
	unit := r.unit
	if unit == 0 {
		unit = time.Minute
	}
	wait := time.Duration(max(j.RetryWaitMin, 1)) * unit
	at := r.now().Add(wait)
	rt := Retry{At: at, Attempt: run.Attempt + 1, Of: j.RetryCount + 1, Trigger: run.Trigger, Due: run.Due}
	if err := r.Store.SetRetry(j.ID, rt); err != nil {
		return
	}
	run.NextRetry = at
	run.Message += fmt.Sprintf("\nÚjrapróbálás %s-kor (%d/%d. próbálkozás).", at.In(r.loc()).Format("15:04"), rt.Attempt, rt.Of)
	time.AfterFunc(wait, func() { r.Tick() })
}

// CancelRetry drops the pending retry of a job.
func (r *Runner) CancelRetry(jobID string) bool {
	ok := r.Store.ClearRetry(jobID)
	if ok {
		r.changed()
	}
	return ok
}

// execute runs one job and returns the finished run.
func (r *Runner) execute(ctx context.Context, a *active, j Job) Run {
	run := a.run
	finish := func(status, msg string) Run {
		run.Status, run.Message, run.Ended = status, msg, r.now()
		run.DurationMs = run.Ended.Sub(run.Started).Milliseconds()
		return run
	}
	exe := strings.TrimSpace(r.Store.Config().Exe)
	if exe == "" {
		return finish(StatusError, "Nincs beállítva a KNIME (KNIME menü → KNIME és NodePit Batch).")
	}
	work := filepath.Join(r.Store.Dir(), "munka", run.ID)
	defer removeAll(work)
	data := filepath.Join(work, "workspace")
	if err := os.MkdirAll(data, 0o755); err != nil {
		return finish(StatusError, "Az ideiglenes munkamappa nem hozható létre: "+err.Error())
	}
	if ws := strings.TrimSpace(j.Workspace); ws != "" {
		abs, err := filepath.Abs(ws)
		if err != nil {
			abs = ws
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return finish(StatusError, "A megadott KNIME munkaterület nem található: "+ws)
		}
		data = abs
	}
	wf, err := workflowPath(j.Workflow)
	if err != nil {
		return finish(StatusError, err.Error())
	}
	if j.Copy {
		if st, _ := os.Stat(wf); st != nil && st.IsDir() {
			dst := filepath.Join(work, "workflow", filepath.Base(wf))
			if err := copyDir(wf, dst); err != nil {
				return finish(StatusError, "A workflow másolása nem sikerült: "+err.Error())
			}
			wf = dst
		}
	}
	pw := map[string]string{}
	for _, c := range j.Credentials {
		if c.HasPassword && r.Password != nil {
			p, err := r.Password(j.ID, c.Name)
			if err != nil {
				return finish(StatusError, fmt.Sprintf("A(z) %q hitelesítés jelszava nem olvasható: %v", c.Name, err))
			}
			pw[c.Name] = p
		}
	}
	cfg := r.Store.Config()
	args := BuildArgs(j, data, wf, pw, cfg.ConsoleLog, strings.TrimSpace(cfg.Preferences))
	run.Log = run.ID + ".log"
	// an own workspace keeps the knime.log of earlier runs: only what this
	// run adds is read
	var logFrom int64
	if st, err := os.Stat(knimeLogPath(data)); err == nil {
		logFrom = st.Size()
	}
	var masks []string
	for _, p := range pw {
		if p != "" {
			masks = append(masks, p)
		}
	}
	r.mu.Lock()
	a.data, a.log, a.logFrom, a.masks = data, run.Log, logFrom, masks
	r.mu.Unlock()
	lf, err := os.Create(r.Store.LogPath(run.Log))
	if err != nil {
		return finish(StatusError, "A naplófájl nem hozható létre: "+err.Error())
	}
	defer lf.Close()
	fmt.Fprintf(lf, "BI Monitor – KNIME futtatás: %s\nIndítva: %s (%s)\nParancs: %s %s\n\n",
		j.Name, run.Started.Format("2006-01-02 15:04:05"), run.Trigger, exe, mask(strings.Join(quoteArgs(args), " "), pw))
	out := &tailWriter{w: lf, max: 64 << 10, masks: masks}
	command := r.Command
	if command == nil {
		command = defaultCommand
	}
	cmd := command(ctx, exe, args)
	cmd.Stdout, cmd.Stderr = out, out
	if cmd.Dir == "" {
		cmd.Dir = filepath.Dir(exe)
	}
	runErr := cmd.Start()
	if runErr == nil {
		// Windows: the KNIME process tree dies with the BI Monitor
		release := killWithParent(cmd)
		runErr = cmd.Wait()
		release()
	}
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	out.Flush()
	// through the masking writer: knime.log may repeat the arguments
	appendKnimeLog(out, knimeLogPath(data), logFrom)
	out.Flush()
	text := out.String()
	r.mu.Lock()
	canceled := a.canceled
	r.mu.Unlock()
	switch {
	case cmd.ProcessState != nil && code == 0:
		// finished successfully, even if a stop or the time limit came
		// at the very same moment
	case canceled:
		return finish(StatusCanceled, "Megszakítva.")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return finish(StatusTimeout, fmt.Sprintf("Időtúllépés: %d perc után leállítva.", j.TimeoutMin))
	case cmd.ProcessState == nil:
		return finish(StatusError, "A KNIME nem indítható: "+errText(runErr))
	}
	c := code
	run.ExitCode = &c
	run.NodesExecuted, run.NodesTotal = ParseNodeStates(text)
	if code == 0 {
		return finish(StatusOK, ExitText(0))
	}
	run.FailedNode = ParseFailedNode(text)
	if ws := strings.TrimSpace(j.Workspace); ws != "" && strings.Contains(strings.ToLower(text), "workspace") &&
		(strings.Contains(strings.ToLower(text), "in use") || strings.Contains(strings.ToLower(text), "locked")) {
		text += "\nA munkaterület foglalt: valószínűleg nyitva van a KNIME ablakban."
	}
	msg := ExitText(code)
	if hint := hintFrom(text); hint != "" {
		msg += " " + hint
	}
	if fn := run.FailedNode; fn != nil {
		// the failed node first: it says what to fix
		msg += "\nHibás csomópont: " + fn.Name + " (#" + fn.ID + ") – " + fn.Message
	} else if errs := errorLines(text, 3); len(errs) > 0 {
		msg += "\n" + strings.Join(errs, "\n")
	}
	if run.NodesTotal > 0 {
		msg += fmt.Sprintf("\nLefutott csomópontok: %d / %d.", run.NodesExecuted, run.NodesTotal)
	}
	return finish(StatusError, msg)
}

func errText(err error) string {
	if err == nil {
		return "ismeretlen ok"
	}
	return err.Error()
}

// workflowPath checks the workflow: a folder with workflow.knime, or a
// .knwf / .knar file.
func workflowPath(p string) (string, error) {
	p = strings.TrimSpace(strings.Trim(strings.TrimSpace(p), `"`))
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("a workflow nem található: %s", p)
	}
	if st.IsDir() {
		if _, err := os.Stat(filepath.Join(p, "workflow.knime")); err != nil {
			return "", fmt.Errorf("ebben a mappában nincs KNIME workflow (workflow.knime): %s", p)
		}
		return p, nil
	}
	low := strings.ToLower(p)
	if !strings.HasSuffix(low, ".knwf") && !strings.HasSuffix(low, ".knar") {
		return "", fmt.Errorf("a workflow egy mappa vagy .knwf / .knar fájl lehet: %s", p)
	}
	return p, nil
}

func hintFrom(text string) string {
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "locked by another knime"):
		return "A workflow nyitva van egy másik KNIME-ban – zárja be, vagy kapcsolja be a „Futtatás másolatból” opciót."
	case strings.Contains(low, "com.nodepit.batch") && strings.Contains(low, "could not be found"):
		return "A NodePit Batch nincs telepítve ebben a KNIME-ban."
	case strings.Contains(low, "workspace") && strings.Contains(low, "in use"):
		return "A munkamappa foglalt."
	}
	return ""
}

// errorLines are the last distinct ERROR lines of the output and knime.log.
func errorLines(text string, n int) []string {
	var out []string
	seen := map[string]bool{}
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		l := strings.TrimSpace(lines[i])
		if !strings.Contains(l, "ERROR") && !strings.Contains(l, "did not execute") {
			continue
		}
		if len(l) > 300 {
			l = l[:300] + "…"
		}
		if !seen[l] {
			seen[l] = true
			out = append([]string{l}, out...)
		}
	}
	return out
}

// appendKnimeLog adds the end of the run's knime.log to the log file.
func appendKnimeLog(w io.Writer, path string, from int64) {
	text := readFrom(path, from, 4<<20, 400)
	if text == "" {
		return
	}
	fmt.Fprintf(w, "\n===== knime.log (a futás sorai, legfeljebb az utolsó 400) =====\n%s\n", text)
}

func knimeLogPath(data string) string {
	return filepath.Join(data, ".metadata", "knime", "knime.log")
}

// readFrom reads a file from the offset from (from the start when the file
// got shorter: KNIME rotated it), at most the last max bytes and, if
// lines > 0, the last lines lines.
func readFrom(path string, from, max int64, lines int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	size := st.Size()
	if from > size {
		from = 0
	}
	cut := false
	if size-from > max {
		from, cut = size-max, true
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(f, max))
	text := strings.TrimRight(string(b), "\r\n")
	if cut { // the first line is probably partial
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	if lines > 0 {
		ls := strings.Split(text, "\n")
		if len(ls) > lines {
			ls = ls[len(ls)-lines:]
		}
		text = strings.Join(ls, "\n")
	}
	return text
}

func quoteArgs(a []string) []string {
	out := make([]string, len(a))
	for i, s := range a {
		if strings.ContainsAny(s, " \t") {
			s = `"` + s + `"`
		}
		out[i] = s
	}
	return out
}

func mask(s string, pw map[string]string) string {
	for _, p := range pw {
		if p != "" {
			s = strings.ReplaceAll(s, p, "••••")
		}
	}
	return s
}

// tailWriter writes through and keeps the last max bytes for evaluation.
// Passwords (masks) never reach the log: the output is masked line by line
// (a password may arrive split over two writes), the rest of an unfinished
// line waits for the next write or Flush.
type tailWriter struct {
	mu    sync.Mutex
	w     io.Writer
	buf   []byte
	max   int
	masks []string
	pend  []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pend = append(t.pend, p...)
	cut := -1
	for i := len(t.pend) - 1; i >= 0; i-- {
		if t.pend[i] == '\n' {
			cut = i + 1
			break
		}
	}
	if cut < 0 && len(t.pend) < 64<<10 {
		return len(p), nil // wait for the end of the line
	}
	if cut < 0 {
		cut = len(t.pend)
	}
	if err := t.emit(t.pend[:cut]); err != nil {
		return 0, err
	}
	t.pend = append([]byte(nil), t.pend[cut:]...)
	return len(p), nil
}

// Flush writes the unfinished last line.
func (t *tailWriter) Flush() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.pend) > 0 {
		_ = t.emit(t.pend)
		t.pend = nil
	}
}

func (t *tailWriter) emit(b []byte) error {
	s := string(b)
	for _, m := range t.masks {
		s = strings.ReplaceAll(s, m, "••••")
	}
	t.buf = append(t.buf, s...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	_, err := io.WriteString(t.w, s)
	return err
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// removeAll deletes a temporary folder; on Windows Java may still hold its
// files for a moment after the process ended.
func removeAll(dir string) {
	for i := 0; i < 10; i++ {
		if err := os.RemoveAll(dir); err == nil {
			return
		}
		time.Sleep(time.Duration(i+1) * 200 * time.Millisecond)
	}
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if d.Name() == ".knimeLock" {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		o, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(o, in); err != nil {
			o.Close()
			return err
		}
		return o.Close()
	})
}

// Tick starts the scheduled runs that are due. A job's first tick only
// remembers the last instant (no catching up after saving); instants more
// than CatchUp old (the program was not running) are recorded as missed.
func (r *Runner) Tick() {
	if r.stopped.Load() {
		return
	}
	r.tickMu.Lock()
	defer r.tickMu.Unlock()
	now := r.now()
	for _, j := range r.Store.Jobs() {
		if !j.Scheduled || !j.Enabled {
			continue
		}
		sc, err := schedule.Compile(j.Schedule, r.Calendar, r.Location)
		if err != nil {
			continue
		}
		prev, ok := sc.Prev(now)
		if !ok {
			continue
		}
		last := r.Store.LastDue(j.ID)
		if last.IsZero() {
			// first look after saving: instants after the save are due,
			// earlier ones are not caught up
			last = j.Updated
			if last.IsZero() || last.After(now) {
				last = now
			}
		}
		if !prev.After(last) {
			continue
		}
		_ = r.Store.SetLastDue(j.ID, prev)
		late := now.Sub(prev)
		switch {
		case late > CatchUp:
			run := Run{ID: NewID(), JobID: j.ID, JobName: j.Name, Trigger: "ütemezett", Due: prev, Queued: now, Ended: now, Status: StatusMissed,
				Message: "Kimaradt: a BI Monitor nem futott az ütemezett időpontban (" + prev.In(r.loc()).Format("2006-01-02 15:04") + ")."}
			_ = r.Store.PutRun(run)
			if r.OnFinish != nil {
				r.OnFinish(run, j)
			}
		default:
			if _, err := r.Enqueue(j, "ütemezett", prev); err != nil {
				run := Run{ID: NewID(), JobID: j.ID, JobName: j.Name, Trigger: "ütemezett", Due: prev, Queued: now, Ended: now, Status: StatusSkipped,
					Message: "Kihagyva: az előző futás még tart."}
				if !errors.Is(err, errBusy) {
					run.Status, run.Message = StatusError, "Nem indítható: "+err.Error()
				}
				_ = r.Store.PutRun(run)
				if r.OnFinish != nil {
					r.OnFinish(run, j)
				}
			}
		}
		r.changed()
	}
	r.tickRetries(now)
}

// MaxRetryAge: a retry that was due this long ago (the program was not
// running) is dropped instead of starting a stale run.
const MaxRetryAge = 12 * time.Hour

// tickRetries starts the pending retries that are due.
func (r *Runner) tickRetries(now time.Time) {
	for id, rt := range r.Store.Retries() {
		if now.Before(rt.At) {
			continue
		}
		r.Store.ClearRetry(id)
		j, ok := r.Store.Job(id)
		switch {
		case !ok || (j.Scheduled && !j.Enabled):
			// deleted or paused meanwhile
		case now.Sub(rt.At) > MaxRetryAge:
			run := Run{ID: NewID(), JobID: j.ID, JobName: j.Name, Trigger: rt.Trigger, Due: rt.Due, Queued: now, Ended: now, Status: StatusMissed,
				Attempt: rt.Attempt, Attempts: rt.Of,
				Message: "Az újrapróbálás elmaradt: a BI Monitor nem futott " + rt.At.In(r.loc()).Format("2006-01-02 15:04") + "-kor."}
			_ = r.Store.PutRun(run)
			if r.OnFinish != nil {
				r.OnFinish(run, j)
			}
		default:
			if _, busy := r.Busy(id); !busy { // a newer run is going anyway
				_, _ = r.enqueue(j, rt.Trigger, rt.Due, rt.Attempt)
			}
		}
		r.changed()
	}
}

func (r *Runner) loc() *time.Location {
	if r.Location != nil {
		return r.Location
	}
	return time.Local
}

// Next is the next scheduled instant of a job (zero if none).
func (r *Runner) Next(j Job) time.Time {
	if !j.Scheduled || !j.Enabled {
		return time.Time{}
	}
	sc, err := schedule.Compile(j.Schedule, r.Calendar, r.Location)
	if err != nil {
		return time.Time{}
	}
	t, _ := sc.Next(r.now())
	return t
}

// RunCommand runs a KNIME command (e.g. installing an extension) hidden,
// returning the exit code and the end of its output.
func RunCommand(ctx context.Context, exe string, args []string) (int, string, error) {
	cmd := defaultCommand(ctx, exe, args)
	cmd.Dir = filepath.Dir(exe)
	out := &tailWriter{w: io.Discard, max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	if cmd.ProcessState == nil {
		return -1, out.String(), err
	}
	return cmd.ProcessState.ExitCode(), out.String(), nil
}
