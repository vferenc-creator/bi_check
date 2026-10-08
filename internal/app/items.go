package app

import (
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"bimonitor/internal/engine"
	"bimonitor/internal/model"
	"bimonitor/internal/pathpattern"
	"bimonitor/internal/schedule"
	"bimonitor/internal/status"
	"bimonitor/internal/teamstore"
)

// ItemView is an item together with its live state.
type ItemView struct {
	Item  model.Item       `json:"item"`
	State engine.ItemState `json:"state"`
	Team  *TeamItemInfo    `json:"team,omitempty"` // shared mode only
}

// Snapshot is what the UI renders.
type Snapshot struct {
	Items       []ItemView `json:"items"`
	Servers     any        `json:"servers"`
	Paused      time.Time  `json:"pausedUntil"`
	At          time.Time  `json:"at"`
	Team        *TeamView  `json:"team"`
	MutedGroups []string   `json:"mutedGroups"`
}

func (a *App) snapshot() Snapshot {
	s := a.Settings.Get()
	states := map[string]engine.ItemState{}
	for _, st := range a.Engine.States() {
		states[st.ID] = st
	}
	items := a.effectiveItems(s)
	out := Snapshot{Items: make([]ItemView, 0, len(items)), Servers: a.Engine.Servers(), Paused: s.Notifications.PausedUntil, At: time.Now(),
		Team: a.teamView(s), MutedGroups: s.MutedGroups}
	ts, tst := a.teamStore(), a.teamState()
	var locks map[string]teamstore.Lock
	if ts != nil {
		locks = ts.Locks()
	}
	for _, it := range items {
		st, ok := states[it.ID]
		if !ok {
			st = engine.ItemState{ID: it.ID, Status: model.StatusUnknown, ScheduleText: it.Schedule.Describe()}
		}
		v := ItemView{Item: it, State: st}
		if ts != nil && tst != nil {
			v.Team = a.teamInfo(s, ts, tst, it.ID, locks)
		}
		out.Items = append(out.Items, v)
	}
	return out
}

func (a *App) findItem(id string) (model.Item, bool) {
	for _, it := range a.effectiveItems(a.Settings.Get()) {
		if it.ID == id {
			return it, true
		}
	}
	return model.Item{}, false
}

func (a *App) view(id string) ItemView {
	it, _ := a.findItem(id)
	st, _ := a.Engine.State(id)
	v := ItemView{Item: it, State: st}
	if ts, tst := a.teamStore(), a.teamState(); ts != nil && tst != nil {
		v.Team = a.teamInfo(a.Settings.Get(), ts, tst, id, ts.Locks())
	}
	return v
}

var driveRe = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// normalizeItem trims, converts mapped drives to UNC and validates.
func (a *App) normalizeItem(it *model.Item) (warnings []string, err error) {
	it.Name = strings.TrimSpace(it.Name)
	it.Group = strings.TrimSpace(it.Group)
	it.Owner = strings.TrimSpace(it.Owner)
	it.Path = CleanPath(it.Path)
	if it.Name == "" {
		return nil, errors.New("a név megadása kötelező")
	}
	if it.Path == "" {
		return nil, errors.New("az útvonal megadása kötelező")
	}
	if driveRe.MatchString(it.Path) {
		if unc, ok := a.P.ToUNC(it.Path); ok {
			warnings = append(warnings, fmt.Sprintf("A csatolt meghajtós útvonalat UNC-re alakítottam: %s", unc))
			it.Path = unc
		} else {
			warnings = append(warnings, "Helyi meghajtós útvonal: más gépeken (és más felhasználóknál) nem biztos, hogy ugyanígy elérhető. Hálózati fájlnál használjon \\\\szerver\\megosztás\\… formát.")
		}
	}
	if _, err := pathpattern.Parse(it.Path); err != nil {
		return nil, err
	}
	if _, err := schedule.Compile(it.Schedule, a.Calendar(), a.Loc); err != nil {
		return nil, fmt.Errorf("ütemezés: %w", err)
	}
	if it.GraceMinutes < 0 || it.GraceMinutes > 7*24*60 {
		return nil, errors.New("a türelmi idő 0 perc és 7 nap között lehet")
	}
	if it.EarlyMinutes < 0 || it.EarlyMinutes > 24*60 {
		return nil, errors.New("a korai tolerancia 0 perc és 24 óra között lehet")
	}
	if it.Suspicious.DropPercent < 0 || it.Suspicious.DropPercent > 99 {
		return nil, errors.New("a méretcsökkenés küszöbe 0 és 99% között lehet")
	}
	if it.Suspicious.MinBytes < 0 {
		return nil, errors.New("a minimális méret nem lehet negatív")
	}
	if err := it.Gap.Validate(); err != nil {
		return nil, err
	}
	return warnings, nil
}

