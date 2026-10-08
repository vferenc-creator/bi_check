// Package status turns "when was it expected" + "what is on disk" into one
// of the monitor's states, with a human readable (Hungarian) explanation.
package status

import (
	"fmt"
	"sort"
	"time"

	"bimonitor/internal/checker"
	"bimonitor/internal/model"
)

// Input of an evaluation.
type Input struct {
	Now time.Time
	// Expected is the latest expected drop time (≤ Now); HasExpected=false
	// if the schedule had no occurrence yet.
	Expected    time.Time
	HasExpected bool
	// PrevExpected is the expected time before Expected (zero if none).
	PrevExpected time.Time
	Grace        time.Duration
	Early        time.Duration
	Result       checker.Result
	Rules        model.SuspiciousRules
	// Gap: daily window in which a missing/old file is not a problem.
	Gap model.GapWindow
	// RecentSizes are sizes of previous arrivals (newest first).
	RecentSizes []int64
	Loc         *time.Location
}

// Output of an evaluation.
type Output struct {
	Status model.Status
	Reason string
	// WindowStart: a file modified at or after this counts for Expected.
	WindowStart time.Time
	// Deadline = Expected + Grace.
	Deadline time.Time
	// Fresh reports whether the found file belongs to the current expectation.
	Fresh bool
	// Gap: the file is missing, but this is allowed right now (gap window).
	Gap bool
}

// FutureSkew: modification times further in the future are suspicious.
const FutureSkew = 5 * time.Minute

// WindowStart computes from when a file counts for the expectation at t:
// t - early, but never earlier than halfway since the previous expectation
// (otherwise one file could satisfy two consecutive expectations).
func WindowStart(t, prev time.Time, early time.Duration) time.Time {
	ws := t.Add(-early)
	if !prev.IsZero() {
		mid := prev.Add(t.Sub(prev) / 2)
		if ws.Before(mid) {
			ws = mid
		}
	}
	return ws
}

// Evaluate decides the status.
func Evaluate(in Input) Output {
	loc := in.Loc
	if loc == nil {
		loc = time.Local
	}
	hm := func(t time.Time) string { return fmtTime(t, in.Now, loc) }
	r := in.Result
	out := Output{}

	if r.Kind == checker.Unreachable {
		out.Status = model.StatusUnreachable
		out.Reason = r.Reason
		if in.HasExpected {
			out.Deadline = in.Expected.Add(in.Grace)
		}
		return out
	}
	if !in.HasExpected {
		out.Status = model.StatusWaiting
		out.Reason = "Még nem volt elvárt lerakási időpont."
		if r.Kind == checker.Found && r.File != nil {
			out.Reason += " A fájl jelenleg megvan (" + hm(r.File.ModTime) + ")."
		}
		return out
	}

	out.WindowStart = WindowStart(in.Expected, in.PrevExpected, in.Early)
	out.Deadline = in.Expected.Add(in.Grace)

	if r.Kind == checker.Found && r.File != nil && !r.File.ModTime.Before(out.WindowStart) {
		out.Fresh = true
		f := r.File
		delay := f.ModTime.Sub(in.Expected)
		if s := suspicious(in, f); s != "" {
			out.Status = model.StatusSuspicious
			out.Reason = s
			return out
		}
		out.Status = model.StatusOK
		switch {
		case delay > in.Grace:
			out.Reason = fmt.Sprintf("Megérkezett %s-kor, %s késéssel (elvárt: %s).", hm(f.ModTime), fmtDur(delay), hm(in.Expected))
		case delay > 0:
			out.Reason = fmt.Sprintf("Megérkezett %s-kor, a türelmi időn belül (elvárt: %s).", hm(f.ModTime), hm(in.Expected))
		default:
			out.Reason = fmt.Sprintf("Időben megérkezett: %s (elvárt: %s).", hm(f.ModTime), hm(in.Expected))
		}
		return out
	}

	var what string
	switch {
	case r.Kind == checker.Found && r.File != nil:
		what = fmt.Sprintf("A legfrissebb fájl %s-kor módosult, ez az elvárt %s előtti.", hm(r.File.ModTime), hm(in.Expected))
	case r.Reason != "":
		what = r.Reason
	default:
		what = "A fájl nem található."
	}
	if in.Gap.Contains(in.Now.In(loc)) {
		out.Status, out.Gap = model.StatusWaiting, true
		out.Reason = fmt.Sprintf("Üres ablak (%s): ilyenkor a fájl hiányozhat, %s után újra figyelem. %s", in.Gap.Describe(), in.Gap.To, what)
		return out
	}
	if in.Now.Before(out.Deadline) {
		out.Status = model.StatusLate
		out.Reason = fmt.Sprintf("Elvárt: %s, türelmi idő %s-ig. %s", hm(in.Expected), hm(out.Deadline), what)
		return out
	}
	out.Status = model.StatusMissing
	out.Reason = fmt.Sprintf("Nem érkezett meg (elvárt: %s, határidő: %s). %s", hm(in.Expected), hm(out.Deadline), what)
	return out
}

