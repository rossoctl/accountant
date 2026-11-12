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

// inPlaceSession runs the requests of one thread over a client body whose message 2 is 5302 bytes. Each
// step reports its ops and forwards message 2 at the given size.
func inPlaceSession(t *testing.T, steps ...inPlaceStep) (*Trackers, []Result) {
	t.Helper()
	ts := NewTrackers()
	client := with(msgs(9, 3000), b("m9", 3000))
	client[2] = b("c2", 5302)
	var out []Result
	for i, st := range steps {
		fwd := append([]blk(nil), client...)
		fwd[2] = b("f2", st.size)
		out = append(out, req{at: t0.Add(time.Duration(i) * 10 * time.Second), client: mk(nil, ex2Sys, client),
			clientBPs: bps5(sys(0), msg(9)), ops: st.ops, fwd: mk(nil, ex2Sys, fwd), facts: exact5m,
			usage: Usage{CacheWrite: 40000}}.run(ts))
	}
	return ts, out
}

type inPlaceStep struct {
	size int
	ops  []Operation
}

func activeIDs(ts *Trackers) []string {
	var out []string
	for _, op := range ts.tracker(ThreadKey("s1", Thread{ID: "main"}), time.Time{}).snapshot().Active {
		out = append(out, op.ChangeID)
	}
	return out
}

func noSumFailure(t *testing.T, res Result) {
	t.Helper()
	for _, c := range res.Checks {
		if c.Check == CheckSum {
			t.Fatalf("sum check failed: %+v", c)
		}
	}
}

// A change that another operator rewrote further (its SizeBefore is the first change's SizeAfter) is a
// chain. The first change stays, with and without spans on the second.
func TestAChainedInPlaceChangeStays(t *testing.T) {
	x := rewrite("x", "context-guru.extract", "cx", msg(2), 5302, 5148)
	y := rewrite("y", "context-guru.sweep", "cy", msg(2), 5148, 299)
	ts, res := inPlaceSession(t, inPlaceStep{5148, []Operation{x}}, inPlaceStep{299, []Operation{y}})
	noSumFailure(t, res[1])
	if got := activeIDs(ts); len(got) != 2 {
		t.Fatalf("Active = %v, want both changes", got)
	}
	y.Spans = []Span{{Section: SectionMessages, From: 2, To: 2, SizeBefore: Size{Bytes: 5148}, SizeAfter: Size{Bytes: 299}}}
	ts, res = inPlaceSession(t, inPlaceStep{5148, []Operation{x}}, inPlaceStep{299, []Operation{y}})
	noSumFailure(t, res[1])
	if got := activeIDs(ts); len(got) != 2 {
		t.Fatalf("with spans: Active = %v, want both changes", got)
	}
}

// Ending the last change of a chain frees the one before it: the repeat pass ends both.
func TestTakingOverAChainEndsEveryLink(t *testing.T) {
	x := rewrite("x", "context-guru.extract", "cx", msg(2), 5302, 5148)
	y := rewrite("y", "context-guru.sweep", "cy", msg(2), 5148, 3000)
	z := rewrite("z", "context-guru.llm", "cz", msg(2), 5302, 299)
	ts, res := inPlaceSession(t, inPlaceStep{5148, []Operation{x}}, inPlaceStep{3000, []Operation{y}}, inPlaceStep{299, []Operation{z}})
	noChecks(t, res[2])
	if got := activeIDs(ts); len(got) != 1 || got[0] != "cz" {
		t.Fatalf("Active = %v, want only cz", got)
	}
}