// SaveResult is returned by saveItem.
type SaveResult struct {
	View     ItemView      `json:"view"`
	Warnings []string      `json:"warnings,omitempty"`
	Conflict *ConflictInfo `json:"conflict,omitempty"` // shared mode: newer version on the share
}

func (a *App) saveItem(it model.Item) (SaveResult, error) {
	if it.Source != "" {
		return SaveResult{}, errors.New("közös listából származó elem itt nem szerkeszthető – másolja a saját listájába")
	}
	warn, err := a.normalizeItem(&it)
	if err != nil {
		return SaveResult{}, err
	}
	if ts := a.teamStore(); ts != nil {
		res, err := a.teamSave(ts, it, false)
		res.Warnings = warn
		return res, err
	}
	now := time.Now()
	_, err = a.Settings.Update(func(s *model.Settings) error {
		if it.ID == "" {
			it.ID = model.NewID()
			it.CreatedAt = now
			it.UpdatedAt = now
			s.Items = append(s.Items, it)
			return nil
		}
		for i := range s.Items {
			if s.Items[i].ID == it.ID {
				it.CreatedAt = s.Items[i].CreatedAt
				it.UpdatedAt = now
				s.Items[i] = it
				return nil
			}
		}
		if it.CreatedAt.IsZero() {
			it.CreatedAt = now
		}
		it.UpdatedAt = now
		s.Items = append(s.Items, it)
		return nil
	})
	if err != nil {
		return SaveResult{}, err
	}
	a.reconfigure()
	return SaveResult{View: a.view(it.ID), Warnings: warn}, nil
}

type idParam struct {
	ID string `json:"id"`
}

type enableParam struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

// PreviewParams for previewSchedule.
type PreviewParams struct {
	Schedule schedule.Spec `json:"schedule"`
	Count    int           `json:"count"`
}

// Preview describes upcoming expected times.
type Preview struct {
	Text  string      `json:"text"`
	Error string      `json:"error,omitempty"`
	Prev  *time.Time  `json:"prev,omitempty"`
	Next  []time.Time `json:"next"`
	Notes []string    `json:"notes,omitempty"` // holiday remarks for the listed days
}

// TestResult is returned by testPath.
type TestResult struct {
	Status   model.Status `json:"status"`
	Reason   string       `json:"reason"`
	Expected *time.Time   `json:"expected,omitempty"`
	Deadline *time.Time   `json:"deadline,omitempty"`
	Result   any          `json:"result"`
	Warnings []string     `json:"warnings,omitempty"`
	Error    string       `json:"error,omitempty"`
	NormPath string       `json:"normPath"`
}

// BrowseResult is returned by browseFile.
type BrowseResult struct {
	Path        string   `json:"path"`
	Converted   bool     `json:"converted"`
	Local       bool     `json:"local"`
	Suggestions []string `json:"suggestions,omitempty"`
}