func suspicious(in Input, f *checker.FileInfo) string {
	if f.ModTime.After(in.Now.Add(FutureSkew)) {
		return fmt.Sprintf("A fájl módosítási ideje a jövőben van (%s) – órabeállítási hiba lehet a szerveren.", f.ModTime.In(locOr(in.Loc)).Format("2006.01.02. 15:04"))
	}
	if in.Rules.ZeroBytes && f.Size == 0 {
		return "A fájl 0 bájtos."
	}
	if in.Rules.MinBytes > 0 && f.Size < in.Rules.MinBytes {
		return fmt.Sprintf("A fájl mérete (%s) kisebb a megadott minimumnál (%s).", FormatSize(f.Size), FormatSize(in.Rules.MinBytes))
	}
	if in.Rules.DropPercent > 0 && len(in.RecentSizes) >= 3 {
		med := median(in.RecentSizes)
		limit := float64(med) * (1 - float64(in.Rules.DropPercent)/100)
		if med > 0 && float64(f.Size) < limit {
			return fmt.Sprintf("A fájl (%s) drasztikusan kisebb a szokásosnál (medián: %s, több mint %d%%-kal kevesebb).", FormatSize(f.Size), FormatSize(med), in.Rules.DropPercent)
		}
	}
	return ""
}

func locOr(l *time.Location) *time.Location {
	if l == nil {
		return time.Local
	}
	return l
}

func median(v []int64) int64 {
	s := append([]int64(nil), v...)
	if len(s) > 20 {
		s = s[:20]
	}
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// fmtTime prints "15:04" for today, "tegnap 15:04", or a full date.
func fmtTime(t, now time.Time, loc *time.Location) string {
	t, now = t.In(loc), now.In(loc)
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	day1 := time.Date(y1, m1, d1, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(y2, m2, d2, 0, 0, 0, 0, time.UTC)
	switch int(day2.Sub(day1).Hours() / 24) {
	case 0:
		return t.Format("15:04")
	case 1:
		return "tegnap " + t.Format("15:04")
	case -1:
		return "holnap " + t.Format("15:04")
	}
	return t.Format("2006.01.02. 15:04")
}

func fmtDur(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	m := int(d.Round(time.Minute).Minutes())
	switch {
	case m < 60:
		return fmt.Sprintf("%d perc", m)
	case m < 24*60:
		if m%60 == 0 {
			return fmt.Sprintf("%d óra", m/60)
		}
		return fmt.Sprintf("%d óra %d perc", m/60, m%60)
	}
	return fmt.Sprintf("%d nap %d óra", m/(24*60), (m%(24*60))/60)
}

// FormatSize prints a byte count in Hungarian notation.
func FormatSize(b int64) string {
	const k = 1024
	switch {
	case b < k:
		return fmt.Sprintf("%d B", b)
	case b < k*k:
		return fmt.Sprintf("%.1f KB", float64(b)/k)
	case b < k*k*k:
		return fmt.Sprintf("%.1f MB", float64(b)/(k*k))
	}
	return fmt.Sprintf("%.2f GB", float64(b)/(k*k*k))
}
