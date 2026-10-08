package app

import "strings"

// CleanPath tidies a path typed or pasted by the user: surrounding spaces and
// quotes are removed ("…", '…', „…”, “…”) – Explorer's "Copy as path" puts the
// path in double quotes. A single quote is only stripped when it wraps the
// whole path, because ' is legal inside Windows file names.
func CleanPath(p string) string {
	p = strings.TrimSpace(p)
	for i := 0; i < 3; i++ {
		before := p
		for _, q := range [][2]string{{`"`, `"`}, {"'", "'"}, {"„", "”"}, {"“", "”"}, {"”", "”"}, {"‘", "’"}, {"«", "»"}} {
			if len(p) >= len(q[0])+len(q[1]) && strings.HasPrefix(p, q[0]) && strings.HasSuffix(p, q[1]) {
				p = strings.TrimSpace(p[len(q[0]) : len(p)-len(q[1])])
			}
		}
		// A double quote can never be part of a Windows path.
		p = strings.TrimSpace(strings.Trim(p, `"„”“`))
		if p == before {
			break
		}
	}
	return p
}
