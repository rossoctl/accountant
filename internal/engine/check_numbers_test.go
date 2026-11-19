package engine

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Issue #90: a failed check carries its numbers in typed fields (docs/events/events.md §6.8.1), so a
// host never parses Detail.

// num is the value of an optional number, or "absent".
func num(p *int) string {
	if p == nil {
		return "absent"
	}
	return strconv.Itoa(*p)
}

// wantNums compares the optional int fields of f with want. A field that is not in want must be absent.
func wantNums(t *testing.T, f CheckFailure, want map[string]int) {
	t.Helper()
	fields := map[string]*int{
		"Index": f.Index, "Want": f.Want, "WantAfter": f.WantAfter, "Got": f.Got, "GotAfter": f.GotAfter,
		"Limit": f.Limit, "LimitAfter": f.LimitAfter, "Tokens": f.Tokens, "TokensAfter": f.TokensAfter,
		"Bytes": f.Bytes, "BytesAfter": f.BytesAfter,
	}
	for name, p := range fields {
		w, ok := want[name]
		switch {
		case !ok && p != nil:
			t.Errorf("%s: %s = %d, want absent", f.Check, name, *p)
		case ok && (p == nil || *p != w):
			t.Errorf("%s: %s = %s, want %d", f.Check, name, num(p), w)
		}
	}
	if f.Check != CheckSizeTokens && (f.RatioMin != nil || f.RatioMax != nil) {
		t.Errorf("%s: RatioMin/RatioMax set, want absent", f.Check)
	}
}

// findCheck returns the one failure named name.
func findCheck(t *testing.T, res Result, name string) CheckFailure {
	t.Helper()
	for _, c := range res.Checks {
		if c.Check == name {
			return c
		}
	}
	t.Fatalf("Checks = %+v, want %s", res.Checks, name)
	return CheckFailure{}
}

// The unit depends only on the check (§6.8.1); a check with no want and got has none.
func TestCheckUnitFollowsTheTable(t *testing.T) {
	want := map[string]string{
		CheckSum: UnitBytes, CheckSpans: UnitBytes, CheckSizeRange: UnitBytes,
		CheckConservation: UnitTokens, CheckMediaTokens: UnitTokens, CheckOpaqueTokens: UnitTokens,
		CheckSizeTokens: "", CheckUniqueChange: "", CheckSharedWarm: "", CheckBadUsage: "",
		CheckRatioRange: "", CheckBreakpoints: "", CheckMediaUnseen: "", "other": "",
	}
	for name, u := range want {
		if got := (CheckFailure{Check: name}).Unit(); got != u {
			t.Errorf("%s: Unit = %q, want %q", name, got, u)
		}
	}
}

// An unreported 400-byte message: the bodies changed 600 bytes in messages, the operations claim 0.
// 0 is a real value, so Got is present.
func TestTheSumCheckCarriesItsNumbers(t *testing.T) {
	res := req{at: t0, client: mk(nil, nil, msgs(2, 1000)), fwd: mk(nil, nil, []blk{b("m0", 1000), b("x", 400)}),
		usage: Usage{Input: 1400}}.run(NewTrackers())
	f := findCheck(t, res, CheckSum)
	if f.Section != SectionMessages {
		t.Errorf("Section = %q, want messages", f.Section)
	}
	wantNums(t, f, map[string]int{"Want": 600, "Got": 0})
}

func TestTheSpansCheckCarriesItsNumbers(t *testing.T) {
	op := Operation{ID: "op1", Kind: KindRewrite, Operator: "tool-prune", ChangeID: "tp", At: tool(0),
		Spans:      []Span{{Section: SectionTools, From: 0, To: 0, SizeBefore: Size{Bytes: 100}}},
		SizeBefore: Size{Bytes: 150}}
	res := req{at: t0, client: mk([]blk{b("t0", 100)}, nil, msgs(1, 100)), fwd: mk(nil, nil, msgs(1, 100)),
		ops: []Operation{op}, usage: Usage{Input: 100}}.run(NewTrackers())
	f := findCheck(t, res, CheckSpans)
	if f.Operator != "tool-prune" || f.Section != "" {
		t.Errorf("Operator = %q Section = %q, want tool-prune and no section", f.Operator, f.Section)
	}
	wantNums(t, f, map[string]int{"Want": 100, "WantAfter": 0, "Got": 150, "GotAfter": 0})
}

