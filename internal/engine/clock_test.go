package engine

import (
	"testing"
	"time"
)

// accountant#16: the clock and the order of calls must not win over the usage evidence.

// A request whose At is before the stored request's At must not replace the stored fingerprint.
func TestAnOutOfOrderRequestKeepsTheNewerFingerprint(t *testing.T) {
	ts := NewTrackers()
	bAt := t0.Add(2 * time.Minute)
	for _, q := range []req{
		{at: t0, client: mk(nil, nil, msgs(3, 1000)), clientBPs: bps5(msg(2)), facts: exact5m, usage: Usage{CacheWrite: 3000}},
		{at: bAt, client: mk(nil, nil, msgs(4, 1000)), clientBPs: bps5(msg(3)), facts: exact5m, usage: Usage{CacheRead: 3000, CacheWrite: 1000}},
	} {
		q.run(ts)
	}
	// C arrives late: its At is 1 hour before B.
	c := req{at: bAt.Add(-time.Hour), client: mk(nil, nil, msgs(5, 1000)), clientBPs: bps5(msg(4)), facts: exact5m,
		usage: Usage{CacheRead: 4000, CacheWrite: 1000}}
	c.run(ts)
	d := req{at: bAt.Add(time.Minute), client: mk(nil, nil, msgs(6, 1000)), clientBPs: bps5(msg(5)), facts: exact5m,
		usage: Usage{CacheRead: 4000, CacheWrite: 2000}}
	res := d.run(ts)
	if res.Phase != PhaseHot || !samePos(res.Boundary, ptr(msg(3))) {
		t.Fatalf("D: phase %q boundary %v, want hot at messages[3] (compared with B, not C)", res.Phase, res.Boundary)
	}
}

// The provider read the entry although the clock says it expired: the read is the evidence.
func TestAReadThatCoversThePreviousExtentBeatsTheTTLEdge(t *testing.T) {
	ts := NewTrackers()
	req{at: t0, client: mk(nil, nil, msgs(3, 1000)), clientBPs: bps5(msg(2)), facts: exact5m,
		usage: Usage{CacheWrite: 3000}}.run(ts)
	res := req{at: t0.Add(5*time.Minute + 2*time.Second), client: mk(nil, nil, msgs(4, 1000)), clientBPs: bps5(msg(3)),
		facts: exact5m, usage: Usage{CacheRead: 3000, CacheWrite: 1000}}.run(ts)
	if res.Phase != PhaseHot || !samePos(res.Boundary, ptr(msg(2))) {
		t.Fatalf("phase %q boundary %v, want hot at messages[2]", res.Phase, res.Boundary)
	}
	noChecks(t, res)
}

// No read, no evidence: the gap past an exact TTL stays cold.
func TestNoReadPastTheTTLStaysCold(t *testing.T) {
	ts := NewTrackers()
	req{at: t0, client: mk(nil, nil, msgs(3, 1000)), clientBPs: bps5(msg(2)), facts: exact5m,
		usage: Usage{CacheWrite: 3000}}.run(ts)
	res := req{at: t0.Add(5*time.Minute + 2*time.Second), client: mk(nil, nil, msgs(4, 1000)), clientBPs: bps5(msg(3)),
		facts: exact5m, usage: Usage{CacheWrite: 4000}}.run(ts)
	if res.Phase != PhaseCold {
		t.Fatalf("phase %q, want cold", res.Phase)
	}
}

// A fork of a warm parent starts from the parent's fingerprint, so it is not unknown.
func TestAForkIsSeededFromItsParent(t *testing.T) {
	ts := NewTrackers()
	req{at: t0, client: mk(nil, nil, msgs(6, 1000)), clientBPs: bps5(msg(5)), facts: exact5m,
		usage: Usage{CacheWrite: 6000}}.run(ts)
	fork := req{at: t0.Add(10 * time.Second), client: mk(nil, nil, msgs(7, 1000)), clientBPs: bps5(msg(6)), facts: exact5m,
		thread: Thread{ID: "sub", Parent: "main"}, usage: Usage{CacheRead: 6000, CacheWrite: 1000}}
	res := fork.run(ts)
	if res.Phase != PhaseHot || !samePos(res.Boundary, ptr(msg(5))) || hasUnpriced(res, ReasonPenaltyUnknown) {
		t.Fatalf("fork: phase %q boundary %v unpriced %+v, want hot at messages[5], no penalty_unknown",
			res.Phase, res.Boundary, res.Unpriced)
	}
	// The parent keeps its own state.
	p := ts.tracker(ThreadKey("s1", Thread{ID: "main"}), t0).snapshot()
	if p.Version != 1 {
		t.Fatalf("the fork changed the parent: Version = %d", p.Version)
	}
}

// A fork whose body does not keep the parent's cached prefix is not seeded.
func TestAForkThatDivergesInsideTheParentExtentIsNotSeeded(t *testing.T) {
	ts := NewTrackers()
	req{at: t0, client: mk(nil, nil, msgs(6, 1000)), clientBPs: bps5(msg(5)), facts: exact5m,
		usage: Usage{CacheWrite: 6000}}.run(ts)
	other := with(msgs(3, 1000), b("x3", 1000), b("x4", 1000), b("x5", 1000))
	res := req{at: t0.Add(10 * time.Second), client: mk(nil, nil, other), clientBPs: bps5(msg(5)), facts: exact5m,
		thread: Thread{ID: "sub", Parent: "main"}, usage: Usage{CacheWrite: 6000}}.run(ts)
	if res.Phase != PhaseUnknown {
		t.Fatalf("phase %q, want unknown", res.Phase)
	}
}
