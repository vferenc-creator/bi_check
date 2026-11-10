package knime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bimonitor/internal/schedule"
)

// An own workspace keeps the knime.log of the earlier runs (and of the
// KNIME window): the failed node and the log come from this run only.
func TestOwnWorkspaceLogOffset(t *testing.T) {
	h := newHarness(t)
	ws := filepath.Join(h.root, "ws")
	must(t, os.MkdirAll(ws, 0o755))
	j1 := h.job(t, "fail30", func(j *Job) { j.Workspace = ws })
	h.r.Enqueue(j1, "kézi", time.Time{})
	r := h.wait(t)
	if r.FailedNode == nil || r.FailedNode.ID != "3:12" {
		t.Fatalf("first run: %+v", r.FailedNode)
	}
	j2 := h.job(t, "fail30b", func(j *Job) { j.Workspace = ws })
	h.r.Enqueue(j2, "kézi", time.Time{})
	r = h.wait(t)
	if r.FailedNode == nil || r.FailedNode.ID != "3:20" || strings.Contains(r.Message, "Login failed") {
		t.Fatalf("second run must name its own node: %+v / %q", r.FailedNode, r.Message)
	}
	b, _ := os.ReadFile(h.st.LogPath(r.Log))
	if strings.Contains(string(b), "Login failed") || !strings.Contains(string(b), "file in use") {
		t.Fatalf("log of the second run holds the first one's lines:\n%s", b)
	}
}

// The live log hides the run's passwords and shows only this run's lines.
func TestLiveLogMasked(t *testing.T) {
	h := newHarness(t)
	ws := filepath.Join(h.root, "ws")
	must(t, os.MkdirAll(filepath.Join(ws, ".metadata", "knime"), 0o755))
	must(t, os.WriteFile(filepath.Join(ws, ".metadata", "knime", "knime.log"), []byte("RÉGI SOR a tegnapi futásból\n"), 0o644))
	j := h.job(t, "slowargs", func(j *Job) {
		j.Workspace = ws
		j.Credentials = []Credential{{Name: "bi", Login: "d.admin", HasPassword: true}}
	})
	run, err := h.r.Enqueue(j, "kézi", time.Time{})
	must(t, err)
	var text string
	for i := 0; i < 50 && !strings.Contains(text, "Arguments"); i++ {
		time.Sleep(100 * time.Millisecond)
		text, _ = h.r.LiveLog(run.ID)
	}
	if !strings.Contains(text, "Arguments") || strings.Contains(text, "Titok123") || !strings.Contains(text, "••••") {
		t.Fatalf("live log not masked:\n%s", text)
	}
	if strings.Contains(text, "RÉGI SOR") {
		t.Fatalf("live log shows an earlier run:\n%s", text)
	}
	h.r.Cancel(run.ID)
	if r := h.wait(t); r.Status != StatusCanceled {
		t.Fatalf("%+v", r)
	}
}

func scheduledJob(t *testing.T, h *harness, name string, retries int) Job {
	return h.job(t, name, func(j *Job) {
		j.Scheduled, j.Enabled, j.RetryCount = true, true, retries
		j.Schedule = schedule.Spec{Type: schedule.Daily, Times: []string{"03:00"}}
	})
}