func (a *App) registerItemAPI() {
	a.register("listItems", func() (Snapshot, error) { return a.snapshot(), nil })

	a.register("newItem", func() (model.Item, error) {
		it := model.NewItem()
		it.ID = ""
		return it, nil
	})

	a.register("saveItem", a.saveItem)

	a.register("deleteItem", func(p idParam) error {
		if ts := a.teamStore(); ts != nil {
			err := withTeamLock(ts, p.ID, func() error { _, err := ts.Delete(p.ID, -1); return err })
			a.reconfigure()
			return err
		}
		_, err := a.Settings.Update(func(s *model.Settings) error {
			for i := range s.Items {
				if s.Items[i].ID == p.ID {
					s.Items = append(s.Items[:i], s.Items[i+1:]...)
					return nil
				}
			}
			return errors.New("az elem nem található (lehet, hogy közös listából származik)")
		})
		if err == nil {
			a.reconfigure()
		}
		return err
	})

	a.register("duplicateItem", func(p idParam) (ItemView, error) {
		src, ok := a.findItem(p.ID)
		if !ok {
			return ItemView{}, errors.New("az elem nem található")
		}
		cp := src
		cp.ID = ""
		cp.Source = ""
		cp.Name = src.Name + " (másolat)"
		r, err := a.saveItem(cp)
		return r.View, err
	})

	a.register("setEnabled", func(p enableParam) (ItemView, error) {
		if ts := a.teamStore(); ts != nil {
			r, ok := ts.Get(p.ID)
			if !ok {
				return ItemView{}, errors.New("az elem nem található")
			}
			it := r.Definition
			it.ID = r.ID
			it.Enabled = p.Enabled
			err := withTeamLock(ts, p.ID, func() error { _, err := ts.Save(it, -1); return err })
			a.reconfigure()
			return a.view(p.ID), err
		}
		if strings.HasPrefix(p.ID, sharedPrefix) {
			err := a.setOverride(p.ID, func(ov *model.ItemOverride) { ov.Disabled = !p.Enabled })
			return a.view(p.ID), err
		}
		_, err := a.Settings.Update(func(s *model.Settings) error {
			for i := range s.Items {
				if s.Items[i].ID == p.ID {
					s.Items[i].Enabled = p.Enabled
					s.Items[i].UpdatedAt = time.Now()
					return nil
				}
			}
			return errors.New("az elem nem található")
		})
		if err != nil {
			return ItemView{}, err
		}
		a.reconfigure()
		return a.view(p.ID), nil
	})

	a.register("checkNow", func(id string) (Snapshot, error) {
		a.Checker.ResetServers()
		var done <-chan struct{}
		if id == "" {
			done = a.Engine.CheckNow()
		} else {
			done = a.Engine.CheckNow(id)
		}
		wait := time.Duration(a.Settings.Get().TimeoutSec)*time.Second*3 + 5*time.Second
		select {
		case <-done:
		case <-time.After(wait):
		}
		return a.snapshot(), nil
	})

	a.register("previewSchedule", func(p PreviewParams) (Preview, error) {
		out := Preview{Text: p.Schedule.Describe(), Next: []time.Time{}}
		cal := a.Calendar()
		sc, err := schedule.Compile(p.Schedule, cal, a.Loc)
		if err != nil {
			out.Error = err.Error()
			return out, nil
		}
		n := p.Count
		if n <= 0 || n > 50 {
			n = 10
		}
		now := time.Now().In(a.Loc)
		if prev, ok := sc.Prev(now); ok {
			out.Prev = &prev
		}
		out.Next = sc.NextN(now, n)
		return out, nil
	})

	a.register("testPath", func(it model.Item) (TestResult, error) {
		warn, err := a.normalizeItem(&it)
		if err != nil {
			return TestResult{Error: err.Error(), NormPath: it.Path}, nil
		}
		sc, _ := schedule.Compile(it.Schedule, a.Calendar(), a.Loc)
		now := time.Now()
		exp, has := sc.Prev(now)
		var prev time.Time
		ref := now
		if has {
			prev, _ = sc.PrevBefore(exp)
			ref = exp
		}
		res := a.Checker.Check(it.Path, ref.In(a.Loc), it.Token == model.TokenAny)
		out := status.Evaluate(status.Input{Now: time.Now(), Expected: exp, HasExpected: has, PrevExpected: prev,
			Grace: it.Grace(), Early: it.Early(), Result: res, Rules: it.Suspicious, Gap: it.Gap, Loc: a.Loc})
		tr := TestResult{Status: out.Status, Reason: out.Reason, Result: res, Warnings: warn, NormPath: it.Path}
		if has {
			tr.Expected = &exp
			d := out.Deadline
			tr.Deadline = &d
		}
		return tr, nil
	})

	a.register("browseFile", func(current string) (BrowseResult, error) {
		current = CleanPath(current)
		dir := ""
		if c := strings.TrimSpace(current); c != "" && !strings.ContainsAny(c, "*?{") {
			dir = filepath.Dir(c)
		} else if c != "" {
			if i := strings.LastIndexAny(c, `\/`); i > 0 && !strings.ContainsAny(c[:i], "*?{") {
				dir = c[:i]
			}
		}
		log.Printf("browseFile: kezdő mappa %q", dir)
		p, ok := a.P.OpenFileDialog("Figyelendő fájl kiválasztása", dir, []FileFilter{
			{"Minden fájl", "*.*"},
			{"Excel", "*.xlsx;*.xlsm;*.xls"},
			{"CSV / szöveg", "*.csv;*.txt"},
			{"Parquet", "*.parquet"},
		})
		log.Printf("browseFile: eredmény ok=%v %q", ok, p)
		if !ok {
			return BrowseResult{}, nil
		}
		out := BrowseResult{Path: p}
		if driveRe.MatchString(p) {
			if unc, ok := a.P.ToUNC(p); ok {
				out.Path, out.Converted = unc, true
			} else {
				out.Local = true
			}
		}
		out.Suggestions = pathpattern.Suggest(out.Path)
		return out, nil
	})

	a.register("toUNC", func(p string) (BrowseResult, error) {
		p = CleanPath(p)
		out := BrowseResult{Path: p}
		if driveRe.MatchString(p) {
			if unc, ok := a.P.ToUNC(p); ok {
				out.Path, out.Converted = unc, true
			} else {
				out.Local = true
			}
		}
		out.Suggestions = pathpattern.Suggest(out.Path)
		return out, nil
	})

	type openParam struct {
		ID     string `json:"id"`
		Target string `json:"target"` // "file" | "folder"
	}
	a.register("openPath", func(p openParam) error {
		it, ok := a.findItem(p.ID)
		if !ok {
			return errors.New("az elem nem található")
		}
		st, _ := a.Engine.State(p.ID)
		if p.Target == "file" {
			if st.File == nil {
				return errors.New("nincs megnyitható fájl (még nem található)")
			}
			return a.P.ShellOpen(st.File.Path)
		}
		if st.File != nil {
			return a.P.ShowInFolder(st.File.Path)
		}
		dir := st.Resolved
		if dir == "" {
			dir = it.Path
		}
		if i := strings.LastIndexAny(dir, `\/`); i > 0 {
			dir = dir[:i]
		}
		return a.P.ShellOpen(dir)
	})

	a.register("groups", func() ([]string, error) {
		seen := map[string]bool{}
		var out []string
		for _, it := range a.effectiveItems(a.Settings.Get()) {
			if it.Group != "" && !seen[it.Group] {
				seen[it.Group] = true
				out = append(out, it.Group)
			}
		}
		for _, g := range []string{"KNIME", "DyntellBI", "ERP export", "Riport"} {
			if !seen[g] {
				out = append(out, g)
			}
		}
		return out, nil
	})
}
