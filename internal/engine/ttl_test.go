package engine

import (
	"testing"
	"time"
)

// accountant#40 and #41: the clock judges the entry the previous request WROTE, breakpoint by breakpoint.
// A request is judged by the TTL each breakpoint of the previous request had, not by its own EntryFacts.TTL.

func ttlFacts(d time.Duration) EntryFacts { return EntryFacts{TTL: d, Lifetime: LifetimeExact} }

func bpTTL(p Position, ttl string) Breakpoint { return Breakpoint{Pos: p, TTL: ttl} }

// replayC1 runs the replay scenario of #41. The test bills one token per byte.
//
//	r1: client [2000, 3000, 2000], forwarded [2000, 1000, 2000]: change c1 shrinks messages[1] by 2000 tokens.
//	    One breakpoint at messages[2] with ttl1. r1 writes 5000 tokens.
//	r2, gap later: both bodies get messages[3] (500). One breakpoint at messages[3] with ttl2.
//	    The change c1 is replayed, and its saving is 2000 tokens. Hot: the 2000 are read (CacheRead).
//	    Cold: the 2000 are written again, at the tier of the breakpoint that covers them.
func replayC1(gap time.Duration, ttl1, ttl2 string, f1, f2 EntryFacts, u2 Usage) (Result, PricedOp) {
	ts := NewTrackers()
	b1 := []Breakpoint{bpTTL(msg(2), ttl1)}
	req{at: t0, client: mk(nil, nil, []blk{b("a", 2000), b("b", 3000), b("c", 2000)}), clientBPs: b1,
		ops: []Operation{rewrite("o", "mask", "c1", msg(1), 3000, 1000)},
		fwd: mk(nil, nil, []blk{b("a", 2000), b("b2", 1000), b("c", 2000)}), fwdBPs: b1,
		facts: f1, usage: Usage{CacheWrite: 5000}}.run(ts)
	b2 := []Breakpoint{bpTTL(msg(3), ttl2)}
	res := req{at: t0.Add(gap), client: mk(nil, nil, []blk{b("a", 2000), b("b", 3000), b("c", 2000), b("d", 500)}), clientBPs: b2,
		fwd: mk(nil, nil, []blk{b("a", 2000), b("b2", 1000), b("c", 2000), b("d", 500)}), fwdBPs: b2,
		facts: f2, usage: u2}.run(ts)
	return res, opC1(res)
}

// opC1 is the priced operation of change c1. The scenarios always carry it.
func opC1(res Result) PricedOp {
	for _, p := range res.Ops {
		if p.ChangeID == "c1" {
			return p
		}
	}
	return PricedOp{}
}

// #41, case 1. r1 wrote a 5m entry. r2 comes 30 minutes later and asks for 1h. The entry is long expired,
// so the phase is cold and the 2000 replayed tokens are written again, at the tier of r2's breakpoint (1h).
// r2 bills no read: 5500 tokens are all written (5500 at 1h).
func TestAFiveMinuteEntryIsNotRevivedByAnHourAsk(t *testing.T) {
	res, op := replayC1(30*time.Minute, "5m", "1h", ttlFacts(5*time.Minute), ttlFacts(time.Hour),
		Usage{CacheWrite: 5500, CacheWrite1h: 5500})
	if res.Phase != PhaseCold {
		t.Fatalf("phase %q, want cold (the entry was written for 5m)", res.Phase)
	}
	if op.Saved != (TierTokens{CacheWrite1h: 2000}) {
		t.Fatalf("saved %+v, want 2000 at the 1h write tier", op.Saved)
	}
}

// Control of the case above: r2 asks 5m too. Cold, 2000 at the 5m write tier.
func TestAFiveMinuteEntryAfterThirtyMinutesIsCold(t *testing.T) {
	res, op := replayC1(30*time.Minute, "5m", "5m", ttlFacts(5*time.Minute), ttlFacts(5*time.Minute), Usage{CacheWrite: 5500})
	if res.Phase != PhaseCold || op.Saved != (TierTokens{CacheWrite5m: 2000}) {
		t.Fatalf("phase %q saved %+v, want cold, 2000 at the 5m write tier", res.Phase, op.Saved)
	}
}

