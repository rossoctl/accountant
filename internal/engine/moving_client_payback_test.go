package engine

import (
	"testing"
	"time"
)

// A client that moves its own breakpoint to the newest message on every turn, as Claude Code does.
//
// Request 0: the client marks msg(1); the host adds a mark at msg(2). The provider writes 3000. The client
// body would write 2000 and bill msg(2)'s 1000 as input, so the host pays a markPremium on 1000 tokens.
//
// Request 1: the client appends m3 and marks it. The provider reads 3000 and writes 100. The client body has
// no entry at msg(2), so it would read 2000 and WRITE 1100. msg(2)'s 1000 tokens move from a 5m write to a
// read: the markPayback for request 0's premium. Before the fix B* landed on the host's mark, and the request
// booked nothing.
func TestPaybackWhenTheClientWouldHaveWrittenTheTail(t *testing.T) {
	ts := NewTrackers()
	client := []blk{b("m0", 1000), b("m1", 1000), b("m2", 1000)}
	r0 := req{at: t0, client: mk(nil, nil, client), clientBPs: bps5(msg(1)), fwdBPs: bps5(msg(1), msg(2)),
		facts: exact5m, usage: Usage{CacheWrite: 3000}}.run(ts)
	if e := effect(t, r0, EffectMarkPremium); e.Saved != (TierTokens{Input: 1000}) || e.Caused != (TierTokens{CacheWrite5m: 1000}) {
		t.Fatalf("request 0 premium = %+v, want 1000 input -> 5m write", e)
	}
	c1 := append(append([]blk(nil), client...), b("m3", 100))
	r1 := req{at: t0.Add(40 * time.Second), client: mk(nil, nil, c1), clientBPs: bps5(msg(3)), fwd: mk(nil, nil, c1),
		fwdBPs: bps5(msg(3)), facts: exact5m, usage: Usage{CacheRead: 3000, CacheWrite: 100}}.run(ts)
	if r1.Boundary == nil || *r1.Boundary != msg(1) {
		t.Fatalf("B* = %v, want msg(1): the client body has no entry at the host's mark", r1.Boundary)
	}
	e := effect(t, r1, EffectMarkPayback)
	if e.Saved != (TierTokens{CacheWrite5m: 1000}) || e.Caused != (TierTokens{CacheRead: 1000}) {
		t.Fatalf("payback = %+v, want 1000 5m write -> read", e)
	}
	noChecks(t, r1)
}

// The same at 1h: the client body would write msg(2) at its own breakpoint's TTL, so the payback moves a 1h
// write to a read.
func TestPaybackMovesTheClientWriteAtItsTTL(t *testing.T) {
	ts := NewTrackers()
	client := []blk{b("m0", 1000), b("m1", 1000), b("m2", 1000)}
	h := func(at ...Position) []Breakpoint {
		var out []Breakpoint
		for _, p := range at {
			out = append(out, bpTTL(p, "1h"))
		}
		return out
	}
	req{at: t0, client: mk(nil, nil, client), clientBPs: h(msg(1)), fwdBPs: h(msg(1), msg(2)),
		facts: ttlFacts(time.Hour), usage: Usage{CacheWrite: 3000, CacheWrite1h: 3000}}.run(ts)
	c1 := append(append([]blk(nil), client...), b("m3", 100))
	r1 := req{at: t0.Add(40 * time.Second), client: mk(nil, nil, c1), clientBPs: h(msg(3)), fwd: mk(nil, nil, c1),
		fwdBPs: h(msg(3)), facts: ttlFacts(time.Hour), usage: Usage{CacheRead: 3000, CacheWrite: 100, CacheWrite1h: 100}}.run(ts)
	e := effect(t, r1, EffectMarkPayback)
	if e.Saved != (TierTokens{CacheWrite1h: 1000}) || e.Caused != (TierTokens{CacheRead: 1000}) {
		t.Fatalf("payback = %+v, want 1000 1h write -> read", e)
	}
	noChecks(t, r1)
}

// Review of #109. The host hashes cache_control, so moving the client's mark off msg(1) changes msg(1)'s hash:
// clientFD is 1. The provider reads the whole extent, so the full-read rule sets B* to the extent. That must be
// the client's extent, msg(1), not the host's mark at msg(2), or no position is after B* and no payback is booked.
func TestPaybackWhenTheMovedMarkChangesTheHash(t *testing.T) {
	ts := NewTrackers()
	client := []blk{b("m0", 1000), b("m1", 1000), b("m2", 1000)}
	req{at: t0, client: mk(nil, nil, client), clientBPs: bps5(msg(1)), fwdBPs: bps5(msg(1), msg(2)),
		facts: exact5m, usage: Usage{CacheWrite: 3000}}.run(ts)
	c1 := []blk{b("m0", 1000), b("m1-unmarked", 1000), b("m2", 1000), b("m3", 100)}
	r1 := req{at: t0.Add(40 * time.Second), client: mk(nil, nil, c1), clientBPs: bps5(msg(3)), fwd: mk(nil, nil, c1),
		fwdBPs: bps5(msg(3)), facts: exact5m, usage: Usage{CacheRead: 3000, CacheWrite: 100}}.run(ts)
	if r1.Boundary == nil || *r1.Boundary != msg(1) {
		t.Fatalf("B* = %v, want msg(1): the full read proves no content changed, not that the client body had the host's entry", r1.Boundary)
	}
	e := effect(t, r1, EffectMarkPayback)
	if e.Saved != (TierTokens{CacheWrite5m: 1000}) || e.Caused != (TierTokens{CacheRead: 1000}) {
		t.Fatalf("payback = %+v, want 1000 5m write -> read", e)
	}
	noChecks(t, r1)
}

// Both moves on one request. Request 0: the client marks msg(1), the host marks msg(3). Request 1: the client
// marks msg(1) and msg(2), the host marks the new tail msg(4). The provider reads the host's 4000. The client body
// has its entry at msg(1): it would write msg(2) at its msg(2) mark and bill msg(3) as input. So msg(2) moves
// from a 5m write to a read, and msg(3) from input to a read.
func TestPaybackBooksTheInputAndTheWriteMove(t *testing.T) {
	ts := NewTrackers()
	client := []blk{b("m0", 1000), b("m1", 1000), b("m2", 1000), b("m3", 1000)}
	req{at: t0, client: mk(nil, nil, client), clientBPs: bps5(msg(1)), fwdBPs: bps5(msg(1), msg(3)),
		facts: exact5m, usage: Usage{CacheWrite: 4000}}.run(ts)
	c1 := with(client, b("m4", 100))
	r1 := req{at: t0.Add(40 * time.Second), client: mk(nil, nil, c1), clientBPs: bps5(msg(1), msg(2)), fwd: mk(nil, nil, c1),
		fwdBPs: bps5(msg(1), msg(2), msg(4)), facts: exact5m, usage: Usage{CacheRead: 4000, CacheWrite: 100}}.run(ts)
	e := effect(t, r1, EffectMarkPayback)
	if e.Saved != (TierTokens{Input: 1000, CacheWrite5m: 1000}) || e.Caused != (TierTokens{CacheRead: 2000}) {
		t.Fatalf("payback = %+v, want 1000 input and 1000 5m write -> 2000 read", e)
	}
}
