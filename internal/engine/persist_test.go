package engine

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// A restart must not lose the active cut: the result after Snapshot and Restore equals the result
// with no restart, and a set made fresh (no Restore) does lose it.
func TestSnapshotRestoreKeepsTheActiveChange(t *testing.T) {
	tools := []blk{b("t0", 1000), b("t1", 1000), b("t2", 3000)}
	fwdTools := tools[:2]
	sysB := []blk{b("sys", 3000)}
	op := Operation{ID: "op1", Kind: KindRewrite, Operator: "tool-prune", ChangeID: "tp:x", At: tool(2),
		SizeBefore: Size{Bytes: 3000}}
	m0 := msgs(1, 1000)
	ts := NewTrackers()
	req{at: t0, client: mk(tools, sysB, m0), clientBPs: bps5(sys(0), msg(0)), ops: []Operation{op},
		fwd: mk(fwdTools, sysB, m0), facts: exact5m, usage: Usage{CacheWrite: 7000}}.run(ts)
	m1 := msgs(2, 1000)
	next := req{at: t0.Add(time.Minute), client: mk(tools, sysB, m1), clientBPs: bps5(sys(0), msg(1)),
		fwd: mk(fwdTools, sysB, m1), facts: exact5m, usage: Usage{CacheRead: 7000, CacheWrite: 1000}}

	restored := NewTrackers()
	snap := ts.Snapshot()
	if err := restored.Restore(snap); err != nil {
		t.Fatal(err)
	}
	if string(restored.Snapshot()) != string(snap) {
		t.Fatal("Snapshot after Restore must give the same bytes")
	}
	want := next.run(ts)
	got := next.run(restored)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after restore: %+v\nwant: %+v", got, want)
	}
	if p := findOp(t, got, "active:tp:x"); p.Saved.CacheRead == 0 {
		t.Fatalf("saved %+v, want a cache read saving", p.Saved)
	}
	lost := next.run(NewTrackers())
	if len(lost.Ops) != 0 {
		t.Fatalf("a fresh set must not know the change: %+v", lost.Ops)
	}
}

func TestRestoreRefusesBadInputAndChangesNothing(t *testing.T) {
	ts := NewTrackers()
	seenAt(ts, "s", "a", t0)
	before := ts.Snapshot()
	for _, in := range []string{"", "{", `{"version":99}`} {
		if ts.Restore([]byte(in)) == nil {
			t.Fatalf("Restore(%q) must fail", in)
		}
	}
	if string(ts.Snapshot()) != string(before) {
		t.Fatal("a failed Restore changed the state")
	}
}

func TestTrimmedCountsDroppedThreads(t *testing.T) {
	ts := NewTrackers()
	if ts.Trimmed() != 0 {
		t.Fatal("a new set has trimmed nothing")
	}
	for i := range maxThreads + 5 {
		seenAt(ts, "s", fmt.Sprint("t", i), t0.Add(time.Duration(i)*time.Millisecond))
	}
	trimOnce(ts)
	if want := uint64(maxThreads + 5 - (maxThreads - maxThreads/10)); ts.Trimmed() != want {
		t.Fatalf("Trimmed = %d, want %d", ts.Trimmed(), want)
	}
}
