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
	if from := (Position{SectionMessages, 0}); e.From == nil || *e.From != from {
		t.Fatalf("From = %v, want messages[0]: the range starts at the client's last breakpoint", e.From)
	}
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
	// the provider reads msg(0) and writes msg(1) under the host's new mark
	m := msgs(2, 1000)
	res := req{at: t0.Add(10 * time.Second), client: mk(nil, nil, m), clientBPs: bps5(msg(0)),
		fwdBPs: bps5(msg(0), msg(1)), facts: exact5m, usage: Usage{CacheRead: 1000, CacheWrite: 1000}}.run(ts)
	e := effect(t, res, EffectMarkPremium) // msg(1) is new: the provider wrote it
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

// weight is the cost of a tier mix in input-token units.
func weight(t TierTokens) float64 {
	return float64(t.Input) + 1.25*float64(t.CacheWrite5m) + 2*float64(t.CacheWrite1h) + 0.1*float64(t.CacheRead)
}

func net(res Result) float64 {
	var n float64
	for _, o := range res.Ops {
		n += weight(o.Saved) - weight(o.Caused)
	}
	for _, e := range res.CacheEffects {
		if !e.InBaseline {
			n += weight(e.Saved) - weight(e.Caused)
		}
	}
	return n
}

func bp1h(p Position) Breakpoint { return Breakpoint{Pos: p, TTL: "1h"} }

// A headttl change holds the head at 1h and the host adds a 1h mark at the tail. ttlPremium must book
// the head only; markPremium books the tail. Each token is booked once.
// Client body bills: sys 1000 w5m, m0 1000 w5m, m1+m2 2000 input = 4500.
// Forwarded bills: sys 1000 w1h, m0 1000 w5m, m1+m2 2000 w1h = 7250. The difference is 2750.
func TestHeadTTLAndAHostTailMarkBookEachTokenOnce(t *testing.T) {
	sysB, m := []blk{b("sys", 1000)}, msgs(3, 1000)
	head := rewrite("h", "headttl", "headttl:s1:sys", sys(0), 1000, 1000)
	res := req{at: t0, client: mk(nil, sysB, m), clientBPs: bps5(sys(0), msg(0)), ops: []Operation{head},
		fwdBPs: []Breakpoint{bp1h(sys(0)), bp5(msg(0)), bp1h(msg(2))},
		facts:  EntryFacts{TTL: time.Hour, Lifetime: LifetimeExact},
		usage:  Usage{CacheWrite: 4000, CacheWrite1h: 3000}}.run(NewTrackers())
	if p := effect(t, res, EffectTTLPremium); p.Saved != (TierTokens{CacheWrite5m: 1000}) || p.Caused != (TierTokens{CacheWrite1h: 1000}) {
		t.Fatalf("ttlPremium = %+v, want the head only: 1000 w5m -> 1000 w1h", p)
	}
	if e := effect(t, res, EffectMarkPremium); e.Saved != (TierTokens{Input: 2000}) || e.Caused != (TierTokens{CacheWrite1h: 2000}) {
		t.Fatalf("markPremium = %+v, want 2000 input -> 2000 w1h", e)
	}
	if got := net(res); got != -2750 {
		t.Fatalf("net = %v, want -2750", got)
	}
	noChecks(t, res)
}

// The range crosses breakpoints of different TTLs. Each part is written at the TTL of the first breakpoint
// at or after it (§6.2).
func TestMarkPremiumPricesEachPartAtItsOwnTTL(t *testing.T) {
	tools, sysB, m := []blk{b("t", 1000)}, []blk{b("sys", 1000)}, msgs(2, 1000)
	cases := []struct {
		name string
		fwd  []Breakpoint
		use  Usage
		want TierTokens
	}{
		{"1h inside, 5m last", []Breakpoint{bp5(tool(0)), bp1h(sys(0)), bp5(msg(1))}, Usage{CacheWrite: 4000, CacheWrite1h: 1000},
			TierTokens{CacheWrite5m: 2000, CacheWrite1h: 1000}},
		{"5m inside, 1h last", []Breakpoint{bp5(tool(0)), bp5(sys(0)), bp1h(msg(1))}, Usage{CacheWrite: 4000, CacheWrite1h: 2000},
			TierTokens{CacheWrite5m: 1000, CacheWrite1h: 2000}},
	}
	for _, c := range cases {
		res := req{at: t0, client: mk(tools, sysB, m), clientBPs: bps5(tool(0)), fwdBPs: c.fwd,
			facts: EntryFacts{TTL: time.Hour, Lifetime: LifetimeExact}, usage: c.use}.run(NewTrackers())
		e := effect(t, res, EffectMarkPremium)
		if e.Saved != (TierTokens{Input: 3000}) || e.Caused != c.want {
			t.Errorf("%s: effect = %+v, want 3000 input -> %+v", c.name, e, c.want)
		}
		if got := net(res); got != -(1.25*float64(c.want.CacheWrite5m)+2*float64(c.want.CacheWrite1h))+3000 {
			t.Errorf("%s: net = %v", c.name, got)
		}
		noChecks(t, res)
	}
}

