// Package calendar knows which days are working days in Hungary.
//
// Built in:
//   - fixed public holidays (Mt. 102. §): jan. 1., márc. 15., máj. 1.,
//     aug. 20., okt. 23., nov. 1., dec. 25–26.
//   - Easter based: nagypéntek, húsvéthétfő, pünkösdhétfő
//     (húsvét- és pünkösdvasárnap are Sundays anyway)
//   - the yearly "munkanap-áthelyezés" (bridge days) from the NGM decree for
//     the years listed in transfers.go. These change every year and MUST be
//     checked; the user can add/remove days in the settings, which take
//     precedence over everything built in.
package calendar

import (
	"fmt"
	"sort"
	"time"
)

// Date is a civil date without time zone.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// DateOf returns the calendar date of t in its own location.
func DateOf(t time.Time) Date {
	y, m, d := t.Date()
	return Date{y, m, d}
}

// ParseDate parses YYYY-MM-DD.
func ParseDate(s string) (Date, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return Date{}, fmt.Errorf("érvénytelen dátum: %q (ÉÉÉÉ-HH-NN formátum kell)", s)
	}
	return DateOf(t), nil
}

func (d Date) t() time.Time { return time.Date(d.Year, d.Month, d.Day, 12, 0, 0, 0, time.UTC) }

// AddDays returns d shifted by n days.
func (d Date) AddDays(n int) Date { return DateOf(d.t().AddDate(0, 0, n)) }

// Weekday of the date.
func (d Date) Weekday() time.Weekday { return d.t().Weekday() }

// String formats as YYYY-MM-DD.
func (d Date) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day) }

// Before reports d < o.
func (d Date) Before(o Date) bool { return d.t().Before(o.t()) }

// DaysIn returns the number of days in the month of d.
func (d Date) DaysIn() int { return DaysIn(d.Year, d.Month) }

// DaysIn returns the number of days in a month.
func DaysIn(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 12, 0, 0, 0, time.UTC).Day()
}

// IsWeekend reports Saturday or Sunday.
func (d Date) IsWeekend() bool {
	wd := d.Weekday()
	return wd == time.Saturday || wd == time.Sunday
}

// Kind of a special day.
type Kind string

const (
	KindHoliday Kind = "holiday" // public holiday (munkaszüneti nap)
	KindRestDay Kind = "rest"    // transferred rest day (áthelyezett pihenőnap)
	KindWorkday Kind = "work"    // transferred working day, usually a Saturday
)

// Day is a special day in the calendar.
type Day struct {
	Date    Date   `json:"-"`
	DateStr string `json:"date"`
	Kind    Kind   `json:"kind"`
	Name    string `json:"name"`
	BuiltIn bool   `json:"builtIn"`
}

// Overrides are the user's own corrections.
type Overrides struct {
	// RestDays: extra non-working days (YYYY-MM-DD).
	RestDays []string `json:"restDays,omitempty"`
	// WorkDays: extra working days, e.g. a working Saturday.
	WorkDays []string `json:"workDays,omitempty"`
	// Ignore: built-in special days to ignore (YYYY-MM-DD).
	Ignore []string `json:"ignore,omitempty"`
}

// Calendar answers working-day questions.
type Calendar struct {
	rest   map[Date]bool
	work   map[Date]bool
	ignore map[Date]bool
}

// New builds a calendar with the user's overrides. Invalid dates are skipped.
func New(o Overrides) *Calendar {
	c := &Calendar{rest: map[Date]bool{}, work: map[Date]bool{}, ignore: map[Date]bool{}}
	add := func(m map[Date]bool, list []string) {
		for _, s := range list {
			if d, err := ParseDate(s); err == nil {
				m[d] = true
			}
		}
	}
	add(c.rest, o.RestDays)
	add(c.work, o.WorkDays)
	add(c.ignore, o.Ignore)
	return c
}

// Default is a calendar without user overrides.
var Default = New(Overrides{})

// Easter returns Easter Sunday (Gregorian, anonymous algorithm).
func Easter(year int) Date {
	a := year % 19
	b := year / 100
	c := year % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := (h + l - 7*m + 114) / 31
	day := (h+l-7*m+114)%31 + 1
	return Date{year, time.Month(month), day}
}

