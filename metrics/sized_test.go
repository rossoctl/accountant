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
// 50,000 and caused 10,000, so the request without it had 290,000 tokens. The 240,000 tokens that stay
// also drop from 2x to 1x: that is the re-rate.
func TestSavedPricedWithoutTheOperationCausedAtBilled(t *testing.T) {
	saved, added := usd(t, tiered{}, sizedObs(250_000, rewrite(50_000, 10_000)))
	if !near(saved, 50_000*2e-6+240_000*1e-6) { // moved tokens at 2x, plus the re-rate of the 240,000 that stay
		t.Errorf("saved usd = %v, want 0.34", saved)
	}
	if !near(added, 10_000*1e-6) { // 250,000 is below the line: 1x
		t.Errorf("added usd = %v, want 0.01", added)
	}
	// The net is the whole bill difference: 290,000 at 2x minus 250,000 at 1x.
	if want := 290_000*2e-6 - 250_000*1e-6; !near(saved-added, want) {
		t.Errorf("net = %v, want the bill difference %v", saved-added, want)
	}
}

func TestTheLineIsExclusive(t *testing.T) {
	// base = billed + saved - caused
	at, _ := usd(t, tiered{}, sizedObs(252_000, rewrite(20_000, 0))) // 272,000: not above
	if !near(at, 20_000*1e-6) {
		t.Errorf("at the line: saved usd = %v, want 0.02 (1x)", at)
	}
	over, _ := usd(t, tiered{}, sizedObs(252_000, rewrite(20_001, 0))) // 272,001: above
	want := 20_001*2e-6 + 252_000*1e-6                                 // moved tokens at 2x, plus the re-rate of the 252,000 that stay
	if !near(over, want) {
		t.Errorf("one token above: saved usd = %v, want %v", over, want)
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
	// net: +1000 write5m and -400 read. Both are client-side figures, so both take the base size (2x).
	if v := sumF(g["llm.savings.interaction.usd"]); !near(v, 600*2e-6) {
		t.Errorf("interaction usd = %v, want %v", v, 600*2e-6)
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

func byComponent(ps []point) map[string]float64 {
	m := map[string]float64{}
	for _, p := range ps {
		m[p.attrs["component"]] += p.f
	}
	return m
}

func named(name string, saved, caused int) accountant.PricedOp {
	op := rewrite(saved, caused)
	op.Operator = "x." + name
	return op
}

// The re-rate splits across the operations by net saved tokens. The parts add up to the request total.
func TestReRateSplitsByNetSavedTokens(t *testing.T) {
	// billed 250,000; A saves 30,000, B saves 10,000: base 290,000. Re-rate: 250,000 x 1e-6 = 0.25.
	s, r := newSinkWith(t, tiered{})
	s.Observe(sizedObs(250_000, named("a", 30_000, 0), named("b", 10_000, 0)))
	g := collect(t, r)
	got := byComponent(g["llm.savings.saved.usd"])
	if !near(got["a"], 30_000*2e-6+0.25*0.75) || !near(got["b"], 10_000*2e-6+0.25*0.25) {
		t.Errorf("per component = %v, want a=%v b=%v", got, 30_000*2e-6+0.1875, 10_000*2e-6+0.0625)
	}
	if total, want := sumF(g["llm.savings.saved.usd"]), 290_000*2e-6-250_000*1e-6; !near(total, want) {
		t.Errorf("total = %v, want the bill difference %v", total, want)
	}
}

// The sent request is above the line and the request without the operations is not. The re-rate is
// negative: it books to added.usd, and the component's net goes below 0.
func TestNegativeReRate(t *testing.T) {
	// billed 285,000. A saves 5,000. B caused 40,000. Base = 250,000. Stay = 245,000.
	s, r := newSinkWith(t, tiered{})
	s.Observe(sizedObs(285_000, named("a", 5_000, 0), named("b", 0, 40_000)))
	g := collect(t, r)
	saved, added := byComponent(g["llm.savings.saved.usd"]), byComponent(g["llm.savings.added.usd"])
	if !near(saved["a"]-added["a"], 5_000*1e-6-245_000*1e-6) {
		t.Errorf("a net = %v, want %v", saved["a"]-added["a"], 5_000*1e-6-245_000*1e-6)
	}
	if net, want := sumF(g["llm.savings.saved.usd"])-sumF(g["llm.savings.added.usd"]), 250_000*1e-6-285_000*2e-6; !near(net, want) {
		t.Errorf("net = %v, want the bill difference %v", net, want)
	}
	if net := saved["a"] - added["a"]; net >= 0 {
		t.Errorf("a net = %v, want below 0", net)
	}
}

// No operation has a net saving: nobody owns the re-rate. It books to interaction.usd.
func TestReRateWithNoSavingOpIsUnattributed(t *testing.T) {
	s, r := newSinkWith(t, tiered{})
	s.Observe(sizedObs(285_000, named("b", 0, 40_000))) // base 245,000, stay 245,000
	g := collect(t, r)
	var dir string
	for _, p := range g["llm.savings.interaction.usd"] {
		dir = p.attrs["direction"]
		if !near(p.f, 245_000*1e-6) {
			t.Errorf("interaction usd = %v, want 0.245", p.f)
		}
	}
	if dir != "added" {
		t.Errorf("direction = %q, want added", dir)
	}
	if added := sumF(g["llm.savings.added.usd"]); !near(added, 40_000*2e-6) {
		t.Errorf("added usd = %v, want 0.08", added)
	}
}

// A request that crosses no line gets no re-rate.
func TestNoLineNoReRate(t *testing.T) {
	for _, billed := range []int{100_000, 400_000} { // both sides below, then both above
		saved, added := usd(t, tiered{}, sizedObs(billed, rewrite(20_000, 5_000)))
		rate := 1e-6
		if billed > line {
			rate = 2e-6
		}
		if !near(saved, 20_000*rate) || !near(added, 5_000*rate) {
			t.Errorf("billed %d: saved %v added %v, want %v and %v", billed, saved, added, 20_000*rate, 5_000*rate)
		}
	}
}

// outTiered prices input at 1 USD per million tokens and output at 4, and both at 2x and 1.5x
// above the line.
type outTiered struct{}

func (outTiered) Price(string, time.Time, Tier) (float64, bool) { return 1e-6, false }

func (outTiered) PriceAt(_ string, _ time.Time, t Tier, n int64) (float64, bool) {
	r, big := 1e-6, 2.0
	if t == TierOutput {
		r, big = 4e-6, 1.5
	}
	if n > line {
		r *= big
	}
	return r, false
}

// 290K to 250K with 2,000 output tokens: the output stays and goes from 1.5x to 1x.
func TestOutputIsReRated(t *testing.T) {
	o := sizedObs(250_000, rewrite(50_000, 10_000))
	o.Output = 2_000
	s, r := newSinkWith(t, outTiered{})
	s.Observe(o)
	g := collect(t, r)
	saved, added := sumF(g["llm.savings.saved.usd"]), sumF(g["llm.savings.added.usd"])
	outRerate := 2_000 * (6e-6 - 4e-6) // 0.004
	if !near(saved, 50_000*2e-6+240_000*1e-6+outRerate) {
		t.Errorf("saved usd = %v, want %v", saved, 50_000*2e-6+240_000*1e-6+outRerate)
	}
	if !near(added, 10_000*1e-6) {
		t.Errorf("added usd = %v, want 0.01", added)
	}
	// The bill difference: 290K input at 2x and 2K output at 6e-6, minus 250K at 1x and 2K at 4e-6.
	if want := 290_000*2e-6 + 2_000*6e-6 - 250_000*1e-6 - 2_000*4e-6; !near(saved-added, want) {
		t.Errorf("net = %v, want the bill difference %v", saved-added, want)
	}
	// Output tokens do not count toward the prompt size: base stays 290,000, not 292,000.
	var out float64
	for _, p := range g["llm.savings.saved.usd"] {
		if p.attrs["tier"] == "output" {
			out += p.f
		}
	}
	if !near(out, outRerate) {
		t.Errorf("output re-rate = %v, want %v", out, outRerate)
	}
}

// A host that leaves output at 0 gets no output re-rate.
func TestZeroOutputHasNoOutputReRate(t *testing.T) {
	s, r := newSinkWith(t, outTiered{})
	s.Observe(sizedObs(250_000, rewrite(50_000, 10_000)))
	for _, p := range collect(t, r)["llm.savings.saved.usd"] {
		if p.attrs["tier"] == "output" {
			t.Errorf("output series = %v, want none", p.f)
		}
	}
}

// Output tokens do not move the prompt size: output at the line does not tip a request over it.
func TestOutputDoesNotCountTowardPromptSize(t *testing.T) {
	o := sizedObs(252_000, rewrite(20_000, 0)) // base 272,000: not above the line
	o.Output = 50_000
	saved, _ := usd(t, outTiered{}, o)
	if !near(saved, 20_000*1e-6) {
		t.Errorf("saved usd = %v, want 0.02 (1x, no re-rate)", saved)
	}
}

// tierPricer has a rate for each tier. A request above the line pays 2x for every tier.
type tierPricer struct{}

var tierRate = map[Tier]float64{TierInput: 3e-6, TierCacheRead: 3e-7, TierCacheWrite5m: 3.75e-6, TierCacheWrite1h: 6e-6, TierOutput: 15e-6}

func (tierPricer) Price(_ string, _ time.Time, t Tier) (float64, bool) { return tierRate[t], false }
func (tierPricer) PriceAt(_ string, _ time.Time, t Tier, n int64) (float64, bool) {
	if n > line {
		return 2 * tierRate[t], false
	}
	return tierRate[t], false
}

// net adds every counter that belongs to the net saving (metrics spec: saved - added + cache effect + interaction).
func net(g map[string][]point) float64 {
	n := sumF(g["llm.savings.saved.usd"]) - sumF(g["llm.savings.added.usd"])
	for _, p := range g["llm.savings.cache_effect.usd"] {
		if p.attrs["direction"] == "saved" {
			n += p.f
		} else {
			n -= p.f
		}
	}
	for _, p := range g["llm.savings.interaction.usd"] {
		if p.attrs["direction"] == "saved" {
			n += p.f
		} else {
			n -= p.f
		}
	}
	return n
}

// A request with a counted cache effect, a rescue interaction and a crossed line. The net saving must
// equal the bill difference, with each tier priced at its own rate.
//
// Sent (billed 270,000, 1x): input 100,000, read 150,000, write5m 20,000.
// A trim saves 50,000 input. A rescued span saves 1,000 read. A break (not in the baseline) saved
// 20,000 read and caused 20,000 write5m. The interaction holds 1,000 write against the 1,000 read.
// Client body (321,000, 2x): input 150,000, read 170,000, write5m 1,000.
func TestEffectAndInteractionAddUpToTheBillDifference(t *testing.T) {
	o := sizedObs(0)
	o.Usage = accountant.Usage{Input: 100_000, CacheRead: 150_000, CacheWrite: 20_000}
	op := accountant.PricedOp{Kind: accountant.KindRewrite, Operator: "x.trim",
		Saved: accountant.TierTokens{Input: 50_000, CacheRead: 1_000}}
	o.Result.Ops = []accountant.PricedOp{op}
	o.Result.CacheEffects = []accountant.CacheEffect{{Kind: accountant.EffectBreak,
		Saved: accountant.TierTokens{CacheRead: 20_000}, Caused: accountant.TierTokens{CacheWrite5m: 20_000}}}
	o.Result.Interaction = accountant.TierTokens{CacheWrite5m: 1_000}
	o.Result.InteractionCaused = accountant.TierTokens{CacheRead: 1_000}

	client := 150_000*2*tierRate[TierInput] + 170_000*2*tierRate[TierCacheRead] + 1_000*2*tierRate[TierCacheWrite5m]
	sent := 100_000*tierRate[TierInput] + 150_000*tierRate[TierCacheRead] + 20_000*tierRate[TierCacheWrite5m]

	s, r := newSinkWith(t, tierPricer{})
	s.Observe(o)
	if got, want := net(collect(t, r)), client-sent; !near(got, want) {
		t.Errorf("net = %v, want the bill difference %v", got, want)
	}
}
