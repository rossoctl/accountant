package engine

import (
	"math"
	"strconv"
	"testing"
)

// overCap is the first count above the cap. On a 32-bit int no count can reach it.
func overCap(t *testing.T) int {
	t.Helper()
	if strconv.IntSize < 64 {
		t.Skip("a 32-bit int cannot hold a count above the cap")
	}
	capv := maxTierTokens
	v := capv + 1
	return int(v)
}

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
	over := overCap(t)
	for name, u := range map[string]Usage{
		"maxint":    {Input: math.MaxInt, CacheRead: math.MaxInt, CacheWrite: math.MaxInt},
		"overCap":   {Input: over},
		"cacheRead": {CacheRead: over},
		"write1h":   {CacheWrite: over, CacheWrite1h: over},
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
	if strconv.IntSize < 64 {
		t.Skip("a 32-bit int cannot exceed the int32 maximum")
	}
	big := int64(math.MaxInt32) + 5
	if clamp32(int(big)) != math.MaxInt32 || clamp32(7) != 7 {
		t.Fatal("clamp32 does not saturate")
	}
}

// A tier at the cap is accepted by the sign and cap rule (the body then decides: ratio_range here).
func TestUsageAtCapIsNotBadUsage(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("a 32-bit int cannot hold the cap")
	}
	capv := maxTierTokens
	at := int(capv)
	client := mk(nil, nil, []blk{b("a", 8000)})
	q := req{at: t0, client: client, clientBPs: bps5(msg(0)), facts: exact5m, usage: Usage{Input: at}}
	if res := q.run(NewTrackers()); hasUnpriced(res, ReasonBadUsage) {
		t.Fatalf("Unpriced = %+v, a tier at the cap is not bad_usage", res.Unpriced)
	}
}

// A too-big report replaces nothing: the active change stays priced and stays in the state (finding 1).
func TestSizeRangeReportKeepsActiveChange(t *testing.T) {
	sysb := []blk{b("sys", 2000)}
	m := msgs(4, 3000)
	mc, mf := append(append([]blk{}, m...), b("m4", 6000)), append(append([]blk{}, m...), b("m4f", 1000))
	ts := NewTrackers()
	req{at: t0, client: mk(nil, sysb, m), fwd: mk(nil, sysb, m), clientBPs: bps5(msg(3)), facts: exact5m, usage: Usage{CacheWrite: 14000}}.run(ts)
	good := rewrite("o1", "trim", "c1", msg(4), 6000, 1000)
	req{at: t0.Add(10), client: mk(nil, sysb, mc), fwd: mk(nil, sysb, mf), clientBPs: bps5(msg(4)), facts: exact5m,
		ops: []Operation{good}, usage: Usage{CacheRead: 14000, CacheWrite: 1000}}.run(ts)
	bad := rewrite("o2", "trim", "c1", msg(4), math.MaxInt/2, 0)
	mc2, mf2 := append(append([]blk{}, mc...), b("m5", 1000)), append(append([]blk{}, mf...), b("m5", 1000))
	res := req{at: t0.Add(20), client: mk(nil, sysb, mc2), fwd: mk(nil, sysb, mf2), clientBPs: bps5(msg(5)), facts: exact5m,
		ops: []Operation{bad}, usage: Usage{CacheRead: 15000, CacheWrite: 1000}}.run(ts)
	if !hasUnpriced(res, ReasonSizeRange) {
		t.Fatalf("Unpriced = %+v, want size_range", res.Unpriced)
	}
	if p := findOp(t, res, "active:c1"); tierSum(p.Saved) == 0 {
		t.Fatalf("active op not priced: %+v", p)
	}
	st := ts.tracker(ThreadKey("s1", Thread{ID: "main"}), t0).snapshot()
	if len(st.Active) != 1 || st.Active[0].SizeBefore.Bytes != 6000 {
		t.Fatalf("state keeps %+v, want the original c1", st.Active)
	}
}

// An operator after an inserting operator receives a body larger than the client body (finding 3).
func TestSizeRangeBoundIncludesInsertedBytes(t *testing.T) {
	client := mk(nil, nil, []blk{b("a", 4000), b("b", 4000)})
	insert := Operation{ID: "i", Kind: KindRewrite, Operator: "retrieve", ChangeID: "ci", At: msg(1),
		SizeBefore: Size{Bytes: 0}, SizeAfter: Size{Bytes: 100000}, MessageSplice: &MessageSplice{At: 1, Removed: 0, Inserted: 1}}
	run := func(before int) Result {
		down := rewrite("d", "trim", "cd", msg(1), before, 100)
		q := req{at: t0, client: client, clientBPs: bps5(msg(1)), facts: exact5m, ops: []Operation{insert, down}, usage: Usage{Input: 2000}}
		q.fwd = mk(nil, nil, []blk{b("a", 4000), b("ins", 100000), b("b", 4000)}) // the insertion stays in the body
		return q.run(NewTrackers())
	}
	if res := run(90000); hasUnpriced(res, ReasonSizeRange) {
		t.Fatalf("a report inside client+inserted must stay priced: %+v", res.Unpriced)
	}
	if res := run(8000 + 100000 + 1); !hasUnpriced(res, ReasonSizeRange) {
		t.Fatalf("a report above client+inserted must be size_range: %+v", res.Unpriced)
	}
}

