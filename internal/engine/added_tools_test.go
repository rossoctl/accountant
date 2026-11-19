package engine

import (
	"sort"
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

// After the added change ends, the trim is stored without X in its position. Requests 3 and 4 read the same
// client tool 0, so the trim stays and no check fails. Before, the stored tool(1) read client tool 1 once X had
// left Active, restored ended the trim, and the sum check failed by 400 bytes on every later request.
func TestAnInPlaceChangeAfterAddedContentStaysOnLaterRequests(t *testing.T) {
	tools := shiftTools()
	fwd2 := []blk{b("r0", 600), tools[1], tools[2]}
	ts, res := shiftSession(t, tools, fwd2)
	noChecks(t, res)
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	for i := 2; i <= 3; i++ {
		res := req{at: t0.Add(time.Duration(i) * 10 * time.Second), client: mk(tools, system, ms),
			clientBPs: bps5(sys(0), msg(1)), facts: exact5m, fwd: mk(fwd2, system, ms),
			usage: Usage{CacheRead: 3300}}.run(ts)
		noChecks(t, res)
		if got := activeIDs(ts); len(got) != 1 || got[0] != "cg:trim:t1" {
			t.Fatalf("request %d: Active = %v, want only cg:trim:t1", i+1, got)
		}
	}
}

// The host still adds X, and another operator now rewrites client tool 0 (reported at tool(1), 1000 -> 300). The
// trim is not reported, so takenOver must end it. X sits before client tool 0 but measured no client bytes, so it
// is not a chain partner of the trim.
func TestAnInPlaceChangeAfterAddedContentEndsWhenAnotherOperatorTakesItOver(t *testing.T) {
	ts := NewTrackers()
	tools := shiftTools()
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	client := mk(tools, system, ms)
	add := rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(0), 0, 340)
	trim := rewrite("s", "context-guru.trim", "cg:trim:t1", tool(1), 1000, 600)
	noChecks(t, req{at: t0, client: client, clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{add, trim}, fwd: mk([]blk{b("x", 340), b("r0", 600), tools[1], tools[2]}, system, ms),
		usage: Usage{CacheWrite: 3500}}.run(ts))
	y := rewrite("y", "context-guru.sweep", "cg:sweep:t1", tool(1), 1000, 300)
	res := req{at: t0.Add(10 * time.Second), client: client, clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{y}, fwd: mk([]blk{b("x", 340), b("y0", 300), tools[1], tools[2]}, system, ms),
		usage: Usage{CacheWrite: 3500}}.run(ts)
	noSumFailure(t, res)
	got := activeIDs(ts)
	sort.Strings(got)
	if len(got) != 2 || got[0] != "cg:expand:decl-added" || got[1] != "cg:sweep:t1" {
		t.Fatalf("Active = %v, want cg:expand:decl-added and cg:sweep:t1", got)
	}
}

// accountant#100: request 1 is the #94 setup. On request 2 the host stops adding X, and a DryRun D is reported
// at tool(2). D ran after the host stopped adding X, so its position never counted X: it is stored at tool(2).
// Before, the rebase for ended content took X out of D too and stored it at tool(1).
func reportedAfterAddedEnds(t *testing.T) (*Trackers, []blk, []blk) {
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
	d := rewrite("d", "context-guru.extract", "cg:extract:t2", tool(2), 1000, 800)
	d.DryRun = true
	res := req{at: t0.Add(10 * time.Second), client: mk(tools, system, ms), clientBPs: bps5(sys(0), msg(1)),
		facts: exact5m, ops: []Operation{d}, fwd: mk([]blk{b("r0", 600), tools[1], tools[2]}, system, ms),
		usage: Usage{CacheWrite: 3500}}.run(ts)
	noSumFailure(t, res)
	return ts, system, ms
}