// builtinDays returns the built-in special days of a year.
func builtinDays(year int) []Day {
	e := Easter(year)
	days := []Day{
		{Date: Date{year, 1, 1}, Kind: KindHoliday, Name: "Újév"},
		{Date: Date{year, 3, 15}, Kind: KindHoliday, Name: "Nemzeti ünnep"},
		{Date: e.AddDays(-2), Kind: KindHoliday, Name: "Nagypéntek"},
		{Date: e, Kind: KindHoliday, Name: "Húsvétvasárnap"},
		{Date: e.AddDays(1), Kind: KindHoliday, Name: "Húsvéthétfő"},
		{Date: Date{year, 5, 1}, Kind: KindHoliday, Name: "A munka ünnepe"},
		{Date: e.AddDays(49), Kind: KindHoliday, Name: "Pünkösdvasárnap"},
		{Date: e.AddDays(50), Kind: KindHoliday, Name: "Pünkösdhétfő"},
		{Date: Date{year, 8, 20}, Kind: KindHoliday, Name: "Államalapítás ünnepe"},
		{Date: Date{year, 10, 23}, Kind: KindHoliday, Name: "Nemzeti ünnep"},
		{Date: Date{year, 11, 1}, Kind: KindHoliday, Name: "Mindenszentek"},
		{Date: Date{year, 12, 25}, Kind: KindHoliday, Name: "Karácsony"},
		{Date: Date{year, 12, 26}, Kind: KindHoliday, Name: "Karácsony másnapja"},
	}
	for _, t := range transfers[year] {
		d, err := ParseDate(t.date)
		if err != nil {
			continue
		}
		days = append(days, Day{Date: d, Kind: t.kind, Name: t.name})
	}
	for i := range days {
		days[i].BuiltIn = true
	}
	return days
}

func (c *Calendar) builtin(d Date) (Day, bool) {
	if c.ignore[d] {
		return Day{}, false
	}
	// Transfers take precedence over fixed holidays on the same date.
	var found Day
	ok := false
	for _, b := range builtinDays(d.Year) {
		if b.Date == d {
			if !ok || b.Kind != KindHoliday {
				found, ok = b, true
			}
		}
	}
	return found, ok
}

// IsWorkday reports whether d is a working day in Hungary.
func (c *Calendar) IsWorkday(d Date) bool {
	if c.work[d] {
		return true
	}
	if c.rest[d] {
		return false
	}
	if b, ok := c.builtin(d); ok {
		return b.Kind == KindWorkday
	}
	return !d.IsWeekend()
}

// Describe returns why d is special ("" for an ordinary day).
func (c *Calendar) Describe(d Date) string {
	switch {
	case c.work[d]:
		return "Áthelyezett munkanap (saját beállítás)"
	case c.rest[d]:
		return "Pihenőnap (saját beállítás)"
	}
	if b, ok := c.builtin(d); ok {
		return b.Name
	}
	return ""
}

// Year lists every special day of a year (built-in and user-defined).
func (c *Calendar) Year(year int) []Day {
	byDate := map[Date]Day{}
	for _, b := range builtinDays(year) {
		if c.ignore[b.Date] {
			continue
		}
		if prev, ok := byDate[b.Date]; ok && prev.Kind != KindHoliday {
			continue
		}
		byDate[b.Date] = b
	}
	for d := range c.rest {
		if d.Year == year {
			byDate[d] = Day{Date: d, Kind: KindRestDay, Name: "Pihenőnap (saját)"}
		}
	}
	for d := range c.work {
		if d.Year == year {
			byDate[d] = Day{Date: d, Kind: KindWorkday, Name: "Munkanap (saját)"}
		}
	}
	out := make([]Day, 0, len(byDate))
	for _, d := range byDate {
		d.DateStr = d.Date.String()
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out
}

// KnownTransferYears lists the years with built-in bridge-day data.
func KnownTransferYears() []int {
	ys := make([]int, 0, len(transfers))
	for y := range transfers {
		ys = append(ys, y)
	}
	sort.Ints(ys)
	return ys
}
