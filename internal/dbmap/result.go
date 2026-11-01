package dbmap

import (
	"cmp"
	"encoding/hex"
	"fmt"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Kind of a result column (how it is compared, filtered and aligned).
type Kind string

const (
	KindText   Kind = "text"
	KindNumber Kind = "number"
	KindDate   Kind = "date"
	KindBool   Kind = "bool"
	KindBinary Kind = "binary"
)

// ResultCol describes one column of a result.
type ResultCol struct {
	Name string `json:"name"`
	Type string `json:"type"` // SQL type name
	Kind Kind   `json:"kind"`
}

// Num is an exact decimal (decimal/numeric/money) with a float for sorting.
type Num struct {
	S string
	F float64
}

// Result is the rows of one table, kept in memory for paging and filtering.
type Result struct {
	ID        string      `json:"id"`
	Table     string      `json:"table"`
	Cols      []ResultCol `json:"cols"`
	Total     int         `json:"total"`     // rows loaded
	Truncated bool        `json:"truncated"` // more rows exist than loaded
	Limit     int         `json:"limit"`
	All       bool        `json:"all"` // "whole table" was requested
	// Stopped tells why loading ended before the limit: "memory" – the
	// computer was running out of free memory.
	Stopped   string    `json:"stopped,omitempty"`
	FetchedAt time.Time `json:"fetchedAt"`
	TookMs    int64     `json:"tookMs"`

	rows   [][]any // cells: nil, int64, float64, Num, string, bool, time.Time
	intern []interner

	mu      sync.Mutex
	viewKey string
	view    []int32
}

// DefaultMaxRows is the row limit of "Teljes tábla" when none is set.
const DefaultMaxRows = 100_000

// MaxText is the longest text kept from one cell.
const MaxText = 4000

// Query asks for a page of a result.
type Query struct {
	Offset  int               `json:"offset"`
	Limit   int               `json:"limit"`
	Search  string            `json:"search"`
	Filters map[string]string `json:"filters"` // column name → filter expression
	Sort    string            `json:"sort"`    // column name or ""
	Desc    bool              `json:"desc"`
}

// Page is the answer: display strings (nil = NULL).
type Page struct {
	Offset   int        `json:"offset"`
	Rows     [][]any    `json:"rows"`
	Filtered int        `json:"filtered"`
	Total    int        `json:"total"`
	Error    string     `json:"error,omitempty"` // invalid filter
	Stats    []ColStats `json:"stats,omitempty"`
}

// ColStats summarise the filtered rows of a column (shown in the header tooltip).
type ColStats struct {
	Nulls    int    `json:"nulls"`
	Distinct int    `json:"distinct"` // capped at 10 000
	Min      string `json:"min,omitempty"`
	Max      string `json:"max,omitempty"`
}

// NewResult builds a result (used by sources).
func NewResult(table string, cols []ResultCol) *Result {
	return &Result{ID: randHex(6), Table: table, Cols: cols, FetchedAt: time.Now()}
}

// interner shares repeated cell values of one column (partner names,
// dates, prices…): a big table then needs far less memory.
type interner struct {
	m          map[any]any
	off        bool
	hits, miss int
}

const internMax = 1 << 16 // distinct values remembered per column

// Add appends one row (cells already normalised by Cell).
func (r *Result) Add(row []any) {
	if r.intern == nil {
		r.intern = make([]interner, len(row))
	}
	for i, v := range row {
		switch v.(type) {
		case string, Num, time.Time, int64, float64:
		default:
			continue
		}
		in := &r.intern[i]
		if in.off {
			continue
		}
		if x, ok := in.m[v]; ok {
			row[i] = x
			in.hits++
			continue
		}
		in.miss++
		switch {
		case len(in.m) < internMax:
			if in.m == nil {
				in.m = map[any]any{}
			}
			in.m[v] = v
		case in.hits < in.miss: // mostly unique values: not worth it
			in.off, in.m = true, nil
		}
	}
	r.rows = append(r.rows, row)
	r.Total = len(r.rows)
}

// Finish is called when loading ended: it frees the loading helpers.
func (r *Result) Finish() { r.intern = nil }

// KindOf maps a SQL Server type name to a column kind.
func KindOf(sqlType string) Kind {
	switch strings.ToUpper(sqlType) {
	case "INT", "BIGINT", "SMALLINT", "TINYINT", "DECIMAL", "NUMERIC", "MONEY", "SMALLMONEY", "FLOAT", "REAL":
		return KindNumber
	case "DATE", "DATETIME", "DATETIME2", "SMALLDATETIME", "DATETIMEOFFSET", "TIME":
		return KindDate
	case "BIT":
		return KindBool
	case "BINARY", "VARBINARY", "IMAGE", "TIMESTAMP", "ROWVERSION", "GEOGRAPHY", "GEOMETRY", "HIERARCHYID", "UDT":
		return KindBinary
	}
	return KindText
}

// Cell normalises a scanned value of a column of the given SQL type.
func Cell(v any, sqlType string) any {
	t := strings.ToUpper(sqlType)
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		switch t {
		case "DECIMAL", "NUMERIC", "MONEY", "SMALLMONEY":
			f, _ := strconv.ParseFloat(string(x), 64)
			return Num{S: string(x), F: f}
		case "UNIQUEIDENTIFIER":
			if len(x) == 16 {
				// SQL Server stores the first three groups little-endian
				return strings.ToUpper(fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%x-%x",
					x[3], x[2], x[1], x[0], x[5], x[4], x[7], x[6], x[8:10], x[10:]))
			}
		}
		if KindOf(t) == KindBinary || !isText(x) {
			if len(x) > 64 {
				return "0x" + strings.ToUpper(hex.EncodeToString(x[:64])) + "…"
			}
			return "0x" + strings.ToUpper(hex.EncodeToString(x))
		}
		return clipText(string(x))
	case string:
		return clipText(x)
	case int64, float64, bool, time.Time:
		return x
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case int16:
		return int64(x)
	case int8:
		return int64(x)
	case uint8:
		return int64(x)
	case float32:
		return float64(x)
	case fmt.Stringer:
		return clipText(x.String())
	}
	return clipText(fmt.Sprint(v))
}

