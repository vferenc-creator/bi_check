// Package schedule answers the central question of the monitor: when was the
// output last expected, and when is it expected next?
//
// All calculations happen in wall-clock time of the configured location
// (Europe/Budapest). Rules for daylight saving time:
//
//   - a wall time that does not exist (spring forward, e.g. 02:30) maps to
//     the first valid instant after the gap (03:00);
//   - a wall time that occurs twice (fall back) counts once, at its first
//     occurrence.
//
// Grace periods and early tolerances are absolute durations and are applied
// by the caller, so they work across midnight and DST changes too.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
)

// Type of a schedule.
type Type string

const (
	Hourly   Type = "hourly"   // every N minutes within an optional window
	Daily    Type = "daily"    // every day at given times
	Weekly   Type = "weekly"   // on selected weekdays at given times
	Workdays Type = "workdays" // Monday–Friday, optionally honoring HU holidays
	Monthly  Type = "monthly"  // once a month
	Cron     Type = "cron"     // 5-field cron expression
)

// HolidayRule says what happens when an expected day is not a working day.
type HolidayRule string

const (
	HolidayNone HolidayRule = ""     // ignore holidays
	HolidaySkip HolidayRule = "skip" // no expectation on non-working days
	HolidayNext HolidayRule = "next" // move to the next working day
	HolidayPrev HolidayRule = "prev" // move to the previous working day
)

// MonthlyMode selects the day of month.
type MonthlyMode string

const (
	MonthDayN         MonthlyMode = "day"          // MonthDay-th (clamped to month length)
	MonthLastDay      MonthlyMode = "lastDay"      // last calendar day
	MonthFirstWorkday MonthlyMode = "firstWorkday" // first working day
	MonthLastWorkday  MonthlyMode = "lastWorkday"  // last working day
	MonthNthWorkday   MonthlyMode = "nthWorkday"   // MonthDay-th working day
)

// DayFilter restricts hourly schedules to certain days.
type DayFilter string

const (
	DaysAll      DayFilter = ""         // every day
	DaysWeekdays DayFilter = "weekdays" // Monday–Friday
	DaysWorkdays DayFilter = "workdays" // Hungarian working days
	DaysCustom   DayFilter = "custom"   // the Weekdays list
)

// Spec is the user-editable (and JSON-persisted) schedule definition.
type Spec struct {
	Type Type `json:"type"`

	// Times of day ("HH:MM") for daily, weekly, workdays and monthly.
	Times []string `json:"times,omitempty"`
	// Weekdays (1 = Monday … 7 = Sunday) for weekly, and hourly with DaysCustom.
	Weekdays []int `json:"weekdays,omitempty"`

	// Hourly: every IntervalMinutes starting at From up to and including To.
	// If To < From the window wraps past midnight (e.g. 22:00–02:00); the
	// part after midnight belongs to the day the window started on.
	IntervalMinutes int       `json:"intervalMinutes,omitempty"`
	From            string    `json:"from,omitempty"`
	To              string    `json:"to,omitempty"`
	Days            DayFilter `json:"days,omitempty"`

	// Workdays: true = Hungarian working days (holidays, bridge days and
	// working Saturdays honored), false = plain Monday–Friday.
	UseHolidays bool `json:"useHolidays,omitempty"`

	// Monthly.
	MonthlyMode MonthlyMode `json:"monthlyMode,omitempty"`
	MonthDay    int         `json:"monthDay,omitempty"`

	// Daily, weekly, monthly (calendar day modes) and cron.
	HolidayRule HolidayRule `json:"holidayRule,omitempty"`

	// Cron expression: "min hour dom month dow" or @daily, @hourly, ...
	Cron string `json:"cron,omitempty"`
}

// parseClock parses "HH:MM" (also "H:MM") into minutes after midnight.
func parseClock(s string) (int, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("érvénytelen időpont: %q (ÓÓ:PP formátum kell)", s)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 || len(parts[1]) != 2 {
		return 0, fmt.Errorf("érvénytelen időpont: %q (ÓÓ:PP formátum kell)", s)
	}
	return h*60 + m, nil
}

func formatClock(min int) string { return fmt.Sprintf("%02d:%02d", min/60, min%60) }
