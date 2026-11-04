package engine

import (
	"testing"
	"time"
)

// Issue 19: a mask removes 1201 tokens in a request that a ping rescued. Without the proxy the
// whole client body is written cold. The ops book the mask at the read tier and the rescue books
// 1255 write -> read. The rest, 1201 write -> read, is the interaction (§6.2).
func TestARescuedRequestWithAContentChangeReportsTheInteraction(t *testing.T) {
	sysB := []blk{b("sys", 1255)}
	client := mk(nil, sysB, []blk{b("m0", 1214)})
	fwd := mk(nil, sysB, []blk{b("m0", 13)})
	ts := NewTrackers()
	f := pingFacts()
	f.PingRead = ptr(msg(0))
	req{at: t0.Add(20 * time.Minute), client: client, clientBPs: bps5(sys(0), msg(0)), facts: f, usage: Usage{CacheWrite: 2469}}.run(ts)
	res := req{at: t0.Add(28 * time.Minute), client: client, clientBPs: bps5(sys(0), msg(0)), facts: f,
		ops: []Operation{rewrite("op1", "mask", "mask:1", msg(0), 1214, 13)}, fwd: fwd,
		usage: Usage{CacheRead: 1255, CacheWrite: 13}}.run(ts)
	noChecks(t, res)
	if e := effect(t, res, EffectRescue); e.Saved != (TierTokens{CacheWrite5m: 1255}) || e.Caused != (TierTokens{CacheRead: 1255}) {
		t.Fatalf("rescue %+v, want 1255 write->read", e)
	}
	if p := findOp(t, res, "op1"); p.Saved != (TierTokens{CacheRead: 1201}) {
		t.Fatalf("mask saved %+v, want 1201 booked at the read tier", p.Saved)
	}
	if res.Interaction != (TierTokens{CacheWrite5m: 1201}) || res.InteractionCaused != (TierTokens{CacheRead: 1201}) {
		t.Fatalf("interaction %+v caused %+v, want 1201 write -> read", res.Interaction, res.InteractionCaused)
	}
}

// A request with no rescue has no interaction.
func TestAContentChangeWithNoRescueHasNoInteraction(t *testing.T) {
	ts := NewTrackers()
	sysB := []blk{b("sys", 1255)}
	req{at: t0, client: mk(nil, sysB, nil), clientBPs: bps5(sys(0)), facts: exact5m, usage: Usage{CacheWrite: 1255}}.run(ts)
	res := req{at: t0.Add(time.Minute), client: mk(nil, sysB, []blk{b("m0", 1214)}), clientBPs: bps5(sys(0), msg(0)), facts: exact5m,
		ops: []Operation{rewrite("op1", "mask", "mask:1", msg(0), 1214, 13)}, fwd: mk(nil, sysB, []blk{b("m0", 13)}),
		usage: Usage{CacheRead: 1255, CacheWrite: 13}}.run(ts)
	if res.Interaction != (TierTokens{}) || res.InteractionCaused != (TierTokens{}) {
		t.Fatalf("interaction %+v %+v, want none", res.Interaction, res.InteractionCaused)
	}
}

// The limits of design §6.2 "The limit". Each test below starts from the exact case of issue 19
// (a mask removes 1201 tokens, the rescue books 1255) and changes one fact. Each expects no
// interaction: a doubtful figure may only lower a saving (R45).
//
// The scenario: the earlier request wrote the client body (2569 tokens: 1255 system, 1214 for
// messages[0], 100 for messages[1]). The mask cuts messages[0] to 13, so on the rescued request the
// provider reads the 1255 system tokens the ping kept and writes the other 113. A reported mask is
// the one exception: its replay has the same forwarded prefix as the earlier request, so the
// provider reads 1368 and writes nothing. The rescue books 1255 in the first case.

type interactionCase struct {
	facts   EntryFacts
	bps     []Breakpoint
	op      Operation
	reports bool // the mask is reported on the earlier request and replayed (active) on this one
}

