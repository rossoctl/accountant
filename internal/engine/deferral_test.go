package engine

import (
	"testing"
	"time"
)

// A host that defers tools with defer_loading leaves each definition in the body it forwards. The
// provider keeps a deferred tool out of the prompt, so the provider bills a stub for it. The host
// describes that by building the forwarded Body as the prompt the provider bills: a deferred tool is
// the size of its stub. The deferral is then an ordinary shrink of a tools position, and the library
// prices it with no new field (accountant#60).
func deferralBodies() (tools, fwdTools []blk, op Operation) {
	tools = []blk{b("t0", 1000), b("t1", 3000), b("t2", 2000)}
	fwdTools = []blk{b("t0", 1000), b("t1", 40), b("t2", 40)} // t1 and t2 are deferred: the bill holds a stub
	op = Operation{ID: "op1", Kind: KindRewrite, Operator: "tool-defer", ChangeID: "td:set-1", At: tool(1),
		Spans: []Span{
			{Section: SectionTools, From: 1, To: 1, SizeBefore: Size{Bytes: 3000}, SizeAfter: Size{Bytes: 40}},
			{Section: SectionTools, From: 2, To: 2, SizeBefore: Size{Bytes: 2000}, SizeAfter: Size{Bytes: 40}}},
		SizeBefore: Size{Bytes: 5000}, SizeAfter: Size{Bytes: 80}}
	return tools, fwdTools, op
}

func TestADeferralIsPricedAsAShrinkOfTheDeferredTools(t *testing.T) {
	tools, fwdTools, op := deferralBodies()
	sysB := []blk{b("sys", 3000)}
	ts := NewTrackers()
	run := func(at time.Time, n int, usage Usage, ops ...Operation) Result {
		m := msgs(n, 1000)
		return req{at: at, client: mk(tools, sysB, m), clientBPs: bps5(sys(0), msg(n-1)), ops: ops,
			fwd: mk(fwdTools, sysB, m), fwdBPs: bps5(sys(0), msg(n-1)), facts: exact5m, usage: usage}.run(ts)
	}
	// The deferred tools save 5,000 - 80 = 4,920 tokens. The first request has nothing cached, so the
	// saving is a write.
	res := run(t0, 1, Usage{CacheWrite: 5080}, op)
	if p := findOp(t, res, "op1"); p.Saved != (TierTokens{CacheWrite5m: 4920}) || p.Caused != (TierTokens{}) {
		t.Fatalf("first request: %+v, want 4920 written and nothing caused", p)
	}
	noChecks(t, res) // the sum check holds: the span bytes equal the body difference
	// The tools come first in the prefix, so a hot request reads the saving.
	res = run(t0.Add(time.Minute), 2, Usage{CacheRead: 5080, CacheWrite: 1000})
	if p := findOp(t, res, "active:td:set-1"); p.Saved != (TierTokens{CacheRead: 4920}) {
		t.Fatalf("hot request: %+v, want 4920 read", p)
	}
	noChecks(t, res) // conservation holds: the bill difference is the saving
	if len(res.CacheEffects) != 0 {
		t.Fatalf("CacheEffects = %+v, want none: the forwarded prefix did not change", res.CacheEffects)
	}
	// After the entry expires the saving is written again.
	res = run(t0.Add(30*time.Minute), 3, Usage{CacheWrite: 7080})
	if p := findOp(t, res, "active:td:set-1"); p.Saved != (TierTokens{CacheWrite5m: 4920}) {
		t.Fatalf("cold request: %+v, want 4920 written", p)
	}
	noChecks(t, res)
}

// A host that reports the deferral but builds the forwarded Body from the wire bytes, so the deferred
// tools keep their full size, books a saving the provider never gave. The sum check catches it.
func TestADeferralWithTheWireBodyFailsTheSumCheck(t *testing.T) {
	tools, _, op := deferralBodies()
	sysB := []blk{b("sys", 3000)}
	m := msgs(1, 1000)
	res := req{at: t0, client: mk(tools, sysB, m), clientBPs: bps5(sys(0), msg(0)), ops: []Operation{op},
		fwd: mk(tools, sysB, m), fwdBPs: bps5(sys(0), msg(0)), facts: exact5m, usage: Usage{CacheWrite: 1000 + 6000 + 3000}}.run(NewTrackers())
	if len(res.Checks) == 0 || res.Checks[0].Check != CheckSum {
		t.Fatalf("Checks = %+v, want a sum failure: the op removes 4920 bytes the bodies still hold", res.Checks)
	}
}

// A deferral with no ChangeID cannot repeat on later requests, so the library does not price it as a
// change (R22); the same input as any other rewrite.
func TestADeferralWithoutAChangeIDIsNotCarriedForward(t *testing.T) {
	tools, fwdTools, op := deferralBodies()
	op.ChangeID = ""
	sysB := []blk{b("sys", 3000)}
	ts := NewTrackers()
	m := msgs(1, 1000)
	req{at: t0, client: mk(tools, sysB, m), clientBPs: bps5(sys(0), msg(0)), ops: []Operation{op},
		fwd: mk(fwdTools, sysB, m), fwdBPs: bps5(sys(0), msg(0)), facts: exact5m, usage: Usage{CacheWrite: 5080}}.run(ts)
	m = msgs(2, 1000)
	res := req{at: t0.Add(time.Minute), client: mk(tools, sysB, m), clientBPs: bps5(sys(0), msg(1)),
		fwd: mk(fwdTools, sysB, m), fwdBPs: bps5(sys(0), msg(1)), facts: exact5m, usage: Usage{CacheRead: 5080, CacheWrite: 1000}}.run(ts)
	for _, p := range res.Ops {
		if p.Operator == "tool-defer" {
			t.Fatalf("second request carries %+v: an op with no ChangeID must not repeat", p)
		}
	}
}
