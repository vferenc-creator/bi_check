package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bimonitor/internal/dbmap"
	"bimonitor/internal/model"
)

func TestDatabaseTabWithDemo(t *testing.T) {
	a, _, dir := newTestApp(t)
	// The defaults are prefilled from the KNIME connection (EF-BI / Dashboard,
	// SQL user d.admin) – but with no password, and no structure fetched yet.
	v := call[DBView](t, a, "dbLoad", dbLoadParam{})
	if !v.Configured || v.Conn.Server != "EF-BI" || v.Conn.Port != 1433 || v.Conn.Database != "Dashboard" || v.Conn.Auth != "sql" || v.Conn.User != "d.admin" || v.Conn.HasPassword {
		t.Fatalf("defaults from KNIME: %+v", v.Conn)
	}
	v = call[DBView](t, a, "dbSaveConn", DBConn{DBSettings: model.DBSettings{Server: DemoServer, Database: "BI_DEMO"}})
	if !v.Configured || v.Conn.Auth != "windows" || v.Conn.MaxRows != 100000 {
		t.Fatalf("%+v", v.Conn)
	}
	info := call[dbmap.ConnInfo](t, a, "dbTest", DBConn{DBSettings: model.DBSettings{Server: DemoServer, Database: "BI_DEMO"}})
	if info.Tables == 0 {
		t.Fatal(info)
	}
	f := call[dbFetchResult](t, a, "dbFetch", nil)
	if f.Summary.Tables < 5 || len(f.Summary.New) != 0 || f.Snap.Schema == nil || len(f.Snap.Doc.Tables) != f.Summary.Tables {
		t.Fatalf("%+v", f.Summary)
	}
	doc := call[dbmap.Doc](t, a, "dbApply", []dbmap.Op{
		{Op: "tableDesc", Table: "fact.OnkoltsegHavi", Text: "Havi önköltség"},
		{Op: "relAdd", Relation: &dbmap.Relation{From: "fact.OnkoltsegHavi", FromCol: "TermekID", To: "dim.Termek", ToCol: "TermekID"}},
	})
	if doc.Tables["fact.onkoltseghavi"].Desc != "Havi önköltség" || len(doc.Relations) != 1 || doc.Rev != 2 {
		t.Fatalf("%+v", doc)
	}
	// unchanged → small answer; after an edit only the map is sent
	v = call[DBView](t, a, "dbLoad", dbLoadParam{Known: true, Rev: doc.Rev, Fetch: f.Snap.Schema.FetchedAt})
	if !v.Unchanged {
		t.Fatal("expected unchanged")
	}
	v = call[DBView](t, a, "dbLoad", dbLoadParam{Known: true, Rev: 1, Fetch: f.Snap.Schema.FetchedAt})
	if v.Unchanged || v.Snap == nil || v.Snap.Schema != nil || v.Snap.Doc.Rev != 2 {
		t.Fatalf("%+v", v)
	}
	// a failing op changes nothing
	if _, err := a.Call("dbApply", mustJSON([]dbmap.Op{{Op: "tableDesc", Table: "fact.OnkoltsegHavi", Text: "x"}, {Op: "tableDesc", Table: "nincs.ilyen"}})); err == nil {
		t.Fatal("expected error")
	}
	if d := a.dbView(nil).Snap.Doc; d.Tables["fact.onkoltseghavi"].Desc != "Havi önköltség" || d.Rev != 2 {
		t.Fatal("a failed batch must not be saved")
	}

	res := call[*dbmap.Result](t, a, "dbQuery", dbQueryParam{Table: "fact.Ertekesites"})
	if res.Total != 100 || !res.Truncated || len(res.Cols) != 9 {
		t.Fatalf("%+v", res)
	}
	page := call[dbmap.Page](t, a, "dbPage", dbPageParam{ID: res.ID, Query: dbmap.Query{Limit: 10, Filters: map[string]string{"Penznem": "=EUR"}}})
	if page.Total != 100 || page.Filtered == 0 || page.Filtered >= 100 || len(page.Rows) > 10 {
		t.Fatalf("%+v", page)
	}
	all := call[*dbmap.Result](t, a, "dbQuery", dbQueryParam{Table: "dim.Partner", All: true})
	if all.Total != 412 || all.Truncated {
		t.Fatalf("%+v", all)
	}
	if _, err := a.Call("dbQuery", mustJSON(dbQueryParam{Table: "dbo.nincs"})); err == nil {
		t.Fatal("unknown table")
	}

	// the map lives in its own folder; settings.json has no password
	if _, err := os.Stat(filepath.Join(a.cacheDir(), dbmap.FolderName)); err != nil {
		t.Fatal(err)
	}
	call[DBView](t, a, "dbSaveConn", DBConn{DBSettings: model.DBSettings{Server: "EF-SQL01\\BI", Database: "BI", Auth: "sql", User: "olvaso"}, Password: "Titok123"})
	b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if strings.Contains(string(b), "Titok123") || !strings.Contains(string(b), "olvaso") {
		t.Fatal("password must not be in settings.json")
	}
	if v := a.dbView(nil); !v.Conn.HasPassword || v.Conn.Password != "" {
		t.Fatalf("%+v", v.Conn)
	}
	if _, err := a.Call("dbSaveConn", mustJSON(DBConn{DBSettings: model.DBSettings{Server: "a;b", Database: "BI"}})); err == nil {
		t.Fatal("invalid server name")
	}
	call[DBView](t, a, "dbSaveConn", DBConn{DBSettings: model.DBSettings{Server: "EF-SQL01", Database: "BI", Auth: "windows"}})
	if a.dbView(nil).Conn.HasPassword {
		t.Fatal("switching to Windows authentication forgets the password")
	}
}

