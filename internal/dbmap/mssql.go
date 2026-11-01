package dbmap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"
)

// Source is a database that can be browsed (MS SQL, or the demo source).
type Source interface {
	Info(ctx context.Context) (ConnInfo, error)
	Schema(ctx context.Context) (*Schema, error)
	// Rows reads at most limit rows of a table (limit+1 are asked for, to
	// know whether there are more). progress is called now and then.
	Rows(ctx context.Context, t *Table, limit int, progress func(n int)) (*Result, error)
	// Descriptions re-reads the MS_Description of one table and its columns.
	Descriptions(ctx context.Context, schema, table string) (*Descs, error)
	// SetDescription writes one description – the only statement that
	// changes anything: EXEC <proc> @sema, @tabla, @mezo (NULL = the
	// table), @leiras, with parameters.
	SetDescription(ctx context.Context, proc, schema, table, column, text string) error
	Close() error
}

// Descs are the descriptions of one table as stored in the database.
type Descs struct {
	Table   string            `json:"table"`
	Columns map[string]string `json:"columns"` // column name → description ("" = none)
}

// DefaultDescProc is the team's procedure that writes descriptions.
const DefaultDescProc = "meta.leiras_beallit"

var procRe = regexp.MustCompile(`^[\p{L}_][\p{L}\p{N}_@$#]{0,127}(\.[\p{L}_][\p{L}\p{N}_@$#]{0,127})?$`)

// QuoteProc validates a procedure name ("meta.leiras_beallit") and returns
// it bracket-quoted ("[meta].[leiras_beallit]"). Anything that is not a
// plain one- or two-part name is refused.
func QuoteProc(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !procRe.MatchString(name) {
		return "", fmt.Errorf("érvénytelen eljárásnév: %q (formátum: séma.eljárás, pl. %s)", name, DefaultDescProc)
	}
	parts := strings.Split(name, ".")
	for i, p := range parts {
		parts[i] = Quote(p)
	}
	return strings.Join(parts, "."), nil
}

// ConnInfo is shown after "Kapcsolat tesztelése".
type ConnInfo struct {
	Server        string `json:"server"`
	Database      string `json:"database"`
	Login         string `json:"login"`
	ServerVersion string `json:"serverVersion"`
	Tables        int    `json:"tables"`
	// CanWrite: the login could change data. The program still only reads,
	// but a read-only login (db_datareader) is the safer setup.
	CanWrite bool `json:"canWrite"`
	// The description procedure: exists / may be executed by this login.
	DescProc     string `json:"descProc,omitempty"`
	DescProcOK   bool   `json:"descProcOk"`
	DescProcExec bool   `json:"descProcExec"`
}

// Config of an MS SQL connection.
type Config struct {
	Server   string // EF-SQL01, EF-SQL01\BI, EF-SQL01,1433
	Port     int    // 0 = from Server, or 1433 (named instance: SQL Browser)
	Database string
	Auth     string // "windows" (default) or "sql"
	User     string
	Password string
	Encrypt  string // "trust" (default): encrypted, certificate accepted; "strict"; "off"
	Timeout  time.Duration
}

