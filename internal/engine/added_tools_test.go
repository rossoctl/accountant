package engine

import (
	"testing"
	"time"
)

// accountant#70, repro case A: the host adds two tool declarations after the client's 7. On the next request
// the client sends no tools. The client removed what precedes the added declarations, so both changes end
// (design Q12). On main they stayed active, and the sum check failed by -1285 bytes (340 + 945) on every later
// request of the thread.
func TestAddedToolsEndWhenTheClientDropsItsTools(t *testing.T) {
	ts := NewTrackers()
	tools := msgs(7, 1000)
	system := []blk{b("s0", 1100), b("s1", 1100), b("s2", 1101)}
	client1 := mk(tools, system, []blk{b("m0", 5000), b("m1", 5000)})
	fwd1 := mk(with(tools, b("t7", 340), b("t8", 945)), system, []blk{b("m0", 5000), b("m1", 5000)})
	addA := rewrite("a", "context-guru.expand", "cg:expand:decl-added:h1", tool(7), 0, 340)
	addB := rewrite("b", "context-guru.adjudicate", "cg:adjudicate:decl-added:h2", tool(8), 0, 945)
	noSumFailure(t, req{at: t0, client: client1, clientBPs: bps5(sys(2), msg(1)), facts: exact5m,
		ops: []Operation{addA, addB}, fwd: fwd1, usage: Usage{CacheWrite: 5400}}.run(ts))
	if got := activeIDs(ts); len(got) != 2 {
		t.Fatalf("after request 1: Active = %v, want both added declarations", got)
	}
	client2 := mk(nil, system, []blk{b("n0", 24410)})
	for i := 1; i <= 2; i++ {
		res := req{at: t0.Add(time.Duration(i) * 10 * time.Second), client: client2, clientBPs: bps5(sys(2), msg(0)),
			facts: exact5m, usage: Usage{Input: 11235}}.run(ts)
		noSumFailure(t, res)
		if got := activeIDs(ts); len(got) != 0 {
			t.Fatalf("request %d: Active = %v, want none", i+1, got)
		}
	}
}

// The client removes only its last tool, the one just before the added declaration. That is an edit of what
// precedes the added content, so the change ends (design Q12). On main the end rule measured the insertion point in
// the new, shorter section and missed the edit.
func TestAnAddedToolEndsWhenTheClientRemovesTheToolBeforeIt(t *testing.T) {
	ts := NewTrackers()
	tools := msgs(3, 1000)
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	add := rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(3), 0, 400)
	req{at: t0, client: mk(tools, system, ms), clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{add}, fwd: mk(with(tools, b("t3", 400)), system, ms), usage: Usage{CacheWrite: 3500}}.run(ts)
	res := req{at: t0.Add(10 * time.Second), client: mk(tools[:2], system, ms), clientBPs: bps5(sys(0), msg(1)),
		facts: exact5m, usage: Usage{CacheWrite: 3000}}.run(ts)
	noSumFailure(t, res)
	if got := activeIDs(ts); len(got) != 0 {
		t.Fatalf("Active = %v, want none", got)
	}
}

// Negative control: the client appends a tool after its own. Nothing before the added declaration changed, so
// the change stays.
func TestAnAddedToolStaysWhenTheClientAppendsATool(t *testing.T) {
	ts := NewTrackers()
	tools := msgs(3, 1000)
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	add := rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(3), 0, 400)
	req{at: t0, client: mk(tools, system, ms), clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{add}, fwd: mk(with(tools, b("t3", 400)), system, ms), usage: Usage{CacheWrite: 3500}}.run(ts)
	tools2 := with(tools, b("t3c", 600))
	res := req{at: t0.Add(10 * time.Second), client: mk(tools2, system, ms), clientBPs: bps5(sys(0), msg(1)),
		facts: exact5m, fwd: mk(with(tools, b("t3", 400), b("t3c", 600)), system, ms),
		usage: Usage{CacheWrite: 4000}}.run(ts)
	noSumFailure(t, res)
	if got := activeIDs(ts); len(got) != 1 {
		t.Fatalf("Active = %v, want the added declaration", got)
	}
}

// The added declaration's position is in forwarded space. With another added declaration before it in the
// same section, the same index is one further along in the client body. The client edits the tool after the
// one the declaration followed, so nothing before the declaration changed and the change stays.
func TestAnAddedToolStaysWhenTheClientEditsAfterItWithAnotherAddedBefore(t *testing.T) {
	ts := NewTrackers()
	tools := msgs(5, 1000) // c0..c4
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	x := rewrite("x", "context-guru.expand", "cg:expand:decl-x", tool(2), 0, 300)
	y := rewrite("y", "context-guru.expand", "cg:expand:decl-y", tool(4), 0, 400)
	fwd := func(c3 blk) []blk { // c0 c1 X c2 Y c3 c4
		return []blk{tools[0], tools[1], b("x", 300), tools[2], b("y", 400), c3, tools[4]}
	}
	noSumFailure(t, req{at: t0, client: mk(tools, system, ms), clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{x, y}, fwd: mk(fwd(tools[3]), system, ms), usage: Usage{CacheWrite: 3500}}.run(ts))
	edited := []blk{tools[0], tools[1], tools[2], b("e3", 1000), tools[4]}
	res := req{at: t0.Add(10 * time.Second), client: mk(edited, system, ms), clientBPs: bps5(sys(0), msg(1)),
		facts: exact5m, fwd: mk(fwd(b("e3", 1000)), system, ms), usage: Usage{CacheWrite: 3500}}.run(ts)
	noSumFailure(t, res)
	if got := activeIDs(ts); len(got) != 2 {
		t.Fatalf("Active = %v, want both added declarations", got)
	}
}