func isText(b []byte) bool {
	for _, c := range b {
		if c < 9 {
			return false
		}
	}
	return true
}

func clipText(s string) string {
	if len(s) > MaxText {
		r := []rune(s)
		if len(r) > MaxText {
			return string(r[:MaxText]) + "…"
		}
	}
	return s
}

// Display formats a cell for the UI.
func Display(v any, sqlType string) any {
	switch x := v.(type) {
	case nil:
		return nil
	case Num:
		return x.S
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "1"
		}
		return "0"
	case time.Time:
		switch strings.ToUpper(sqlType) {
		case "DATE":
			return x.Format("2006-01-02")
		case "TIME":
			return strings.TrimSuffix(x.Format("15:04:05.0000000"), ".0000000")
		case "DATETIMEOFFSET":
			return trimFrac(x.Format("2006-01-02 15:04:05.0000000")) + x.Format(" -07:00")
		}
		return trimFrac(x.Format("2006-01-02 15:04:05.0000000"))
	case string:
		return x
	}
	return fmt.Sprint(v)
}

func trimFrac(s string) string {
	if i := strings.LastIndexByte(s, '.'); i > 0 {
		f := strings.TrimRight(s[i+1:], "0")
		if f == "" {
			return s[:i]
		}
		return s[:i+1] + f
	}
	return s
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case Num:
		return x.F, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// compare orders two cells of one column; NULL is the smallest.
func compare(a, b any) int {
	if a == nil || b == nil {
		switch {
		case a == nil && b == nil:
			return 0
		case a == nil:
			return -1
		default:
			return 1
		}
	}
	if fa, ok := number(a); ok {
		if fb, ok := number(b); ok {
			return cmp.Compare(fa, fb)
		}
	}
	if ta, ok := a.(time.Time); ok {
		if tb, ok := b.(time.Time); ok {
			return ta.Compare(tb)
		}
	}
	return compareFold(cellText(a, ""), cellText(b, ""))
}

// cellText is the text a cell is shown, searched and text-filtered by.
func cellText(v any, sqlType string) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case Num:
		return x.S
	}
	s, _ := Display(v, sqlType).(string)
	return s
}

