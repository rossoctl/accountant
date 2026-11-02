package engine

import (
	"reflect"
	"slices"
	"sync"
	"time"
)

// ThreadState is kept between requests and replaced after each one (§6.1).
type ThreadState struct {
	Active    []Operation  // rewrite and setting operations still applied (§4.6)
	PrevPrint *Fingerprint // this thread's previous request; nil = none (§5.2)
	Cache     CacheState   // what the previous request left cached; zero value = unknown
	Version   uint64       // +1 at each replace; the optimistic lock (R53)
}

// Tracker keeps the state of one thread between requests.
type Tracker struct {
	mu    sync.Mutex
	state ThreadState
	// created is the At of the request that made this Tracker. The sweep ages a thread by it while
	// the thread has no previous request (state.PrevPrint is nil).
	created time.Time
}

// lastSeen is the time the sweep ages the Tracker by: its previous request, else its creation.
func (t *Tracker) lastSeen() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state.PrevPrint != nil {
		return t.state.PrevPrint.At
	}
	return t.created
}

// snapshot returns a copy of the thread's state. Callers must not share the slices.
func (t *Tracker) snapshot() ThreadState {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.state
	s.Active = append([]Operation(nil), t.state.Active...)
	return s
}

// Bounds on the thread state the library keeps (design §6.1, "Memory"). Each Tracker holds a
// thread's previous fingerprint and its active changes.
const (
	// sweepEvery is how many calls of BeginRequest pass between two idle sweeps.
	sweepEvery = 1000
	// idleAfter is how long a thread must be unseen to be dropped. It is twice the longest cache TTL
	// (1h). Past the TTL the entry has expired, so the library can only find the phase unknown, and
	// the dropped state could not have produced a figure.
	idleAfter = 2 * time.Hour
	// maxThreads is the hard cap. Over it, the threads seen longest ago are dropped down to
	// maxThreads - maxThreads/10, so the sort runs once for each tenth of the cap.
	maxThreads = 10000
)

// Trackers holds one Tracker for each thread. It is safe for concurrent use.
// It bounds its own memory: BeginRequest drops threads unseen for idleAfter, and the oldest ones
// when more than maxThreads are held. A dropped thread loses its active changes (design §11, Q13).
type Trackers struct {
	mu    sync.Mutex
	m     map[string]*Tracker
	calls uint64 // BeginRequest calls, for the sweep
}

// NewTrackers returns an empty set: every thread starts with no history.
func NewTrackers() *Trackers { return &Trackers{m: map[string]*Tracker{}} }

// ThreadKey is the key of a thread's Tracker: the session and the thread ID.
func ThreadKey(session string, t Thread) string { return session + "\x00" + t.ID }

// tracker returns the thread's Tracker, and makes one when there is none. at is the time of the
// request, for a new Tracker's age.
func (ts *Trackers) tracker(key string, at time.Time) *Tracker {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.trackerLocked(key, at)
}

func (ts *Trackers) trackerLocked(key string, at time.Time) *Tracker {
	t, ok := ts.m[key]
	if !ok {
		t = &Tracker{created: at}
		ts.m[key] = t
	}
	return t
}

// sweep drops the threads unseen since before, then, if more than maxThreads remain, the ones seen
// longest ago. A Request in flight is not affected: it holds its own copy of the state
// (EndRequest), and Compute looks the Tracker up again, so after a drop it makes a new, empty one,
// calculates against that (Result.Recomputed, phase unknown) and stores the next state there.
// Callers hold ts.mu.
func (ts *Trackers) sweep(now time.Time, idle bool) {
	if idle {
		before := now.Add(-idleAfter)
		for k, t := range ts.m {
			if t.lastSeen().Before(before) {
				delete(ts.m, k)
			}
		}
	}
	keep := maxThreads - maxThreads/10
	over := len(ts.m) - keep
	if len(ts.m) <= maxThreads || over <= 0 {
		return
	}
	type aged struct {
		key string
		at  time.Time
	}
	all := make([]aged, 0, len(ts.m))
	for k, t := range ts.m {
		all = append(all, aged{k, t.lastSeen()})
	}
	slices.SortFunc(all, func(a, b aged) int { return a.at.Compare(b.at) })
	for _, a := range all[:over] {
		delete(ts.m, a.key)
	}
}

