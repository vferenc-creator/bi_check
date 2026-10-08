package teamstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"time"
)

func (s *Store) lockPath(id string) string { return s.dir("locks", id+".lock") }

// readLock reads a lock file; its heartbeat is the file's modification time.
// A lock file that exists but cannot be parsed yet (being written right now)
// counts as held by an unknown owner.
func (s *Store) readLock(id string) (Lock, error) {
	p := s.lockPath(id)
	info, err := os.Stat(p)
	if err != nil {
		return Lock{}, err
	}
	var l Lock
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Lock{}, err
		}
		l = Lock{ID: id, Since: info.ModTime()}
	} else if json.Unmarshal(b, &l) != nil {
		l = Lock{ID: id, Since: info.ModTime()}
	}
	l.ID = id
	l.Heartbeat = info.ModTime()
	l.Expired = s.now().Sub(l.Heartbeat) > s.opt.LockTTL
	l.Mine = l.Owner.Instance != "" && l.Owner.Instance == s.opt.Identity.Instance
	return l, nil
}

// createLock creates the lock file exclusively. Only one instance can win.
func (s *Store) createLock(id string) error {
	now := s.now()
	l := Lock{ID: id, Owner: s.opt.Identity, Since: now}
	b, _ := json.MarshalIndent(l, "", "  ")
	p := s.lockPath(id)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(p)
		if werr != nil {
			return werr
		}
		return cerr
	}
	_ = os.Chtimes(p, now, now)
	return nil
}

// Acquire takes the edit lock of a report (or of "_calendar"). If another
// instance holds it, a *LockedError is returned – unless its heartbeat is
// older than the TTL, in which case the orphaned lock is taken over.
func (s *Store) Acquire(id string) (Lock, error) {
	if err := s.writable(); err != nil {
		return Lock{}, err
	}
	var result Lock
	err := withTimeout(s.opt.Timeout, func() error {
		if err := os.MkdirAll(s.dir("locks"), 0o755); err != nil {
			return err
		}
		for attempt := 0; attempt < 3; attempt++ {
			err := s.createLock(id)
			if err == nil {
				l, rerr := s.readLock(id)
				if rerr != nil {
					return rerr
				}
				result = l
				return nil
			}
			if !errors.Is(err, fs.ErrExist) {
				return err
			}
			cur, rerr := s.readLock(id)
			if errors.Is(rerr, fs.ErrNotExist) {
				continue // released meanwhile: try again
			}
			if rerr != nil {
				return rerr
			}
			if cur.Mine {
				result = cur // re-entrant
				_ = os.Chtimes(s.lockPath(id), s.now(), s.now())
				return nil
			}
			if !cur.Expired {
				return &LockedError{Lock: cur}
			}
			// Orphaned lock: take it over under the exclusive break token.
			broke, err := s.breakIfExpired(id)
			if err != nil {
				return err
			}
			if !broke {
				if again, rerr := s.readLock(id); rerr == nil {
					return &LockedError{Lock: again}
				}
			}
		}
		return fmt.Errorf("a zár megszerzése nem sikerült (versenyhelyzet), próbálja újra")
	})
	if err != nil {
		var le *LockedError
		if !errors.As(err, &le) {
			s.markOfflineIfNetwork(err)
		}
		return Lock{}, err
	}
	s.mu.Lock()
	s.myLocks[id] = true
	s.locks[id] = result
	s.mu.Unlock()
	return result, nil
}

// breakTokenTTL: a break token older than this belongs to a crashed breaker.
const breakTokenTTL = time.Minute

// withBreakToken runs fn while holding locks\<id>.lock.break, created
// exclusively. Without the token two instances could both decide (from
// stale reads) to break the lock, and the second would destroy the first
// one's brand-new lock. ok=false: another instance is breaking right now.
func (s *Store) withBreakToken(id string, fn func() error) (ok bool, err error) {
	tok := s.lockPath(id) + ".break"
	for i := 0; i < 2; i++ {
		f, err := os.OpenFile(tok, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			_ = os.Chtimes(tok, s.now(), s.now())
			defer os.Remove(tok)
			return true, fn()
		}
		if !errors.Is(err, fs.ErrExist) {
			return false, err
		}
		info, serr := os.Stat(tok)
		if serr == nil && s.now().Sub(info.ModTime()) > breakTokenTTL {
			os.Remove(tok) // stale token of a crashed breaker
			continue
		}
		return false, nil
	}
	return false, nil
}