func TestAChangeReportedWhenAddedContentEndsIsStoredWhereItWasReported(t *testing.T) {
	ts, _, _ := reportedAfterAddedEnds(t)
	want := map[string]Position{"cg:trim:t1": tool(0), "cg:extract:t2": tool(2)}
	active := ts.tracker(ThreadKey("s1", Thread{ID: "main"}), time.Time{}).snapshot().Active
	if len(active) != len(want) {
		t.Fatalf("Active = %v, want %v", activeIDs(ts), want)
	}
	for _, op := range active {
		if op.At != want[op.ChangeID] {
			t.Errorf("%s stored at %+v, want %+v", op.ChangeID, op.At, want[op.ChangeID])
		}
	}
}

// On request 3 the client edits only its tool 2. D is at tool(2), so the edit ends it; the trim of tool 0 stays.
// Before, D read client tool 1, saw no edit, and stayed.
func TestAChangeReportedWhenAddedContentEndsEndsOnAClientEditAtItsItem(t *testing.T) {
	ts, system, ms := reportedAfterAddedEnds(t)
	tools := shiftTools()
	edited := []blk{tools[0], tools[1], b("e2", 1000)}
	res := req{at: t0.Add(20 * time.Second), client: mk(edited, system, ms), clientBPs: bps5(sys(0), msg(1)),
		facts: exact5m, fwd: mk([]blk{b("r0", 600), tools[1], b("e2", 1000)}, system, ms),
		usage: Usage{CacheWrite: 3500}}.run(ts)
	noSumFailure(t, res)
	if got := activeIDs(ts); len(got) != 1 || got[0] != "cg:trim:t1" {
		t.Fatalf("Active = %v, want only cg:trim:t1", got)
	}
}

// accountant#100, review of #101: X trims client tool 0 and adds one tool after it, and T trims client tool 2,
// at tool(3) in the output. On request 2 the host stops X, so restored ends it, and a reported P sweeps T's
// output at tool(2). P ran without X's added tool, so it covers client tool 2 and is a chain partner of T: both
// stay. Before, clientSide took X out of P's position too, P seemed to cover client tool 1, takenOver ended T,
// and the sum check failed.
func TestAReportedChainPartnerAfterEndedAddedContentKeepsTheChange(t *testing.T) {
	ts := NewTrackers()
	tools := []blk{b("c0", 1000), b("c1", 1200), b("c2", 1000), b("c3", 1000)}
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	x := rewrite("x", "context-guru.expand", "cg:expand:x", tool(0), 1000, 840)
	x.Spans = []Span{
		{Section: SectionTools, From: 0, To: 0, SizeBefore: Size{Bytes: 1000}, SizeAfter: Size{Bytes: 500}},
		{Section: SectionTools, From: 1, To: 1, SizeAfter: Size{Bytes: 340}},
	}
	trim := rewrite("t", "context-guru.trim", "cg:trim:c2", tool(3), 1000, 700)
	noChecks(t, req{at: t0, client: mk(tools, system, ms), clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops:   []Operation{x, trim},
		fwd:   mk([]blk{b("r0", 500), b("x", 340), tools[1], b("t2", 700), tools[3]}, system, ms),
		usage: Usage{CacheWrite: 3500}}.run(ts))
	p := rewrite("p", "context-guru.sweep", "cg:sweep:c2", tool(2), 700, 300)
	res := req{at: t0.Add(10 * time.Second), client: mk(tools, system, ms), clientBPs: bps5(sys(0), msg(1)),
		facts: exact5m, ops: []Operation{p},
		fwd: mk([]blk{tools[0], tools[1], b("p2", 300), tools[3]}, system, ms), usage: Usage{CacheWrite: 3500}}.run(ts)
	noChecks(t, res)
	got := activeIDs(ts)
	sort.Strings(got)
	if len(got) != 2 || got[0] != "cg:sweep:c2" || got[1] != "cg:trim:c2" {
		t.Fatalf("Active = %v, want cg:sweep:c2 and cg:trim:c2", got)
	}
}

