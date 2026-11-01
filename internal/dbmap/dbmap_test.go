package dbmap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDSN(t *testing.T) {
	cases := []struct {
		server, host, inst string
		port               uint64
	}{
		{"EF-SQL01", "EF-SQL01", "", 0},
		{`EF-SQL01\BI`, "EF-SQL01", "BI", 0},
		{"EF-SQL01,1444", "EF-SQL01", "", 1444},
		{"ef-sql01.energofish.local:1433", "ef-sql01.energofish.local", "", 1433},
		{`\\EF-SQL01\BI`, "EF-SQL01", "BI", 0},
	}
	for _, c := range cases {
		cfg, err := Config{Server: c.server, Database: "BI", Password: "p;w=d}"}.DSN()
		if err != nil {
			t.Fatalf("%s: %v", c.server, err)
		}
		if cfg.Host != c.host || cfg.Instance != c.inst || cfg.Port != c.port || cfg.Database != "BI" || cfg.User != "" || cfg.Password != "" {
			t.Fatalf("%s: %+v", c.server, cfg)
		}
		if !cfg.ReadOnlyIntent || cfg.AppName != "BI Output Monitor" {
			t.Fatalf("read-only intent / app name: %+v", cfg)
		}
	}
	// the separate port field (as in the KNIME connector: EF-BI / 1433 / Dashboard)
	if cfg, err := (Config{Server: "EF-BI", Port: 1433, Database: "Dashboard"}).DSN(); err != nil || cfg.Host != "EF-BI" || cfg.Port != 1433 || cfg.Database != "Dashboard" {
		t.Fatalf("port field: %+v %v", cfg, err)
	}
	if cfg, err := (Config{Server: "EF-BI,1500", Port: 1433, Database: "D"}).DSN(); err != nil || cfg.Port != 1433 {
		t.Fatalf("port field wins: %+v %v", cfg, err)
	}
	if _, err := (Config{Server: "EF-BI", Port: 70000, Database: "D"}).DSN(); err == nil {
		t.Fatal("invalid port")
	}
	cfg, err := Config{Server: "x", Database: "BI", Auth: "sql", User: "olvaso", Password: "p;w=d}"}.DSN()
	if err != nil || cfg.User != "olvaso" || cfg.Password != "p;w=d}" {
		t.Fatalf("sql auth: %+v %v", cfg, err)
	}
	for _, bad := range []Config{{Server: "", Database: "BI"}, {Server: "x;y=1", Database: "BI"}, {Server: "x", Database: ""}, {Server: "x", Database: "a;b"},
		{Server: "x,99999", Database: "a"}, {Server: "x", Database: "a", Auth: "sql"}} {
		if _, err := bad.DSN(); err == nil {
			t.Fatalf("expected error for %+v", bad)
		}
	}
}

func TestTypeNameAndCells(t *testing.T) {
	for _, c := range []struct {
		in   string
		ml   int64
		p, s int64
		want string
	}{{"nvarchar", 100, 0, 0, "nvarchar(50)"}, {"varchar", -1, 0, 0, "varchar(max)"}, {"decimal", 9, 18, 2, "decimal(18,2)"}, {"int", 4, 10, 0, "int"},
		{"datetime2", 8, 27, 0, "datetime2(0)"}, {"datetime2", 8, 27, 7, "datetime2"}} {
		if got := TypeName(c.in, c.ml, c.p, c.s); got != c.want {
			t.Fatalf("%s: %s", c.want, got)
		}
	}
	g := Cell([]byte{0x33, 0x22, 0x11, 0x00, 0x55, 0x44, 0x77, 0x66, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}, "UNIQUEIDENTIFIER")
	if g != "00112233-4455-6677-8899-AABBCCDDEEFF" {
		t.Fatalf("guid %v", g)
	}
	if n := Cell([]byte("12.50"), "DECIMAL").(Num); n.S != "12.50" || n.F != 12.5 {
		t.Fatalf("%+v", n)
	}
	if v := Cell([]byte{0, 1, 2}, "VARBINARY"); v != "0x000102" {
		t.Fatalf("%v", v)
	}
	d := time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)
	if Display(d, "date") != "2024-01-31" || Display(d.Add(6*time.Hour+500*time.Millisecond), "datetime2") != "2024-01-31 06:00:00.5" {
		t.Fatal(Display(d.Add(6*time.Hour+500*time.Millisecond), "datetime2"))
	}
	if Quote("a]b") != "[a]]b]" {
		t.Fatal(Quote("a]b"))
	}
}

