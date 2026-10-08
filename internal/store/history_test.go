package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHistory(t *testing.T) {
	h, err := OpenHistory(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	base := time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		exp := base.AddDate(0, 0, i)
		d := int64(60 * i)
		ok, err := h.RecordArrival(Arrival{ItemID: "a", FilePath: "x", ModTime: exp.Add(time.Duration(i) * time.Minute), Size: int64(1000 + i), Expected: &exp, DelaySec: &d, SeenAt: exp})
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	// Duplicate is ignored.
	if ok, _ := h.RecordArrival(Arrival{ItemID: "a", FilePath: "x", ModTime: base, Size: 1000, SeenAt: base}); ok {
		t.Fatal("duplicate inserted")
	}
	sz := h.RecentSizes("a", 3)
	if len(sz) != 3 || sz[0] != 1004 {
		t.Fatal(sz)
	}
	arr, err := h.Arrivals("a", base.AddDate(0, 0, 2), 100)
	if err != nil || len(arr) != 3 || arr[0].Expected == nil || *arr[0].DelaySec != 240 {
		t.Fatalf("%v %+v", err, arr)
	}
	h.RecordTransition(Transition{ItemID: "a", At: base, From: "ok", To: "missing", Reason: "r"})
	h.RecordTransition(Transition{ItemID: "b", At: base.Add(time.Hour), From: "missing", To: "ok", Reason: "r2"})
	tr, _ := h.Transitions("", base, 10)
	if len(tr) != 2 || tr[0].ItemID != "b" {
		t.Fatal(tr)
	}
	tr, _ = h.Transitions("a", base, 10)
	if len(tr) != 1 {
		t.Fatal(tr)
	}
	h.SetIncident("a", &Incident{Key: "k1", Status: "missing", At: base})
	h.SetIncident("a", &Incident{Key: "k2", Status: "missing", At: base})
	h.SetIncident("b", &Incident{Key: "k", Status: "missing", At: base})
	if in := h.Incidents(); len(in) != 2 || in["a"].Key != "k2" {
		t.Fatal(in)
	}
	h.SetMeta("x", "1")
	h.SetMeta("x", "2")
	if h.Meta("x") != "2" || h.Meta("nope") != "" {
		t.Fatal("meta")
	}
	if err := h.Prune(base.AddDate(0, 0, 3), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if arr, _ := h.Arrivals("a", time.Time{}, 100); len(arr) != 2 {
		t.Fatal("prune arrivals", len(arr))
	}
	if in := h.Incidents(); len(in) != 1 {
		t.Fatal("prune incidents", in)
	}
}
