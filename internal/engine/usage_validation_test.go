package engine

import "testing"

// Issue #12: usage that does not fit the body must not book a saving.

// 5000 forwarded bytes, a 3000 byte cut, usage of 100000 tokens: 20 tokens per byte.
func TestRatioOutOfRangePricesNothing(t *testing.T) {
	client := mk(nil, nil, []blk{b("a", 8000)})
	fwd := mk(nil, nil, []blk{b("a", 5000)})
	q := req{at: t0, client: client, fwd: fwd, clientBPs: bps5(msg(0)), fwdBPs: bps5(msg(0)), facts: exact5m,
		ops: []Operation{rewrite("o1", "trim", "c1", msg(0), 8000, 5000)}, usage: Usage{CacheRead: 100000}}
	res := q.run(NewTrackers())
	if !hasUnpriced(res, ReasonRatioRange) {
		t.Fatalf("Unpriced = %+v, want ratio_range", res.Unpriced)
	}
	if p := findOp(t, res, "o1"); tierSum(p.Saved) != 0 {
		t.Fatalf("Saved = %+v, want none", p.Saved)
	}
	if len(res.Checks) == 0 || res.Checks[len(res.Checks)-1].Check != ReasonRatioRange {
		t.Fatalf("Checks = %+v, want ratio_range", res.Checks)
	}
}

func TestNegativeUsagePricesNothing(t *testing.T) {
	client := mk(nil, nil, []blk{b("a", 8000)})
	fwd := mk(nil, nil, []blk{b("a", 5000)})
	q := req{at: t0, client: client, fwd: fwd, clientBPs: bps5(msg(0)), fwdBPs: bps5(msg(0)), facts: exact5m,
		ops: []Operation{rewrite("o1", "trim", "c1", msg(0), 8000, 5000)}, usage: Usage{CacheRead: -4000, Input: 9000}}
	res := q.run(NewTrackers())
	if !hasUnpriced(res, ReasonBadUsage) {
		t.Fatalf("Unpriced = %+v, want bad_usage", res.Unpriced)
	}
	if p := findOp(t, res, "o1"); tierSum(p.Saved) != 0 {
		t.Fatalf("Saved = %+v, want none", p.Saved)
	}
}

func TestBreakpointLimitsPricesNothing(t *testing.T) {
	body := mk(nil, nil, msgs(8, 100))
	var many []Breakpoint
	for i := range 8 {
		many = append(many, bp5(msg(i)))
	}
	for name, bps := range map[string][]Breakpoint{
		"eight":      many,
		"unknownTTL": {{Pos: msg(7), TTL: "10m"}},
	} {
		q := req{at: t0, client: body, clientBPs: bps, facts: exact5m, usage: Usage{Input: 800},
			ops: []Operation{rewrite("o1", "trim", "c1", msg(0), 100, 50)}}
		res := q.run(NewTrackers())
		if !hasUnpriced(res, ReasonBreakpoints) {
			t.Fatalf("%s: Unpriced = %+v, want breakpoints", name, res.Unpriced)
		}
	}
}

func TestFourBreakpointsAndEmptyTTLAreValid(t *testing.T) {
	body := mk(nil, nil, msgs(8, 100))
	bps := []Breakpoint{bp5(msg(1)), bp5(msg(3)), {Pos: msg(5)}, {Pos: msg(7), TTL: "1h"}}
	q := req{at: t0, client: body, clientBPs: bps, facts: exact5m, usage: Usage{CacheWrite: 800}}
	res := q.run(NewTrackers())
	if hasUnpriced(res, ReasonBreakpoints) || hasUnpriced(res, ReasonBadUsage) || hasUnpriced(res, ReasonRatioRange) || len(res.Checks) != 0 {
		t.Fatalf("Unpriced=%+v Checks=%+v, want none", res.Unpriced, res.Checks)
	}
}
