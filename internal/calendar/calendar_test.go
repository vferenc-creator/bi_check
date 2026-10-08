package calendar

import (
	"testing"
	"time"
)

func d(s string) Date {
	x, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return x
}

func TestEaster(t *testing.T) {
	cases := map[int]string{
		2024: "2024-03-31",
		2025: "2025-04-20",
		2026: "2026-04-05",
		2027: "2027-03-28",
		2028: "2028-04-16",
		2038: "2038-04-25",
	}
	for y, want := range cases {
		if got := Easter(y).String(); got != want {
			t.Errorf("Easter(%d) = %s, want %s", y, got, want)
		}
	}
}

func TestIsWorkday(t *testing.T) {
	c := Default
	cases := []struct {
		day  string
		want bool
		why  string
	}{
		{"2026-10-08", true, "ordinary Thursday"},
		{"2026-10-10", false, "Saturday"},
		{"2026-10-11", false, "Sunday"},
		{"2026-10-23", false, "national holiday (Friday)"},
		{"2026-04-03", false, "Good Friday"},
		{"2026-04-06", false, "Easter Monday"},
		{"2026-05-25", false, "Whit Monday"},
		{"2026-01-02", false, "bridge rest day"},
		{"2026-01-10", true, "working Saturday"},
		{"2026-08-08", true, "working Saturday"},
		{"2026-08-21", false, "bridge rest day"},
		{"2026-12-12", true, "working Saturday"},
		{"2026-12-24", false, "Christmas Eve rest day"},
		{"2026-12-25", false, "Christmas"},
		{"2025-05-02", false, "bridge 2025"},
		{"2025-05-17", true, "working Saturday 2025"},
		{"2025-12-24", false, "Christmas Eve 2025"},
		{"2027-03-15", false, "national holiday on Monday"},
		{"2027-03-29", false, "Easter Monday 2027"},
	}
	for _, tc := range cases {
		if got := c.IsWorkday(d(tc.day)); got != tc.want {
			t.Errorf("%s (%s): IsWorkday=%v, want %v", tc.day, tc.why, got, tc.want)
		}
	}
}

func TestOverrides(t *testing.T) {
	c := New(Overrides{
		RestDays: []string{"2026-10-09"},
		WorkDays: []string{"2026-10-17"},
		Ignore:   []string{"2026-12-12"},
	})
	if c.IsWorkday(d("2026-10-09")) {
		t.Error("user rest day should not be a workday")
	}
	if !c.IsWorkday(d("2026-10-17")) {
		t.Error("user workday Saturday should be a workday")
	}
	if c.IsWorkday(d("2026-12-12")) {
		t.Error("ignored built-in working Saturday should fall back to weekend")
	}
	// User workday wins over a holiday.
	c2 := New(Overrides{WorkDays: []string{"2026-10-23"}})
	if !c2.IsWorkday(d("2026-10-23")) {
		t.Error("explicit user workday should override a holiday")
	}
}

func TestYearListing(t *testing.T) {
	days := Default.Year(2026)
	if len(days) < 13 {
		t.Fatalf("expected at least 13 special days, got %d", len(days))
	}
	for i := 1; i < len(days); i++ {
		if !days[i-1].Date.Before(days[i].Date) {
			t.Fatalf("not sorted/unique at %d: %v %v", i, days[i-1].Date, days[i].Date)
		}
	}
	if days[0].DateStr != "2026-01-01" {
		t.Errorf("first day = %s", days[0].DateStr)
	}
}

func TestDateHelpers(t *testing.T) {
	if got := d("2024-02-28").AddDays(1).String(); got != "2024-02-29" {
		t.Error(got)
	}
	if DaysIn(2024, time.February) != 29 || DaysIn(2026, time.February) != 28 {
		t.Error("DaysIn")
	}
	if _, err := ParseDate("2026-13-01"); err == nil {
		t.Error("expected error")
	}
}