// A usage with no cache tokens: the forwarded body was uncacheable, so the marks wrote nothing.
func TestNoPremiumWhenTheUsageShowsNoCacheTokens(t *testing.T) {
	res := req{at: t0, client: mk(nil, nil, msgs(3, 1000)), clientBPs: bps5(msg(0)), fwdBPs: bps5(msg(0), msg(2)),
		facts: exact5m, usage: Usage{Input: 3000}}.run(NewTrackers())
	noEffectOf(t, res, EffectMarkPremium)
}

// The head, a host 1h mark, and an operation that adds 500 tokens inside the marked range. The added
// tokens are priced by the operation (Caused.CacheWrite1h), so ttlPremium must not book them again.
// Client body bills: sys 1000 w5m, m0 1000 w5m, m1+m2 2000 input = 4500.
// Forwarded bills: sys 1000 w1h, m0 1000 w5m, m1+m2 2500 w1h = 8250. The difference is 3750.
// Booked: ttlPremium 1000 x 0.75 + markPremium 2000 x 1.0 + operation 500 x 2.0 = 3750.
func TestHeadTTLHostMarkAndAnAddingOperationBookEachTokenOnce(t *testing.T) {
	sysB := []blk{b("sys", 1000)}
	client := []blk{b("m0", 1000), b("m1", 1000), b("m2", 1000)}
	fwd := []blk{b("m0", 1000), b("m1", 1500), b("m2", 1000)}
	head := rewrite("h", "headttl", "headttl:s1:sys", sys(0), 1000, 1000)
	add := rewrite("a", "host.expand", "host:expand:x", msg(1), 1000, 1500)
	res := req{at: t0, client: mk(nil, sysB, client), clientBPs: bps5(sys(0), msg(0)), ops: []Operation{head, add},
		fwd: mk(nil, sysB, fwd), fwdBPs: []Breakpoint{bp1h(sys(0)), bp5(msg(0)), bp1h(msg(2))},
		facts: EntryFacts{TTL: time.Hour, Lifetime: LifetimeExact},
		usage: Usage{CacheWrite: 4500, CacheWrite1h: 3500}}.run(NewTrackers())
	if p := effect(t, res, EffectTTLPremium); p.Caused != (TierTokens{CacheWrite1h: 1000}) {
		t.Fatalf("ttlPremium = %+v, want the head only: 1000", p)
	}
	if got := net(res); got != -3750 {
		t.Fatalf("net = %v, want -3750 (the bill difference)", got)
	}
	noChecks(t, res)
}

// The client's last breakpoint comes before the head. The range holds the head, so markPremium books it
// from input to 1h and ttlPremium books nothing.
// Client bills: t 1000 w5m, sys+m0+m1 3000 input = 4250. Forwarded: 1250 + 3000 x 2.0 = 7250. Diff 3000.
func TestHeadTTLAfterTheClientsLastBreakpointIsBookedOnce(t *testing.T) {
	tools, sysB, m := []blk{b("t", 1000)}, []blk{b("sys", 1000)}, msgs(2, 1000)
	head := rewrite("h", "headttl", "headttl:s1:sys", sys(0), 1000, 1000)
	res := req{at: t0, client: mk(tools, sysB, m), clientBPs: bps5(tool(0)), ops: []Operation{head},
		fwdBPs: []Breakpoint{bp5(tool(0)), bp1h(sys(0)), bp1h(msg(1))},
		facts:  EntryFacts{TTL: time.Hour, Lifetime: LifetimeExact},
		usage:  Usage{CacheWrite: 4000, CacheWrite1h: 3000}}.run(NewTrackers())
	noEffectOf(t, res, EffectTTLPremium)
	if e := effect(t, res, EffectMarkPremium); e.Caused != (TierTokens{CacheWrite1h: 3000}) {
		t.Fatalf("markPremium = %+v, want 3000 w1h", e)
	}
	if got := net(res); got != -3000 {
		t.Fatalf("net = %v, want -3000", got)
	}
	noChecks(t, res)
}

