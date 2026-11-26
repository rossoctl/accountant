package engine

import "testing"

func TestWithhold(t *testing.T) {
	saved := func(n int) PricedOp { return PricedOp{Kind: KindRewrite, Saved: TierTokens{Input: n}} }
	check := func(name string) []CheckFailure { return []CheckFailure{{Check: name}} }
	for _, c := range []struct {
		name   string
		res    Result
		before int
		want   string
	}{
		{"clean", Result{Ops: []PricedOp{saved(100)}}, 1000, ""},
		{"sum", Result{Checks: check(CheckSum)}, 1000, WithheldCheckSum},
		{"unique change", Result{Checks: check(CheckUniqueChange)}, 1000, WithheldCheckUniqueChange},
		{"spans", Result{Checks: check(CheckSpans)}, 1000, WithheldCheckSpans},
		{"informational check", Result{Ops: []PricedOp{saved(100)}, Checks: check(CheckConservation)}, 1000, ""},
		{"informational then fatal", Result{Checks: []CheckFailure{{Check: CheckConservation}, {Check: CheckSpans}}}, 1000, WithheldCheckSpans},
		{"unpriced op", Result{Unpriced: []Unpriced{{OpID: "1", Reason: ReasonUnknownKind}}}, 1000, WithheldUnpriced},
		{"unpriced request", Result{Unpriced: []Unpriced{{Reason: ReasonBadUsage}}}, 1000, WithheldUnpriced},
		{"penalty unknown", Result{Ops: []PricedOp{saved(100)}, Unpriced: []Unpriced{{Reason: ReasonPenaltyUnknown}}}, 1000, ""},
		{"size unknown", Result{Ops: []PricedOp{saved(100)}}, 0, WithheldSizeUnknown},
		{"size not given", Result{Ops: []PricedOp{saved(5000)}}, SizeNotGiven, ""},
		{"size not given, fatal check", Result{Checks: check(CheckSum)}, SizeNotGiven, WithheldCheckSum},
		{"nothing saved, size unknown", Result{}, 0, ""},
		{"at the bound", Result{Ops: []PricedOp{saved(600), saved(400)}}, 1000, ""},
		{"over the bound", Result{Ops: []PricedOp{saved(600), saved(401)}}, 1000, WithheldImplausibleSize},
		{"every tier counts", Result{Ops: []PricedOp{{Kind: KindRewrite,
			Saved: TierTokens{Input: 300, CacheWrite5m: 300, CacheWrite1h: 300, CacheRead: 101}}}}, 1000, WithheldImplausibleSize},
		{"dry run and spend not counted", Result{Ops: []PricedOp{
			{Kind: KindRewrite, DryRun: true, Saved: TierTokens{Input: 5000}},
			{Kind: KindSpend, Saved: TierTokens{Input: 5000}},
			saved(100),
		}}, 1000, ""},
		{"a check before the bound", Result{Ops: []PricedOp{saved(5000)}, Checks: check(CheckSpans)}, 1000, WithheldCheckSpans},
		{"unknown effect kind", Result{Ops: []PricedOp{saved(100)}, CacheEffects: []CacheEffect{{Kind: "future"}}}, SizeNotGiven, WithheldUnpriced},
		{"known effect kinds", Result{Ops: []PricedOp{saved(100)}, CacheEffects: []CacheEffect{{Kind: EffectBreak}, {Kind: EffectRescue},
			{Kind: EffectSplit}, {Kind: EffectTTLPremium}, {Kind: EffectTTLRescue}, {Kind: EffectMarkPremium}, {Kind: EffectMarkPayback}}}, 1000, ""},
		// more_examples.md#I44: retrieve inserts 25,000 tokens into an 8,000-token body, and cmdfilter
		// after it removes 20,000 of them. The Result is correct: no check fails.
		{"an operator removes what an earlier one inserted", Result{Ops: []PricedOp{
			{Kind: KindRewrite, Operator: "context-guru.retrieve", Caused: TierTokens{CacheWrite5m: 25000}},
			{Kind: KindRewrite, Operator: "context-guru.cmdfilter", Saved: TierTokens{CacheWrite5m: 20000}},
		}, Unpriced: []Unpriced{{Reason: ReasonPenaltyUnknown}}}, 8000, ""},
		{"over the bound with an insertion", Result{Ops: []PricedOp{
			{Kind: KindRewrite, Caused: TierTokens{CacheWrite5m: 25000}},
			{Kind: KindRewrite, Saved: TierTokens{CacheWrite5m: 33001}},
		}}, 8000, WithheldImplausibleSize},
		{"a dry run's insertion does not widen the bound", Result{Ops: []PricedOp{
			{Kind: KindRewrite, DryRun: true, Caused: TierTokens{Input: 5000}},
			saved(1001),
		}}, 1000, WithheldImplausibleSize},
		{"negative size other than SizeNotGiven", Result{Ops: []PricedOp{saved(100)}}, -5, WithheldSizeUnknown},
		{"unpriced before the bound", Result{Ops: []PricedOp{saved(5000)}, Unpriced: []Unpriced{{Reason: ReasonBadUsage}}}, 1000, WithheldUnpriced},
	} {
		if got := Withhold(c.res, c.before); got != c.want {
			t.Errorf("%s: Withhold = %q, want %q", c.name, got, c.want)
		}
	}
}

// Each check of this list gates exactly when it is fatal. Withhold reads Fatal, so a check that becomes
// fatal later gates too (as check_other until it has a name). TestEveryCheckHasAFatalClass keeps the
// class of each check decided; this list is the checks of today.
func TestWithholdGatesEveryFatalCheck(t *testing.T) {
	for _, name := range []string{CheckSum, CheckUniqueChange, CheckSpans, CheckConservation, CheckSharedWarm,
		CheckBadUsage, CheckRatioRange, CheckMediaUnseen, CheckBreakpoints, CheckMediaTokens, CheckSizeTokens,
		CheckSizeRange, CheckOpaqueTokens} {
		fatal := CheckFailure{Check: name}.Fatal()
		got := Withhold(Result{Checks: []CheckFailure{{Check: name}}}, SizeNotGiven)
		if fatal && got == "" {
			t.Errorf("%s is fatal, and Withhold gives no reason", name)
		}
		if !fatal && got != "" {
			t.Errorf("%s is not fatal, and Withhold gives %q", name, got)
		}
	}
}
