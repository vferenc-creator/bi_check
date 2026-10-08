package app

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"bimonitor/internal/model"
	"bimonitor/internal/schedule"
)

const bom = "\xef\xbb\xbf"

// ExportFormat identifies our JSON files (also used for shared team lists).
const ExportFormat = "energofish-bimonitor"

// ExportFile is the JSON export document.
type ExportFile struct {
	Format     string       `json:"format"`
	Version    int          `json:"version"`
	ExportedAt time.Time    `json:"exportedAt"`
	ExportedBy string       `json:"exportedBy,omitempty"`
	Items      []model.Item `json:"items"`
}

var csvHeader = []string{"id", "nev", "csoport", "felelos", "megjegyzes", "utvonal", "datum_token", "utemezes_leiras",
	"utemezes_json", "turelmi_ido_perc", "korai_perc", "bekapcsolva", "ertesites", "nulla_bajt_gyanus", "min_meret_bajt", "meretcsokkenes_szazalek",
	"hiany_engedelyezve", "hiany_tol", "hiany_ig"}

func boolStr(b bool) string {
	if b {
		return "igen"
	}
	return "nem"
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "igen", "i", "1", "true", "yes", "y", "x":
		return true
	}
	return false
}

// EncodeCSV writes items as semicolon separated UTF-8 CSV with BOM (opens
// directly in Hungarian Excel).
func EncodeCSV(items []model.Item) []byte {
	var buf bytes.Buffer
	buf.WriteString(bom)
	w := csv.NewWriter(&buf)
	w.Comma = ';'
	_ = w.Write(csvHeader)
	for _, it := range items {
		sj, _ := json.Marshal(it.Schedule)
		_ = w.Write([]string{it.ID, it.Name, it.Group, it.Owner, it.Note, it.Path, string(it.Token), it.Schedule.Describe(), string(sj),
			strconv.Itoa(it.GraceMinutes), strconv.Itoa(it.EarlyMinutes), boolStr(it.Enabled), boolStr(it.Notify),
			boolStr(it.Suspicious.ZeroBytes), strconv.FormatInt(it.Suspicious.MinBytes, 10), strconv.Itoa(it.Suspicious.DropPercent),
			boolStr(it.Gap.Enabled), it.Gap.From, it.Gap.To})
	}
	w.Flush()
	return buf.Bytes()
}