// Request is the record of one request. It is private to that request: two requests of one thread
// in flight each have their own (R53).
type Request struct {
	trackers *Trackers

	Meta              RequestMeta
	Client            Body
	ClientBreakpoints []Breakpoint
	Ops               []Operation // as reported, in pipeline order

	// Set by EndRequest.
	Thread      Thread
	Forwarded   Body
	Breakpoints []Breakpoint
	Facts       EntryFacts
	start       ThreadState // the thread's state when EndRequest ran; start.Version is the lock
	ended       bool
}

// BeginRequest opens a record that belongs to no thread yet: the thread is known only at the end of
// the pass (§6.1). Call it before the plugins, with the client body before any plugin.
func (ts *Trackers) BeginRequest(meta RequestMeta, client Body, clientBreakpoints []Breakpoint) *Request {
	ts.mu.Lock()
	ts.calls++
	ts.sweep(meta.At, ts.calls%sweepEvery == 0)
	ts.mu.Unlock()
	return &Request{trackers: ts, Meta: meta, Client: client, ClientBreakpoints: clientBreakpoints}
}

// Add records one reported operation. The framework sets Operator and DryRun first.
func (r *Request) Add(op Operation) { r.Ops = append(r.Ops, op) }

// EndRequest names the thread, records the forwarded body, its breakpoints and the facts only the
// host knows, and takes a copy of the thread's ThreadState. Call it after the last plugin.
//
// It also stores the reported rewrite and setting operations in the ThreadState as active, with a
// Version bump (§4.6). A request can fail before the response, and then Compute never runs; the
// operator reports a change once, so the change would be lost. The fingerprint and the cache state
// change only in Compute, because they need the usage. Compute does not store the operations a
// second time: a reported operation replaces the active one with its ChangeID. When the stored list
// would not change (a retry that reports the same change), nothing is written and Version stays.
func (r *Request) EndRequest(thread Thread, forwarded Body, breakpoints []Breakpoint, facts EntryFacts) {
	r.Thread = thread
	r.Forwarded = forwarded
	r.Breakpoints = breakpoints
	r.Facts = facts
	r.ended = true
	t := r.trackers.tracker(ThreadKey(r.Meta.Session, thread), r.Meta.At)
	for {
		s := t.snapshot()
		active, changed := r.withReported(s)
		if !changed {
			r.start = s
			return
		}
		next := s
		next.Active = active
		if t.replace(s.Version, next) {
			r.start = next
			r.start.Version = s.Version + 1
			r.start.Active = append([]Operation(nil), active...) // not the slice the Tracker holds
			return
		}
	}
}

// withReported returns s.Active with this request's reported rewrite and setting operations in it,
// in client space (§4.3), and whether that differs from s.Active. A reported operation replaces the
// active one with its ChangeID. The other active operations stay: whether they ended is for Compute.
func (r *Request) withReported(s ThreadState) ([]Operation, bool) {
	reported := map[string]bool{}
	for _, op := range r.Ops {
		if isChange(op) {
			reported[op.ChangeID] = true
		}
	}
	if len(reported) == 0 {
		return nil, false
	}
	var out []Operation
	for _, op := range s.Active {
		if !reported[op.ChangeID] {
			out = append(out, op)
		}
	}
	for _, op := range r.newCalc(s, r.items(s), Usage{}).activeOps() {
		if reported[op.ChangeID] {
			out = append(out, op)
		}
	}
	return out, !reflect.DeepEqual(out, s.Active)
}

// replace stores next only if the state is still at version, and gives it version + 1 (R53).
func (t *Tracker) replace(version uint64, next ThreadState) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state.Version != version {
		return false
	}
	next.Version = version + 1
	t.state = next
	return true
}