// Reviewer trace 1 (round 3): headttl at sys(0); client marks sys(0) 5m and msg(2) 5m; the host adds a 1h
// mark at msg(0). Body: sys 1000, m0 2000, m1 1000, m2 1000. The provider writes sys and m0 at 1h
// (CacheWrite 5000, CacheWrite1h 3000). The client body writes all 5000 at 5m.
// True difference: 3000 x 0.75 = 2250 units. The head alone is 1000, so ttlPremium must book the m0 tokens too.
func TestAHostMarkBeforeTheClientsLastBreakpointPaysTheUpgradeOnEveryToken(t *testing.T) {
	sysB := []blk{b("sys", 1000)}
	m := []blk{b("m0", 2000), b("m1", 1000), b("m2", 1000)}
	head := rewrite("h", "headttl", "headttl:s1:sys", sys(0), 1000, 1000)
	res := req{at: t0, client: mk(nil, sysB, m), clientBPs: bps5(sys(0), msg(2)), ops: []Operation{head},
		fwdBPs: []Breakpoint{bp1h(sys(0)), bp1h(msg(0)), bp5(msg(2))},
		facts:  EntryFacts{TTL: time.Hour, Lifetime: LifetimeExact},
		usage:  Usage{CacheWrite: 5000, CacheWrite1h: 3000}}.run(NewTrackers())
	if p := effect(t, res, EffectTTLPremium); p.Saved != (TierTokens{CacheWrite5m: 3000}) || p.Caused != (TierTokens{CacheWrite1h: 3000}) {
		t.Fatalf("ttlPremium = %+v, want 3000 w5m -> 3000 w1h", p)
	}
	if got := net(res); got != -2250 {
		t.Fatalf("net = %v, want -2250", got)
	}
	noChecks(t, res)
}

// Reviewer trace 2 (round 3): headttl at sys(0); client marks sys(0) 5m and msg(0) 5m; an operation adds
// 500 tokens at tools[0]. Forwarded: tools 500, sys 1000, m0 1000; marks at sys(0) 1h and msg(0) 5m.
// The provider writes tools and sys at 1h (1500) and m0 at 5m (1000).
// True difference: baseline 2000 x 1.25 = 2500, billed 1500 x 2.0 + 1000 x 1.25 = 4250, so -1750.
// The operation books its 500 at 1h (-1000). ttlPremium books the sys head only (-750). Total -1750.
func TestAnAddingOperationBeforeTheHeadIsNotBookedByTTLPremium(t *testing.T) {
	sysB := []blk{b("sys", 1000)}
	m := []blk{b("m0", 1000)}
	head := rewrite("h", "headttl", "headttl:s1:sys", sys(0), 1000, 1000)
	add := rewrite("a", "host.add", "host:add:t", tool(0), 0, 500)
	res := req{at: t0, client: mk(nil, sysB, m), clientBPs: bps5(sys(0), msg(0)), ops: []Operation{head, add},
		fwd:    mk([]blk{b("t", 500)}, sysB, m),
		fwdBPs: []Breakpoint{bp1h(sys(0)), bp5(msg(0))},
		facts:  EntryFacts{TTL: time.Hour, Lifetime: LifetimeExact},
		usage:  Usage{CacheWrite: 2500, CacheWrite1h: 1500}}.run(NewTrackers())
	if p := effect(t, res, EffectTTLPremium); p.Caused != (TierTokens{CacheWrite1h: 1000}) {
		t.Fatalf("ttlPremium = %+v, want 1000 (the sys head only)", p)
	}
	if got := net(res); got != -1750 {
		t.Fatalf("net = %v, want -1750", got)
	}
	noChecks(t, res)
}
