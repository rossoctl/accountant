package engine

import (
	"testing"
	"time"
)

// The skew margin is bounded: 11 seconds past the TTL, the same read does not overrule the clock.
func TestAReadPastTheSkewMarginStaysCold(t *testing.T) {
	ts := NewTrackers()
	req{at: t0, client: mk(nil, nil, msgs(3, 1000)), clientBPs: bps5(msg(2)), facts: exact5m,
		usage: Usage{CacheWrite: 3000}}.run(ts)
	res := req{at: t0.Add(5*time.Minute + 11*time.Second), client: mk(nil, nil, msgs(4, 1000)), clientBPs: bps5(msg(3)),
		facts: exact5m, usage: Usage{CacheRead: 3000, CacheWrite: 1000}}.run(ts)
	if res.Phase != PhaseCold {
		t.Fatalf("phase %q, want cold", res.Phase)
	}
}

// An out-of-order request with no read has no evidence: unknown.
func TestAnOutOfOrderRequestWithNoReadIsUnknown(t *testing.T) {
	ts := NewTrackers()
	req{at: t0, client: mk(nil, nil, msgs(3, 1000)), clientBPs: bps5(msg(2)), facts: exact5m,
		usage: Usage{CacheWrite: 3000}}.run(ts)
	res := req{at: t0.Add(-time.Hour), client: mk(nil, nil, msgs(4, 1000)), clientBPs: bps5(msg(3)),
		facts: exact5m, usage: Usage{CacheWrite: 4000}}.run(ts)
	if res.Phase != PhaseUnknown {
		t.Fatalf("phase %q, want unknown", res.Phase)
	}
}

// A fork on another model than its parent is not seeded: the provider keeps a cache for each model.
func TestAForkOnAnotherModelIsNotSeeded(t *testing.T) {
	ts := NewTrackers()
	body := mk(nil, nil, msgs(6, 1000))
	r := ts.BeginRequest(RequestMeta{RequestID: "p", Session: "s1", At: t0, Model: "big"}, body, bps5(msg(5)))
	r.EndRequest(Thread{ID: "main"}, body, bps5(msg(5)), exact5m)
	r.Compute(Usage{CacheWrite: 6000})
	fb := mk(nil, nil, msgs(7, 1000))
	f := ts.BeginRequest(RequestMeta{RequestID: "f", Session: "s1", At: t0.Add(time.Second), Model: "small"}, fb, bps5(msg(6)))
	f.EndRequest(Thread{ID: "sub", Parent: "main"}, fb, bps5(msg(6)), exact5m)
	if res := f.Compute(Usage{CacheWrite: 7000}); res.Phase != PhaseUnknown {
		t.Fatalf("phase %q, want unknown", res.Phase)
	}
}

// A fork whose At is before the parent's last request is not seeded, and its own fingerprint is stored
// (the out-of-order guard compares only with the thread's own previous request).
func TestAForkBeforeTheParentsLastRequestKeepsItsOwnFingerprint(t *testing.T) {
	ts := NewTrackers()
	req{at: t0.Add(time.Minute), client: mk(nil, nil, msgs(6, 1000)), clientBPs: bps5(msg(5)), facts: exact5m,
		usage: Usage{CacheWrite: 6000}}.run(ts)
	res := req{at: t0, client: mk(nil, nil, msgs(7, 1000)), clientBPs: bps5(msg(5), msg(6)), facts: exact5m,
		thread: Thread{ID: "sub", Parent: "main"}, usage: Usage{CacheRead: 6000, CacheWrite: 1000}}.run(ts)
	if res.Phase != PhaseHot { // a read in the usage proves the entry
		t.Fatalf("phase %q, want hot from the read", res.Phase)
	}
	st := ts.tracker(ThreadKey("s1", Thread{ID: "sub"}), t0).snapshot()
	if st.PrevPrint == nil || st.PrevPrint.Thread != "sub" || !st.PrevPrint.At.Equal(t0) || len(st.PrevPrint.Forwarded.Pos) != 7 {
		t.Fatalf("the fork must store its own fingerprint, got %+v", st.PrevPrint)
	}
}

// The fork's second request is compared with the fork's own first request, not with the parent.
func TestAForksSecondRequestUsesItsOwnFingerprint(t *testing.T) {
	ts := NewTrackers()
	req{at: t0, client: mk(nil, nil, msgs(6, 1000)), clientBPs: bps5(msg(5)), facts: exact5m,
		usage: Usage{CacheWrite: 6000}}.run(ts)
	th := Thread{ID: "sub", Parent: "main"}
	req{at: t0.Add(10 * time.Second), client: mk(nil, nil, msgs(7, 1000)), clientBPs: bps5(msg(5), msg(6)), facts: exact5m,
		thread: th, usage: Usage{CacheRead: 6000, CacheWrite: 1000}}.run(ts)
	res := req{at: t0.Add(20 * time.Second), client: mk(nil, nil, msgs(8, 1000)), clientBPs: bps5(msg(7)), facts: exact5m,
		thread: th, usage: Usage{CacheRead: 7000, CacheWrite: 1000}}.run(ts)
	if res.Phase != PhaseHot || !samePos(res.Boundary, ptr(msg(6))) {
		t.Fatalf("phase %q boundary %v, want hot at messages[6] (the fork's own extent)", res.Phase, res.Boundary)
	}
}

// A fork past the parent's TTL is not seeded (a cold seed would raise the saving). A read in the
// usage still makes it hot, as for any thread with no history.
func TestAForkPastTheParentsTTLWithAReadIsNotColdFromTheSeed(t *testing.T) {
	ts := NewTrackers()
	req{at: t0, client: mk(nil, nil, msgs(6, 1000)), clientBPs: bps5(msg(5)), facts: exact5m,
		usage: Usage{CacheWrite: 6000}}.run(ts)
	res := req{at: t0.Add(20 * time.Minute), client: mk(nil, nil, msgs(7, 1000)), clientBPs: bps5(msg(5), msg(6)), facts: exact5m,
		thread: Thread{ID: "sub", Parent: "main"}, usage: Usage{CacheRead: 6000, CacheWrite: 1000}}.run(ts)
	if res.Phase != PhaseHot || !hasUnpriced(res, ReasonPenaltyUnknown) {
		t.Fatalf("phase %q unpriced %+v, want hot from the read with penalty_unknown (unseeded)", res.Phase, res.Unpriced)
	}
}
