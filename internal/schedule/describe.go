package schedule

import (
	"fmt"
	"sort"
	"strings"
)

var huDayShort = [8]string{"", "H", "K", "Sze", "Cs", "P", "Szo", "V"}

func joinTimes(ts []string) string {
	cp := append([]string(nil), ts...)
	for i, t := range cp {
		if m, err := parseClock(t); err == nil {
			cp[i] = formatClock(m)
		}
	}
	sort.Strings(cp)
	return strings.Join(cp, ", ")
}

func joinDays(ds []int) string {
	cp := append([]int(nil), ds...)
	sort.Ints(cp)
	var parts []string
	for _, d := range cp {
		if d >= 1 && d <= 7 {
			parts = append(parts, huDayShort[d])
		}
	}
	return strings.Join(parts, ", ")
}

func ruleSuffix(r HolidayRule) string {
	switch r {
	case HolidaySkip:
		return "; munkaszüneti napon nincs"
	case HolidayNext:
		return "; munkaszüneti napról a következő munkanapra tolva"
	case HolidayPrev:
		return "; munkaszüneti napról az előző munkanapra hozva"
	}
	return ""
}

// Describe returns a short Hungarian, human readable summary of the spec.
func (sp Spec) Describe() string {
	switch sp.Type {
	case Hourly:
		iv := sp.IntervalMinutes
		if iv == 0 {
			iv = 60
		}
		var freq string
		switch {
		case iv == 60:
			freq = "Óránként"
		case iv%60 == 0:
			freq = fmt.Sprintf("%d óránként", iv/60)
		default:
			freq = fmt.Sprintf("%d percenként", iv)
		}
		from, to := sp.From, sp.To
		if from == "" {
			from = "00:00"
		}
		if to == "" {
			to = "23:59"
		}
		win := fmt.Sprintf(", %s–%s", from, to)
		if from == "00:00" && to == "23:59" {
			win = ", egész nap"
		}
		days := ""
		switch sp.Days {
		case DaysWeekdays:
			days = ", hétköznap"
		case DaysWorkdays:
			days = ", munkanapokon"
		case DaysCustom:
			days = ", " + joinDays(sp.Weekdays)
		}
		return freq + win + days
	case Daily:
		return "Naponta " + joinTimes(sp.Times) + ruleSuffix(sp.HolidayRule)
	case Weekly:
		return "Hetente (" + joinDays(sp.Weekdays) + ") " + joinTimes(sp.Times) + ruleSuffix(sp.HolidayRule)
	case Workdays:
		if sp.UseHolidays {
			return "Munkanapokon " + joinTimes(sp.Times) + " (magyar munkaszüneti napokkal)"
		}
		return "Hétköznap (H–P) " + joinTimes(sp.Times)
	case Monthly:
		var which string
		switch sp.MonthlyMode {
		case MonthLastDay:
			which = "a hónap utolsó napján"
		case MonthFirstWorkday:
			which = "a hónap első munkanapján"
		case MonthLastWorkday:
			which = "a hónap utolsó munkanapján"
		case MonthNthWorkday:
			which = fmt.Sprintf("a hónap %d. munkanapján", sp.MonthDay)
		default:
			which = fmt.Sprintf("minden hónap %d. napján", sp.MonthDay)
		}
		return "Havonta, " + which + " " + joinTimes(sp.Times) + ruleSuffix(sp.HolidayRule)
	case Cron:
		return "Cron: " + strings.TrimSpace(sp.Cron) + ruleSuffix(sp.HolidayRule)
	}
	return "Ismeretlen ütemezés"
}
