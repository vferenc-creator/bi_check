package app

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"bimonitor/internal/calendar"
	"bimonitor/internal/model"
	"bimonitor/internal/pathpattern"
)

// Shared team lists: a JSON export placed on a network share (e.g.
// \\EFS-FSRHQ\Groups\BI\monitor\csapat.json). Every subscriber watches the
// same items; one person maintains the file (export from their own list).
// Items are read-only locally, but can be muted or switched off per user.

const sharedPrefix = "shared:"

type sharedState struct {
	items    []model.Item
	modTime  time.Time
	loadedAt time.Time
	err      string
}

// SharedStatus is shown on the settings page.
type SharedStatus struct {
	model.SharedList
	Count    int       `json:"count"`
	LoadedAt time.Time `json:"loadedAt"`
	ModTime  time.Time `json:"modTime"`
	Error    string    `json:"error,omitempty"`
	IsDir    bool      `json:"isDir"` // the path is a folder (old setting meant for shared mode)
}

func sharedID(listID, itemID string) string { return sharedPrefix + listID + ":" + itemID }

// effectiveItems = personal items + items of enabled shared lists, with the
// user's personal overrides applied.
func (a *App) effectiveItems(s model.Settings) []model.Item {
	if ts := a.teamStore(); ts != nil {
		return a.teamItems(s, ts) // shared mode: the shared folder is the list
	}
	out := append([]model.Item(nil), s.Items...)
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, l := range s.SharedLists {
		if !l.Enabled {
			continue
		}
		st := a.shared[l.ID]
		if st == nil {
			continue
		}
		for _, it := range st.items {
			it.ID = sharedID(l.ID, it.ID)
			it.Source = l.Name
			if ov, ok := s.Overrides[it.ID]; ok && ov.Disabled {
				it.Enabled = false
			}
			out = append(out, it)
		}
	}
	return out
}

// readWithTimeout reads a (network) file without risking a hang.
func readWithTimeout(path string, d time.Duration) ([]byte, time.Time, error) {
	type res struct {
		b   []byte
		mod time.Time
		err error
	}
	ch := make(chan res, 1)
	go func() {
		st, err := os.Stat(path)
		if err != nil {
			ch <- res{err: err}
			return
		}
		if st.IsDir() {
			ch <- res{err: errors.New("ez egy mappa, nem lista-fájl – közös szerkesztéshez a Közös módot használja (Beállítások → Közös mód), ott ez a mappa megadható")}
			return
		}
		b, err := os.ReadFile(path)
		ch <- res{b, st.ModTime(), err}
	}()
	select {
	case r := <-ch:
		return r.b, r.mod, r.err
	case <-time.After(d):
		return nil, time.Time{}, fmt.Errorf("időtúllépés (%s)", d)
	}
}

// refreshShared (re)loads shared lists whose file changed. Returns true if
// the effective item list changed.
func (a *App) refreshShared(force bool) bool {
	s := a.Settings.Get()
	changed := false
	live := map[string]bool{}
	for _, l := range s.SharedLists {
		live[l.ID] = true
		if !l.Enabled {
			continue
		}
		a.mu.Lock()
		prev := a.shared[l.ID]
		a.mu.Unlock()
		if !force && prev != nil && time.Since(prev.loadedAt) < 4*time.Minute {
			continue
		}
		data, mod, err := readWithTimeout(l.Path, time.Duration(s.TimeoutSec+5)*time.Second)
		st := &sharedState{loadedAt: time.Now(), modTime: mod}
		if err == nil && prev != nil && prev.err == "" && mod.Equal(prev.modTime) {
			st.items = prev.items
		} else if err == nil {
			items, derr := DecodeItems(data)
			if derr != nil {
				err = derr
			} else {
				var ok []model.Item
				for _, it := range items {
					if it.ID == "" || it.Path == "" {
						continue
					}
					ok = append(ok, it)
				}
				st.items = ok
				changed = true
			}
		}
		if err != nil {
			st.err = err.Error()
			if prev != nil {
				st.items = prev.items // keep watching the last good version
			}
			log.Printf("közös lista (%s): %v", l.Name, err)
		}
		a.mu.Lock()
		a.shared[l.ID] = st
		a.mu.Unlock()
	}
	a.mu.Lock()
	for id := range a.shared {
		if !live[id] {
			delete(a.shared, id)
			changed = true
		}
	}
	a.mu.Unlock()
	return changed
}

func (a *App) sharedStatuses() []SharedStatus {
	s := a.Settings.Get()
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []SharedStatus{}
	for _, l := range s.SharedLists {
		ss := SharedStatus{SharedList: l, IsDir: !strings.HasSuffix(strings.ToLower(l.Path), ".json")}
		if st := a.shared[l.ID]; st != nil {
			ss.Count, ss.LoadedAt, ss.ModTime, ss.Error = len(st.items), st.loadedAt, st.modTime, st.err
		}
		out = append(out, ss)
	}
	return out
}

