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

func trimOnce(ts *Trackers) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.trim()
}

func TestTrimUnderTheCapDropsNothing(t *testing.T) {
	ts := NewTrackers()
	for i := range maxThreads {
		seenAt(ts, "s", fmt.Sprint("t", i), t0.Add(-time.Duration(i)*24*time.Hour)) // days old: age alone drops nothing
	}
	trimOnce(ts)
	if len(ts.m) != maxThreads {
		t.Fatalf("held %d threads, want %d", len(ts.m), maxThreads)
	}
}

func TestTrimOverTheCapKeepsTheMostRecent(t *testing.T) {
	ts := NewTrackers()
	for i := range maxThreads + 5 {
		seenAt(ts, "s", fmt.Sprint("t", i), t0.Add(time.Duration(i)*time.Millisecond))
	}
	trimOnce(ts)
	if want := maxThreads - maxThreads/10; len(ts.m) != want {
		t.Fatalf("held %d threads, want %d", len(ts.m), want)
	}
	if !has(ts, "s", fmt.Sprint("t", maxThreads+4)) || has(ts, "s", "t0") {
		t.Fatal("the cap must keep the newest thread and drop the oldest")
	}
}

// Threads of equal age drop in key order, so the same input always drops the same threads (R26).
func TestTrimBreaksTiesByKey(t *testing.T) {
	ts := NewTrackers()
	for i := range maxThreads + 1 {
		seenAt(ts, "s", fmt.Sprintf("t%05d", i), t0) // all the same age
	}
	trimOnce(ts)
	drop := len(ts.m) // after: 9,000 left
	if drop != maxThreads-maxThreads/10 {
		t.Fatalf("held %d threads", drop)
	}
	if has(ts, "s", "t00000") || !has(ts, "s", fmt.Sprintf("t%05d", maxThreads)) {
		t.Fatal("equal ages must drop the lowest keys first")
	}
}

// BeginRequest runs the cap on its own, with no call from the host.
func TestBeginRequestTrimsOnItsOwn(t *testing.T) {
	ts := NewTrackers()
	for i := range maxThreads + 1 {
		seenAt(ts, "s", fmt.Sprint("t", i), t0.Add(time.Duration(i)*time.Millisecond))
	}
	ts.BeginRequest(RequestMeta{Session: "other", At: t0}, Body{}, nil)
	if want := maxThreads - maxThreads/10; len(ts.m) != want {
		t.Fatalf("held %d threads, want %d", len(ts.m), want)
	}
}

// There is no idle timeout: a thread kept hot only by keep-alive pings must still be there for its
// next request, however many hours have passed and however many other threads came in between
// (design §6.1, Memory). With a 1h lifetime, a ping at 85 minutes kept the entry of the request at
// minute 0 alive, so the request at minute 90 is hot and earns the rescue.
func TestAThreadKeptHotByPingsIsNotDroppedForBeingIdle(t *testing.T) {
	ts := NewTrackers()
	m := msgs(6, 1000)
	sysB := []blk{b("sys", 4000)}
	f := EntryFacts{TTL: time.Hour, Lifetime: LifetimeExact}
	req{at: t0, client: mk(nil, sysB, m), clientBPs: bps5(sys(0), msg(5)), facts: f, usage: Usage{CacheWrite: 10000}}.run(ts)
	for i := range 2000 { // other threads arrive over the next 3 hours
		at := t0.Add(time.Duration(i) * 5 * time.Second)
		ts.BeginRequest(RequestMeta{Session: fmt.Sprint("other", i), At: at}, Body{}, nil)
	}
	f.LastPing, f.PingRead, f.PingWindow = t0.Add(85*time.Minute), ptr(msg(5)), "ka:main:w1"
	res := req{at: t0.Add(90 * time.Minute), client: mk(nil, sysB, with(m, b("m6", 500))), clientBPs: bps5(sys(0), msg(6)),
		facts: f, usage: Usage{CacheRead: 10000, CacheWrite: 500}}.run(ts)
	if res.Phase != PhaseHot {
		t.Fatalf("phase %q, want hot: the thread's state was lost", res.Phase)
	}
	if e := effect(t, res, EffectRescue); e.ChangeID != "ka:main:w1" {
		t.Fatalf("rescue %+v", e)
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
	ts.mu.Lock()
	delete(ts.m, ThreadKey("s", Thread{ID: "main"})) // what trim does to it
	ts.mu.Unlock()
	res := r2.Compute(Usage{Input: 300})
	if !res.Recomputed || res.Phase != PhaseUnknown {
		t.Fatalf("Recomputed %v, phase %v, want a recompute against an empty state", res.Recomputed, res.Phase)
	}
	if len(ts.m) != 1 || ts.m[ThreadKey("s", Thread{ID: "main"})].snapshot().PrevPrint == nil {
		t.Fatal("Compute must store the next state in a new Tracker")
	}
}

func TestTrimIsSafeAgainstConcurrentRequests(t *testing.T) {
	ts := NewTrackers()
	body := mk(nil, nil, msgs(3, 100))
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 300 {
				at := t0.Add(time.Duration(i) * time.Hour)
				th := Thread{ID: fmt.Sprint("t", g, "-", i%5)}
				r := ts.BeginRequest(RequestMeta{Session: "s", At: at, Billing: Billing{Mode: BillingAPI}}, body, nil)
				r.EndRequest(th, body, nil, EntryFacts{TTL: 5 * time.Minute})
				r.Compute(Usage{Input: 300})
				if i%17 == 0 {
					trimOnce(ts)
				}
			}
		}()
	}
	wg.Wait()
}
