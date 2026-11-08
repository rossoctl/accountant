package engine

import (
	"testing"
	"time"
)

// accountant#52. Request 0 has the client's mark at msg(0) and the host's at msg(2); the provider writes all
// 3000 tokens. The client body would write 1000 and bill 2000 as input. Request 1 repeats the marks without
// the host's: the provider reads 3000, the client body would read 1000 and bill 2000 as input. The 2000
// tokens move from input to read, which is the markPayback.
func TestTheTurnAfterAnAddedBreakpointGetsAPayback(t *testing.T) {
	ts := NewTrackers()
	client := []blk{b("m0", 1000), b("m1", 1000), b("m2", 1000)}
	req{at: t0, client: mk(nil, nil, client), clientBPs: bps5(msg(0)), fwdBPs: bps5(msg(0), msg(2)),
		facts: exact5m, usage: Usage{CacheWrite: 3000}}.run(ts)
	c1 := append(append([]blk(nil), client...), b("m3", 100))
	res := req{at: t0.Add(40 * time.Second), client: mk(nil, nil, c1), clientBPs: bps5(msg(0)), fwd: mk(nil, nil, c1),
		fwdBPs: bps5(msg(0)), facts: exact5m, usage: Usage{CacheRead: 3000, CacheWrite: 100}}.run(ts)
	e := effect(t, res, EffectMarkPayback)
	if e.Saved != (TierTokens{Input: 2000}) || e.Caused != (TierTokens{CacheRead: 2000}) {
		t.Fatalf("effect = %+v, want 2000 input -> 2000 read", e)
	}
	noChecks(t, res)
}

// Past the TTL the entry is gone: the provider reads nothing, so there is no payback.
func TestNoPaybackAfterTheEntryExpired(t *testing.T) {
	ts := NewTrackers()
	client := []blk{b("m0", 1000), b("m1", 1000), b("m2", 1000)}
	req{at: t0, client: mk(nil, nil, client), clientBPs: bps5(msg(0)), fwdBPs: bps5(msg(0), msg(2)),
		facts: exact5m, usage: Usage{CacheWrite: 3000}}.run(ts)
	res := req{at: t0.Add(6 * time.Minute), client: mk(nil, nil, client), clientBPs: bps5(msg(0)), fwd: mk(nil, nil, client),
		fwdBPs: bps5(msg(0)), facts: exact5m, usage: Usage{CacheWrite: 3000}}.run(ts)
	noEffectOf(t, res, EffectMarkPayback)
}

// accountant#56. A 1h ask billed at 5m (CacheWrite1h = 0, Facts.TTL 5m) is a 5m entry: ten minutes later the
// phase is cold. Without the host's TTL the same usage is taken as a host that left CacheWrite1h unset.
func TestAOneHourAskBilledAtFiveMinutesIsAFiveMinuteEntry(t *testing.T) {
	run := func(f EntryFacts) Result {
		ts := NewTrackers()
		c := []blk{b("a", 1000), b("b", 1000)}
		req{at: t0, client: mk(nil, nil, c), clientBPs: []Breakpoint{bpTTL(msg(1), "1h")}, facts: f,
			usage: Usage{CacheWrite: 2000}}.run(ts)
		return req{at: t0.Add(10 * time.Minute), client: mk(nil, nil, c), clientBPs: []Breakpoint{bpTTL(msg(1), "1h")}, facts: f,
			usage: Usage{CacheWrite: 2000}}.run(ts)
	}
	if res := run(exact5m); res.Phase != PhaseCold {
		t.Fatalf("billed at 5m with Facts.TTL 5m: phase %q, want cold", res.Phase)
	}
	if res := run(ttlFacts(time.Hour)); res.Phase != PhaseHot {
		t.Fatalf("Facts.TTL 1h: phase %q, want hot: the host's TTL is the 1h it asked for", res.Phase)
	}
}

