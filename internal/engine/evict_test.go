package engine

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// seenAt makes a thread whose previous request was at the given time.
func seenAt(ts *Trackers, session, thread string, at time.Time) {
	ts.tracker(ThreadKey(session, Thread{ID: thread}), at).state = ThreadState{PrevPrint: &Fingerprint{At: at}}
}

func has(ts *Trackers, session, thread string) bool {
	_, ok := ts.m[ThreadKey(session, Thread{ID: thread})]
	return ok
}

// sweepOnce runs the idle sweep as BeginRequest does on every sweepEvery-th call.
func sweepOnce(ts *Trackers, now time.Time) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.sweep(now, true)
}

func TestSweepDropsOnlyIdleThreads(t *testing.T) {
	ts := NewTrackers()
	seenAt(ts, "s", "old", t0.Add(-3*time.Hour))
	seenAt(ts, "s", "recent", t0.Add(-time.Minute))
	ts.tracker(ThreadKey("s", Thread{ID: "fresh"}), t0) // no previous request: aged by creation
	ts.tracker(ThreadKey("s", Thread{ID: "stale-new"}), t0.Add(-3*time.Hour))
	sweepOnce(ts, t0)
	if has(ts, "s", "old") || has(ts, "s", "stale-new") {
		t.Fatal("an idle thread was kept")
	}
	if !has(ts, "s", "recent") || !has(ts, "s", "fresh") {
		t.Fatal("a recent thread was dropped")
	}
}

func TestSweepCapKeepsTheMostRecent(t *testing.T) {
	ts := NewTrackers()
	for i := range maxThreads + 5 {
		seenAt(ts, "s", fmt.Sprint("t", i), t0.Add(time.Duration(i)*time.Millisecond))
	}
	sweepOnce(ts, t0.Add(time.Hour)) // inside idleAfter: only the cap acts
	if want := maxThreads - maxThreads/10; len(ts.m) != want {
		t.Fatalf("held %d threads, want %d", len(ts.m), want)
	}
	newest := fmt.Sprint("t", maxThreads+4)
	oldest := "t0"
	if !has(ts, "s", newest) || has(ts, "s", oldest) {
		t.Fatalf("the cap must keep the newest (%v) and drop the oldest (%v)", has(ts, "s", newest), has(ts, "s", oldest))
	}
}

func TestSweepUnderTheCapDropsNothingRecent(t *testing.T) {
	ts := NewTrackers()
	for i := range 20 {
		seenAt(ts, "s", fmt.Sprint("t", i), t0.Add(time.Duration(i)*time.Second))
	}
	sweepOnce(ts, t0.Add(time.Minute))
	if len(ts.m) != 20 {
		t.Fatalf("held %d threads, want 20", len(ts.m))
	}
}

// BeginRequest sweeps on its own every sweepEvery-th call, with the request's time as the clock.
func TestBeginRequestSweepsIdleThreadsOnItsOwn(t *testing.T) {
	ts := NewTrackers()
	seenAt(ts, "s", "old", t0.Add(-3*time.Hour))
	for i := 1; i < sweepEvery; i++ {
		ts.BeginRequest(RequestMeta{Session: "other", At: t0}, Body{}, nil)
	}
	if !has(ts, "s", "old") {
		t.Fatal("swept before the sweepEvery-th call")
	}
	ts.BeginRequest(RequestMeta{Session: "other", At: t0}, Body{}, nil)
	if has(ts, "s", "old") {
		t.Fatal("the sweepEvery-th call did not drop the idle thread")
	}
}

// A Request in flight holds its own copy of the state. After its thread is dropped, Compute makes a
// new Tracker, calculates against an empty state and stores the next state there.
func TestDroppedThreadStillComputes(t *testing.T) {
	ts := NewTrackers()
	body := mk(nil, nil, msgs(3, 100))
	meta := func(id string) RequestMeta {
		return RequestMeta{RequestID: id, Session: "s", At: t0, Billing: Billing{Mode: BillingAPI}}
	}
	r1 := ts.BeginRequest(meta("r1"), body, nil)
	r1.EndRequest(Thread{ID: "main"}, body, nil, EntryFacts{TTL: 5 * time.Minute})
	r1.Compute(Usage{Input: 300})
	r2 := ts.BeginRequest(meta("r2"), body, nil)
	r2.EndRequest(Thread{ID: "main"}, body, nil, EntryFacts{TTL: 5 * time.Minute})
	sweepOnce(ts, t0.Add(10*time.Hour))
	if len(ts.m) != 0 {
		t.Fatal("precondition: the thread must be dropped")
	}
	res := r2.Compute(Usage{Input: 300})
	if !res.Recomputed || res.Phase != PhaseUnknown {
		t.Fatalf("Recomputed %v, phase %v, want a recompute against an empty state", res.Recomputed, res.Phase)
	}
	if len(ts.m) != 1 || ts.m[ThreadKey("s", Thread{ID: "main"})].snapshot().PrevPrint == nil {
		t.Fatal("Compute must store the next state in a new Tracker")
	}
}

func TestSweepIsSafeAgainstConcurrentRequests(t *testing.T) {
	ts := NewTrackers()
	body := mk(nil, nil, msgs(3, 100))
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 300 {
				at := t0.Add(time.Duration(i) * time.Hour) // each call ages the earlier threads
				th := Thread{ID: fmt.Sprint("t", g, "-", i%5)}
				r := ts.BeginRequest(RequestMeta{Session: "s", At: at, Billing: Billing{Mode: BillingAPI}}, body, nil)
				r.EndRequest(th, body, nil, EntryFacts{TTL: 5 * time.Minute})
				r.Compute(Usage{Input: 300})
				if i%17 == 0 {
					sweepOnce(ts, at)
				}
			}
		}()
	}
	wg.Wait()
}