func exactInteraction() interactionCase {
	f := pingFacts()
	f.PingRead = ptr(msg(1))
	return interactionCase{facts: f, bps: bps5(sys(0), msg(1)),
		op: rewrite("op1", "mask", "mask:1", msg(0), 1214, 13)}
}

func (c interactionCase) run() Result {
	sysB := []blk{b("sys", 1255)}
	client := mk(nil, sysB, []blk{b("m0", 1214), b("m1", 100)})
	fwd := mk(nil, sysB, []blk{b("m0", 13), b("m1", 100)})
	ts := NewTrackers()
	first := req{at: t0.Add(20 * time.Minute), client: client, clientBPs: c.bps, facts: c.facts, usage: Usage{CacheWrite: 2569}}
	if c.reports {
		first.ops, first.fwd = []Operation{c.op}, fwd
		first.usage = Usage{CacheWrite: 1368}
	}
	first.run(ts)
	second := req{at: t0.Add(28 * time.Minute), client: client, clientBPs: c.bps, facts: c.facts, fwd: fwd,
		usage: Usage{CacheRead: 1255, CacheWrite: 113}}
	if c.reports {
		second.usage = Usage{CacheRead: 1368}
	} else {
		second.ops = []Operation{c.op}
	}
	return second.run(ts)
}

func wantNoInteraction(t *testing.T, res Result) {
	t.Helper()
	if res.Interaction != (TierTokens{}) || res.InteractionCaused != (TierTokens{}) {
		t.Fatalf("interaction %+v caused %+v, want none", res.Interaction, res.InteractionCaused)
	}
}

func TestTheInteractionBaseCaseHolds(t *testing.T) {
	res := exactInteraction().run()
	if res.Interaction != (TierTokens{CacheWrite5m: 1201}) || res.InteractionCaused != (TierTokens{CacheRead: 1201}) {
		t.Fatalf("interaction %+v caused %+v, want 1201 write -> read", res.Interaction, res.InteractionCaused)
	}
}

// The mask is before the ping's extent but past B*, so it is not priced at the read tier.
func TestNoInteractionWhenTheMaskIsPastTheBoundary(t *testing.T) {
	c := exactInteraction()
	c.bps = bps5(sys(0))
	res := c.run()
	if p := findOp(t, res, "op1"); p.Saved.CacheRead != 0 {
		t.Fatalf("setup: mask saved %+v, want it past B*", p.Saved)
	}
	wantNoInteraction(t, res)
}

// B* never lies past the ping's extent, so a mask after the extent is also past B*. The tier guard
// and the extent guard both keep it out; removing both makes this test fail.
func TestNoInteractionWhenTheMaskIsAfterThePingExtent(t *testing.T) {
	c := exactInteraction()
	c.facts.PingRead = ptr(sys(0))
	wantNoInteraction(t, c.run())
}

// A sibling kept sys[0] warm, so the rescue credits only the 255 tokens after it (R54). The rescue
// stays positive, so only the SharedWarm guard keeps the interaction out. The system has two blocks
// here so that the rescue is not zero.
func TestNoInteractionWhenASiblingKeptTheHeadWarm(t *testing.T) {
	c := exactInteraction()
	c.facts.SharedWarm = ptr(sys(0))
	c.bps = bps5(sys(1), msg(1))
	sysB := []blk{b("sys0", 1000), b("sys1", 255)}
	client := mk(nil, sysB, []blk{b("m0", 1214), b("m1", 100)})
	fwd := mk(nil, sysB, []blk{b("m0", 13), b("m1", 100)})
	ts := NewTrackers()
	req{at: t0.Add(20 * time.Minute), client: client, clientBPs: c.bps, facts: c.facts, usage: Usage{CacheWrite: 2569}}.run(ts)
	res := req{at: t0.Add(28 * time.Minute), client: client, clientBPs: c.bps, facts: c.facts, fwd: fwd,
		ops: []Operation{c.op}, usage: Usage{CacheRead: 1255, CacheWrite: 113}}.run(ts)
	noChecks(t, res)
	if e := effect(t, res, EffectRescue); e.Saved != (TierTokens{CacheWrite5m: 255}) {
		t.Fatalf("setup: rescue %+v, want 255 tokens", e.Saved)
	}
	wantNoInteraction(t, res)
}

