package engine

import (
	"testing"
	"time"
)

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
	// The engine prices only "5m" and "1h": a custom string fails even when listed.
	f := exact5m
	f.Provider = Provider{TTLs: []string{"5m", "24h"}}
	res := req{at: t0, client: body, clientBPs: []Breakpoint{{Pos: msg(1), TTL: "24h"}}, facts: f, usage: Usage{CacheWrite: 400}}.run(NewTrackers())
	if !hasCheck(res, ReasonBreakpoints) {
		t.Fatalf("24h: Checks = %+v, want breakpoints", res.Checks)
	}
	// An empty TTL is valid, and an empty non-nil list allows nothing else.
	f.Provider = Provider{TTLs: []string{}}
	res = req{at: t0, client: body, clientBPs: []Breakpoint{{Pos: msg(1)}}, facts: f, usage: Usage{CacheWrite: 400}}.run(NewTrackers())
	if hasCheck(res, ReasonBreakpoints) {
		t.Fatalf("empty TTL: Checks = %+v, want valid", res.Checks)
	}
	res = req{at: t0, client: body, clientBPs: bps5(msg(1)), facts: f, usage: Usage{CacheWrite: 400}}.run(NewTrackers())
	if !hasCheck(res, ReasonBreakpoints) {
		t.Fatalf("empty list, 5m: Checks = %+v, want breakpoints", res.Checks)
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

// Row 4 through Compute: the same body, a hot entry, and a read 200 tokens short of the cached extent.
// Inside the default 128 tokens plus 2% it is no break. A provider that reports the exact count has
// no such slack, so the short read is a break.
func TestReadGranularityDecidesTheEarlyLossVerdict(t *testing.T) {
	run := func(p Provider) Result {
		f := exact5m
		f.Provider = p
		ts := NewTrackers()
		body := mk(nil, nil, msgs(6, 1000))
		req{at: t0, client: body, clientBPs: bps5(msg(5)), facts: f, usage: Usage{CacheWrite: 6000}}.run(ts)
		return req{at: t0.Add(time.Minute), client: body, clientBPs: bps5(msg(5)), facts: f,
			usage: Usage{CacheRead: 5800, CacheWrite: 200}}.run(ts)
	}
	if res := run(Provider{}); len(res.CacheEffects) != 0 {
		t.Fatalf("default: CacheEffects = %+v, want none", res.CacheEffects)
	}
	if res := run(Provider{ReadGranularity: 1}); len(res.CacheEffects) == 0 {
		t.Fatalf("exact: want a break for the short read")
	}
}
