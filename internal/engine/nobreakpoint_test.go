package engine

import (
	"testing"
	"time"
)

// No breakpoints (OpenAI automatic prefix caching): every position is a breakpoint. A cut inside
// the cached prefix is priced at the read rate and breaks the cache from the cut.

// noBPSetup sends a first request of 10 messages of 1000 tokens (all cached, no breakpoints).
func noBPSetup() (*Trackers, []blk) {
	m := msgs(10, 1000)
	ts := NewTrackers()
	req{at: t0, client: mk(nil, nil, m), facts: exact5m, usage: Usage{CacheRead: 10000}}.run(ts)
	return ts, m
}

// cutTurn cuts `cut` tokens from message `at` and appends a 500-token message.
func cutTurn(ts *Trackers, m []blk, at, cut int, usage Usage) Result {
	fwd := append([]blk(nil), m...)
	fwd[at] = b("cut", 1000-cut)
	fwd = append(fwd, b("m10", 500))
	op := rewrite("op1", "test.cut", "t:cut:1", msg(at), 1000, 1000-cut)
	return req{at: t0.Add(time.Minute), client: mk(nil, nil, with(m, b("m10", 500))), ops: []Operation{op},
		fwd: mk(nil, nil, fwd), facts: exact5m, usage: usage}.run(ts)
}

func TestNoBreakpointCutInsideCacheIsReadAndBreaks(t *testing.T) {
	ts, m := noBPSetup()
	// The provider read 1920 tokens (messages 0 and 1, rounded to 128) and billed the rest as input.
	res := cutTurn(ts, m, 2, 800, Usage{CacheRead: 1920, Input: 7780})
	noChecks(t, res)
	if p := findOp(t, res, "op1"); p.Saved != (TierTokens{CacheRead: 800}) {
		t.Fatalf("saved %+v, want 800 read", p.Saved)
	}
	br := effect(t, res, EffectBreak)
	if br.From == nil || *br.From != msg(1) {
		t.Fatalf("break From = %+v, want messages[1]", br.From)
	}
	// From messages[1] to B* (messages[9]): 7 messages of 1000 and the cut one of 200 = 7200.
	if br.Saved != (TierTokens{CacheRead: 7200}) || br.Caused != (TierTokens{Input: 7200}) {
		t.Fatalf("break saved %+v caused %+v, want 7200 read moved to 7200 input", br.Saved, br.Caused)
	}
}

// The break leaves the new prefix cached: the next request is hot and prices a cut as a read.
func TestNoBreakpointThreadStaysHotAfterABreak(t *testing.T) {
	ts, m := noBPSetup()
	cutTurn(ts, m, 2, 800, Usage{CacheRead: 1920, Input: 7780})
	st := stateOf(ts, "main")
	if st.Cache.CachePhase != PhaseHot || st.Cache.Extent == nil {
		t.Fatalf("cache state %+v, want hot with an extent", st.Cache)
	}
	// Second request: the same cut as before, plus a 300-token cut at message 8. The provider read
	// through message 8 (8704 tokens, rounded to 128).
	fwd := append([]blk(nil), m...)
	fwd[2] = b("cut", 200)
	fwd[8] = b("m8c", 700)
	fwd = append(fwd, b("m10", 500), b("m11", 100))
	ops := []Operation{
		rewrite("op1", "test.cut", "t:cut:1", msg(2), 1000, 200),
		rewrite("op2", "test.cut", "t:cut:2", msg(8), 1000, 700),
	}
	res := req{at: t0.Add(2 * time.Minute), client: mk(nil, nil, with(m, b("m10", 500), b("m11", 100))), ops: ops,
		fwd: mk(nil, nil, fwd), facts: exact5m, usage: Usage{CacheRead: 8704, Input: 796}}.run(ts)
	if res.Phase != PhaseHot || res.Boundary == nil {
		t.Fatalf("phase %v boundary %v, want hot with a boundary", res.Phase, res.Boundary)
	}
	if p := findOp(t, res, "op2"); p.Saved.CacheRead != 300 {
		t.Fatalf("saved %+v, want 300 read", p.Saved)
	}
}

// A cut at the tail is inside the read: priced as read, and the break it records moves nothing.
func TestNoBreakpointCutAtTailIsRead(t *testing.T) {
	ts, m := noBPSetup()
	res := cutTurn(ts, m, 9, 800, Usage{CacheRead: 9000, Input: 700})
	noChecks(t, res)
	if p := findOp(t, res, "op1"); p.Saved != (TierTokens{CacheRead: 800}) {
		t.Fatalf("saved %+v, want 800 read", p.Saved)
	}
	br := effect(t, res, EffectBreak)
	if br.Saved != (TierTokens{}) || br.Caused != (TierTokens{}) {
		t.Fatalf("break %+v, want zero tokens", br)
	}
}

// B* = x when this request has no client breakpoint, also when the previous one had some.
func TestNoBreakpointBoundaryIsTheEditWhenThePreviousHadBreakpoints(t *testing.T) {
	m := msgs(10, 1000)
	ts := NewTrackers()
	req{at: t0, client: mk(nil, nil, m), clientBPs: bps5(msg(9)), fwd: mk(nil, nil, m), fwdBPs: bps5(msg(9)),
		facts: exact5m, usage: Usage{CacheWrite: 10000}}.run(ts)
	m2 := append([]blk(nil), m...)
	m2[4] = b("edit", 1000)
	res := req{at: t0.Add(time.Minute), client: mk(nil, nil, m2), fwd: mk(nil, nil, m2), facts: exact5m,
		usage: Usage{CacheRead: 4096, Input: 5904}}.run(ts)
	if res.Boundary == nil || *res.Boundary != msg(3) {
		t.Fatalf("boundary %+v, want messages[3] (the last position before the edit)", res.Boundary)
	}
}

// B* = x with no breakpoint anywhere and a client edit inside the cached prefix.
func TestNoBreakpointBoundaryIsTheEditWithNoBreakpointAnywhere(t *testing.T) {
	ts, m := noBPSetup()
	m2 := append([]blk(nil), m...)
	m2[4] = b("edit", 1000)
	res := req{at: t0.Add(time.Minute), client: mk(nil, nil, m2), fwd: mk(nil, nil, m2), facts: exact5m,
		usage: Usage{CacheRead: 4096, Input: 5904}}.run(ts)
	if res.Boundary == nil || *res.Boundary != msg(3) {
		t.Fatalf("boundary %+v, want messages[3]", res.Boundary)
	}
}
