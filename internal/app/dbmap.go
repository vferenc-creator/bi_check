package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bimonitor/internal/dbmap"
	"bimonitor/internal/model"
	"bimonitor/internal/secret"
)

// The "Adatbázis" tab: a read-only browser of the team's BI database.
// Nothing is queried automatically – only when someone presses a button.

// DemoServer as server name selects the built-in sample database.
const DemoServer = "(demo)"

type dbState struct {
	mu      sync.Mutex
	src     dbmap.Source
	srcSig  string
	results []*dbmap.Result // newest last, at most 4
	cancel  context.CancelFunc
	demo    *dbmap.Demo
}

func (a *App) dbPasswordPath() string {
	return filepath.Join(filepath.Dir(a.Settings.Path()), "sql-jelszo.bin")
}

// DBConn is the connection form (the password is never sent to the UI).
type DBConn struct {
	model.DBSettings
	Password      string `json:"password,omitempty"`      // new password (empty = keep)
	ClearPassword bool   `json:"clearPassword,omitempty"` // forget the stored one
	HasPassword   bool   `json:"hasPassword"`
}

func (a *App) dbConnView() DBConn {
	s := a.Settings.Get().Database
	return DBConn{DBSettings: s, HasPassword: secret.Exists(a.dbPasswordPath())}
}

func (a *App) dbConfig(s model.DBSettings, pw string) dbmap.Config {
	return dbmap.Config{Server: s.Server, Database: s.Database, Auth: s.Auth, User: s.User, Password: pw, Encrypt: s.Encrypt,
		Timeout: time.Duration(s.TimeoutSec) * time.Second}
}

func isDemo(s model.DBSettings) bool {
	return strings.EqualFold(strings.TrimSpace(s.Server), DemoServer)
}

// dbSource returns the source for the saved settings (cached).
func (a *App) dbSource() (dbmap.Source, model.DBSettings, error) {
	s := a.Settings.Get().Database
	if strings.TrimSpace(s.Server) == "" || strings.TrimSpace(s.Database) == "" {
		return nil, s, errors.New("még nincs megadva az adatbázis – Beállítások → Adatbázis")
	}
	pw := ""
	if s.Auth == "sql" && !isDemo(s) {
		var err error
		if pw, err = secret.Load(a.dbPasswordPath()); err != nil {
			return nil, s, err
		}
	}
	sig := fmt.Sprintf("%+v|%x", s, len(pw)) + pw
	a.db.mu.Lock()
	defer a.db.mu.Unlock()
	if a.db.src != nil && a.db.srcSig == sig {
		return a.db.src, s, nil
	}
	if a.db.src != nil {
		_ = a.db.src.Close()
		a.db.src = nil
	}
	var src dbmap.Source
	if isDemo(s) {
		if a.db.demo == nil {
			a.db.demo = &dbmap.Demo{}
		}
		src = a.db.demo
	} else {
		m, err := dbmap.OpenMSSQL(a.dbConfig(s, pw))
		if err != nil {
			return nil, s, err
		}
		src = m
	}
	a.db.src, a.db.srcSig = src, sig
	return src, s, nil
}

func (a *App) dbStore() *dbmap.Store {
	s := a.Settings.Get()
	st := &dbmap.Store{Local: a.cacheDir(), Who: a.dbWho(), Timeout: time.Duration(s.TimeoutSec+5) * time.Second}
	if s.Team.Enabled && strings.TrimSpace(s.Team.Folder) != "" {
		st.Shared = s.Team.Folder
	}
	return st
}

func (a *App) dbWho() string {
	id := a.opt.Identity
	n := strings.TrimSpace(id.Name())
	switch {
	case n == "":
		return id.Host
	case id.Host != "":
		return n + " (" + id.Host + ")"
	}
	return n
}

// DBView is what the tab loads.
type DBView struct {
	Conn       DBConn          `json:"conn"`
	Configured bool            `json:"configured"`
	Snap       *dbmap.Snapshot `json:"snap,omitempty"`
	Error      string          `json:"error,omitempty"`
	Unchanged  bool            `json:"unchanged,omitempty"`
}

type dbLoadParam struct {
	Rev   int64     `json:"rev"`
	Fetch time.Time `json:"fetch"`
	Known bool      `json:"known"`
}

// FetchSummary tells what changed at a structure fetch.
type FetchSummary struct {
	Tables     int      `json:"tables"`
	New        []string `json:"new"`
	Gone       []string `json:"gone"`
	NewColumns int      `json:"newColumns"`
	TookMs     int64    `json:"tookMs"`
	Warnings   []string `json:"warnings,omitempty"`
}

type dbFetchResult struct {
	DBView
	Summary FetchSummary `json:"summary"`
}

