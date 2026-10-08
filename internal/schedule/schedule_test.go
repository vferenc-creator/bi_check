package schedule

import (
	"strings"
	"testing"
	"time"

	"bimonitor/internal/calendar"
)

var bud = mustLoc("Europe/Budapest")

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

// at parses a Budapest wall time "2006-01-02 15:04".
func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, bud)
	if err != nil {
		panic(err)
	}
	return t
}

func compile(t *testing.T, sp Spec) *Schedule {
	t.Helper()
	s, err := Compile(sp, calendar.Default, bud)
	if err != nil {
		t.Fatalf("Compile(%+v): %v", sp, err)
	}
	return s
}

func fmtT(t time.Time) string { return t.In(bud).Format("2006-01-02 15:04 MST") }

func expectPrev(t *testing.T, s *Schedule, now, want string) {
	t.Helper()
	got, ok := s.Prev(at(now))
	if !ok {
		t.Fatalf("Prev(%s): none, want %s", now, want)
	}
	if w := at(want); !got.Equal(w) {
		t.Errorf("Prev(%s) = %s, want %s", now, fmtT(got), fmtT(w))
	}
}

func expectNext(t *testing.T, s *Schedule, now, want string) {
	t.Helper()
	got, ok := s.Next(at(now))
	if !ok {
		t.Fatalf("Next(%s): none, want %s", now, want)
	}
	if w := at(want); !got.Equal(w) {
		t.Errorf("Next(%s) = %s, want %s", now, fmtT(got), fmtT(w))
	}
}

func TestDaily(t *testing.T) {
	s := compile(t, Spec{Type: Daily, Times: []string{"06:00", "14:30"}})
	expectPrev(t, s, "2026-10-08 05:59", "2026-10-07 14:30")
	expectPrev(t, s, "2026-10-08 06:00", "2026-10-08 06:00") // exactly at the time
	expectPrev(t, s, "2026-10-08 14:29", "2026-10-08 06:00")
	expectPrev(t, s, "2026-10-08 23:59", "2026-10-08 14:30")
	expectNext(t, s, "2026-10-08 06:00", "2026-10-08 14:30") // strictly after
	expectNext(t, s, "2026-10-08 15:00", "2026-10-09 06:00")
}

func TestMidnightCrossing(t *testing.T) {
	s := compile(t, Spec{Type: Daily, Times: []string{"23:30"}})
	expectPrev(t, s, "2026-10-09 00:10", "2026-10-08 23:30")
	expectPrev(t, s, "2027-01-01 00:00", "2026-12-31 23:30") // year boundary
	s0 := compile(t, Spec{Type: Daily, Times: []string{"00:00"}})
	expectPrev(t, s0, "2026-10-08 00:00", "2026-10-08 00:00")
	expectPrev(t, s0, "2026-10-07 23:59", "2026-10-07 00:00")
	expectNext(t, s0, "2026-10-07 23:59", "2026-10-08 00:00")
}

func TestHourlyWindow(t *testing.T) {
	s := compile(t, Spec{Type: Hourly, From: "06:00", To: "20:00"})
	expectPrev(t, s, "2026-10-08 21:30", "2026-10-08 20:00")
	expectPrev(t, s, "2026-10-08 05:00", "2026-10-07 20:00")
	expectPrev(t, s, "2026-10-08 06:00", "2026-10-08 06:00")
	expectPrev(t, s, "2026-10-08 13:59", "2026-10-08 13:00")
	expectNext(t, s, "2026-10-08 20:00", "2026-10-09 06:00")
	if n := len(s.occurrencesOn(calendar.Date{Year: 2026, Month: 10, Day: 8})); n != 15 {
		t.Errorf("06–20 hourly: %d occurrences, want 15", n)
	}
}

func TestHourlyIntervalAndOffset(t *testing.T) {
	s := compile(t, Spec{Type: Hourly, IntervalMinutes: 30, From: "00:15"})
	expectPrev(t, s, "2026-10-08 10:44", "2026-10-08 10:15")
	expectPrev(t, s, "2026-10-08 10:45", "2026-10-08 10:45")
	expectPrev(t, s, "2026-10-08 00:10", "2026-10-07 23:45")
	s2 := compile(t, Spec{Type: Hourly, IntervalMinutes: 180, From: "06:00", To: "18:00"})
	expectPrev(t, s2, "2026-10-08 17:59", "2026-10-08 15:00")
	expectNext(t, s2, "2026-10-08 18:00", "2026-10-09 06:00")
}

