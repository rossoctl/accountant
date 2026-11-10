package engine

import "testing"

// accountant#33: the provider profile in EntryFacts. The zero value keeps the Anthropic behaviour.

// An OpenAI model before GPT-5.6 rounds the cached count down to a multiple of 128 (the zero value).
// A provider that reports the exact count has granularity 1, and the same count reaches less far.
func TestReadGranularityWidensOrNarrowsTheExtent(t *testing.T) {
	body := mk(nil, nil, msgs(6, 100)) // one token per byte: the prefix of message i is 100*(i+1)
	r := newRatio(600, body)
	if got := extentWith(body, r, 150, nil, Provider{}); got != extentWith(body, r, 150, nil, Provider{ReadGranularity: 128}) {
		t.Fatalf("zero value = %d, want the 128 default", got)
	}
	wide := extentWith(body, r, 150, nil, Provider{ReadGranularity: 128}) // limit 150+128+3 = 281: 2 messages
	exact := extentWith(body, r, 150, nil, Provider{ReadGranularity: 1})  // limit 150+1+3 = 154: 1 message
	if wide <= exact {
		t.Fatalf("wide = %d, exact = %d, want wide > exact", wide, exact)
	}
	if got := extentWith(body, r, 150, nil, Provider{ReadGranularity: 512}); got <= wide {
		t.Fatalf("granularity 512 = %d, want more than %d", got, wide)
	}
}

// A Bedrock model that has no 1h TTL: "1h" fails the breakpoints check. The default accepts it.
func TestProviderTTLsDecideTheBreakpointsCheck(t *testing.T) {
	body := mk(nil, nil, msgs(4, 100))
	bps := []Breakpoint{{Pos: msg(1), TTL: "1h"}}
	run := func(p Provider) Result {
		f := exact5m
		f.Provider = p
		return req{at: t0, client: body, clientBPs: bps, facts: f, usage: Usage{CacheWrite: 400}}.run(NewTrackers())
	}
	if res := run(Provider{}); hasCheck(res, ReasonBreakpoints) {
		t.Fatalf("default: Checks = %+v, want 1h valid", res.Checks)
	}
	if res := run(Provider{TTLs: []string{"5m"}}); !hasCheck(res, ReasonBreakpoints) {
		t.Fatalf("5m only: Checks = %+v, want breakpoints", res.Checks)
	}
	if res := run(Provider{TTLs: []string{"5m", "1h"}}); hasCheck(res, ReasonBreakpoints) {
		t.Fatalf("5m and 1h: Checks = %+v, want valid", res.Checks)
	}
	// An empty TTL is always valid, and a provider may name its own string.
	f := exact5m
	f.Provider = Provider{TTLs: []string{"24h"}}
	res := req{at: t0, client: body, clientBPs: []Breakpoint{{Pos: msg(1)}, {Pos: msg(2), TTL: "24h"}}, facts: f, usage: Usage{CacheWrite: 400}}.run(NewTrackers())
	if hasCheck(res, ReasonBreakpoints) {
		t.Fatalf("24h: Checks = %+v, want valid", res.Checks)
	}
}

// Gemini explicit caching bills storage. The result carries the tokens written; the host prices the hours.
func TestStorageBilledReportsTheTokensWritten(t *testing.T) {
	body := mk(nil, nil, msgs(4, 100))
	run := func(p Provider, u Usage) Result {
		f := exact5m
		f.Provider = p
		return req{at: t0, client: body, clientBPs: bps5(msg(1)), facts: f, usage: u}.run(NewTrackers())
	}
	if res := run(Provider{}, Usage{CacheWrite: 400}); res.StorageTokens != 0 {
		t.Fatalf("not billed: StorageTokens = %d, want 0", res.StorageTokens)
	}
	if res := run(Provider{StorageBilled: true}, Usage{CacheWrite: 400}); res.StorageTokens != 400 {
		t.Fatalf("billed: StorageTokens = %d, want 400", res.StorageTokens)
	}
	// Nothing is priced without usage, so no storage is reported.
	if res := run(Provider{StorageBilled: true}, Usage{}); res.StorageTokens != 0 {
		t.Fatalf("no usage: StorageTokens = %d, want 0", res.StorageTokens)
	}
}

// The read tolerance of the read-beats-the-clock rule follows the profile too.
func TestToleranceUsesTheProfile(t *testing.T) {
	if got, want := (Provider{}).tolerance(1000), 128+20; got != want {
		t.Fatalf("zero = %d, want %d", got, want)
	}
	if got, want := (Provider{ReadGranularity: 1}).tolerance(1000), 1+20; got != want {
		t.Fatalf("1 = %d, want %d", got, want)
	}
}