// sortKey makes text sort case-insensitively in Hungarian alphabetical
// order: á, é, í, ó, ú go with their base letter, ö/ő after o, ü/ű after u.
func sortKey(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.ToLower(s)
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for _, c := range strings.ToLower(s) {
		switch c {
		case 'á', 'à', 'â', 'ä':
			b.WriteByte('a')
		case 'é', 'è', 'ê', 'ë':
			b.WriteByte('e')
		case 'í', 'ì', 'î', 'ï':
			b.WriteByte('i')
		case 'ó', 'ò', 'ô':
			b.WriteByte('o')
		case 'ö', 'ő':
			b.WriteString("o~")
		case 'ú', 'ù', 'û':
			b.WriteByte('u')
		case 'ü', 'ű':
			b.WriteString("u~")
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// compareFold compares like sortKey (without allocating for ASCII).
func compareFold(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		x, y := a[i], b[i]
		if x >= 0x80 || y >= 0x80 {
			return strings.Compare(sortKey(a[i:]), sortKey(b[i:]))
		}
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return cmp.Compare(x, y)
		}
	}
	return cmp.Compare(len(a), len(b))
}

// matcher finds a lower-case text in cells, case-insensitively.
type matcher struct {
	low     string
	ascii   bool // no accents: compare bytes without allocating
	numeric bool // could appear in a number or date (else those are skipped)
}

func newMatcher(low string) matcher {
	m := matcher{low: low, ascii: true, numeric: true}
	for i := 0; i < len(low); i++ {
		c := low[i]
		if c >= 0x80 {
			m.ascii = false
		}
		if !strings.ContainsRune("0123456789.-+: e", rune(c)) {
			m.numeric = false
		}
	}
	return m
}

func (m matcher) match(v any, sqlType string) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return m.text(x)
	case Num, int64, float64, bool, time.Time:
		if !m.numeric {
			return false
		}
	}
	return m.text(cellText(v, sqlType))
}

func (m matcher) text(s string) bool {
	if !m.ascii {
		return strings.Contains(strings.ToLower(s), m.low)
	}
	n := len(m.low)
	if n == 0 {
		return true
	}
	first := m.low[0]
	for i := 0; i+n <= len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != first {
			continue
		}
		j := 1
		for ; j < n; j++ {
			c := s[i+j]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != m.low[j] {
				break
			}
		}
		if j == n {
			return true
		}
	}
	return false
}

// ---- filters -----------------------------------------------------------------

type cond func(v any) bool

// parseFilter understands:
//
//	szöveg        tartalmazza (kis- és nagybetű mindegy)
//	=érték        pontosan egyenlő      !=érték / <>érték  nem egyenlő
//	>  >=  <  <=  összehasonlítás (számoknál számként, dátumnál dátumként)
//	a..b          a és b között (a határokkal együtt)
//	üres / null   NULL érték           !üres / !null  nem NULL
func parseFilter(expr string, kind Kind, sqlType string) (cond, error) {
	e := strings.TrimSpace(expr)
	if e == "" {
		return nil, nil
	}
	low := strings.ToLower(e)
	switch low {
	case "null", "üres", "(üres)":
		return func(v any) bool { return v == nil }, nil
	case "!null", "!üres", "nem üres":
		return func(v any) bool { return v != nil }, nil
	}
	cmpTo := func(lit string) (func(v any) (int, bool), error) {
		lit = strings.TrimSpace(lit)
		if kind == KindNumber || kind == KindBool {
			f, err := parseNum(lit)
			if err != nil {
				return nil, fmt.Errorf("nem szám: %q", lit)
			}
			return func(v any) (int, bool) {
				x, ok := number(v)
				if !ok {
					return 0, false
				}
				return cmp.Compare(x, f), true
			}, nil
		}
		ll := strings.ToLower(lit)
		return func(v any) (int, bool) {
			if v == nil {
				return 0, false
			}
			d := strings.ToLower(cellText(v, sqlType))
			if kind == KindDate && len(d) > len(ll) && strings.HasPrefix(d, ll) {
				return 0, true // "2024-01" = that month
			}
			return strings.Compare(d, ll), true
		}, nil
	}
	if a, b, ok := strings.Cut(e, ".."); ok && a != "" && b != "" {
		lo, err := cmpTo(a)
		if err != nil {
			return nil, err
		}
		hi, err := cmpTo(b)
		if err != nil {
			return nil, err
		}
		return func(v any) bool {
			x, ok1 := lo(v)
			y, ok2 := hi(v)
			return ok1 && ok2 && x >= 0 && y <= 0
		}, nil
	}
	for _, op := range []string{">=", "<=", "!=", "<>", ">", "<", "="} {
		if !strings.HasPrefix(e, op) {
			continue
		}
		c, err := cmpTo(e[len(op):])
		if err != nil {
			return nil, err
		}
		return func(v any) bool {
			r, ok := c(v)
			if !ok {
				return op == "!=" || op == "<>"
			}
			switch op {
			case ">=":
				return r >= 0
			case "<=":
				return r <= 0
			case ">":
				return r > 0
			case "<":
				return r < 0
			case "=":
				return r == 0
			}
			return r != 0
		}, nil
	}
	m := newMatcher(low)
	return func(v any) bool { return m.match(v, sqlType) }, nil
}