// DSN builds the driver configuration (without ever putting the password
// into a connection string).
func (c Config) DSN() (msdsn.Config, error) {
	host, inst, port, err := splitServer(c.Server)
	if err != nil {
		return msdsn.Config{}, err
	}
	if c.Port < 0 || c.Port > 65535 {
		return msdsn.Config{}, fmt.Errorf("érvénytelen port: %d", c.Port)
	}
	if c.Port > 0 {
		port = c.Port // the separate port field wins over "SZERVER,port"
	}
	db := strings.TrimSpace(c.Database)
	if db == "" {
		return msdsn.Config{}, errors.New("adja meg az adatbázis nevét")
	}
	if strings.ContainsAny(db, ";{}=") {
		return msdsn.Config{}, errors.New("az adatbázis neve érvénytelen karaktert tartalmaz")
	}
	server := host
	if inst != "" {
		server += `\` + inst
	}
	enc := "encrypt=true;TrustServerCertificate=true"
	switch c.Encrypt {
	case "strict":
		enc = "encrypt=true;TrustServerCertificate=false"
	case "off":
		enc = "encrypt=false"
	}
	ado := "server=" + server + ";database=" + db + ";" + enc + ";app name=BI Output Monitor;ApplicationIntent=ReadOnly"
	if port > 0 {
		ado += ";port=" + strconv.Itoa(port)
	}
	cfg, err := msdsn.Parse(ado)
	if err != nil {
		return cfg, fmt.Errorf("kapcsolati beállítás: %w", err)
	}
	cfg.Database = db
	if c.Auth == "sql" {
		if strings.TrimSpace(c.User) == "" {
			return cfg, errors.New("SQL Server-hitelesítéshez adja meg a felhasználónevet")
		}
		cfg.User, cfg.Password = strings.TrimSpace(c.User), c.Password
	} else {
		cfg.User, cfg.Password = "", "" // Windows (integrated) authentication
	}
	t := c.Timeout
	if t <= 0 {
		t = 30 * time.Second
	}
	cfg.DialTimeout = min(t, 20*time.Second)
	cfg.ConnTimeout = t
	return cfg, nil
}

func splitServer(s string) (host, inst string, port int, err error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "tcp:"), `\\`)
	if s == "" {
		return "", "", 0, errors.New("adja meg a szerver nevét (pl. EF-SQL01 vagy EF-SQL01\\PÉLDÁNY)")
	}
	if strings.ContainsAny(s, ";{}= ") {
		return "", "", 0, errors.New("a szerver neve érvénytelen karaktert tartalmaz")
	}
	p := ""
	if i := strings.LastIndexAny(s, ",:"); i > 0 && !strings.Contains(s[i:], `\`) {
		s, p = s[:i], s[i+1:]
	}
	if i := strings.IndexByte(s, '\\'); i >= 0 {
		s, inst = s[:i], s[i+1:]
	}
	if p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return "", "", 0, fmt.Errorf("érvénytelen port: %s", p)
		}
		port = n
	}
	if s == "." || strings.EqualFold(s, "(local)") {
		s = "localhost"
	}
	return s, inst, port, nil
}

// MSSQL is a read-only Microsoft SQL Server source.
type MSSQL struct {
	cfg     Config
	db      *sql.DB
	timeout time.Duration
}

// OpenMSSQL prepares a connection (nothing is sent before the first call).
func OpenMSSQL(c Config) (*MSSQL, error) {
	cfg, err := c.DSN()
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(mssql.NewConnectorConfig(cfg))
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(2 * time.Minute)
	t := c.Timeout
	if t <= 0 {
		t = 30 * time.Second
	}
	return &MSSQL{cfg: c, db: db, timeout: t}, nil
}

func (m *MSSQL) Close() error { return m.db.Close() }

func (m *MSSQL) ctx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, m.timeout)
}

// Info checks the connection.
func (m *MSSQL) Info(parent context.Context) (ConnInfo, error) {
	ctx, cancel := m.ctx(parent)
	defer cancel()
	var ci ConnInfo
	var writer, owner, perm sql.NullInt64
	err := m.db.QueryRowContext(ctx, `SELECT CAST(@@SERVERNAME AS nvarchar(256)), CAST(DB_NAME() AS nvarchar(256)), CAST(SUSER_SNAME() AS nvarchar(256)),
  CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
  IS_ROLEMEMBER('db_datawriter'), IS_ROLEMEMBER('db_owner'), HAS_PERMS_BY_NAME(DB_NAME(), 'DATABASE', 'INSERT'),
  (SELECT COUNT(*) FROM sys.objects WHERE type IN ('U','V') AND is_ms_shipped = 0)`).
		Scan(&ci.Server, &ci.Database, &ci.Login, &ci.ServerVersion, &writer, &owner, &perm, &ci.Tables)
	if err != nil {
		return ci, Friendly(err)
	}
	ci.CanWrite = writer.Int64 == 1 || owner.Int64 == 1 || perm.Int64 == 1
	return ci, nil
}

// CheckProc tells whether the description procedure exists and may be run.
func (m *MSSQL) CheckProc(parent context.Context, proc string) (exists, exec bool, err error) {
	if _, err := QuoteProc(proc); err != nil {
		return false, false, err
	}
	ctx, cancel := m.ctx(parent)
	defer cancel()
	var id, perm sql.NullInt64
	if err := m.db.QueryRowContext(ctx, `SELECT OBJECT_ID(@p1, 'P'), HAS_PERMS_BY_NAME(@p1, 'OBJECT', 'EXECUTE')`, strings.TrimSpace(proc)).Scan(&id, &perm); err != nil {
		return false, false, Friendly(err)
	}
	return id.Valid, perm.Int64 == 1, nil
}

// qTableDesc: the descriptions of one table and its columns (parameters
// @sema, @tabla – the names are never put into the SQL text).
const qTableDesc = `SELECT
    CAST(tep.value AS NVARCHAR(4000)) AS tabla_leiras,
    c.name                            AS mezo,
    CAST(cep.value AS NVARCHAR(4000)) AS mezo_leiras
FROM sys.objects t
JOIN sys.columns c ON c.object_id = t.object_id
LEFT JOIN sys.extended_properties tep
       ON tep.class = 1 AND tep.major_id = t.object_id
      AND tep.minor_id = 0 AND tep.name = 'MS_Description'
LEFT JOIN sys.extended_properties cep
       ON cep.class = 1 AND cep.major_id = t.object_id
      AND cep.minor_id = c.column_id AND cep.name = 'MS_Description'
WHERE t.type IN ('U','V') AND SCHEMA_NAME(t.schema_id) = @sema
  AND t.name = @tabla
ORDER BY c.column_id`

// Descriptions re-reads the descriptions of one table.
func (m *MSSQL) Descriptions(parent context.Context, schema, table string) (*Descs, error) {
	ctx, cancel := m.ctx(parent)
	defer cancel()
	rows, err := m.db.QueryContext(ctx, qTableDesc, sql.Named("sema", schema), sql.Named("tabla", table))
	if err != nil {
		return nil, Friendly(err)
	}
	defer rows.Close()
	d := &Descs{Columns: map[string]string{}}
	n := 0
	for rows.Next() {
		var td, cd sql.NullString
		var col string
		if err := rows.Scan(&td, &col, &cd); err != nil {
			return nil, Friendly(err)
		}
		d.Table = td.String
		d.Columns[col] = cd.String
		n++
	}
	if err := rows.Err(); err != nil {
		return nil, Friendly(err)
	}
	if n == 0 {
		return nil, fmt.Errorf("a(z) %s.%s tábla nem található az adatbázisban", schema, table)
	}
	return d, nil
}

// SetDescription runs the team's description procedure with parameters.
// This is the only statement of the program that changes the database.
func (m *MSSQL) SetDescription(parent context.Context, proc, schema, table, column, text string) error {
	q, err := QuoteProc(proc)
	if err != nil {
		return err
	}
	ctx, cancel := m.ctx(parent)
	defer cancel()
	var col any // NULL = the description of the table
	if column != "" {
		col = column
	}
	if _, err := m.db.ExecContext(ctx, "EXEC "+q+" @p1, @p2, @p3, @p4", schema, table, col, text); err != nil {
		return Friendly(err)
	}
	return nil
}

const qColumns = `SELECT s.name, o.name, o.type, o.create_date, o.modify_date,
  c.name, t.name, c.max_length, c.precision, c.scale, c.is_nullable, c.is_identity, c.is_computed
FROM sys.objects o
JOIN sys.schemas s ON s.schema_id = o.schema_id
JOIN sys.columns c ON c.object_id = o.object_id
JOIN sys.types t ON t.user_type_id = c.user_type_id
WHERE o.type IN ('U','V') AND o.is_ms_shipped = 0
ORDER BY s.name, o.name, c.column_id`

const qPK = `SELECT s.name, o.name, c.name
FROM sys.indexes i
JOIN sys.objects o ON o.object_id = i.object_id
JOIN sys.schemas s ON s.schema_id = o.schema_id
JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
WHERE i.is_primary_key = 1 AND o.is_ms_shipped = 0`

const qFK = `SELECT fk.name, ps.name, po.name, pc.name, rs.name, ro.name, rc.name
FROM sys.foreign_keys fk
JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
JOIN sys.objects po ON po.object_id = fkc.parent_object_id
JOIN sys.schemas ps ON ps.schema_id = po.schema_id
JOIN sys.columns pc ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
JOIN sys.objects ro ON ro.object_id = fkc.referenced_object_id
JOIN sys.schemas rs ON rs.schema_id = ro.schema_id
JOIN sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
ORDER BY fk.name, fkc.constraint_column_id`

const qRows = `SELECT s.name, o.name, SUM(p.rows)
FROM sys.partitions p
JOIN sys.objects o ON o.object_id = p.object_id
JOIN sys.schemas s ON s.schema_id = o.schema_id
WHERE o.type = 'U' AND o.is_ms_shipped = 0 AND p.index_id IN (0, 1)
GROUP BY s.name, o.name`

const qDesc = `SELECT s.name, o.name, ep.minor_id, ISNULL(c.name, ''), CAST(ep.value AS nvarchar(4000))
FROM sys.extended_properties ep
JOIN sys.objects o ON o.object_id = ep.major_id
JOIN sys.schemas s ON s.schema_id = o.schema_id
LEFT JOIN sys.columns c ON c.object_id = ep.major_id AND c.column_id = ep.minor_id
WHERE ep.class = 1 AND ep.name = 'MS_Description' AND o.is_ms_shipped = 0`

// Schema reads the structure (catalog views only).
func (m *MSSQL) Schema(parent context.Context) (*Schema, error) {
	ctx, cancel := context.WithTimeout(parent, 3*m.timeout)
	defer cancel()
	start := time.Now()
	sch := &Schema{Server: m.cfg.Server, Database: m.cfg.Database, Tables: []Table{}, FKs: []FK{}}
	_ = m.db.QueryRowContext(ctx, `SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64))`).Scan(&sch.ServerVersion)

	rows, err := m.db.QueryContext(ctx, qColumns)
	if err != nil {
		return nil, Friendly(err)
	}
	idx := map[string]int{}
	for rows.Next() {
		var sName, oName, oType, cName, tName string
		var created, modified time.Time
		var maxLen, prec, scale int64
		var nullable, ident, computed bool
		if err := rows.Scan(&sName, &oName, &oType, &created, &modified, &cName, &tName, &maxLen, &prec, &scale, &nullable, &ident, &computed); err != nil {
			rows.Close()
			return nil, Friendly(err)
		}
		k := Norm(sName + "." + oName)
		i, ok := idx[k]
		if !ok {
			i = len(sch.Tables)
			idx[k] = i
			sch.Tables = append(sch.Tables, Table{Schema: sName, Name: oName, View: strings.TrimSpace(oType) == "V", Rows: -1, Created: created, Modified: modified})
		}
		sch.Tables[i].Columns = append(sch.Tables[i].Columns, Column{Name: cName, Type: TypeName(tName, maxLen, prec, scale),
			Nullable: nullable, Identity: ident, Computed: computed})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, Friendly(err)
	}
	rows.Close()

	warn := func(what string, err error) {
		sch.Warnings = append(sch.Warnings, what+": "+Friendly(err).Error())
	}
	if err := m.each(ctx, qPK, func(r *sql.Rows) error {
		var s, o, c string
		if err := r.Scan(&s, &o, &c); err != nil {
			return err
		}
		if i, ok := idx[Norm(s+"."+o)]; ok {
			if col := sch.Tables[i].Col(c); col != nil {
				col.PK = true
			}
		}
		return nil
	}); err != nil {
		warn("elsődleges kulcsok", err)
	}
	byName := map[string]int{}
	if err := m.each(ctx, qFK, func(r *sql.Rows) error {
		var name, ps, po, pc, rs, ro, rc string
		if err := r.Scan(&name, &ps, &po, &pc, &rs, &ro, &rc); err != nil {
			return err
		}
		from, to := ps+"."+po, rs+"."+ro
		k := Norm(from) + "|" + name
		i, ok := byName[k]
		if !ok {
			i = len(sch.FKs)
			byName[k] = i
			sch.FKs = append(sch.FKs, FK{Name: name, From: from, To: to})
		}
		sch.FKs[i].FromCols = append(sch.FKs[i].FromCols, pc)
		sch.FKs[i].ToCols = append(sch.FKs[i].ToCols, rc)
		return nil
	}); err != nil {
		warn("idegen kulcsok", err)
	}
	if err := m.each(ctx, qRows, func(r *sql.Rows) error {
		var s, o string
		var n sql.NullInt64
		if err := r.Scan(&s, &o, &n); err != nil {
			return err
		}
		if i, ok := idx[Norm(s+"."+o)]; ok && n.Valid {
			sch.Tables[i].Rows = n.Int64
		}
		return nil
	}); err != nil {
		warn("sorok száma", err)
	}
	if err := m.each(ctx, qDesc, func(r *sql.Rows) error {
		var s, o, c string
		var minor int64
		var v sql.NullString
		if err := r.Scan(&s, &o, &minor, &c, &v); err != nil {
			return err
		}
		i, ok := idx[Norm(s+"."+o)]
		if !ok || !v.Valid {
			return nil
		}
		if minor == 0 {
			sch.Tables[i].Desc = v.String
		} else if col := sch.Tables[i].Col(c); col != nil && c != "" {
			col.Desc = v.String
		}
		return nil
	}); err != nil {
		warn("leírások", err)
	}
	sch.TookMs = time.Since(start).Milliseconds()
	sch.FetchedAt = time.Now()
	return sch, nil
}

func (m *MSSQL) each(ctx context.Context, q string, fn func(*sql.Rows) error) error {
	rows, err := m.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Rows reads the first rows of a table – the only statement that touches
// table data, and it is a plain SELECT of a table taken from the catalog.
func (m *MSSQL) Rows(ctx context.Context, t *Table, limit int, progress func(int)) (*Result, error) {
	if t == nil || t.Gone {
		return nil, errors.New("a tábla nem létezik")
	}
	start := time.Now()
	q := fmt.Sprintf("SELECT TOP (%d) * FROM %s.%s", limit+1, Quote(t.Schema), Quote(t.Name))
	rows, err := m.db.QueryContext(ctx, q)
	if err != nil {
		return nil, Friendly(err)
	}
	defer rows.Close()
	cts, err := rows.ColumnTypes()
	if err != nil {
		return nil, Friendly(err)
	}
	cols := make([]ResultCol, len(cts))
	for i, ct := range cts {
		cols[i] = ResultCol{Name: ct.Name(), Type: strings.ToLower(ct.DatabaseTypeName()), Kind: KindOf(ct.DatabaseTypeName())}
	}
	res := NewResult(t.Key(), cols)
	res.Limit = limit
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if res.Total >= limit {
			res.Truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, Friendly(err)
		}
		row := make([]any, len(cols))
		for i, v := range vals {
			row[i] = Cell(v, cts[i].DatabaseTypeName())
		}
		res.Add(row)
		if progress != nil && res.Total%2000 == 0 {
			progress(res.Total)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, Friendly(err)
	}
	res.TookMs = time.Since(start).Milliseconds()
	return res, nil
}

// TypeName formats a column type like SSMS does: nvarchar(50), decimal(18,2).
func TypeName(t string, maxLen, prec, scale int64) string {
	switch strings.ToLower(t) {
	case "varchar", "char", "varbinary", "binary":
		if maxLen < 0 {
			return t + "(max)"
		}
		return fmt.Sprintf("%s(%d)", t, maxLen)
	case "nvarchar", "nchar":
		if maxLen < 0 {
			return t + "(max)"
		}
		return fmt.Sprintf("%s(%d)", t, maxLen/2)
	case "decimal", "numeric":
		return fmt.Sprintf("%s(%d,%d)", t, prec, scale)
	case "datetime2", "time", "datetimeoffset":
		if scale != 7 {
			return fmt.Sprintf("%s(%d)", t, scale)
		}
	}
	return t
}

// Friendly turns driver errors into Hungarian explanations.
func Friendly(err error) error {
	if err == nil {
		return nil
	}
	var se mssql.Error
	if errors.As(err, &se) {
		switch se.Number {
		case 18456:
			return errors.New("a bejelentkezés nem sikerült (hibás felhasználónév/jelszó, vagy ennek a Windows-felhasználónak nincs joga a szerverhez)")
		case 4060:
			return errors.New("az adatbázis nem nyitható meg: nincs ilyen nevű adatbázis, vagy nincs hozzá jogosultság")
		case 229, 230:
			return fmt.Errorf("nincs olvasási jogosultság: %s", se.Message)
		case 1222:
			return errors.New("a tábla épp zárolva van (valószínűleg most töltődik) – próbálja újra kicsit később")
		case 2812:
			return fmt.Errorf("a leírást író tárolt eljárás nem található – ellenőrizze a nevét (Beállítások → Adatbázis): %s", se.Message)
		}
		return fmt.Errorf("SQL Server: %s", se.Message)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("időtúllépés – a szerver nem válaszolt időben")
	}
	if errors.Is(err, context.Canceled) {
		return errors.New("megszakítva")
	}
	var ne net.Error
	if errors.As(err, &ne) || strings.Contains(err.Error(), "no such host") || strings.Contains(err.Error(), "unable to open tcp connection") {
		return fmt.Errorf("a szerver nem érhető el (név, példány vagy hálózat?): %v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "tls") || strings.Contains(err.Error(), "certificate") {
		return fmt.Errorf("titkosítási hiba – próbálja a „Titkosítás” beállítás másik lehetőségét: %v", err)
	}
	return err
}
