package engine

import (
	"testing"
	"time"
)

// accountant#69: a model change that no operation reports is in the baseline in every row that adds a
// break, not only in row 4 (§5.2). The provider keeps a cache for each model, so the client body
// misses too.

// runModel is req.run with a model on the request.
func runModel(ts *Trackers, model string, q req) Result { return runModelKey(ts, model, "", q) }

// runModelKey is runModel with a ModelKey.
func runModelKey(ts *Trackers, model, key string, q req) Result {
	r := ts.BeginRequest(RequestMeta{RequestID: "r", Session: "s1", Model: model, ModelKey: key, At: q.at}, q.client, q.clientBPs)
	for _, op := range q.ops {
		r.Add(op)
	}
	fwd := q.fwd
	if fwd.Pos == nil {
		fwd = q.client
	}
	r.EndRequest(Thread{ID: "main"}, fwd, q.clientBPs, q.facts)
	return r.Compute(q.usage)
}

var msSys = []blk{b("sys", 1000)}

// msWarm sends a first request on model: the host masks messages[1] (2000 -> 100 bytes), and the
// cache is written.
func msWarm(t *testing.T, ts *Trackers, model string) {
	t.Helper()
	mask := rewrite("op1", "context-guru.mask", "cg:mask", msg(1), 2000, 100)
	noChecks(t, runModel(ts, model, req{at: t0, client: mk(nil, msSys, []blk{b("m0", 1000), b("m1", 2000)}),
		clientBPs: bps5(sys(0), msg(1)), ops: []Operation{mask},
		fwd: mk(nil, msSys, []blk{b("m0", 1000), b("k1", 100)}), facts: exact5m, usage: Usage{CacheWrite: 2100}}))
}

// msNext is the request 19 s later: the client appends m2, and the host does not apply the mask, so
// the forwarded body differs from the previous one at messages[1], inside the previous extent (row 1).
func msNext(ops ...Operation) req {
	return req{at: t0.Add(19 * time.Second), client: mk(nil, msSys, []blk{b("m0", 1000), b("m1", 2000), b("m2", 500)}),
		clientBPs: bps5(sys(0), msg(2)), ops: ops, facts: exact5m, usage: Usage{CacheWrite: 4500}}
}

func TestAModelChangeInRow1IsInTheBaseline(t *testing.T) {
	ts := NewTrackers()
	msWarm(t, ts, "claude-sonnet-4-5")
	br := effect(t, runModel(ts, "claude-opus-4-5", msNext()), EffectBreak)
	if !br.InBaseline || br.Cause != CauseModelChanged || br.ChangeID != "" || br.Saved.CacheRead == 0 {
		t.Fatalf("break %+v: want in the baseline, Cause %q, no ChangeID, moved tokens", br, CauseModelChanged)
	}
}

// The same request with no model change is the row 1 break of a change that ended: it is charged.
func TestRow1WithNoModelChangeIsStillCharged(t *testing.T) {
	ts := NewTrackers()
	msWarm(t, ts, "claude-sonnet-4-5")
	br := effect(t, runModel(ts, "claude-sonnet-4-5", msNext()), EffectBreak)
	if br.InBaseline || br.Cause != "" {
		t.Fatalf("break %+v: want a charged break with no cause", br)
	}
}

func msRoute(id, change, from, to string) Operation {
	return Operation{ID: id, Kind: KindSetting, Operator: "inference-router", ChangeID: change,
		Settings: map[string]SettingChange{"model": {From: &from, To: &to, At: ptr(sys(0))}}}
}

// A plugin that reports the model change caused the miss. Row 1 charges that plugin, as row 3 does,
// and not the rewrite that ended at the first difference.
func TestARoutedModelChangeInRow1IsChargedToTheRouter(t *testing.T) {
	from, to := "claude-sonnet-4-5", "claude-opus-4-5"
	ts := NewTrackers()
	msWarm(t, ts, from)
	res := runModel(ts, to, msNext(msRoute("op2", "rt:model", from, to)))
	br := effect(t, res, EffectBreak)
	if br.InBaseline || br.Cause != "" || br.ChangeID != "rt:model" || br.Saved.CacheRead == 0 {
		t.Fatalf("break %+v: want a charged break of rt:model, with moved tokens", br)
	}
}

// A router changed the model on an earlier request and is not reported now: the model goes back to
// the client's. That ended change caused the miss (Q12), so the break is not in the baseline.
func TestAnEndedRouterModelChangeIsNotInTheBaseline(t *testing.T) {
	small, large := "claude-haiku-4-5", "claude-sonnet-4-5"
	m := msgs(4, 8000)
	ts := NewTrackers()
	runModel(ts, small, req{at: t0, client: mk(nil, msSys, m), clientBPs: bps5(sys(0), msg(3)),
		ops: []Operation{msRoute("op1", "rt:model", large, small)}, facts: exact5m, usage: Usage{CacheWrite: 33000}})
	q := req{at: t0.Add(time.Minute), client: mk(nil, msSys, with(m, b("m4", 1000))), clientBPs: bps5(sys(0), msg(4)),
		facts: exact5m, usage: Usage{CacheWrite: 34000}}
	br := effect(t, runModel(ts, large, q), EffectBreak)
	if br.InBaseline || br.Cause != "" {
		t.Fatalf("break %+v: want a charged break with no cause", br)
	}
}

