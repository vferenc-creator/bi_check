package knime

import (
	"bufio"
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

// BuildArgs is the command line of a run (passwords in pw by credential name).
func BuildArgs(j Job, data, workflow string, pw map[string]string, consoleLog bool) []string {
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
	a = append(a, SplitArgs(j.ExtraArgs)...)
	return append(a, workflow)
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

	tickMu sync.Mutex // one scheduler step at a time

	mu     sync.Mutex
	queue  []*pending
	active map[string]*active
	wake   chan struct{}
}

type pending struct {
	run Run
	job Job
}

type active struct {
	run      Run
	cancel   context.CancelFunc
	canceled bool
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
	return Run{}, false
}

// Enqueue queues a run of a job.
func (r *Runner) Enqueue(j Job, trigger string, due time.Time) (Run, error) {
	if cur, busy := r.Busy(j.ID); busy {
		return cur, errors.New("ez a futtatás már fut vagy sorban áll")
	}
	run := Run{ID: NewID(), JobID: j.ID, JobName: j.Name, Trigger: trigger, Due: due, Queued: r.now(), Status: StatusQueued}
	if err := r.Store.PutRun(run); err != nil {
		return run, err
	}
	r.mu.Lock()
	if r.active == nil {
		r.active = map[string]*active{}
	}
	r.queue = append(r.queue, &pending{run: run, job: j})
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

// CancelAll stops everything (program exit).
func (r *Runner) CancelAll() {
	r.mu.Lock()
	q := r.queue
	r.queue = nil
	for _, a := range r.active {
		a.canceled = true
		a.cancel()
	}
	r.mu.Unlock()
	for _, p := range q {
		run := p.run
		run.Status, run.Message, run.Ended = StatusCanceled, "A program leállt, mielőtt elindult.", r.now()
		_ = r.Store.PutRun(run)
	}
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
		if len(r.queue) == 0 || len(r.active) >= r.Store.Config().Parallel {
			r.mu.Unlock()
			return
		}
		p := r.queue[0]
		r.queue = r.queue[1:]
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
		a := &active{run: p.run, cancel: cancel}
		r.active[p.run.ID] = a
		r.mu.Unlock()
		_ = r.Store.PutRun(p.run)
		r.changed()
		go func() {
			defer cancel()
			run := r.execute(ctx, a, p.job)
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
	defer os.RemoveAll(work)
	data := filepath.Join(work, "workspace")
	if err := os.MkdirAll(data, 0o755); err != nil {
		return finish(StatusError, "Az ideiglenes munkamappa nem hozható létre: "+err.Error())
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
	args := BuildArgs(j, data, wf, pw, r.Store.Config().ConsoleLog)
	run.Log = run.ID + ".log"
	lf, err := os.Create(r.Store.LogPath(run.Log))
	if err != nil {
		return finish(StatusError, "A naplófájl nem hozható létre: "+err.Error())
	}
	defer lf.Close()
	fmt.Fprintf(lf, "BI Monitor – KNIME futtatás: %s\nIndítva: %s (%s)\nParancs: %s %s\n\n",
		j.Name, run.Started.Format("2006-01-02 15:04:05"), run.Trigger, exe, mask(strings.Join(quoteArgs(args), " "), pw))
	out := &tailWriter{w: lf, max: 64 << 10}
	for _, p := range pw {
		if p != "" {
			out.masks = append(out.masks, p)
		}
	}
	command := r.Command
	if command == nil {
		command = defaultCommand
	}
	cmd := command(ctx, exe, args)
	cmd.Stdout, cmd.Stderr = out, out
	if cmd.Dir == "" {
		cmd.Dir = filepath.Dir(exe)
	}
	runErr := cmd.Run()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	// through the masking writer: knime.log may repeat the arguments
	appendKnimeLog(out, filepath.Join(data, ".metadata", "knime", "knime.log"))
	text := out.String()
	switch {
	case a.canceled:
		return finish(StatusCanceled, "Megszakítva.")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return finish(StatusTimeout, fmt.Sprintf("Időtúllépés: %d perc után leállítva.", j.TimeoutMin))
	case cmd.ProcessState == nil:
		return finish(StatusError, "A KNIME nem indítható: "+errText(runErr))
	}
	c := code
	run.ExitCode = &c
	if code == 0 {
		return finish(StatusOK, ExitText(0))
	}
	msg := ExitText(code)
	if hint := hintFrom(text); hint != "" {
		msg += " " + hint
	}
	if errs := errorLines(text, 3); len(errs) > 0 {
		msg += "\n" + strings.Join(errs, "\n")
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
func appendKnimeLog(w io.Writer, path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	var ring []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		ring = append(ring, sc.Text())
		if len(ring) > 400 {
			ring = ring[len(ring)-400:]
		}
	}
	fmt.Fprintf(w, "\n===== knime.log (utolsó %d sor) =====\n%s\n", len(ring), strings.Join(ring, "\n"))
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
// Passwords (masks) never reach the log.
type tailWriter struct {
	mu    sync.Mutex
	w     io.Writer
	buf   []byte
	max   int
	masks []string
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	q := p
	for _, m := range t.masks {
		q = []byte(strings.ReplaceAll(string(q), m, "••••"))
	}
	t.buf = append(t.buf, q...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	if _, err := t.w.Write(q); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
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
			_ = r.Store.SetLastDue(j.ID, prev)
			continue
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
				_ = r.Store.PutRun(run)
				if r.OnFinish != nil {
					r.OnFinish(run, j)
				}
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