func parseNum(s string) (float64, error) {
	s = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), " ", ""), " ", "")
	if strings.Count(s, ",") == 1 && !strings.Contains(s, ".") {
		s = strings.Replace(s, ",", ".", 1)
	}
	return strconv.ParseFloat(s, 64)
}

// Fetch returns a page, filtering and sorting in memory.
func (r *Result) Fetch(q Query) Page {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := Page{Offset: q.Offset, Total: len(r.rows), Rows: [][]any{}}
	key := fmt.Sprintf("%q|%v|%s|%v", q.Search, sortedMap(q.Filters), q.Sort, q.Desc)
	if r.view == nil || r.viewKey != key {
		view, err := r.compute(q)
		if err != nil {
			p.Error = err.Error()
			p.Filtered = 0
			return p
		}
		r.view, r.viewKey = view, key
	}
	p.Filtered = len(r.view)
	lim := q.Limit
	if lim <= 0 || lim > 1000 {
		lim = 200
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	for i := q.Offset; i < len(r.view) && i < q.Offset+lim; i++ {
		row := r.rows[r.view[i]]
		out := make([]any, len(row))
		for c, v := range row {
			out[c] = Display(v, r.Cols[c].Type)
		}
		p.Rows = append(p.Rows, out)
	}
	return p
}

// Stats summarise the currently filtered rows (columns in parallel).
func (r *Result) Stats() []ColStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := r.view
	if idx == nil {
		idx = make([]int32, len(r.rows))
		for i := range idx {
			idx[i] = int32(i)
		}
	}
	out := make([]ColStats, len(r.Cols))
	parallel(len(r.Cols), func(c int) {
		st := &out[c]
		seen := map[any]struct{}{}
		var lo, hi any
		for _, i := range idx {
			v := r.rows[i][c]
			if v == nil {
				st.Nulls++
				continue
			}
			if len(seen) < 10000 {
				seen[v] = struct{}{}
			}
			if lo == nil || compare(v, lo) < 0 {
				lo = v
			}
			if hi == nil || compare(v, hi) > 0 {
				hi = v
			}
		}
		st.Distinct = len(seen)
		if lo != nil && r.Cols[c].Kind != KindBinary {
			st.Min = cellText(lo, r.Cols[c].Type)
			st.Max = cellText(hi, r.Cols[c].Type)
		}
	})
	return out
}

// parallel runs fn(0..n-1) on all processor cores.
func parallel(n int, fn func(i int)) {
	w := min(runtime.NumCPU(), n)
	if w <= 1 {
		for i := 0; i < n; i++ {
			fn(i)
		}
		return
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for range w {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= n {
					return
				}
				fn(i)
			}
		}()
	}
	wg.Wait()
}

func sortedMap(m map[string]string) []string {
	var out []string
	for k, v := range m {
		if strings.TrimSpace(v) != "" {
			out = append(out, k+"="+v)
		}
	}
	sort.Strings(out)
	return out
}

