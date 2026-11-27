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
		// Two operations claim one removal: ops claim 8,128 bytes, the bodies lost 4,064 (design §6.7).
		{"sum, ops claim bytes", Result{Ops: []PricedOp{saved(100)},
			Checks: []CheckFailure{sumFailure(SectionTools, 4064, 8128, 2)}}, 1000, WithheldCheckSum},
		// A plugin appends to system and reports nothing. A reported op in tools is still right.
		{"sum, unreported change only", Result{Ops: []PricedOp{saved(100)},
			Checks: []CheckFailure{sumFailure(SectionSystem, -25, 0, 0)}}, 1000, ""},
		{"sum, unreported change, nothing to book", Result{Checks: []CheckFailure{sumFailure(SectionSystem, -25, 0, 0)}}, 1000, ""},
		{"sum, unreported change and an overclaim", Result{Ops: []PricedOp{saved(100)},
			Checks: []CheckFailure{sumFailure(SectionSystem, -25, 0, 0), sumFailure(SectionTools, 4064, 8128, 2)}}, 1000, WithheldCheckSum},
		{"sum, unreported change, then spans", Result{Ops: []PricedOp{saved(100)},
			Checks: []CheckFailure{sumFailure(SectionSystem, -25, 0, 0), {Check: CheckSpans}}}, 1000, WithheldCheckSpans},
		// Got is a net: a false removal of 100 bytes and a real insert of 100 bytes cancel to Got 0. The
		// section is claimed (Claims 2), so it gates.
		{"sum, claims cancel to Got 0", Result{Ops: []PricedOp{saved(100)},
			Checks: []CheckFailure{sumFailure(SectionTools, -100, 0, 2)}}, 1000, WithheldCheckSum},
		{"sum, Claims absent", Result{Checks: []CheckFailure{{Check: CheckSum, Got: intp(0)}}}, 1000, WithheldCheckSum},
		// An unreported change in the section of a reported op: ops claim bytes, so it gates.
		{"sum, reported and unreported in one section", Result{Ops: []PricedOp{saved(100)},
			Checks: []CheckFailure{sumFailure(SectionMessages, 4089, 4064, 1)}}, 1000, WithheldCheckSum},
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

// End to end: a reported removal in tools, and a 100-byte system block that no operation reported (a
// host plugin that injects text). The sum check fails for system with Got 0, and the removal still books.
func TestWithholdKeepsAReportedOpBesideAnUnreportedChange(t *testing.T) {
	client := mk([]blk{b("t0", 1000), b("t1", 1000)}, []blk{b("s0", 500)}, msgs(2, 1000))
	fwd := mk([]blk{b("t0", 1000)}, []blk{b("s0", 500), b("inj", 100)}, msgs(2, 1000))
	res := req{at: t0, client: client, fwd: fwd, usage: Usage{Input: 3600},
		ops: []Operation{rewrite("o1", "h.prune", "p:t1", Position{Section: SectionTools, Index: 1}, 1000, 0)}}.run(NewTrackers())
	f := findCheck(t, res, CheckSum)
	if f.Section != SectionSystem || f.Got == nil || *f.Got != 0 || f.Claims == nil || *f.Claims != 0 {
		t.Fatalf("sum failure = %+v, want section system with Got 0 and Claims 0", f)
	}
	if res.Reliable() {
		t.Fatal("Reliable = true: the sum failure is still fatal, only Withhold scopes it")
	}
	if got := tierSum(findOp(t, res, "o1").Saved); got <= 0 {
		t.Fatalf("o1 saved %d, want a saving", got)
	}
	if got := Withhold(res, 4000); got != "" {
		t.Errorf("Withhold = %q, want \"\": the reported removal is right", got)
	}
}

