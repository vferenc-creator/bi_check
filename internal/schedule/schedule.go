package schedule

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"bimonitor/internal/calendar"
)

// Schedule is a compiled, validated Spec bound to a calendar and location.
type Schedule struct {
	spec  Spec
	cal   *calendar.Calendar
	loc   *time.Location
	times []int // minutes after midnight (sorted) for day-based types
	cron  *cronExpr
	wdays [8]bool // index 1..7 (Mon..Sun)

	// hourly
	from, to, interval int
}

// maxScanDays bounds the search for the previous/next occurrence.
const (
	maxScanDays     = 400
	maxScanDaysCron = 366*8 + 2 // e.g. "0 0 29 2 1" can be years apart
)

// Compile validates a spec. cal may be nil (no holidays), loc nil = Local.
func Compile(spec Spec, cal *calendar.Calendar, loc *time.Location) (*Schedule, error) {
	if cal == nil {
		cal = calendar.Default
	}
	if loc == nil {
		loc = time.Local
	}
	s := &Schedule{spec: spec, cal: cal, loc: loc}

	parseTimes := func() error {
		if len(spec.Times) == 0 {
			return errors.New("legalább egy időpontot meg kell adni")
		}
		seen := map[int]bool{}
		for _, t := range spec.Times {
			m, err := parseClock(t)
			if err != nil {
				return err
			}
			if !seen[m] {
				seen[m] = true
				s.times = append(s.times, m)
			}
		}
		sort.Ints(s.times)
		return nil
	}
	parseWeekdays := func(required bool) error {
		for _, w := range spec.Weekdays {
			if w < 1 || w > 7 {
				return fmt.Errorf("érvénytelen nap: %d (1=hétfő … 7=vasárnap)", w)
			}
			s.wdays[w] = true
		}
		if required && len(spec.Weekdays) == 0 {
			return errors.New("legalább egy napot ki kell választani")
		}
		return nil
	}
	checkRule := func() error {
		switch spec.HolidayRule {
		case HolidayNone, HolidaySkip, HolidayNext, HolidayPrev:
			return nil
		}
		return fmt.Errorf("ismeretlen ünnepnap-szabály: %q", spec.HolidayRule)
	}

	switch spec.Type {
	case Hourly:
		s.interval = spec.IntervalMinutes
		if s.interval == 0 {
			s.interval = 60
		}
		if s.interval < 1 || s.interval > 24*60 {
			return nil, errors.New("a gyakoriság 1 perc és 24 óra között lehet")
		}
		var err error
		s.from, s.to = 0, 23*60+59
		if spec.From != "" {
			if s.from, err = parseClock(spec.From); err != nil {
				return nil, err
			}
		}
		if spec.To != "" {
			if s.to, err = parseClock(spec.To); err != nil {
				return nil, err
			}
		}
		switch spec.Days {
		case DaysAll, DaysWeekdays, DaysWorkdays:
		case DaysCustom:
			if err := parseWeekdays(true); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("ismeretlen napszűrő: %q", spec.Days)
		}
	case Daily:
		if err := parseTimes(); err != nil {
			return nil, err
		}
		if err := checkRule(); err != nil {
			return nil, err
		}
	case Weekly:
		if err := parseTimes(); err != nil {
			return nil, err
		}
		if err := parseWeekdays(true); err != nil {
			return nil, err
		}
		if err := checkRule(); err != nil {
			return nil, err
		}
	case Workdays:
		if err := parseTimes(); err != nil {
			return nil, err
		}
	case Monthly:
		if err := parseTimes(); err != nil {
			return nil, err
		}
		switch spec.MonthlyMode {
		case MonthDayN, "":
			s.spec.MonthlyMode = MonthDayN
			if spec.MonthDay < 1 || spec.MonthDay > 31 {
				return nil, errors.New("a hónap napja 1 és 31 között lehet")
			}
		case MonthNthWorkday:
			if spec.MonthDay < 1 || spec.MonthDay > 23 {
				return nil, errors.New("az N. munkanap 1 és 23 között lehet")
			}
		case MonthLastDay, MonthFirstWorkday, MonthLastWorkday:
		default:
			return nil, fmt.Errorf("ismeretlen havi mód: %q", spec.MonthlyMode)
		}
		if err := checkRule(); err != nil {
			return nil, err
		}
	case Cron:
		c, err := parseCron(spec.Cron)
		if err != nil {
			return nil, err
		}
		s.cron = c
		s.times = c.times()
		if len(s.times) == 0 {
			return nil, errors.New("a cron kifejezés egyetlen időpontot sem ad")
		}
		if err := checkRule(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("ismeretlen ütemezés típus: %q", spec.Type)
	}
	return s, nil
}

// Spec returns the (normalized) spec.
func (s *Schedule) Spec() Spec { return s.spec }

// Location returns the time zone of the schedule.
func (s *Schedule) Location() *time.Location { return s.loc }

// isoWeekday: 1 = Monday … 7 = Sunday.
func isoWeekday(d calendar.Date) int {
	wd := int(d.Weekday())
	if wd == 0 {
		return 7
	}
	return wd
}

// baseDay reports whether the schedule's own rule selects day d
// (before applying the holiday rule).
func (s *Schedule) baseDay(d calendar.Date) bool {
	switch s.spec.Type {
	case Daily:
		return true
	case Weekly:
		return s.wdays[isoWeekday(d)]
	case Workdays:
		if s.spec.UseHolidays {
			return s.cal.IsWorkday(d)
		}
		return !d.IsWeekend()
	case Monthly:
		return s.monthlyMatch(d)
	case Cron:
		return s.cron.matchDay(d.Year, d.Month, d.Day, d.Weekday())
	}
	return false
}

func (s *Schedule) monthlyMatch(d calendar.Date) bool {
	switch s.spec.MonthlyMode {
	case MonthDayN:
		day := s.spec.MonthDay
		if n := d.DaysIn(); day > n {
			day = n
		}
		return d.Day == day
	case MonthLastDay:
		return d.Day == d.DaysIn()
	case MonthFirstWorkday:
		return s.nthWorkdayOfMonth(d.Year, d.Month, 1) == d
	case MonthLastWorkday:
		for day := d.DaysIn(); day >= 1; day-- {
			x := calendar.Date{Year: d.Year, Month: d.Month, Day: day}
			if s.cal.IsWorkday(x) {
				return x == d
			}
		}
	case MonthNthWorkday:
		return s.nthWorkdayOfMonth(d.Year, d.Month, s.spec.MonthDay) == d
	}
	return false
}

func (s *Schedule) nthWorkdayOfMonth(y int, m time.Month, n int) calendar.Date {
	count := 0
	for day := 1; day <= calendar.DaysIn(y, m); day++ {
		x := calendar.Date{Year: y, Month: m, Day: day}
		if s.cal.IsWorkday(x) {
			count++
			if count == n {
				return x
			}
		}
	}
	return calendar.Date{}
}

// effectiveDay reports whether occurrences fall on day d after the holiday
// rule moved/skipped base days.
func (s *Schedule) effectiveDay(d calendar.Date) bool {
	rule := s.spec.HolidayRule
	if s.spec.Type == Workdays || s.spec.Type == Hourly ||
		(s.spec.Type == Monthly && (s.spec.MonthlyMode == MonthFirstWorkday || s.spec.MonthlyMode == MonthLastWorkday || s.spec.MonthlyMode == MonthNthWorkday)) {
		rule = HolidayNone // already working-day based
	}
	switch rule {
	case HolidayNone:
		return s.baseDay(d)
	case HolidaySkip:
		return s.baseDay(d) && s.cal.IsWorkday(d)
	case HolidayNext, HolidayPrev:
		if !s.cal.IsWorkday(d) {
			return false
		}
		if s.baseDay(d) {
			return true
		}
		step := -1
		if rule == HolidayPrev {
			step = 1
		}
		// Any base day in the run of non-working days next to d moves here.
		for i, x := 0, d.AddDays(step); i < 31 && !s.cal.IsWorkday(x); i, x = i+1, x.AddDays(step) {
			if s.baseDay(x) {
				return true
			}
		}
	}
	return false
}

func (s *Schedule) hourlyDayOK(d calendar.Date) bool {
	switch s.spec.Days {
	case DaysWeekdays:
		return !d.IsWeekend()
	case DaysWorkdays:
		return s.cal.IsWorkday(d)
	case DaysCustom:
		return s.wdays[isoWeekday(d)]
	}
	return true
}

// occurrencesOn returns the sorted, de-duplicated expected instants whose
// wall-clock date is d.
func (s *Schedule) occurrencesOn(d calendar.Date) []time.Time {
	var out []time.Time
	if s.spec.Type == Hourly {
		end := s.to
		if s.to < s.from {
			end += 24 * 60
		}
		// Sequences anchored on d (same day part) and on d-1 (after-midnight part).
		if s.hourlyDayOK(d) {
			for m := s.from; m <= end && m < 24*60; m += s.interval {
				out = append(out, s.wall(d, m))
			}
		}
		if end >= 24*60 {
			prev := d.AddDays(-1)
			if s.hourlyDayOK(prev) {
				for m := s.from; m <= end; m += s.interval {
					if m >= 24*60 {
						out = append(out, s.wall(d, m-24*60))
					}
				}
			}
		}
	} else if s.effectiveDay(d) {
		for _, m := range s.times {
			out = append(out, s.wall(d, m))
		}
	}
	if len(out) < 2 {
		return out
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	uniq := out[:1]
	for _, t := range out[1:] {
		if !t.Equal(uniq[len(uniq)-1]) {
			uniq = append(uniq, t)
		}
	}
	return uniq
}

// wall converts a wall-clock time on date d to an instant, resolving DST
// gaps (→ first instant after the gap) and overlaps (→ first occurrence).
func (s *Schedule) wall(d calendar.Date, minute int) time.Time {
	h, m := minute/60, minute%60
	t := time.Date(d.Year, d.Month, d.Day, h, m, 0, 0, s.loc)
	if t.Hour() != h || t.Minute() != m || t.Day() != d.Day {
		// Non-existent local time: walk from well before the gap to the first
		// instant whose wall clock is at or after the requested time.
		want := h*60 + m
		c := time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, s.loc)
		if want >= 180 {
			c = t.Add(-3 * time.Hour).Truncate(time.Minute)
		}
		for i := 0; i < 24*60; i++ {
			if cd := calendar.DateOf(c); cd == d && c.Hour()*60+c.Minute() >= want {
				return c
			} else if d.Before(cd) {
				return c
			}
			c = c.Add(time.Minute)
		}
		return t
	}
	for _, back := range []time.Duration{time.Hour, 30 * time.Minute} {
		if e := t.Add(-back); e.Hour() == h && e.Minute() == m && e.Day() == d.Day {
			return e // ambiguous: take the first occurrence
		}
	}
	return t
}

func (s *Schedule) scanDays() int {
	if s.spec.Type == Cron {
		return maxScanDaysCron
	}
	return maxScanDays
}

// Prev returns the latest expected instant at or before now.
func (s *Schedule) Prev(now time.Time) (time.Time, bool) {
	now = now.In(s.loc)
	d := calendar.DateOf(now)
	for i := 0; i < s.scanDays(); i++ {
		occ := s.occurrencesOn(d)
		for j := len(occ) - 1; j >= 0; j-- {
			if !occ[j].After(now) {
				return occ[j], true
			}
		}
		d = d.AddDays(-1)
	}
	return time.Time{}, false
}

// PrevBefore returns the latest expected instant strictly before t.
func (s *Schedule) PrevBefore(t time.Time) (time.Time, bool) {
	return s.Prev(t.Add(-time.Nanosecond))
}

// Next returns the earliest expected instant strictly after now.
func (s *Schedule) Next(now time.Time) (time.Time, bool) {
	now = now.In(s.loc)
	d := calendar.DateOf(now)
	for i := 0; i < s.scanDays(); i++ {
		for _, t := range s.occurrencesOn(d) {
			if t.After(now) {
				return t, true
			}
		}
		d = d.AddDays(1)
	}
	return time.Time{}, false
}

// NextN returns up to n upcoming instants after now.
func (s *Schedule) NextN(now time.Time, n int) []time.Time {
	var out []time.Time
	t := now
	for len(out) < n {
		nx, ok := s.Next(t)
		if !ok {
			break
		}
		out = append(out, nx)
		t = nx
	}
	return out
}

// Between returns all expected instants in (from, to], capped at limit.
func (s *Schedule) Between(from, to time.Time, limit int) []time.Time {
	var out []time.Time
	from, to = from.In(s.loc), to.In(s.loc)
	for d := calendar.DateOf(from); !calendar.DateOf(to).Before(d); d = d.AddDays(1) {
		for _, t := range s.occurrencesOn(d) {
			if t.After(from) && !t.After(to) {
				out = append(out, t)
				if len(out) >= limit {
					return out
				}
			}
		}
	}
	return out
}
