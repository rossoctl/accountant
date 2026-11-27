package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// The exported Ratio is the ratio Compute used: it converts bytes with the same integer math, for text,
// media, opaque bytes and a capped ratio.
func TestRatioConvertsLikeTheEngine(t *testing.T) {
	cases := []struct {
		name   string
		body   Body
		billed int
	}{
		{"text only", mk(nil, nil, msgs(4, 1000)), 1600},
		{"media", NewBody(nil, nil, [][]byte{imageBlock(400000), text("t", 4000)}), 1600 + 1000},
		{"opaque", NewBody(nil, nil, [][]byte{thinkingBlock("t", 25000), text("m", 20000)}), 8100},
		{"capped", NewBody(nil, nil, [][]byte{imageBlock(400000), text("t", 40)}), 100},
		{"empty", Body{}, 100},
	}
	for _, c := range cases {
		want := newRatio(c.billed, c.body)
		got := NewRatio(c.billed, c.body)
		tok, by := got.Text()
		if tok != want.tokens || by != want.bytes || got.Capped() != want.capped {
			t.Errorf("%s: Text/Capped = %d, %d, %v; want %d, %d, %v", c.name, tok, by, got.Capped(), want.tokens, want.bytes, want.capped)
		}
		for _, n := range []int{0, 1, 7, 1000, 123457} {
			if g, w := got.Tokens(n), want.of(n); g != w {
				t.Errorf("%s: Tokens(%d) = %d, want %d", c.name, n, g, w)
			}
		}
		// A ratio rebuilt from the stored counts converts the same text.
		again := RatioFromText(tok, by, got.Capped())
		for _, n := range []int{0, 7, 1000, 123457} {
			if g, w := again.Tokens(n), want.of(n); g != w {
				t.Errorf("%s: rebuilt Tokens(%d) = %d, want %d", c.name, n, g, w)
			}
		}
	}
	if !NewRatio(cases[3].billed, cases[3].body).Capped() {
		t.Error("the capped case is not capped: the test proves nothing")
	}
	if got := (Ratio{}).Tokens(1000); got != 0 {
		t.Errorf("zero Ratio.Tokens = %d, want 0", got)
	}
}

// Compute sets Result.Ratio. The saving of a cut of text bytes equals Ratio.Tokens of those bytes, and
// RemovalSaving with that ratio prices them the same as Figure prices the operation.
func TestRemovalWithRatioEqualsFigure(t *testing.T) {
	think := thinkingBlock("t", 25000)
	pad := 5 - (len(think)-25000)%5
	client := [][]byte{text("m0", 70000+pad), think, text("m2", 20000)}
	fwd := [][]byte{text("m0", 70000+pad), think, text("m2", 10000)}
	fb := NewBody(nil, nil, fwd)
	textBytes := fb.cumBytes(2) - fb.cumOpaque(2)
	billed := textBytes*2/5 + 100
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "c", At: msg(2),
		SizeBefore: Size{Bytes: 20000}, SizeAfter: Size{Bytes: 10000}}
	res := priceBP(client, fwd, op, billed)
	noChecks(t, res)
	if res.Ratio == nil {
		t.Fatal("Result.Ratio is nil on a priced request")
	}
	if want := NewRatio(billed, fb); *res.Ratio != want {
		t.Fatalf("Result.Ratio = %+v, want %+v", *res.Ratio, want)
	}
	saved := tierSum(findOp(t, res, "o1").Saved)
	if got := res.Ratio.Tokens(10000); got != saved || saved != 4000 {
		t.Fatalf("Ratio.Tokens(10000) = %d, saved = %d, want 4000 both", got, saved)
	}
	u := Usage{CacheWrite: billed}
	fig := Figure(FigureInput{Model: "m", At: t0, Billed: u, Result: res}, linePricer{})
	rem := RemovalSaving(RemovalInput{Model: "m", At: t0, Tier: HeadTier(u), Prompt: int64(billed),
		RemovedBytes: 10000, Ratio: res.Ratio}, linePricer{})
	if !removalNear(rem.SavedUSD, fig.ByComponent["mask"]) || rem.SavedUSD == 0 {
		t.Fatalf("RemovalSaving = %g, Figure = %g, want equal and above 0", rem.SavedUSD, fig.ByComponent["mask"])
	}
	// The factor is not applied twice: the tokens are already the provider's.
	again := RemovalSaving(RemovalInput{Model: "m", At: t0, Tier: HeadTier(u), Prompt: int64(billed),
		RemovedBytes: 10000, Ratio: res.Ratio, Factor: 3, Removed: 99999}, linePricer{})
	if again != rem {
		t.Fatalf("Factor and Removed changed the figure with a Ratio: %+v vs %+v", again, rem)
	}
	// An added list of names is converted with the same ratio.
	net := RemovalSaving(RemovalInput{Model: "m", At: t0, Tier: TierCacheRead, Prompt: int64(billed),
		RemovedBytes: 10000, AddedBytes: 2500, Ratio: res.Ratio}, linePricer{})
	if want := net.SavedUSD - net.AddedUSD; !removalNear(net.NetUSD, want) || net.AddedUSD <= 0 {
		t.Fatalf("NetUSD = %g, AddedUSD = %g", net.NetUSD, net.AddedUSD)
	}
}

