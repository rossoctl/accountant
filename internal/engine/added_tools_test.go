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

// accountant#89: the host adds a tool declaration on request 1, then its configuration changes (preset off)
// and later requests forward the client body unchanged, without the declaration. The client edited nothing, so
// the Q12 rule cannot end the change. The forwarded tools are the client's own again, so the change ends. On
// main it stayed, and the sum check failed by -340 bytes on every later request of the thread.
func TestAnAddedToolEndsWhenTheHostStopsAddingIt(t *testing.T) {
	ts := NewTrackers()
	tools := msgs(3, 1000)
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	client := mk(tools, system, ms)
	add := rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(3), 0, 340)
	noSumFailure(t, req{at: t0, client: client, clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{add}, fwd: mk(with(tools, b("t3", 340)), system, ms), usage: Usage{CacheWrite: 3500}}.run(ts))
	if got := activeIDs(ts); len(got) != 1 {
		t.Fatalf("after request 1: Active = %v, want the added declaration", got)
	}
	for i := 1; i <= 2; i++ {
		res := req{at: t0.Add(time.Duration(i) * 10 * time.Second), client: client, clientBPs: bps5(sys(0), msg(1)),
			facts: exact5m, fwd: mk(tools, system, ms), usage: Usage{CacheWrite: 3500}}.run(ts)
		noChecks(t, res)
		if got := activeIDs(ts); len(got) != 0 {
			t.Fatalf("request %d: Active = %v, want none", i+1, got)
		}
	}
}

// Negative control for accountant#89: the host keeps adding the declaration and the client sends the same body.
// The forwarded tools are not the client's, so the change stays.
func TestAnAddedToolStaysWhileTheHostKeepsAddingIt(t *testing.T) {
	ts := NewTrackers()
	tools := msgs(3, 1000)
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	client := mk(tools, system, ms)
	fwd := mk(with(tools, b("t3", 340)), system, ms)
	add := rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(3), 0, 340)
	req{at: t0, client: client, clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{add}, fwd: fwd, usage: Usage{CacheWrite: 3500}}.run(ts)
	res := req{at: t0.Add(10 * time.Second), client: client, clientBPs: bps5(sys(0), msg(1)),
		facts: exact5m, fwd: fwd, usage: Usage{CacheRead: 3500}}.run(ts)
	noSumFailure(t, res)
	if got := activeIDs(ts); len(got) != 1 {
		t.Fatalf("Active = %v, want the added declaration", got)
	}
}

// A DryRun forwards the client's own body by design (R12), so the forwarded tools equal the client's on every
// request. That is no proof that the host stopped adding it: the DryRun change stays, and "would have saved"
// shows on every request (§4.6).
func TestAnAddedDryRunToolStaysWhenTheForwardedToolsAreTheClients(t *testing.T) {
	ts := NewTrackers()
	tools := msgs(3, 1000)
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	client := mk(tools, system, ms)
	add := rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(3), 0, 340)
	add.DryRun = true
	noSumFailure(t, req{at: t0, client: client, clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{add}, usage: Usage{CacheWrite: 3500}}.run(ts))
	res := req{at: t0.Add(10 * time.Second), client: client, clientBPs: bps5(sys(0), msg(1)),
		facts: exact5m, usage: Usage{CacheRead: 3500}}.run(ts)
	noSumFailure(t, res)
	if got := activeIDs(ts); len(got) != 1 {
		t.Fatalf("Active = %v, want the DryRun added declaration", got)
	}
}

// The host keeps an in-place rewrite of tool 0 (1000 -> 600) and stops adding the declaration, as a preset
// change that keeps one lever and drops another does. Tool 0 has the size the active rewrite gives it, so
// the forwarded tools carry no added content: the added change ends, and the rewrite stays.
func TestAnAddedToolEndsWhenTheHostStopsAddingItButKeepsAnInPlaceChange(t *testing.T) {
	ts := NewTrackers()
	tools := msgs(3, 1000)
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	client := mk(tools, system, ms)
	shrunk := []blk{b("r0", 600), tools[1], tools[2]}
	shrink := rewrite("s", "context-guru.trim", "cg:trim:t0", tool(0), 1000, 600)
	add := rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(3), 0, 340)
	noSumFailure(t, req{at: t0, client: client, clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{shrink, add}, fwd: mk(with(shrunk, b("t3", 340)), system, ms),
		usage: Usage{CacheWrite: 3500}}.run(ts))
	res := req{at: t0.Add(10 * time.Second), client: client, clientBPs: bps5(sys(0), msg(1)),
		facts: exact5m, fwd: mk(shrunk, system, ms), usage: Usage{CacheWrite: 3500}}.run(ts)
	noChecks(t, res)
	if got := activeIDs(ts); len(got) != 1 || got[0] != "cg:trim:t0" {
		t.Fatalf("Active = %v, want only cg:trim:t0", got)
	}
}