// accountant#70 review: a rewrite of a range that continues from an active in-place change keeps it.
func TestARangeRewriteContinuingFromAnInPlaceChangeKeepsIt(t *testing.T) {
	ts := NewTrackers()
	client := mk(nil, ex2Sys, []blk{b("m0", 3000), b("m1", 8000), b("m2", 3000), b("m3", 3000)})
	fwd1 := mk(nil, ex2Sys, []blk{b("m0", 3000), b("m1x", 7000), b("m2", 3000), b("m3", 3000)})
	x := rewrite("x", "context-guru.extract", "cx", msg(1), 8000, 7000)
	req{at: t0, client: client, clientBPs: bps5(sys(0), msg(3)), facts: exact5m,
		ops: []Operation{x}, fwd: fwd1, usage: Usage{CacheWrite: 16000}}.run(ts)
	y := Operation{ID: "y", Kind: KindRewrite, Operator: "trim", ChangeID: "cy", At: msg(1),
		SizeBefore: Size{Bytes: 10000}, SizeAfter: Size{Bytes: 5000},
		Spans: []Span{{Section: SectionMessages, From: 1, To: 2,
			SizeBefore: Size{Bytes: 10000}, SizeAfter: Size{Bytes: 5000}}}}
	fwd2 := mk(nil, ex2Sys, []blk{b("m0", 3000), b("m1y", 3000), b("m2y", 2000), b("m3", 3000)})
	res := req{at: t0.Add(10 * time.Second), client: client, clientBPs: bps5(sys(0), msg(3)),
		facts: exact5m, ops: []Operation{y}, fwd: fwd2,
		usage: Usage{CacheRead: 11000, CacheWrite: 3000}}.run(ts)
	noChecks(t, res)
	if got := activeIDs(ts); len(got) != 2 {
		t.Fatalf("Active = %v, want both changes", got)
	}
}

// accountant#70 review: an earlier splice shifts the index, so a later summary's range is not in client
// space. The rule does not run with a splice in the state, and the change under the summary keeps its credit.
func TestAChangeUnderALaterSpliceIsNotEndedByTheTakeoverRule(t *testing.T) {
	ts := NewTrackers()
	ms := msgs(13, 1000)
	ms[10], ms[11] = b("m10", 8000), b("m11", 3000)
	client := mk(nil, ex2Sys, ms)
	ins := []blk{b("i0", 1000), b("i1", 1000), b("i2", 1000), b("i3", 1000), b("i4", 1000)}
	fwd1 := mk(nil, ex2Sys, with(ins, ms...))
	s1 := rewrite("s1", "retrieve", "cs1", msg(0), 0, 5000)
	s1.MessageSplice = &MessageSplice{At: 0, Removed: 0, Inserted: 5}
	req{at: t0, client: client, clientBPs: bps5(sys(0), msg(12)), facts: exact5m,
		ops: []Operation{s1}, fwd: fwd1, usage: Usage{CacheWrite: 30000}}.run(ts)
	fwd2 := mk(nil, ex2Sys, with(ins, append(ms[0:10:10], b("m10x", 7000), b("m11", 3000), b("m12", 1000))...))
	x := rewrite("x", "context-guru.extract", "cx", msg(15), 8000, 7000)
	req{at: t0.Add(10 * time.Second), client: client, clientBPs: bps5(sys(0), msg(12)),
		facts: exact5m, ops: []Operation{x}, fwd: fwd2, usage: Usage{CacheWrite: 29000}}.run(ts)
	fwd3 := mk(nil, ex2Sys, with(ins, append(ms[0:10:10], b("sum", 2000), b("m12", 1000))...))
	s2 := rewrite("s2", "summarize", "cs2", msg(15), 10000, 2000)
	s2.MessageSplice = &MessageSplice{At: 15, Removed: 2, Inserted: 1}
	res := req{at: t0.Add(20 * time.Second), client: client, clientBPs: bps5(sys(0), msg(12)),
		facts: exact5m, ops: []Operation{s2}, fwd: fwd3,
		usage: Usage{CacheRead: 18000, CacheWrite: 3000}}.run(ts)
	noSumFailure(t, res)
	if got := activeIDs(ts); !slicesContain(got, "cx") {
		t.Fatalf("Active = %v, want the extract change kept", got)
	}
}

func slicesContain(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}
