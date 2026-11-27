package engine

import (
	"testing"
	"time"
)

// Design §5.2: a fold that the host applies again, with the same bytes, never causes a miss. When a
// new change cuts the same message, the new change pays for the rewrite. The first changed position is
// covered by both, and the earlier fold used to win.
func TestARewriteIsChargedToTheNewChangeNotTheRepeatedOne(t *testing.T) {
	for _, reportAgain := range []bool{false, true} {
		name := "fold stays active"
		if reportAgain {
			name = "fold reported again"
		}
		t.Run(name, func(t *testing.T) {
			ts := NewTrackers()
			client := mk(nil, ex2Sys, msgs(12, 3000))
			fold := rewrite("f", "context-guru.searchfold", "cf", msg(6), 3000, 500)
			sweep1 := rewrite("s1", "context-guru.extract_llm_sweep", "cs1", msg(1), 3000, 100)
			fwd := func(sizes map[int]int) Body {
				ms := msgs(12, 3000)
				for k, v := range sizes {
					ms[k] = b("x"+itoa(k)+"s"+itoa(v), v)
				}
				return mk(nil, ex2Sys, ms)
			}
			bp := bps5(sys(0), msg(11))
			req{at: t0, client: client, clientBPs: bp, facts: exact5m, usage: Usage{CacheWrite: 36000}}.run(ts)
			req{at: t0.Add(10 * time.Second), client: client, clientBPs: bp, facts: exact5m,
				ops: []Operation{fold, sweep1}, fwd: fwd(map[int]int{6: 500, 1: 100}),
				usage: Usage{CacheRead: 3000, CacheWrite: 30000}}.run(ts)
			// The sweep makes a new cut on the folded message. The fold forwards what it forwarded before.
			sweep2 := rewrite("s2", "context-guru.extract_llm_sweep", "cs2", msg(6), 500, 100)
			ops := []Operation{sweep2}
			if reportAgain {
				ops = []Operation{fold, sweep1, sweep2}
			}
			res := req{at: t0.Add(20 * time.Second), client: client, clientBPs: bp, facts: exact5m, ops: ops,
				fwd: fwd(map[int]int{6: 100, 1: 100}), usage: Usage{CacheRead: 6000, CacheWrite: 20000}}.run(ts)
			if br := effect(t, res, EffectBreak); br.ChangeID != "cs2" {
				t.Fatalf("break charged to %q, want the new change cs2", br.ChangeID)
			}
		})
	}
}

// With no new change at the first changed position, the earlier change keeps the charge.
func TestARepeatedChangeKeepsTheChargeWhenNoNewChangeCoversTheFirstDifference(t *testing.T) {
	ts := NewTrackers()
	client := mk(nil, ex2Sys, msgs(12, 3000))
	fold := rewrite("f", "context-guru.searchfold", "cf", msg(6), 3000, 500)
	fwd := func(n int) Body {
		ms := msgs(12, 3000)
		ms[6] = b("x6s"+itoa(n), n)
		return mk(nil, ex2Sys, ms)
	}
	bp := bps5(sys(0), msg(11))
	req{at: t0, client: client, clientBPs: bp, facts: exact5m, usage: Usage{CacheWrite: 36000}}.run(ts)
	req{at: t0.Add(10 * time.Second), client: client, clientBPs: bp, facts: exact5m,
		ops: []Operation{fold}, fwd: fwd(500), usage: Usage{CacheRead: 3000, CacheWrite: 30000}}.run(ts)
	fold2 := rewrite("f", "context-guru.searchfold", "cf", msg(6), 3000, 200)
	res := req{at: t0.Add(20 * time.Second), client: client, clientBPs: bp, facts: exact5m,
		ops: []Operation{fold2}, fwd: fwd(200), usage: Usage{CacheRead: 6000, CacheWrite: 20000}}.run(ts)
	if br := effect(t, res, EffectBreak); br.ChangeID != "cf" {
		t.Fatalf("break charged to %q, want cf", br.ChangeID)
	}
}