// The rule covers the system section too: the host added a system block and stops adding it.
func TestAnAddedSystemBlockEndsWhenTheHostStopsAddingIt(t *testing.T) {
	ts := NewTrackers()
	tools := msgs(3, 1000)
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	client := mk(tools, system, ms)
	add := rewrite("a", "context-guru.expand", "cg:expand:sys-added", sys(1), 0, 500)
	noSumFailure(t, req{at: t0, client: client, clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{add}, fwd: mk(tools, with(system, b("s1", 500)), ms), usage: Usage{CacheWrite: 3500}}.run(ts))
	res := req{at: t0.Add(10 * time.Second), client: client, clientBPs: bps5(sys(0), msg(1)),
		facts: exact5m, usage: Usage{CacheWrite: 3500}}.run(ts)
	noChecks(t, res)
	if got := activeIDs(ts); len(got) != 0 {
		t.Fatalf("Active = %v, want none", got)
	}
}

// accountant#94: the host adds a tool at tool(0) and trims the client's first tool, at tool(1) in its output.
// On request 2 it stops adding the tool and keeps the trim. The trim's position counted the added tool, so the
// end rules read client tool 1 there: the trim looked restored and the added change looked kept. Expect: the
// added change ends, the trim stays, and no check fails.
func TestAnInPlaceChangeAfterAddedContentStaysWhenTheHostStopsAdding(t *testing.T) {
	ts := NewTrackers()
	tools := msgs(3, 1000)
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	client := mk(tools, system, ms)
	add := rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(0), 0, 340)
	trim := rewrite("s", "context-guru.trim", "cg:trim:t1", tool(1), 1000, 600)
	noChecks(t, req{at: t0, client: client, clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{add, trim}, fwd: mk([]blk{b("x", 340), b("r0", 600), tools[1], tools[2]}, system, ms),
		usage: Usage{CacheWrite: 3500}}.run(ts))
	res := req{at: t0.Add(10 * time.Second), client: client, clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		fwd: mk([]blk{b("r0", 600), tools[1], tools[2]}, system, ms), usage: Usage{CacheWrite: 3500}}.run(ts)
	noChecks(t, res)
	if got := activeIDs(ts); len(got) != 1 || got[0] != "cg:trim:t1" {
		t.Fatalf("Active = %v, want only cg:trim:t1", got)
	}
}

// shiftTools are the client tools of shiftSession. Tool 1 has another size than tool 0, so a rule that reads the
// wrong one of them fails.
func shiftTools() []blk { return []blk{b("c0", 1000), b("c1", 1200), b("c2", 1000)} }

// shiftSession is the #94 setup: the host adds X at tool(0) and trims client tool 0 (1000 -> 600), which is at
// tool(1) in its output. Request 2 sends client2 and forwards fwd2 with no new ops.
func shiftSession(t *testing.T, client2, fwd2 []blk) (*Trackers, Result) {
	t.Helper()
	ts := NewTrackers()
	tools := shiftTools()
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	add := rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(0), 0, 340)
	trim := rewrite("s", "context-guru.trim", "cg:trim:t1", tool(1), 1000, 600)
	noChecks(t, req{at: t0, client: mk(tools, system, ms), clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{add, trim}, fwd: mk([]blk{b("x", 340), b("r0", 600), tools[1], tools[2]}, system, ms),
		usage: Usage{CacheWrite: 3500}}.run(ts))
	res := req{at: t0.Add(10 * time.Second), client: mk(client2, system, ms), clientBPs: bps5(sys(0), msg(1)),
		facts: exact5m, fwd: mk(fwd2, system, ms), usage: Usage{CacheWrite: 3500}}.run(ts)
	return ts, res
}

// Control: the host keeps both changes and the client sends the same body. Both stay.
func TestAnInPlaceChangeAfterAddedContentStaysWhileBothAreApplied(t *testing.T) {
	tools := shiftTools()
	ts, res := shiftSession(t, tools, []blk{b("x", 340), b("r0", 600), tools[1], tools[2]})
	noChecks(t, res)
	if got := activeIDs(ts); len(got) != 2 {
		t.Fatalf("Active = %v, want both changes", got)
	}
}

// The host stops trimming and keeps adding X. The trimmed item is client tool 0, which is forwarded at tool(1)
// with its own size again, so restored ends the trim. On main, restored read client tool 1 there, and
// takenOver ended the trim instead.
func TestAnInPlaceChangeAfterAddedContentEndsWhenTheHostRestoresIt(t *testing.T) {
	tools := shiftTools()
	ts, res := shiftSession(t, tools, []blk{b("x", 340), tools[0], tools[1], tools[2]})
	noChecks(t, res)
	if got := activeIDs(ts); len(got) != 1 || got[0] != "cg:expand:decl-added" {
		t.Fatalf("Active = %v, want only cg:expand:decl-added", got)
	}
}

// The client edits its tool 1, after the trimmed tool 0. That is not an edit at or before the trim, so the trim
// stays; the edit is after X's insertion point too, so X stays. On main the trim's position was read as
// client tool 1, and the edit ended it.
func TestAnInPlaceChangeAfterAddedContentStaysWhenTheClientEditsAfterIt(t *testing.T) {
	tools := shiftTools()
	edited := []blk{tools[0], b("e1", 1200), tools[2]}
	ts, res := shiftSession(t, edited, []blk{b("x", 340), b("r0", 600), b("e1", 1200), tools[2]})
	noSumFailure(t, res)
	if got := activeIDs(ts); len(got) != 2 {
		t.Fatalf("Active = %v, want both changes", got)
	}
}