// SizeAfter is bounded the same way, on the operation and on a span (finding 4).
func TestSizeAfterAboveLimitIsNotPriced(t *testing.T) {
	client := mk(nil, nil, []blk{b("a", 2000)})
	if strconv.IntSize < 64 {
		t.Skip("sizes above 2^32 need a 64-bit int")
	}
	hugeBytes, hugeTokens, bigBytes := int64(8_000_000_000_000), int64(16_000_000_000_000), int64(1)<<40
	op := rewrite("o1", "trim", "c1", msg(0), 2000, int(hugeBytes))
	op.SizeAfter.Tokens = int(hugeTokens)
	spanned := Operation{ID: "o2", Kind: KindRewrite, Operator: "trim", ChangeID: "c2", At: msg(0),
		Spans:      []Span{{Section: SectionMessages, From: 0, To: 0, SizeBefore: Size{Bytes: 2000}, SizeAfter: Size{Bytes: int(bigBytes)}}},
		SizeBefore: Size{Bytes: 2000}, SizeAfter: Size{Bytes: int(bigBytes)}}
	q := req{at: t0, client: client, clientBPs: bps5(msg(0)), facts: exact5m, ops: []Operation{op, spanned}, usage: Usage{Input: 2000}}
	res := q.run(NewTrackers())
	for _, id := range []string{"o1", "o2"} {
		if !hasUnpriced(res, ReasonSizeRange) || tierSum(findOp(t, res, id).Caused) != 0 {
			t.Fatalf("%s: Unpriced=%+v Caused=%+v", id, res.Unpriced, findOp(t, res, id).Caused)
		}
	}
}

// A DryRun insertion (observe mode) is priced as would-have-saved: only its SizeBefore is bounded.
func TestDryRunInsertionStaysPriced(t *testing.T) {
	client := mk(nil, nil, []blk{b("a", 1000), b("b", 1000)})
	dry := rewrite("d", "retrieve", "cd", msg(1), 0, 50000)
	dry.DryRun = true
	q := req{at: t0, client: client, clientBPs: bps5(msg(1)), facts: exact5m, ops: []Operation{dry}, usage: Usage{Input: 500}}
	res := q.run(NewTrackers())
	if hasUnpriced(res, ReasonSizeRange) {
		t.Fatalf("Unpriced = %+v, a DryRun SizeAfter is not bounded by check 13", res.Unpriced)
	}
	for _, c := range res.Checks {
		if c.Check == ReasonSizeRange {
			t.Fatalf("Checks = %+v", res.Checks)
		}
	}
	// its SizeBefore is still bounded
	bad := rewrite("d2", "retrieve", "cd2", msg(1), 50000, 0)
	bad.DryRun = true
	q.ops = []Operation{bad}
	if res := q.run(NewTrackers()); !hasUnpriced(res, ReasonSizeRange) {
		t.Fatalf("Unpriced = %+v, a DryRun SizeBefore above the bound is size_range", res.Unpriced)
	}
}

// A malformed re-report of an active insertion must not lower the bound for an honest downstream
// operator: the active insertion still counts in the sums.
func TestMalformedReReportKeepsInsertionInBound(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("sizes above 2^32 need a 64-bit int")
	}
	client := mk(nil, nil, []blk{b("a", 2000), b("b", 2000)})
	fwd := mk(nil, nil, []blk{b("a", 2000), b("ins", 100000), b("b", 2000)})
	ts := NewTrackers()
	ci := rewrite("i", "retrieve", "ci", msg(1), 0, 100000)
	ci.MessageSplice = &MessageSplice{At: 1, Removed: 0, Inserted: 1}
	req{at: t0, client: client, fwd: fwd, clientBPs: bps5(msg(1)), facts: exact5m, ops: []Operation{ci},
		usage: Usage{CacheWrite: 26000}}.run(ts)
	hugeBytes := int64(8_000_000_000_000)
	huge := int(hugeBytes)
	bad := rewrite("i2", "retrieve", "ci", msg(1), huge, 100000)
	o2 := rewrite("o2", "trim", "c2", msg(1), 100000, 20000)
	fwd2 := mk(nil, nil, []blk{b("a", 2000), b("ins", 20000), b("b", 2000)})
	res := req{at: t0.Add(10), client: client, fwd: fwd2, clientBPs: bps5(msg(1)), facts: exact5m,
		ops: []Operation{bad, o2}, usage: Usage{CacheRead: 1000, CacheWrite: 5000}}.run(ts)
	for _, u := range res.Unpriced {
		if u.OpID == "o2" {
			t.Fatalf("o2 is unpriced (%s): the bound lost the active insertion", u.Reason)
		}
	}
	if !hasUnpriced(res, ReasonSizeRange) {
		t.Fatalf("the huge re-report must be size_range: %+v", res.Unpriced)
	}
	st := ts.tracker(ThreadKey("s1", Thread{ID: "main"}), t0).snapshot()
	found := false
	for _, a := range st.Active {
		found = found || (a.ChangeID == "ci" && a.SizeAfter.Bytes == 100000)
	}
	if !found {
		t.Fatalf("state lost the active insertion: %+v", st.Active)
	}
}