// breakIfExpired re-reads the lock under the break token and moves it away
// only if it is still expired. Returns true if the lock is now free.
func (s *Store) breakIfExpired(id string) (bool, error) {
	broke := false
	ok, err := s.withBreakToken(id, func() error {
		cur, err := s.readLock(id)
		if errors.Is(err, fs.ErrNotExist) {
			broke = true
			return nil
		}
		if err != nil {
			return err
		}
		if !cur.Expired || cur.Mine {
			return nil
		}
		if err := s.breakLockFile(id, cur, "lejárt (nincs életjel "+s.opt.LockTTL.String()+" óta)"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		broke = true
		return nil
	})
	return ok && broke, err
}

// breakLockFile renames the lock away. Callers hold the break token.
func (s *Store) breakLockFile(id string, cur Lock, why string) error {
	p := s.lockPath(id)
	broken := p + ".broken-" + s.now().Format("20060102-150405") + "-" + s.opt.Identity.Instance
	if err := os.Rename(p, broken); err != nil {
		return err
	}
	os.Remove(broken)
	log.Printf("zár feloldva (%s): %s – korábbi tulajdonos: %s", id, why, cur.Holder())
	return nil
}

// Heartbeat refreshes the modification time of every lock we hold and
// returns the ids of locks we have lost (broken by someone else).
func (s *Store) Heartbeat() (lost []string) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.myLocks))
	for id := range s.myLocks {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		var still bool
		err := withTimeout(s.opt.Timeout, func() error {
			l, err := s.readLock(id)
			if err != nil {
				return err
			}
			if !l.Mine {
				return nil
			}
			still = true
			return os.Chtimes(s.lockPath(id), s.now(), s.now())
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			// Network problem: keep the lock locally, it is re-checked when back online.
			s.markOfflineIfNetwork(err)
			continue
		}
		if !still {
			s.mu.Lock()
			delete(s.myLocks, id)
			s.mu.Unlock()
			lost = append(lost, id)
		}
	}
	return lost
}

// Release removes our lock (no-op if it is not ours any more).
func (s *Store) Release(id string) error {
	s.mu.Lock()
	delete(s.myLocks, id)
	s.mu.Unlock()
	err := withTimeout(s.opt.Timeout, func() error {
		l, err := s.readLock(id)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !l.Mine {
			return nil
		}
		return retry(func() error { return os.Remove(s.lockPath(id)) })
	})
	s.mu.Lock()
	if l, ok := s.locks[id]; ok && l.Mine {
		delete(s.locks, id)
	}
	s.mu.Unlock()
	return err
}

// ReleaseAll releases every lock of this instance (window closed, exit).
func (s *Store) ReleaseAll() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.myLocks))
	for id := range s.myLocks {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		if err := s.Release(id); err != nil {
			log.Printf("zár feloldása (%s): %v", id, err)
		}
	}
}

// HoldsLock reports whether this instance currently holds the lock (as far
// as it knows locally).
func (s *Store) HoldsLock(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.myLocks[id]
}

// ForceUnlock removes somebody else's lock after the user confirmed it.
// The action is recorded in the report's change log.
func (s *Store) ForceUnlock(id string) (Lock, error) {
	if err := s.writable(); err != nil {
		return Lock{}, err
	}
	var prev Lock
	err := withTimeout(s.opt.Timeout, func() error {
		ok, err := s.withBreakToken(id, func() error {
			cur, err := s.readLock(id)
			if err != nil {
				return err
			}
			prev = cur
			return s.breakLockFile(id, cur, "kézi feloldás: "+s.opt.Identity.Name())
		})
		if err == nil && !ok {
			return errors.New("valaki más éppen feloldja ezt a zárat – próbálja újra pár másodperc múlva")
		}
		return err
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Lock{}, nil // already free
		}
		return Lock{}, err
	}
	s.mu.Lock()
	delete(s.locks, id)
	s.mu.Unlock()
	if id != CalendarLockID {
		// Record it in the change log (takes and releases the lock briefly).
		if _, err := s.Acquire(id); err == nil {
			_ = s.appendChange(id, Change{Action: "unlocked", Note: "Zár kézi feloldása – korábban: " + prev.Holder()})
			_ = s.Release(id)
		}
	}
	return prev, nil
}

// markOfflineIfNetwork flips to offline on errors that look like the share
// being unreachable.
func (s *Store) markOfflineIfNetwork(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, errTimeout) || !(errors.Is(err, fs.ErrExist) || errors.Is(err, fs.ErrNotExist)) {
		serr := withTimeout(s.opt.Timeout, func() error { _, e := os.Stat(s.opt.Root); return e })
		if serr != nil || errors.Is(err, errTimeout) {
			s.mu.Lock()
			s.online = false
			s.lastErr = humanErr(err)
			s.mu.Unlock()
		}
	}
}

// LockTTL returns the orphan timeout.
func (s *Store) LockTTL() time.Duration { return s.opt.LockTTL }
