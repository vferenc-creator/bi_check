package engine

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bimonitor/internal/calendar"
	"bimonitor/internal/checker"
	"bimonitor/internal/model"
	"bimonitor/internal/schedule"
)

var bud, _ = time.LoadLocation("Europe/Budapest")

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

func TestEngineLifecycle(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "export.csv")
	clk := &fakeClock{t: time.Date(2026, 10, 8, 5, 0, 0, 0, bud)}

	var mu sync.Mutex
	var events []Event
	e := New(Config{
		Checker:  checker.New(nil, checker.Options{Now: clk.Now}),
		Loc:      bud,
		Now:      clk.Now,
		OnEvents: func(ev []Event) { mu.Lock(); events = append(events, ev...); mu.Unlock() },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	it := model.NewItem()
	it.Name = "Export"
	it.Path = file
	it.Schedule = schedule.Spec{Type: schedule.Daily, Times: []string{"06:00"}}
	it.GraceMinutes = 30
	it.EarlyMinutes = 0

	// Yesterday's file exists → OK before 06:00.
	os.WriteFile(file, []byte("data"), 0o644)
	os.Chtimes(file, time.Now(), time.Date(2026, 10, 7, 6, 3, 0, 0, bud))
	e.Configure([]model.Item{it}, calendar.Default, time.Minute, 2)
	<-e.CheckNow()
	if st, _ := e.State(it.ID); st.Status != model.StatusOK {
		t.Fatalf("05:00: %s %s", st.Status, st.Reason)
	}

	// 06:10: still yesterday's file → late.
	clk.Set(time.Date(2026, 10, 8, 6, 10, 0, 0, bud))
	<-e.CheckNow(it.ID)
	if st, _ := e.State(it.ID); st.Status != model.StatusLate {
		t.Fatalf("06:10: %s", st.Status)
	}
	// 06:31: missing.
	clk.Set(time.Date(2026, 10, 8, 6, 31, 0, 0, bud))
	<-e.CheckNow(it.ID)
	st, _ := e.State(it.ID)
	if st.Status != model.StatusMissing {
		t.Fatalf("06:31: %s", st.Status)
	}
	// File arrives at 06:40 → OK, with an arrival event 40 minutes late.
	clk.Set(time.Date(2026, 10, 8, 6, 45, 0, 0, bud))
	os.Chtimes(file, time.Now(), time.Date(2026, 10, 8, 6, 40, 0, 0, bud))
	<-e.CheckNow(it.ID)
	if st, _ := e.State(it.ID); st.Status != model.StatusOK || !st.Fresh {
		t.Fatalf("06:45: %s", st.Status)
	}

	mu.Lock()
	defer mu.Unlock()
	var transitions []string
	var arrivals []Event
	for _, ev := range events {
		if ev.Kind == EvTransition {
			transitions = append(transitions, string(ev.New.Status))
		} else {
			arrivals = append(arrivals, ev)
		}
	}
	want := []string{"ok", "late", "missing", "ok"}
	if len(transitions) != len(want) {
		t.Fatalf("transitions %v, want %v", transitions, want)
	}
	for i := range want {
		if transitions[i] != want[i] {
			t.Fatalf("transitions %v, want %v", transitions, want)
		}
	}
	if len(arrivals) != 2 || !arrivals[0].Initial || arrivals[1].Delay != 40*time.Minute {
		t.Fatalf("arrivals: %+v", arrivals)
	}
}

func TestEngineDisabledAndReconfigure(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 10, 8, 12, 0, 0, 0, bud)}
	e := New(Config{Checker: checker.New(nil, checker.Options{Now: clk.Now}), Loc: bud, Now: clk.Now})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	it := model.NewItem()
	it.Path = filepath.Join(t.TempDir(), "x.csv")
	it.Enabled = false
	e.Configure([]model.Item{it}, nil, time.Minute, 1)
	if st, _ := e.State(it.ID); st.Status != model.StatusDisabled {
		t.Fatal(st.Status)
	}
	it.Enabled = true
	e.Configure([]model.Item{it}, nil, time.Minute, 1)
	<-e.CheckNow(it.ID)
	if st, _ := e.State(it.ID); st.Status != model.StatusMissing {
		t.Fatalf("%s %s", st.Status, st.Reason)
	}
	// Bad schedule is reported, not checked.
	it.Schedule = schedule.Spec{Type: schedule.Cron, Cron: "bad"}
	e.Configure([]model.Item{it}, nil, time.Minute, 1)
	if st, _ := e.State(it.ID); st.ScheduleError == "" {
		t.Fatal("expected schedule error")
	}
	// Removing the item.
	e.Configure(nil, nil, time.Minute, 1)
	if _, ok := e.State(it.ID); ok {
		t.Fatal("item should be gone")
	}
}

func TestEngineChecksAtExpectedTimeAutomatically(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "h.csv")
	os.WriteFile(file, []byte("1"), 0o644)
	os.Chtimes(file, time.Now(), time.Date(2026, 10, 8, 9, 1, 0, 0, bud))
	clk := &fakeClock{t: time.Date(2026, 10, 8, 9, 30, 0, 0, bud)}
	e := New(Config{Checker: checker.New(nil, checker.Options{Now: clk.Now}), Loc: bud, Now: clk.Now})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	it := model.NewItem()
	it.Path = file
	it.Schedule = schedule.Spec{Type: schedule.Hourly}
	it.EarlyMinutes = 0
	e.Configure([]model.Item{it}, nil, time.Hour, 1) // long periodic interval
	<-e.CheckNow()
	if st, _ := e.State(it.ID); st.Status != model.StatusOK {
		t.Fatal(st.Status)
	}
	// Crossing 10:00 must trigger a check without waiting for the interval.
	clk.Set(time.Date(2026, 10, 8, 10, 0, 5, 0, bud))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st, _ := e.State(it.ID); st.Status == model.StatusLate {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	st, _ := e.State(it.ID)
	t.Fatalf("expected an automatic check at 10:00 → late, got %s", st.Status)
}
