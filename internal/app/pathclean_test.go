package app

import "testing"

func TestCleanPath(t *testing.T) {
	cases := map[string]string{
		`"\\EFS-FSRHQ\Groups\BI\x.xlsx"`: `\\EFS-FSRHQ\Groups\BI\x.xlsx`,
		`  "\\srv\a b\c.csv"  `:          `\\srv\a b\c.csv`,
		`'\\srv\a\c.csv'`:                `\\srv\a\c.csv`,
		`„\\srv\a\c.csv”`:                `\\srv\a\c.csv`,
		`“\\srv\a\c.csv”`:                `\\srv\a\c.csv`,
		`"\\srv\a\c.csv`:                 `\\srv\a\c.csv`,
		`\\srv\a\O'Brien.xlsx`:           `\\srv\a\O'Brien.xlsx`,
		`'\\srv\a\O'Brien.xlsx'`:         `\\srv\a\O'Brien.xlsx`,
		`"'\\srv\a\x.csv'"`:              `\\srv\a\x.csv`,
		`\\srv\a\x.csv`:                  `\\srv\a\x.csv`,
	}
	for in, want := range cases {
		if got := CleanPath(in); got != want {
			t.Errorf("CleanPath(%q) = %q, want %q", in, got, want)
		}
	}
}
