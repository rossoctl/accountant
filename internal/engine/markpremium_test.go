package engine

import (
	"testing"
	"time"
)

// The tests bill one token per byte. The client has one breakpoint at msg(0). The host adds a
// breakpoint at the last message. The client body bills the tokens after its last breakpoint as input
// (1.0x). The forwarded body has them written (1.25x for 5m, 2.0x for 1h): the host's mark costs the
// difference, and the markPremium effect books it (design §4.4, accountant#49).

func markOp(at Position, before, after int) Operation {
	return rewrite("tail", "host.tailbreakpoint", "host:tailbreakpoint:x", at, before, after)
}

// warm runs request 0, which writes msg(0) under the client's mark alone.
func warm(t *testing.T, ts *Trackers, ttl string) {
	t.Helper()
	m := msgs(2, 1000)
	f := exact5m
	req{at: t0, client: mk(nil, nil, m[:1]), clientBPs: []Breakpoint{{Pos: msg(0), TTL: ttl}}, facts: f,
		usage: Usage{CacheWrite: 1000}}.run(ts)
}

func TestAnAddedTailBreakpointPaysTheWritePremiumWhenHot(t *testing.T) {
	ts := NewTrackers()
	warm(t, ts, "5m")
	client := []blk{b("m0", 1000), b("m1", 1000), b("m2", 1000)}
	fwd := []blk{b("m0", 1000), b("m1", 1000), b("m2", 1063)}
	res := req{at: t0.Add(10 * time.Second), client: mk(nil, nil, client), clientBPs: bps5(msg(0)),
		ops: []Operation{markOp(msg(2), 1000, 1063)}, fwd: mk(nil, nil, fwd), fwdBPs: bps5(msg(0), msg(2)),
		facts: exact5m, usage: Usage{CacheRead: 1000, CacheWrite: 2063}}.run(ts)
	e := effect(t, res, EffectMarkPremium)
	// the client body bills m1 and m2 (2000 tokens) as input; the forwarded one writes them
	if e.Saved != (TierTokens{Input: 2000}) || e.Caused != (TierTokens{CacheWrite5m: 2000}) || e.InBaseline ||
		e.ChangeID != "host:tailbreakpoint:x" || e.TTL != "5m" || !e.Estimated {
		t.Fatalf("effect = %+v, want 2000 input -> 2000 write5m, host:tailbreakpoint:x", e)
	}
	noChecks(t, res)
}

func TestAnAddedTailBreakpointPaysThePremiumWhenCold(t *testing.T) {
	// the first request: the client body would write msg(0) and bill the rest as input
	client := []blk{b("m0", 1000), b("m1", 1000), b("m2", 1000)}
	res := req{at: t0, client: mk(nil, nil, client), clientBPs: bps5(msg(0)), fwdBPs: bps5(msg(0), msg(2)),
		facts: exact5m, usage: Usage{CacheWrite: 3000}}.run(NewTrackers())
	e := effect(t, res, EffectMarkPremium)
	if e.Saved != (TierTokens{Input: 2000}) || e.Caused != (TierTokens{CacheWrite5m: 2000}) || e.ChangeID != "" {
		t.Fatalf("effect = %+v, want 2000 input -> 2000 write5m, no change", e)
	}
	noChecks(t, res)
}

func TestAnAddedTailBreakpointAt1hPaysTheOneHourPremium(t *testing.T) {
	ts := NewTrackers()
	warm(t, ts, "1h")
	client := []blk{b("m0", 1000), b("m1", 1000), b("m2", 1000)}
	f := EntryFacts{TTL: time.Hour, Lifetime: LifetimeExact}
	res := req{at: t0.Add(10 * time.Second), client: mk(nil, nil, client),
		clientBPs: []Breakpoint{{Pos: msg(0), TTL: "1h"}},
		fwdBPs:    []Breakpoint{{Pos: msg(0), TTL: "1h"}, {Pos: msg(2), TTL: "1h"}},
		facts:     f, usage: Usage{CacheRead: 1000, CacheWrite: 2000, CacheWrite1h: 2000}}.run(ts)
	e := effect(t, res, EffectMarkPremium)
	if e.Saved != (TierTokens{Input: 2000}) || e.Caused != (TierTokens{CacheWrite1h: 2000}) || e.TTL != "1h" {
		t.Fatalf("effect = %+v, want 2000 input -> 2000 write1h", e)
	}
	noChecks(t, res)
}

func TestNoPremiumWhenTheHostMarkIsInsideThePreviousRead(t *testing.T) {
	ts := NewTrackers()
	warm(t, ts, "5m")
	// the provider reads all of msg(0..1): the host's mark at msg(1) wrote nothing new
	m := msgs(2, 1000)
	res := req{at: t0.Add(10 * time.Second), client: mk(nil, nil, m), clientBPs: bps5(msg(0)),
		fwdBPs: bps5(msg(0), msg(1)), facts: exact5m, usage: Usage{CacheRead: 1000, CacheWrite: 1000}}.run(ts)
	e := effect(t, res, EffectMarkPremium) // msg(1) is new: written
	if e.Caused != (TierTokens{CacheWrite5m: 1000}) {
		t.Fatalf("effect = %+v", e)
	}
	ts2 := NewTrackers()
	req{at: t0, client: mk(nil, nil, m), clientBPs: bps5(msg(0)), fwdBPs: bps5(msg(0), msg(1)), facts: exact5m,
		usage: Usage{CacheWrite: 2000}}.run(ts2)
	// the same body again: the provider reads both, writes nothing
	res = req{at: t0.Add(10 * time.Second), client: mk(nil, nil, m), clientBPs: bps5(msg(0)),
		fwdBPs: bps5(msg(0), msg(1)), facts: exact5m, usage: Usage{CacheRead: 2000}}.run(ts2)
	noEffectOf(t, res, EffectMarkPremium)
	noChecks(t, res)
}

func TestNoPremiumWhenTheClientMarkIsAtTheEndOrAbsent(t *testing.T) {
	m := msgs(3, 1000)
	res := req{at: t0, client: mk(nil, nil, m), clientBPs: bps5(msg(0), msg(2)), facts: exact5m,
		usage: Usage{CacheWrite: 3000}}.run(NewTrackers())
	noEffectOf(t, res, EffectMarkPremium)
	// no client mark: every position is a mark, so the host adds none
	res = req{at: t0, client: mk(nil, nil, m), fwdBPs: bps5(msg(2)), facts: exact5m,
		usage: Usage{CacheWrite: 3000}}.run(NewTrackers())
	noEffectOf(t, res, EffectMarkPremium)
}

func TestARemovedClientBreakpointPaysNoPremium(t *testing.T) {
	m := msgs(3, 1000)
	res := req{at: t0, client: mk(nil, nil, m), clientBPs: bps5(msg(0), msg(2)), fwdBPs: bps5(msg(0)),
		facts: exact5m, usage: Usage{CacheWrite: 1000, Input: 2000}}.run(NewTrackers())
	noEffectOf(t, res, EffectMarkPremium)
}

func noEffectOf(t *testing.T, res Result, kind CacheEffectKind) {
	t.Helper()
	for _, e := range res.CacheEffects {
		if e.Kind == kind {
			t.Fatalf("unexpected %s effect: %+v", kind, e)
		}
	}
}
