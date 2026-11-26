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

// The size bound uses the host's TokensBefore, plus the tokens the operations caused. 0 means the host
// does not give it: no size gate.
func TestTokensBeforeGatesTheSize(t *testing.T) {
	for _, c := range []struct {
		before int
		want   string
	}{
		{0, ""},
		{1990, ""}, // saved 2,000 = 1,990 before + 10 caused
		{1989, accountant.WithheldImplausibleSize},
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

// A withheld request above a price line books no re-rate: the re-rate shares of the operations and the
// re-rate no operation owns come from the same figure, so the sink drops them with the saving.
func TestWithheldDropsTheReRate(t *testing.T) {
	o := sizedObs(250_000, rewrite(50_000, 10_000))
	saved, added := usd(t, tiered{}, o)
	if saved <= 50_000*2e-6 {
		t.Fatalf("saved usd = %v: the clean request must have a re-rate for this test to mean anything", saved)
	}
	o.Result.Checks = []accountant.CheckFailure{{Check: accountant.CheckSum}}
	s, r := newSinkWith(t, tiered{})
	s.Observe(o)
	got := collect(t, r)
	for _, name := range []string{"llm.savings.saved.usd", "llm.savings.added.usd", "llm.savings.interaction.usd"} {
		if len(got[name]) != 0 {
			t.Errorf("%s = %+v, want no point on a withheld request (clean: saved %v, added %v)", name, got[name], saved, added)
		}
	}
}

// One unpriced operation withholds the whole request: the priced operation beside it books nothing
// (design §6.7). Before the gate, the sink booked the priced one.
func TestOneUnpricedOpWithholdsTheRequest(t *testing.T) {
	s, r := newSink(t)
	o := obs("m",
		accountant.PricedOp{OpID: "ok", Kind: accountant.KindRewrite, Operator: "h.a", Saved: accountant.TierTokens{Input: 1000}},
		accountant.PricedOp{OpID: "bad", Kind: accountant.KindRewrite, Operator: "h.b"},
	)
	o.Result.Unpriced = []accountant.Unpriced{{OpID: "bad", Reason: accountant.ReasonSizeRange}}
	s.Observe(o)
	got := collect(t, r)
	if len(got["llm.savings.saved.tokens"]) != 0 || len(got["llm.savings.saved.usd"]) != 0 {
		t.Errorf("saved = %v / %v, want none", got["llm.savings.saved.tokens"], got["llm.savings.saved.usd"])
	}
	if w := got["llm.savings.withheld"]; len(w) != 1 || w[0].attrs["reason"] != accountant.WithheldUnpriced {
		t.Errorf("withheld = %+v, want unpriced", w)
	}
	if p := got["llm.savings.requests"]; len(p) != 1 || p[0].attrs["result"] != "unpriced" {
		t.Errorf("requests = %+v, want result unpriced (it wins over withheld)", p)
	}
}

// A negative TokensBefore is "not given", as 0 is: the library's own bound decides (design §6.7). The
// request saves fewer message tokens than its messages hold, so nothing is withheld.
func TestNegativeTokensBeforeUsesTheLibrarysBound(t *testing.T) {
	s, r := newSink(t)
	o := withheldObs()
	o.Result.MessageTokens = 5000
	o.Result.Ops[0].SavedMessages = accountant.TierTokens{Input: 1000}
	o.TokensBefore = -7
	s.Observe(o)
	if w := collect(t, r)["llm.savings.withheld"]; len(w) != 0 {
		t.Errorf("withheld = %+v, want none", w)
	}
}

// With no TokensBefore the sink uses the library's own size bound (design §6.7). A request that saves more
// message tokens than its messages hold is withheld, so the push agrees with a host that gates the same way.
// A large tool removal is not withheld: tool tokens are not message tokens.
func TestNoTokensBeforeUsesTheLibrarysOwnBound(t *testing.T) {
	for _, c := range []struct {
		name string
		op   accountant.PricedOp
		want string
	}{
		{"messages saved over their size", accountant.PricedOp{Kind: accountant.KindRewrite, Operator: "h.a", ChangeID: "a",
			Saved: accountant.TierTokens{Input: 1800}, SavedMessages: accountant.TierTokens{Input: 1800}}, accountant.WithheldImplausibleSize},
		{"a large tool removal", accountant.PricedOp{Kind: accountant.KindRewrite, Operator: "h.defer", ChangeID: "d",
			Saved: accountant.TierTokens{Input: 24000}}, ""},
	} {
		s, r := newSink(t)
		o := obs("m", c.op)
		o.Result.MessageTokens = 1000
		s.Observe(o)
		got := collect(t, r)
		w := got["llm.savings.withheld"]
		switch {
		case c.want == "" && len(w) != 0:
			t.Errorf("%s: withheld = %+v, want none", c.name, w)
		case c.want != "" && (len(w) != 1 || w[0].attrs["reason"] != c.want):
			t.Errorf("%s: withheld = %+v, want reason %s", c.name, w, c.want)
		}
		if c.want != "" && len(got["llm.savings.saved.tokens"]) != 0 {
			t.Errorf("%s: saved.tokens = %+v, want none on a withheld request", c.name, got["llm.savings.saved.tokens"])
		}
	}
}