// A DryRun mask is computed but not sent (R12): the forwarded body is the client body, and the
// provider reads all 2569 tokens. price still books the mask, so the DryRun guard is exercised.
func TestNoInteractionForADryRunMask(t *testing.T) {
	c := exactInteraction()
	c.op.DryRun = true
	sysB := []blk{b("sys", 1255)}
	client := mk(nil, sysB, []blk{b("m0", 1214), b("m1", 100)})
	ts := NewTrackers()
	req{at: t0.Add(20 * time.Minute), client: client, clientBPs: c.bps, facts: c.facts, usage: Usage{CacheWrite: 2569}}.run(ts)
	res := req{at: t0.Add(28 * time.Minute), client: client, clientBPs: c.bps, facts: c.facts, fwd: client,
		ops: []Operation{c.op}, usage: Usage{CacheRead: 2569}}.run(ts)
	noChecks(t, res)
	if p := findOp(t, res, "op1"); !p.DryRun || p.Saved == (TierTokens{}) {
		t.Fatalf("setup: dry-run mask %+v, want it priced", p)
	}
	wantNoInteraction(t, res)
}

// A 1h head entry read after the 5m TTL is a ttlRescue, not a ping rescue. There is no ping, so
// the library adds no interaction.
func TestNoInteractionOnATTLRescueWithNoPing(t *testing.T) {
	facts := EntryFacts{TTL: time.Hour, Lifetime: LifetimeExact}
	bps0 := []Breakpoint{{Pos: sys(0), TTL: "1h"}, {Pos: msg(0), TTL: "5m"}}
	bps1 := []Breakpoint{{Pos: sys(0), TTL: "1h"}, {Pos: msg(1), TTL: "5m"}}
	head := rewrite("op2", "headttl", "headttl:s1:sys", sys(0), 1255, 1255)
	mask := rewrite("op1", "mask", "mask:1", msg(0), 1214, 13)
	sysB := []blk{b("sys", 1255)}
	client0 := mk(nil, sysB, []blk{b("m0", 1214)})
	client := mk(nil, sysB, []blk{b("m0", 1214), b("m1", 100)})
	fwd0 := mk(nil, sysB, []blk{b("m0", 13)})
	fwd := mk(nil, sysB, []blk{b("m0", 13), b("m1", 100)})
	ts := NewTrackers()
	req{at: t0, client: client0, clientBPs: bps0, fwdBPs: bps0, facts: facts, ops: []Operation{head, mask}, fwd: fwd0,
		usage: Usage{CacheWrite: 1268, CacheWrite1h: 1255}}.run(ts)
	res := req{at: t0.Add(10 * time.Minute), client: client, clientBPs: bps1, fwdBPs: bps1, facts: facts,
		ops: []Operation{head, mask}, fwd: fwd, usage: Usage{CacheRead: 1255, CacheWrite: 113}}.run(ts)
	effect(t, res, EffectTTLRescue)
	wantNoInteraction(t, res)
}

// An active mask, replayed from an earlier request, is priced like a reported one (price loops the
// active items too). It removes the same 1201 tokens that a rescued request would have written
// cold, so the interaction is 1201, as for a reported mask.
func TestAnActiveMaskOnARescuedRequestHasTheInteraction(t *testing.T) {
	c := exactInteraction()
	c.reports = true
	res := c.run()
	if p := findOp(t, res, "active:mask:1"); p.Saved != (TierTokens{CacheRead: 1201}) {
		t.Fatalf("setup: active mask saved %+v", p.Saved)
	}
	if res.Interaction != (TierTokens{CacheWrite5m: 1201}) || res.InteractionCaused != (TierTokens{CacheRead: 1201}) {
		t.Fatalf("interaction %+v caused %+v, want 1201 write -> read", res.Interaction, res.InteractionCaused)
	}
}
