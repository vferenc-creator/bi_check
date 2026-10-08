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

	retry bool // transient failure worth another attempt
}

// Options configure a Checker.
type Options struct {
	Timeout            time.Duration // per file system call (default 10s)
	MaxInflightPerHost int           // concurrent calls per server (default 3); further calls wait in line
	QueueWait          time.Duration // max wait for a free slot (default 3×Timeout)
	FailThreshold      int           // consecutive failures that open the breaker (default 3)
	BackoffMin         time.Duration // breaker open time when it first opens (default 30s)
	BackoffMax         time.Duration // cap (default 5m)
	ListCacheTTL       time.Duration // share directory listings between items (default 5s)
	// RetryDelays are the pauses before re-trying a check that failed with
	// a transient network error (nil = 1s, 3s; empty = no retry).
	RetryDelays []time.Duration
	Now         func() time.Time
	Sleep       func(time.Duration)
}

// Checker performs checks; safe for concurrent use.
type Checker struct {
	fs  FS
	opt Options

	mu      sync.Mutex
	servers map[string]*serverState
	lists   map[string]listEntry
	listing map[string]*listCall // listings in progress (one request per folder)
}

type serverState struct {
	slots     chan struct{} // one token per running call (also abandoned, timed-out ones)
	failures  int           // consecutive failures
	openUntil time.Time
	lastErr   string
	opened    chan struct{} // closed when the breaker opens (wakes queued calls)
}

type listEntry struct {
	at    time.Time
	files []FileInfo
	err   error
}

type listCall struct {
	done  chan struct{}
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
		opt.MaxInflightPerHost = 3
	}
	if opt.FailThreshold <= 0 {
		opt.FailThreshold = 3
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
	if opt.RetryDelays == nil {
		opt.RetryDelays = []time.Duration{time.Second, 3 * time.Second}
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Sleep == nil {
		opt.Sleep = time.Sleep
	}
	return &Checker{fs: fs, opt: opt, servers: map[string]*serverState{}, lists: map[string]listEntry{}, listing: map[string]*listCall{}}
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

// state returns the server's state; c.mu must be held.
func (c *Checker) state(server string) *serverState {
	st := c.servers[server]
	if st == nil {
		st = &serverState{slots: make(chan struct{}, c.opt.MaxInflightPerHost), opened: make(chan struct{})}
		c.servers[server] = st
	}
	return st
}

// do runs op against server with timeout and circuit breaker. At most
// MaxInflightPerHost calls run against one server at a time; the others
// wait in line (shares dislike bursts of parallel requests).
func (c *Checker) do(server string, op func() error) error {
	c.mu.Lock()
	st := c.state(server)
	if c.opt.Now().Before(st.openUntil) {
		e := breakerOpenError{until: st.openUntil, last: st.lastErr}
		c.mu.Unlock()
		return e
	}
	slots, opened := st.slots, st.opened
	timeout := c.opt.Timeout
	queueWait := c.opt.QueueWait
	if queueWait <= 0 {
		queueWait = 3 * timeout
	}
	c.mu.Unlock()

	select {
	case slots <- struct{}{}:
	default:
		wait := time.NewTimer(queueWait)
		select {
		case slots <- struct{}{}:
			wait.Stop()
		case <-opened:
			wait.Stop()
			c.mu.Lock()
			e := breakerOpenError{until: st.openUntil, last: st.lastErr}
			c.mu.Unlock()
			return e
		case <-wait.C:
			return errBusy
		}
	}

	done := make(chan error, 1)
	go func() {
		err := op()
		<-slots // a timed-out call keeps its slot until it really returns
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

// serverFailed counts a failure; the breaker opens only after
// FailThreshold failures in a row (a success in between resets the count),
// so one slow answer does not make every item on the server unreachable.
func (c *Checker) serverFailed(server, msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.state(server)
	st.failures++
	st.lastErr = msg
	n := st.failures - c.opt.FailThreshold
	if n < 0 {
		return
	}
	back := c.opt.BackoffMin << n
	if back > c.opt.BackoffMax || back <= 0 || n > 30 {
		back = c.opt.BackoffMax
	}
	st.openUntil = c.opt.Now().Add(back)
	close(st.opened)
	st.opened = make(chan struct{})
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
		out = append(out, ServerStatus{Server: k, Down: now.Before(st.openUntil), RetryAt: st.openUntil, LastError: st.lastErr, Inflight: len(st.slots)})
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
		st.failures = 0
	}
	c.lists = map[string]listEntry{}
}

// Check inspects path, resolving date tokens with ref. anyDate makes file
// name tokens match any digits.
func (c *Checker) Check(path string, ref time.Time, anyDate bool) Result {
	start := c.opt.Now()
	res := c.check(path, ref, anyDate)
	for _, d := range c.opt.RetryDelays {
		if !res.retry {
			break
		}
		// Transient network trouble (timeout, dropped SMB session, busy
		// server): look again shortly instead of reporting it right away.
		c.opt.Sleep(d)
		c.dropListing(res.Resolved)
		res = c.check(path, ref, anyDate)
	}
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
	if call := c.listing[key]; call != nil {
		// Another item lists the same folder right now: share its answer.
		c.mu.Unlock()
		<-call.done
		return call.files, call.err
	}
	call := &listCall{done: make(chan struct{})}
	c.listing[key] = call
	c.mu.Unlock()

	var got []FileInfo // only read after op returned (a timed-out op may still write it)
	call.err = c.do(server, func() error {
		var e error
		got, e = c.fs.ListFiles(r.Dir, r.Join)
		return e
	})
	if call.err == nil {
		call.files = got
	}
	c.mu.Lock()
	delete(c.listing, key)
	if call.err != errTimeout && call.err != errBusy {
		if _, open := call.err.(breakerOpenError); !open {
			c.lists[key] = listEntry{at: c.opt.Now(), files: call.files, err: call.err}
		}
	}
	c.mu.Unlock()
	close(call.done)
	return call.files, call.err
}

// dropListing forgets a cached listing of the folder of resolved (before a
// retry, so it really asks the server again).
func (c *Checker) dropListing(resolved string) {
	i := strings.LastIndexAny(resolved, `\/`)
	if i <= 0 {
		return
	}
	c.mu.Lock()
	delete(c.lists, strings.ToLower(resolved[:i]))
	c.mu.Unlock()
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
		res.Kind, res.ServerProblem, res.retry = Unreachable, true, true
		res.Reason = fmt.Sprintf("Időtúllépés: a(z) %s szerver nem válaszolt időben.", server)
		return res
	}
	if err == errBusy {
		res.Kind, res.ServerProblem, res.retry = Unreachable, true, true
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
		res.Kind, res.ServerProblem, res.retry = Unreachable, true, true
		_, rmsg := classify(rerr)
		if rerr == errTimeout {
			rmsg = "időtúllépés"
		}
		res.Reason = "A megosztás nem érhető el (" + root + "): " + rmsg
	case errAccess:
		res.Kind, res.Reason = Unreachable, msg
	case errServer:
		c.serverFailed(server, msg)
		res.Kind, res.ServerProblem, res.Reason, res.retry = Unreachable, true, msg, true
	default:
		res.Kind, res.Reason, res.retry = Unreachable, "Fájlrendszer hiba: "+msg, true
	}
	return res
}