// A model setting that fails a check cannot be priced. It still keeps the break out of the
// baseline, so the doubtful figure lowers the saving and does not raise it (R45).
func TestABadModelSettingKeepsTheBreakOutOfTheBaseline(t *testing.T) {
	from, to := "claude-sonnet-4-5", "claude-opus-4-5"
	ts := NewTrackers()
	msWarm(t, ts, from)
	route := msRoute("op2", "rt:model", from, to)
	route.SizeBefore = Size{Bytes: -1} // a negative size: the operation cannot be priced
	res := runModel(ts, to, msNext(route))
	br := effect(t, res, EffectBreak)
	if br.InBaseline || br.Cause != "" || br.ChangeID != "" {
		t.Fatalf("break %+v: want a charged break with no cause and no ChangeID", br)
	}
	if !hasUnpricedOp(res, "op2", ReasonNegativeSize) {
		t.Fatalf("unpriced %+v: want op2 %s", res.Unpriced, ReasonNegativeSize)
	}
}

// Row 3: a plugin reports an effort change on the request where the client changes the model. The
// client body misses too, so the break is the model's, not the setting's.
func TestAModelChangeWinsOverAReportedParamChange(t *testing.T) {
	m := msgs(4, 8000)
	ts := NewTrackers()
	runModel(ts, "claude-sonnet-4-5", req{at: t0, client: mk(nil, msSys, m), clientBPs: bps5(sys(0), msg(3)),
		facts: exact5m, usage: Usage{CacheWrite: 33000}})
	hi := "high"
	op := Operation{ID: "op1", Kind: KindSetting, Operator: "router", ChangeID: "rt:effort",
		Settings: map[string]SettingChange{"effort": {To: &hi, At: ptr(sys(0))}}}
	q := req{at: t0.Add(time.Minute), client: mk(nil, msSys, with(m, b("m4", 1000))), clientBPs: bps5(sys(0), msg(4)),
		ops: []Operation{op}, facts: exact5m, usage: Usage{CacheWrite: 34000}}
	br := effect(t, runModel(ts, "claude-opus-4-5", q), EffectBreak)
	if !br.InBaseline || br.Cause != CauseModelChanged || br.ChangeID != "" {
		t.Fatalf("break %+v: want in the baseline, Cause %q, no ChangeID", br, CauseModelChanged)
	}
}

// msEffort is a setting operation that changes only effort.
func msEffort(id string) Operation {
	hi := "high"
	return Operation{ID: id, Kind: KindSetting, Operator: "router", ChangeID: "rt:effort",
		Settings: map[string]SettingChange{"effort": {To: &hi, At: ptr(sys(0))}}}
}

// msRun4 sends requests on equal bodies that only append, so a break is row 3 or row 4. Request k
// runs on models[k] with ops[k], and nothing is read after the first one.
func msRun4(t *testing.T, models []string, ops [][]Operation) Result {
	t.Helper()
	return msRunKeys(t, models, make([]string, len(models)), ops)
}

// msRunKeys is msRun4 with keys[k] as the ModelKey of request k.
func msRunKeys(t *testing.T, models, keys []string, ops [][]Operation) Result {
	t.Helper()
	m := msgs(4, 8000)
	ts := NewTrackers()
	var res Result
	for k, model := range models {
		u := Usage{CacheWrite: 33000 + 1000*k}
		res = runModelKey(ts, model, keys[k], req{at: t0.Add(time.Duration(k) * time.Minute), client: mk(nil, msSys, m),
			clientBPs: bps5(sys(0), msg(len(m)-1)), ops: ops[k], facts: exact5m, usage: u})
		m = with(m, b("n", 1000))
	}
	return res
}

// Only the request that ends a router setting is charged: a later model switch by the client is in
// the baseline again.
func TestAStaleRouterSettingDoesNotChargeALaterClientSwitch(t *testing.T) {
	small, large, opus := "claude-haiku-4-5", "claude-sonnet-4-5", "claude-opus-4-5"
	res := msRun4(t, []string{small, large, large, opus},
		[][]Operation{{msRoute("op1", "rt:model", large, small)}, nil, nil, nil})
	br := effect(t, res, EffectBreak)
	if !br.InBaseline || br.Cause != CauseModelChanged || br.ChangeID != "" {
		t.Fatalf("break %+v: want in the baseline, Cause %q, no ChangeID", br, CauseModelChanged)
	}
}