// setOverride changes a personal override of a shared item.
func (a *App) setOverride(id string, fn func(*model.ItemOverride)) error {
	if !strings.HasPrefix(id, sharedPrefix) {
		return errors.New("csak közös listás elemhez")
	}
	_, err := a.Settings.Update(func(s *model.Settings) error {
		if s.Overrides == nil {
			s.Overrides = map[string]model.ItemOverride{}
		}
		ov := s.Overrides[id]
		fn(&ov)
		if ov == (model.ItemOverride{}) {
			delete(s.Overrides, id)
		} else {
			s.Overrides[id] = ov
		}
		return nil
	})
	if err == nil {
		a.reconfigure()
	}
	return err
}

// CalendarView is returned by getCalendar.
type CalendarView struct {
	Year       int                `json:"year"`
	Days       []calendar.Day     `json:"days"`
	Overrides  calendar.Overrides `json:"overrides"`
	KnownYears []int              `json:"knownYears"`
	Shared     bool               `json:"shared"` // stored in the shared folder
}

func (a *App) registerTeamAPI() {
	a.register("sharedLists", func() ([]SharedStatus, error) { return a.sharedStatuses(), nil })

	a.register("saveSharedLists", func(lists []model.SharedList) ([]SharedStatus, error) {
		for i := range lists {
			l := &lists[i]
			l.Name = strings.TrimSpace(l.Name)
			l.Path = CleanPath(l.Path)
			if driveRe.MatchString(l.Path) {
				if unc, ok := a.P.ToUNC(l.Path); ok {
					l.Path = unc
				}
			}
			if l.Path == "" {
				return nil, errors.New("a közös lista útvonala kötelező")
			}
			if _, err := pathpattern.Parse(l.Path); err != nil {
				return nil, err
			}
			if l.Name == "" {
				l.Name = l.Path[strings.LastIndexAny(l.Path, `\/`)+1:]
			}
			if l.ID == "" {
				l.ID = model.NewID()[:8]
			}
		}
		if _, err := a.Settings.Update(func(s *model.Settings) error { s.SharedLists = lists; return nil }); err != nil {
			return nil, err
		}
		a.refreshShared(true)
		a.reconfigure()
		return a.sharedStatuses(), nil
	})

	a.register("removeLegacyList", func(id string) ([]SharedStatus, error) {
		_, err := a.Settings.Update(func(s *model.Settings) error {
			var out []model.SharedList
			for _, l := range s.SharedLists {
				if l.ID != id {
					out = append(out, l)
				}
			}
			s.SharedLists = out
			return nil
		})
		if err != nil {
			return nil, err
		}
		a.refreshShared(true)
		a.reconfigure()
		return a.sharedStatuses(), nil
	})

	a.register("reloadSharedLists", func() ([]SharedStatus, error) {
		a.refreshShared(true)
		a.reconfigure()
		return a.sharedStatuses(), nil
	})

	a.register("browseJSON", func() (BrowseResult, error) {
		p, ok := a.P.OpenFileDialog("Közös lista kiválasztása", "", []FileFilter{{"BI Monitor lista (JSON)", "*.json"}})
		if !ok {
			return BrowseResult{}, nil
		}
		out := BrowseResult{Path: p}
		if driveRe.MatchString(p) {
			if unc, ok := a.P.ToUNC(p); ok {
				out.Path, out.Converted = unc, true
			}
		}
		return out, nil
	})

	type ovParam struct {
		ID       string `json:"id"`
		Disabled *bool  `json:"disabled"`
		Mute     *bool  `json:"mute"`
	}
	a.register("setOverride", func(p ovParam) error {
		return a.setOverride(p.ID, func(ov *model.ItemOverride) {
			if p.Disabled != nil {
				ov.Disabled = *p.Disabled
			}
			if p.Mute != nil {
				ov.Mute = *p.Mute
			}
		})
	})

	a.register("getCalendar", func(year int) (CalendarView, error) {
		if year == 0 {
			year = time.Now().Year()
		}
		s := a.Settings.Get()
		return CalendarView{Year: year, Days: a.Calendar().Year(year), Overrides: a.calendarOverrides(s), KnownYears: calendar.KnownTransferYears(), Shared: a.teamStore() != nil}, nil
	})

	a.register("saveCalendar", func(o calendar.Overrides) error {
		for _, l := range [][]string{o.RestDays, o.WorkDays, o.Ignore} {
			for _, d := range l {
				if _, err := calendar.ParseDate(d); err != nil {
					return err
				}
			}
		}
		if ts := a.teamStore(); ts != nil {
			_, rev, _ := ts.Calendar()
			if err := ts.SaveCalendar(o, rev); err != nil {
				return teamErr(err)
			}
			a.reconfigure()
			return nil
		}
		if _, err := a.Settings.Update(func(s *model.Settings) error { s.Calendar = o; return nil }); err != nil {
			return err
		}
		a.reconfigure()
		return nil
	})
}