func TestHourlyWrappingWindowWithWorkdays(t *testing.T) {
	// Night batch 22:00–02:00 on working days. The after-midnight part belongs
	// to the day the window started.
	s := compile(t, Spec{Type: Hourly, From: "22:00", To: "02:00", Days: DaysWorkdays})
	// Friday 2026-10-09 is a workday → Saturday 01:00 is expected.
	expectPrev(t, s, "2026-10-10 01:30", "2026-10-10 01:00")
	// Saturday evening: Saturday is not a workday → last was Sat 02:00.
	expectPrev(t, s, "2026-10-10 23:30", "2026-10-10 02:00")
	// Monday 00:30: Sunday's window does not run → still Sat 02:00.
	expectPrev(t, s, "2026-10-12 00:30", "2026-10-10 02:00")
	expectNext(t, s, "2026-10-12 00:30", "2026-10-12 22:00")
	// Thursday 2026-10-22 window runs into Friday 2026-10-23 (holiday) morning.
	expectPrev(t, s, "2026-10-23 01:10", "2026-10-23 01:00")
	// Friday 2026-10-23 evening: holiday → no window.
	expectPrev(t, s, "2026-10-23 23:00", "2026-10-23 02:00")
}

func TestHourlyDayFilters(t *testing.T) {
	s := compile(t, Spec{Type: Hourly, From: "08:00", To: "16:00", Days: DaysWeekdays})
	expectPrev(t, s, "2026-10-11 12:00", "2026-10-09 16:00") // Sunday → Friday
	// Weekdays filter does not care about holidays.
	expectPrev(t, s, "2026-10-23 12:00", "2026-10-23 12:00")
	c := compile(t, Spec{Type: Hourly, From: "08:00", To: "09:00", Days: DaysCustom, Weekdays: []int{6}})
	expectPrev(t, c, "2026-10-14 12:00", "2026-10-10 09:00")
}

func TestWeekly(t *testing.T) {
	s := compile(t, Spec{Type: Weekly, Weekdays: []int{1, 4}, Times: []string{"07:00"}})
	expectPrev(t, s, "2026-10-08 06:59", "2026-10-05 07:00") // Thu before 7 → Mon
	expectPrev(t, s, "2026-10-08 07:00", "2026-10-08 07:00")
	expectPrev(t, s, "2026-10-11 20:00", "2026-10-08 07:00") // Sunday → Thu
	expectNext(t, s, "2026-10-08 08:00", "2026-10-12 07:00")
}

func TestWeeklyHolidayRules(t *testing.T) {
	// Easter Monday 2026-04-06.
	next := compile(t, Spec{Type: Weekly, Weekdays: []int{1}, Times: []string{"08:00"}, HolidayRule: HolidayNext})
	expectPrev(t, next, "2026-04-06 12:00", "2026-03-30 08:00")
	expectPrev(t, next, "2026-04-07 08:00", "2026-04-07 08:00")
	skip := compile(t, Spec{Type: Weekly, Weekdays: []int{1}, Times: []string{"08:00"}, HolidayRule: HolidaySkip})
	expectPrev(t, skip, "2026-04-10 12:00", "2026-03-30 08:00")
	prev := compile(t, Spec{Type: Weekly, Weekdays: []int{1}, Times: []string{"08:00"}, HolidayRule: HolidayPrev})
	// Monday is a holiday, the preceding Fri (Good Friday) too → Thursday 04-02.
	expectPrev(t, prev, "2026-04-06 12:00", "2026-04-02 08:00")
	none := compile(t, Spec{Type: Weekly, Weekdays: []int{1}, Times: []string{"08:00"}})
	expectPrev(t, none, "2026-04-06 12:00", "2026-04-06 08:00")
}

