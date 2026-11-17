package engine

import (
	"testing"
	"time"
)

// accountant#69: a model change that no operation reports is in the baseline in every row that adds a
// break, not only in row 4 (§5.2). The provider keeps a cache for each model, so the client body
// misses too.

// runModel is req.run with a model on the request.
func runModel(ts *Trackers, model string, q req) Result {
	r := ts.BeginRequest(RequestMeta{RequestID: "r", Session: "s1", Model: model, At: q.at}, q.client, q.clientBPs)
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

// A plugin that reports the model change caused the miss: the break is not in the baseline.
func TestARoutedModelChangeIsNotInTheBaseline(t *testing.T) {
	from, to := "claude-sonnet-4-5", "claude-opus-4-5"
	route := Operation{ID: "op2", Kind: KindSetting, Operator: "inference-router", ChangeID: "rt:model",
		Settings: map[string]SettingChange{"model": {From: &from, To: &to, At: ptr(sys(0))}}}
	ts := NewTrackers()
	msWarm(t, ts, from)
	br := effect(t, runModel(ts, to, msNext(route)), EffectBreak)
	if br.InBaseline || br.Cause != "" {
		t.Fatalf("break %+v: want a charged break with no cause", br)
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
