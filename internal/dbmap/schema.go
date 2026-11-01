// Package dbmap is the read-only database browser: it fetches the structure
// of a Microsoft SQL database on request, keeps a hand-made "map" of it
// (table and column descriptions, relations drawn by the team, groups,
// notes, positions) and lets one look at the rows of a table.
//
// Nothing here ever writes to the database: the only statements sent are
// the fixed catalog queries in mssql.go and "SELECT TOP (n) * FROM [s].[t]"
// for tables that exist in the fetched structure.
package dbmap

import (
	"strings"
	"time"
)

// Schema is the structure of the database at one fetch.
type Schema struct {
	Server        string    `json:"server"`
	Database      string    `json:"database"`
	ServerVersion string    `json:"serverVersion,omitempty"`
	FetchedAt     time.Time `json:"fetchedAt"`
	// Changed: last change of the stored structure – the fetch itself or a
	// description written/re-read since (other computers reload on change).
	Changed   time.Time `json:"changed,omitempty"`
	FetchedBy string    `json:"fetchedBy,omitempty"`
	TookMs    int64     `json:"tookMs"`
	Tables    []Table   `json:"tables"`
	FKs       []FK      `json:"fks"`
	// Warnings: parts that could not be read (e.g. no permission for row
	// counts) – the structure itself is still usable.
	Warnings []string `json:"warnings,omitempty"`
}

// Table is a table or view.
type Table struct {
	Schema   string    `json:"schema"`
	Name     string    `json:"name"`
	View     bool      `json:"view,omitempty"`
	Rows     int64     `json:"rows"` // -1 = unknown (views)
	Created  time.Time `json:"created"`
	Modified time.Time `json:"modified"`
	Desc     string    `json:"desc,omitempty"` // MS_Description in the database
	Columns  []Column  `json:"columns"`
	// Gone: was there at an earlier fetch but not at the last one (kept so
	// its descriptions are not lost).
	Gone bool `json:"gone,omitempty"`
}

// Column of a table.
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable,omitempty"`
	Identity bool   `json:"identity,omitempty"`
	Computed bool   `json:"computed,omitempty"`
	PK       bool   `json:"pk,omitempty"`
	Desc     string `json:"desc,omitempty"` // MS_Description in the database
}

// FK is a foreign key defined in the database.
type FK struct {
	Name     string   `json:"name"`
	From     string   `json:"from"` // table key of the referencing table
	FromCols []string `json:"fromCols"`
	To       string   `json:"to"`
	ToCols   []string `json:"toCols"`
}

// Stamp identifies the version of a stored structure.
func (s *Schema) Stamp() time.Time {
	if s == nil {
		return time.Time{}
	}
	if !s.Changed.IsZero() {
		return s.Changed
	}
	return s.FetchedAt
}

// Key is "schema.name".
func (t *Table) Key() string { return t.Schema + "." + t.Name }

// Norm is the case-insensitive form of a key or column name (SQL Server
// identifiers are case-insensitive under the usual collations).
func Norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Find returns the table with the given key (case-insensitive).
func (s *Schema) Find(key string) *Table {
	if s == nil {
		return nil
	}
	k := Norm(key)
	for i := range s.Tables {
		if Norm(s.Tables[i].Key()) == k {
			return &s.Tables[i]
		}
	}
	return nil
}

// Col returns the named column (case-insensitive).
func (t *Table) Col(name string) *Column {
	if t == nil {
		return nil
	}
	n := Norm(name)
	for i := range t.Columns {
		if Norm(t.Columns[i].Name) == n {
			return &t.Columns[i]
		}
	}
	return nil
}

// Quote returns a bracket-quoted SQL Server identifier.
func Quote(id string) string { return "[" + strings.ReplaceAll(id, "]", "]]") + "]" }
