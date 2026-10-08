package teamstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"strings"

	"bimonitor/internal/model"
	"bimonitor/internal/schedule"
)

func (s *Store) reportPath(id string) string { return s.dir("reports", id+".json") }

// definition strips runtime/personal fields before storing an item.
func definition(it model.Item) model.Item {
	it.Source = ""
	it.Rev = 0
	it.Notify = true // notifications are a personal setting in shared mode
	return it
}

// writeReport writes r (already incremented) and updates the in-memory state.
func (s *Store) writeReport(r *Report) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(s.dir("reports"), r.ID+".json", b, s.opt.Identity.Instance); err != nil {
		return err
	}
	info, err := os.Stat(s.reportPath(r.ID))
	s.mu.Lock()
	cp := *r
	s.reports[r.ID] = &cp
	if err == nil {
		s.meta[r.ID] = fileMeta{info.ModTime(), info.Size()}
	}
	s.saveCacheLocked()
	s.mu.Unlock()
	return nil
}

func addChange(r *Report, c Change) {
	r.Changes = append(r.Changes, c)
	if len(r.Changes) > MaxChanges {
		r.Changes = r.Changes[len(r.Changes)-MaxChanges:]
	}
}

// Create stores a new report. If it.ID is empty a new id is generated; an
// existing id is refused (use Save for updates).
func (s *Store) Create(it model.Item, action string) (Report, error) {
	if err := s.writable(); err != nil {
		return Report{}, err
	}
	if it.ID == "" {
		it.ID = NewID()
	}
	if action == "" {
		action = "created"
	}
	st := s.stamp()
	r := Report{Schema: SchemaVersion, ID: it.ID, Revision: 1, Created: st, Modified: st, Definition: definition(it)}
	addChange(&r, Change{Rev: 1, Stamp: st, Action: action})
	err := withTimeout(s.opt.Timeout, func() error {
		if err := os.MkdirAll(s.dir("reports"), 0o755); err != nil {
			return err
		}
		if _, err := os.Stat(s.reportPath(it.ID)); err == nil {
			return fmt.Errorf("már létezik riport ezzel az azonosítóval: %s", it.ID)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return s.writeReport(&r)
	})
	if err != nil {
		s.markOfflineIfNetwork(err)
		return Report{}, err
	}
	return r, nil
}

// readCurrent reads the report file directly from the share.
func (s *Store) readCurrent(id string) (Report, error) {
	var r Report
	err := readJSON(s.reportPath(id), &r)
	return r, err
}

// mutate is the common path of Save/Delete/Restore: it requires our lock,
// re-reads the file, checks the revision, applies fn and writes rev+1.
func (s *Store) mutate(id string, baseRev int, fn func(r *Report, st Stamp) (Change, error)) (Report, error) {
	if err := s.writable(); err != nil {
		return Report{}, err
	}
	var out Report
	err := withTimeout(s.opt.Timeout*2, func() error {
		l, err := s.readLock(id)
		if err != nil || !l.Mine {
			s.mu.Lock()
			delete(s.myLocks, id)
			s.mu.Unlock()
			return ErrNotLocked
		}
		cur, err := s.readCurrent(id)
		if errors.Is(err, errCorrupt) {
			// Half-written file on the share: repair it from the last good
			// version we know, if the caller's base matches that version.
			s.mu.Lock()
			good, ok := s.reports[id]
			s.mu.Unlock()
			if !ok || (baseRev >= 0 && good.Revision != baseRev) {
				return err
			}
			cur, err = *good, nil
		}
		if err != nil {
			return err
		}
		if cur.Schema > SchemaVersion {
			return &ReadOnlyError{fmt.Sprintf("A riportot egy újabb programverzió mentette (formátum %d) – frissítse a BI Monitort.", cur.Schema)}
		}
		if baseRev >= 0 && cur.Revision != baseRev {
			return &ConflictError{Current: cur, Base: baseRev}
		}
		st := s.stamp()
		c, err := fn(&cur, st)
		if err != nil {
			return err
		}
		cur.Revision++
		cur.Modified = st
		cur.Schema = SchemaVersion
		c.Rev, c.Stamp = cur.Revision, st
		addChange(&cur, c)
		if err := s.writeReport(&cur); err != nil {
			return err
		}
		out = cur
		return nil
	})
	if err != nil {
		var ce *ConflictError
		var ro *ReadOnlyError
		if !errors.As(err, &ce) && !errors.As(err, &ro) && err != ErrNotLocked {
			s.markOfflineIfNetwork(err)
		}
		return Report{}, err
	}
	return out, nil
}

// Save updates a report's definition. baseRev is the revision the editor
// opened; pass -1 to overwrite whatever is there (after the user decided so
// in the conflict dialog). The caller must hold the lock.
func (s *Store) Save(it model.Item, baseRev int) (Report, error) {
	return s.mutate(it.ID, baseRev, func(r *Report, _ Stamp) (Change, error) {
		if r.Deleted != nil {
			return Change{}, errors.New("a riport közben a lomtárba került")
		}
		fields := ChangedFields(r.Definition, definition(it))
		r.Definition = definition(it)
		return Change{Action: "modified", Fields: fields}, nil
	})
}

// Delete moves a report to the trash (soft delete). Needs the lock.
func (s *Store) Delete(id string, baseRev int) (Report, error) {
	return s.mutate(id, baseRev, func(r *Report, st Stamp) (Change, error) {
		d := st
		r.Deleted = &d
		return Change{Action: "deleted"}, nil
	})
}

// Restore brings a report back from the trash. Needs the lock.
func (s *Store) Restore(id string) (Report, error) {
	return s.mutate(id, -1, func(r *Report, _ Stamp) (Change, error) {
		if r.Deleted == nil {
			return Change{}, errors.New("a riport nincs a lomtárban")
		}
		r.Deleted = nil
		return Change{Action: "restored"}, nil
	})
}

// Purge deletes a trashed report permanently. Needs the lock.
func (s *Store) Purge(id string) error {
	if err := s.writable(); err != nil {
		return err
	}
	err := withTimeout(s.opt.Timeout, func() error {
		l, err := s.readLock(id)
		if err != nil || !l.Mine {
			return ErrNotLocked
		}
		cur, err := s.readCurrent(id)
		if err != nil {
			return err
		}
		if cur.Deleted == nil {
			return errors.New("csak lomtárban lévő riport törölhető véglegesen")
		}
		return retry(func() error { return os.Remove(s.reportPath(id)) })
	})
	if err == nil {
		s.mu.Lock()
		delete(s.reports, id)
		delete(s.meta, id)
		s.saveCacheLocked()
		s.mu.Unlock()
	}
	return err
}

// appendChange adds a log entry without changing the definition.
func (s *Store) appendChange(id string, c Change) error {
	_, err := s.mutate(id, -1, func(*Report, Stamp) (Change, error) { return c, nil })
	return err
}

// ChangedFields lists the (Hungarian) names of the fields that differ.
func ChangedFields(a, b model.Item) []string {
	type f struct {
		name string
		x, y any
	}
	fs := []f{
		{"név", a.Name, b.Name}, {"csoport", a.Group, b.Group}, {"felelős", a.Owner, b.Owner}, {"megjegyzés", a.Note, b.Note},
		{"útvonal", a.Path, b.Path}, {"dátum token", a.Token, b.Token}, {"ütemezés", a.Schedule, b.Schedule},
		{"türelmi idő", a.GraceMinutes, b.GraceMinutes}, {"korai tolerancia", a.EarlyMinutes, b.EarlyMinutes},
		{"gyanússági szabályok", a.Suspicious, b.Suspicious}, {"figyelés be/ki", a.Enabled, b.Enabled}, {"e-mail címzettek", a.EmailTo, b.EmailTo},
	}
	var out []string
	for _, x := range fs {
		if sa, ok := x.x.(schedule.Spec); ok {
			// Compare by meaning: the editor fills in defaults (e.g. a 60
			// minute interval) that do not change the schedule.
			if sa.Describe() != x.y.(schedule.Spec).Describe() {
				out = append(out, x.name)
			}
			continue
		}
		if !reflect.DeepEqual(x.x, x.y) {
			out = append(out, x.name)
		}
	}
	return out
}

// ---- Migration helper ---------------------------------------------------------------

// Duplicate finds an existing (non-deleted) report with the same id or path.
func (s *Store) Duplicate(it model.Item) (Report, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.reports[it.ID]; ok {
		return *r, "id", true
	}
	for _, r := range s.reports {
		if r.Deleted == nil && strings.EqualFold(r.Definition.Path, it.Path) {
			return *r, "path", true
		}
	}
	return Report{}, "", false
}