// DecodeItems parses an export (JSON or CSV).
func DecodeItems(data []byte) ([]model.Item, error) {
	data = bytes.TrimPrefix(data, []byte(bom))
	trim := bytes.TrimSpace(data)
	if len(trim) > 0 && (trim[0] == '{' || trim[0] == '[') {
		if trim[0] == '[' {
			var items []model.Item
			if err := json.Unmarshal(trim, &items); err != nil {
				return nil, fmt.Errorf("hibás JSON: %w", err)
			}
			return items, nil
		}
		var f ExportFile
		if err := json.Unmarshal(trim, &f); err != nil {
			return nil, fmt.Errorf("hibás JSON: %w", err)
		}
		if f.Format != "" && f.Format != ExportFormat {
			return nil, fmt.Errorf("ismeretlen fájlformátum: %q", f.Format)
		}
		return f.Items, nil
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = ';'
	if !bytes.Contains(bytes.SplitN(data, []byte("\n"), 2)[0], []byte(";")) {
		r.Comma = ','
	}
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("hibás CSV: %w", err)
	}
	if len(rows) < 1 {
		return nil, errors.New("üres fájl")
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	get := func(row []string, name string) string {
		if i, ok := col[name]; ok && i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}
	if _, ok := col["utvonal"]; !ok {
		return nil, errors.New("a CSV-ből hiányzik az „utvonal” oszlop")
	}
	var items []model.Item
	for n, row := range rows[1:] {
		if len(row) == 1 && strings.TrimSpace(row[0]) == "" {
			continue
		}
		it := model.NewItem()
		it.ID = get(row, "id")
		it.Name = get(row, "nev")
		it.Group = get(row, "csoport")
		it.Owner = get(row, "felelos")
		it.Note = get(row, "megjegyzes")
		it.Path = get(row, "utvonal")
		it.Token = model.TokenMode(get(row, "datum_token"))
		if sj := get(row, "utemezes_json"); sj != "" {
			var sp schedule.Spec
			if err := json.Unmarshal([]byte(sj), &sp); err != nil {
				return nil, fmt.Errorf("%d. sor: hibás ütemezés: %v", n+2, err)
			}
			it.Schedule = sp
		}
		if v := get(row, "turelmi_ido_perc"); v != "" {
			it.GraceMinutes, _ = strconv.Atoi(v)
		}
		if v := get(row, "korai_perc"); v != "" {
			it.EarlyMinutes, _ = strconv.Atoi(v)
		}
		if v := get(row, "bekapcsolva"); v != "" {
			it.Enabled = parseBool(v)
		}
		if v := get(row, "ertesites"); v != "" {
			it.Notify = parseBool(v)
		}
		if v := get(row, "nulla_bajt_gyanus"); v != "" {
			it.Suspicious.ZeroBytes = parseBool(v)
		}
		if v := get(row, "min_meret_bajt"); v != "" {
			it.Suspicious.MinBytes, _ = strconv.ParseInt(v, 10, 64)
		}
		if v := get(row, "meretcsokkenes_szazalek"); v != "" {
			it.Suspicious.DropPercent, _ = strconv.Atoi(v)
		}
		it.Gap = model.GapWindow{Enabled: parseBool(get(row, "hiany_engedelyezve")), From: get(row, "hiany_tol"), To: get(row, "hiany_ig")}
		items = append(items, it)
	}
	return items, nil
}

// ImportRow describes what importing one item would do.
type ImportRow struct {
	Item   model.Item `json:"item"`
	Action string     `json:"action"` // add | update | invalid
	Error  string     `json:"error,omitempty"`
	Match  string     `json:"match,omitempty"` // name of the existing item
}

// ImportPreview is shown before applying an import.
type ImportPreview struct {
	File string      `json:"file"`
	Rows []ImportRow `json:"rows"`
}

func (a *App) previewImport(items []model.Item) []ImportRow {
	existing := a.Settings.Get().Items
	if a.teamStore() != nil {
		existing = a.effectiveItems(a.Settings.Get())
	}
	byID := map[string]model.Item{}
	byPath := map[string]model.Item{}
	for _, it := range existing {
		byID[it.ID] = it
		byPath[strings.ToLower(it.Path)] = it
	}
	var rows []ImportRow
	for _, it := range items {
		it.Source = ""
		row := ImportRow{Item: it, Action: "add"}
		cp := it
		if _, err := a.normalizeItem(&cp); err != nil {
			row.Action, row.Error = "invalid", err.Error()
		} else {
			row.Item = cp
			if ex, ok := byID[it.ID]; ok && it.ID != "" {
				row.Action, row.Match = "update", ex.Name
			} else if ex, ok := byPath[strings.ToLower(cp.Path)]; ok {
				row.Action, row.Match = "update", ex.Name
				row.Item.ID = ex.ID
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func (a *App) applyImport(rows []ImportRow) (map[string]int, error) {
	counts := map[string]int{"added": 0, "updated": 0}
	if ts := a.teamStore(); ts != nil {
		var errs []string
		for _, r := range rows {
			if r.Action == "invalid" {
				continue
			}
			it := r.Item
			if _, err := a.normalizeItem(&it); err != nil {
				errs = append(errs, it.Name+": "+err.Error())
				continue
			}
			if r.Action == "update" {
				if err := withTeamLock(ts, it.ID, func() error { _, err := ts.Save(it, -1); return err }); err != nil {
					errs = append(errs, it.Name+": "+err.Error())
					continue
				}
				counts["updated"]++
				continue
			}
			if _, _, dup := ts.Duplicate(it); dup {
				it.ID = ""
			}
			if _, err := ts.Create(it, "imported"); err != nil {
				errs = append(errs, it.Name+": "+teamErr(err).Error())
				continue
			}
			counts["added"]++
		}
		a.reconfigure()
		if len(errs) > 0 {
			return counts, errors.New(strings.Join(errs, "\n"))
		}
		return counts, nil
	}
	now := time.Now()
	_, err := a.Settings.Update(func(s *model.Settings) error {
		idx := map[string]int{}
		for i, it := range s.Items {
			idx[it.ID] = i
		}
		for _, r := range rows {
			it := r.Item
			if r.Action == "invalid" {
				continue
			}
			if _, err := a.normalizeItem(&it); err != nil {
				return fmt.Errorf("%s: %w", it.Name, err)
			}
			it.Source = ""
			it.UpdatedAt = now
			if i, ok := idx[it.ID]; ok && r.Action == "update" {
				it.CreatedAt = s.Items[i].CreatedAt
				s.Items[i] = it
				counts["updated"]++
				continue
			}
			if _, clash := idx[it.ID]; clash || it.ID == "" {
				it.ID = model.NewID()
			}
			if it.CreatedAt.IsZero() {
				it.CreatedAt = now
			}
			idx[it.ID] = len(s.Items)
			s.Items = append(s.Items, it)
			counts["added"]++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	a.reconfigure()
	return counts, nil
}

func (a *App) registerTransferAPI() {
	type exportParam struct {
		Format string   `json:"format"` // json | csv
		IDs    []string `json:"ids"`    // empty = all personal items
	}
	a.register("exportItems", func(p exportParam) (string, error) {
		all := a.Settings.Get().Items
		var items []model.Item
		if len(p.IDs) == 0 {
			items = all
		} else {
			want := map[string]bool{}
			for _, id := range p.IDs {
				want[id] = true
			}
			for _, it := range all {
				if want[it.ID] {
					items = append(items, it)
				}
			}
		}
		stamp := time.Now().Format("20060102")
		var data []byte
		var name, ext string
		var filters []FileFilter
		if p.Format == "csv" {
			data = EncodeCSV(items)
			name, ext = "bi-monitor-elemek-"+stamp+".csv", "csv"
			filters = []FileFilter{{"CSV (Excel)", "*.csv"}}
		} else {
			doc := ExportFile{Format: ExportFormat, Version: 1, ExportedAt: time.Now(), ExportedBy: os.Getenv("USERNAME"), Items: items}
			var err error
			if data, err = json.MarshalIndent(doc, "", "  "); err != nil {
				return "", err
			}
			name, ext = "bi-monitor-elemek-"+stamp+".json", "json"
			filters = []FileFilter{{"BI Monitor lista (JSON)", "*.json"}}
		}
		path, ok := a.P.SaveFileDialog("Figyelt elemek exportálása", name, ext, filters)
		if !ok {
			return "", nil
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return "", fmt.Errorf("a fájl írása nem sikerült: %w", err)
		}
		return path, nil
	})

	a.register("importPreview", func() (ImportPreview, error) {
		path, ok := a.P.OpenFileDialog("Figyelt elemek importálása", "", []FileFilter{{"BI Monitor lista (JSON, CSV)", "*.json;*.csv"}, {"Minden fájl", "*.*"}})
		if !ok {
			return ImportPreview{}, nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return ImportPreview{}, fmt.Errorf("a fájl nem olvasható: %w", err)
		}
		items, err := DecodeItems(data)
		if err != nil {
			return ImportPreview{}, err
		}
		if len(items) == 0 {
			return ImportPreview{}, errors.New("a fájl nem tartalmaz elemeket")
		}
		return ImportPreview{File: path, Rows: a.previewImport(items)}, nil
	})

	a.register("importApply", func(rows []ImportRow) (map[string]int, error) { return a.applyImport(rows) })
}
