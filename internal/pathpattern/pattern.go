// Package pathpattern resolves watched paths that contain wildcards or
// date tokens, e.g.
//
//	\\EFS-FSRHQ\Groups\BI\export.xlsx                 a concrete file
//	\\EF-BI\exports\sales_*.parquet                   newest matching file
//	\\EFS-FSRHQ\Groups\Riport\riport_{yyyyMMdd}.xlsx  the expected day's file
//	\\EF-BI\out\{yyyy}\{MM}\daily_{yyyyMMdd:-1d}.csv  yesterday's data, dated folders
//
// Tokens: yyyy yy MM M dd d HH H mm, plus any literal separators, optionally
// followed by an offset ":-1d", ":+2h", ":-1M", ":-1w", ":-1y", ":-30m".
// Wildcards (* and ?) are allowed in the file name only. Matching is
// case-insensitive, as on Windows.
package pathpattern

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Pattern is a parsed path.
type Pattern struct {
	raw  string
	sep  string
	dir  segment // directory part (tokens allowed, no wildcards)
	file segment // file name part
}

type segment []piece

type piece struct {
	lit    string // literal text (when tok == "")
	tok    string // token format, e.g. "yyyyMMdd"
	offset offset
	wild   byte // '*' or '?' (file name only)
}

type offset struct {
	n    int
	unit byte // d h m M w y
}

