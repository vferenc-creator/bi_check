package dbmap

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// DocFormat is the version of the map file layout.
const DocFormat = 1

// Doc is the team's hand-made map of one database: everything that is not
// in the database itself. It is small and changes often (each edit), while
// the fetched structure (Schema) is stored separately.
type Doc struct {
	Format    int                   `json:"format"`
	Server    string                `json:"server"`
	Database  string                `json:"database"`
	Rev       int64                 `json:"rev"`
	UpdatedAt time.Time             `json:"updatedAt"`
	UpdatedBy string                `json:"updatedBy,omitempty"`
	Tables    map[string]*TableMeta `json:"tables"` // key: Norm(schema.name)
	Relations []Relation            `json:"relations"`
	Groups    []Group               `json:"groups"`
	Notes     []Note                `json:"notes"`
	// Hidden FK relations from the database (by FK name) – not drawn.
	HiddenFKs []string `json:"hiddenFks,omitempty"`
}

// TableMeta is what the team added to one table.
type TableMeta struct {
	Desc      string              `json:"desc,omitempty"`
	DescBy    string              `json:"descBy,omitempty"`
	DescAt    *time.Time          `json:"descAt,omitempty"`
	X         float64             `json:"x"`
	Y         float64             `json:"y"`
	Placed    bool                `json:"placed"`
	Group     string              `json:"group,omitempty"` // Group.ID
	Collapsed bool                `json:"collapsed,omitempty"`
	FirstSeen time.Time           `json:"firstSeen"`
	New       bool                `json:"new,omitempty"` // appeared at the last fetch
	Columns   map[string]*ColMeta `json:"columns,omitempty"`
}

// ColMeta is what the team added to one column.
type ColMeta struct {
	Desc   string     `json:"desc,omitempty"`
	DescBy string     `json:"descBy,omitempty"`
	DescAt *time.Time `json:"descAt,omitempty"`
	New    bool       `json:"new,omitempty"` // appeared at the last fetch
}

// Relation is a link drawn by the team: From.FromCol (the "many" side,
// e.g. Ertekesites.PartnerID) refers to To.ToCol (e.g. Partner.PartnerID).
type Relation struct {
	ID      string    `json:"id"`
	From    string    `json:"from"`
	FromCol string    `json:"fromCol"`
	To      string    `json:"to"`
	ToCol   string    `json:"toCol"`
	Note    string    `json:"note,omitempty"`
	By      string    `json:"by,omitempty"`
	At      time.Time `json:"at"`
}

// Group is a coloured area of related tables ("Önköltség", "Értékesítés").
type Group struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// Note is a sticky note on the map.
type Note struct {
	ID    string    `json:"id"`
	Text  string    `json:"text"`
	Color string    `json:"color,omitempty"`
	X     float64   `json:"x"`
	Y     float64   `json:"y"`
	W     float64   `json:"w"`
	H     float64   `json:"h"`
	By    string    `json:"by,omitempty"`
	At    time.Time `json:"at"`
}

// NewDoc returns an empty map for a database.
func NewDoc(server, database string) *Doc {
	return &Doc{Format: DocFormat, Server: server, Database: database, Tables: map[string]*TableMeta{},
		Relations: []Relation{}, Groups: []Group{}, Notes: []Note{}}
}

