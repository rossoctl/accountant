package engine

import (
	"math"
	"testing"
)

// Issue #44: input validation gaps.

// An operation whose size is above the client body is not priced, whatever its Tokens say.
func TestOpLargerThanClientBodyIsNotPriced(t *testing.T) {
	client := mk(nil, nil, []blk{b("a", 2000)})
	op := rewrite("o1", "trim", "c1", msg(0), math.MaxInt, 0)
	op.SizeBefore.Tokens = math.MaxInt
	q := req{at: t0, client: client, clientBPs: bps5(msg(0)), facts: exact5m, ops: []Operation{op}, usage: Usage{Input: 2000}}
	res := q.run(NewTrackers())
	if !hasUnpriced(res, ReasonSizeRange) {
		t.Fatalf("Unpriced = %+v, want size_range", res.Unpriced)
	}
	if p := findOp(t, res, "o1"); tierSum(p.Saved) != 0 {
		t.Fatalf("Saved = %+v, want none", p.Saved)
	}
}

// A usage tier above the cap is bad_usage, and the sum of tiers cannot wrap.
func TestHugeUsageIsBadUsage(t *testing.T) {
	for name, u := range map[string]Usage{
		"maxint":    {Input: math.MaxInt, CacheRead: math.MaxInt, CacheWrite: math.MaxInt},
		"overCap":   {Input: maxTierTokens + 1},
		"cacheRead": {CacheRead: maxTierTokens + 1},
		"write1h":   {CacheWrite: maxTierTokens + 1, CacheWrite1h: maxTierTokens + 1},
	} {
		client := mk(nil, nil, []blk{b("a", 8000)})
		q := req{at: t0, client: client, clientBPs: bps5(msg(0)), facts: exact5m, usage: u,
			ops: []Operation{rewrite("o1", "trim", "c1", msg(0), 100, 50)}}
		res := q.run(NewTrackers())
		if !hasUnpriced(res, ReasonBadUsage) {
			t.Errorf("%s: Unpriced = %+v, want bad_usage", name, res.Unpriced)
		}
	}
}

// A spend whose Call has a negative, NaN or infinite figure fails bad_usage; a valid one is unchanged.
func TestCallUsageValidated(t *testing.T) {
	bad := []CallUsage{
		{Usage: Usage{Input: 10}, Output: -1},
		{Usage: Usage{Input: 10}, CostUSD: -0.5},
		{Usage: Usage{Input: 10}, CostUSD: math.NaN()},
		{Usage: Usage{Input: 10}, CostUSD: math.Inf(1)},
		{Usage: Usage{Input: -10}},
	}
	for i, cu := range bad {
		ops := RequestOps{Ops: []Operation{{ID: "s", Kind: KindSpend, Call: &ModelCall{Model: "m", Usage: cu}}}}
		res := ComputeStandalone(ops)
		if !hasUnpriced(res, ReasonBadUsage) || len(res.Checks) != 1 || res.Checks[0].Check != ReasonBadUsage {
			t.Errorf("case %d: Unpriced=%+v Checks=%+v, want bad_usage", i, res.Unpriced, res.Checks)
		}
	}
	good := RequestOps{Ops: []Operation{{ID: "s", Kind: KindSpend, Call: &ModelCall{Model: "m",
		Usage: CallUsage{Usage: Usage{Input: 10}, Output: 5, CostUSD: 0.01}}}}}
	if res := ComputeStandalone(good); len(res.Unpriced) != 0 || len(res.Checks) != 0 {
		t.Fatalf("valid call: Unpriced=%+v Checks=%+v, want none", res.Unpriced, res.Checks)
	}
}

func TestCallUsageValidatedInRequest(t *testing.T) {
	client := mk(nil, nil, []blk{b("a", 2000)})
	sp := Operation{ID: "s", Kind: KindSpend, Call: &ModelCall{Model: "m", Usage: CallUsage{Usage: Usage{Input: 10}, CostUSD: math.NaN()}}}
	q := req{at: t0, client: client, clientBPs: bps5(msg(0)), facts: exact5m, ops: []Operation{sp}, usage: Usage{Input: 2000}}
	res := q.run(NewTrackers())
	if !hasUnpriced(res, ReasonBadUsage) {
		t.Fatalf("Unpriced = %+v, want bad_usage", res.Unpriced)
	}
}

func TestClamp32(t *testing.T) {
	if clamp32(math.MaxInt32+5) != math.MaxInt32 || clamp32(7) != 7 {
		t.Fatal("clamp32 does not saturate")
	}
}
