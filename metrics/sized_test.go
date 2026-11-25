package metrics

import (
	"math"
	"testing"
	"time"

	"github.com/rossoctl/accountant"
)

const line = 272_000 // a request above this size pays 2x for every token

// tiered prices input at 1 USD per million tokens, and 2x for a request above the line. It has both
// Price (the rate of a small request) and PriceAt.
type tiered struct{}

func (tiered) Price(string, time.Time, Tier) (float64, bool) { return 1e-6, false }

func (tiered) PriceAt(_ string, _ time.Time, _ Tier, n int64) (float64, bool) {
	if n > line {
		return 2e-6, false
	}
	return 1e-6, false
}

// plainOnly hides PriceAt: a host that did not adopt SizedPricer.
type plainOnly struct{ p Pricer }

func (h plainOnly) Price(m string, at time.Time, t Tier) (float64, bool) { return h.p.Price(m, at, t) }

func sizedObs(billed int, ops ...accountant.PricedOp) Observation {
	o := obs("gpt", ops...)
	o.Usage = accountant.Usage{Input: billed}
	return o
}

func near(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

func usd(t *testing.T, p Pricer, o Observation) (saved, added float64) {
	t.Helper()
	s, r := newSinkWith(t, p)
	s.Observe(o)
	g := collect(t, r)
	return sumF(g["llm.savings.saved.usd"]), sumF(g["llm.savings.added.usd"])
}

func rewrite(saved, caused int) accountant.PricedOp {
	return accountant.PricedOp{Kind: accountant.KindRewrite, Operator: "x.trim",
		Saved: accountant.TierTokens{Input: saved}, Caused: accountant.TierTokens{Input: caused}}
}

// The saved side is above the line and the billed side is below it. Billed 250,000. The trim saved
// 50,000 and caused 10,000, so the request without it had 290,000 tokens.
func TestSavedPricedWithoutTheOperationCausedAtBilled(t *testing.T) {
	saved, added := usd(t, tiered{}, sizedObs(250_000, rewrite(50_000, 10_000)))
	if !near(saved, 50_000*2e-6) { // 290,000 is above the line: 2x
		t.Errorf("saved usd = %v, want 0.1", saved)
	}
	if !near(added, 10_000*1e-6) { // 250,000 is below the line: 1x
		t.Errorf("added usd = %v, want 0.01", added)
	}
}

func TestTheLineIsExclusive(t *testing.T) {
	// base = billed + saved - caused
	at, _ := usd(t, tiered{}, sizedObs(252_000, rewrite(20_000, 0))) // 272,000: not above
	if !near(at, 20_000*1e-6) {
		t.Errorf("at the line: saved usd = %v, want 0.02 (1x)", at)
	}
	over, _ := usd(t, tiered{}, sizedObs(252_000, rewrite(20_001, 0))) // 272,001: above
	if !near(over, 20_001*2e-6) {
		t.Errorf("one token above: saved usd = %v, want %v (2x)", over, 20_001*2e-6)
	}
}

// A dry-run operation changed nothing, so it does not move the size without the operations.
func TestDryRunDoesNotMoveTheBaseSize(t *testing.T) {
	dry := rewrite(100_000, 0)
	dry.DryRun = true
	saved, _ := usd(t, tiered{}, sizedObs(250_000, rewrite(10_000, 0), dry)) // base 260,000
	if !near(saved, 10_000*1e-6) {
		t.Errorf("saved usd = %v, want 0.01 (1x)", saved)
	}
}

// A host without PriceAt gets the figures of Price, and a sized host gets different ones for the
// same observation.
func TestHostWithoutPriceAtSeesNoChange(t *testing.T) {
	o := sizedObs(250_000, rewrite(50_000, 10_000))
	saved, added := usd(t, plainOnly{tiered{}}, o)
	if !near(saved, 50_000*1e-6) || !near(added, 10_000*1e-6) {
		t.Errorf("plain host: saved %v added %v, want 0.05 and 0.01", saved, added)
	}
	o.Usage = accountant.Usage{}
	s2, a2 := usd(t, flat{}, o)
	if !near(s2, 50_000*0.001) || !near(a2, 10_000*0.001) {
		t.Errorf("flat host: saved %v added %v", s2, a2)
	}
	sSized, _ := usd(t, tiered{}, sizedObs(250_000, rewrite(50_000, 10_000)))
	if near(sSized, saved) {
		t.Error("the sized host must differ here, or the test proves nothing")
	}
}

// With no billed usage, the size is unknown: the sized host gets the plain Price.
func TestUnknownSizeUsesPlainPrice(t *testing.T) {
	o := sizedObs(0, rewrite(300_000, 0))
	saved, _ := usd(t, tiered{}, o)
	if !near(saved, 300_000*1e-6) {
		t.Errorf("saved usd = %v, want 0.3 (plain Price)", saved)
	}
}

// A spend call is priced at the call's own prompt size.
func TestSpendCallPricedAtItsOwnSize(t *testing.T) {
	op := accountant.PricedOp{Kind: accountant.KindSpend, Operator: "x.summarize", OpID: "s1",
		Call: &accountant.ModelCall{Model: "gpt", Usage: accountant.CallUsage{Usage: accountant.Usage{Input: 300_000}}}}
	_, added := usd(t, tiered{}, sizedObs(100_000, op)) // the request is small, the call is large
	if !near(added, 300_000*2e-6) {
		t.Errorf("added usd = %v, want 0.6", added)
	}
}

// A cache effect and the interaction follow the same rule: saved at the base size, added at the billed size.
func TestEffectsAndInteractionFollowTheSides(t *testing.T) {
	o := sizedObs(250_000, rewrite(50_000, 0)) // base 300,000
	o.Result.Interaction = accountant.TierTokens{CacheWrite5m: 1000}
	o.Result.InteractionCaused = accountant.TierTokens{CacheRead: 400}
	o.Result.CacheEffects = []accountant.CacheEffect{{Kind: accountant.EffectRescue,
		Saved:  accountant.TierTokens{Input: 100},
		Caused: accountant.TierTokens{Input: 100}}}
	s, r := newSinkWith(t, tiered{})
	s.Observe(o)
	g := collect(t, r)
	var eSaved, eAdded float64
	for _, p := range g["llm.savings.cache_effect.usd"] {
		if p.attrs["direction"] == "saved" {
			eSaved += p.f
		} else {
			eAdded += p.f
		}
	}
	if !near(eSaved, 100*2e-6) || !near(eAdded, 100*1e-6) {
		t.Errorf("effect saved %v added %v, want 0.0002 and 0.0001", eSaved, eAdded)
	}
	// net: +1000 write5m (saved side, 2x), -400 read (added side, 1x)
	if v := sumF(g["llm.savings.interaction.usd"]); !near(v, 1000*2e-6-400*1e-6) {
		t.Errorf("interaction usd = %v, want %v", v, 1000*2e-6-400*1e-6)
	}
}

// splitPricer gives a different rate to Price and to PriceAt, so a test can see which one ran.
type splitPricer struct{ estTier Tier }

func (splitPricer) Price(string, time.Time, Tier) (float64, bool) { return 3e-6, false }
func (p splitPricer) PriceAt(_ string, _ time.Time, t Tier, _ int64) (float64, bool) {
	return 1e-6, t == p.estTier
}

// When the derived size is not above 0, the saved side uses plain Price. The caused side keeps the billed size.
func TestBaseSizeNotCredibleFallsBackToPrice(t *testing.T) {
	// billed 1,000; saved 100; caused 5,000: base = -3,900
	saved, added := usd(t, splitPricer{}, sizedObs(1_000, rewrite(100, 5_000)))
	if !near(saved, 100*3e-6) {
		t.Errorf("saved usd = %v, want 0.0003 (plain Price)", saved)
	}
	if !near(added, 5_000*1e-6) {
		t.Errorf("added usd = %v, want 0.005 (PriceAt at the billed size)", added)
	}
}

// A tier with 0 tokens is not priced, so its estimated return marks nothing.
func TestEmptyTierDoesNotMarkEstimated(t *testing.T) {
	o := sizedObs(250_000)
	o.Result.CacheEffects = []accountant.CacheEffect{{Kind: accountant.EffectRescue,
		Saved: accountant.TierTokens{Input: 100}}}
	s, r := newSinkWith(t, splitPricer{estTier: TierCacheWrite1h})
	s.Observe(o)
	if n := sumI(collect(t, r)["llm.savings.estimated"]); n != 0 {
		t.Errorf("estimated requests = %d, want 0", n)
	}
	// The same pricer marks the request when the estimated tier holds tokens.
	o.Result.CacheEffects[0].Saved = accountant.TierTokens{CacheWrite1h: 100}
	s2, r2 := newSinkWith(t, splitPricer{estTier: TierCacheWrite1h})
	s2.Observe(o)
	if n := sumI(collect(t, r2)["llm.savings.estimated"]); n != 1 {
		t.Errorf("estimated requests = %d, want 1", n)
	}
}
