package engine

import (
	"testing"
	"time"
)

// accountant#70, cause A: operator X rewrites message M in place, then operator Y rewrites M from the same
// size. X is never reported as ended, and the forwarded item is neither the client's size nor X's SizeAfter,
// so no end rule fires. Both stay active, the sum check counts both savings, and it fails for the rest of
// the session.
func TestAnInPlaceChangeTakenOverByAnotherOperatorEnds(t *testing.T) {
	ts := NewTrackers()
	client := with(msgs(9, 3000), b("m9", 3000))
	client[2] = b("c2", 5302)
	opX := rewrite("opX", "context-guru.extract", "cg:extract:m2", msg(2), 5302, 5148)
	opY := rewrite("opY", "context-guru.extract_llm_sweep", "cg:sweep:m2", msg(2), 5302, 299)
	fwd := func(size int) []blk {
		out := append([]blk(nil), client...)
		out[2] = b("f2", size)
		return out
	}
	reqs := []req{
		{at: t0, client: mk(nil, ex2Sys, client), clientBPs: bps5(sys(0), msg(9)), facts: exact5m,
			usage: Usage{CacheWrite: 35000}},
		{at: t0.Add(10 * time.Second), client: mk(nil, ex2Sys, client), clientBPs: bps5(sys(0), msg(9)),
			ops: []Operation{opX}, fwd: mk(nil, ex2Sys, fwd(5148)), facts: exact5m,
			usage: Usage{CacheWrite: 34800}},
		// Y takes over M. X is not reported again.
		{at: t0.Add(20 * time.Second), client: mk(nil, ex2Sys, client), clientBPs: bps5(sys(0), msg(9)),
			ops: []Operation{opY}, fwd: mk(nil, ex2Sys, fwd(299)), facts: exact5m,
			usage: Usage{CacheWrite: 30000}},
		// The next request replays nothing new. Y stays in place.
		{at: t0.Add(30 * time.Second), client: mk(nil, ex2Sys, client), clientBPs: bps5(sys(0), msg(9)),
			fwd: mk(nil, ex2Sys, fwd(299)), facts: exact5m, usage: Usage{CacheRead: 30000}},
	}
	active := func() []string {
		var out []string
		for _, op := range ts.tracker(ThreadKey("s1", Thread{ID: "main"}), time.Time{}).snapshot().Active {
			out = append(out, op.ID)
		}
		return out
	}
	reqs[0].run(ts)
	reqs[1].run(ts)
	res := reqs[2].run(ts)
	noChecks(t, res)
	if got := active(); len(got) != 1 || got[0] != "opY" {
		t.Fatalf("after Y took over M: active %v, want only opY", got)
	}
	noChecks(t, reqs[3].run(ts))
}