// End to end: two operations claim the same removal (design §6.7). The sum check fails with Got above
// Want, and Withhold gates.
func TestWithholdGatesADoubleClaim(t *testing.T) {
	client := mk([]blk{b("t0", 1000), b("t1", 1000)}, nil, msgs(2, 1000))
	fwd := mk([]blk{b("t0", 1000)}, nil, msgs(2, 1000))
	res := req{at: t0, client: client, fwd: fwd, usage: Usage{Input: 3000}, ops: []Operation{
		rewrite("o1", "h.a", "a:t1", Position{Section: SectionTools, Index: 1}, 1000, 0),
		rewrite("o2", "h.b", "b:t1", Position{Section: SectionTools, Index: 1}, 1000, 0),
	}}.run(NewTrackers())
	f := findCheck(t, res, CheckSum)
	if f.Got == nil || *f.Got == 0 {
		t.Fatalf("sum failure = %+v, want Got above 0", f)
	}
	if got := Withhold(res, 4000); got != WithheldCheckSum {
		t.Errorf("Withhold = %q, want %q", got, WithheldCheckSum)
	}
}

// End to end: claims that cancel. An insert of a 100-byte tool is real. A removal of 100 bytes from t1
// is false: t1 is still 1,000 bytes in the forwarded body. The net claim is 0, but the section is
// claimed, so Withhold gates (review of #121).
func TestWithholdGatesClaimsThatCancel(t *testing.T) {
	client := mk([]blk{b("t0", 1000), b("t1", 1000)}, nil, msgs(2, 1000))
	fwd := mk([]blk{b("t0", 1000), b("t1", 1000), b("new", 100)}, nil, msgs(2, 1000))
	res := req{at: t0, client: client, fwd: fwd, usage: Usage{Input: 4100}, ops: []Operation{
		rewrite("i1", "h.insert", "c:new", Position{Section: SectionTools, Index: 2}, 0, 100),
		rewrite("o1", "h.prune", "p:t1", Position{Section: SectionTools, Index: 1}, 1000, 900),
	}}.run(NewTrackers())
	f := findCheck(t, res, CheckSum)
	if f.Got == nil || *f.Got != 0 || f.Claims == nil || *f.Claims == 0 {
		t.Fatalf("sum failure = %+v, want Got 0 and Claims above 0", f)
	}
	if got := Withhold(res, 5000); got != WithheldCheckSum {
		t.Errorf("Withhold = %q, want %q: the false removal must not book", got, WithheldCheckSum)
	}
}

// The size bound of Withhold(res) with no size argument (design §6.7). The library measures the client
// messages itself and counts message tokens only.
func TestWithholdOwnSizeBound(t *testing.T) {
	saved := func(n int) PricedOp {
		return PricedOp{Kind: KindRewrite, Saved: TierTokens{Input: n}, SavedMessages: TierTokens{Input: n}}
	}
	for _, c := range []struct {
		name string
		res  Result
		want string
	}{
		{"within the messages", Result{MessageTokens: 1000, Ops: []PricedOp{saved(600), saved(400)}}, ""},
		{"over the messages", Result{MessageTokens: 1000, Ops: []PricedOp{saved(600), saved(401)}}, WithheldImplausibleSize},
		{"nothing saved", Result{}, ""},
		{"messages unmeasured, tokens saved", Result{Ops: []PricedOp{saved(1)}}, WithheldImplausibleSize},
		{"tool declarations do not count", Result{MessageTokens: 121, Ops: []PricedOp{
			{Kind: KindRewrite, Operator: "h.defer", Saved: TierTokens{CacheRead: 2188}}}}, ""},
		{"an insertion in messages widens the bound", Result{MessageTokens: 8000, Ops: []PricedOp{
			{Kind: KindRewrite, CausedMessages: TierTokens{CacheWrite5m: 25000}},
			{Kind: KindRewrite, SavedMessages: TierTokens{CacheWrite5m: 20000}}}}, ""},
		{"an insertion in tools does not widen it", Result{MessageTokens: 1000, Ops: []PricedOp{
			{Kind: KindRewrite, Caused: TierTokens{Input: 5000}},
			saved(1001)}}, WithheldImplausibleSize},
		{"dry run and spend not counted", Result{MessageTokens: 1000, Ops: []PricedOp{
			{Kind: KindRewrite, DryRun: true, SavedMessages: TierTokens{Input: 5000}},
			{Kind: KindSpend, SavedMessages: TierTokens{Input: 5000}}, saved(100)}}, ""},
		{"a check before the bound", Result{Ops: []PricedOp{saved(5000)}, Checks: []CheckFailure{{Check: CheckSpans}}}, WithheldCheckSpans},
	} {
		if got := Withhold(c.res); got != c.want {
			t.Errorf("%s: Withhold = %q, want %q", c.name, got, c.want)
		}
	}
}