// accountant#96: the pricing of an in-place change in tools reads its reported position as a client index.
// One breakpoint sits on client tool 0, so a trim of client tool 0 is inside the written prefix and saves a
// cache write. The host adds X at tool(0) first, so the trim is reported at tool(1). Read as a client index,
// that is client tool 1, which is after the breakpoint, so the saving is priced as input.
func trimTier(t *testing.T, withAdded bool) TierTokens {
	t.Helper()
	tools := shiftTools()
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	ops := []Operation{rewrite("s", "context-guru.trim", "cg:trim:t0", tool(0), 1000, 600)}
	fwdTools := []blk{b("r0", 600), tools[1], tools[2]}
	fwdBP, write := tool(0), 600
	if withAdded {
		ops = []Operation{rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(0), 0, 340),
			rewrite("s", "context-guru.trim", "cg:trim:t0", tool(1), 1000, 600)}
		fwdTools = append([]blk{b("x", 340)}, fwdTools...)
		fwdBP, write = tool(1), 940
	}
	res := req{at: t0, client: mk(tools, system, ms), clientBPs: bps5(tool(0)), ops: ops,
		fwd: mk(fwdTools, system, ms), fwdBPs: bps5(fwdBP), facts: exact5m,
		usage: Usage{CacheWrite: write, Input: 14200}}.run(NewTrackers())
	noChecks(t, res)
	return findOp(t, res, "s").Saved
}

func TestIssue96ControlTrimWithoutAddedContentSavesAWrite(t *testing.T) {
	if got := trimTier(t, false); got != (TierTokens{CacheWrite5m: 400}) {
		t.Fatalf("saved %+v, want 400 cache write", got)
	}
}

func TestIssue96TrimAfterAddedContentSavesTheSameWrite(t *testing.T) {
	if got := trimTier(t, true); got != (TierTokens{CacheWrite5m: 400}) {
		t.Fatalf("saved %+v, want 400 cache write (as without added content)", got)
	}
}

// mediaTools are client tools for the accountant#96 media cases: text, a 400,000-byte image, text.
func mediaTools() [][]byte {
	return [][]byte{text("c0", 4000), imageBlock(400000), text("c2", 4000)}
}

// mediaTrimTokens is the text a trim of client tool 0 from 4000 to 2000 bytes saves: 2000 bytes at 4 a token.
const mediaTrimTokens = 500

// mediaUsage bills the forwarded tools, at 4 bytes a token for text and 1600 for the image, and a
// 4000-byte message.
func mediaUsage(addedBytes int) Usage {
	return Usage{Input: (addedBytes+2000+4000+4000+wrapper)/4 + 1600}
}

func assertTextTrim(t *testing.T, res Result) {
	t.Helper()
	noChecks(t, res)
	if got := tierSum(findOp(t, res, "s").Saved); got < mediaTrimTokens-2 || got > mediaTrimTokens+2 {
		t.Fatalf("saved %d, want about %d: the trim cut text only", got, mediaTrimTokens)
	}
}

// Control: no added content. The trim of client tool 0 saves text.
func TestIssue96ControlTrimBeforeAnImageSavesText(t *testing.T) {
	tools := mediaTools()
	ms := [][]byte{text("m0", 4000)}
	res := req{at: t0, client: NewBody(tools, nil, ms), facts: exact5m, usage: mediaUsage(0),
		ops: []Operation{rewrite("s", "context-guru.trim", "cg:trim:t0", tool(0), 4000, 2000)},
		fwd: NewBody([][]byte{text("r0", 2000), tools[1], tools[2]}, nil, ms)}.run(NewTrackers())
	assertTextTrim(t, res)
}

// The host adds X at tool(0), so the trim is reported at tool(1). Read as a client index, its before range
// is the image, and part of the saving is priced as media.
func TestIssue96TrimAfterAddedContentReadsItsOwnClientItemForMedia(t *testing.T) {
	tools := mediaTools()
	ms := [][]byte{text("m0", 4000)}
	res := req{at: t0, client: NewBody(tools, nil, ms), facts: exact5m, usage: mediaUsage(400),
		ops: []Operation{rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(0), 0, 400),
			rewrite("s", "context-guru.trim", "cg:trim:t0", tool(1), 4000, 2000)},
		fwd: NewBody([][]byte{text("x", 400), text("r0", 2000), tools[1], tools[2]}, nil, ms)}.run(NewTrackers())
	assertTextTrim(t, res)
}