func (r *Result) compute(q Query) ([]int32, error) {
	type fc struct {
		col int
		c   cond
	}
	var fs []fc
	for name, expr := range q.Filters {
		ci := r.colIndex(name)
		if ci < 0 {
			continue
		}
		c, err := parseFilter(expr, r.Cols[ci].Kind, r.Cols[ci].Type)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		if c != nil {
			fs = append(fs, fc{ci, c})
		}
	}
	var sm *matcher
	if s := strings.ToLower(strings.TrimSpace(q.Search)); s != "" {
		m := newMatcher(s)
		sm = &m
	}
	keep := func(row []any) bool {
		for _, f := range fs {
			if !f.c(row[f.col]) {
				return false
			}
		}
		if sm == nil {
			return true
		}
		for c, v := range row {
			if sm.match(v, r.Cols[c].Type) {
				return true
			}
		}
		return false
	}
	n := len(r.rows)
	var view []int32
	if len(fs) == 0 && sm == nil {
		view = make([]int32, n)
		for i := range view {
			view[i] = int32(i)
		}
	} else {
		// big results are filtered in chunks on all cores, keeping row order
		const chunk = 1 << 16
		parts := make([][]int32, (n+chunk-1)/chunk)
		parallel(len(parts), func(p int) {
			var out []int32
			for i := p * chunk; i < n && i < (p+1)*chunk; i++ {
				if keep(r.rows[i]) {
					out = append(out, int32(i))
				}
			}
			parts[p] = out
		})
		total := 0
		for _, p := range parts {
			total += len(p)
		}
		view = make([]int32, 0, total)
		for _, p := range parts {
			view = append(view, p...)
		}
	}
	if sc := r.colIndex(q.Sort); sc >= 0 {
		r.sortView(view, sc, q.Desc)
	}
	return view, nil
}

// sortView sorts the row indexes by one column; NULL is the smallest. Equal
// values keep their order (the view is in row order, ties go by index).
func (r *Result) sortView(view []int32, c int, desc bool) {
	var nulls, vals []int32
	allNum, allTime := true, true
	for _, i := range view {
		v := r.rows[i][c]
		if v == nil {
			nulls = append(nulls, i)
			continue
		}
		vals = append(vals, i)
		if _, ok := number(v); !ok {
			allNum = false
		}
		if _, ok := v.(time.Time); !ok {
			allTime = false
		}
	}
	var by func(a, b int32) int
	switch {
	case allNum:
		fk := make([]float64, len(vals))
		pos := make([]int32, len(vals))
		for k, i := range vals {
			fk[k], _ = number(r.rows[i][c])
			pos[k] = int32(k)
		}
		slices.SortFunc(pos, func(a, b int32) int {
			if x := cmp.Compare(fk[a], fk[b]); x != 0 {
				if desc {
					return -x
				}
				return x
			}
			return cmp.Compare(a, b)
		})
		sorted := make([]int32, len(vals))
		for k, p := range pos {
			sorted[k] = vals[p]
		}
		vals = sorted
	case allTime:
		by = func(a, b int32) int { return r.rows[a][c].(time.Time).Compare(r.rows[b][c].(time.Time)) }
	default:
		lower := map[string]string{}
		sk := make([]string, len(vals))
		pos := make([]int32, len(vals))
		for k, i := range vals {
			s := cellText(r.rows[i][c], r.Cols[c].Type)
			l, ok := lower[s]
			if !ok {
				l = sortKey(s)
				if len(lower) < internMax {
					lower[s] = l
				}
			}
			sk[k], pos[k] = l, int32(k)
		}
		slices.SortFunc(pos, func(a, b int32) int {
			if x := strings.Compare(sk[a], sk[b]); x != 0 {
				if desc {
					return -x
				}
				return x
			}
			return cmp.Compare(a, b)
		})
		sorted := make([]int32, len(vals))
		for k, p := range pos {
			sorted[k] = vals[p]
		}
		vals = sorted
	}
	if by != nil {
		slices.SortFunc(vals, func(a, b int32) int {
			if x := by(a, b); x != 0 {
				if desc {
					return -x
				}
				return x
			}
			return cmp.Compare(a, b)
		})
	}
	// ascending: NULLs first; descending: NULLs last
	if desc {
		copy(view, vals)
		copy(view[len(vals):], nulls)
	} else {
		copy(view, nulls)
		copy(view[len(nulls):], vals)
	}
}

func (r *Result) colIndex(name string) int {
	if name == "" {
		return -1
	}
	for i, c := range r.Cols {
		if c.Name == name {
			return i
		}
	}
	for i, c := range r.Cols {
		if Norm(c.Name) == Norm(name) {
			return i
		}
	}
	return -1
}

// RowLimit is how many rows "Teljes tábla" loads (the user's setting; the
// free memory of the computer is watched while loading).
func RowLimit(want int) int {
	if want <= 0 {
		return DefaultMaxRows
	}
	return want
}