// End to end, the deferral case (design §6.7): the operator removes 8 of 10 tool declarations
// (24,000 tokens) from a request whose messages are 1,000 tokens. The request is healthy. The old bound,
// fed the message size, withholds it only because it counted the tool tokens.
func TestWithholdKeepsALargeToolRemoval(t *testing.T) {
	tools := make([]blk, 10)
	for i := range tools {
		tools[i] = b("t"+itoa(i), 3000)
	}
	client := mk(tools, nil, msgs(2, 500))
	fwd := mk(tools[:2], nil, msgs(2, 500))
	res := req{at: t0, client: client, fwd: fwd, usage: Usage{Input: 7000}, ops: []Operation{
		rewrite("o1", "h.defer", "d:tools", Position{Section: SectionTools, Index: 2}, 24000, 0),
	}}.run(NewTrackers())
	op := findOp(t, res, "o1")
	if tierSum(op.Saved) != 24000 || !op.SavedMessages.IsZero() {
		t.Fatalf("o1 Saved = %+v, SavedMessages = %+v, want 24000 in tools and none in messages", op.Saved, op.SavedMessages)
	}
	if res.MessageTokens != 1000 {
		t.Fatalf("MessageTokens = %d, want 1000", res.MessageTokens)
	}
	if got := Withhold(res); got != "" {
		t.Errorf("Withhold = %q, want \"\": a tool removal is not a size fault", got)
	}
	if got := Withhold(res, 1000); got != WithheldImplausibleSize {
		t.Errorf("legacy Withhold(res, 1000) = %q, want %q: the old form counts every operation", got, WithheldImplausibleSize)
	}
}

// The bound is a defense: the span and sum checks already keep the saved bytes inside the body, so no
// request that passes them can save more message tokens than its messages hold. A result that does is
// withheld, also when its own checks are clean.
func TestWithholdGatesAnImpossibleMessageSaving(t *testing.T) {
	res := Result{MessageTokens: 1000, Ops: []PricedOp{
		{OpID: "o1", Kind: KindRewrite, Saved: TierTokens{Input: 1800}, SavedMessages: TierTokens{Input: 1800}},
	}}
	if got := Withhold(res); got != WithheldImplausibleSize {
		t.Errorf("Withhold = %q, want %q", got, WithheldImplausibleSize)
	}
	res.Ops[0].CausedMessages = TierTokens{Input: 800}
	if got := Withhold(res); got != "" {
		t.Errorf("Withhold with the caused tokens = %q, want \"\"", got)
	}
}

// End to end: the host counts the removed message at 0.67 tokens a byte (600 for 900 bytes). Check 12
// accepts that. The request ratio is 0.25, so the whole messages section is 250 tokens and the host
// count alone is above it. The bound measures at the ratio (225 tokens), so the saving is booked.
func TestWithholdKeepsDenserThanRatioRemovedContent(t *testing.T) {
	client := mk([]blk{b("t0", 4000)}, nil, []blk{b("m0", 100), b("m1", 900)})
	fwd := mk([]blk{b("t0", 4000)}, nil, []blk{b("m0", 100)})
	op := rewrite("o1", "h.drop", "d:m1", msg(1), 900, 0)
	op.SizeBefore.Tokens = 600
	res := req{at: t0, client: client, fwd: fwd, usage: Usage{Input: 1025}, ops: []Operation{op}}.run(NewTrackers())
	p := findOp(t, res, "o1")
	if got := tierSum(p.Saved); got != 600 {
		t.Fatalf("Saved = %d, want the host's 600", got)
	}
	if got := tierSum(p.SavedMessages); got != 225 || res.MessageTokens != 250 {
		t.Fatalf("SavedMessages = %d, MessageTokens = %d, want 225 and 250 (both at the ratio)", got, res.MessageTokens)
	}
	if got := Withhold(res); got != "" {
		t.Errorf("Withhold = %q, want \"\": dense content is not a size fault", got)
	}
}
