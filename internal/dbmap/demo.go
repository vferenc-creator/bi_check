package dbmap

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"
)

var (
	_ Source = (*Demo)(nil)
	_ Source = (*MSSQL)(nil)
)

// Demo is an in-memory sample database (development server, tests and the
// "Bemutató adatbázis" option) – shaped like a small cleaned BI database.
type Demo struct {
	// Extra tables appear at the next Schema call (to try "new table").
	Extra []Table

	mu    sync.Mutex
	descs map[string]string // Norm(schema.table)+"|"+Norm(column) → description
	Calls []string          // procedure calls (tests)
}

func (d *Demo) tables() []Table {
	out := append(append([]Table(nil), demoTables...), d.Extra...)
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range out {
		t := &out[i]
		t.Columns = append([]Column(nil), t.Columns...)
		k := Norm(t.Key())
		if v, ok := d.descs[k+"|"]; ok {
			t.Desc = v
		}
		for j := range t.Columns {
			if v, ok := d.descs[k+"|"+Norm(t.Columns[j].Name)]; ok {
				t.Columns[j].Desc = v
			}
		}
	}
	return out
}

func (d *Demo) Descriptions(_ context.Context, schema, table string) (*Descs, error) {
	for _, t := range d.tables() {
		if Norm(t.Key()) == Norm(schema+"."+table) {
			out := &Descs{Table: t.Desc, Columns: map[string]string{}}
			for _, c := range t.Columns {
				out.Columns[c.Name] = c.Desc
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("a(z) %s.%s tábla nem található az adatbázisban", schema, table)
}

func (d *Demo) SetDescription(_ context.Context, proc, schema, table, column, text string) error {
	q, err := QuoteProc(proc)
	if err != nil {
		return err
	}
	var found *Table
	for _, t := range d.tables() {
		if Norm(t.Key()) == Norm(schema+"."+table) {
			t := t
			found = &t
		}
	}
	if found == nil || (column != "" && found.Col(column) == nil) {
		return errors.New("SQL Server: a megadott tábla vagy mező nem létezik")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.descs == nil {
		d.descs = map[string]string{}
	}
	d.descs[Norm(schema+"."+table)+"|"+Norm(column)] = text
	d.Calls = append(d.Calls, fmt.Sprintf("EXEC %s %q, %q, %q, N%q", q, schema, table, column, text))
	return nil
}

func col(name, typ string, opts ...string) Column {
	c := Column{Name: name, Type: typ}
	for _, o := range opts {
		switch o {
		case "pk":
			c.PK = true
		case "null":
			c.Nullable = true
		case "id":
			c.Identity = true
		}
	}
	return c
}

var demoTables = []Table{
	{Schema: "dim", Name: "Partner", Rows: 412, Desc: "Vevők és szállítók (tisztított partnertörzs)", Columns: []Column{
		col("PartnerID", "int", "pk", "id"), col("PartnerKod", "nvarchar(20)"), col("Nev", "nvarchar(200)"), col("Orszag", "char(2)"),
		col("Varos", "nvarchar(80)", "null"), col("Szegmens", "nvarchar(40)", "null"), col("Aktiv", "bit"), col("ElsoVasarlas", "date", "null")}},
	{Schema: "dim", Name: "Termek", Rows: 236, Columns: []Column{
		col("TermekID", "int", "pk", "id"), col("Cikkszam", "nvarchar(30)"), col("Megnevezes", "nvarchar(200)"), col("Termekcsoport", "nvarchar(60)"),
		col("Kiszereles", "nvarchar(30)", "null"), col("MennyisegiEgyseg", "nvarchar(10)")}},
	{Schema: "dim", Name: "Datum", Rows: 3653, Columns: []Column{
		col("DatumKulcs", "int", "pk"), col("Datum", "date"), col("Ev", "smallint"), col("Honap", "tinyint"), col("HonapNev", "nvarchar(20)"), col("Munkanap", "bit")}},
	{Schema: "fact", Name: "Ertekesites", Rows: 48210, Desc: "Számlasoros értékesítés, napi töltés 06:00", Columns: []Column{
		col("ErtekesitesID", "bigint", "pk", "id"), col("DatumKulcs", "int"), col("PartnerID", "int"), col("TermekID", "int"),
		col("Mennyiseg", "decimal(18,3)"), col("NettoErtek", "decimal(18,2)"), col("Penznem", "char(3)"), col("Szamlaszam", "nvarchar(30)"), col("BetoltveIdo", "datetime2(0)")}},
	{Schema: "fact", Name: "OnkoltsegHavi", Rows: 9120, Desc: "Havi önköltség termékenként, 2019-től", Columns: []Column{
		col("Ev", "smallint", "pk"), col("Honap", "tinyint", "pk"), col("TermekID", "int", "pk"), col("Anyagkoltseg", "decimal(18,2)"),
		col("Berkoltseg", "decimal(18,2)"), col("Rezsi", "decimal(18,2)"), col("Onkoltseg", "decimal(18,2)"), col("Megjegyzes", "nvarchar(400)", "null")}},
	{Schema: "elemzes", Name: "PartnerKategoria", Rows: 412, Columns: []Column{
		col("PartnerID", "int", "pk"), col("ABC", "char(1)"), col("Forgalom12Ho", "decimal(18,2)"), col("Trend", "nvarchar(20)"), col("Szamitva", "datetime2(0)")}},
	{Schema: "elemzes", Name: "v_PartnerForgalom", View: true, Rows: -1, Columns: []Column{
		col("PartnerID", "int"), col("Nev", "nvarchar(200)"), col("Ev", "smallint"), col("Forgalom", "decimal(38,2)", "null")}},
}

var demoFKs = []FK{
	{Name: "FK_Ertekesites_Partner", From: "fact.Ertekesites", FromCols: []string{"PartnerID"}, To: "dim.Partner", ToCols: []string{"PartnerID"}},
	{Name: "FK_Ertekesites_Termek", From: "fact.Ertekesites", FromCols: []string{"TermekID"}, To: "dim.Termek", ToCols: []string{"TermekID"}},
}

func (d *Demo) Close() error { return nil }

func (d *Demo) Info(context.Context) (ConnInfo, error) {
	return ConnInfo{Server: "DEMO", Database: "BI_DEMO", Login: "demo", ServerVersion: "16.0 (bemutató)", Tables: len(demoTables) + len(d.Extra)}, nil
}

func (d *Demo) Schema(context.Context) (*Schema, error) {
	s := &Schema{Server: "DEMO", Database: "BI_DEMO", ServerVersion: "16.0 (bemutató)", FetchedAt: time.Now(), TookMs: 42}
	base := time.Date(2024, 3, 1, 8, 0, 0, 0, time.UTC)
	for i, t := range d.tables() {
		t.Created, t.Modified = base.AddDate(0, 0, i*3), base.AddDate(0, 3, i)
		s.Tables = append(s.Tables, t)
	}
	s.FKs = append([]FK(nil), demoFKs...)
	return s, nil
}

func (d *Demo) Rows(ctx context.Context, t *Table, limit int, progress func(int) bool) (*Result, error) {
	cols := make([]ResultCol, len(t.Columns))
	for i, c := range t.Columns {
		base := strings.ToUpper(strings.SplitN(c.Type, "(", 2)[0])
		cols[i] = ResultCol{Name: c.Name, Type: strings.ToLower(base), Kind: KindOf(base)}
	}
	res := NewResult(t.Key(), cols)
	res.Limit = limit
	n := int(t.Rows)
	if n < 0 {
		n = 800
	}
	rnd := rand.New(rand.NewSource(int64(len(t.Name))))
	words := []string{"Halfeldolgozó Kft.", "Balaton Hal Zrt.", "Tisza-Fish Bt.", "Fogas Trade Kft.", "Ponty és Társa", "Harcsa Kereskedőház", "Dunamenti Hal", "Kárász Kft."}
	cities := []string{"Budapest", "Szeged", "Siófok", "Debrecen", "Győr", "Pécs", "Kecskemét"}
	day0 := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)
	for r := 0; r < n && r <= limit; r++ {
		if r == limit {
			res.Truncated = true
			break
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		row := make([]any, len(cols))
		for i, c := range t.Columns {
			typ := strings.ToLower(c.Type)
			switch {
			case c.Nullable && rnd.Intn(9) == 0:
				row[i] = nil
			case c.PK && c.Identity, c.Name == "PartnerID" && t.Name == "PartnerKategoria":
				row[i] = int64(r + 1)
			case strings.HasSuffix(c.Name, "ID"):
				row[i] = int64(1 + rnd.Intn(400))
			case c.Name == "Ev":
				row[i] = int64(2019 + r%7)
			case c.Name == "Honap":
				row[i] = int64(1 + r%12)
			case c.Name == "DatumKulcs":
				d := day0.AddDate(0, 0, r%2500)
				row[i] = int64(d.Year()*10000 + int(d.Month())*100 + d.Day())
			case strings.HasPrefix(typ, "decimal"):
				v := float64(rnd.Intn(5_000_000)) / 100
				row[i] = Num{S: fmt.Sprintf("%.2f", v), F: v}
			case typ == "bit":
				row[i] = rnd.Intn(5) > 0
			case typ == "date":
				row[i] = day0.AddDate(0, 0, rnd.Intn(2400))
			case strings.HasPrefix(typ, "datetime"):
				row[i] = day0.AddDate(0, 0, rnd.Intn(2400)).Add(6*time.Hour + time.Duration(rnd.Intn(3600))*time.Second)
			case strings.Contains(typ, "int"):
				row[i] = int64(rnd.Intn(1000))
			case c.Name == "Nev":
				row[i] = words[rnd.Intn(len(words))]
			case c.Name == "Varos":
				row[i] = cities[rnd.Intn(len(cities))]
			case c.Name == "Orszag":
				row[i] = []string{"HU", "HU", "HU", "RO", "SK", "AT"}[rnd.Intn(6)]
			case c.Name == "Penznem":
				row[i] = []string{"HUF", "HUF", "EUR"}[rnd.Intn(3)]
			case c.Name == "ABC":
				row[i] = []string{"A", "B", "C"}[rnd.Intn(3)]
			default:
				row[i] = fmt.Sprintf("%s-%04d", strings.ToUpper(c.Name[:min(3, len(c.Name))]), r+1)
			}
		}
		res.Add(row)
		if progress != nil && res.Total%2000 == 0 && !progress(res.Total) {
			res.Truncated, res.Stopped = true, "memory"
			break
		}
	}
	res.Finish()
	return res, nil
}