// Merge check with media_tokens (#46): a report over the image that is also above the size bound is
// size_range (the report is a bug), not media_tokens. An honest operation next to it stays priced.
func TestSizeRangeWinsOverMediaTokens(t *testing.T) {
	img := imageBlock(2_000_000)
	client := [][]byte{text("m0", 2000), img, text("m2", 8000)}
	fwd := [][]byte{text("m0", 2000), img, text("m2", 8000)}
	touch := Operation{ID: "touch", Kind: KindRewrite, Operator: "x", ChangeID: "t", At: msg(1),
		SizeBefore: Size{Bytes: 50_000_000}, SizeAfter: Size{Bytes: len(img)}}
	cut := Operation{ID: "cut", Kind: KindRewrite, Operator: "mask", ChangeID: "c", At: msg(2),
		SizeBefore: Size{Bytes: 8000}, SizeAfter: Size{Bytes: 8000}}
	ts := NewTrackers()
	r := ts.BeginRequest(RequestMeta{RequestID: "r", Session: "s", At: t0}, NewBody(nil, nil, client), bps5(msg(2)))
	r.Add(touch)
	r.Add(cut)
	r.EndRequest(Thread{ID: "main"}, NewBody(nil, nil, fwd), bps5(msg(2)), exact5m)
	res := r.Compute(Usage{CacheWrite: 2350})
	if !hasUnpricedOp(res, "touch", ReasonSizeRange) || hasUnpricedOp(res, "touch", ReasonMediaTokens) {
		t.Fatalf("Unpriced = %+v, want touch size_range", res.Unpriced)
	}
	if hasUnpricedOp(res, "cut", ReasonSizeRange) {
		t.Fatalf("Unpriced = %+v, cut is honest", res.Unpriced)
	}
}

// A chain of honest reports must stay priced when a malformed re-report of an active insertion sits
// beside them. Client 32,000 bytes; active ci inserts 100,000. Request 1: bad (re-report of ci, wrong
// size), v inserts 90,000, d cuts 222,000 to 72,000. Forwarded = 32,000+100,000+90,000-150,000 = 72,000.
func TestHonestChainBesideMalformedReReport(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("sizes above 2^32 need a 64-bit int")
	}
	client := mk(nil, nil, []blk{b("a", 16000), b("b", 16000)})
	fwd0 := mk(nil, nil, []blk{b("a", 16000), b("ins", 100000), b("b", 16000)})
	ts := NewTrackers()
	ci := rewrite("i", "retrieve", "ci", msg(1), 0, 100000)
	ci.MessageSplice = &MessageSplice{At: 1, Removed: 0, Inserted: 1}
	req{at: t0, client: client, fwd: fwd0, clientBPs: bps5(msg(1)), facts: exact5m, ops: []Operation{ci},
		usage: Usage{CacheWrite: 33000}}.run(ts)
	hugeBytes := int64(8_000_000_000_000)
	bad := rewrite("bad", "retrieve", "ci", msg(1), int(hugeBytes), 100000)
	v := rewrite("v", "more", "cv", msg(1), 0, 90000)
	v.MessageSplice = &MessageSplice{At: 1, Removed: 0, Inserted: 1}
	d := rewrite("d", "trim", "cd", msg(1), 222000, 72000)
	fwd1 := mk(nil, nil, []blk{b("x", 72000)})
	res := req{at: t0.Add(10), client: client, fwd: fwd1, clientBPs: bps5(msg(1)), facts: exact5m,
		ops: []Operation{bad, v, d}, usage: Usage{CacheRead: 1000, CacheWrite: 17000}}.run(ts)
	if !hasUnpricedOp(res, "bad", ReasonSizeRange) {
		t.Fatalf("Unpriced = %+v, want bad size_range", res.Unpriced)
	}
	for _, id := range []string{"v", "d"} {
		if hasUnpricedOp(res, id, ReasonSizeRange) {
			t.Fatalf("%s is size_range: an honest report was dropped (Unpriced %+v)", id, res.Unpriced)
		}
	}
	for _, c := range res.Checks {
		if c.Check == CheckSum {
			t.Fatalf("sum check failed: %+v", res.Checks)
		}
	}
}