func TestWorkdays(t *testing.T) {
	hu := compile(t, Spec{Type: Workdays, Times: []string{"06:00"}, UseHolidays: true})
	// Weekend: Saturday → Friday.
	expectPrev(t, hu, "2026-10-10 12:00", "2026-10-09 06:00")
	// Friday 2026-10-23 is a national holiday → Thursday.
	expectPrev(t, hu, "2026-10-24 09:00", "2026-10-22 06:00")
	// Working Saturday 2026-12-12.
	expectPrev(t, hu, "2026-12-12 07:00", "2026-12-12 06:00")
	// Christmas: 24 (rest), 25, 26 → after Wednesday 23rd comes Monday 28th.
	expectPrev(t, hu, "2026-12-27 12:00", "2026-12-23 06:00")
	expectNext(t, hu, "2026-12-23 07:00", "2026-12-28 06:00")
	// New year bridge: Jan 1 holiday, Jan 2 rest day.
	expectNext(t, hu, "2025-12-31 07:00", "2026-01-05 06:00")

	plain := compile(t, Spec{Type: Workdays, Times: []string{"06:00"}})
	expectPrev(t, plain, "2026-10-24 09:00", "2026-10-23 06:00") // ignores holiday
	expectPrev(t, plain, "2026-12-12 07:00", "2026-12-11 06:00") // ignores working Saturday
}

func TestMonthly(t *testing.T) {
	first := compile(t, Spec{Type: Monthly, MonthlyMode: MonthDayN, MonthDay: 1, Times: []string{"06:00"}})
	expectPrev(t, first, "2026-10-08 12:00", "2026-10-01 06:00")
	expectPrev(t, first, "2026-10-01 05:00", "2026-09-01 06:00")
	expectNext(t, first, "2026-12-15 00:00", "2027-01-01 06:00")

	d31 := compile(t, Spec{Type: Monthly, MonthDay: 31, Times: []string{"20:00"}})
	expectPrev(t, d31, "2026-03-01 00:00", "2026-02-28 20:00") // clamped
	expectPrev(t, d31, "2024-03-01 00:00", "2024-02-29 20:00") // leap year
	expectPrev(t, d31, "2026-05-01 00:00", "2026-04-30 20:00")

	last := compile(t, Spec{Type: Monthly, MonthlyMode: MonthLastDay, Times: []string{"23:00"}})
	expectPrev(t, last, "2026-10-08 12:00", "2026-09-30 23:00")

	fw := compile(t, Spec{Type: Monthly, MonthlyMode: MonthFirstWorkday, Times: []string{"08:00"}})
	// January 2026: 1st holiday, 2nd bridge day, 3–4 weekend → Monday 5th.
	expectNext(t, fw, "2025-12-31 00:00", "2026-01-05 08:00")
	// November 2026: 1st is Sunday → Monday 2nd.
	expectNext(t, fw, "2026-10-08 00:00", "2026-11-02 08:00")

	nw := compile(t, Spec{Type: Monthly, MonthlyMode: MonthNthWorkday, MonthDay: 3, Times: []string{"08:00"}})
	expectNext(t, nw, "2025-12-31 00:00", "2026-01-07 08:00")

	lw := compile(t, Spec{Type: Monthly, MonthlyMode: MonthLastWorkday, Times: []string{"18:00"}})
	expectPrev(t, lw, "2026-11-01 00:00", "2026-10-30 18:00") // 31 is Saturday
	expectPrev(t, lw, "2027-01-01 00:00", "2026-12-31 18:00")
}

func TestMonthlyHolidayShift(t *testing.T) {
	// November 1st 2026 is a Sunday (and a holiday).
	next := compile(t, Spec{Type: Monthly, MonthDay: 1, Times: []string{"06:00"}, HolidayRule: HolidayNext})
	expectNext(t, next, "2026-10-08 00:00", "2026-11-02 06:00")
	prev := compile(t, Spec{Type: Monthly, MonthDay: 1, Times: []string{"06:00"}, HolidayRule: HolidayPrev})
	expectNext(t, prev, "2026-10-08 00:00", "2026-10-30 06:00") // Fri before (Oct 31 Sat)
	skip := compile(t, Spec{Type: Monthly, MonthDay: 1, Times: []string{"06:00"}, HolidayRule: HolidaySkip})
	expectNext(t, skip, "2026-10-08 00:00", "2026-12-01 06:00")
}

func TestDailyHolidaySkip(t *testing.T) {
	s := compile(t, Spec{Type: Daily, Times: []string{"06:00"}, HolidayRule: HolidaySkip})
	expectPrev(t, s, "2026-10-25 12:00", "2026-10-22 06:00")
}

