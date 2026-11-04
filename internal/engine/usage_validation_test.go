package engine

import (
	"testing"
	"time"
)

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

// Design §6.5: the reason is the first check that fails, in the order 8, 9, 10. A request with 8 breakpoints
// and 20 tokens per byte fails check 9 first.
func TestTheReasonIsTheFirstCheckThatFails(t *testing.T) {
	var many []Breakpoint
	for i := range 8 {
		many = append(many, bp5(msg(i)))
	}
	body := mk(nil, nil, msgs(8, 100)) // 800 bytes
	res := req{at: t0, client: body, clientBPs: many, facts: exact5m, usage: Usage{CacheRead: 16000}}.run(NewTrackers())
	if !hasCheck(res, ReasonRatioRange) || hasCheck(res, ReasonBreakpoints) {
		t.Fatalf("Checks = %+v, want ratio_range only", res.Checks)
	}
}

// A usage with only CacheWrite1h is malformed (it is part of CacheWrite, never an addition). An empty body
// with billed tokens has an infinite ratio, which is outside the range too.
func TestAMalformedOrInfiniteUsageIsRefused(t *testing.T) {
	body := mk(nil, nil, []blk{b("a", 10000)})
	res := req{at: t0, client: body, clientBPs: bps5(msg(0)), facts: exact5m, usage: Usage{CacheWrite1h: 500}}.run(NewTrackers())
	if !hasCheck(res, ReasonBadUsage) {
		t.Fatalf("1h-only usage: Checks = %+v, want bad_usage (CacheWrite1h is part of CacheWrite)", res.Checks)
	}
	res = req{at: t0, client: mk(nil, nil, nil), facts: exact5m, usage: Usage{Input: 500}}.run(NewTrackers())
	if !hasCheck(res, ReasonRatioRange) {
		t.Fatalf("empty body: Checks = %+v, want ratio_range", res.Checks)
	}
}

// After a failed check the next request has an unknown cache state, as after a response with no usage (§6.5).
func TestTheRequestAfterAFailedCheckIsUnknown(t *testing.T) {
	ts := NewTrackers()
	body := mk(nil, nil, []blk{b("a", 5000)})
	req{at: t0, client: body, clientBPs: bps5(msg(0)), facts: exact5m, usage: Usage{CacheRead: 100000}}.run(ts)
	res := req{at: t0.Add(time.Minute), client: body, clientBPs: bps5(msg(0)), facts: exact5m,
		usage: Usage{Input: 5000}}.run(ts)
	if res.Phase != PhaseUnknown {
		t.Fatalf("phase %q, want unknown after a failed check", res.Phase)
	}
}

func hasCheck(res Result, name string) bool {
	for _, c := range res.Checks {
		if c.Check == name {
			return true
		}
	}
	return false
}
