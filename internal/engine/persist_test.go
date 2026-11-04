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
	snap := mustSnap(t, ts)
	if err := restored.Restore(snap); err != nil {
		t.Fatal(err)
	}
	if string(mustSnap(t, restored)) != string(snap) {
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
	before := mustSnap(t, ts)
	for _, in := range []string{"", "{", `{"version":99}`, `{"version":1}`,
		`{"version":1,"threads":[{"key":""}]}`,
		`{"version":1,"threads":[{"key":"a"},{"key":"a"}]}`,
		`{"version":1,"threads":[{"key":"a","prevPrint":{"client":{"pos":[{"hash":"x"}]}}}]}`} {
		if ts.Restore([]byte(in)) == nil {
			t.Fatalf("Restore(%q) must fail", in)
		}
	}
	if string(mustSnap(t, ts)) != string(before) {
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

func mustSnap(t *testing.T, ts *Trackers) []byte {
	t.Helper()
	b, err := ts.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The splices of the saved fingerprint map Cached back into client space. Drop them and the hot
// request after a summary prices differently.
func TestSnapshotRestoreKeepsTheSplices(t *testing.T) {
	tools, sysB := []blk{b("t", 4000)}, []blk{b("sys", 4000)}
	client := []blk{b("m0", 4000), b("m1", 3000), b("m2", 3000), b("m3", 1500), b("m4", 1500)}
	fwd := []blk{b("S", 500), b("m3", 1500), b("m4", 1500)}
	sum := rewrite("op2", "context-guru.summarize", "cg:summarize:5d21", msg(0), 10000, 500)
	sum.MessageSplice = &MessageSplice{At: 0, Removed: 3, Inserted: 1}
	ts := NewTrackers()
	req{at: t0, client: mk(tools, sysB, client), clientBPs: bps5(sys(0), msg(4)), facts: exact5m,
		usage: Usage{CacheWrite: 21000}}.run(ts)
	req{at: t0.Add(time.Minute), client: mk(tools, sysB, client), clientBPs: bps5(sys(0), msg(4)),
		ops: []Operation{sum}, fwd: mk(tools, sysB, fwd), fwdBPs: bps5(sys(0), msg(2)), facts: exact5m,
		usage: Usage{CacheRead: 8000, CacheWrite: 3500}}.run(ts)
	restored := NewTrackers()
	if err := restored.Restore(mustSnap(t, ts)); err != nil {
		t.Fatal(err)
	}
	clientB, fwdB := with(client, b("m5", 500)), with(fwd, b("m5", 500))
	next := req{at: t0.Add(2 * time.Minute), client: mk(tools, sysB, clientB), clientBPs: bps5(sys(0), msg(5)),
		fwd: mk(tools, sysB, fwdB), fwdBPs: bps5(sys(0), msg(3)), facts: exact5m,
		usage: Usage{CacheRead: 11500, CacheWrite: 500}}
	want := next.run(ts)
	got := next.run(restored)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after restore: %+v\nwant: %+v", got, want)
	}
	if p := findOp(t, got, "active:cg:summarize:5d21"); p.Saved.CacheRead != 9500 {
		t.Fatalf("saved %+v, want 9500 read", p.Saved)
	}
}

// Threads made in two orders give the same bytes.
func TestSnapshotIsTheSameForAnyBuildOrder(t *testing.T) {
	a, b2 := NewTrackers(), NewTrackers()
	for i := range 20 {
		seenAt(a, "s", fmt.Sprint("t", i), t0.Add(time.Duration(i)*time.Second))
	}
	for i := 19; i >= 0; i-- {
		seenAt(b2, "s", fmt.Sprint("t", i), t0.Add(time.Duration(i)*time.Second))
	}
	if string(mustSnap(t, a)) != string(mustSnap(t, b2)) {
		t.Fatal("build order changed the bytes")
	}
}

func TestSnapshotReturnsAnErrorForAnUnencodableTime(t *testing.T) {
	ts := NewTrackers()
	seenAt(ts, "s", "a", time.Date(12000, 1, 1, 0, 0, 0, 0, time.UTC))
	if _, err := ts.Snapshot(); err == nil {
		t.Fatal("want an error for a year past 9999")
	}
}
