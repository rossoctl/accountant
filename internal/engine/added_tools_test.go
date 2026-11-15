package engine

import (
	"testing"
	"time"
)

// accountant#70, repro case A: the host adds two tool declarations after the client's 7. On the next request
// the client sends no tools. The client removed what precedes the added declarations, so both changes end
// (§4.3). On main they stayed active, and the sum check failed by -1285 bytes (340 + 945) on every later
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
// precedes the added content, so the change ends (§4.3). On main the end rule measured the insertion point in
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
