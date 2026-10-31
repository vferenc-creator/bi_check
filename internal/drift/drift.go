// Package drift notices when an output arrives a little later week by week
// – before it becomes late. It only looks at arrival history (delay of each
// arrival against its expected time) and is deliberately conservative: a
// single late day is noise, a steady trend is a drift.
package drift

import (
	"math"
	"sort"
	"time"
)

// Sample is one arrival: when it was expected and how late it came
// (negative = early).
type Sample struct {
	Expected time.Time
	Delay    time.Duration
}

// Point is one arrival for the small chart.
type Point struct {
	Expected time.Time `json:"expected"`
	DelayMin float64   `json:"delayMin"`
}

// Result of an analysis.
type Result struct {
	Drifting bool `json:"drifting"`
	// Typical delay at the beginning and now (minutes, median).
	FromMin float64 `json:"fromMin"`
	ToMin   float64 `json:"toMin"`
	// FromClock / ToClock: typical arrival time ("06:05"), when every
	// expectation is at the same time of day; empty otherwise.
	FromClock string `json:"fromClock,omitempty"`
	ToClock   string `json:"toClock,omitempty"`
	// SlopeMinPerWeek: how much later it comes every week (robust estimate).
	SlopeMinPerWeek float64 `json:"slopeMinPerWeek"`
	Weeks           int     `json:"weeks"`
	Samples         int     `json:"samples"`
	// NearGrace: the typical arrival now uses most of the grace period.
	NearGrace bool    `json:"nearGrace"`
	Points    []Point `json:"points"`
}

// Tuning (kept conservative on purpose – it is only a gentle hint).
const (
	Window       = 42 * 24 * time.Hour // look at the last 6 weeks
	MinSamples   = 8
	MinSpan      = 14 * 24 * time.Hour
	MinShift     = 15.0 // minutes between the typical delay then and now
	MinSlope     = 3.0  // minutes later per week
	maxSamples   = 300
	nearGraceMul = 0.7
)

// Analyze looks for a steady drift in samples (any order) up to now.
func Analyze(samples []Sample, now time.Time, grace time.Duration, loc *time.Location) Result {
	if loc == nil {
		loc = time.Local
	}
	var s []Sample
	for _, x := range samples {
		if !x.Expected.After(now) && now.Sub(x.Expected) <= Window {
			s = append(s, x)
		}
	}
	sort.Slice(s, func(i, j int) bool { return s[i].Expected.Before(s[j].Expected) })
	if len(s) > maxSamples {
		// thin out evenly (keeping the newest) so dense schedules still
		// cover the whole window
		thin := make([]Sample, 0, maxSamples)
		for i := 0; i < maxSamples; i++ {
			thin = append(thin, s[len(s)-1-(len(s)-1)*i/(maxSamples-1)])
		}
		for i, j := 0, len(thin)-1; i < j; i, j = i+1, j-1 {
			thin[i], thin[j] = thin[j], thin[i]
		}
		s = thin
	}
	res := Result{Samples: len(s), Points: make([]Point, 0, len(s))}
	for _, x := range s {
		res.Points = append(res.Points, Point{Expected: x.Expected, DelayMin: round1(x.Delay.Minutes())})
	}
	if len(s) < MinSamples || s[len(s)-1].Expected.Sub(s[0].Expected) < MinSpan {
		return res
	}
	third := len(s) / 3
	if third < 3 {
		third = 3
	}
	early, late := minutes(s[:third]), minutes(s[len(s)-third:])
	res.FromMin, res.ToMin = round1(median(early)), round1(median(late))
	res.SlopeMinPerWeek = round1(theilSen(s) * 7)
	span := s[len(s)-1].Expected.Sub(s[0].Expected)
	res.Weeks = int(math.Round(span.Hours() / 24 / 7))
	if res.Weeks < 2 {
		res.Weeks = 2
	}
	if c := sameClock(s, loc); c >= 0 {
		res.FromClock = clock(c + int(math.Round(res.FromMin)))
		res.ToClock = clock(c + int(math.Round(res.ToMin)))
	}
	res.NearGrace = grace > 0 && res.ToMin >= grace.Minutes()*nearGraceMul
	res.Drifting = res.ToMin-res.FromMin >= MinShift && res.SlopeMinPerWeek >= MinSlope && res.ToMin >= 0
	return res
}

func minutes(s []Sample) []float64 {
	out := make([]float64, len(s))
	for i, x := range s {
		out[i] = x.Delay.Minutes()
	}
	return out
}

func median(v []float64) float64 {
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	n := len(c)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

// theilSen is the median of pairwise slopes (minutes per day): robust
// against single very late or very early days.
func theilSen(s []Sample) float64 {
	var slopes []float64
	for i := 0; i < len(s); i++ {
		for j := i + 1; j < len(s); j++ {
			dx := s[j].Expected.Sub(s[i].Expected).Hours() / 24
			if dx < 0.5 {
				continue
			}
			slopes = append(slopes, (s[j].Delay.Minutes()-s[i].Delay.Minutes())/dx)
		}
	}
	return median(slopes)
}

// sameClock returns the common time of day (minutes) of the expectations,
// or -1 if they differ.
func sameClock(s []Sample, loc *time.Location) int {
	c := -1
	for _, x := range s {
		t := x.Expected.In(loc)
		m := t.Hour()*60 + t.Minute()
		if c >= 0 && m != c {
			return -1
		}
		c = m
	}
	return c
}

func clock(m int) string {
	m = ((m % 1440) + 1440) % 1440
	return time.Date(2000, 1, 1, m/60, m%60, 0, 0, time.UTC).Format("15:04")
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