// accountant#56. A 1h head and a 5m tail. A ping at minute 4 reads both; the return at minute 8 reads both. The
// head was alive without the ping, so the rescue is the 5m tail only.
func TestAPingRescuesTheFiveMinuteTailOfAMixedEntryOnly(t *testing.T) {
	ts := NewTrackers()
	c := []blk{b("a", 2000), b("b", 2000), b("c", 2000)}
	mixed := []Breakpoint{bpTTL(msg(1), "1h"), bpTTL(msg(2), "5m")}
	req{at: t0, client: mk(nil, nil, c), clientBPs: mixed, facts: exact5m, usage: Usage{CacheWrite: 6000, CacheWrite1h: 4000}}.run(ts)
	pr := msg(2)
	f := exact5m
	f.LastPing, f.PingRead, f.PingWindow = t0.Add(4*time.Minute), &pr, "ka:w"
	c1 := append(append([]blk(nil), c...), b("d", 500))
	res := req{at: t0.Add(8 * time.Minute), client: mk(nil, nil, c1), clientBPs: mixed, facts: f,
		usage: Usage{CacheRead: 6000, CacheWrite: 500}}.run(ts)
	e := effect(t, res, EffectRescue)
	if e.Saved != (TierTokens{CacheWrite5m: 2000}) || e.Caused != (TierTokens{CacheRead: 2000}) {
		t.Fatalf("rescue = %+v, want the 2000-token tail, 5m write -> read", e)
	}
}

// Review of #57, finding 1. The client marks only the 1h head; the host adds the 5m tail mark. A ping kept the
// tail alive, so the tail rescue books the tail. markPayback must not book the same tokens as well.
func TestNoPaybackOnTopOfATailRescue(t *testing.T) {
	ts := NewTrackers()
	c := []blk{b("a", 2000), b("b", 2000), b("c", 2000)}
	mixed := []Breakpoint{bpTTL(msg(1), "1h"), bpTTL(msg(2), "5m")}
	head := []Breakpoint{bpTTL(msg(1), "1h")}
	req{at: t0, client: mk(nil, nil, c), clientBPs: head, fwdBPs: mixed, facts: exact5m,
		usage: Usage{CacheWrite: 6000, CacheWrite1h: 4000}}.run(ts)
	pr := msg(2)
	f := exact5m
	f.LastPing, f.PingRead, f.PingWindow = t0.Add(4*time.Minute), &pr, "ka:w"
	c1 := append(append([]blk(nil), c...), b("d", 500))
	res := req{at: t0.Add(8 * time.Minute), client: mk(nil, nil, c1), clientBPs: head, fwd: mk(nil, nil, c1),
		fwdBPs: []Breakpoint{bpTTL(msg(1), "1h"), bpTTL(msg(3), "5m")}, facts: f,
		usage: Usage{CacheRead: 6000, CacheWrite: 500}}.run(ts)
	effect(t, res, EffectRescue)
	noEffectOf(t, res, EffectMarkPayback)
}

// Review of #57, finding 2. A 1h head (headttl) read after 5 minutes gets a ttlRescue. The client's last mark is
// before the head, so markPayback would book the head again. It must not.
func TestNoPaybackOnTopOfAHeadTTLRescue(t *testing.T) {
	facts := ttlFacts(5 * time.Minute)
	toolB, sysB := []blk{b("t", 500)}, []blk{b("sys", 1000)}
	head := rewrite("op2", "headttl", "headttl:s1:sys", sys(0), 1000, 1000)
	cb := []Breakpoint{{Pos: Position{SectionTools, 0}, TTL: "5m"}}
	fb := []Breakpoint{{Pos: Position{SectionTools, 0}, TTL: "5m"}, {Pos: sys(0), TTL: "1h"}}
	ts := NewTrackers()
	req{at: t0, client: mk(toolB, sysB, []blk{b("m0", 1000)}), clientBPs: cb, fwdBPs: fb, facts: facts, ops: []Operation{head},
		usage: Usage{CacheWrite: 2500, CacheWrite1h: 1000}}.run(ts)
	res := req{at: t0.Add(10 * time.Minute), client: mk(toolB, sysB, []blk{b("m0", 1000)}), clientBPs: cb, fwdBPs: fb,
		facts: facts, ops: []Operation{head}, usage: Usage{CacheRead: 1500, CacheWrite: 1000}}.run(ts)
	effect(t, res, EffectTTLRescue)
	noEffectOf(t, res, EffectMarkPayback)
}
