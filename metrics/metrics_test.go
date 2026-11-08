package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/rossoctl/accountant"
	sdk "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// flat prices every token at 1 USD per 1000, except output at 5, and marks one model as a fallback.
type flat struct{}

func (flat) Price(model string, _ time.Time, t Tier) (float64, bool) {
	r := 0.001
	if t == TierOutput {
		r = 0.005
	}
	return r, model == "guess"
}

func newSink(t *testing.T, opts ...Option) (*Sink, *sdk.ManualReader) {
	t.Helper()
	r := sdk.NewManualReader()
	p := sdk.NewMeterProvider(sdk.WithReader(r))
	s, err := New(p.Meter("test"), flat{}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return s, r
}

type point struct {
	attrs map[string]string
	i     int64
	f     float64
}

// collect returns the data points of each instrument by name.
func collect(t *testing.T, r *sdk.ManualReader) map[string][]point {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string][]point{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range d.DataPoints {
					out[m.Name] = append(out[m.Name], point{attrs: kv(dp.Attributes.ToSlice()), i: dp.Value})
				}
			case metricdata.Sum[float64]:
				for _, dp := range d.DataPoints {
					out[m.Name] = append(out[m.Name], point{attrs: kv(dp.Attributes.ToSlice()), f: dp.Value})
				}
			}
		}
	}
	return out
}

func sumI(ps []point) (n int64) {
	for _, p := range ps {
		n += p.i
	}
	return n
}

func sumF(ps []point) (n float64) {
	for _, p := range ps {
		n += p.f
	}
	return n
}

func obs(model string, ops ...accountant.PricedOp) accountant.Observation {
	return accountant.Observation{
		Meta:   accountant.RequestMeta{Model: model, At: time.Unix(1700000000, 0)},
		Result: accountant.Result{Ops: ops},
	}
}

func TestSavedAndAddedAreBookedByTier(t *testing.T) {
	s, r := newSink(t)
	s.Observe(obs("m", accountant.PricedOp{
		Kind: accountant.KindRewrite, Operator: "context-guru.mask",
		Saved:  accountant.TierTokens{Input: 5000},
		Caused: accountant.TierTokens{CacheWrite5m: 50000},
	}))
	got := collect(t, r)
	if n := sumI(got["llm.savings.saved.tokens"]); n != 5000 {
		t.Errorf("saved tokens = %d, want 5000", n)
	}
	if n := sumI(got["llm.savings.added.tokens"]); n != 50000 {
		t.Errorf("added tokens = %d, want 50000", n)
	}
	if v := sumF(got["llm.savings.saved.usd"]); v < 4.99 || v > 5.01 {
		t.Errorf("saved usd = %v, want 5", v)
	}
	if v := sumF(got["llm.savings.added.usd"]); v < 49.99 || v > 50.01 {
		t.Errorf("added usd = %v, want 50", v)
	}
	p := got["llm.savings.saved.tokens"][0].attrs
	if p["kind"] != "rewrite" || p["tier"] != "input" || p["component"] != "mask" {
		t.Errorf("labels = %v, want kind=rewrite tier=input component=mask", p)
	}
}

func TestDryRunAddsNothing(t *testing.T) {
	s, r := newSink(t)
	s.Observe(obs("m", accountant.PricedOp{Kind: accountant.KindRewrite, Operator: "x.mask", DryRun: true,
		Saved: accountant.TierTokens{Input: 100}}))
	got := collect(t, r)
	if len(got["llm.savings.saved.tokens"]) != 0 || len(got["llm.savings.saved.usd"]) != 0 {
		t.Fatalf("a dry-run op booked tokens: %v", got)
	}
	if p := got["llm.savings.requests"]; len(p) != 1 || p[0].attrs["result"] != "skipped" {
		t.Fatalf("requests = %v, want one skipped", p)
	}
}

func TestSpendBooksTheCallAsAdded(t *testing.T) {
	s, r := newSink(t)
	s.Observe(obs("m", accountant.PricedOp{Kind: accountant.KindSpend, Operator: "context-guru.compact",
		Call: &accountant.ModelCall{Model: "m", Usage: accountant.CallUsage{
			Usage:  accountant.Usage{Input: 100, CacheRead: 200, CacheWrite: 300, CacheWrite1h: 100},
			Output: 50,
		}}}))
	by := map[string]int64{}
	for _, p := range collect(t, r)["llm.savings.added.tokens"] {
		by[p.attrs["tier"]] += p.i
	}
	want := map[string]int64{"input": 100, "cache_read": 200, "cache_write_5m": 200, "cache_write_1h": 100, "output": 50}
	for k, v := range want {
		if by[k] != v {
			t.Errorf("added[%s] = %d, want %d", k, by[k], v)
		}
	}
}

