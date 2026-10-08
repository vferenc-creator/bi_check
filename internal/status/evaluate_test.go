package status

import (
	"strings"
	"testing"
	"time"

	"bimonitor/internal/calendar"
	"bimonitor/internal/checker"
	"bimonitor/internal/model"
	"bimonitor/internal/schedule"
)

var bud, _ = time.LoadLocation("Europe/Budapest")

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, bud)
	if err != nil {
		panic(err)
	}
	return t
}

func found(mod string, size int64) checker.Result {
	return checker.Result{Kind: checker.Found, File: &checker.FileInfo{Name: "x", ModTime: at(mod), Size: size}}
}

// eval runs the full chain: schedule → expected times → status.
func eval(t *testing.T, sp schedule.Spec, now string, r checker.Result, grace, early time.Duration) Output {
	t.Helper()
	s, err := schedule.Compile(sp, calendar.Default, bud)
	if err != nil {
		t.Fatal(err)
	}
	n := at(now)
	exp, ok := s.Prev(n)
	var prev time.Time
	if ok {
		prev, _ = s.PrevBefore(exp)
	}
	return Evaluate(Input{Now: n, Expected: exp, HasExpected: ok, PrevExpected: prev, Grace: grace, Early: early,
		Result: r, Rules: model.SuspiciousRules{ZeroBytes: true, DropPercent: 70}, Loc: bud})
}

var daily6 = schedule.Spec{Type: schedule.Daily, Times: []string{"06:00"}}

func TestOKLateMissing(t *testing.T) {
	g := 30 * time.Minute
	cases := []struct {
		now, mod string
		want     model.Status
	}{
		{"2026-10-08 05:00", "2026-10-07 06:05", model.StatusOK},      // yesterday's file is fine before 6:00
		{"2026-10-08 06:10", "2026-10-07 06:05", model.StatusLate},    // within grace
		{"2026-10-08 06:30", "2026-10-07 06:05", model.StatusMissing}, // grace over (deadline inclusive)
		{"2026-10-08 06:45", "2026-10-08 06:40", model.StatusOK},      // arrived late → OK again
		{"2026-10-08 06:10", "2026-10-08 06:02", model.StatusOK},
	}
	for _, c := range cases {
		o := eval(t, daily6, c.now, found(c.mod, 1000), g, 0)
		if o.Status != c.want {
			t.Errorf("now=%s mod=%s: %s (%s), want %s", c.now, c.mod, o.Status, o.Reason, c.want)
		}
	}
}

func TestEarlyTolerance(t *testing.T) {
	// KNIME finishes at 05:50 for the 06:00 expectation.
	o := eval(t, daily6, "2026-10-08 06:20", found("2026-10-08 05:50", 10), 30*time.Minute, 0)
	if o.Status != model.StatusLate {
		t.Fatalf("without early tolerance 05:50 is too early: %s", o.Status)
	}
	o = eval(t, daily6, "2026-10-08 06:20", found("2026-10-08 05:50", 10), 30*time.Minute, 30*time.Minute)
	if o.Status != model.StatusOK {
		t.Fatalf("with 30m early tolerance: %s %s", o.Status, o.Reason)
	}
}

func TestEarlyToleranceNeverReusesPreviousFile(t *testing.T) {
	// Hourly; the 10:05 file satisfied 10:00. Huge early tolerance must not
	// let it satisfy 11:00 as well (window starts halfway: 10:30).
	h := schedule.Spec{Type: schedule.Hourly}
	o := eval(t, h, "2026-10-08 11:40", found("2026-10-08 10:05", 10), 30*time.Minute, 3*time.Hour)
	if o.Status != model.StatusMissing {
		t.Fatalf("got %s (%s)", o.Status, o.Reason)
	}
	if ws := o.WindowStart; !ws.Equal(at("2026-10-08 10:30")) {
		t.Fatalf("window start %s", ws)
	}
}