// On request 2 the host stops adding X and keeps the trim. The trim's stored position still counts X, so
// read in the forwarded body without the ended X, its after range is the image.
func TestIssue96TrimAfterEndedAddedContentReadsItsOwnForwardedItemForMedia(t *testing.T) {
	ts := NewTrackers()
	tools := mediaTools()
	ms := [][]byte{text("m0", 4000)}
	noChecks(t, req{at: t0, client: NewBody(tools, nil, ms), facts: exact5m, usage: mediaUsage(400),
		ops: []Operation{rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(0), 0, 400),
			rewrite("s", "context-guru.trim", "cg:trim:t0", tool(1), 4000, 2000)},
		fwd: NewBody([][]byte{text("x", 400), text("r0", 2000), tools[1], tools[2]}, nil, ms)}.run(ts))
	res := req{at: t0.Add(10 * time.Second), client: NewBody(tools, nil, ms), facts: exact5m, usage: mediaUsage(0),
		fwd: NewBody([][]byte{text("r0", 2000), tools[1], tools[2]}, nil, ms)}.run(ts)
	if got := activeIDs(ts); len(got) != 1 || got[0] != "cg:trim:t0" {
		t.Fatalf("Active = %v, want only cg:trim:t0", got)
	}
	noChecks(t, res)
	p := findOp(t, res, "active:cg:trim:t0")
	if got := tierSum(p.Saved); got < mediaTrimTokens-2 || got > mediaTrimTokens+2 {
		t.Fatalf("saved %d, want about %d: the trim cut text only", got, mediaTrimTokens)
	}
}

// accountant#96, the forwarded side of a cost: the host adds X at tool(0) and grows client tool 0 from 1000 to
// 1500 bytes, at tool(1) in its output. On request 2, after the TTL, it stops adding X and keeps the growth, which is
// forwarded at tool(0) with a breakpoint on it, so its cost is a cache write. Read without taking out the ended X, it is
// at tool(1), after the breakpoint, and its cost is priced as input.
func TestIssue96GrowthAfterEndedAddedContentIsPricedAtItsOwnForwardedItem(t *testing.T) {
	ts := NewTrackers()
	tools := shiftTools()
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	client := mk(tools, system, ms)
	noChecks(t, req{at: t0, client: client, facts: exact5m, usage: Usage{Input: 340 + 1500 + 2200 + 12000},
		ops: []Operation{rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(0), 0, 340),
			rewrite("g", "context-guru.expand", "cg:expand:grow-t0", tool(1), 1000, 1500)},
		fwd: mk([]blk{b("x", 340), b("g0", 1500), tools[1], tools[2]}, system, ms)}.run(ts))
	// after the TTL, so the request is cold and the breakpoint, not B*, decides the tier
	res := req{at: t0.Add(10 * time.Minute), client: client, facts: exact5m,
		fwd: mk([]blk{b("g0", 1500), tools[1], tools[2]}, system, ms), fwdBPs: bps5(tool(0)),
		usage: Usage{CacheWrite: 1500, Input: 2200 + 12000}}.run(ts)
	if got := activeIDs(ts); len(got) != 1 || got[0] != "cg:expand:grow-t0" {
		t.Fatalf("Active = %v, want only cg:expand:grow-t0", got)
	}
	noChecks(t, res)
	if p := findOp(t, res, "active:cg:expand:grow-t0"); p.Caused != (TierTokens{CacheWrite5m: 500}) {
		t.Fatalf("caused %+v, want 500 cache write", p.Caused)
	}
}

