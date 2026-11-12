package engine

import (
	"testing"
	"time"
)

// accountant#70, cause B: a tail mark and a tool deferral rewrite messages[1]. On the next request the
// deferral is reported again with the same ChangeID, and the mark is not. The re-reported deferral
// replaces the active one, but the mark stays active.
func TestCauseBStaleTailMarkSameChangeID(t *testing.T) {
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

// accountant#70, cause B in full. The host keys a deferral's ChangeID on a hash of the message it read
// (context-guru proxy/accountant_envelope.go, messageOps). When the tail mark moves, the deferral reads
// another message, gets another ChangeID, and does not replace the old one. Three changes stay active at
// messages[1]: the mark, the old deferral and the new one. The sum check is off by 443 bytes, the
// 72 of the mark and the 371 of the old deferral, as on the hosted proxy.
func TestCauseBStaleMarkAndStaleDeferral(t *testing.T) {
	ts := NewTrackers()
	client := with(msgs(3, 3000))
	client[1] = b("c1", 17073)
	fwd := func(n int) Body {
		out := append([]blk(nil), client...)
		out[1] = b("f1", n)
		return mk(nil, ex2Sys, out)
	}
	mark := rewrite("mark", "tailbreakpoint", "cg:tailmark", msg(1), 17073, 17145)
	defA := rewrite("defA", "tooldefer", "cg:tooldefer:msg-rewritten:1:aaaa", msg(1), 17145, 17516)
	defB := rewrite("defB", "tooldefer", "cg:tooldefer:msg-rewritten:1:bbbb", msg(1), 17073, 17454)
	step := func(i int, size int, ops ...Operation) req {
		return req{at: t0.Add(time.Duration(i) * 10 * time.Second), client: mk(nil, ex2Sys, client),
			clientBPs: bps5(sys(0), msg(2)), ops: ops, fwd: fwd(size), facts: exact5m, usage: Usage{CacheWrite: 30000}}
	}
	req{at: t0, client: mk(nil, ex2Sys, client), clientBPs: bps5(sys(0), msg(2)), facts: exact5m,
		usage: Usage{CacheWrite: 30000}}.run(ts)
	noSumFailure(t, step(1, 17516, mark, defA).run(ts))
	res := step(2, 17454, defB).run(ts)
	noChecks(t, res)
	if got := activeIDs(ts); len(got) != 1 || got[0] != "cg:tooldefer:msg-rewritten:1:bbbb" {
		t.Fatalf("Active = %v, want only the new deferral", got)
	}
	noChecks(t, step(3, 17454).run(ts))
}