type dbQueryParam struct {
	Table string `json:"table"`
	All   bool   `json:"all"`
}

type dbPageParam struct {
	ID string `json:"id"`
	dbmap.Query
}

func (a *App) dbView(p *dbLoadParam) DBView {
	v := DBView{Conn: a.dbConnView()}
	s := v.Conn.DBSettings
	v.Configured = strings.TrimSpace(s.Server) != "" && strings.TrimSpace(s.Database) != ""
	if !v.Configured {
		return v
	}
	snap, err := a.dbStore().Load(s.Server, s.Database)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	if p != nil && p.Known && snap.Doc.Rev == p.Rev && snap.Schema.Stamp().Equal(p.Fetch) && !snap.FromCache {
		return DBView{Conn: v.Conn, Configured: true, Unchanged: true}
	}
	if p != nil && p.Known && snap.Schema.Stamp().Equal(p.Fetch) {
		snap.Schema = nil // unchanged structure: only the (small) map is sent
	}
	v.Snap = snap
	return v
}

func (a *App) registerDBAPI() {
	a.register("dbLoad", func(p dbLoadParam) (DBView, error) { return a.dbView(&p), nil })

	a.register("dbSaveConn", func(p DBConn) (DBView, error) {
		p.Server, p.Database, p.User = strings.TrimSpace(p.Server), strings.TrimSpace(p.Database), strings.TrimSpace(p.User)
		if !isDemo(p.DBSettings) && p.Server != "" {
			if _, err := a.dbConfig(p.DBSettings, "x").DSN(); err != nil {
				return DBView{}, err
			}
		}
		if p.DescWrite != "off" {
			if strings.TrimSpace(p.DescProc) == "" {
				p.DescProc = dbmap.DefaultDescProc
			}
			if _, err := dbmap.QuoteProc(p.DescProc); err != nil {
				return DBView{}, err
			}
			p.DescProc = strings.TrimSpace(p.DescProc)
		}
		if p.Auth != "sql" || p.ClearPassword {
			if err := secret.Save(a.dbPasswordPath(), ""); err != nil {
				return DBView{}, err
			}
		}
		if p.Auth == "sql" && p.Password != "" {
			if err := secret.Save(a.dbPasswordPath(), p.Password); err != nil {
				return DBView{}, fmt.Errorf("a jelszó mentése nem sikerült: %w", err)
			}
		}
		if _, err := a.Settings.Update(func(s *model.Settings) error {
			s.Database = p.DBSettings
			s.Normalize()
			return nil
		}); err != nil {
			return DBView{}, err
		}
		return a.dbView(nil), nil
	})

	a.register("dbTest", func(p DBConn) (dbmap.ConnInfo, error) {
		s := p.DBSettings
		s.Server, s.Database = strings.TrimSpace(s.Server), strings.TrimSpace(s.Database)
		if isDemo(s) {
			ci, err := (&dbmap.Demo{}).Info(context.Background())
			if s.DescWrite != "off" {
				ci.DescProc, ci.DescProcOK, ci.DescProcExec = dbmap.DefaultDescProc, true, true
			}
			return ci, err
		}
		pw := p.Password
		if pw == "" && s.Auth == "sql" && !p.ClearPassword {
			var err error
			if pw, err = secret.Load(a.dbPasswordPath()); err != nil {
				return dbmap.ConnInfo{}, err
			}
		}
		if s.TimeoutSec < 5 {
			s.TimeoutSec = 30
		}
		m, err := dbmap.OpenMSSQL(a.dbConfig(s, pw))
		if err != nil {
			return dbmap.ConnInfo{}, err
		}
		defer m.Close()
		ci, err := m.Info(context.Background())
		if err != nil || s.DescWrite == "off" {
			return ci, err
		}
		if strings.TrimSpace(s.DescProc) == "" {
			s.DescProc = dbmap.DefaultDescProc
		}
		ci.DescProc = s.DescProc
		ci.DescProcOK, ci.DescProcExec, err = m.CheckProc(context.Background(), s.DescProc)
		return ci, err
	})

	a.register("dbFetch", func() (dbFetchResult, error) {
		src, s, err := a.dbSource()
		if err != nil {
			return dbFetchResult{}, err
		}
		fresh, err := src.Schema(context.Background())
		if err != nil {
			return dbFetchResult{}, err
		}
		fresh.Server, fresh.Database, fresh.FetchedBy = s.Server, s.Database, a.dbWho()
		var sum FetchSummary
		snap, err := a.dbStore().Update(s.Server, s.Database, func(d *dbmap.Doc, prev *dbmap.Schema) (*dbmap.Schema, error) {
			out := d.Merge(prev, fresh, time.Now())
			for _, t := range out.Tables {
				m := d.Tables[dbmap.Norm(t.Key())]
				switch {
				case t.Gone:
					sum.Gone = append(sum.Gone, t.Key())
				case m != nil && m.New:
					sum.New = append(sum.New, t.Key())
				}
				if m != nil {
					for _, c := range m.Columns {
						if c.New {
							sum.NewColumns++
						}
					}
				}
			}
			return out, nil
		})
		if err != nil {
			return dbFetchResult{}, err
		}
		sum.Tables, sum.TookMs, sum.Warnings = len(fresh.Tables), fresh.TookMs, fresh.Warnings
		if sum.New == nil {
			sum.New = []string{}
		}
		if sum.Gone == nil {
			sum.Gone = []string{}
		}
		log.Printf("adatbázis: szerkezet lekérve (%d tábla, %d új, %d eltűnt, %d ms)", sum.Tables, len(sum.New), len(sum.Gone), sum.TookMs)
		return dbFetchResult{DBView: DBView{Conn: a.dbConnView(), Configured: true, Snap: snap}, Summary: sum}, nil
	})

	a.register("dbApply", func(ops []dbmap.Op) (*dbmap.Doc, error) {
		s := a.Settings.Get().Database
		if strings.TrimSpace(s.Server) == "" {
			return nil, errors.New("nincs beállítva adatbázis")
		}
		who := a.dbWho()
		snap, err := a.dbStore().Update(s.Server, s.Database, func(d *dbmap.Doc, sch *dbmap.Schema) (*dbmap.Schema, error) {
			now := time.Now()
			for _, op := range ops {
				if err := d.Apply(op, sch, who, now); err != nil {
					return nil, err
				}
			}
			return nil, nil
		})
		if err != nil {
			return nil, err
		}
		return snap.Doc, nil
	})

	a.register("dbQuery", func(p dbQueryParam) (*dbmap.Result, error) {
		src, s, err := a.dbSource()
		if err != nil {
			return nil, err
		}
		snap, err := a.dbStore().Load(s.Server, s.Database)
		if err != nil {
			return nil, err
		}
		t := snap.Schema.Find(p.Table)
		if t == nil || t.Gone {
			return nil, errors.New("ez a tábla nem szerepel a lekért szerkezetben – kérje le újra a szerkezetet")
		}
		limit := 100
		var ctx context.Context
		var cancel context.CancelFunc
		if p.All {
			limit = dbmap.RowLimit(s.MaxRows, len(t.Columns))
			ctx, cancel = context.WithTimeout(context.Background(), 30*time.Minute)
		} else {
			ctx, cancel = context.WithTimeout(context.Background(), time.Duration(s.TimeoutSec)*time.Second)
		}
		a.db.mu.Lock()
		if a.db.cancel != nil {
			a.db.cancel() // one query at a time
		}
		a.db.cancel = cancel
		a.db.mu.Unlock()
		defer func() {
			cancel()
			a.db.mu.Lock()
			a.db.cancel = nil
			a.db.mu.Unlock()
		}()
		last := time.Time{}
		res, err := src.Rows(ctx, t, limit, func(n int) {
			if time.Since(last) > 300*time.Millisecond {
				last = time.Now()
				a.P.Push("dbProgress", map[string]any{"table": t.Key(), "rows": n})
			}
		})
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil, errors.New("a lekérdezés megszakítva")
			}
			return nil, dbmap.Friendly(err)
		}
		res.All = p.All
		a.db.mu.Lock()
		a.db.results = append(a.db.results, res)
		if len(a.db.results) > 4 {
			a.db.results = a.db.results[len(a.db.results)-4:]
		}
		a.db.mu.Unlock()
		log.Printf("adatbázis: %s – %d sor (%d ms)", t.Key(), res.Total, res.TookMs)
		return res, nil
	})

	a.register("dbSetDesc", a.dbSetDesc)
	a.register("dbReadDesc", func(p dbDescParam) (dbDescResult, error) { return a.dbDescribe(p, false) })

	a.register("dbCancel", func() error {
		a.db.mu.Lock()
		defer a.db.mu.Unlock()
		if a.db.cancel != nil {
			a.db.cancel()
		}
		return nil
	})

	a.register("dbPage", func(p dbPageParam) (dbmap.Page, error) {
		r := a.dbResult(p.ID)
		if r == nil {
			return dbmap.Page{}, errors.New("a lekérdezés eredménye már nincs meg – kérje le újra")
		}
		return r.Fetch(p.Query), nil
	})

	a.register("dbStats", func(p idParam) ([]dbmap.ColStats, error) {
		r := a.dbResult(p.ID)
		if r == nil {
			return nil, errors.New("a lekérdezés eredménye már nincs meg")
		}
		return r.Stats(), nil
	})

	a.register("dbCloseResult", func(p idParam) error {
		a.db.mu.Lock()
		defer a.db.mu.Unlock()
		out := a.db.results[:0]
		for _, r := range a.db.results {
			if r.ID != p.ID {
				out = append(out, r)
			}
		}
		a.db.results = out
		return nil
	})
}