func TestGraceAcrossMidnight(t *testing.T) {
	sp := schedule.Spec{Type: schedule.Daily, Times: []string{"23:30"}}
	o := eval(t, sp, "2026-10-09 00:15", found("2026-10-08 10:00", 10), time.Hour, 0)
	if o.Status != model.StatusLate || !o.Deadline.Equal(at("2026-10-09 00:30")) {
		t.Fatalf("%s %s %s", o.Status, o.Deadline, o.Reason)
	}
	o = eval(t, sp, "2026-10-09 00:31", found("2026-10-08 10:00", 10), time.Hour, 0)
	if o.Status != model.StatusMissing {
		t.Fatalf("%s", o.Status)
	}
}

func TestGraceAcrossDST(t *testing.T) {
	// 01:30 + 2h grace on the spring-forward night ends at 04:30 CEST (2 real hours).
	sp := schedule.Spec{Type: schedule.Daily, Times: []string{"01:30"}}
	o := eval(t, sp, "2026-03-29 04:20", found("2026-03-28 01:35", 10), 2*time.Hour, 0)
	if o.Status != model.StatusLate || !o.Deadline.Equal(at("2026-03-29 04:30")) {
		t.Fatalf("%s deadline %s", o.Status, o.Deadline.In(bud))
	}
}

func TestWeekendAndHoliday(t *testing.T) {
	wd := schedule.Spec{Type: schedule.Workdays, Times: []string{"06:00"}, UseHolidays: true}
	// Saturday: Friday's file is still the expected one.
	if o := eval(t, wd, "2026-10-10 12:00", found("2026-10-09 06:10", 10), 30*time.Minute, 0); o.Status != model.StatusOK {
		t.Fatalf("weekend: %s %s", o.Status, o.Reason)
	}
	// Friday Oct 23 (holiday): Thursday's file is fine.
	if o := eval(t, wd, "2026-10-23 09:00", found("2026-10-22 06:05", 10), 30*time.Minute, 0); o.Status != model.StatusOK {
		t.Fatalf("holiday: %s %s", o.Status, o.Reason)
	}
	// Monday after: Thursday's file is now missing.
	if o := eval(t, wd, "2026-10-26 07:00", found("2026-10-22 06:05", 10), 30*time.Minute, 0); o.Status != model.StatusMissing {
		t.Fatalf("monday: %s %s", o.Status, o.Reason)
	}
}

func TestUnreachableIsNotMissing(t *testing.T) {
	r := checker.Result{Kind: checker.Unreachable, Reason: "A hálózat vagy a szerver nem érhető el."}
	o := eval(t, daily6, "2026-10-08 09:00", r, 30*time.Minute, 0)
	if o.Status != model.StatusUnreachable || !strings.Contains(o.Reason, "hálózat") {
		t.Fatalf("%s %s", o.Status, o.Reason)
	}
}

func TestNotFound(t *testing.T) {
	r := checker.Result{Kind: checker.NotFound, Reason: "A fájl nem található."}
	if o := eval(t, daily6, "2026-10-08 06:05", r, 30*time.Minute, 0); o.Status != model.StatusLate {
		t.Fatal(o.Status)
	}
	if o := eval(t, daily6, "2026-10-08 07:05", r, 30*time.Minute, 0); o.Status != model.StatusMissing {
		t.Fatal(o.Status)
	}
}

