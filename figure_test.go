package accountant_test

import (
	"math"
	"testing"
	"time"

	"github.com/rossoctl/accountant"
)

// The cases follow the host tests that priced the same Results with their own copy of this math. Each
// one must give the same dollars through Figure.

// gpt56 is gpt-5.6 at plausible list rates: above 272,000 prompt tokens, 2x input, cache read and cache
// write, and 1.5x output.
type gpt56 struct{}

func (gpt56) PriceAt(_ string, _ time.Time, t accountant.Tier, n int64) (float64, bool) {
	base := map[accountant.Tier]float64{
		accountant.TierInput: 5e-6, accountant.TierCacheRead: 0.5e-6,
		accountant.TierCacheWrite5m: 5e-6, accountant.TierCacheWrite1h: 5e-6, accountant.TierOutput: 30e-6,
	}[t]
	if n > 272_000 {
		if t == accountant.TierOutput {
			return base * 1.5, false
		}
		return base * 2, false
	}
	return base, false
}

// flat has no line: the same rate at every size.
type flat struct{ gpt56 }

func (flat) PriceAt(m string, at time.Time, t accountant.Tier, _ int64) (float64, bool) {
	return gpt56{}.PriceAt(m, at, t, 0)
}

type haiku struct{}

func (haiku) PriceAt(_ string, _ time.Time, t accountant.Tier, n int64) (float64, bool) {
	r := map[accountant.Tier]float64{accountant.TierInput: 1e-6, accountant.TierCacheRead: 0.1e-6,
		accountant.TierCacheWrite5m: 1.25e-6, accountant.TierCacheWrite1h: 1.25e-6, accountant.TierOutput: 5e-6}[t]
	if n > 100_000 {
		r *= 5
	}
	return r, false
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func tok(in int) accountant.TierTokens { return accountant.TierTokens{Input: in} }

func op(name string, saved, caused accountant.TierTokens) accountant.PricedOp {
	return accountant.PricedOp{Kind: accountant.KindRewrite, Operator: name, ChangeID: "c-" + name, Saved: saved, Caused: caused}
}

func fig(p accountant.SizedPricer, u accountant.Usage, out int, r accountant.Result) accountant.USDFigure {
	return accountant.Figure(accountant.FigureInput{Model: "m", At: time.Unix(0, 0), Billed: u, Output: out, Result: r}, p)
}

// Billed 250,000, without 300,000. The saved tokens bill at 2x, the 250,000 that stay drop from 2x to 1x.
func TestSavedSideIsPricedAtTheClientSize(t *testing.T) {
	r := accountant.Result{Ops: []accountant.PricedOp{op("mask", tok(50_000), accountant.TierTokens{})}}
	f := fig(gpt56{}, accountant.Usage{Input: 250_000}, 0, r)
	if want := 300_000*10e-6 - 250_000*5e-6; !near(f.ByComponent["mask"], want) {
		t.Fatalf("saving %v want %v", f.ByComponent["mask"], want)
	}
	f = fig(gpt56{}, accountant.Usage{Input: 200_000}, 0, r) // both below the line
	if want := 50_000 * 5e-6; !near(f.ByComponent["mask"], want) {
		t.Fatalf("below the line: %v want %v", f.ByComponent["mask"], want)
	}
	f = fig(gpt56{}, accountant.Usage{Input: 400_000}, 0, r) // both above the line
	if want := 50_000 * 10e-6; !near(f.ByComponent["mask"], want) {
		t.Fatalf("above the line: %v want %v", f.ByComponent["mask"], want)
	}
}

// A component adds 40,000 tokens to a request of 300,000. Nobody has a net saving, so the re-rate of
// the 260,000 that stay (-1.30) is unowned.
func TestCausedSideIsPricedAtTheBilledSize(t *testing.T) {
	r := accountant.Result{Ops: []accountant.PricedOp{op("mask", accountant.TierTokens{}, tok(40_000))}}
	f := fig(gpt56{}, accountant.Usage{Input: 300_000}, 0, r)
	if want := -40_000 * 10e-6; !near(f.ByComponent["mask"], want) {
		t.Fatalf("cost %v want %v", f.ByComponent["mask"], want)
	}
	if want := -260_000 * 5e-6; !near(f.UnownedReRateUSD, want) {
		t.Fatalf("unowned re-rate %v want %v", f.UnownedReRateUSD, want)
	}
	if want := 260_000*5e-6 - 300_000*10e-6; !near(f.DeltaUSD, want) {
		t.Fatalf("delta %v want %v", f.DeltaUSD, want)
	}
}

func TestHaikuSidesOnOppositeSides(t *testing.T) {
	r := accountant.Result{Ops: []accountant.PricedOp{op("mask", accountant.TierTokens{CacheRead: 20_000}, accountant.TierTokens{})}}
	f := fig(haiku{}, accountant.Usage{CacheRead: 90_000}, 0, r) // without: 110,000, so 5x
	if want := 110_000*0.5e-6 - 90_000*0.1e-6; !near(f.ByComponent["mask"], want) {
		t.Fatalf("saving %v want %v", f.ByComponent["mask"], want)
	}
}

// A trim takes 290,000 to 250,000, with 2,000 output (1.5x above the line).
func TestFullBillDifferenceAcrossTheLine(t *testing.T) {
	r := accountant.Result{Ops: []accountant.PricedOp{op("mask", tok(40_000), accountant.TierTokens{})}}
	f := fig(gpt56{}, accountant.Usage{Input: 250_000}, 2_000, r)
	want := (290_000*10e-6 + 2_000*45e-6) - (250_000*5e-6 + 2_000*30e-6)
	if !near(f.ByComponent["mask"], want) || !near(f.DeltaUSD, want) || !near(f.TotalUSD(), want) {
		t.Fatalf("component %v delta %v total %v want %v", f.ByComponent["mask"], f.DeltaUSD, f.TotalUSD(), want)
	}
}

// Two components split the re-rate 3:1 by net saved tokens, and the components add up to the total.
func TestReRateSplitsByNetSavedTokens(t *testing.T) {
	r := accountant.Result{Ops: []accountant.PricedOp{
		op("a", tok(30_000), accountant.TierTokens{}), op("b", tok(10_000), accountant.TierTokens{})}}
	f := fig(gpt56{}, accountant.Usage{Input: 250_000}, 0, r) // without: 290,000
	re := 250_000 * (10e-6 - 5e-6)
	if want := 30_000*10e-6 + re*0.75; !near(f.ByComponent["a"], want) {
		t.Fatalf("a %v want %v", f.ByComponent["a"], want)
	}
	if want := 10_000*10e-6 + re*0.25; !near(f.ByComponent["b"], want) {
		t.Fatalf("b %v want %v", f.ByComponent["b"], want)
	}
	if want := 290_000*10e-6 - 250_000*5e-6; !near(f.ByComponent["a"]+f.ByComponent["b"], want) {
		t.Fatalf("sum %v want %v", f.ByComponent["a"]+f.ByComponent["b"], want)
	}
	// The re-rate share is in the first-removal column, not in the replay column.
	if !near(f.ByComponentFirst["a"], f.ByComponent["a"]) || f.ByComponentReplay["a"] != 0 {
		t.Fatalf("first %v replay %v", f.ByComponentFirst["a"], f.ByComponentReplay["a"])
	}
}

// The request sent is above the line and the one without is below it: the re-rate is negative, and the
// owner of the net saving can go below 0.
func TestNegativeReRateCanTakeAComponentBelowZero(t *testing.T) {
	r := accountant.Result{Ops: []accountant.PricedOp{op("a", tok(1_000), tok(30_000))}} // net saved < 0: unowned
	f := fig(gpt56{}, accountant.Usage{Input: 290_000}, 0, r)                            // without: 261,000
	if f.UnownedReRateUSD >= 0 {
		t.Fatalf("unowned re-rate %v want < 0", f.UnownedReRateUSD)
	}
	r = accountant.Result{Ops: []accountant.PricedOp{op("a", tok(2_000), accountant.TierTokens{}), op("b", accountant.TierTokens{}, tok(30_000))}}
	f = fig(gpt56{}, accountant.Usage{Input: 275_000}, 0, r) // without: 247,000: below the line
	want := 247_000*5e-6 - 275_000*10e-6
	if !near(f.DeltaUSD, want) || f.ByComponent["a"] >= 2_000*5e-6 {
		t.Fatalf("delta %v want %v, a %v", f.DeltaUSD, want, f.ByComponent["a"])
	}
}

// No line crossed, or a price with no tiers: no re-rate, whatever the sizes.
func TestNoLineNoReRate(t *testing.T) {
	r := accountant.Result{Ops: []accountant.PricedOp{op("mask", tok(50_000), accountant.TierTokens{})}}
	f := fig(gpt56{}, accountant.Usage{Input: 100_000}, 3_000, r)
	if !near(f.ByComponent["mask"], 50_000*5e-6) || f.UnownedReRateUSD != 0 {
		t.Fatalf("got %v", f.ByComponent["mask"])
	}
	f = fig(flat{}, accountant.Usage{Input: 250_000}, 0, r)
	if !near(f.DeltaUSD, 50_000*5e-6) {
		t.Fatalf("flat delta %v", f.DeltaUSD)
	}
}

// Unsized: no size is derived, and no re-rate is booked.
func TestUnsizedBooksNoReRate(t *testing.T) {
	r := accountant.Result{Ops: []accountant.PricedOp{op("mask", tok(50_000), accountant.TierTokens{})}}
	f := accountant.Figure(accountant.FigureInput{Billed: accountant.Usage{Input: 250_000}, Result: r, Unsized: true}, flat{})
	if !near(f.DeltaUSD, 50_000*5e-6) {
		t.Fatalf("delta %v", f.DeltaUSD)
	}
}

// No billed usage: every side prices at the base rate (size 0).
func TestWithoutUsageKeepsBaseRates(t *testing.T) {
	r := accountant.Result{Ops: []accountant.PricedOp{op("mask", tok(50_000), accountant.TierTokens{})}}
	f := fig(gpt56{}, accountant.Usage{}, 0, r)
	if !near(f.ByComponent["mask"], 50_000*5e-6) {
		t.Fatalf("got %v", f.ByComponent["mask"])
	}
}

func cacheEffectResult(baseline bool) accountant.Result {
	return accountant.Result{
		Ops: []accountant.PricedOp{op("mask", tok(50_000), accountant.TierTokens{})},
		CacheEffects: []accountant.CacheEffect{
			{Kind: accountant.EffectRescue, Saved: accountant.TierTokens{CacheWrite5m: 1_000}, Caused: accountant.TierTokens{CacheRead: 1_000}},
			{Kind: accountant.EffectBreak, ChangeID: "c-mask", Saved: accountant.TierTokens{CacheRead: 20_000}, Caused: accountant.TierTokens{CacheWrite5m: 20_000}, InBaseline: baseline},
		}}
}

// Billed 270,000: input 100,000, read 150,000, write 20,000. The request without has 320,000 (2x). The
// total must equal the bill difference with no token re-rated twice: the Caused tokens of a counted
// cache effect do not stay.
func TestReRateDoesNotCountCacheEffectTokensTwice(t *testing.T) {
	f := fig(gpt56{}, accountant.Usage{Input: 100_000, CacheRead: 150_000, CacheWrite: 20_000}, 0, cacheEffectResult(false))
	without := 150_000*10e-6 + 169_000*1e-6 + 1_000*10e-6
	sent := 100_000*5e-6 + 150_000*0.5e-6 + 20_000*5e-6
	if !near(f.TotalUSD(), without-sent) {
		t.Fatalf("booked %v, bill difference %v", f.TotalUSD(), without-sent)
	}
	if f.KeepAliveSavedUSD == 0 {
		t.Fatal("the rescue is not booked")
	}
}

// A break the client body has too is booked nowhere, so its tokens stay and are re-rated once.
func TestReRateKeepsInBaselineBreakTokens(t *testing.T) {
	r := accountant.Result{
		Ops: []accountant.PricedOp{op("mask", tok(180_000), accountant.TierTokens{})},
		CacheEffects: []accountant.CacheEffect{{Kind: accountant.EffectBreak, InBaseline: true,
			Saved: accountant.TierTokens{CacheRead: 20_000}, Caused: accountant.TierTokens{CacheWrite5m: 20_000}}}}
	f := fig(gpt56{}, accountant.Usage{Input: 100_000, CacheWrite: 20_000}, 0, r) // without: 300,000; sent: 120,000
	want := 180_000*10e-6 + 100_000*5e-6 + 20_000*5e-6
	if !near(f.DeltaUSD, want) || !near(f.TotalUSD(), want) {
		t.Fatalf("delta %v want %v", f.DeltaUSD, want)
	}
	if f.UnattributedBreakUSD <= 0 || f.EndedBreakUSD != 0 {
		t.Fatalf("unattributed %v ended %v", f.UnattributedBreakUSD, f.EndedBreakUSD)
	}
}

// Every part of a request that mixes all of it adds up to the bill difference: components, the ended
// break, the unowned parts, the effects and the interaction.
func TestPartsAddUpToTheBillDifference(t *testing.T) {
	r := accountant.Result{
		Ops: []accountant.PricedOp{
			op("a", accountant.TierTokens{CacheRead: 30_000, Input: 20_000}, tok(2_000)),
			op("b", tok(10_000), accountant.TierTokens{}),
			{Kind: accountant.KindSpend, Operator: "k", Call: &accountant.ModelCall{Model: "m", Usage: accountant.CallUsage{Usage: accountant.Usage{Input: 9_000}, Output: 100}}},
		},
		CacheEffects: []accountant.CacheEffect{
			{Kind: accountant.EffectRescue, Saved: accountant.TierTokens{CacheWrite5m: 4_000}, Caused: accountant.TierTokens{CacheRead: 4_000}},
			{Kind: accountant.EffectBreak, ChangeID: "c-a", Saved: accountant.TierTokens{CacheRead: 5_000}, Caused: accountant.TierTokens{CacheWrite5m: 5_000}},
			{Kind: accountant.EffectBreak, Saved: accountant.TierTokens{CacheRead: 3_000}, Caused: accountant.TierTokens{CacheWrite5m: 3_000}},
			{Kind: accountant.EffectTTLPremium, Saved: accountant.TierTokens{CacheWrite5m: 1_000}, Caused: accountant.TierTokens{CacheWrite1h: 1_000}},
			{Kind: accountant.EffectMarkPremium, Saved: tok(1_000), Caused: accountant.TierTokens{CacheWrite5m: 1_000}},
			{Kind: accountant.EffectSplit, Saved: accountant.TierTokens{CacheWrite5m: 2_000}, Caused: accountant.TierTokens{CacheRead: 2_000}},
			{Kind: accountant.EffectBreak, InBaseline: true, Saved: accountant.TierTokens{CacheRead: 7_000}, Caused: accountant.TierTokens{CacheWrite5m: 7_000}},
		},
		Interaction:       accountant.TierTokens{CacheWrite5m: 6_000},
		InteractionCaused: accountant.TierTokens{CacheRead: 6_000},
	}
	u := accountant.Usage{Input: 60_000, CacheRead: 150_000, CacheWrite: 40_000, CacheWrite1h: 1_000} // sent: 250,000, below the line
	f := fig(gpt56{}, u, 1_500, r)
	sum := f.EndedBreakUSD + f.UnownedMarkUSD + f.UnownedReRateUSD
	for _, v := range f.ByComponent {
		sum += v
	}
	if !near(sum, f.DeltaUSD) {
		t.Fatalf("components + unowned = %v, delta %v", sum, f.DeltaUSD)
	}
	if f.UnattributedBreakUSD <= 0 || f.EndedBreakUSD >= 0 || f.UnownedMarkUSD == 0 {
		t.Fatalf("unattributed %v ended %v mark %v", f.UnattributedBreakUSD, f.EndedBreakUSD, f.UnownedMarkUSD)
	}

	// The bill without the operations, tier by tier: the bill as sent, plus the Saved tokens and minus the
	// Caused tokens of every counted part. The InBaseline break is in both bills, so it is left out.
	sent := accountant.TierTokens{Input: 60_000, CacheRead: 150_000, CacheWrite5m: 39_000, CacheWrite1h: 1_000}
	without := sent
	parts := []accountant.TierTokens{}
	for _, o := range r.Ops {
		if o.Call == nil {
			parts = append(parts, o.Saved.Sub(o.Caused))
		}
	}
	for _, e := range r.CacheEffects {
		if !e.InBaseline {
			parts = append(parts, e.Net())
		}
	}
	parts = append(parts, r.Interaction.Sub(r.InteractionCaused))
	for _, p := range parts {
		without = without.Add(p)
	}
	size := func(t accountant.TierTokens) int64 {
		return int64(t.Input + t.CacheRead + t.CacheWrite5m + t.CacheWrite1h)
	}
	bill := func(t accountant.TierTokens, n int64, out int) float64 {
		var usd float64
		for tier, c := range map[accountant.Tier]int{accountant.TierInput: t.Input, accountant.TierCacheRead: t.CacheRead,
			accountant.TierCacheWrite5m: t.CacheWrite5m, accountant.TierCacheWrite1h: t.CacheWrite1h, accountant.TierOutput: out} {
			rate, _ := gpt56{}.PriceAt("m", time.Time{}, tier, n)
			usd += rate * float64(c)
		}
		return usd
	}
	if size(sent) > 272_000 || size(without) <= 272_000 {
		t.Fatalf("the case must cross the line: sent %d without %d", size(sent), size(without))
	}
	want := bill(without, size(without), 1_500) - bill(sent, size(sent), 1_500)
	if !near(f.TotalUSD(), want) {
		t.Fatalf("total %v, bill difference %v", f.TotalUSD(), want)
	}
}

// A spend call is priced at its own model and size, and is in neither the delta nor the total. A refused
// call is not priced.
func TestSpendCallIsASeparateBill(t *testing.T) {
	call := accountant.PricedOp{OpID: "s1", Kind: accountant.KindSpend, Operator: "k",
		Call: &accountant.ModelCall{Model: "m", Usage: accountant.CallUsage{Usage: accountant.Usage{Input: 1_000}, Output: 100}}}
	f := fig(gpt56{}, accountant.Usage{Input: 10_000}, 0, accountant.Result{Ops: []accountant.PricedOp{call}})
	if want := 1_000*5e-6 + 100*30e-6; !near(f.CallUSD, want) {
		t.Fatalf("call %v want %v", f.CallUSD, want)
	}
	if f.DeltaUSD != 0 || f.TotalUSD() != 0 {
		t.Fatalf("delta %v total %v", f.DeltaUSD, f.TotalUSD())
	}
	f = fig(gpt56{}, accountant.Usage{Input: 10_000}, 0, accountant.Result{Ops: []accountant.PricedOp{call},
		Unpriced: []accountant.Unpriced{{OpID: "s1", Reason: accountant.ReasonNoUsage}}})
	if f.CallUSD != 0 || len(f.Unpriced) != 1 {
		t.Fatalf("refused call: %v %v", f.CallUSD, f.Unpriced)
	}
}

// A dry-run op is not counted and does not move the size.
func TestDryRunIsNotCounted(t *testing.T) {
	dry := op("mask", tok(100_000), accountant.TierTokens{})
	dry.DryRun = true
	f := fig(gpt56{}, accountant.Usage{Input: 250_000}, 0, accountant.Result{Ops: []accountant.PricedOp{dry}})
	if f.DeltaUSD != 0 || len(f.Lines) != 0 {
		t.Fatalf("delta %v lines %v", f.DeltaUSD, f.Lines)
	}
}

// The held-floor parts: the saved tokens at their own tier, and the same tokens as read.
func TestSavedAsReadFloorParts(t *testing.T) {
	r := accountant.Result{Ops: []accountant.PricedOp{op("mask", tok(50_000), accountant.TierTokens{})}}
	f := fig(gpt56{}, accountant.Usage{Input: 250_000}, 0, r) // without: 300,000 (2x)
	if !near(f.ByComponentSaved["mask"], 50_000*10e-6) || !near(f.ByComponentSavedAsRead["mask"], 50_000*1e-6) {
		t.Fatalf("saved %v as read %v", f.ByComponentSaved["mask"], f.ByComponentSavedAsRead["mask"])
	}
}

// An effect of an unknown kind is listed as unpriced, never counted.
func TestUnknownEffectKindIsUnpriced(t *testing.T) {
	r := accountant.Result{CacheEffects: []accountant.CacheEffect{{Kind: "weird", Saved: tok(100)}}}
	f := fig(gpt56{}, accountant.Usage{Input: 1_000}, 0, r)
	if len(f.Unpriced) != 1 || f.DeltaUSD != 0 || f.Effects[0].Counted {
		t.Fatalf("got %+v", f)
	}
}
