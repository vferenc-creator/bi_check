package dbmap

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	FetchedAt time.Time   `json:"fetchedAt"`
	TookMs    int64       `json:"tookMs"`

	rows [][]any // cells: nil, int64, float64, Num, string, bool, time.Time, []byte

	mu      sync.Mutex
	viewKey string
	view    []int32
}

// MaxCells limits the memory a result may take (rows × columns).
const MaxCells = 3_000_000

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

// Add appends one row (cells already normalised by Cell).
func (r *Result) Add(row []any) {
	r.rows = append(r.rows, row)
	r.Total = len(r.rows)
}

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
			switch {
			case fa < fb:
				return -1
			case fa > fb:
				return 1
			}
			return 0
		}
	}
	if ta, ok := a.(time.Time); ok {
		if tb, ok := b.(time.Time); ok {
			return ta.Compare(tb)
		}
	}
	sa, sb := fmt.Sprint(Display(a, "")), fmt.Sprint(Display(b, ""))
	return strings.Compare(strings.ToLower(sa), strings.ToLower(sb))
}

// ---- filters -----------------------------------------------------------------

type cond func(v any, disp string) bool

// parseFilter understands:
//
//	szöveg        tartalmazza (kis- és nagybetű mindegy)
//	=érték        pontosan egyenlő      !=érték / <>érték  nem egyenlő
//	>  >=  <  <=  összehasonlítás (számoknál számként, dátumnál dátumként)
//	a..b          a és b között (a határokkal együtt)
//	üres / null   NULL érték           !üres / !null  nem NULL
func parseFilter(expr string, kind Kind) (cond, error) {
	e := strings.TrimSpace(expr)
	if e == "" {
		return nil, nil
	}
	low := strings.ToLower(e)
	switch low {
	case "null", "üres", "(üres)":
		return func(v any, _ string) bool { return v == nil }, nil
	case "!null", "!üres", "nem üres":
		return func(v any, _ string) bool { return v != nil }, nil
	}
	cmp := func(lit string) (func(v any, disp string) (int, bool), error) {
		lit = strings.TrimSpace(lit)
		if kind == KindNumber || kind == KindBool {
			f, err := parseNum(lit)
			if err != nil {
				return nil, fmt.Errorf("nem szám: %q", lit)
			}
			return func(v any, _ string) (int, bool) {
				x, ok := number(v)
				if !ok {
					return 0, false
				}
				switch {
				case x < f:
					return -1, true
				case x > f:
					return 1, true
				}
				return 0, true
			}, nil
		}
		ll := strings.ToLower(lit)
		return func(v any, disp string) (int, bool) {
			if v == nil {
				return 0, false
			}
			d := strings.ToLower(disp)
			if kind == KindDate && len(d) > len(ll) && strings.HasPrefix(d, ll) {
				return 0, true // "2024-01" = that month
			}
			return strings.Compare(d, ll), true
		}, nil
	}
	if a, b, ok := strings.Cut(e, ".."); ok && a != "" && b != "" {
		lo, err := cmp(a)
		if err != nil {
			return nil, err
		}
		hi, err := cmp(b)
		if err != nil {
			return nil, err
		}
		return func(v any, d string) bool {
			x, ok1 := lo(v, d)
			y, ok2 := hi(v, d)
			return ok1 && ok2 && x >= 0 && y <= 0
		}, nil
	}
	for _, op := range []string{">=", "<=", "!=", "<>", ">", "<", "="} {
		if !strings.HasPrefix(e, op) {
			continue
		}
		c, err := cmp(e[len(op):])
		if err != nil {
			return nil, err
		}
		return func(v any, d string) bool {
			r, ok := c(v, d)
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
	return func(v any, d string) bool { return v != nil && strings.Contains(strings.ToLower(d), low) }, nil
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

// Stats summarise the currently filtered rows.
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
	for c := range r.Cols {
		st := &out[c]
		seen := map[string]bool{}
		var lo, hi any
		for _, i := range idx {
			v := r.rows[i][c]
			if v == nil {
				st.Nulls++
				continue
			}
			if len(seen) < 10000 {
				seen[fmt.Sprint(Display(v, r.Cols[c].Type))] = true
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
			st.Min = fmt.Sprint(Display(lo, r.Cols[c].Type))
			st.Max = fmt.Sprint(Display(hi, r.Cols[c].Type))
		}
	}
	return out
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
		c, err := parseFilter(expr, r.Cols[ci].Kind)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		if c != nil {
			fs = append(fs, fc{ci, c})
		}
	}
	search := strings.ToLower(strings.TrimSpace(q.Search))
	view := make([]int32, 0, len(r.rows))
	disp := func(v any, c int) string {
		if v == nil {
			return ""
		}
		return fmt.Sprint(Display(v, r.Cols[c].Type))
	}
rows:
	for i, row := range r.rows {
		for _, f := range fs {
			if !f.c(row[f.col], disp(row[f.col], f.col)) {
				continue rows
			}
		}
		if search != "" {
			hit := false
			for c, v := range row {
				if v != nil && strings.Contains(strings.ToLower(disp(v, c)), search) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		view = append(view, int32(i))
	}
	if sc := r.colIndex(q.Sort); sc >= 0 {
		sort.SliceStable(view, func(a, b int) bool {
			c := compare(r.rows[view[a]][sc], r.rows[view[b]][sc])
			if q.Desc {
				return c > 0
			}
			return c < 0
		})
	}
	return view, nil
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

// RowLimit is how many rows may be loaded for a table with ncols columns.
func RowLimit(want, ncols int) int {
	if ncols < 1 {
		ncols = 1
	}
	cap := MaxCells / ncols
	if want <= 0 || want > cap {
		return cap
	}
	return want
}