func (a *App) dbResult(id string) *dbmap.Result {
	a.db.mu.Lock()
	defer a.db.mu.Unlock()
	for _, r := range a.db.results {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// ---- descriptions in the database --------------------------------------------

type dbDescParam struct {
	Table  string `json:"table"`
	Column string `json:"column"` // "" = the table itself
	Text   string `json:"text"`
}

type dbDescResult struct {
	Table   dbmap.Table `json:"table"`
	Changed time.Time   `json:"changed"`
	Doc     *dbmap.Doc  `json:"doc"`
	Warning string      `json:"warning,omitempty"`
}

// maxDescLen: MS_Description is an sql_variant (at most 7500 bytes).
const maxDescLen = 3750

func (a *App) dbSetDesc(p dbDescParam) (dbDescResult, error) {
	if a.Settings.Get().Database.DescWrite == "off" {
		return dbDescResult{}, errors.New("a leírások adatbázisba írása ki van kapcsolva (Beállítások → Adatbázis)")
	}
	return a.dbDescribe(p, true)
}

// dbDescribe writes (write=true) one description with the team's procedure
// and re-reads the descriptions of the table; the stored structure is
// updated so the other computers see the change as well.
func (a *App) dbDescribe(p dbDescParam, write bool) (dbDescResult, error) {
	src, s, err := a.dbSource()
	if err != nil {
		return dbDescResult{}, err
	}
	snap, err := a.dbStore().Load(s.Server, s.Database)
	if err != nil {
		return dbDescResult{}, err
	}
	t := snap.Schema.Find(p.Table)
	if t == nil || t.Gone {
		return dbDescResult{}, errors.New("ez a tábla nem szerepel a lekért szerkezetben – kérje le újra a szerkezetet")
	}
	col := ""
	if p.Column != "" {
		c := t.Col(p.Column)
		if c == nil {
			return dbDescResult{}, fmt.Errorf("nincs ilyen mező: %s", p.Column)
		}
		col = c.Name // exact name from the database
	}
	var res dbDescResult
	ctx := context.Background()
	text := strings.TrimSpace(strings.ReplaceAll(p.Text, "\r\n", "\n"))
	if write {
		if n := len([]rune(text)); n > maxDescLen {
			return res, fmt.Errorf("a leírás túl hosszú (%d karakter, legfeljebb %d)", n, maxDescLen)
		}
		if err := src.SetDescription(ctx, s.DescProc, t.Schema, t.Name, col, text); err != nil {
			return res, err
		}
		what := t.Key()
		if col != "" {
			what += "." + col
		}
		log.Printf("adatbázis: leírás mentve (%s): %s", s.DescProc, what)
	}
	d, err := src.Descriptions(ctx, t.Schema, t.Name)
	if err != nil {
		if !write {
			return res, err
		}
		// written, but could not be read back: keep what we wrote
		res.Warning = "A leírás elmentve, de visszaolvasni nem sikerült: " + err.Error()
		d = &dbmap.Descs{Table: t.Desc, Columns: map[string]string{}}
		for _, c := range t.Columns {
			d.Columns[c.Name] = c.Desc
		}
		if col == "" {
			d.Table = text
		} else {
			d.Columns[col] = text
		}
	} else if write {
		got := d.Table
		if col != "" {
			got = d.Columns[col]
		}
		if strings.TrimSpace(got) != text {
			res.Warning = "Az eljárás lefutott, de az adatbázisban ez a leírás olvasható vissza: „" + got + "”"
		}
	}
	key := t.Key()
	out, err := a.dbStore().Update(s.Server, s.Database, func(doc *dbmap.Doc, sch *dbmap.Schema) (*dbmap.Schema, error) {
		tt := sch.Find(key)
		if tt == nil {
			return nil, errors.New("a tábla közben eltűnt a térképről")
		}
		tt.Desc = d.Table
		for i := range tt.Columns {
			if v, ok := d.Columns[tt.Columns[i].Name]; ok {
				tt.Columns[i].Desc = v
			}
		}
		sch.Changed = time.Now()
		return sch, nil
	})
	if err != nil {
		if write {
			return res, fmt.Errorf("a leírás az adatbázisba bekerült, de a térképre nem sikerült menteni: %w", err)
		}
		return res, err
	}
	res.Table = *out.Schema.Find(key)
	res.Changed = out.Schema.Changed
	res.Doc = out.Doc
	return res, nil
}