// accountant#80: the request that ends a router setting removes it from Active. Later, the client
// picks the router's To itself and goes back: the switch away from it is the client's, so it is in
// the baseline. Before the fix the setting stayed active, matched again and charged the break.
func TestAnEndedRouterSettingLeavesActive(t *testing.T) {
	small, large := "claude-haiku-4-5", "claude-sonnet-4-5"
	res := msRun4(t, []string{small, large, small, large},
		[][]Operation{{msRoute("op1", "rt:model", large, small)}, nil, nil, nil})
	br := effect(t, res, EffectBreak)
	if !br.InBaseline || br.Cause != CauseModelChanged || br.ChangeID != "" {
		t.Fatalf("break %+v: want in the baseline, Cause %q, no ChangeID", br, CauseModelChanged)
	}
}

// A request with no model does not keep an ended router setting in Active (accountant#80, round 1).
// Request 3 runs on large, so the setting ends there, and the client's later switch away from small
// is in the baseline.
func TestARequestWithNoModelDoesNotKeepARouterSetting(t *testing.T) {
	small, large := "claude-haiku-4-5", "claude-sonnet-4-5"
	res := msRun4(t, []string{small, "", large, small, large},
		[][]Operation{{msRoute("op1", "rt:model", large, small)}, nil, nil, nil, nil})
	br := effect(t, res, EffectBreak)
	if !br.InBaseline || br.Cause != CauseModelChanged || br.ChangeID != "" {
		t.Fatalf("break %+v: want in the baseline, Cause %q, no ChangeID", br, CauseModelChanged)
	}
}

// A new spelling of the router's model, with the same ModelKey, is not a model change, so it does not
// end the setting (accountant#80, round 1). The router stops on request 4: that break counts.
func TestANewSpellingOfTheModelDoesNotEndARouterSetting(t *testing.T) {
	small, large := "claude-haiku-4-5", "claude-sonnet-4-5"
	res := msRunKeys(t, []string{small, "anthropic/" + small, small, large},
		[]string{"haiku", "haiku", "haiku", "sonnet"},
		[][]Operation{{msRoute("op1", "rt:model", large, small)}, nil, nil, nil})
	br := effect(t, res, EffectBreak)
	if br.InBaseline || br.Cause != "" || br.ChangeID != "" {
		t.Fatalf("break %+v: want a charged break with no cause and no ChangeID", br)
	}
}

// Row 3 with a model change: the operations decide who pays, not the first reported setting.
func TestRow3WithAModelChangeChargesTheModelOperation(t *testing.T) {
	small, large := "claude-haiku-4-5", "claude-sonnet-4-5"
	bad := msRoute("op2", "rt:model", large, small)
	bad.SizeBefore = Size{Bytes: -1}
	for _, c := range []struct {
		name       string
		models     []string
		ops        [][]Operation
		wantChange string
	}{
		{"effort, then model: the model operation pays", []string{large, small},
			[][]Operation{nil, {msEffort("op1"), msRoute("op2", "rt:model", large, small)}}, "rt:model"},
		{"the router ends, and effort is reported: the ended change pays", []string{small, large},
			[][]Operation{{msRoute("op1", "rt:model", large, small)}, {msEffort("op2")}}, ""},
		{"a bad model setting and a good effort: nobody is named", []string{large, small},
			[][]Operation{nil, {msEffort("op1"), bad}}, ""},
	} {
		br := effect(t, msRun4(t, c.models, c.ops), EffectBreak)
		if br.InBaseline || br.Cause != "" || br.ChangeID != c.wantChange {
			t.Errorf("%s: break %+v, want a charged break of %q", c.name, br, c.wantChange)
		}
	}
}

// Row 1 with an ended router and a live rewrite at the first difference: the rewrite did not change
// the model, so it is not named. The ended router change pays, with no ChangeID.
func TestRow1DoesNotNameALiveRewriteForAModelChange(t *testing.T) {
	small, large := "claude-haiku-4-5", "claude-sonnet-4-5"
	m := []blk{b("m0", 1000), b("m1", 2000)}
	ts := NewTrackers()
	noChecks(t, runModel(ts, small, req{at: t0, client: mk(nil, msSys, m), clientBPs: bps5(sys(0), msg(1)),
		ops: []Operation{msRoute("op1", "rt:model", large, small)}, facts: exact5m, usage: Usage{CacheWrite: 4000}}))
	mask := rewrite("op2", "context-guru.mask", "cg:mask", msg(1), 2000, 100)
	res := runModel(ts, large, req{at: t0.Add(19 * time.Second), client: mk(nil, msSys, with(m, b("m2", 500))),
		clientBPs: bps5(sys(0), msg(2)), ops: []Operation{mask},
		fwd: mk(nil, msSys, []blk{b("m0", 1000), b("k1", 100), b("m2", 500)}), facts: exact5m, usage: Usage{CacheWrite: 2600}})
	br := effect(t, res, EffectBreak)
	if br.InBaseline || br.Cause != "" || br.ChangeID != "" {
		t.Fatalf("break %+v: want a charged break with no ChangeID and no cause", br)
	}
}