// Parse validates and parses a path.
func Parse(path string) (*Pattern, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return nil, errors.New("az útvonal üres")
	}
	if strings.ContainsAny(p, "\"<>|") {
		return nil, errors.New("az útvonal tiltott karaktert tartalmaz (\" < > |)")
	}
	sep := `\`
	if !strings.Contains(p, `\`) {
		sep = "/"
	}
	idx := strings.LastIndexAny(p, `\/`)
	if idx < 0 || idx == len(p)-1 {
		return nil, errors.New("az útvonalnak egy fájlra (vagy fájlmintára) kell mutatnia, pl. \\\\szerver\\mappa\\fajl.xlsx")
	}
	pat := &Pattern{raw: p, sep: sep}
	dirStr, fileStr := p[:idx], p[idx+1:]
	var err error
	if pat.dir, err = parseSegment(dirStr, false); err != nil {
		return nil, err
	}
	if pat.file, err = parseSegment(fileStr, true); err != nil {
		return nil, err
	}
	return pat, nil
}

func parseSegment(s string, allowWild bool) (segment, error) {
	var seg segment
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			seg = append(seg, piece{lit: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '*', '?':
			if !allowWild {
				return nil, fmt.Errorf("a * és ? helyettesítő karakter csak a fájlnévben használható, a mappanévben nem (%q)", s)
			}
			flush()
			seg = append(seg, piece{wild: c})
		case '{':
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				return nil, fmt.Errorf("lezáratlan { a következőben: %q", s)
			}
			body := s[i+1 : i+end]
			tp, err := parseToken(body)
			if err != nil {
				return nil, err
			}
			flush()
			seg = append(seg, tp)
			i += end
		case '}':
			return nil, fmt.Errorf("felesleges } a következőben: %q", s)
		default:
			lit.WriteByte(c)
		}
	}
	flush()
	return seg, nil
}

var tokenRe = regexp.MustCompile(`^(yyyy|yy|MM|M|dd|d|HH|H|mm|[^A-Za-z])+$`)

func parseToken(body string) (piece, error) {
	f, off, hasOff := strings.Cut(body, ":")
	if f == "" || !tokenRe.MatchString(f) || !strings.ContainsAny(f, "yMdHm") {
		return piece{}, fmt.Errorf("ismeretlen dátum token: {%s} (használható: yyyy, yy, MM, dd, HH, mm, pl. {yyyyMMdd} vagy {yyyy-MM-dd:-1d})", body)
	}
	p := piece{tok: f}
	if hasOff {
		o, err := parseOffset(off)
		if err != nil {
			return piece{}, fmt.Errorf("{%s}: %w", body, err)
		}
		p.offset = o
	}
	return p, nil
}

func parseOffset(s string) (offset, error) {
	if len(s) < 3 || (s[0] != '+' && s[0] != '-') {
		return offset{}, errors.New("az eltolás formája pl. -1d, +2h, -1M")
	}
	unit := s[len(s)-1]
	if !strings.ContainsRune("dhmMwy", rune(unit)) {
		return offset{}, errors.New("az eltolás egysége d (nap), h (óra), m (perc), w (hét), M (hónap) vagy y (év) lehet")
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil {
		return offset{}, errors.New("érvénytelen eltolás: " + s)
	}
	return offset{n: n, unit: unit}, nil
}

func (o offset) apply(t time.Time) time.Time {
	switch o.unit {
	case 'd':
		return t.AddDate(0, 0, o.n)
	case 'w':
		return t.AddDate(0, 0, 7*o.n)
	case 'M':
		return t.AddDate(0, o.n, 0)
	case 'y':
		return t.AddDate(o.n, 0, 0)
	case 'h':
		return t.Add(time.Duration(o.n) * time.Hour)
	case 'm':
		return t.Add(time.Duration(o.n) * time.Minute)
	}
	return t
}

// formatToken renders a token format for t.
func formatToken(f string, t time.Time) string {
	var sb strings.Builder
	for i := 0; i < len(f); {
		switch {
		case strings.HasPrefix(f[i:], "yyyy"):
			fmt.Fprintf(&sb, "%04d", t.Year())
			i += 4
		case strings.HasPrefix(f[i:], "yy"):
			fmt.Fprintf(&sb, "%02d", t.Year()%100)
			i += 2
		case strings.HasPrefix(f[i:], "MM"):
			fmt.Fprintf(&sb, "%02d", int(t.Month()))
			i += 2
		case f[i] == 'M':
			fmt.Fprintf(&sb, "%d", int(t.Month()))
			i++
		case strings.HasPrefix(f[i:], "dd"):
			fmt.Fprintf(&sb, "%02d", t.Day())
			i += 2
		case f[i] == 'd':
			fmt.Fprintf(&sb, "%d", t.Day())
			i++
		case strings.HasPrefix(f[i:], "HH"):
			fmt.Fprintf(&sb, "%02d", t.Hour())
			i += 2
		case f[i] == 'H':
			fmt.Fprintf(&sb, "%d", t.Hour())
			i++
		case strings.HasPrefix(f[i:], "mm"):
			fmt.Fprintf(&sb, "%02d", t.Minute())
			i += 2
		default:
			sb.WriteByte(f[i])
			i++
		}
	}
	return sb.String()
}

// tokenRegex renders a token format as a digit regex (for "any" mode).
func tokenRegex(f string) string {
	var sb strings.Builder
	for i := 0; i < len(f); {
		switch {
		case strings.HasPrefix(f[i:], "yyyy"):
			sb.WriteString(`\d{4}`)
			i += 4
		case strings.HasPrefix(f[i:], "yy"), strings.HasPrefix(f[i:], "MM"), strings.HasPrefix(f[i:], "dd"),
			strings.HasPrefix(f[i:], "HH"), strings.HasPrefix(f[i:], "mm"):
			sb.WriteString(`\d{2}`)
			i += 2
		case f[i] == 'M' || f[i] == 'd' || f[i] == 'H':
			sb.WriteString(`\d{1,2}`)
			i++
		default:
			sb.WriteString(regexp.QuoteMeta(string(f[i])))
			i++
		}
	}
	return sb.String()
}

// IsPattern reports whether the file name needs a directory listing.
func (p *Pattern) IsPattern() bool {
	for _, pc := range p.file {
		if pc.wild != 0 || pc.tok != "" {
			return true
		}
	}
	return false
}

// HasTokens reports whether the path contains date tokens.
func (p *Pattern) HasTokens() bool {
	for _, s := range append(append(segment{}, p.dir...), p.file...) {
		if s.tok != "" {
			return true
		}
	}
	return false
}

// Resolved is a pattern bound to a reference time.
type Resolved struct {
	Dir     string         // directory to look in
	File    string         // exact file name (when Exact)
	Exact   bool           // no listing needed
	Display string         // human-readable resolved path
	re      *regexp.Regexp // file name matcher (when !Exact)
}

// Resolve binds tokens to t. anyDate=true turns file-name tokens into digit
// wildcards instead (directory tokens always use t).
func (p *Pattern) Resolve(t time.Time, anyDate bool) *Resolved {
	var dir strings.Builder
	for _, pc := range p.dir {
		if pc.tok != "" {
			dir.WriteString(formatToken(pc.tok, pc.offset.apply(t)))
		} else {
			dir.WriteString(pc.lit)
		}
	}
	r := &Resolved{Dir: dir.String()}
	exact := true
	var name, re strings.Builder
	re.WriteString("(?i)^")
	for _, pc := range p.file {
		switch {
		case pc.wild == '*':
			exact = false
			name.WriteByte('*')
			re.WriteString(".*")
		case pc.wild == '?':
			exact = false
			name.WriteByte('?')
			re.WriteString(".")
		case pc.tok != "" && anyDate:
			exact = false
			name.WriteString("{" + pc.tok + "}")
			re.WriteString(tokenRegex(pc.tok))
		case pc.tok != "":
			s := formatToken(pc.tok, pc.offset.apply(t))
			name.WriteString(s)
			re.WriteString(regexp.QuoteMeta(s))
		default:
			name.WriteString(pc.lit)
			re.WriteString(regexp.QuoteMeta(pc.lit))
		}
	}
	re.WriteString("$")
	r.Exact = exact
	r.File = name.String()
	r.Display = r.Dir + p.sep + r.File
	if !exact {
		r.re = regexp.MustCompile(re.String())
	}
	return r
}

// Match reports whether a file name matches.
func (r *Resolved) Match(name string) bool {
	if r.Exact {
		return strings.EqualFold(name, r.File)
	}
	return r.re.MatchString(name)
}

// Join builds the full path of a file in the resolved directory.
func (r *Resolved) Join(name string) string {
	sep := `\`
	if !strings.Contains(r.Dir, `\`) {
		sep = "/"
	}
	return r.Dir + sep + name
}

// ShareRoot returns \\server\share for UNC paths ("" otherwise) and the
// server name.
func ShareRoot(path string) (root, server string) {
	p := strings.ReplaceAll(path, "/", `\`)
	if !strings.HasPrefix(p, `\\`) {
		return "", ""
	}
	parts := strings.SplitN(p[2:], `\`, 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return `\\` + parts[0] + `\` + parts[1], strings.ToUpper(parts[0])
}

// ServerKey groups paths for the circuit breaker: the UNC server name, or
// the drive / root for local paths.
func ServerKey(path string) string {
	if _, srv := ShareRoot(path); srv != "" {
		return srv
	}
	p := strings.ReplaceAll(path, "/", `\`)
	if len(p) >= 2 && p[1] == ':' {
		return strings.ToUpper(p[:2])
	}
	return "local"
}

var dateRuns = []struct {
	re  *regexp.Regexp
	tok string
	lay string
}{
	{regexp.MustCompile(`(19|20)\d{2}-\d{2}-\d{2}`), "{yyyy-MM-dd}", "2006-01-02"},
	{regexp.MustCompile(`(19|20)\d{2}_\d{2}_\d{2}`), "{yyyy_MM_dd}", "2006_01_02"},
	{regexp.MustCompile(`(19|20)\d{2}\.\d{2}\.\d{2}`), "{yyyy.MM.dd}", "2006.01.02"},
	{regexp.MustCompile(`(19|20)\d{6}`), "{yyyyMMdd}", "20060102"},
	{regexp.MustCompile(`(19|20)\d{2}-\d{2}`), "{yyyy-MM}", "2006-01"},
	{regexp.MustCompile(`(19|20)\d{4}`), "{yyyyMM}", "200601"},
}

// Suggest proposes patterns for a concrete file path whose name contains a
// date, e.g. riport_20261008.xlsx → riport_{yyyyMMdd}.xlsx and riport_*.xlsx.
func Suggest(path string) []string {
	idx := strings.LastIndexAny(path, `\/`)
	dir, name := path[:idx+1], path[idx+1:]
	for _, d := range dateRuns {
		loc := d.re.FindStringIndex(name)
		if loc == nil {
			continue
		}
		if _, err := time.Parse(d.lay, name[loc[0]:loc[1]]); err != nil {
			continue
		}
		withTok := dir + name[:loc[0]] + d.tok + name[loc[1]:]
		withStar := dir + name[:loc[0]] + "*" + name[loc[1]:]
		return []string{withTok, withStar}
	}
	return nil
}