func TestDSTSpringForward(t *testing.T) {
	// 2026-03-29: 02:00 CET → 03:00 CEST.
	s := compile(t, Spec{Type: Daily, Times: []string{"02:30"}})
	got, _ := s.Next(at("2026-03-29 00:00"))
	want := time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC) // 03:00 CEST
	if !got.Equal(want) {
		t.Errorf("non-existent 02:30 → %s, want %s", fmtT(got), fmtT(want))
	}
	// Day after: normal.
	got, _ = s.Next(got)
	if w := at("2026-03-30 02:30"); !got.Equal(w) {
		t.Errorf("next day → %s", fmtT(got))
	}
	// Hourly on the short day: 23 expected instants.
	h := compile(t, Spec{Type: Hourly})
	if n := len(h.occurrencesOn(calendar.Date{Year: 2026, Month: 3, Day: 29})); n != 23 {
		t.Errorf("hourly on spring-forward day: %d, want 23", n)
	}
	// Prev just after the gap.
	p, _ := h.Prev(time.Date(2026, 3, 29, 1, 10, 0, 0, time.UTC)) // 03:10 CEST
	if w := time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC); !p.Equal(w) {
		t.Errorf("hourly prev after gap = %s", fmtT(p))
	}
	p, _ = h.Prev(time.Date(2026, 3, 29, 0, 59, 0, 0, time.UTC)) // 01:59 CET
	if w := time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC); !p.Equal(w) {
		t.Errorf("hourly prev before gap = %s", fmtT(p))
	}
}

func TestDSTFallBack(t *testing.T) {
	// 2026-10-25: 03:00 CEST → 02:00 CET, 02:xx happens twice.
	s := compile(t, Spec{Type: Daily, Times: []string{"02:30"}})
	got, _ := s.Next(at("2026-10-25 00:00"))
	want := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC) // first 02:30 (CEST)
	if !got.Equal(want) {
		t.Errorf("ambiguous 02:30 → %s (%s UTC), want first occurrence", fmtT(got), got.UTC())
	}
	// During the second 02:30 the latest expected instant is still the first one.
	p, _ := s.Prev(time.Date(2026, 10, 25, 1, 45, 0, 0, time.UTC)) // 02:45 CET
	if !p.Equal(want) {
		t.Errorf("prev in repeated hour = %s", p.UTC())
	}
	h := compile(t, Spec{Type: Hourly})
	if n := len(h.occurrencesOn(calendar.Date{Year: 2026, Month: 10, Day: 25})); n != 24 {
		t.Errorf("hourly on fall-back day: %d, want 24 (repeated 02:00 counted once)", n)
	}
	// 6:00 daily: 25 real hours between the two expectations.
	d := compile(t, Spec{Type: Daily, Times: []string{"06:00"}})
	a, _ := d.Prev(at("2026-10-24 12:00"))
	b, _ := d.Next(a)
	if b.Sub(a) != 25*time.Hour {
		t.Errorf("gap across fall-back = %s, want 25h", b.Sub(a))
	}
}

func TestCron(t *testing.T) {
	s := compile(t, Spec{Type: Cron, Cron: "0 6 * * 1-5"})
	expectPrev(t, s, "2026-10-11 12:00", "2026-10-09 06:00")
	q := compile(t, Spec{Type: Cron, Cron: "*/15 8-17 * * *"})
	expectPrev(t, q, "2026-10-08 12:07", "2026-10-08 12:00")
	expectPrev(t, q, "2026-10-08 07:59", "2026-10-07 17:45")
	leap := compile(t, Spec{Type: Cron, Cron: "0 0 29 2 *"})
	expectPrev(t, leap, "2026-10-08 00:00", "2024-02-29 00:00")
	expectNext(t, leap, "2026-10-08 00:00", "2028-02-29 00:00")
	// Vixie OR semantics: the 1st of the month OR any Monday.
	or := compile(t, Spec{Type: Cron, Cron: "0 9 1 * MON"})
	expectPrev(t, or, "2026-10-04 12:00", "2026-10-01 09:00")
	expectPrev(t, or, "2026-10-06 12:00", "2026-10-05 09:00")
	sun7 := compile(t, Spec{Type: Cron, Cron: "30 5 * jan-dec 7"})
	expectPrev(t, sun7, "2026-10-08 12:00", "2026-10-04 05:30")
	mac := compile(t, Spec{Type: Cron, Cron: "@daily"})
	expectPrev(t, mac, "2026-10-08 12:00", "2026-10-08 00:00")
	hol := compile(t, Spec{Type: Cron, Cron: "0 6 * * *", HolidayRule: HolidaySkip})
	expectPrev(t, hol, "2026-10-23 12:00", "2026-10-22 06:00")
}

