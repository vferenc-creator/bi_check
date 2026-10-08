package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronExpr is a parsed 5-field cron expression (Vixie semantics: when both
// day-of-month and day-of-week are restricted, a day matches if EITHER does).
type cronExpr struct {
	minute, hour, dom, month, dow uint64 // bit sets
	domStar, dowStar              bool
}

var cronMacros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

var monthNames = map[string]int{"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6, "JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12}
var dowNames = map[string]int{"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6}

func parseCron(expr string) (*cronExpr, error) {
	e := strings.TrimSpace(expr)
	if m, ok := cronMacros[strings.ToLower(e)]; ok {
		e = m
	}
	f := strings.Fields(e)
	if len(f) != 5 {
		return nil, fmt.Errorf("a cron kifejezésnek 5 mezőből kell állnia (perc óra nap hónap hétnapja), ez %d mezős", len(f))
	}
	c := &cronExpr{}
	var err error
	if c.minute, _, err = parseCronField(f[0], 0, 59, nil, "perc"); err != nil {
		return nil, err
	}
	if c.hour, _, err = parseCronField(f[1], 0, 23, nil, "óra"); err != nil {
		return nil, err
	}
	if c.dom, c.domStar, err = parseCronField(f[2], 1, 31, nil, "nap"); err != nil {
		return nil, err
	}
	if c.month, _, err = parseCronField(f[3], 1, 12, monthNames, "hónap"); err != nil {
		return nil, err
	}
	if c.dow, c.dowStar, err = parseCronField(f[4], 0, 7, dowNames, "hét napja"); err != nil {
		return nil, err
	}
	if c.dow&(1<<7) != 0 { // 7 = Sunday
		c.dow |= 1
	}
	return c, nil
}

func parseCronField(field string, lo, hi int, names map[string]int, label string) (uint64, bool, error) {
	var bits uint64
	star := field == "*" || field == "?"
	for _, part := range strings.Split(field, ",") {
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			s, err := strconv.Atoi(part[i+1:])
			if err != nil || s <= 0 {
				return 0, false, fmt.Errorf("cron %s mező: érvénytelen lépésköz %q", label, part)
			}
			step = s
			part = part[:i]
		}
		var a, b int
		switch {
		case part == "*" || part == "?":
			a, b = lo, hi
		case strings.Contains(part, "-"):
			ab := strings.SplitN(part, "-", 2)
			var err error
			if a, err = cronValue(ab[0], names); err != nil {
				return 0, false, fmt.Errorf("cron %s mező: %v", label, err)
			}
			if b, err = cronValue(ab[1], names); err != nil {
				return 0, false, fmt.Errorf("cron %s mező: %v", label, err)
			}
		default:
			v, err := cronValue(part, names)
			if err != nil {
				return 0, false, fmt.Errorf("cron %s mező: %v", label, err)
			}
			a, b = v, v
			if step > 1 { // "5/15" means 5-hi/15
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, false, fmt.Errorf("cron %s mező: %q kívül esik a %d–%d tartományon", label, part, lo, hi)
		}
		for v := a; v <= b; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, star, nil
}

func cronValue(s string, names map[string]int) (int, error) {
	if v, ok := names[strings.ToUpper(s)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("érvénytelen érték %q", s)
	}
	return v, nil
}

func (c *cronExpr) matchDay(y int, m time.Month, d int, wd time.Weekday) bool {
	if c.month&(1<<uint(m)) == 0 {
		return false
	}
	domOK := c.dom&(1<<uint(d)) != 0
	dowOK := c.dow&(1<<uint(wd)) != 0
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dowOK
	case c.dowStar:
		return domOK
	default:
		return domOK || dowOK
	}
}

func (c *cronExpr) times() []int {
	var out []int
	for h := 0; h < 24; h++ {
		if c.hour&(1<<uint(h)) == 0 {
			continue
		}
		for m := 0; m < 60; m++ {
			if c.minute&(1<<uint(m)) != 0 {
				out = append(out, h*60+m)
			}
		}
	}
	return out
}