// One change adds two declarations as a single span, and another change adds one after them. The client
// edits a tool after the one the second change followed, so both changes stay. The span counts as two items.
func TestAnAddedToolStaysWhenTheClientEditsAfterItWithASpanBefore(t *testing.T) {
	ts := NewTrackers()
	tools := msgs(5, 1000) // c0..c4
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	x := rewrite("x", "context-guru.expand", "cg:expand:decl-x", tool(2), 0, 700)
	x.Spans = []Span{{Section: SectionTools, From: 2, To: 3, SizeAfter: Size{Bytes: 700}}}
	y := rewrite("y", "context-guru.expand", "cg:expand:decl-y", tool(5), 0, 400)
	fwd := func(c3 blk) []blk { // c0 c1 X1 X2 c2 Y c3 c4
		return []blk{tools[0], tools[1], b("x1", 350), b("x2", 350), tools[2], b("y", 400), c3, tools[4]}
	}
	noSumFailure(t, req{at: t0, client: mk(tools, system, ms), clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{x, y}, fwd: mk(fwd(tools[3]), system, ms), usage: Usage{CacheWrite: 3500}}.run(ts))
	edited := []blk{tools[0], tools[1], tools[2], b("e3", 1000), tools[4]}
	res := req{at: t0.Add(10 * time.Second), client: mk(edited, system, ms), clientBPs: bps5(sys(0), msg(1)),
		facts: exact5m, fwd: mk(fwd(b("e3", 1000)), system, ms), usage: Usage{CacheWrite: 3500}}.run(ts)
	noSumFailure(t, res)
	if got := activeIDs(ts); len(got) != 2 {
		t.Fatalf("Active = %v, want both added declarations", got)
	}
}

// The pipeline order is not the index order: y adds at tool(3) and runs first, then x adds at tool(2). y's
// position is in y's own output, which does not have X, so X must not be taken out of it. The client edits c2,
// the tool y followed, so y ends. x followed c1, which did not change, so x stays.
func TestAnAddedToolEndsWhenALaterChangeAddedBeforeIt(t *testing.T) {
	ts := NewTrackers()
	tools := msgs(5, 1000) // c0..c4
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	y := rewrite("y", "context-guru.expand", "cg:expand:decl-y", tool(3), 0, 400)
	x := rewrite("x", "context-guru.expand", "cg:expand:decl-x", tool(2), 0, 300)
	fwd1 := []blk{tools[0], tools[1], b("x", 300), tools[2], b("y", 400), tools[3], tools[4]}
	noSumFailure(t, req{at: t0, client: mk(tools, system, ms), clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{y, x}, fwd: mk(fwd1, system, ms), usage: Usage{CacheWrite: 3500}}.run(ts))
	edited := []blk{tools[0], tools[1], b("e2", 1000), tools[3], tools[4]}
	fwd2 := []blk{tools[0], tools[1], b("x", 300), b("e2", 1000), tools[3], tools[4]}
	res := req{at: t0.Add(10 * time.Second), client: mk(edited, system, ms), clientBPs: bps5(sys(0), msg(1)),
		facts: exact5m, fwd: mk(fwd2, system, ms), usage: Usage{CacheWrite: 3500}}.run(ts)
	noSumFailure(t, res)
	if got := activeIDs(ts); len(got) != 1 || got[0] != "cg:expand:decl-x" {
		t.Fatalf("Active = %v, want only cg:expand:decl-x", got)
	}
}

// A change reported on a later request runs after the active ones. a adds after c4 on request 1. b adds at
// tool(0) on request 2, so b's B is not in a's output and must not be taken out of a's position. On request 3
// the client edits c4, the tool a followed, so a ends. b is at the first position, so it stays.
func TestAnAddedToolEndsWhenAChangeOnALaterRequestAddedBeforeIt(t *testing.T) {
	ts := NewTrackers()
	tools := msgs(5, 1000) // c0..c4
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	a := rewrite("a", "context-guru.expand", "cg:expand:decl-a", tool(5), 0, 400)
	bb := rewrite("b", "context-guru.expand", "cg:expand:decl-b", tool(0), 0, 300)
	noSumFailure(t, req{at: t0, client: mk(tools, system, ms), clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{a}, fwd: mk(with(tools, b("a", 400)), system, ms), usage: Usage{CacheWrite: 3500}}.run(ts))
	fwd2 := append([]blk{b("b", 300)}, with(tools, b("a", 400))...)
	noSumFailure(t, req{at: t0.Add(10 * time.Second), client: mk(tools, system, ms), clientBPs: bps5(sys(0), msg(1)),
		facts: exact5m, ops: []Operation{bb}, fwd: mk(fwd2, system, ms), usage: Usage{CacheWrite: 3500}}.run(ts))
	edited := []blk{tools[0], tools[1], tools[2], tools[3], b("e4", 1000)}
	fwd3 := append([]blk{b("b", 300)}, edited...)
	res := req{at: t0.Add(20 * time.Second), client: mk(edited, system, ms), clientBPs: bps5(sys(0), msg(1)),
		facts: exact5m, fwd: mk(fwd3, system, ms), usage: Usage{CacheWrite: 3500}}.run(ts)
	noSumFailure(t, res)
	if got := activeIDs(ts); len(got) != 1 || got[0] != "cg:expand:decl-b" {
		t.Fatalf("Active = %v, want only cg:expand:decl-b", got)
	}
}