func TestUnpricedChecksAndEstimated(t *testing.T) {
	s, r := newSink(t)
	o := obs("guess", accountant.PricedOp{Kind: accountant.KindRewrite, Operator: "h.dedup", Estimated: true,
		Saved: accountant.TierTokens{Input: 10}})
	o.Result.Unpriced = []accountant.Unpriced{{OpID: "a", Reason: "penalty_unknown"}}
	o.Result.Checks = []accountant.CheckFailure{{Check: "sum"}}
	s.Observe(o)
	got := collect(t, r)
	if n := sumI(got["llm.savings.unpriced"]); n != 1 {
		t.Errorf("unpriced = %d, want 1", n)
	}
	if got["llm.savings.unpriced"][0].attrs["reason"] != "penalty_unknown" {
		t.Errorf("unpriced reason = %v", got["llm.savings.unpriced"][0].attrs)
	}
	if n := sumI(got["llm.savings.check_failures"]); n != 1 {
		t.Errorf("check failures = %d, want 1", n)
	}
	if n := sumI(got["llm.savings.estimated"]); n != 1 {
		t.Errorf("estimated = %d, want 1", n)
	}
	if p := got["llm.savings.requests"]; p[0].attrs["result"] != "unpriced" {
		t.Errorf("result = %v, want unpriced", p[0].attrs)
	}
}

// A cache effect is a breakdown only. It must never reach saved or added (spec 5.2).
func TestCacheEffectGoesToItsOwnCounterOnly(t *testing.T) {
	s, r := newSink(t)
	o := obs("m")
	o.Result.CacheEffects = []accountant.CacheEffect{
		{ID: "ce:1", Kind: accountant.EffectTTLPremium, Caused: accountant.TierTokens{CacheWrite1h: 1000}},
		{ID: "ce:2", Kind: accountant.EffectBreak, Cause: accountant.CauseModelChanged, Caused: accountant.TierTokens{CacheWrite5m: 2000}},
		{ID: "ce:3", Kind: accountant.EffectBreak, Caused: accountant.TierTokens{CacheWrite5m: 10}, InBaseline: true},
	}
	s.Observe(o)
	got := collect(t, r)
	if len(got["llm.savings.saved.usd"])+len(got["llm.savings.added.usd"]) != 0 {
		t.Fatalf("a cache effect reached saved or added: %v", got)
	}
	eff := got["llm.savings.cache_effect.usd"]
	if len(eff) != 2 {
		t.Fatalf("cache effect points = %d, want 2 (the baseline one is skipped): %v", len(eff), eff)
	}
	seen := map[string]string{}
	for _, p := range eff {
		seen[p.attrs["effect"]] = p.attrs["cause"] + "/" + p.attrs["direction"]
	}
	if seen["ttl_premium"] != "none/added" || seen["break"] != "model_changed/added" {
		t.Errorf("effects = %v", seen)
	}
}

func TestComponentIsBounded(t *testing.T) {
	s, r := newSink(t, WithComponents("mask", "dedup"))
	for _, op := range []string{"cg.mask", "cg.dedup", "cg.surprise", "tool-prune"} {
		s.Observe(obs("m", accountant.PricedOp{Kind: accountant.KindRewrite, Operator: op,
			Saved: accountant.TierTokens{Input: 1}}))
	}
	seen := map[string]bool{}
	for _, p := range collect(t, r)["llm.savings.saved.tokens"] {
		seen[p.attrs["component"]] = true
	}
	if len(seen) != 3 || !seen["mask"] || !seen["dedup"] || !seen["other"] {
		t.Fatalf("components = %v, want mask, dedup, other", seen)
	}
}

func TestComponentLimitWithoutAList(t *testing.T) {
	s, _ := newSink(t)
	for i := 0; i < maxComponents+10; i++ {
		s.component("op" + itoa(i))
	}
	if got := s.component("brand-new"); got != "other" {
		t.Fatalf("component past the limit = %q, want other", got)
	}
	if got := s.component("op0"); got != "op0" {
		t.Fatalf("a known component = %q, want op0", got)
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}
