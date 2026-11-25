package engine

import (
	"testing"
	"time"
)

// accountant#108: a pruner deletes tool 2, which holds the client's cache mark, and moves the mark onto the
// last survivor, tool 1 (an in-place change, +37 bytes). On the next request the pruner deletes tool 1 too
// and moves the mark onto tool 0. The forwarded body has no item for the first mark move any more.
func TestAnInPlaceChangeEndsWhenALaterChangeDeletesItsItem(t *testing.T) {
	ts := NewTrackers()
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	client := mk([]blk{b("read", 1000), b("bash", 800), b("nb", 600)}, system, ms)
	rmNB := rewrite("rm-nb", "tool-prune", "tool-prune:nb", tool(2), 600, 0)
	rmBash := rewrite("rm-bash", "tool-prune", "tool-prune:bash", tool(1), 800, 0)
	markBash := rewrite("mark-bash", "tool-prune", "tool-prune:marker:bash", tool(1), 800, 837)
	markRead := rewrite("mark-read", "tool-prune", "tool-prune:marker:read", tool(0), 1000, 1037)

	run := func(i int, ops []Operation, fwdTools []blk) Result {
		return req{at: t0.Add(time.Duration(i) * 10 * time.Second), client: client, clientBPs: bps5(tool(2), msg(1)),
			facts: exact5m, ops: ops, fwd: mk(fwdTools, system, ms), usage: Usage{CacheWrite: 3500}}.run(ts)
	}
	noChecks(t, run(0, []Operation{rmNB, markBash}, []blk{b("read", 1000), b("bash+", 837)}))
	for i := 1; i <= 2; i++ {
		res := run(i, []Operation{rmBash, rmNB, markRead}, []blk{b("read+", 1037)})
		for _, c := range res.Checks {
			t.Logf("request %d: %+v", i+1, c)
		}
		noChecks(t, res)
		for _, id := range activeIDs(ts) {
			if id == markBash.ChangeID {
				t.Fatalf("request %d: Active = %v, want no %s", i+1, activeIDs(ts), markBash.ChangeID)
			}
		}
	}
}

// Negative control: the pruner repeats the same prune and the same mark move. The mark move stays.
func TestAnInPlaceChangeStaysWhileItsItemIsForwarded(t *testing.T) {
	ts := NewTrackers()
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	client := mk([]blk{b("read", 1000), b("bash", 800), b("nb", 600)}, system, ms)
	rmNB := rewrite("rm-nb", "tool-prune", "tool-prune:nb", tool(2), 600, 0)
	markBash := rewrite("mark-bash", "tool-prune", "tool-prune:marker:bash", tool(1), 800, 837)
	fwd := mk([]blk{b("read", 1000), b("bash+", 837)}, system, ms)
	for i := 0; i < 3; i++ {
		ops := []Operation{rmNB}
		if i == 0 {
			ops = append(ops, markBash) // reported once, then active
		}
		noChecks(t, req{at: t0.Add(time.Duration(i) * 10 * time.Second), client: client, clientBPs: bps5(tool(2), msg(1)),
			facts: exact5m, ops: ops, fwd: fwd, usage: Usage{CacheWrite: 3500}}.run(ts))
	}
	found := false
	for _, id := range activeIDs(ts) {
		found = found || id == markBash.ChangeID
	}
	if !found {
		t.Fatalf("Active = %v, want %s", activeIDs(ts), markBash.ChangeID)
	}
}

// Negative control: a later operator deletes the output of the mark move (its SizeBefore is 837, not the
// client's 800). That is a chain, so the mark move stays and the deletion prices the rest.
func TestAnInPlaceChangeStaysWhenADeletionReadItsOutput(t *testing.T) {
	ts := NewTrackers()
	system := []blk{b("s0", 2000)}
	ms := []blk{b("m0", 5000), b("m1", 5000)}
	client := mk([]blk{b("read", 1000), b("bash", 800), b("nb", 600)}, system, ms)
	rmNB := rewrite("rm-nb", "tool-prune", "tool-prune:nb", tool(2), 600, 0)
	markBash := rewrite("mark-bash", "tool-prune", "tool-prune:marker:bash", tool(1), 800, 837)
	rmMarked := rewrite("rm-marked", "other-prune", "other-prune:bash", tool(1), 837, 0)
	run := func(i int, ops []Operation, fwdTools []blk) Result {
		return req{at: t0.Add(time.Duration(i) * 10 * time.Second), client: client, clientBPs: bps5(tool(2), msg(1)),
			facts: exact5m, ops: ops, fwd: mk(fwdTools, system, ms), usage: Usage{CacheWrite: 3500}}.run(ts)
	}
	noChecks(t, run(0, []Operation{rmNB, markBash}, []blk{b("read", 1000), b("bash+", 837)}))
	noChecks(t, run(1, []Operation{rmNB, rmMarked}, []blk{b("read", 1000)}))
	found := false
	for _, id := range activeIDs(ts) {
		found = found || id == markBash.ChangeID
	}
	if !found {
		t.Fatalf("Active = %v, want %s", activeIDs(ts), markBash.ChangeID)
	}
}