// accountant#96, the interaction (§6.2): the issue 19 case in system. A ping rescued the request and kept
// s0 and s1 (to the breakpoint on s1). The host adds X after s0 and masks s1, at sys(2) in its output. The
// mask saves 1201 tokens at the read tier, and the interaction books them write -> read. Read as a client
// index, sys(2) is s2, past the ping's extent, and the interaction is lost.
func TestIssue96InteractionReadsTheMaskedClientItem(t *testing.T) {
	system := []blk{b("s0", 1255), b("s1", 1214), b("s2", 50)}
	ms := []blk{b("m1", 100)}
	client := mk(nil, system, ms)
	f := pingFacts()
	f.PingRead = ptr(sys(1))
	ts := NewTrackers()
	noChecks(t, req{at: t0.Add(20 * time.Minute), client: client, clientBPs: bps5(sys(1)), facts: f,
		usage: Usage{CacheWrite: 2469, Input: 150}}.run(ts))
	res := req{at: t0.Add(28 * time.Minute), client: client, clientBPs: bps5(sys(1)), facts: f,
		ops: []Operation{rewrite("a", "context-guru.expand", "cg:expand:decl-added", sys(1), 0, 40),
			rewrite("op1", "mask", "mask:1", sys(2), 1214, 13)},
		fwd:    mk(nil, []blk{b("s0", 1255), b("x", 40), b("s1m", 13), b("s2", 50)}, ms),
		fwdBPs: bps5(sys(2)), usage: Usage{CacheRead: 1255, CacheWrite: 53, Input: 150}}.run(ts)
	noChecks(t, res)
	if p := findOp(t, res, "op1"); p.Saved != (TierTokens{CacheRead: 1201}) {
		t.Fatalf("mask saved %+v, want 1201 booked at the read tier", p.Saved)
	}
	if res.Interaction != (TierTokens{CacheWrite5m: 1201}) || res.InteractionCaused != (TierTokens{CacheRead: 1201}) {
		t.Fatalf("interaction %+v caused %+v, want 1201 write -> read", res.Interaction, res.InteractionCaused)
	}
}

// attributionSession is the accountant#96 setup for cache-effect attribution: the host adds X at tool(0) and
// trims client tool 1 (1200 -> 600), at tool(2) in its output. Request 1 has a client breakpoint on tool 0.
// Request 2, after the TTL, has the client breakpoints clientBPs, stops adding X, keeps the trim, and forwards the
// breakpoints fwdBPs.
func attributionSession(t *testing.T, clientBPs, fwdBPs []Breakpoint, usage Usage) Result {
	t.Helper()
	ts := NewTrackers()
	tools := shiftTools()
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	client := mk(tools, system, ms)
	noChecks(t, req{at: t0, client: client, clientBPs: bps5(tool(0)), fwdBPs: bps5(tool(1)), facts: exact5m,
		usage: Usage{CacheWrite: 1340, Input: 600 + 1000 + 12000},
		ops: []Operation{rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(0), 0, 340),
			rewrite("s", "context-guru.trim", "cg:trim:t1", tool(2), 1200, 600)},
		fwd: mk([]blk{b("x", 340), tools[0], b("r1", 600), tools[2]}, system, ms)}.run(ts))
	// after the TTL: a cold request has no row 1 break, which would price the writes itself
	res := req{at: t0.Add(10 * time.Minute), client: client, clientBPs: clientBPs, facts: exact5m,
		fwd: mk([]blk{tools[0], b("r1", 600), tools[2]}, system, ms), fwdBPs: fwdBPs, usage: usage}.run(ts)
	if got := activeIDs(ts); len(got) != 1 || got[0] != "cg:trim:t1" {
		t.Fatalf("Active = %v, want only cg:trim:t1", got)
	}
	noChecks(t, res)
	return res
}

