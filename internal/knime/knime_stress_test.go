package knime

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestStress hammers the runner the way a busy server and impatient users
// would: many jobs, parallel runs, random outcomes, retries, timeouts, and
// concurrent "run now", "stop" and "cancel retry" clicks plus scheduler
// ticks. It checks the invariants the whole feature relies on.
func TestStress(t *testing.T) {
	if testing.Short() {
		t.Skip("stress")
	}
	root := t.TempDir()
	st, err := OpenStore(filepath.Join(root, "knime"))
	must(t, err)
	const parallel = 3
	_, err = st.SetConfig(Config{Exe: fakeKnime(t, true), Parallel: parallel})
	must(t, err)
	var finished atomic.Int64
	r := &Runner{Store: st, Command: helperCommand, unit: 20 * time.Millisecond,
		OnFinish: func(Run, Job) { finished.Add(1) }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)

	var jobs []Job
	for i := 0; i < 12; i++ {
		j := Job{Name: "stress", Workflow: workflow(t, filepath.Join(root, "wf", string(rune('a'+i))), "rnd"),
			RetryCount: i % 3, RetryWaitMin: 1}
		if i%4 == 0 {
			j.TimeoutMin = 10 // 200 ms: the slow runs time out
		}
		j, err := st.SaveJob(j, time.Now())
		must(t, err)
		jobs = append(jobs, j)
	}

	// invariant sampler: a job never runs twice, never more than `parallel`
	stop := make(chan struct{})
	var violations []string
	var vmu sync.Mutex
	var samples atomic.Int64
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			samples.Add(1)
			running := 0
			seen := map[string]int{}
			for _, x := range r.Live() {
				seen[x.JobID]++
				if x.Status == StatusRunning {
					running++
				}
			}
			vmu.Lock()
			if running > parallel {
				violations = append(violations, "too many running")
			}
			for id, n := range seen {
				if n > 1 {
					violations = append(violations, "job twice: "+id)
				}
			}
			vmu.Unlock()
		}
	}()

	// four "users" clicking for a few seconds
	deadline := time.Now().Add(4 * time.Second)
	var wg sync.WaitGroup
	for u := 0; u < 4; u++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rnd := rand.New(rand.NewSource(seed))
			for time.Now().Before(deadline) {
				j := jobs[rnd.Intn(len(jobs))]
				switch n := rnd.Intn(10); {
				case n < 7:
					r.Enqueue(j, "kézi", time.Time{})
				case n < 8 && rnd.Intn(3) == 0: // stop clicks are rarer than run clicks
					if live := r.Live(); len(live) > 0 {
						r.Cancel(live[rnd.Intn(len(live))].ID)
					}
				case n < 9:
					r.CancelRetry(j.ID)
				default:
					r.Tick()
				}
				time.Sleep(time.Duration(rnd.Intn(15)) * time.Millisecond)
			}
		}(int64(u) + time.Now().UnixNano())
	}
	wg.Wait()

	// drain: no new retries, wait for everything to finish
	until := time.Now().Add(30 * time.Second)
	for {
		for _, j := range jobs {
			r.CancelRetry(j.ID)
		}
		if len(r.Live()) == 0 && len(st.Retries()) == 0 {
			time.Sleep(100 * time.Millisecond) // a retry timer may fire right now
			if len(r.Live()) == 0 && len(st.Retries()) == 0 {
				break
			}
		}
		if time.Now().After(until) {
			t.Fatalf("not drained: live %d, retries %d", len(r.Live()), len(st.Retries()))
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(stop)

	vmu.Lock()
	if len(violations) > 0 {
		t.Fatalf("invariant violations (%d), first: %s", len(violations), violations[0])
	}
	vmu.Unlock()
	runs := st.Runs("", 0)
	counts := map[string]int{}
	for _, x := range runs {
		counts[x.Status]++
		if x.Status == StatusQueued || x.Status == StatusRunning {
			t.Fatalf("run left unfinished: %+v", x)
		}
		if x.Status == StatusOK && (x.ExitCode == nil || *x.ExitCode != 0) {
			t.Fatalf("ok without exit 0: %+v", x)
		}
		if x.Status == StatusError && x.ExitCode != nil && *x.ExitCode == 30 && x.FailedNode == nil {
			t.Fatalf("exit 30 without the failed node: %+v", x)
		}
		if x.ExitCode != nil && *x.ExitCode == 10 && !x.NextRetry.IsZero() {
			t.Fatalf("exit 10 must not be retried: %+v", x)
		}
	}
	if counts[StatusOK] == 0 || counts[StatusError] == 0 || counts[StatusCanceled] == 0 {
		t.Fatalf("the stress did not cover the outcomes: %v", counts)
	}
	if ents, _ := os.ReadDir(filepath.Join(st.Dir(), "munka")); len(ents) != 0 {
		t.Fatalf("%d temporary workspaces left", len(ents))
	}
	// the history on disk is the same after a reopen
	st2, err := OpenStore(st.Dir())
	must(t, err)
	if n := len(st2.Runs("", 0)); n != len(runs) {
		t.Fatalf("history on disk %d, in memory %d", n, len(runs))
	}
	t.Logf("runs: %d %v · finished callbacks: %d · samples: %d", len(runs), counts, finished.Load(), samples.Load())
}
