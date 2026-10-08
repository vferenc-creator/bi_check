// Package checker inspects watched paths on (slow, unreliable) network
// shares: every file system call runs with a timeout, and a per-server
// circuit breaker stops a dead server from tying up goroutines.
package checker

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"bimonitor/internal/pathpattern"
)

// Kind is the outcome of a check.
type Kind string

const (
	Found       Kind = "found"
	NotFound    Kind = "notFound"
	Unreachable Kind = "unreachable"
)

// Result of checking one path.
type Result struct {
	Kind          Kind       `json:"kind"`
	File          *FileInfo  `json:"file,omitempty"`
	Matches       int        `json:"matches"`
	Candidates    []FileInfo `json:"candidates,omitempty"` // newest matching files (max 5)
	Resolved      string     `json:"resolved"`             // path after token substitution
	Reason        string     `json:"reason,omitempty"`
	Server        string     `json:"server"`
	ServerProblem bool       `json:"serverProblem,omitempty"`
	CheckedAt     time.Time  `json:"checkedAt"`
	TookMs        int64      `json:"tookMs"`
}

// Options configure a Checker.
type Options struct {
	Timeout            time.Duration // per file system call (default 10s)
	MaxInflightPerHost int           // concurrent calls per server (default 4)
	BackoffMin         time.Duration // breaker open time after the 1st failure (default 30s)
	BackoffMax         time.Duration // cap (default 5m)
	ListCacheTTL       time.Duration // share directory listings between items (default 5s)
	Now                func() time.Time
}

// Checker performs checks; safe for concurrent use.
type Checker struct {
	fs  FS
	opt Options

	mu      sync.Mutex
	servers map[string]*serverState
	lists   map[string]listEntry
}

type serverState struct {
	inflight  int
	failures  int
	openUntil time.Time
	lastErr   string
}

type listEntry struct {
	at    time.Time
	files []FileInfo
	err   error
}

// New creates a checker over fs (nil = the real file system).
func New(fs FS, opt Options) *Checker {
	if fs == nil {
		fs = OSFS{}
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 10 * time.Second
	}
	if opt.MaxInflightPerHost <= 0 {
		opt.MaxInflightPerHost = 4
	}
	if opt.BackoffMin <= 0 {
		opt.BackoffMin = 30 * time.Second
	}
	if opt.BackoffMax <= 0 {
		opt.BackoffMax = 5 * time.Minute
	}
	if opt.ListCacheTTL == 0 {
		opt.ListCacheTTL = 5 * time.Second
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Checker{fs: fs, opt: opt, servers: map[string]*serverState{}, lists: map[string]listEntry{}}
}

// SetTimeout changes the per-call timeout.
func (c *Checker) SetTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	c.opt.Timeout = d
	c.mu.Unlock()
}

var (
	errTimeout = errors.New("timeout")
	errBusy    = errors.New("busy")
)

type breakerOpenError struct {
	until time.Time
	last  string
}

func (e breakerOpenError) Error() string { return "breaker open" }

// do runs op against server with timeout and circuit breaker.
func (c *Checker) do(server string, op func() error) error {
	c.mu.Lock()
	st := c.servers[server]
	if st == nil {
		st = &serverState{}
		c.servers[server] = st
	}
	now := c.opt.Now()
	if now.Before(st.openUntil) {
		e := breakerOpenError{until: st.openUntil, last: st.lastErr}
		c.mu.Unlock()
		return e
	}
	if st.inflight >= c.opt.MaxInflightPerHost {
		c.mu.Unlock()
		return errBusy
	}
	st.inflight++
	timeout := c.opt.Timeout
	c.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		err := op()
		c.mu.Lock()
		st.inflight--
		c.mu.Unlock()
		done <- err
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		c.serverFailed(server, fmt.Sprintf("A szerver %d másodpercen belül nem válaszolt.", int(timeout.Seconds())))
		return errTimeout
	}
}

func (c *Checker) serverFailed(server, msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.servers[server]
	st.failures++
	back := c.opt.BackoffMin << (st.failures - 1)
	if back > c.opt.BackoffMax || back <= 0 {
		back = c.opt.BackoffMax
	}
	st.openUntil = c.opt.Now().Add(back)
	st.lastErr = msg
}

func (c *Checker) serverOK(server string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if st := c.servers[server]; st != nil {
		st.failures = 0
		st.openUntil = time.Time{}
		st.lastErr = ""
	}
}

// ServerStatus describes a server's breaker (for the UI).
type ServerStatus struct {
	Server    string    `json:"server"`
	Down      bool      `json:"down"`
	RetryAt   time.Time `json:"retryAt"`
	LastError string    `json:"lastError"`
	Inflight  int       `json:"inflight"`
}

// Servers returns breaker states.
func (c *Checker) Servers() []ServerStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.opt.Now()
	var out []ServerStatus
	for k, st := range c.servers {
		out = append(out, ServerStatus{Server: k, Down: now.Before(st.openUntil), RetryAt: st.openUntil, LastError: st.lastErr, Inflight: st.inflight})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Server < out[j].Server })
	return out
}

// ResetServers closes all breakers (used by "check now").
func (c *Checker) ResetServers() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, st := range c.servers {
		st.openUntil = time.Time{}
	}
	c.lists = map[string]listEntry{}
}