func (d *Doc) fix() {
	if d.Tables == nil {
		d.Tables = map[string]*TableMeta{}
	}
	if d.Relations == nil {
		d.Relations = []Relation{}
	}
	if d.Groups == nil {
		d.Groups = []Group{}
	}
	if d.Notes == nil {
		d.Notes = []Note{}
	}
	for _, m := range d.Tables {
		if m.Columns == nil {
			m.Columns = map[string]*ColMeta{}
		}
	}
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- Fetch merge -------------------------------------------------------------

// Merge takes a freshly fetched structure: new tables and columns are marked
// "new" (except at the very first fetch), tables that disappeared are kept
// from prev with Gone set, and new tables get a place on the map.
// It returns the schema to store (fresh + gone tables).
func (d *Doc) Merge(prev, fresh *Schema, now time.Time) *Schema {
	d.fix()
	first := prev == nil || len(prev.Tables) == 0
	out := *fresh
	out.Tables = append([]Table(nil), fresh.Tables...)
	seen := map[string]bool{}
	for _, m := range d.Tables {
		m.New = false
		for _, c := range m.Columns {
			c.New = false
		}
	}
	for i := range out.Tables {
		t := &out.Tables[i]
		k := Norm(t.Key())
		seen[k] = true
		m := d.Tables[k]
		pt := prev.Find(t.Key())
		if m == nil {
			m = &TableMeta{FirstSeen: now, Columns: map[string]*ColMeta{}, Collapsed: len(t.Columns) > 18}
			d.Tables[k] = m
		}
		if !first && (pt == nil || pt.Gone) {
			m.New = true
		}
		for _, c := range t.Columns {
			if !first && pt != nil && !pt.Gone && pt.Col(c.Name) == nil {
				cm := m.Columns[Norm(c.Name)]
				if cm == nil {
					cm = &ColMeta{}
					m.Columns[Norm(c.Name)] = cm
				}
				cm.New = true
			}
		}
	}
	if prev != nil {
		for _, t := range prev.Tables {
			k := Norm(t.Key())
			if seen[k] {
				continue
			}
			if _, keep := d.Tables[k]; !keep {
				continue // forgotten
			}
			t.Gone = true
			out.Tables = append(out.Tables, t)
			seen[k] = true
		}
	}
	sort.SliceStable(out.Tables, func(i, j int) bool { return Norm(out.Tables[i].Key()) < Norm(out.Tables[j].Key()) })
	d.Layout(&out, true)
	return &out
}

// ---- Edits -------------------------------------------------------------------

// Op is one edit of the map, sent by the UI.
type Op struct {
	Op       string    `json:"op"`
	Table    string    `json:"table,omitempty"`
	Column   string    `json:"column,omitempty"`
	Text     string    `json:"text,omitempty"`
	Bool     bool      `json:"bool,omitempty"`
	ID       string    `json:"id,omitempty"`
	Pos      []Pos     `json:"pos,omitempty"`
	Group    *Group    `json:"group,omitempty"`
	Relation *Relation `json:"relation,omitempty"`
	Note     *Note     `json:"note,omitempty"`
	Tables   []string  `json:"tables,omitempty"`
}

// Pos is a table position.
type Pos struct {
	Table string  `json:"table"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
}

var errNoTable = errors.New("a tábla nem található a térképen – kérje le újra a szerkezetet")

const (
	maxDesc = 4000
	maxNote = 2000
)

func clip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if utf8.RuneCountInString(s) > n {
		r := []rune(s)
		s = string(r[:n])
	}
	return s
}

// Apply performs one edit. sch is the current structure (for validation).
func (d *Doc) Apply(op Op, sch *Schema, who string, now time.Time) error {
	d.fix()
	meta := func(key string) (*TableMeta, error) {
		if m := d.Tables[Norm(key)]; m != nil {
			return m, nil
		}
		return nil, errNoTable
	}
	switch op.Op {
	case "tableDesc":
		m, err := meta(op.Table)
		if err != nil {
			return err
		}
		m.Desc, m.DescBy, m.DescAt = clip(op.Text, maxDesc), who, &now
	case "colDesc":
		m, err := meta(op.Table)
		if err != nil {
			return err
		}
		if t := sch.Find(op.Table); t != nil && t.Col(op.Column) == nil {
			return fmt.Errorf("nincs ilyen mező: %s", op.Column)
		}
		cm := m.Columns[Norm(op.Column)]
		if cm == nil {
			cm = &ColMeta{}
			m.Columns[Norm(op.Column)] = cm
		}
		cm.Desc, cm.DescBy, cm.DescAt = clip(op.Text, maxDesc), who, &now
	case "move":
		for _, p := range op.Pos {
			if m := d.Tables[Norm(p.Table)]; m != nil {
				m.X, m.Y, m.Placed = round(p.X), round(p.Y), true
			}
		}
		for _, p := range op.Pos { // notes move with the same op ("note:<id>")
			if id, ok := strings.CutPrefix(p.Table, "note:"); ok {
				for i := range d.Notes {
					if d.Notes[i].ID == id {
						d.Notes[i].X, d.Notes[i].Y = round(p.X), round(p.Y)
					}
				}
			}
		}
	case "collapse":
		m, err := meta(op.Table)
		if err != nil {
			return err
		}
		m.Collapsed = op.Bool
	case "setGroup":
		if op.ID != "" && d.group(op.ID) == nil {
			return errors.New("a csoport már nem létezik")
		}
		for _, t := range op.Tables {
			if m := d.Tables[Norm(t)]; m != nil {
				m.Group = op.ID
			}
		}
	case "groupSave":
		g := op.Group
		if g == nil || strings.TrimSpace(g.Name) == "" {
			return errors.New("a csoport neve kötelező")
		}
		g.Name = clip(g.Name, 80)
		if g.ID == "" {
			g.ID = newID()
		}
		if old := d.group(g.ID); old != nil {
			*old = *g
		} else {
			d.Groups = append(d.Groups, *g)
		}
	case "groupDelete":
		out := d.Groups[:0]
		for _, g := range d.Groups {
			if g.ID != op.ID {
				out = append(out, g)
			}
		}
		d.Groups = out
		for _, m := range d.Tables {
			if m.Group == op.ID {
				m.Group = ""
			}
		}
	case "relAdd":
		r := op.Relation
		if r == nil {
			return errors.New("hiányzó kapcsolat")
		}
		ft, tt := sch.Find(r.From), sch.Find(r.To)
		if ft == nil || tt == nil {
			return errNoTable
		}
		fc, tc := ft.Col(r.FromCol), tt.Col(r.ToCol)
		if fc == nil || tc == nil {
			return errors.New("a kapcsolat egyik mezője nem létezik")
		}
		if Norm(r.From) == Norm(r.To) && Norm(r.FromCol) == Norm(r.ToCol) {
			return errors.New("egy mezőt nem lehet önmagához kötni")
		}
		for _, x := range d.Relations {
			if x.ID != r.ID && sameRel(x, *r) {
				return errors.New("ez a kapcsolat már létezik")
			}
		}
		for _, fk := range sch.FKs {
			if len(fk.FromCols) == 1 && Norm(fk.From) == Norm(r.From) && Norm(fk.FromCols[0]) == Norm(r.FromCol) &&
				Norm(fk.To) == Norm(r.To) && Norm(fk.ToCols[0]) == Norm(r.ToCol) {
				return errors.New("ez a kapcsolat az adatbázisban is definiálva van (idegen kulcs)")
			}
		}
		nr := Relation{ID: r.ID, From: ft.Key(), FromCol: fc.Name, To: tt.Key(), ToCol: tc.Name, Note: clip(r.Note, 500), By: who, At: now}
		if nr.ID == "" {
			nr.ID = newID()
		}
		replaced := false
		for i := range d.Relations {
			if d.Relations[i].ID == nr.ID {
				d.Relations[i], replaced = nr, true
			}
		}
		if !replaced {
			d.Relations = append(d.Relations, nr)
		}
	case "relDelete":
		out := d.Relations[:0]
		for _, r := range d.Relations {
			if r.ID != op.ID {
				out = append(out, r)
			}
		}
		d.Relations = out
	case "relNote":
		for i := range d.Relations {
			if d.Relations[i].ID == op.ID {
				d.Relations[i].Note = clip(op.Text, 500)
			}
		}
	case "fkHide":
		d.HiddenFKs = without(d.HiddenFKs, op.ID)
		if op.Bool {
			d.HiddenFKs = append(d.HiddenFKs, op.ID)
		}
	case "noteSave":
		n := op.Note
		if n == nil {
			return errors.New("hiányzó jegyzet")
		}
		n.Text = clip(n.Text, maxNote)
		n.X, n.Y = round(n.X), round(n.Y)
		if n.W < 120 {
			n.W = 200
		}
		if n.H < 60 {
			n.H = 120
		}
		n.By, n.At = who, now
		if n.ID == "" {
			n.ID = newID()
		}
		for i := range d.Notes {
			if d.Notes[i].ID == n.ID {
				d.Notes[i] = *n
				return nil
			}
		}
		d.Notes = append(d.Notes, *n)
	case "noteDelete":
		out := d.Notes[:0]
		for _, n := range d.Notes {
			if n.ID != op.ID {
				out = append(out, n)
			}
		}
		d.Notes = out
	case "forget":
		// a table that no longer exists: drop it with its descriptions
		k := Norm(op.Table)
		if t := sch.Find(op.Table); t != nil && !t.Gone {
			return errors.New("csak az adatbázisból eltűnt táblát lehet eltávolítani")
		}
		delete(d.Tables, k)
		out := d.Relations[:0]
		for _, r := range d.Relations {
			if Norm(r.From) != k && Norm(r.To) != k {
				out = append(out, r)
			}
		}
		d.Relations = out
	case "layout":
		d.Layout(sch, false)
	default:
		return fmt.Errorf("ismeretlen módosítás: %s", op.Op)
	}
	return nil
}

func (d *Doc) group(id string) *Group {
	for i := range d.Groups {
		if d.Groups[i].ID == id {
			return &d.Groups[i]
		}
	}
	return nil
}

func sameRel(a, b Relation) bool {
	return Norm(a.From) == Norm(b.From) && Norm(a.FromCol) == Norm(b.FromCol) && Norm(a.To) == Norm(b.To) && Norm(a.ToCol) == Norm(b.ToCol)
}

func without(s []string, x string) []string {
	out := s[:0]
	for _, v := range s {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

func round(v float64) float64 { return float64(int64(v + 0.5*sign(v))) }

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}
