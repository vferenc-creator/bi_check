package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bimonitor/internal/model"
	"bimonitor/internal/schedule"
	"bimonitor/internal/store"
)

// A daily 06:00 report that arrives a bit later every day shows the gentle
// hint (text with clock times); arrivals that
// belong to an older schedule (07:00) are ignored.
func TestDriftHint(t *testing.T) {
	a, _, dir := newTestApp(t)
	it := model.NewItem()
	it.Name = "Napi export"
	it.Path = filepath.Join(dir, "export.csv")
	it.Schedule = schedule.Spec{Type: schedule.Daily, Times: []string{"06:00"}}
	it.GraceMinutes = 45
	saved := call[SaveResult](t, a, "saveItem", it)
	id := saved.View.Item.ID

	// pretend the item exists for long
	if _, err := a.Settings.Update(func(s *model.Settings) error {
		for i := range s.Items {
			s.Items[i].CreatedAt = time.Now().AddDate(0, -3, 0)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().In(a.Loc)
	day0 := time.Date(now.Year(), now.Month(), now.Day(), 6, 0, 0, 0, a.Loc).AddDate(0, 0, -28)
	rec := func(exp time.Time, delayMin int) {
		d := int64(delayMin * 60)
		e := exp
		mt := exp.Add(time.Duration(delayMin) * time.Minute)
		if _, err := a.History.RecordArrival(store.Arrival{ItemID: id, FilePath: fmt.Sprintf("x%d", exp.Unix()), ModTime: mt, Size: 1,
			Expected: &e, DelaySec: &d, SeenAt: mt}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 28; i++ {
		exp := day0.AddDate(0, 0, i)
		rec(exp, 5+i*5/4+[]int{0, 3, -2, 1}[i%4])
		// an old schedule (07:00) with wildly varying delays must not count
		rec(exp.Add(time.Hour), []int{-50, 80, 0, 40}[i%4])
	}

	if !a.refreshDrift(time.Now(), true) {
		t.Fatal("expected a hint to appear")
	}
	v := a.view(id)
	if v.Drift == nil {
		t.Fatal("no drift hint")
	}
	if !strings.Contains(v.Drift.Text, "egyre később") || v.Drift.FromClock == "" || v.Drift.ToClock <= v.Drift.FromClock || v.Drift.GraceEnd != "06:45" {
		t.Fatalf("%+v", v.Drift)
	}
	t.Log(v.Drift.Text)
	if a.refreshDrift(time.Now(), false) {
		t.Fatal("no change expected without new data")
	}
}

func TestNoDriftWhenSteady(t *testing.T) {
	a, _, dir := newTestApp(t)
	it := model.NewItem()
	it.Name = "Stabil"
	it.Path = filepath.Join(dir, "s.csv")
	it.Schedule = schedule.Spec{Type: schedule.Daily, Times: []string{"06:00"}}
	id := call[SaveResult](t, a, "saveItem", it).View.Item.ID
	now := time.Now().In(a.Loc)
	day0 := time.Date(now.Year(), now.Month(), now.Day(), 6, 0, 0, 0, a.Loc).AddDate(0, 0, -28)
	for i := 0; i < 28; i++ {
		e := day0.AddDate(0, 0, i)
		d := int64([]int{5, 9, 2, 12, 6}[i%5] * 60)
		a.History.RecordArrival(store.Arrival{ItemID: id, FilePath: fmt.Sprint(i), ModTime: e.Add(time.Duration(d) * time.Second), Size: 1, Expected: &e, DelaySec: &d, SeenAt: e})
	}
	a.refreshDrift(time.Now(), true)
	if d := a.view(id).Drift; d != nil {
		t.Fatalf("steady arrivals: %+v", d)
	}
}
