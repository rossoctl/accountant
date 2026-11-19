package engine

import (
	"fmt"
	"testing"
	"time"
)

// age is the pair (ActiveOps, OldestActiveTurns) of a Result.
type age struct{ count, oldest int }

func ageOf(r Result) age { return age{r.ActiveOps, r.OldestActiveTurns} }

// accountant#91: the count of active changes and the age of the oldest. A change ages by one on each
// request that applies it again. A report of the same change again does not make it new. An end sets
// both values back to 0.
func TestActiveOpsAndAgeFollowAChange(t *testing.T) {
	opX := rewrite("opX", "context-guru.extract", "cg:extract:m2", msg(2), 5302, 5148)
	_, res := inPlaceSession(t,
		inPlaceStep{size: 5302},                        // nothing active yet
		inPlaceStep{size: 5148, ops: []Operation{opX}}, // X enters
		inPlaceStep{size: 5148},                        // X is applied again
		inPlaceStep{size: 5148, ops: []Operation{opX}}, // the host reports X again: it is not new
		inPlaceStep{size: 5148},                        // X is applied again
		inPlaceStep{size: 5302},                        // the client size: X ended
	)
	want := []age{{0, 0}, {1, 0}, {1, 1}, {1, 2}, {1, 3}, {0, 0}}
	for i, r := range res {
		noChecks(t, r)
		if got := ageOf(r); got != want[i] {
			t.Fatalf("request %d: (ActiveOps, OldestActiveTurns) = %v, want %v", i, got, want[i])
		}
	}
}

// accountant#70 cause A, as seen through the two values: X ends when Y takes over the item, so the
// count stays at 1 and the oldest is Y, not X. Before the fix, X stayed active: the count would be 2
// and the age would keep the request on which X entered.
func TestATakenOverChangeLeavesOnlyTheNewOneActive(t *testing.T) {
	opX := rewrite("opX", "context-guru.extract", "cg:extract:m2", msg(2), 5302, 5148)
	opY := rewrite("opY", "context-guru.extract_llm_sweep", "cg:sweep:m2", msg(2), 5302, 299)
	_, res := inPlaceSession(t,
		inPlaceStep{size: 5302},
		inPlaceStep{size: 5148, ops: []Operation{opX}},
		inPlaceStep{size: 5148},
		inPlaceStep{size: 299, ops: []Operation{opY}}, // Y takes over; X is not reported again
		inPlaceStep{size: 299},
	)
	want := []age{{0, 0}, {1, 0}, {1, 1}, {1, 0}, {1, 1}}
	for i, r := range res {
		if got := ageOf(r); got != want[i] {
			t.Fatalf("request %d: (ActiveOps, OldestActiveTurns) = %v, want %v", i, got, want[i])
		}
	}
}

// The count rises with each change that stays, and the age is that of the oldest change.
func TestActiveOpsCountsEveryChangeAndAgesTheOldest(t *testing.T) {
	ts := NewTrackers()
	client := with(msgs(9, 3000), b("m9", 3000))
	fwd := append([]blk(nil), client...)
	var res []Result
	for i := range 3 {
		var ops []Operation
		if i > 0 { // request 1 masks message 1, request 2 masks message 3
			m := 2*i - 1
			ops = []Operation{rewrite(fmt.Sprintf("op%d", i), "context-guru.mask", fmt.Sprintf("cg:mask:m%d", m), msg(m), 3000, 100)}
			fwd = append([]blk(nil), fwd...)
			fwd[m] = b(fmt.Sprintf("k%d", m), 100)
		}
		res = append(res, req{at: t0.Add(time.Duration(i) * 10 * time.Second), client: mk(nil, ex2Sys, client),
			clientBPs: bps5(sys(0), msg(9)), ops: ops, fwd: mk(nil, ex2Sys, fwd), facts: exact5m,
			usage: Usage{CacheWrite: 30000}}.run(ts))
	}
	want := []age{{0, 0}, {1, 0}, {2, 1}}
	for i, r := range res {
		noChecks(t, r)
		if got := ageOf(r); got != want[i] {
			t.Fatalf("request %d: (ActiveOps, OldestActiveTurns) = %v, want %v", i, got, want[i])
		}
	}
}

