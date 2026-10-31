package pathpattern

import (
	"regexp"
	"strings"
	"time"
)

// Text is a pattern for free text such as an e-mail subject: the same
// wildcards (* and ?) and date tokens ({yyyy.MM.dd}, {yyyyMMdd:-1d}) as file
// names, without directories. A pattern with no wildcard matches subjects
// that contain it anywhere; matching ignores case and repeated spaces.
type Text struct {
	raw  string
	seg  segment
	wild bool
}

// ParseText validates a text pattern.
func ParseText(s string) (*Text, error) {
	s = strings.TrimSpace(s)
	seg, err := parseSegment(s, true)
	if err != nil {
		return nil, err
	}
	t := &Text{raw: s, seg: seg}
	for _, p := range seg {
		if p.wild != 0 {
			t.wild = true
		}
	}
	return t, nil
}

var spaces = regexp.MustCompile(`\s+`)

// NormalizeText collapses whitespace (mail clients fold long subjects).
func NormalizeText(s string) string { return strings.TrimSpace(spaces.ReplaceAllString(s, " ")) }

// regex renders the pattern; tokens become t's date, or digit patterns when
// anyDate is set. The result is valid in both Go and JavaScript (use the
// "i" flag in JS; Go gets "(?i)" from Regexp).
func (x *Text) regex(t time.Time, anyDate bool) string {
	var re strings.Builder
	if !x.wild {
		re.WriteString(".*")
	} else {
		re.WriteString("^")
	}
	for _, pc := range x.seg {
		switch {
		case pc.wild == '*':
			re.WriteString(".*")
		case pc.wild == '?':
			re.WriteString(".")
		case pc.tok != "" && anyDate:
			re.WriteString(tokenRegex(pc.tok))
		case pc.tok != "":
			re.WriteString(regexp.QuoteMeta(formatToken(pc.tok, pc.offset.apply(t))))
		default:
			lit := NormalizeText(pc.lit)
			if strings.HasPrefix(pc.lit, " ") && lit != "" {
				lit = " " + lit
			}
			if strings.HasSuffix(pc.lit, " ") && lit != "" {
				lit += " "
			}
			if lit == "" && pc.lit != "" {
				lit = " "
			}
			re.WriteString(regexp.QuoteMeta(lit))
		}
	}
	if !x.wild {
		re.WriteString(".*")
	} else {
		re.WriteString("$")
	}
	return re.String()
}

// Matcher returns a case-insensitive matcher for reference time t.
func (x *Text) Matcher(t time.Time, anyDate bool) func(string) bool {
	re := regexp.MustCompile("(?is)" + x.regex(t, anyDate))
	return func(s string) bool { return re.MatchString(NormalizeText(s)) }
}

// LooseRegex is a JavaScript-compatible regex (use with the "i" flag) where
// every date token matches any digits. Thunderbird uses it to pick the
// candidate messages; the exact date check happens in the application.
func (x *Text) LooseRegex() string { return x.regex(time.Time{}, true) }

// String returns the pattern as written.
func (x *Text) String() string { return x.raw }