// #41, case 2. r1 wrote a 1h entry. r2 comes 10 minutes later with 5m marks. The 1h entry is alive and the
// provider reads it: 5000 read, 500 written. Hot, and the 2000 replayed tokens are read (CacheRead).
func TestAnHourEntryIsAliveForAFiveMinuteRequest(t *testing.T) {
	res, op := replayC1(10*time.Minute, "1h", "5m", ttlFacts(time.Hour), ttlFacts(5*time.Minute),
		Usage{CacheRead: 5000, CacheWrite: 500})
	if res.Phase != PhaseHot || op.Saved != (TierTokens{CacheRead: 2000}) {
		t.Fatalf("phase %q saved %+v, want hot, 2000 read", res.Phase, op.Saved)
	}
}

// Same, and the provider billed no read (it dropped the entry): with no evidence the clock says hot for a
// 1h entry at 10 minutes, so the phase is hot. The usage then makes the break visible.
func TestAnHourEntryAfterTenMinutesIsHotOnTheClock(t *testing.T) {
	res, _ := replayC1(10*time.Minute, "1h", "5m", ttlFacts(time.Hour), ttlFacts(5*time.Minute), Usage{CacheWrite: 5500})
	if res.Phase != PhaseHot {
		t.Fatalf("phase %q, want hot (the entry was written for 1h)", res.Phase)
	}
}

// #41, related. A 5m entry, a gap of 5m30s, and a billed read of the whole previous extent (5000). The
// read is the evidence: hot, and the replayed 2000 tokens are read. The gap is 20 s past the TTL, outside
// the old 10 s margin.
func TestABilledReadBeatsTheClockPastTheOldSkewMargin(t *testing.T) {
	res, op := replayC1(5*time.Minute+30*time.Second, "5m", "5m", ttlFacts(5*time.Minute), ttlFacts(5*time.Minute),
		Usage{CacheRead: 5000, CacheWrite: 500})
	if res.Phase != PhaseHot || op.Saved != (TierTokens{CacheRead: 2000}) {
		t.Fatalf("phase %q saved %+v, want hot, 2000 read", res.Phase, op.Saved)
	}
}

// A billed read that does NOT reach the previous extent is no proof: the 5m entry stays cold.
func TestAShortReadDoesNotBeatTheClock(t *testing.T) {
	res, _ := replayC1(5*time.Minute+30*time.Second, "5m", "5m", ttlFacts(5*time.Minute), ttlFacts(5*time.Minute),
		Usage{CacheRead: 1000, CacheWrite: 4500})
	if res.Phase != PhaseCold {
		t.Fatalf("phase %q, want cold", res.Phase)
	}
}

// mixedHead runs #40. Messages [2000, 3000, 2000]. Breakpoints: messages[1] at 1h, messages[2] at 5m. Change c1
// shrinks messages[1] from 3000 to 1000, so the forwarded head is 2000+1000 = 3000 tokens. r1 writes 5000
// (3000 of them at 1h). r2, 10 minutes later, adds messages[3] (500) and has breakpoints messages[1] at 1h and
// messages[3] at 5m. The provider reads the 1h head (3000) and writes the 5m tail again (2000) and the new
// message (500): 2500 written. The replayed saving of c1 is 2000 tokens, inside the head: read tier.
func mixedHead(ttl time.Duration, u2 Usage) (Result, PricedOp) {
	ts := NewTrackers()
	f := ttlFacts(ttl)
	b1 := []Breakpoint{bpTTL(msg(1), "1h"), bpTTL(msg(2), "5m")}
	req{at: t0, client: mk(nil, nil, []blk{b("a", 2000), b("b", 3000), b("c", 2000)}), clientBPs: b1,
		ops: []Operation{rewrite("o", "mask", "c1", msg(1), 3000, 1000)},
		fwd: mk(nil, nil, []blk{b("a", 2000), b("b2", 1000), b("c", 2000)}), fwdBPs: b1,
		facts: f, usage: Usage{CacheWrite: 5000, CacheWrite1h: 3000}}.run(ts)
	b2 := []Breakpoint{bpTTL(msg(1), "1h"), bpTTL(msg(3), "5m")}
	res := req{at: t0.Add(10 * time.Minute), client: mk(nil, nil, []blk{b("a", 2000), b("b", 3000), b("c", 2000), b("d", 500)}), clientBPs: b2,
		fwd: mk(nil, nil, []blk{b("a", 2000), b("b2", 1000), b("c", 2000), b("d", 500)}), fwdBPs: b2,
		facts: f, usage: u2}.run(ts)
	return res, opC1(res)
}

