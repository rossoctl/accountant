package metrics

import (
	"testing"

	"github.com/rossoctl/accountant"
)

// withheldObs is a request with a saving, a cost, a cache effect, an interaction and a spend.
func withheldObs() Observation {
	o := obs("m",
		accountant.PricedOp{Kind: accountant.KindRewrite, Operator: "h.a", ChangeID: "a",
			Saved: accountant.TierTokens{Input: 1000}, Caused: accountant.TierTokens{CacheWrite5m: 10}},
		accountant.PricedOp{Kind: accountant.KindRewrite, Operator: "h.b", ChangeID: "b",
			Saved: accountant.TierTokens{Input: 1000}},
		accountant.PricedOp{Kind: accountant.KindSpend, Operator: "h.summarize",
			Call: &accountant.ModelCall{Model: "m", Usage: accountant.CallUsage{
				Usage: accountant.Usage{Input: 300}, Output: 20}}},
	)
	o.Result.CacheEffects = []accountant.CacheEffect{
		{ID: "ce:1", Kind: accountant.EffectBreak, ChangeID: "a", Caused: accountant.TierTokens{CacheWrite5m: 2000}},
	}
	o.Result.Interaction = accountant.TierTokens{CacheWrite5m: 1000}
	return o
}

// Two operators claim one removal: the sum check fails. The sink books the spend's call, and no saving,
// cost, cache effect or interaction (rossoctl/accountant#116).
func TestFatalCheckBooksOnlyTheSpend(t *testing.T) {
	s, r := newSink(t)
	o := withheldObs()
	o.Result.Checks = []accountant.CheckFailure{{Check: accountant.CheckSum}}
	s.Observe(o)
	got := collect(t, r)

	for _, name := range []string{"llm.savings.saved.tokens", "llm.savings.saved.usd",
		"llm.savings.cache_effect.usd", "llm.savings.interaction.usd"} {
		if len(got[name]) != 0 {
			t.Errorf("%s = %+v, want no point on a withheld request", name, got[name])
		}
	}
	for _, p := range got["llm.savings.added.tokens"] {
		if p.attrs["kind"] != string(accountant.KindSpend) {
			t.Errorf("added.tokens point %+v, want only the spend's call", p)
		}
	}
	if n := sumI(got["llm.savings.added.tokens"]); n != 320 {
		t.Errorf("added.tokens = %d, want 320 (the call's 300 input and 20 output)", n)
	}
	if usd := sumF(got["llm.savings.added.usd"]); usd < 0.3999 || usd > 0.4001 {
		t.Errorf("added.usd = %v, want 0.4 (300 at 0.001 and 20 at 0.005)", usd)
	}
	for _, p := range got["llm.savings.added.usd"] {
		if p.attrs["kind"] != string(accountant.KindSpend) {
			t.Errorf("added.usd point %+v, want only the spend's call", p)
		}
	}
	w := got["llm.savings.withheld"]
	if len(w) != 1 || w[0].i != 1 || w[0].attrs["reason"] != accountant.WithheldCheckSum {
		t.Errorf("withheld = %+v, want 1 with reason check_sum", w)
	}
	if n := sumI(got["llm.savings.check_failures"]); n != 1 {
		t.Errorf("check_failures = %d, want 1: the diagnostic still moves", n)
	}
	if p := got["llm.savings.requests"]; len(p) != 1 || p[0].attrs["result"] != "withheld" {
		t.Errorf("requests = %+v, want result withheld", p)
	}
}

// The same request with no failed check books everything, and nothing is withheld.
func TestCleanRequestIsNotWithheld(t *testing.T) {
	s, r := newSink(t)
	s.Observe(withheldObs())
	got := collect(t, r)
	if n := sumI(got["llm.savings.saved.tokens"]); n != 2000 {
		t.Errorf("saved.tokens = %d, want 2000", n)
	}
	if len(got["llm.savings.cache_effect.usd"]) == 0 || len(got["llm.savings.interaction.usd"]) == 0 {
		t.Errorf("cache_effect or interaction missing: %v", got)
	}
	if len(got["llm.savings.withheld"]) != 0 {
		t.Errorf("withheld = %+v, want none", got["llm.savings.withheld"])
	}
	if p := got["llm.savings.requests"]; len(p) != 1 || p[0].attrs["result"] != "priced" {
		t.Errorf("requests = %+v, want result priced", p)
	}
}

// The size bound uses the host's TokensBefore. 0 means the host does not give it: no size gate.
func TestTokensBeforeGatesTheSize(t *testing.T) {
	for _, c := range []struct {
		before int
		want   string
	}{
		{0, ""},
		{2000, ""},
		{1999, accountant.WithheldImplausibleSize},
	} {
		s, r := newSink(t)
		o := withheldObs()
		o.TokensBefore = c.before
		s.Observe(o)
		got := collect(t, r)
		reason := ""
		if w := got["llm.savings.withheld"]; len(w) == 1 {
			reason = w[0].attrs["reason"]
		}
		if reason != c.want {
			t.Errorf("TokensBefore %d: withheld reason = %q, want %q", c.before, reason, c.want)
		}
		if saved := sumI(got["llm.savings.saved.tokens"]); (c.want == "") != (saved == 2000) {
			t.Errorf("TokensBefore %d: saved.tokens = %d", c.before, saved)
		}
	}
}

// A token counter moves for every billing mode: a withheld request on a subscription books its spend's
// tokens and no saving.
func TestWithheldWithoutMoney(t *testing.T) {
	s, r := newSink(t)
	o := withheldObs()
	o.Meta.Billing.Mode = accountant.BillingSubscription
	o.Result.Checks = []accountant.CheckFailure{{Check: accountant.CheckSpans}}
	s.Observe(o)
	got := collect(t, r)
	if len(got["llm.savings.saved.tokens"]) != 0 || len(got["llm.savings.added.usd"]) != 0 {
		t.Errorf("saved.tokens %v, added.usd %v: want none", got["llm.savings.saved.tokens"], got["llm.savings.added.usd"])
	}
	if n := sumI(got["llm.savings.added.tokens"]); n != 320 {
		t.Errorf("added.tokens = %d, want 320", n)
	}
	if w := got["llm.savings.withheld"]; len(w) != 1 || w[0].attrs["reason"] != accountant.WithheldCheckSpans {
		t.Errorf("withheld = %+v, want check_spans", w)
	}
}
