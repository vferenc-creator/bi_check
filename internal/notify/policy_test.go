package notify

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bimonitor/internal/engine"
	"bimonitor/internal/model"
	"bimonitor/internal/store"
)

var bud, _ = time.LoadLocation("Europe/Budapest")

func at(s string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04", s, bud)
	return t
}

func settings() model.Settings {
	s := model.DefaultSettings()
	s.Notifications.Quiet = model.QuietHours{Enabled: true, From: "22:00", To: "06:00"}
	return s
}

func item(name string) model.Item {
	it := model.NewItem()
	it.Name = name
	return it
}

func ev(it model.Item, from, to model.Status, exp time.Time) engine.Event {
	return engine.Event{Kind: engine.EvTransition, Item: it,
		Old: engine.ItemState{ID: it.ID, Status: from}, New: engine.ItemState{ID: it.ID, Status: to, Expected: exp, Reason: "ok"}}
}

func TestOneNotificationPerIncidentAndRecovery(t *testing.T) {
	p := NewPolicy(nil)
	s := settings()
	it := item("Riport")
	exp := at("2026-10-08 06:00")
	n := p.OnTransition(ev(it, model.StatusLate, model.StatusMissing, exp), s, false, at("2026-10-08 06:31"))
	if len(n) != 1 || !n[0].Toast || n[0].Kind != KindProblem {
		t.Fatalf("%+v", n)
	}
	// Network flaps: unreachable is a new problem (one notice), but going
	// back to missing for the same expectation must stay silent.
	if n := p.OnTransition(ev(it, model.StatusMissing, model.StatusUnreachable, exp), s, false, at("2026-10-08 06:40")); len(n) != 1 {
		t.Fatalf("unreachable: %+v", n)
	}
	if n := p.OnTransition(ev(it, model.StatusUnreachable, model.StatusMissing, exp), s, false, at("2026-10-08 06:45")); len(n) != 0 {
		t.Fatal("same incident notified twice")
	}
	if n := p.OnTransition(ev(it, model.StatusMissing, model.StatusUnreachable, exp), s, false, at("2026-10-08 06:50")); len(n) != 0 {
		t.Fatal("flapping must not spam")
	}
	n = p.OnTransition(ev(it, model.StatusMissing, model.StatusOK, exp), s, false, at("2026-10-08 07:00"))
	if len(n) != 1 || n[0].Kind != KindRecovery {
		t.Fatalf("recovery: %+v", n)
	}
	// OK → OK-ish transitions without incident: nothing.
	if n := p.OnTransition(ev(it, model.StatusLate, model.StatusOK, exp), s, false, at("2026-10-09 06:05")); len(n) != 0 {
		t.Fatal("no recovery without a notified problem")
	}
}

func TestNextDaysMissingIsNewIncident(t *testing.T) {
	p := NewPolicy(nil)
	s := settings()
	it := item("R")
	p.OnTransition(ev(it, model.StatusLate, model.StatusMissing, at("2026-10-08 06:00")), s, false, at("2026-10-08 07:00"))
	n := p.OnTransition(ev(it, model.StatusLate, model.StatusMissing, at("2026-10-09 06:00")), s, false, at("2026-10-09 07:00"))
	if len(n) != 1 {
		t.Fatal("a new expectation that is missing again must notify")
	}
}