// #40. Whatever single TTL the host reports, the head is alive at 10 minutes and its read is billed.
func TestAMixedHeadIsReadWhateverTheHostTTL(t *testing.T) {
	for _, ttl := range []time.Duration{5 * time.Minute, time.Hour} {
		res, op := mixedHead(ttl, Usage{CacheRead: 3000, CacheWrite: 2500})
		if res.Phase != PhaseHot {
			t.Errorf("Facts.TTL=%v: phase %q, want hot", ttl, res.Phase)
		}
		if op.Saved != (TierTokens{CacheRead: 2000}) {
			t.Errorf("Facts.TTL=%v: saved %+v, want 2000 read", ttl, op.Saved)
		}
		if len(res.CacheEffects) != 0 {
			t.Errorf("Facts.TTL=%v: effects %+v, want none (the head was read, only the 5m tail expired)", ttl, res.CacheEffects)
		}
	}
}

// With no billed read the same request is judged by the clock per breakpoint: the head (1h) is alive at 10
// minutes, the tail (5m) is not. The head is the live extent, so the phase is hot.
func TestAMixedHeadIsHotOnTheClockAlone(t *testing.T) {
	res, op := mixedHead(5*time.Minute, Usage{CacheWrite: 5500, CacheWrite1h: 3000})
	if res.Phase != PhaseHot {
		t.Fatalf("phase %q, want hot (the 1h head is alive)", res.Phase)
	}
	_ = op
}

// When every breakpoint is past its own TTL and nothing was read, the phase is cold.
func TestAMixedEntryPastEveryTTLIsCold(t *testing.T) {
	ts := NewTrackers()
	b1 := []Breakpoint{bpTTL(msg(1), "1h"), bpTTL(msg(2), "5m")}
	body3 := mk(nil, nil, msgs(3, 2000))
	req{at: t0, client: body3, clientBPs: b1, facts: ttlFacts(5 * time.Minute), usage: Usage{CacheWrite: 6000, CacheWrite1h: 4000}}.run(ts)
	res := req{at: t0.Add(61 * time.Minute), client: mk(nil, nil, msgs(4, 2000)), clientBPs: bpsTTL("5m", msg(3)),
		facts: ttlFacts(5 * time.Minute), usage: Usage{CacheWrite: 8000}}.run(ts)
	if res.Phase != PhaseCold {
		t.Fatalf("phase %q, want cold", res.Phase)
	}
}

func bpsTTL(ttl string, ps ...Position) []Breakpoint {
	out := make([]Breakpoint, len(ps))
	for i, p := range ps {
		out[i] = bpTTL(p, ttl)
	}
	return out
}

// The next request's own TTL decides nothing about the entry the previous request left: a fork is seeded
// only inside the parent's own TTL (5m here), whatever TTL the fork asks for.
func TestAForkIsSeededByTheParentsTTL(t *testing.T) {
	ts := NewTrackers()
	req{at: t0, client: mk(nil, nil, msgs(6, 1000)), clientBPs: bps5(msg(5)), facts: exact5m, usage: Usage{CacheWrite: 6000}}.run(ts)
	fork := req{at: t0.Add(30 * time.Minute), client: mk(nil, nil, msgs(7, 1000)), clientBPs: bpsTTL("1h", msg(6)),
		facts: ttlFacts(time.Hour), thread: Thread{ID: "sub", Parent: "main"}, usage: Usage{CacheWrite: 7000, CacheWrite1h: 7000}}
	if res := fork.run(ts); res.Phase == PhaseHot {
		t.Fatalf("phase %q: a 1h ask must not seed a fork from a 5m entry that is 30 minutes old", res.Phase)
	}
}