func TestCronErrors(t *testing.T) {
	for _, e := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *", "a * * * *", "5-1 * * * *"} {
		if _, err := Compile(Spec{Type: Cron, Cron: e}, nil, bud); err == nil {
			t.Errorf("expected error for %q", e)
		}
	}
}

func TestValidation(t *testing.T) {
	bad := []Spec{
		{Type: "nope"},
		{Type: Daily},
		{Type: Daily, Times: []string{"25:00"}},
		{Type: Daily, Times: []string{"6:5"}},
		{Type: Weekly, Times: []string{"06:00"}},
		{Type: Weekly, Times: []string{"06:00"}, Weekdays: []int{0}},
		{Type: Monthly, Times: []string{"06:00"}, MonthDay: 0},
		{Type: Monthly, Times: []string{"06:00"}, MonthlyMode: MonthNthWorkday, MonthDay: 30},
		{Type: Hourly, IntervalMinutes: -5},
		{Type: Hourly, From: "x"},
		{Type: Hourly, Days: DaysCustom},
		{Type: Daily, Times: []string{"06:00"}, HolidayRule: "maybe"},
	}
	for _, sp := range bad {
		if _, err := Compile(sp, nil, bud); err == nil {
			t.Errorf("expected validation error for %+v", sp)
		}
	}
	if _, err := Compile(Spec{Type: Daily, Times: []string{"6:05"}}, nil, bud); err != nil {
		t.Errorf("H:MM should be accepted: %v", err)
	}
}

func TestBetweenAndNextN(t *testing.T) {
	s := compile(t, Spec{Type: Workdays, Times: []string{"06:00"}, UseHolidays: true})
	// October 2026: 22 weekdays minus Oct 23 holiday = 21 working days.
	got := s.Between(at("2026-09-30 23:59"), at("2026-10-31 23:59"), 1000)
	if len(got) != 21 {
		t.Errorf("October 2026 working days = %d, want 21", len(got))
	}
	n := s.NextN(at("2026-10-21 12:00"), 3)
	want := []string{"2026-10-22 06:00", "2026-10-26 06:00", "2026-10-27 06:00"}
	for i := range want {
		if i >= len(n) || !n[i].Equal(at(want[i])) {
			t.Fatalf("NextN = %v, want %v", n, want)
		}
	}
}

func TestUserCalendarOverride(t *testing.T) {
	cal := calendar.New(calendar.Overrides{RestDays: []string{"2026-10-09"}})
	s, err := Compile(Spec{Type: Workdays, Times: []string{"06:00"}, UseHolidays: true}, cal, bud)
	if err != nil {
		t.Fatal(err)
	}
	expectPrev(t, s, "2026-10-09 12:00", "2026-10-08 06:00")
}

func TestDescribe(t *testing.T) {
	cases := map[string]Spec{
		"Óránként, 06:00–20:00, munkanapokon":               {Type: Hourly, From: "06:00", To: "20:00", Days: DaysWorkdays},
		"30 percenként, egész nap":                          {Type: Hourly, IntervalMinutes: 30},
		"Naponta 06:00, 14:30":                              {Type: Daily, Times: []string{"14:30", "6:00"}},
		"Hetente (H, P) 07:00":                              {Type: Weekly, Weekdays: []int{5, 1}, Times: []string{"07:00"}},
		"Munkanapokon 06:00 (magyar munkaszüneti napokkal)": {Type: Workdays, Times: []string{"06:00"}, UseHolidays: true},
		"Havonta, a hónap első munkanapján 08:00":           {Type: Monthly, MonthlyMode: MonthFirstWorkday, Times: []string{"08:00"}},
		"Havonta, minden hónap 1. napján 06:00; munkaszüneti napról a következő munkanapra tolva": {Type: Monthly, MonthDay: 1, Times: []string{"06:00"}, HolidayRule: HolidayNext},
	}
	for want, sp := range cases {
		if got := sp.Describe(); got != want {
			t.Errorf("Describe = %q, want %q", got, want)
		}
	}
	if !strings.HasPrefix(Spec{Type: Cron, Cron: "0 6 * * *"}.Describe(), "Cron:") {
		t.Error("cron describe")
	}
}
