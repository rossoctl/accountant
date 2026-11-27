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

// A rebuilt ratio gives exactly the live ratio's tokens for text bytes, at every size, rounding edges included.
func TestRebuiltRatioEqualsTheLiveRatioForText(t *testing.T) {
	for _, billed := range []int{1600, 1601, 1777, 3333} {
		live := NewRatio(billed, mk(nil, nil, msgs(4, 999)))
		tok, by := live.Text()
		again := RatioFromText(tok, by, live.Capped())
		for n := 0; n <= 5000; n++ {
			if g, w := again.Tokens(n), live.Tokens(n); g != w {
				t.Fatalf("billed %d: Tokens(%d) = %d, live %d", billed, n, g, w)
			}
		}
		for _, n := range []int{by - 1, by, by + 1, 1<<31 - 1} {
			if g, w := again.Tokens(n), live.Tokens(n); g != w {
				t.Fatalf("billed %d: Tokens(%d) = %d, live %d", billed, n, g, w)
			}
		}
	}
}

// A capped ratio keeps its estimate meaning in RemovalSaving, and the Factor stays ignored with a Ratio.
func TestRemovalWithACappedRatioIsEstimated(t *testing.T) {
	capped := NewRatio(100, NewBody(nil, nil, [][]byte{imageBlock(400000), text("t", 40)}))
	if !capped.Capped() {
		t.Fatal("fixture is not capped")
	}
	in := RemovalInput{Model: "m", At: t0, Tier: TierCacheRead, Prompt: 10_000, RemovedBytes: 40, Ratio: &capped, Factor: 5}
	if got := RemovalSaving(in, linePricer{}); !got.Estimated {
		t.Fatal("a capped ratio gave Estimated false")
	}
	live := RatioFromText(3000, 6000, false)
	in.Ratio = &live
	a := RemovalSaving(in, linePricer{})
	in.Factor = 1
	if b := RemovalSaving(in, linePricer{}); a.Estimated || a != b {
		t.Fatalf("Factor changed a figure with a Ratio, or an uncapped ratio is Estimated: %+v vs %+v", a, b)
	}
}

// A hand-built body with no text bytes, billed at or below the media price, passes check 9 with 0 tokens over 0
// bytes. That ratio is out of range, and Compute sets no Result.Ratio for it.
func TestNoTextRatioIsNotExported(t *testing.T) {
	b := Body{Start: [3]int{0, 0, 0}, Pos: []PosHash{{Hash: 1, Bytes: 2500, Media: 2500}}}
	if r := NewRatio(10, b); r.InRange() {
		t.Fatalf("a ratio of 0 tokens over 0 bytes is in range: %+v", r)
	}
	res := req{at: t0, client: b, fwd: b, facts: exact5m, usage: Usage{Input: 10}}.run(NewTrackers())
	if res.Ratio != nil {
		t.Fatalf("Result.Ratio = %+v for a body with no text, want nil (checks %+v)", res.Ratio, res.Checks)
	}
}

// Counts above the bound are not a ratio: no overflow, no tokens, and a capped (estimate) mark.
func TestRatioFromTextRejectsHugeCounts(t *testing.T) {
	maxInt := int(^uint(0) >> 1) // 2^63-1 on 64-bit, 2^31-1 on 32-bit
	for _, c := range [][2]int{{maxInt, 1000}, {1000, maxInt}, {-1, 5}, {5, -1}} {
		r := RatioFromText(c[0], c[1], false)
		if got := r.Tokens(1000); got != 0 || !r.Capped() {
			t.Errorf("RatioFromText(%d, %d): Tokens = %d capped = %v, want 0 and capped", c[0], c[1], got, r.Capped())
		}
	}
	r := RatioFromText(maxRatioCount, maxRatioCount, false)
	if got := r.Tokens(maxRatioCount); got != maxRatioCount {
		t.Errorf("at the bound Tokens = %d, want %d", got, maxRatioCount)
	}
	if maxInt > maxRatioCount {
		above := maxRatioCount
		above++
		if got := r.Tokens(above); got != 0 {
			t.Errorf("above the bound Tokens = %d, want 0", got)
		}
	}
}

// NewRatio with a bill above the bound is out of range too: converts 0, capped, no overflow.
func TestNewRatioClampsAHugeBill(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	if maxInt <= maxRatioCount {
		t.Skip("a 32-bit int cannot hold a count above the bound")
	}
	r := NewRatio(maxInt/2, mk(nil, nil, msgs(2, 100)))
	if !r.Capped() || r.Tokens(maxRatioCount) != 0 || r.InRange() {
		t.Fatalf("huge bill: capped=%v tokens=%d inrange=%v", r.Capped(), r.Tokens(maxRatioCount), r.InRange())
	}
}

// A request whose media price is above the bill gets a capped Result.Ratio, and a figure that uses it is estimated.
func TestComputeSetsACappedResultRatio(t *testing.T) {
	body := [][]byte{imageBlock(400000), text("t", 400)}
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(1),
		SizeBefore: Size{Bytes: 400}, SizeAfter: Size{Bytes: 400}}
	res, _ := priceOne(t, body, body, op, Usage{Input: 200})
	if res.Ratio == nil || !res.Ratio.Capped() {
		t.Fatalf("Result.Ratio = %+v, want a capped ratio (checks %+v)", res.Ratio, res.Checks)
	}
	got := RemovalSaving(RemovalInput{Model: "m", At: t0, Tier: TierInput, Prompt: 200, RemovedBytes: 40, Ratio: res.Ratio}, linePricer{})
	if !got.Estimated {
		t.Fatal("a figure with the capped Result.Ratio is not Estimated")
	}
}