// A scheduled run that the program's stop interrupted goes on at the next
// start: once even without retries, and never endlessly.
func TestResumeAfterStop(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.r.Start(ctx)
	j := scheduledJob(t, h, "slow", 0)
	manual := h.job(t, "slow", nil)
	run, err := h.r.Enqueue(j, "ütemezett", time.Now())
	must(t, err)
	_, err = h.r.Enqueue(manual, "kézi", time.Time{})
	must(t, err)
	time.Sleep(300 * time.Millisecond)
	h.r.Stop(10 * time.Second)

	got, _ := h.st.Run(run.ID)
	if got.Status != StatusCanceled || !strings.HasPrefix(got.Message, MsgStoppedRunning) || !strings.Contains(got.Message, "újra elindul (2/2.") {
		t.Fatalf("interrupted run: %+v", got)
	}
	rt, ok := h.st.PendingRetry(j.ID)
	if !ok || rt.Attempt != 2 || rt.Of != 2 || rt.Trigger != "ütemezett" {
		t.Fatalf("continuation not planned: %+v %v", rt, ok)
	}
	if _, ok := h.st.PendingRetry(manual.ID); ok {
		t.Fatal("a manual run must not be repeated on its own")
	}

	// the next start: the run goes on as the 2nd try
	st2, err := OpenStore(h.st.Dir())
	must(t, err)
	done := make(chan Run, 4)
	r2 := &Runner{Store: st2, Command: helperCommand, unit: 100 * time.Millisecond, OnFinish: func(r Run, _ Job) { done <- r }}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	r2.Start(ctx2)
	var live Run
	for i := 0; i < 50; i++ {
		if l := r2.Live(); len(l) == 1 && l[0].Status == StatusRunning {
			live = l[0]
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if live.JobID != j.ID || live.Attempt != 2 || live.Attempts != 2 {
		t.Fatalf("not continued: %+v", live)
	}
	// stopped again: no third start
	r2.Stop(10 * time.Second)
	got, _ = st2.Run(live.ID)
	if got.Status != StatusCanceled || strings.Contains(got.Message, "újra elindul") {
		t.Fatalf("second interruption: %+v", got)
	}
	if _, ok := st2.PendingRetry(j.ID); ok {
		t.Fatal("must not continue endlessly")
	}
}

// The program or the computer stopped without a clean exit: the run left
// "running" in the history is closed and continued at the start.
func TestResumeAfterCrash(t *testing.T) {
	h := newHarness(t)
	j := scheduledJob(t, h, "ok", 2)
	queued := scheduledJob(t, h, "ok", 0)
	now := time.Now()
	must(t, h.st.PutRun(Run{ID: "r1", JobID: j.ID, JobName: j.Name, Trigger: "ütemezett", Queued: now, Started: now, Status: StatusRunning, Attempt: 1, Attempts: 3}))
	must(t, h.st.PutRun(Run{ID: "r2", JobID: queued.ID, JobName: queued.Name, Trigger: "ütemezett", Queued: now, Status: StatusQueued, Attempt: 1, Attempts: 1}))
	must(t, os.MkdirAll(filepath.Join(h.st.Dir(), "munka", "r1", "workspace"), 0o755))

	st2, err := OpenStore(h.st.Dir())
	must(t, err)
	if ents, _ := os.ReadDir(filepath.Join(st2.Dir(), "munka")); len(ents) != 0 {
		t.Fatal("temporary workspace left")
	}
	r1, _ := st2.Run("r1")
	r2, _ := st2.Run("r2")
	if r1.Status != StatusCanceled || r1.Ended.IsZero() || r2.Status != StatusCanceled || r2.Ended.IsZero() {
		t.Fatalf("not closed: %+v %+v", r1, r2)
	}
	if rt, ok := st2.PendingRetry(j.ID); !ok || rt.Attempt != 2 || rt.Of != 3 {
		t.Fatalf("running run not continued: %+v", rt)
	}
	if rt, ok := st2.PendingRetry(queued.ID); !ok || rt.Attempt != 1 {
		t.Fatalf("a run that never started must start (as the same try): %+v", rt)
	}
	// saved: a second reopen sees the same
	st3, err := OpenStore(h.st.Dir())
	must(t, err)
	if x, _ := st3.Run("r1"); x.Status != StatusCanceled || !strings.Contains(x.Message, "újra elindul") {
		t.Fatalf("not saved: %+v", x)
	}
	// and the start runs both
	done := make(chan Run, 4)
	r := &Runner{Store: st3, Command: helperCommand, unit: 100 * time.Millisecond, OnFinish: func(r Run, _ Job) { done <- r }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	for i := 0; i < 2; i++ {
		select {
		case x := <-done:
			if x.Status != StatusOK {
				t.Fatalf("%+v", x)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("not continued")
		}
	}
}