func demoSchema(t *testing.T, d *Demo) *Schema {
	s, err := d.Schema(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMergeNewAndGone(t *testing.T) {
	demo := &Demo{}
	doc := NewDoc("DEMO", "BI_DEMO")
	now := time.Now()
	s1 := doc.Merge(nil, demoSchema(t, demo), now)
	for k, m := range doc.Tables {
		if m.New || !m.Placed {
			t.Fatalf("first fetch: nothing is new, everything placed: %s %+v", k, m)
		}
	}
	// a new table and a new column; one table disappears
	demo.Extra = []Table{{Schema: "dbo", Name: "Uj_Tabla", Rows: 3, Columns: []Column{col("ID", "int", "pk")}}}
	s2raw := demoSchema(t, demo)
	for i := range s2raw.Tables {
		if s2raw.Tables[i].Name == "Termek" {
			s2raw.Tables[i].Columns = append(s2raw.Tables[i].Columns, col("Vonalkod", "nvarchar(20)", "null"))
		}
	}
	var kept []Table
	for _, tb := range s2raw.Tables {
		if tb.Name != "PartnerKategoria" {
			kept = append(kept, tb)
		}
	}
	s2raw.Tables = kept
	if err := doc.Apply(Op{Op: "tableDesc", Table: "elemzes.PartnerKategoria", Text: "ABC elemzés"}, s1, "Anna", now); err != nil {
		t.Fatal(err)
	}
	s2 := doc.Merge(s1, s2raw, now.Add(time.Hour))
	if m := doc.Tables["dbo.uj_tabla"]; m == nil || !m.New || !m.Placed {
		t.Fatalf("new table: %+v", m)
	}
	if doc.Tables["dim.termek"].New || !doc.Tables["dim.termek"].Columns["vonalkod"].New {
		t.Fatal("new column must be marked")
	}
	gone := s2.Find("elemzes.PartnerKategoria")
	if gone == nil || !gone.Gone || doc.Tables["elemzes.partnerkategoria"].Desc != "ABC elemzés" {
		t.Fatalf("gone table must be kept with its description: %+v", gone)
	}
	// forgetting is only allowed for gone tables
	if err := doc.Apply(Op{Op: "forget", Table: "dim.Partner"}, s2, "x", now); err == nil {
		t.Fatal("existing table cannot be forgotten")
	}
	if err := doc.Apply(Op{Op: "forget", Table: "elemzes.PartnerKategoria"}, s2, "x", now); err != nil {
		t.Fatal(err)
	}
	s3 := doc.Merge(s2, s2raw, now.Add(2*time.Hour))
	if s3.Find("elemzes.PartnerKategoria") != nil {
		t.Fatal("forgotten table must not come back")
	}
	if doc.Tables["dbo.uj_tabla"].New {
		t.Fatal("'new' lasts until the next fetch")
	}
}

func TestOps(t *testing.T) {
	doc := NewDoc("DEMO", "BI_DEMO")
	sch := doc.Merge(nil, demoSchema(t, &Demo{}), time.Now())
	now := time.Now()
	ok := func(op Op) {
		t.Helper()
		if err := doc.Apply(op, sch, "Anna (PC1)", now); err != nil {
			t.Fatalf("%s: %v", op.Op, err)
		}
	}
	bad := func(op Op) {
		t.Helper()
		if err := doc.Apply(op, sch, "Anna", now); err == nil {
			t.Fatalf("%s should fail", op.Op)
		}
	}
	ok(Op{Op: "colDesc", Table: "fact.OnkoltsegHavi", Column: "onkoltseg", Text: "  anyag + bér + rezsi  "})
	if c := doc.Tables["fact.onkoltseghavi"].Columns["onkoltseg"]; c.Desc != "anyag + bér + rezsi" || c.DescBy != "Anna (PC1)" {
		t.Fatalf("%+v", c)
	}
	bad(Op{Op: "colDesc", Table: "fact.OnkoltsegHavi", Column: "nincs"})
	bad(Op{Op: "tableDesc", Table: "dbo.nincs"})
	ok(Op{Op: "relAdd", Relation: &Relation{From: "fact.onkoltseghavi", FromCol: "termekid", To: "dim.termek", ToCol: "TermekID"}})
	if r := doc.Relations[0]; r.From != "fact.OnkoltsegHavi" || r.FromCol != "TermekID" || r.ID == "" {
		t.Fatalf("names are taken from the database: %+v", r)
	}
	bad(Op{Op: "relAdd", Relation: &Relation{From: "fact.OnkoltsegHavi", FromCol: "TermekID", To: "dim.Termek", ToCol: "TermekID"}})  // duplicate
	bad(Op{Op: "relAdd", Relation: &Relation{From: "fact.Ertekesites", FromCol: "PartnerID", To: "dim.Partner", ToCol: "PartnerID"}}) // FK exists
	bad(Op{Op: "relAdd", Relation: &Relation{From: "fact.Ertekesites", FromCol: "Nincs", To: "dim.Partner", ToCol: "PartnerID"}})
	ok(Op{Op: "groupSave", Group: &Group{Name: "Önköltség", Color: "#F2A531"}})
	gid := doc.Groups[0].ID
	ok(Op{Op: "setGroup", ID: gid, Tables: []string{"fact.OnkoltsegHavi", "dim.Termek"}})
	ok(Op{Op: "move", Pos: []Pos{{Table: "dim.Termek", X: 10.4, Y: -20.6}}})
	if m := doc.Tables["dim.termek"]; m.X != 10 || m.Y != -21 || m.Group != gid {
		t.Fatalf("%+v", m)
	}
	ok(Op{Op: "noteSave", Note: &Note{Text: "Havonta 5-én zárul", X: 5, Y: 5}})
	nid := doc.Notes[0].ID
	ok(Op{Op: "move", Pos: []Pos{{Table: "note:" + nid, X: 100, Y: 50}}})
	if doc.Notes[0].X != 100 || doc.Notes[0].W < 120 {
		t.Fatalf("%+v", doc.Notes[0])
	}
	ok(Op{Op: "groupDelete", ID: gid})
	if doc.Tables["dim.termek"].Group != "" || len(doc.Groups) != 0 {
		t.Fatal("group delete must ungroup its tables")
	}
	ok(Op{Op: "relDelete", ID: doc.Relations[0].ID})
	ok(Op{Op: "noteDelete", ID: nid})
	ok(Op{Op: "fkHide", ID: "FK_Ertekesites_Termek", Bool: true})
	bad(Op{Op: "valami"})
	if len(doc.Relations) != 0 || len(doc.Notes) != 0 || len(doc.HiddenFKs) != 1 {
		t.Fatalf("%+v", doc)
	}
}

func TestLayoutNoOverlap(t *testing.T) {
	demo := &Demo{}
	for i := 0; i < 40; i++ {
		demo.Extra = append(demo.Extra, Table{Schema: "stg", Name: fmt.Sprintf("T%02d", i), Columns: make([]Column, 1+i%25)})
	}
	doc := NewDoc("D", "B")
	sch := doc.Merge(nil, demoSchema(t, demo), time.Now())
	if err := doc.Apply(Op{Op: "groupSave", Group: &Group{ID: "g", Name: "Tény"}}, sch, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = doc.Apply(Op{Op: "setGroup", ID: "g", Tables: []string{"fact.Ertekesites", "fact.OnkoltsegHavi"}}, sch, "", time.Now())
	_ = doc.Apply(Op{Op: "noteSave", Note: &Note{ID: "n1", Text: "x", X: 300, Y: 300, W: 200, H: 120}}, sch, "", time.Now())
	_ = doc.Apply(Op{Op: "layout"}, sch, "", time.Now())
	type box struct{ x0, y0, x1, y1 float64 }
	kc := doc.keyColumns(sch)
	var boxes []box
	for i := range sch.Tables {
		tb := &sch.Tables[i]
		m := doc.Tables[Norm(tb.Key())]
		boxes = append(boxes, box{m.X, m.Y, m.X + CardW, m.Y + CardHeight(tb, m, kc[Norm(tb.Key())])})
	}
	for _, n := range doc.Notes {
		boxes = append(boxes, box{n.X, n.Y, n.X + n.W, n.Y + n.H})
	}
	for i := range boxes {
		for j := i + 1; j < len(boxes); j++ {
			a, b := boxes[i], boxes[j]
			if a.x0 < b.x1 && b.x0 < a.x1 && a.y0 < b.y1 && b.y0 < a.y1 {
				t.Fatalf("cards %d and %d overlap: %+v %+v", i, j, a, b)
			}
		}
	}
}

func TestStoreSharedConcurrent(t *testing.T) {
	shared := t.TempDir()
	stores := []*Store{
		{Shared: shared, Local: t.TempDir(), Who: "Anna (PC1)"},
		{Shared: shared, Local: t.TempDir(), Who: "Péter (PC2)"},
	}
	fresh := demoSchema(t, &Demo{})
	if _, err := stores[0].Update("DEMO", "BI_DEMO", func(d *Doc, sch *Schema) (*Schema, error) {
		return d.Merge(sch, fresh, time.Now()), nil
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		for si, s := range stores {
			wg.Add(1)
			go func(i, si int, s *Store) {
				defer wg.Done()
				_, err := s.Update("DEMO", "BI_DEMO", func(d *Doc, sch *Schema) (*Schema, error) {
					return nil, d.Apply(Op{Op: "noteSave", Note: &Note{ID: fmt.Sprintf("n%d-%d", si, i), Text: "x"}}, sch, s.Who, time.Now())
				})
				errs <- err
			}(i, si, s)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	snap, err := stores[1].Load("DEMO", "BI_DEMO")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Doc.Notes) != 40 || snap.Doc.Rev != 41 || snap.Schema == nil || len(snap.Schema.Tables) != len(fresh.Tables) {
		t.Fatalf("no edit may be lost: notes=%d rev=%d", len(snap.Doc.Notes), snap.Doc.Rev)
	}
	// only our own sub-folder is used in the shared folder
	es, _ := os.ReadDir(shared)
	if len(es) != 1 || es[0].Name() != FolderName {
		t.Fatalf("unexpected entries in the shared folder: %v", es)
	}
	files, _ := os.ReadDir(filepath.Join(shared, FolderName))
	for _, f := range files {
		if strings.HasPrefix(f.Name(), ".tmp-") || strings.HasSuffix(f.Name(), ".lock") {
			t.Fatalf("leftover %s", f.Name())
		}
	}
	// offline: the local copy is shown, edits are refused
	off := &Store{Shared: filepath.Join(shared, "nincs-ilyen", "x"), Local: stores[1].Local, Who: "Péter"}
	_ = off
	if err := os.Rename(filepath.Join(shared, FolderName, Key("DEMO", "BI_DEMO")+".json"), filepath.Join(shared, FolderName, "broken")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(shared, FolderName, Key("DEMO", "BI_DEMO")+".json"), []byte("{fél"), 0o644)
	snap, err = stores[1].Load("DEMO", "BI_DEMO")
	if err != nil || !snap.FromCache || len(snap.Doc.Notes) != 40 {
		t.Fatalf("damaged shared file → local copy: %+v %v", snap, err)
	}
	if _, err := stores[1].Update("DEMO", "BI_DEMO", func(d *Doc, sch *Schema) (*Schema, error) { return nil, nil }); err == nil {
		t.Fatal("must not overwrite a shared map it could not read")
	}
}

func TestStoreLocalThenShared(t *testing.T) {
	local := t.TempDir()
	s := &Store{Local: local, Who: "Anna"}
	if _, err := s.Update("S", "D", func(d *Doc, sch *Schema) (*Schema, error) {
		return d.Merge(sch, demoSchema(t, &Demo{}), time.Now()), d.Apply(Op{Op: "noteSave", Note: &Note{Text: "helyi"}}, nil, "Anna", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	shared := t.TempDir()
	s2 := &Store{Shared: shared, Local: local, Who: "Anna"}
	snap, err := s2.Load("S", "D")
	if err != nil || len(snap.Doc.Notes) != 1 || snap.Schema == nil {
		t.Fatalf("switching to shared mode starts from the local map: %+v %v", snap, err)
	}
}

func TestResultFilterSort(t *testing.T) {
	r := NewResult("t", []ResultCol{{Name: "ID", Type: "int", Kind: KindNumber}, {Name: "Nev", Type: "nvarchar", Kind: KindText},
		{Name: "Ertek", Type: "decimal", Kind: KindNumber}, {Name: "Datum", Type: "date", Kind: KindDate}})
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 100; i++ {
		var name any = fmt.Sprintf("Partner %d", i)
		if i%10 == 0 {
			name = nil
		}
		v := float64(i) * 1.5
		r.Add([]any{int64(i), name, Num{S: fmt.Sprintf("%.2f", v), F: v}, day.AddDate(0, 0, i)})
	}
	page := func(q Query) Page {
		t.Helper()
		p := r.Fetch(q)
		if p.Error != "" {
			t.Fatal(p.Error)
		}
		return p
	}
	if p := page(Query{Filters: map[string]string{"ID": ">=95"}}); p.Filtered != 6 {
		t.Fatalf(">= %d", p.Filtered)
	}
	if p := page(Query{Filters: map[string]string{"Ertek": "10..15"}}); p.Filtered != 4 { // 10.5 12 13.5 15
		t.Fatalf(".. %d", p.Filtered)
	}
	if p := page(Query{Filters: map[string]string{"Ertek": "<3,5"}}); p.Filtered != 2 {
		t.Fatalf("comma decimal %d", p.Filtered)
	}
	if p := page(Query{Filters: map[string]string{"Nev": "üres"}}); p.Filtered != 10 {
		t.Fatalf("null %d", p.Filtered)
	}
	if p := page(Query{Filters: map[string]string{"Nev": "partner 1"}}); p.Filtered != 10 { // 1 and 11..19; 10 and 100 are NULL
		t.Fatalf("contains %d", p.Filtered)
	}
	if p := page(Query{Filters: map[string]string{"Datum": "2024-02"}}); p.Filtered != 29 {
		t.Fatalf("plain text on dates is 'contains' %d", p.Filtered)
	}
	if p := page(Query{Filters: map[string]string{"Datum": "=2024-02"}}); p.Filtered != 29 {
		t.Fatalf("=month %d", p.Filtered)
	}
	if p := page(Query{Filters: map[string]string{"Datum": "2024-03-01..2024-03-05"}}); p.Filtered != 5 {
		t.Fatalf("date range %d", p.Filtered)
	}
	p := page(Query{Sort: "Ertek", Desc: true, Limit: 3})
	if p.Rows[0][0] != "100" || p.Rows[0][2] != "150.00" || len(p.Rows) != 3 || p.Filtered != 100 {
		t.Fatalf("%v", p.Rows)
	}
	p = page(Query{Sort: "Nev", Limit: 1})
	if p.Rows[0][1] != nil {
		t.Fatal("NULL sorts first ascending")
	}
	p = page(Query{Search: "partner 99"})
	if p.Filtered != 1 {
		t.Fatalf("search %d", p.Filtered)
	}
	if p := r.Fetch(Query{Filters: map[string]string{"ID": ">abc"}}); p.Error == "" {
		t.Fatal("invalid number filter must be reported")
	}
	st := r.Stats()
	if st[1].Nulls != 0 && st[0].Min != "1" {
		t.Fatalf("%+v", st)
	}
	_ = r.Fetch(Query{})
	st = r.Stats()
	if st[1].Nulls != 10 || st[0].Min != "1" || st[0].Max != "100" || st[0].Distinct != 100 {
		t.Fatalf("%+v", st)
	}
	if RowLimit(100000, 60) != 50000 || RowLimit(100, 10) != 100 {
		t.Fatal("row limit")
	}
}

func TestDemoRows(t *testing.T) {
	d := &Demo{}
	sch := demoSchema(t, d)
	tb := sch.Find("fact.Ertekesites")
	res, err := d.Rows(context.Background(), tb, 100, nil)
	if err != nil || res.Total != 100 || !res.Truncated {
		t.Fatalf("%+v %v", res, err)
	}
	all, _ := d.Rows(context.Background(), sch.Find("dim.Partner"), 100000, nil)
	if all.Total != 412 || all.Truncated {
		t.Fatalf("%d %v", all.Total, all.Truncated)
	}
}

func TestQuoteProc(t *testing.T) {
	ok := map[string]string{"meta.leiras_beallit": "[meta].[leiras_beallit]", " leiras_beallit ": "[leiras_beallit]", "meta.leírás_beállít": "[meta].[leírás_beállít]"}
	for in, want := range ok {
		if got, err := QuoteProc(in); err != nil || got != want {
			t.Fatalf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "meta.x; DROP TABLE y", "a.b.c", "x'--", "[meta].[x]", "meta. x", "1abc", "meta.leiras beallit"} {
		if _, err := QuoteProc(bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}

func TestDemoDescriptions(t *testing.T) {
	d := &Demo{}
	if err := d.SetDescription(context.Background(), DefaultDescProc, "dim", "partner", "nev", "A partner neve"); err != nil {
		t.Fatal(err)
	}
	ds, err := d.Descriptions(context.Background(), "dim", "Partner")
	if err != nil || ds.Columns["Nev"] != "A partner neve" || ds.Table == "" {
		t.Fatalf("%+v %v", ds, err)
	}
	if s := demoSchema(t, d); s.Find("dim.Partner").Col("Nev").Desc != "A partner neve" {
		t.Fatal("the next structure fetch shows it too")
	}
	if err := d.SetDescription(context.Background(), DefaultDescProc, "dim", "Partner", "nincs", "x"); err == nil {
		t.Fatal("unknown column")
	}
}