// The host adds a breakpoint on the trimmed item, at tool(1) once X ended. The markPremium has the
// ChangeID of the operation whose span covers that breakpoint: the trim. Read without taking out the ended X,
// the trim is at tool(2), and the premium names no change.
func TestIssue96MarkPremiumNamesTheChangeAfterEndedAddedContent(t *testing.T) {
	res := attributionSession(t, bps5(tool(0)), bps5(tool(0), tool(1)), Usage{CacheWrite: 1600, Input: 13000})
	if e := effect(t, res, EffectMarkPremium); e.ChangeID != "cg:trim:t1" {
		t.Fatalf("markPremium ChangeID = %q, want cg:trim:t1", e.ChangeID)
	}
}

// The client sets a 5m breakpoint on tool 1, and the host forwards it at 1h on the trimmed item. The
// ttlPremium has the ChangeID of the operation that covers that 1h breakpoint: the trim.
func TestIssue96TTLPremiumNamesTheChangeAfterEndedAddedContent(t *testing.T) {
	res := attributionSession(t, bps5(tool(1)), []Breakpoint{{Pos: tool(1), TTL: "1h"}},
		Usage{CacheWrite: 1600, CacheWrite1h: 1600, Input: 13000})
	if e := effect(t, res, EffectTTLPremium); e.ChangeID != "cg:trim:t1" {
		t.Fatalf("ttlPremium ChangeID = %q, want cg:trim:t1", e.ChangeID)
	}
}

// accountant#96, §5.2 row 1: the host adds X at tool(0) and trims client tool 0, at tool(1) in its output. On
// request 2 it stops adding X and keeps the trim. The first difference is X's slot, where the trimmed item now
// sits. X's end changed that position, not the trim, which forwards the same bytes as before. A break that a
// change leaves behind when it ends has no ChangeID.
func TestIssue96ARowOneBreakThatEndedAddedContentLeavesNamesNoChange(t *testing.T) {
	_, res := shiftSession(t, shiftTools(), []blk{b("r0", 600), shiftTools()[1], shiftTools()[2]})
	noChecks(t, res)
	if e := effect(t, res, EffectBreak); e.ChangeID != "" {
		t.Fatalf("break ChangeID = %q, want none: the ended X left it", e.ChangeID)
	}
}

// The host stops adding X and, in the same request, first trims client tool 0 (reported at tool(0), as X is
// gone from its input). The section has the client's item count, and the one item that differs has the size
// the reported trim gives it, so X ends (accountant#89). On main only an active in-place change could explain
// a size, so X stayed and the sum check failed. The trim is new and sits at the first difference, so it
// changed that position too, and the row 1 break names it.
func TestIssue96AddedContentEndsWhenAReportedChangeExplainsTheSection(t *testing.T) {
	ts := NewTrackers()
	tools := shiftTools()
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	client := mk(tools, system, ms)
	noChecks(t, req{at: t0, client: client, clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(0), 0, 340)},
		fwd: mk(with([]blk{b("x", 340)}, tools...), system, ms), usage: Usage{CacheWrite: 5540, Input: 10000}}.run(ts))
	res := req{at: t0.Add(10 * time.Second), client: client, clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{rewrite("s", "context-guru.trim", "cg:trim:t0", tool(0), 1000, 600)},
		fwd: mk([]blk{b("r0", 600), tools[1], tools[2]}, system, ms), usage: Usage{CacheWrite: 4800, Input: 10000}}.run(ts)
	if got := activeIDs(ts); len(got) != 1 || got[0] != "cg:trim:t0" {
		t.Fatalf("Active = %v, want only cg:trim:t0", got)
	}
	noChecks(t, res)
	if e := effect(t, res, EffectBreak); e.ChangeID != "cg:trim:t0" {
		t.Fatalf("break ChangeID = %q, want cg:trim:t0: the new trim changed the position too", e.ChangeID)
	}
}

