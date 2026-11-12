package engine

import (
	"testing"
	"time"
)

// accountant#70, cause B: a tail mark and a tool deferral rewrite messages[1]. On the next request the
// deferral is reported again with the same ChangeID, and the mark is not. The re-reported deferral
// replaces the active one, but the mark stays active.
func TestCauseBStaleTailMark(t *testing.T) {
	ts := NewTrackers()
	client := with(msgs(3, 3000))
	client[1] = b("c1", 17073)
	fwd := func(n int) Body {
		out := append([]blk(nil), client...)
		out[1] = b("f1", n)
		return mk(nil, ex2Sys, out)
	}
	mark := rewrite("mark", "tailbreakpoint", "cg:tailmark", msg(1), 17073, 17145)
	defA := rewrite("defA", "tooldefer", "cg:tooldefer:msg-rewritten", msg(1), 17145, 17516)
	defB := rewrite("defB", "tooldefer", "cg:tooldefer:msg-rewritten", msg(1), 17073, 17454)
	r := []req{
		{at: t0, client: mk(nil, ex2Sys, client), clientBPs: bps5(sys(0), msg(2)), facts: exact5m, usage: Usage{CacheWrite: 30000}},
		{at: t0.Add(10 * time.Second), client: mk(nil, ex2Sys, client), clientBPs: bps5(sys(0), msg(2)),
			ops: []Operation{mark, defA}, fwd: fwd(17516), facts: exact5m, usage: Usage{CacheWrite: 30000}},
		{at: t0.Add(20 * time.Second), client: mk(nil, ex2Sys, client), clientBPs: bps5(sys(0), msg(2)),
			ops: []Operation{defB}, fwd: fwd(17454), facts: exact5m, usage: Usage{CacheRead: 30000}},
		{at: t0.Add(30 * time.Second), client: mk(nil, ex2Sys, client), clientBPs: bps5(sys(0), msg(2)),
			fwd: fwd(17454), facts: exact5m, usage: Usage{CacheRead: 30000}},
	}
	r[0].run(ts)
	noSumFailure(t, r[1].run(ts))
	res := r[2].run(ts)
	t.Logf("r2 checks %+v active %v", res.Checks, activeIDs(ts))
	noSumFailure(t, res)
	res = r[3].run(ts)
	t.Logf("r3 checks %+v active %v", res.Checks, activeIDs(ts))
	noSumFailure(t, res)
}