func TestSuspicious(t *testing.T) {
	g := 30 * time.Minute
	if o := eval(t, daily6, "2026-10-08 07:00", found("2026-10-08 06:01", 0), g, 0); o.Status != model.StatusSuspicious || !strings.Contains(o.Reason, "0 bájt") {
		t.Fatalf("zero bytes: %s %s", o.Status, o.Reason)
	}
	// Drastic drop vs. median of previous sizes.
	s, _ := schedule.Compile(daily6, nil, bud)
	now := at("2026-10-08 07:00")
	exp, _ := s.Prev(now)
	in := Input{Now: now, Expected: exp, HasExpected: true, Grace: g, Result: found("2026-10-08 06:01", 100_000),
		Rules: model.SuspiciousRules{DropPercent: 70}, RecentSizes: []int64{1_000_000, 1_100_000, 950_000}, Loc: bud}
	if o := Evaluate(in); o.Status != model.StatusSuspicious {
		t.Fatalf("drop: %s", o.Status)
	}
	in.Result = found("2026-10-08 06:01", 900_000)
	if o := Evaluate(in); o.Status != model.StatusOK {
		t.Fatalf("normal size: %s %s", o.Status, o.Reason)
	}
	in.Rules.MinBytes = 1_000_000
	if o := Evaluate(in); o.Status != model.StatusSuspicious {
		t.Fatalf("min bytes: %s", o.Status)
	}
	// Future timestamp.
	in.Rules = model.SuspiciousRules{}
	in.Result = found("2026-10-08 09:00", 10)
	if o := Evaluate(in); o.Status != model.StatusSuspicious {
		t.Fatalf("future: %s", o.Status)
	}
	// Stale file is never "suspicious", it's missing/late.
	in.Result = found("2026-10-07 06:01", 0)
	if o := Evaluate(in); o.Status != model.StatusMissing {
		t.Fatalf("stale zero-byte: %s", o.Status)
	}
}

func TestWaiting(t *testing.T) {
	// A monthly schedule on a leap day that has never happened within the scan range is "waiting".
	in := Input{Now: at("2026-10-08 07:00"), HasExpected: false, Result: checker.Result{Kind: checker.NotFound}, Loc: bud}
	if o := Evaluate(in); o.Status != model.StatusWaiting {
		t.Fatal(o.Status)
	}
}

// A script deletes the file at 00:30 and writes the new one at ~06:05: with
// a 00:20–06:30 gap window the night is "waiting", not "missing".
func TestGapWindow(t *testing.T) {
	gap := model.GapWindow{Enabled: true, From: "00:20", To: "06:30"}
	run := func(now string, r checker.Result) Output {
		s, _ := schedule.Compile(daily6, calendar.Default, bud)
		n := at(now)
		exp, ok := s.Prev(n)
		prev, _ := s.PrevBefore(exp)
		return Evaluate(Input{Now: n, Expected: exp, HasExpected: ok, PrevExpected: prev, Grace: 15 * time.Minute,
			Result: r, Gap: gap, Loc: bud})
	}
	missing := checker.Result{Kind: checker.NotFound, Reason: "A fájl nem található."}
	cases := []struct {
		now  string
		r    checker.Result
		want model.Status
		gap  bool
	}{
		{"2026-10-08 00:10", missing, model.StatusMissing, false}, // before the window: a real problem
		{"2026-10-08 00:45", missing, model.StatusWaiting, true},  // deleted by the script
		{"2026-10-08 06:10", missing, model.StatusWaiting, true},  // late, but still inside the window
		{"2026-10-08 06:10", found("2026-10-08 06:05", 100), model.StatusOK, false},
		{"2026-10-08 06:31", missing, model.StatusMissing, false}, // window over, deadline passed
		{"2026-10-08 03:00", checker.Result{Kind: checker.Unreachable, Reason: "x"}, model.StatusUnreachable, false},
	}
	for _, c := range cases {
		o := run(c.now, c.r)
		if o.Status != c.want || o.Gap != c.gap {
			t.Errorf("%s: %s gap=%v (%s), want %s gap=%v", c.now, o.Status, o.Gap, o.Reason, c.want, c.gap)
		}
	}
	// The window may cross midnight.
	night := model.GapWindow{Enabled: true, From: "23:00", To: "02:00"}
	for s, want := range map[string]bool{"2026-10-08 22:59": false, "2026-10-08 23:00": true, "2026-10-09 01:59": true, "2026-10-09 02:00": false} {
		if night.Contains(at(s)) != want {
			t.Errorf("Contains(%s) != %v", s, want)
		}
	}
	if (model.GapWindow{Enabled: true, From: "25:00", To: "01:00"}).Validate() == nil || (model.GapWindow{Enabled: true, From: "01:00", To: "01:00"}).Validate() == nil {
		t.Error("invalid windows accepted")
	}
}