// PR #104 review: a change reported in the request where added content ends never saw that content, so its
// position does not count it. Request 1 adds X after client tool 0. On request 2 the client edits tool 0, so X
// ends (Q12), and the host does not add X. A new change of client tool 2 is reported at tool(2).
func reportedAfterEnded(t *testing.T, op Operation, fwd2 []byte, usage Usage) Result {
	t.Helper()
	tools := mediaTools()
	ms := [][]byte{text("m0", 4000)}
	ts := NewTrackers()
	noChecks(t, req{at: t0, client: NewBody(tools, nil, ms), facts: exact5m, usage: mediaUsage(400),
		ops: []Operation{rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(1), 0, 400)},
		fwd: NewBody([][]byte{tools[0], text("x", 400), tools[1], tools[2]}, nil, ms)}.run(ts))
	client2 := [][]byte{text("d0", 4000), tools[1], tools[2]}
	res := req{at: t0.Add(10 * time.Second), client: NewBody(client2, nil, ms), facts: exact5m, usage: usage,
		ops: []Operation{op}, fwd: NewBody([][]byte{client2[0], tools[1], fwd2}, nil, ms)}.run(ts)
	if got := activeIDs(ts); len(got) != 1 || got[0] != op.ChangeID {
		t.Fatalf("Active = %v, want only %s", got, op.ChangeID)
	}
	noChecks(t, res)
	return res
}

func TestIssue96AReportedTrimAfterEndedAddedContentReadsItsOwnItem(t *testing.T) {
	res := reportedAfterEnded(t, rewrite("s", "context-guru.trim", "cg:trim:t2", tool(2), 4000, 2000),
		text("r2", 2000), mediaUsage(0))
	if got := tierSum(findOp(t, res, "s").Saved); got < mediaTrimTokens-2 || got > mediaTrimTokens+2 {
		t.Fatalf("saved %d, want about %d: the trim cut text only", got, mediaTrimTokens)
	}
}

func TestIssue96AReportedGrowthAfterEndedAddedContentReadsItsOwnItem(t *testing.T) {
	res := reportedAfterEnded(t, rewrite("g", "context-guru.expand", "cg:expand:grow-t2", tool(2), 4000, 6000),
		text("g2", 6000), Usage{Input: (4000+6000+4000+wrapper)/4 + 1600})
	if got := tierSum(findOp(t, res, "g").Caused); got < 498 || got > 502 {
		t.Fatalf("caused %d, want about 500: the growth added text only", got)
	}
}

// The #89 rule with a reported trim after X's slot: the host stops adding X at tool(0) and first trims client
// tool 1, which it reports at tool(1), as X is not in its input. X ends, and on request 3 the trim stays at
// client tool 1.
func TestIssue96AddedContentEndsWhenAReportedChangeAfterItsSlotExplainsTheSection(t *testing.T) {
	ts := NewTrackers()
	tools := shiftTools()
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	client := mk(tools, system, ms)
	noChecks(t, req{at: t0, client: client, clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{rewrite("a", "context-guru.expand", "cg:expand:decl-added", tool(0), 0, 340)},
		fwd: mk(with([]blk{b("x", 340)}, tools...), system, ms), usage: Usage{CacheWrite: 5540, Input: 10000}}.run(ts))
	fwd := mk([]blk{tools[0], b("r1", 600), tools[2]}, system, ms)
	res := req{at: t0.Add(10 * time.Second), client: client, clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		ops: []Operation{rewrite("s", "context-guru.trim", "cg:trim:t1", tool(1), 1200, 600)},
		fwd: fwd, usage: Usage{CacheWrite: 4600, Input: 10000}}.run(ts)
	if got := activeIDs(ts); len(got) != 1 || got[0] != "cg:trim:t1" {
		t.Fatalf("Active = %v, want only cg:trim:t1", got)
	}
	noChecks(t, res)
	res = req{at: t0.Add(20 * time.Second), client: client, clientBPs: bps5(sys(0), msg(1)), facts: exact5m,
		fwd: fwd, usage: Usage{CacheRead: 4600, Input: 10000}}.run(ts)
	if got := activeIDs(ts); len(got) != 1 || got[0] != "cg:trim:t1" {
		t.Fatalf("request 3: Active = %v, want only cg:trim:t1", got)
	}
	noChecks(t, res)
}