// The limit is a rule, not a claim, so it is in Limit; the operation's bytes are in Got.
func TestTheSizeRangeCheckCarriesItsNumbers(t *testing.T) {
	op := rewrite("o1", "trim", "c1", msg(0), math.MaxInt, 0)
	res := req{at: t0, client: mk(nil, nil, []blk{b("a", 2000)}), clientBPs: bps5(msg(0)), facts: exact5m,
		ops: []Operation{op}, usage: Usage{Input: 2000}}.run(NewTrackers())
	f := findCheck(t, res, CheckSizeRange)
	if f.Limit == nil || f.LimitAfter == nil || *f.Limit < 2000 || *f.Limit >= math.MaxInt {
		t.Fatalf("Limit = %s LimitAfter = %s, want the client body's bytes", num(f.Limit), num(f.LimitAfter))
	}
	wantNums(t, f, map[string]int{"Got": math.MaxInt, "GotAfter": 0, "Limit": *f.Limit, "LimitAfter": *f.LimitAfter})
}

// Two units and no difference, so the numbers are in Tokens and Bytes, not in Want and Got.
func TestTheSizeTokensCheckCarriesItsNumbers(t *testing.T) {
	client := [][]byte{text("j", 10000), text("p", 40000)}
	fwd := [][]byte{nil, text("p", 40000)}
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(0),
		SizeBefore: Size{Bytes: 10000, Tokens: 500000}}
	res, _ := priceOne(t, client, fwd, op, Usage{Input: 12500})
	f := findCheck(t, res, CheckSizeTokens)
	wantNums(t, f, map[string]int{"Tokens": 500000, "TokensAfter": 0, "Bytes": 10000, "BytesAfter": 0})
	if f.RatioMin == nil || f.RatioMax == nil || *f.RatioMin != minTokensPerByte || *f.RatioMax != maxTokensPerByte {
		t.Errorf("RatioMin/RatioMax = %v/%v, want %g/%g", f.RatioMin, f.RatioMax, minTokensPerByte, maxTokensPerByte)
	}
}

// 100 tokens billed against a 1600-token image.
func TestTheMediaTokensCheckCarriesItsNumbers(t *testing.T) {
	body := [][]byte{imageBlock(400000), text("t", 40)}
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(1),
		SizeBefore: Size{Bytes: 40}, SizeAfter: Size{Bytes: 40}}
	res, _ := priceOne(t, body, body, op, Usage{Input: 100})
	wantNums(t, findCheck(t, res, CheckMediaTokens), map[string]int{"Want": 100, "Got": 1600})
}

func TestTheSharedWarmCheckCarriesItsPosition(t *testing.T) {
	f := pingFacts()
	f.SharedWarm = ptr(msg(99))
	c := findCheck(t, ex8(28*time.Minute, f), CheckSharedWarm)
	if c.Section != SectionMessages {
		t.Errorf("Section = %q, want messages", c.Section)
	}
	wantNums(t, c, map[string]int{"Index": 99})
}

// Detail is built from the typed fields, so the two cannot drift. No request in the suite fails
// conservation or opaque_tokens alone, so these two are checked on their constructors.
func TestDetailIsBuiltFromTheFields(t *testing.T) {
	for _, c := range []struct {
		f    CheckFailure
		nums map[string]int
		in   []string
	}{
		{conservationFailure(1200, 900), map[string]int{"Want": 1200, "Got": 900}, []string{"1200", "900"}},
		{opaqueTokensFailure(500, 800, 3000, 4400), map[string]int{"Want": 500, "Got": 800}, []string{"500", "800", "3000", "4400"}},
		{sumFailure(SectionTools, 600, 0), map[string]int{"Want": 600, "Got": 0}, []string{"tools", "600", "0"}},
		{spansFailure("p", 1, 2, 3, 4), map[string]int{"Want": 1, "WantAfter": 2, "Got": 3, "GotAfter": 4}, []string{"1/2", "3/4"}},
		{sizeRangeFailure("p", 9, 8, 7, 6), map[string]int{"Got": 9, "GotAfter": 8, "Limit": 7, "LimitAfter": 6}, []string{"9/8", "7/6"}},
		{sizeTokensFailure("p", 5, 4, 3, 2), map[string]int{"Tokens": 5, "TokensAfter": 4, "Bytes": 3, "BytesAfter": 2}, []string{"5/4", "3/2"}},
		{mediaTokensFailure(100, 1600, 90, "n"), map[string]int{"Want": 100, "Got": 1600}, []string{"100", "1600", "90"}},
		{sharedWarmFailure(Position{Section: SectionSystem, Index: 3}), map[string]int{"Index": 3}, []string{"system", "3"}},
	} {
		wantNums(t, c.f, c.nums)
		for _, s := range c.in {
			if !strings.Contains(c.f.Detail, s) {
				t.Errorf("%s: Detail %q does not hold %q", c.f.Check, c.f.Detail, s)
			}
		}
	}
}
