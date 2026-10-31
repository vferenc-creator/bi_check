package app

import (
	"fmt"
	"math"
	"sync"
	"time"

	"bimonitor/internal/drift"
	"bimonitor/internal/model"
	"bimonitor/internal/schedule"
)

// DriftInfo is the gentle "it comes a little later every week" hint for one
// item. It never changes the status and never notifies.
type DriftInfo struct {
	drift.Result
	Text     string `json:"text"`
	GraceEnd string `json:"graceEnd,omitempty"` // clock time the grace period ends
}

// How often the drift hints are recomputed (arrival history changes slowly).
const driftEvery = 30 * time.Minute

type driftCache struct {
	mu  sync.Mutex
	m   map[string]*DriftInfo
	at  time.Time
	sig string
}

func (a *App) driftFor(id string) *DriftInfo {
	a.drift.mu.Lock()
	defer a.drift.mu.Unlock()
	return a.drift.m[id]
}

// refreshDrift recomputes the hints when due (or when the item list
// changed); it reports whether any hint appeared, changed or went away.
func (a *App) refreshDrift(now time.Time, force bool) bool {
	if a.History == nil {
		return false
	}
	items := a.effectiveItems(a.Settings.Get())
	sig := ""
	for _, it := range items {
		sig += it.ID + it.UpdatedAt.String() + ";"
	}
	a.drift.mu.Lock()
	due := force || a.drift.m == nil || now.Sub(a.drift.at) >= driftEvery || sig != a.drift.sig
	a.drift.mu.Unlock()
	if !due {
		return false
	}
	m := map[string]*DriftInfo{}
	for _, it := range items {
		if d := a.computeDrift(it, now); d != nil {
			m[it.ID] = d
		}
	}
	a.drift.mu.Lock()
	defer a.drift.mu.Unlock()
	changed := len(m) != len(a.drift.m)
	for id, d := range m {
		if old := a.drift.m[id]; old == nil || old.Text != d.Text {
			changed = true
		}
	}
	a.drift.m, a.drift.at, a.drift.sig = m, now, sig
	return changed
}

// computeDrift analyses the first arrival of every expectation of the
// current schedule in the last weeks; nil when there is no drift.
func (a *App) computeDrift(it model.Item, now time.Time) *DriftInfo {
	if !it.Enabled || it.Gap.Enabled {
		return nil
	}
	sc, err := schedule.Compile(it.Schedule, a.Calendar(), a.Loc)
	if err != nil {
		return nil
	}
	from := now.Add(-drift.Window)
	arr, err := a.History.Arrivals(it.ID, from.AddDate(0, 0, -1), 5000)
	if err != nil || len(arr) < drift.MinSamples {
		return nil
	}
	// Only expectations of the schedule as it is now: an edited expected
	// time must not look like a drift.
	cur := map[int64]bool{}
	for _, t := range sc.Between(from, now, 5000) {
		cur[t.UnixMilli()] = true
	}
	first := map[int64]drift.Sample{}
	for _, x := range arr {
		if x.Expected == nil || x.DelaySec == nil || !cur[x.Expected.UnixMilli()] {
			continue
		}
		k := x.Expected.UnixMilli()
		d := time.Duration(*x.DelaySec) * time.Second
		if prev, ok := first[k]; !ok || d < prev.Delay {
			first[k] = drift.Sample{Expected: *x.Expected, Delay: d}
		}
	}
	samples := make([]drift.Sample, 0, len(first))
	for _, s := range first {
		samples = append(samples, s)
	}
	r := drift.Analyze(samples, now, it.Grace(), a.Loc)
	if !r.Drifting {
		return nil
	}
	return describeDrift(r, it, a.Loc)
}

func describeDrift(r drift.Result, it model.Item, loc *time.Location) *DriftInfo {
	d := &DriftInfo{Result: r}
	weeks := fmt.Sprintf("%d hete", r.Weeks)
	slope := int(math.Round(r.SlopeMinPerWeek))
	if r.FromClock != "" {
		d.Text = fmt.Sprintf("%s egyre később érkezik: %s → %s (hetente kb. %d perccel később).", weeks, r.FromClock, r.ToClock, slope)
		if g := it.Grace(); g > 0 && len(r.Points) > 0 {
			e := r.Points[len(r.Points)-1].Expected.In(loc)
			d.GraceEnd = e.Add(g).Format("15:04")
		}
	} else {
		d.Text = fmt.Sprintf("%s egyre később érkezik: a várt időponthoz képest %s → %s (hetente kb. %d perccel később).",
			weeks, fmtLag(r.FromMin), fmtLag(r.ToMin), slope)
	}
	past := it.Grace() > 0 && r.ToMin > it.Grace().Minutes()
	switch {
	case d.GraceEnd != "" && past:
		d.Text += " A türelmi idő " + d.GraceEnd + "-ig tart – mostanában már gyakran utána érkezik."
	case past:
		d.Text += " Mostanában már gyakran a türelmi időn túl érkezik."
	case d.GraceEnd != "" && r.NearGrace:
		d.Text += " A türelmi idő " + d.GraceEnd + "-ig tart, már nincs sok tartalék."
	case d.GraceEnd != "":
		d.Text += " A türelmi idő " + d.GraceEnd + "-ig tart."
	case r.NearGrace:
		d.Text += " Már a türelmi idő nagy részét kihasználja."
	}
	return d
}

func fmtLag(min float64) string {
	m := int(math.Round(min))
	switch {
	case m < 0:
		return fmt.Sprintf("%d perccel előtte", -m)
	case m == 0:
		return "pontosan"
	default:
		return fmt.Sprintf("%d perccel utána", m)
	}
}