func TestIncidentsSurviveRestart(t *testing.T) {
	h, err := store.OpenHistory(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	s := settings()
	it := item("R")
	exp := at("2026-10-08 06:00")
	NewPolicy(h).OnTransition(ev(it, model.StatusLate, model.StatusMissing, exp), s, false, at("2026-10-08 07:00"))
	// Restart: the engine sees "unknown → missing" for the same expectation.
	p2 := NewPolicy(h)
	if n := p2.OnTransition(ev(it, model.StatusUnknown, model.StatusMissing, exp), s, false, at("2026-10-08 08:00")); len(n) != 0 {
		t.Fatal("must not repeat after restart")
	}
	if n := p2.OnTransition(ev(it, model.StatusMissing, model.StatusOK, exp), s, false, at("2026-10-08 08:10")); len(n) != 1 {
		t.Fatal("recovery after restart expected")
	}
}

func TestMuteAckAndLate(t *testing.T) {
	p := NewPolicy(nil)
	s := settings()
	it := item("R")
	it.Notify = false
	if n := p.OnTransition(ev(it, model.StatusOK, model.StatusMissing, at("2026-10-08 06:00")), s, false, at("2026-10-08 07:00")); len(n) != 0 {
		t.Fatal("item notifications off")
	}
	it2 := item("S")
	if n := p.OnTransition(ev(it2, model.StatusOK, model.StatusMissing, at("2026-10-08 06:00")), s, true, at("2026-10-08 07:00")); len(n) != 0 {
		t.Fatal("muted shared item")
	}
	it3 := item("L")
	if n := p.OnTransition(ev(it3, model.StatusOK, model.StatusLate, at("2026-10-08 06:00")), s, false, at("2026-10-08 06:05")); len(n) != 0 {
		t.Fatal("late is silent by default")
	}
	s.Notifications.OnLate = true
	if n := p.OnTransition(ev(it3, model.StatusOK, model.StatusLate, at("2026-10-08 06:00")), s, false, at("2026-10-08 06:05")); len(n) != 1 {
		t.Fatal("late should notify when enabled")
	}
	// Escalation late → missing is a new notice.
	if n := p.OnTransition(ev(it3, model.StatusLate, model.StatusMissing, at("2026-10-08 06:00")), s, false, at("2026-10-08 06:31")); len(n) != 1 {
		t.Fatal("escalation should notify")
	}
	acked := ev(item("A"), model.StatusOK, model.StatusMissing, at("2026-10-08 06:00"))
	acked.New.Acked = true
	if n := p.OnTransition(acked, s, false, at("2026-10-08 07:00")); len(n) != 0 {
		t.Fatal("acknowledged")
	}
}

func TestQuietHoursAndSummary(t *testing.T) {
	p := NewPolicy(nil)
	s := settings()
	a, b := item("A"), item("B")
	exp := at("2026-10-08 02:00")
	n := p.OnTransition(ev(a, model.StatusLate, model.StatusMissing, exp), s, false, at("2026-10-08 03:00"))
	if len(n) != 1 || n[0].Toast {
		t.Fatalf("quiet: no toast expected %+v", n)
	}
	p.OnTransition(ev(b, model.StatusLate, model.StatusMissing, exp), s, false, at("2026-10-08 03:10"))
	// B recovers during the night: no toast, not in the summary.
	if n := p.OnTransition(ev(b, model.StatusMissing, model.StatusOK, exp), s, false, at("2026-10-08 04:00")); len(n) != 0 && n[0].Toast {
		t.Fatal("no recovery toast at night")
	}
	states := []engine.ItemState{{ID: a.ID, Status: model.StatusMissing}, {ID: b.ID, Status: model.StatusOK}}
	names := map[string]string{a.ID: "A", b.ID: "B"}
	if n := p.Tick(s, at("2026-10-08 05:00"), states, names); n != nil {
		t.Fatal("still quiet")
	}
	n = p.Tick(s, at("2026-10-08 06:00"), states, names)
	if len(n) != 1 || n[0].Kind != KindQuietSummary || len(n[0].Items) != 1 {
		t.Fatalf("summary: %+v", n)
	}
	ts := RenderToasts(n)
	if len(ts) != 1 || !strings.Contains(ts[0].Title, "1 hiányzik") {
		t.Fatalf("%+v", ts)
	}
	if n := p.Tick(s, at("2026-10-08 06:01"), states, names); n != nil {
		t.Fatal("summary only once")
	}
	// A recovers in the morning: the summary already mentioned it, so a
	// recovery notice is fine and expected.
	if n := p.OnTransition(ev(a, model.StatusMissing, model.StatusOK, exp), s, false, at("2026-10-08 07:00")); len(n) != 1 || !n[0].Toast {
		t.Fatalf("morning recovery: %+v", n)
	}
}

func TestInQuiet(t *testing.T) {
	q := model.QuietHours{Enabled: true, From: "22:00", To: "06:00"}
	for s, want := range map[string]bool{"2026-10-08 21:59": false, "2026-10-08 22:00": true, "2026-10-08 23:30": true, "2026-10-09 05:59": true, "2026-10-09 06:00": false} {
		if InQuiet(q, at(s)) != want {
			t.Errorf("%s: want %v", s, want)
		}
	}
	day := model.QuietHours{Enabled: true, From: "12:00", To: "13:00"}
	if !InQuiet(day, at("2026-10-08 12:30")) || InQuiet(day, at("2026-10-08 13:00")) {
		t.Error("daytime window")
	}
	we := model.QuietHours{Weekends: true}
	if !InQuiet(we, at("2026-10-10 12:00")) || InQuiet(we, at("2026-10-09 12:00")) {
		t.Error("weekends")
	}
	s := settings()
	s.Notifications.PausedUntil = at("2026-10-08 15:00")
	if ToastsAllowed(s, at("2026-10-08 14:00")) || !ToastsAllowed(s, at("2026-10-08 15:00")) {
		t.Error("pause")
	}
}

func TestRenderGrouping(t *testing.T) {
	mk := func(name, server string, st model.Status, srvProblem bool) Notice {
		it := item(name)
		return Notice{Kind: KindProblem, Item: it, Status: st, Server: server, ServerProblem: srvProblem, Toast: true, Reason: "A szerver nem válaszolt."}
	}
	ns := []Notice{
		mk("A", "EFS-FSRHQ", model.StatusUnreachable, true),
		mk("B", "EFS-FSRHQ", model.StatusUnreachable, true),
		mk("C", "EFS-FSRHQ", model.StatusUnreachable, true),
		mk("D", "EF-BI", model.StatusMissing, false),
	}
	ts := RenderToasts(ns)
	if len(ts) != 2 || !strings.Contains(ts[0].Title, "EFS-FSRHQ nem érhető el (3 elem)") || !strings.HasPrefix(ts[1].Title, "Hiányzik: D") {
		t.Fatalf("%+v", ts)
	}
	ns = append(ns, mk("E", "X", model.StatusSuspicious, false))
	ts = RenderToasts(ns)
	if len(ts) != 2 || ts[1].Title != "2 új probléma" {
		t.Fatalf("%+v", ts)
	}
}

// Entering the allowed gap window is neither a recovery nor a problem.
func TestGapWaitingKeepsIncident(t *testing.T) {
	p := NewPolicy(nil)
	s := settings()
	s.Notifications.Quiet.Enabled = false
	it := item("Riport")
	exp := at("2026-10-08 06:00")
	if n := p.OnTransition(ev(it, model.StatusLate, model.StatusMissing, exp), s, false, at("2026-10-08 06:31")); len(n) != 1 {
		t.Fatalf("%+v", n)
	}
	gap := ev(it, model.StatusMissing, model.StatusWaiting, exp)
	gap.New.Gap = true
	if n := p.OnTransition(gap, s, false, at("2026-10-09 00:30")); len(n) != 0 {
		t.Fatalf("gap must not report a recovery: %+v", n)
	}
	if n := p.OnTransition(ev(it, model.StatusWaiting, model.StatusOK, at("2026-10-09 06:00")), s, false, at("2026-10-09 06:05")); len(n) != 1 || n[0].Kind != KindRecovery {
		t.Fatalf("real recovery expected: %+v", n)
	}
}