// Check inspects path, resolving date tokens with ref. anyDate makes file
// name tokens match any digits.
func (c *Checker) Check(path string, ref time.Time, anyDate bool) Result {
	start := c.opt.Now()
	res := c.check(path, ref, anyDate)
	res.CheckedAt = start
	res.TookMs = c.opt.Now().Sub(start).Milliseconds()
	return res
}

func (c *Checker) check(path string, ref time.Time, anyDate bool) Result {
	pat, err := pathpattern.Parse(path)
	if err != nil {
		return Result{Kind: Unreachable, Reason: "Hibás útvonal: " + err.Error(), Resolved: path}
	}
	r := pat.Resolve(ref, anyDate)
	server := pathpattern.ServerKey(r.Display)
	res := Result{Resolved: r.Display, Server: server}

	if r.Exact {
		full := r.Join(r.File)
		var fi FileInfo
		err := c.do(server, func() error {
			var e error
			fi, e = c.fs.Stat(full)
			return e
		})
		if err == nil {
			c.serverOK(server)
			fi.Path = full
			res.Kind, res.File, res.Matches = Found, &fi, 1
			res.Candidates = []FileInfo{fi}
			return res
		}
		return c.failure(res, server, r.Dir, err, false)
	}

	files, err := c.list(server, r)
	if err != nil {
		return c.failure(res, server, r.Dir, err, true)
	}
	c.serverOK(server)
	var matched []FileInfo
	for _, f := range files {
		if r.Match(f.Name) {
			matched = append(matched, f)
		}
	}
	res.Matches = len(matched)
	if len(matched) == 0 {
		res.Kind = NotFound
		res.Reason = "Nincs a mintára illeszkedő fájl a mappában."
		return res
	}
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].ModTime.Equal(matched[j].ModTime) {
			return matched[i].ModTime.After(matched[j].ModTime)
		}
		return matched[i].Name > matched[j].Name
	})
	newest := matched[0]
	res.Kind, res.File = Found, &newest
	if len(matched) > 5 {
		matched = matched[:5]
	}
	res.Candidates = matched
	return res
}

func (c *Checker) list(server string, r *pathpattern.Resolved) ([]FileInfo, error) {
	key := strings.ToLower(r.Dir)
	c.mu.Lock()
	if e, ok := c.lists[key]; ok && c.opt.Now().Sub(e.at) < c.opt.ListCacheTTL {
		c.mu.Unlock()
		return e.files, e.err
	}
	c.mu.Unlock()
	var files []FileInfo
	err := c.do(server, func() error {
		var e error
		files, e = c.fs.ListFiles(r.Dir, r.Join)
		return e
	})
	if err != errTimeout && err != errBusy {
		if _, open := err.(breakerOpenError); !open {
			c.mu.Lock()
			c.lists[key] = listEntry{at: c.opt.Now(), files: files, err: err}
			c.mu.Unlock()
		}
	}
	return files, err
}

// failure turns an error into a NotFound or Unreachable result.
func (c *Checker) failure(res Result, server, dir string, err error, listing bool) Result {
	switch e := err.(type) {
	case breakerOpenError:
		res.Kind, res.ServerProblem = Unreachable, true
		res.Reason = fmt.Sprintf("A(z) %s szerver nem elérhető (%s) Újrapróbálás: %s.", server, strings.TrimSuffix(e.last, "."), e.until.Local().Format("15:04:05"))
		return res
	}
	if err == errTimeout {
		res.Kind, res.ServerProblem = Unreachable, true
		res.Reason = fmt.Sprintf("Időtúllépés: a(z) %s szerver nem válaszolt időben.", server)
		return res
	}
	if err == errBusy {
		res.Kind, res.ServerProblem = Unreachable, true
		res.Reason = fmt.Sprintf("A(z) %s szerver felé még folyamatban vannak korábbi, lassú kérések.", server)
		return res
	}
	cls, msg := classify(err)
	if listing && cls == errFileNotFound {
		cls = errPathNotFound
	}
	if cls == errFileNotFound {
		// On some systems "file not found" also covers a missing folder.
		if derr := c.do(server, func() error { _, e := c.fs.Stat(dir); return e }); derr != nil {
			if dcls, _ := classify(derr); dcls == errFileNotFound || dcls == errPathNotFound {
				cls = errPathNotFound
			}
		}
	}
	switch cls {
	case errFileNotFound:
		c.serverOK(server)
		res.Kind, res.Reason = NotFound, msg
	case errPathNotFound:
		// Is the share itself reachable? Then the folder really is missing.
		root, _ := pathpattern.ShareRoot(dir)
		if root == "" {
			res.Kind, res.Reason = NotFound, "A mappa nem létezik: "+dir
			return res
		}
		rerr := c.do(server, func() error { _, e := c.fs.Stat(root); return e })
		if rerr == nil {
			c.serverOK(server)
			res.Kind, res.Reason = NotFound, "A mappa nem létezik: "+dir
			return res
		}
		res.Kind, res.ServerProblem = Unreachable, true
		_, rmsg := classify(rerr)
		if rerr == errTimeout {
			rmsg = "időtúllépés"
		}
		res.Reason = "A megosztás nem érhető el (" + root + "): " + rmsg
	case errAccess:
		res.Kind, res.Reason = Unreachable, msg
	case errServer:
		c.serverFailed(server, msg)
		res.Kind, res.ServerProblem, res.Reason = Unreachable, true, msg
	default:
		res.Kind, res.Reason = Unreachable, "Fájlrendszer hiba: "+msg
	}
	return res
}
