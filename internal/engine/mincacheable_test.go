package engine

import "testing"

// §5.1: a prompt below the minimum cacheable length caches nothing, so a cut in it is priced at
// Input and not at the cache-write rate (accountant#15).
func shortPrompt(facts EntryFacts, usage Usage) Result {
	return req{at: t0, client: mk(nil, nil, []blk{b("m0", 2100)}), clientBPs: bps5(msg(0)),
		ops: []Operation{rewrite("a", "op", "c:1", msg(0), 2100, 600)},
		fwd: mk(nil, nil, []blk{b("m0", 600)}), fwdBPs: bps5(msg(0)), facts: facts, usage: usage}.run(NewTrackers())
}

func TestAShortPromptCutIsPricedAsInputWithMinCacheable(t *testing.T) {
	f := exact5m
	f.MinCacheable = 1024
	res := shortPrompt(f, Usage{Input: 600})
	noChecks(t, res)
	if s := findOp(t, res, "a").Saved; s.Input != 1500 || s.CacheWrite5m != 0 {
		t.Fatalf("Saved = %+v, want Input=1500", s)
	}
}

func TestAShortPromptCutIsPricedAsInputWhenUsageShowsNoCache(t *testing.T) {
	res := shortPrompt(exact5m, Usage{Input: 600})
	if s := findOp(t, res, "a").Saved; s.Input != 1500 || s.CacheWrite5m != 0 {
		t.Fatalf("Saved = %+v, want Input=1500", s)
	}
}

func TestALongPromptCutKeepsTheWriteRateWithMinCacheable(t *testing.T) {
	f := exact5m
	f.MinCacheable = 1024
	res := req{at: t0, client: mk(nil, nil, []blk{b("m0", 3000)}), clientBPs: bps5(msg(0)),
		ops: []Operation{rewrite("a", "op", "c:1", msg(0), 3000, 1500)},
		fwd: mk(nil, nil, []blk{b("m0", 1500)}), fwdBPs: bps5(msg(0)), facts: f,
		usage: Usage{CacheWrite: 1500}}.run(NewTrackers())
	if s := findOp(t, res, "a").Saved; s.CacheWrite5m != 1500 {
		t.Fatalf("Saved = %+v, want CacheWrite5m=1500", s)
	}
}