// A request that was not priced has no ratio.
func TestResultRatioIsNilWithoutUsage(t *testing.T) {
	body := mk(nil, nil, msgs(2, 100))
	res := req{at: t0, client: body, fwd: body, facts: exact5m}.run(NewTrackers())
	if res.Ratio != nil {
		t.Fatalf("Ratio = %+v, want nil with no usage", res.Ratio)
	}
}

// With no Ratio the old path is unchanged: Removed, Added and Factor apply, and the byte fields are ignored.
func TestRemovalWithoutRatioIsUnchanged(t *testing.T) {
	in := RemovalInput{Model: "m", At: t0, Tier: TierCacheRead, Prompt: 10_000, Removed: 1000, Added: 200, Factor: 1.5,
		RemovedBytes: 77777, AddedBytes: 55555}
	got := RemovalSaving(in, linePricer{})
	if want := 1000 * 1.5 * 1e-7; !removalNear(got.SavedUSD, want) {
		t.Fatalf("SavedUSD = %g, want %g", got.SavedUSD, want)
	}
	if want := 200 * 1.5 * 1e-7; !removalNear(got.AddedUSD, want) {
		t.Fatalf("AddedUSD = %g, want %g", got.AddedUSD, want)
	}
	if want := 1.5e-7; !removalNear(got.PerTokenUSD, want) {
		t.Fatalf("PerTokenUSD = %g, want %g", got.PerTokenUSD, want)
	}
}

// InRange is check 9 on the text ratio: Compute sets Result.Ratio only for a ratio that is in range.
func TestRatioInRangeIsCheckNine(t *testing.T) {
	body := mk(nil, nil, msgs(4, 1000)) // 4000 bytes
	for _, c := range []struct {
		billed int
		want   bool
	}{{199, false}, {200, true}, {1600, true}, {8000, true}, {8001, false}, {0, false}} {
		if got := NewRatio(c.billed, body).InRange(); got != c.want {
			t.Errorf("billed %d over 4000 bytes: InRange = %v, want %v", c.billed, got, c.want)
		}
	}
	if (Ratio{}).InRange() {
		t.Error("the zero Ratio is in range")
	}
	// A request that fails check 9 has no Result.Ratio.
	res := req{at: t0, client: body, fwd: body, facts: exact5m, usage: Usage{Input: 10}}.run(NewTrackers())
	if res.Ratio != nil {
		t.Errorf("a request with 0.0025 tokens a byte has Ratio %+v, want nil", res.Ratio)
	}
}

// Result.Ratio is not part of the marshaled Result: a host that digests or stores a Result sees no change.
func TestResultRatioIsNotMarshaled(t *testing.T) {
	body := [][]byte{text("m0", 400), text("m1", 400), text("m2", 400)}
	res := priceBP(body, body,
		Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "c", At: msg(1), SizeBefore: Size{Bytes: 400}, SizeAfter: Size{Bytes: 400}}, 300)
	if res.Ratio == nil {
		t.Fatalf("the fixture was not priced: %+v", res.Checks)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"Ratio"`) {
		t.Fatalf("Result.Ratio was marshaled: %s", raw)
	}
}
