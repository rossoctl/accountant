package engine

import "testing"

// §5.1, §6.2, accountant#15. The tests bill one token per byte.

func minFacts(min int) EntryFacts {
	f := exact5m
	f.MinCacheable = min
	return f
}

// A request with a cut after the first breakpoint: m0 goes from 400 to 100 bytes. With no previous
// request the phase is unknown and B* is the read extent (system[0], 500).
func cutAfterBStar(extra []blk, extraBPs []Position, facts EntryFacts, usage Usage) Result {
	sysB := []blk{b("sys", 1000)}
	cm := append([]blk{b("m0", 600)}, extra...)
	fm := append([]blk{b("m0", 300)}, extra...)
	cbps := append(bps5(sys(0), msg(0)), bps5(extraBPs...)...)
	return req{at: t0, client: mk(nil, sysB, cm), clientBPs: cbps,
		ops: []Operation{rewrite("a", "op", "c:1", msg(0), 600, 300)},
		fwd: mk(nil, sysB, fm), fwdBPs: cbps, facts: facts, usage: usage}.run(NewTrackers())
}

// The client prefix up to the breakpoint (1600) is below the minimum: the client body would have cached
// nothing, so the cut is input, not a write. The usage has cache tokens, so only MinCacheable decides.
func TestACutInAClientPrefixBelowMinCacheableIsInput(t *testing.T) {
	res := cutAfterBStar(nil, nil, minFacts(2048), Usage{CacheRead: 1000, Input: 300})
	noChecks(t, res)
	if s := findOp(t, res, "a").Saved; s.Input != 300 || s.CacheWrite5m != 0 {
		t.Fatalf("Saved = %+v, want Input=300", s)
	}
}

// Without MinCacheable the same cut is a write.
func TestACutIsAWriteWhenMinCacheableIsNotSet(t *testing.T) {
	res := cutAfterBStar(nil, nil, exact5m, Usage{CacheRead: 1000, Input: 300})
	if s := findOp(t, res, "a").Saved; s.CacheWrite5m != 300 {
		t.Fatalf("Saved = %+v, want CacheWrite5m=300", s)
	}
}

// Prefixes are cumulative: the first breakpoint (1600) is too short, a later one (2600) reaches the
// minimum and caches the cut.
func TestALaterBreakpointAtTheMinimumCachesTheCut(t *testing.T) {
	res := cutAfterBStar([]blk{b("m1", 1000)}, []Position{msg(1)}, minFacts(2048), Usage{CacheRead: 1000, CacheWrite: 1300})
	noChecks(t, res)
	if s := findOp(t, res, "a").Saved; s.CacheWrite5m != 300 || s.Input != 0 {
		t.Fatalf("Saved = %+v, want CacheWrite5m=300", s)
	}
}

func shortPromptCut(facts EntryFacts, usage Usage) Result {
	return req{at: t0, client: mk(nil, nil, []blk{b("m0", 2100)}), clientBPs: bps5(msg(0)),
		ops: []Operation{rewrite("a", "op", "c:1", msg(0), 2100, 600)},
		fwd: mk(nil, nil, []blk{b("m0", 600)}), fwdBPs: bps5(msg(0)), facts: facts, usage: usage}.run(NewTrackers())
}

// The usage rule describes the forwarded body only. The client prefix (2100) reaches the minimum, so the
// client body would have written the cut, although the forwarded body (600) cached nothing.
func TestAUsageWithNoCacheTokensDoesNotChangeTheClientSide(t *testing.T) {
	res := shortPromptCut(minFacts(1024), Usage{Input: 600})
	if s := findOp(t, res, "a").Saved; s.CacheWrite5m != 1500 || s.Input != 0 {
		t.Fatalf("Saved = %+v, want CacheWrite5m=1500", s)
	}
}

// A client prompt below the minimum (600) is input whatever the usage says.
func TestAShortClientPromptCutIsInput(t *testing.T) {
	res := req{at: t0, client: mk(nil, nil, []blk{b("m0", 600)}), clientBPs: bps5(msg(0)),
		ops: []Operation{rewrite("a", "op", "c:1", msg(0), 600, 100)},
		fwd: mk(nil, nil, []blk{b("m0", 100)}), fwdBPs: bps5(msg(0)), facts: minFacts(1024),
		usage: Usage{Input: 100}}.run(NewTrackers())
	if s := findOp(t, res, "a").Saved; s.Input != 500 || s.CacheWrite5m != 0 {
		t.Fatalf("Saved = %+v, want Input=500", s)
	}
}

// An added block in a forwarded prompt that cached nothing is input.
func TestAnAddedBlockInAnUncacheableForwardedBodyIsInput(t *testing.T) {
	res := req{at: t0, client: mk(nil, nil, []blk{b("m0", 300)}), clientBPs: bps5(msg(0)),
		ops: []Operation{rewrite("a", "op", "c:1", msg(0), 300, 600)},
		fwd: mk(nil, nil, []blk{b("m0", 600)}), fwdBPs: bps5(msg(0)), facts: exact5m,
		usage: Usage{Input: 600}}.run(NewTrackers())
	if c := findOp(t, res, "a").Caused; c.Input != 300 || c.CacheWrite5m != 0 {
		t.Fatalf("Caused = %+v, want Input=300", c)
	}
}

// A break moves read tokens to the write tier. When the forwarded prefix is below the minimum, nothing is
// written and the moved tokens are input (§5.1).
func TestABreakInAForwardedBodyBelowMinCacheableIsInput(t *testing.T) {
	res := ex7Facts(bps5(sys(0), msg(12)), Usage{CacheRead: 15300, CacheWrite: 21010}, minFacts(100000))
	br := effect(t, res, EffectBreak)
	if br.Caused.Input != 19010 || br.Caused.CacheWrite5m != 0 {
		t.Fatalf("break %+v, want 19010 input", br)
	}
}

// With no cache tokens in the usage the provider wrote nothing, so the moved tokens are input (§5.1).
func TestABreakWithNoCacheTokensInTheUsageIsInput(t *testing.T) {
	res := ex7Facts(bps5(sys(0), msg(12)), Usage{Input: 36310}, exact5m)
	for _, e := range res.CacheEffects {
		if e.Kind == EffectBreak && e.Caused.CacheWrite5m != 0 {
			t.Fatalf("break %+v, want no write", e)
		}
	}
}

func TestALongPromptCutKeepsTheWriteRateWithMinCacheable(t *testing.T) {
	res := req{at: t0, client: mk(nil, nil, []blk{b("m0", 3000)}), clientBPs: bps5(msg(0)),
		ops: []Operation{rewrite("a", "op", "c:1", msg(0), 3000, 1500)},
		fwd: mk(nil, nil, []blk{b("m0", 1500)}), fwdBPs: bps5(msg(0)), facts: minFacts(1024),
		usage: Usage{CacheWrite: 1500}}.run(NewTrackers())
	if s := findOp(t, res, "a").Saved; s.CacheWrite5m != 1500 {
		t.Fatalf("Saved = %+v, want CacheWrite5m=1500", s)
	}
}
