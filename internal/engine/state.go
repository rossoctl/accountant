package engine

import (
	"reflect"
	"slices"
	"strings"
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
	// created is the At of the request that made this Tracker. The trim ages a thread by it while
	// the thread has no previous request (state.PrevPrint is nil).
	created time.Time
}

// lastSeen is the time trim ages the Tracker by: its previous request, else its creation.
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

// maxThreads bounds the thread state the library keeps (design §6.1, "Memory"). Each Tracker holds a
// thread's previous fingerprint and its active changes. Over the cap, the threads seen longest ago
// are dropped down to maxThreads - maxThreads/10, so the sort runs once for each tenth of the cap.
//
// There is no idle timeout on purpose. A keep-alive ping keeps a cache entry alive after the last
// real request, and the library learns of a ping only at the thread's next EndRequest
// (EntryFacts.LastPing). A sweep by idle time would drop a thread whose pings kept it hot, and its
// next request would lose the rescue.
const maxThreads = 10000

// Trackers holds one Tracker for each thread. It is safe for concurrent use.
// It bounds its own memory: when more than maxThreads are held, BeginRequest drops the threads seen
// longest ago. A dropped thread loses its active changes (design §11, Q13).
type Trackers struct {
	mu sync.Mutex
	m  map[string]*Tracker
	// trimmed counts the threads trim has dropped (Trimmed).
	trimmed uint64
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

// trim drops the threads seen longest ago when more than maxThreads are held. A thread's age is its
// previous request, else the At of the request that made its Tracker, so a late request (an At far
// before the others) makes a Tracker that is among the first to go. Equal ages drop in key order.
// A Request in flight is not affected: it holds its own copy of the state (EndRequest), and Compute
// looks the Tracker up again, so after a drop it makes a new, empty one, calculates against that
// (Result.Recomputed, phase unknown) and stores the next state there. Callers hold ts.mu.
func (ts *Trackers) trim() {
	if len(ts.m) <= maxThreads {
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
	slices.SortFunc(all, func(a, b aged) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		return strings.Compare(a.key, b.key)
	})
	for _, a := range all[:len(ts.m)-(maxThreads-maxThreads/10)] {
		delete(ts.m, a.key)
		ts.trimmed++
	}
}

// Request is the record of one request. It is private to that request: two requests of one thread
// in flight each have their own (R53).
type Request struct {
	trackers *Trackers

	meta              RequestMeta
	client            Body
	clientBreakpoints []Breakpoint
	ops               []Operation // as reported, in pipeline order

	// Set by EndRequest.
	thread      Thread
	forwarded   Body
	breakpoints []Breakpoint
	facts       EntryFacts
	start       ThreadState // the thread's state when EndRequest ran; start.Version is the lock
	ended       bool
}

// BeginRequest opens a record that belongs to no thread yet: the thread is known only at the end of
// the pass (§6.1). Call it before the plugins, with the client body before any plugin.
func (ts *Trackers) BeginRequest(meta RequestMeta, client Body, clientBreakpoints []Breakpoint) *Request {
	ts.mu.Lock()
	ts.trim()
	ts.mu.Unlock()
	return &Request{trackers: ts, meta: meta, client: client, clientBreakpoints: clientBreakpoints}
}

// Add records one reported operation. The framework sets Operator and DryRun first.
func (r *Request) Add(op Operation) { r.ops = append(r.ops, op) }

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
	r.thread = thread
	r.forwarded = forwarded
	r.breakpoints = breakpoints
	r.facts = facts
	r.ended = true
	t := r.trackers.tracker(ThreadKey(r.meta.Session, thread), r.meta.At)
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

// LowerTTL tells the library that the response billed a shorter cache lifetime than the request asked
// for, when the host learns it after EndRequest: a gateway that bills a 1h ask as 5m. Call it before
// Compute. It lowers EntryFacts.TTL to ttl and, for a ttl below one hour, sets every 1h breakpoint of
// the forwarded body to 5m, because the library judges each part of the entry by the TTL of the
// breakpoint that wrote it (accountant#56). The host's own breakpoint slice is not changed. It does
// nothing before EndRequest, for a ttl that is not shorter than the TTL in EntryFacts, or for a ttl
// of 0 or less: it only lowers.
func (r *Request) LowerTTL(ttl time.Duration) {
	if !r.ended || ttl <= 0 || ttl >= r.facts.TTL {
		return
	}
	r.facts.TTL = ttl
	if ttl >= time.Hour {
		return
	}
	bps := append([]Breakpoint(nil), r.breakpoints...)
	for i := range bps {
		if bps[i].TTL == "1h" {
			bps[i].TTL = "5m"
		}
	}
	r.breakpoints = bps
}

// withReported returns s.Active with this request's reported rewrite and setting operations in it,
// in client space (§4.3), and whether that differs from s.Active. A reported operation replaces the
// active one with its ChangeID. The other active operations stay: whether they ended is for Compute.
func (r *Request) withReported(s ThreadState) ([]Operation, bool) {
	reported := map[string]bool{}
	l := r.limits(s)
	for _, op := range r.ops {
		if isChange(op, l) {
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

// seededFromParent returns s, or, for a thread that has no previous request and names a Parent, a copy
// that starts from the parent's fingerprint and cache state (design §5.2), and true. A fork reads the
// entry its parent wrote, so it is not a first turn. It reads the parent's Tracker as it is at this
// call (spec R26, R54). It seeds only when all of these hold, so a seed can only give a hot phase:
//   - the fork's At is not before the parent's last request, and the gap is within the TTL;
//   - the fork's forwarded body keeps the parent's whole cached extent (equal hashes up to it);
//   - the fork uses the parent's model.
//
// Otherwise the thread stays unknown, and a read in the usage still makes it hot. The active operations
// are not inherited.
func (r *Request) seededFromParent(s ThreadState) (ThreadState, bool) {
	if s.PrevPrint != nil || r.thread.Parent == "" || r.thread.Parent == r.thread.ID || r.facts.TTL <= 0 {
		return s, false
	}
	r.trackers.mu.Lock()
	pt := r.trackers.m[ThreadKey(r.meta.Session, Thread{ID: r.thread.Parent})]
	r.trackers.mu.Unlock()
	if pt == nil {
		return s, false
	}
	ps := pt.snapshot()
	p := ps.PrevPrint
	if p == nil || ps.Cache.Extent == nil || p.modelID() != r.meta.modelID() {
		return s, false
	}
	ttl := ps.Cache.EntryTTL // the parent's entry lives as long as the parent wrote it for
	if ttl <= 0 {
		ttl = r.facts.TTL
	}
	if ps.Cache.Extent != nil { // the longest part decides, as in phaseAndBoundary: a 1h head outlives a 5m tail
		for _, sg := range entrySegs(ps, ttl) {
			ttl = max(ttl, sg.ttl)
		}
	}
	if gap := r.meta.At.Sub(p.At); gap < 0 || gap > ttl {
		return s, false
	}
	i, ok := p.Forwarded.flat(*ps.Cache.Extent)
	if !ok || firstDiff(p.Forwarded, r.forwarded) <= i {
		return s, false
	}
	s.PrevPrint, s.Cache = p, ps.Cache
	return s, true
}
