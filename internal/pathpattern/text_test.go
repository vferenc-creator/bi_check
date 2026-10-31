package pathpattern

import (
	"regexp"
	"testing"
	"time"
)

func TestTextPattern(t *testing.T) {
	ref := time.Date(2026, 10, 31, 6, 0, 0, 0, time.UTC)
	cases := []struct {
		pat, subj string
		any, want bool
	}{
		{"Napi értékesítés", "BI: Napi  értékesítés riport 2026.10.31", false, true}, // no wildcard → contains, spaces folded
		{"napi ÉRTÉKESÍTÉS", "Napi értékesítés", false, true},                        // case-insensitive (also accents)
		{"Napi riport {yyyy.MM.dd}", "Napi riport 2026.10.31", false, true},
		{"Napi riport {yyyy.MM.dd}", "Napi riport 2026.10.30", false, false},
		{"Napi riport {yyyy.MM.dd}", "Napi riport 2026.10.30", true, true},
		{"Heti*összesítő", "Heti KPI összesítő", false, true},
		{"Heti*összesítő", "Re: Heti KPI összesítő", false, false}, // with a wildcard the whole subject must match
		{"Kimutatás {yyyyMMdd:-1d}", "Kimutatás 20261030 (tegnapi)", false, true},
	}
	for _, c := range cases {
		x, err := ParseText(c.pat)
		if err != nil {
			t.Fatal(err)
		}
		if got := x.Matcher(ref, c.any)(c.subj); got != c.want {
			t.Errorf("%q ~ %q (any=%v) = %v, want %v", c.pat, c.subj, c.any, got, c.want)
		}
	}
	x, _ := ParseText("Napi riport {yyyy.MM.dd}")
	loose := x.LooseRegex()
	if !regexp.MustCompile("(?i)" + loose).MatchString("NAPI RIPORT 2025.01.02") {
		t.Errorf("loose regex %q", loose)
	}
	if _, err := ParseText("Riport {xyz}"); err == nil {
		t.Error("bad token accepted")
	}
}
