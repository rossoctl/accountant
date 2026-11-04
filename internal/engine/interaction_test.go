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