// Shared mode: the map goes into its own sub-folder of the shared folder;
// the shared monitoring list files are not touched.
func TestDatabaseMapSharedFolder(t *testing.T) {
	a, _, _ := newTestApp(t)
	shared := t.TempDir()
	if _, err := a.Settings.Update(func(s *model.Settings) error {
		s.Team.Folder = shared // only the folder setting matters for the map
		s.Team.Enabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	call[DBView](t, a, "dbSaveConn", DBConn{DBSettings: model.DBSettings{Server: DemoServer, Database: "BI_DEMO"}})
	call[dbFetchResult](t, a, "dbFetch", nil)
	if _, err := os.Stat(filepath.Join(shared, dbmap.FolderName, dbmap.Key(DemoServer, "BI_DEMO")+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(shared, dbmap.FolderName, dbmap.Key(DemoServer, "BI_DEMO")+".schema.json")); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// Descriptions live in the database: written only through the team's
// procedure (parameters), read back, and visible to the other computer.
func TestDatabaseDescriptions(t *testing.T) {
	a, _, _ := newTestApp(t)
	shared := t.TempDir()
	if _, err := a.Settings.Update(func(s *model.Settings) error {
		s.Team.Folder, s.Team.Enabled = shared, true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	v := call[DBView](t, a, "dbSaveConn", DBConn{DBSettings: model.DBSettings{Server: DemoServer, Database: "BI_DEMO"}})
	if v.Conn.DescWrite != "proc" || v.Conn.DescProc != "meta.leiras_beallit" {
		t.Fatalf("defaults: %+v", v.Conn)
	}
	f := call[dbFetchResult](t, a, "dbFetch", nil)
	stamp := f.Snap.Schema.Stamp()

	r := call[dbDescResult](t, a, "dbSetDesc", dbDescParam{Table: "fact.ertekesites", Column: "penznem", Text: "  A számla pénzneme (pl. HUF, EUR) – 'idézőjellel'  "})
	if r.Warning != "" || r.Table.Col("Penznem").Desc != "A számla pénzneme (pl. HUF, EUR) – 'idézőjellel'" {
		t.Fatalf("%+v", r)
	}
	r = call[dbDescResult](t, a, "dbSetDesc", dbDescParam{Table: "fact.Ertekesites", Text: "Számlasoros értékesítés"})
	if r.Table.Desc != "Számlasoros értékesítés" {
		t.Fatalf("%+v", r.Table)
	}
	demo := a.db.demo
	if len(demo.Calls) != 2 || !strings.HasPrefix(demo.Calls[0], `EXEC [meta].[leiras_beallit] "fact", "Ertekesites", "Penznem"`) ||
		!strings.Contains(demo.Calls[1], `"Ertekesites", ""`) {
		t.Fatalf("procedure calls: %q", demo.Calls)
	}
	// the other computer: the structure stamp changed → it reloads the structure
	v = call[DBView](t, a, "dbLoad", dbLoadParam{Known: true, Rev: 0, Fetch: stamp})
	if v.Snap == nil || v.Snap.Schema == nil || v.Snap.Schema.Find("fact.Ertekesites").Desc != "Számlasoros értékesítés" {
		t.Fatal("changed descriptions must reach the other computers")
	}
	// re-read from the database
	rr := call[dbDescResult](t, a, "dbReadDesc", dbDescParam{Table: "fact.Ertekesites"})
	if rr.Table.Col("Penznem").Desc == "" {
		t.Fatal("re-read")
	}
	// unknown column / table, too long, invalid procedure name
	for _, bad := range []dbDescParam{{Table: "fact.Ertekesites", Column: "nincs"}, {Table: "dbo.nincs"}, {Table: "fact.Ertekesites", Text: strings.Repeat("x", 4000)}} {
		if _, err := a.Call("dbSetDesc", mustJSON(bad)); err == nil {
			t.Fatalf("expected error for %+v", bad)
		}
	}
	for _, proc := range []string{"meta.leiras_beallit; DROP TABLE x", "a.b.c", "x'--", "[meta].[x]"} {
		if _, err := a.Call("dbSaveConn", mustJSON(DBConn{DBSettings: model.DBSettings{Server: DemoServer, Database: "BI_DEMO", DescProc: proc}})); err == nil {
			t.Fatalf("procedure name %q must be refused", proc)
		}
	}
	// switched off: never writes
	call[DBView](t, a, "dbSaveConn", DBConn{DBSettings: model.DBSettings{Server: DemoServer, Database: "BI_DEMO", DescWrite: "off"}})
	if _, err := a.Call("dbSetDesc", mustJSON(dbDescParam{Table: "fact.Ertekesites", Text: "x"})); err == nil {
		t.Fatal("writing must be refused when switched off")
	}
	if len(demo.Calls) != 2 {
		t.Fatalf("no further calls expected: %q", demo.Calls)
	}
}
