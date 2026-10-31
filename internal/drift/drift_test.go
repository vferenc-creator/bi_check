package drift

import (
	"math/rand"
	"testing"
	"time"
)

var bud, _ = time.LoadLocation("Europe/Budapest")

// daily 06:00 expectations over n days, delay(i) minutes
func series(n int, delay func(i int) float64) ([]Sample, time.Time) {
	start := time.Date(2026, 9, 21, 6, 0, 0, 0, bud)
	var s []Sample
	for i := 0; i < n; i++ {
		d := start.AddDate(0, 0, i)
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		s = append(s, Sample{Expected: d, Delay: time.Duration(delay(i) * float64(time.Minute))})
	}
	return s, start.AddDate(0, 0, n)
}

func TestSteadyDrift(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	// 06:05 at first, 1.5 minutes later every day, with ±4 minutes of noise
	s, now := series(28, func(i int) float64 { return 5 + 1.5*float64(i) + r.Float64()*8 - 4 })
	res := Analyze(s, now, 30*time.Minute, bud)
	if !res.Drifting || res.FromClock == "" || res.ToClock <= res.FromClock || res.SlopeMinPerWeek < 5 {
		t.Fatalf("%+v", res)
	}
	if !res.NearGrace {
		t.Fatalf("now ~45 min late with 30 min grace: %+v", res)
	}
	t.Logf("%s → %s, %.1f perc/hét, %d hét", res.FromClock, res.ToClock, res.SlopeMinPerWeek, res.Weeks)
}

func TestNoDriftOnNoiseOrSingleOutliers(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	s, now := series(35, func(i int) float64 {
		if i == 30 || i == 32 {
			return 90 // two very late days near the end
		}
		return 5 + r.Float64()*10 - 5
	})
	if res := Analyze(s, now, 30*time.Minute, bud); res.Drifting {
		t.Fatalf("outliers are not a drift: %+v", res)
	}
}

func TestTooLittleHistory(t *testing.T) {
	s, now := series(9, func(i int) float64 { return float64(10 * i) })
	if res := Analyze(s, now, 30*time.Minute, bud); res.Drifting {
		t.Fatalf("less than two weeks of data: %+v", res)
	}
}

func TestEarlyButLaterIsNotAWarning(t *testing.T) {
	// comes 60 → 30 minutes early: later, but still well before the time
	s, now := series(28, func(i int) float64 { return -60 + 1.2*float64(i) })
	if res := Analyze(s, now, 30*time.Minute, bud); res.Drifting {
		t.Fatalf("still early: %+v", res)
	}
}

func TestGettingEarlierIsNoDrift(t *testing.T) {
	s, now := series(28, func(i int) float64 { return 40 - 1.5*float64(i) })
	if res := Analyze(s, now, 30*time.Minute, bud); res.Drifting {
		t.Fatalf("%+v", res)
	}
}

// Every 30 minutes, 06–20 on weekdays for 5 weeks: far more than the sample
// cap, still the whole period is used.
func TestDenseScheduleKeepsWholeWindow(t *testing.T) {
	start := time.Date(2026, 9, 21, 6, 0, 0, 0, bud)
	var s []Sample
	for d := 0; d < 35; d++ {
		day := start.AddDate(0, 0, d)
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		for k := 0; k < 29; k++ {
			s = append(s, Sample{Expected: day.Add(time.Duration(k) * 30 * time.Minute), Delay: time.Duration(2+0.8*float64(d)) * time.Minute})
		}
	}
	res := Analyze(s, start.AddDate(0, 0, 35), 15*time.Minute, bud)
	if !res.Drifting || res.Weeks < 4 || res.FromClock != "" || len(res.Points) != maxSamples {
		t.Fatalf("%+v", res)
	}
	if !res.Points[0].Expected.Equal(s[0].Expected) || !res.Points[len(res.Points)-1].Expected.Equal(s[len(s)-1].Expected) {
		t.Fatal("thinning must keep the first and the newest sample")
	}
}
