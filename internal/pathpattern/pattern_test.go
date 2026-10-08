package pathpattern

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 8, 6, 0, 0, 0, time.UTC)

func TestConcreteFile(t *testing.T) {
	p, err := Parse(`\\EFS-FSRHQ\Groups\BI\export.xlsx`)
	if err != nil {
		t.Fatal(err)
	}
	if p.IsPattern() {
		t.Fatal("not a pattern")
	}
	r := p.Resolve(t0, false)
	if !r.Exact || r.Dir != `\\EFS-FSRHQ\Groups\BI` || r.File != "export.xlsx" {
		t.Fatalf("%+v", r)
	}
	if !r.Match("EXPORT.XLSX") {
		t.Fatal("case-insensitive match expected")
	}
}

func TestWildcard(t *testing.T) {
	p, err := Parse(`\\EF-BI\exports\sales_*.parquet`)
	if err != nil {
		t.Fatal(err)
	}
	r := p.Resolve(t0, false)
	if r.Exact {
		t.Fatal("should need listing")
	}
	for name, want := range map[string]bool{
		"sales_20261008.parquet": true,
		"SALES_x.PARQUET":        true,
		"sales_.parquet":         true,
		"sales.parquet":          false,
		"sales_1.parquet.tmp":    false,
	} {
		if got := r.Match(name); got != want {
			t.Errorf("Match(%q) = %v, want %v", name, got, want)
		}
	}
	q, _ := Parse(`C:\x\a?.csv`)
	if rq := q.Resolve(t0, false); !rq.Match("a1.csv") || rq.Match("a12.csv") {
		t.Error("? should match exactly one char")
	}
}

func TestDateTokens(t *testing.T) {
	p, err := Parse(`\\srv\out\{yyyy}\{MM}\riport_{yyyyMMdd}.xlsx`)
	if err != nil {
		t.Fatal(err)
	}
	r := p.Resolve(t0, false)
	if !r.Exact || r.Display != `\\srv\out\2026\10\riport_20261008.xlsx` {
		t.Fatalf("resolved %q exact=%v", r.Display, r.Exact)
	}
	any := p.Resolve(t0, true)
	if any.Exact || !any.Match("riport_20250101.xlsx") || any.Match("riport_2025011.xlsx") {
		t.Fatalf("any-date mode: %+v", any)
	}
	if any.Dir != `\\srv\out\2026\10` {
		t.Fatal("directory tokens always use the date")
	}
}

func TestOffsetsAndFormats(t *testing.T) {
	cases := map[string]string{
		`D:\a\d_{yyyyMMdd:-1d}.csv`:      `D:\a\d_20261007.csv`,
		`D:\a\m_{yyyy-MM:-1M}.csv`:       `D:\a\m_2026-09.csv`,
		`D:\a\w_{yyyy-MM-dd:-1w}.csv`:    `D:\a\w_2026-10-01.csv`,
		`D:\a\h_{yyyyMMdd_HHmm:+2h}.csv`: `D:\a\h_20261008_0800.csv`,
		`D:\a\s_{yy}{M}{d}.csv`:          `D:\a\s_26108.csv`,
		`D:\a\jan_{yyyyMMdd:-280d}.csv`:  `D:\a\jan_20260101.csv`,
	}
	for in, want := range cases {
		p, err := Parse(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got := p.Resolve(t0, false).Display; got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{
		"", `\\srv\share\`, `\\srv\*\x.csv`, `\\srv\a\{foo}.csv`, `\\srv\a\{yyyy.csv`,
		`\\srv\a\x}.csv`, `\\srv\a\{yyyy:1d}.csv`, `\\srv\a\{yyyy:-1q}.csv`, `C:\a\b|c.csv`, "nofolder.csv",
	} {
		if _, err := Parse(in); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

func TestShareRootAndServerKey(t *testing.T) {
	root, srv := ShareRoot(`\\EFS-FSRHQ\Groups\BI\x.xlsx`)
	if root != `\\EFS-FSRHQ\Groups` || srv != "EFS-FSRHQ" {
		t.Fatal(root, srv)
	}
	if ServerKey(`\\ef-bi\x\y.csv`) != "EF-BI" || ServerKey(`c:\x\y.csv`) != "C:" || ServerKey("/tmp/x") != "local" {
		t.Fatal("ServerKey")
	}
}

func TestSuggest(t *testing.T) {
	s := Suggest(`\\srv\r\riport_20261008.xlsx`)
	if len(s) != 2 || s[0] != `\\srv\r\riport_{yyyyMMdd}.xlsx` || s[1] != `\\srv\r\riport_*.xlsx` {
		t.Fatal(s)
	}
	if s := Suggest(`\\srv\r\sales_2026-10-08_v2.csv`); len(s) == 0 || s[0] != `\\srv\r\sales_{yyyy-MM-dd}_v2.csv` {
		t.Fatal(s)
	}
	if s := Suggest(`\\srv\r\export.xlsx`); s != nil {
		t.Fatal(s)
	}
	if s := Suggest(`\\srv\r\id_20269999.xlsx`); s != nil {
		t.Fatal("invalid dates must not be suggested", s)
	}
}
