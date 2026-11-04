package engine

import (
	"testing"
	"time"
)

// No breakpoints (OpenAI automatic prefix caching): every position is a breakpoint. A cut inside
// the cached prefix is priced at the read rate and breaks the cache from the cut.
func noBPTurns(t *testing.T, cutAt int, cutTokens int, read int) Result {
	t.Helper()
	m := msgs(10, 1000)
	ts := NewTrackers()
	req{at: t0, client: mk(nil, nil, m), facts: exact5m, usage: Usage{CacheRead: 10000}}.run(ts)
	fwd := append([]blk(nil), m...)
	fwd[cutAt] = b("cut", 1000-cutTokens)
	fwd = append(fwd, b("m10", 500))
	op := rewrite("op1", "test.cut", "t:cut:1", msg(cutAt), 1000, 1000-cutTokens)
	return req{at: t0.Add(time.Minute), client: mk(nil, nil, with(m, b("m10", 500))), ops: []Operation{op},
		fwd: mk(nil, nil, fwd), facts: exact5m, usage: Usage{CacheRead: read, Input: 10500 - cutTokens - read}}.run(ts)
}

func TestNoBreakpointCutInsideCacheIsReadAndBreaks(t *testing.T) {
	res := noBPTurns(t, 2, 800, 1920)
	if res.Boundary == nil {
		t.Fatalf("boundary nil, want a cache boundary")
	}
	if p := findOp(t, res, "op1"); p.Saved != (TierTokens{CacheRead: 800}) {
		t.Fatalf("saved %+v, want 800 read", p.Saved)
	}
	br := effect(t, res, EffectBreak)
	if br.Saved.CacheRead <= 0 || br.Caused.Input <= 0 {
		t.Fatalf("break %+v, want read moved to input", br)
	}
	noChecks(t, res)
}

func TestNoBreakpointCutAtTailIsReadWithoutBreak(t *testing.T) {
	res := noBPTurns(t, 9, 800, 9984)
	if p := findOp(t, res, "op1"); p.Saved != (TierTokens{CacheRead: 800}) {
		t.Fatalf("saved %+v, want 800 read", p.Saved)
	}
	noChecks(t, res)
}