// A restart keeps the age. A snapshot made before the record existed restores without error, and its
// changes age from the next request.
func TestSnapshotRestoreKeepsTheAge(t *testing.T) {
	opX := rewrite("opX", "context-guru.extract", "cg:extract:m2", msg(2), 5302, 5148)
	ts, _ := inPlaceSession(t, inPlaceStep{size: 5302}, inPlaceStep{size: 5148, ops: []Operation{opX}},
		inPlaceStep{size: 5148})
	st := ts.tracker(ThreadKey("s1", Thread{ID: "main"}), time.Time{}).snapshot()
	if st.Requests != 3 || st.FirstApplied["cg:extract:m2"] != 2 {
		t.Fatalf("Requests %d, FirstApplied %v; want 3 and cg:extract:m2 at 2", st.Requests, st.FirstApplied)
	}

	restored := NewTrackers()
	if err := restored.Restore(mustSnap(t, ts)); err != nil {
		t.Fatal(err)
	}
	rs := restored.tracker(ThreadKey("s1", Thread{ID: "main"}), time.Time{}).snapshot()
	if rs.Requests != 3 || rs.FirstApplied["cg:extract:m2"] != 2 {
		t.Fatalf("after Restore: Requests %d, FirstApplied %v; want 3 and cg:extract:m2 at 2", rs.Requests, rs.FirstApplied)
	}

	old := NewTrackers()
	old.mu.Lock()
	old.m = map[string]*Tracker{ThreadKey("s1", Thread{ID: "main"}): {state: ThreadState{Active: st.Active, Version: st.Version}}}
	old.mu.Unlock()
	fromOld := NewTrackers()
	if err := fromOld.Restore(mustSnap(t, old)); err != nil {
		t.Fatalf("a snapshot with no record must restore: %v", err)
	}
	if count, oldest := activeAge(fromOld.tracker(ThreadKey("s1", Thread{ID: "main"}), time.Time{}).snapshot()); count != 1 || oldest != 0 {
		t.Fatalf("after an old snapshot: (%d, %d), want (1, 0)", count, oldest)
	}
}

// EndRequest stores a reported change before Compute counts the request. A request that fails before
// Compute keeps the change, and the change ages from that request.
func TestAChangeAgesFromAnEndRequestWithNoCompute(t *testing.T) {
	ts := NewTrackers()
	client := with(msgs(9, 3000), b("m9", 3000))
	client[2] = b("c2", 5302)
	fwd := append([]blk(nil), client...)
	fwd[2] = b("f2", 5148)
	opX := rewrite("opX", "context-guru.extract", "cg:extract:m2", msg(2), 5302, 5148)
	base := req{client: mk(nil, ex2Sys, client), clientBPs: bps5(sys(0), msg(9)), fwd: mk(nil, ex2Sys, fwd),
		facts: exact5m, usage: Usage{CacheWrite: 40000}}

	q := base
	q.at, q.ops = t0, []Operation{opX}
	q.begin(ts) // EndRequest, and no Compute: the request failed
	st := ts.tracker(ThreadKey("s1", Thread{ID: "main"}), time.Time{}).snapshot()
	if st.Requests != 0 || st.FirstApplied["cg:extract:m2"] != 1 {
		t.Fatalf("Requests %d, FirstApplied %v; want 0 and cg:extract:m2 at 1", st.Requests, st.FirstApplied)
	}
	if err := NewTrackers().Restore(mustSnap(t, ts)); err != nil {
		t.Fatalf("a snapshot between EndRequest and Compute must restore: %v", err)
	}
	for i, want := range []age{{1, 0}, {1, 1}} {
		q := base
		q.at = t0.Add(time.Duration(i+1) * 10 * time.Second)
		if got := ageOf(q.run(ts)); got != want {
			t.Fatalf("request %d after the failed one: %v, want %v", i, got, want)
		}
	}
}

func TestRestoreRefusesAChangeFromAfterTheNextRequest(t *testing.T) {
	in := `{"version":1,"threads":[{"key":"a","cache":{},"stateVersion":1,"requests":2,` +
		`"active":[{"kind":"rewrite","changeId":"c","at":{"section":2,"index":0},"sizeBefore":{"bytes":1},"sizeAfter":{"bytes":0}}],` +
		`"firstApplied":{"c":4}}]}`
	if NewTrackers().Restore([]byte(in)) == nil {
		t.Fatal("Restore must refuse a change that entered after the next request")
	}
}
