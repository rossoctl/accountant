package engine

import (
	"math"
	"strings"
	"testing"
)

// Issue 73: thinking signatures are opaque bytes. The provider bills them as few tokens, so they stay
// out of the text ratio and have a fixed price of 1 token for each 250 bytes.

func thinkingBlock(id string, sig int) []byte {
	return []byte(`{"role":"assistant","content":[{"type":"thinking","thinking":"` + id + `","signature":"` +
		strings.Repeat("S", sig) + `"},{"type":"text","text":"ok"}]}`)
}

func TestOpaqueShapesAreDetected(t *testing.T) {
	s := strings.Repeat("S", 25000)
	shapes := []struct {
		name string
		json string
		want int
	}{
		{"thinking signature", `{"type":"thinking","thinking":"hm","signature":"` + s + `"}`, 25000},
		{"thinking in a message", `{"role":"assistant","content":[{"type":"thinking","thinking":"hm","signature":"` + s + `"}]}`, 25000},
		{"redacted thinking", `{"type":"redacted_thinking","data":"` + s + `"}`, 25000},
		{"two blocks", `[{"type":"thinking","signature":"` + s + `"},{"type":"redacted_thinking","data":"` + s + `"}]`, 50000},
		{"escaped key", `{"type":"thinking","\u0073ignature":"` + s + `"}`, 25000},
		{"escaped type", `{"type":"redacted\u005fthinking","data":"` + s + `"}`, 25000},
		{"signature outside a thinking block", `{"type":"tool_use","input":{"signature":"` + s + `"}}`, 0},
		{"data outside a redacted block", `{"type":"text","data":"` + s + `"}`, 0},
		{"text that quotes the words", `{"type":"text","text":"\"signature\" and redacted_thinking"}`, 0},
		{"signature that is not a string", `{"type":"thinking","signature":12345}`, 0},
		{"cut-off JSON counts what was read", `[{"type":"thinking","signature":"` + s + `"},{"type":`, 25000},
	}
	for _, c := range shapes {
		p := []byte(c.json)
		if got := NewBody(nil, nil, [][]byte{p}).Pos[0].Opaque; int(got) != c.want {
			t.Errorf("%s: Opaque = %d, want %d", c.name, got, c.want)
		}
		if c.want > 0 && !mightHoldOpaque(p) {
			t.Errorf("%s: the pre-filter skips a position that holds opaque bytes", c.name)
		}
		if got := NewBody(nil, nil, [][]byte{p}).Pos[0].Media; got != 0 {
			t.Errorf("%s: Media = %d, want 0: a signature is not media", c.name, got)
		}
	}
	if mightHoldOpaque([]byte(`{"type":"text","text":"plain\n"}`)) {
		t.Errorf("the pre-filter must skip plain text")
	}
}

// The opaque price never takes the bill below the media price and the text floor, so it never caps
// the media and never fails a check. Here the bill is 50 tokens: the text floor is 5 (100 bytes x
// 0.05) and the signature price (100) is cut to the 45 tokens that are left.
func TestOpaquePriceIsCutToTheBill(t *testing.T) {
	b := Body{Start: [3]int{0, 0, 0}, Pos: []PosHash{{Hash: 1, Bytes: 25100, Opaque: 25000}}}
	r := newRatio(50, b)
	if r.capped || r.opaqueOf(nonText{opaque: 25000}) != 45 || r.tokens != 5 {
		t.Fatalf("ratio = %+v, want opaque 45 tokens, text 5 tokens, not capped", r)
	}
}

// A host-built Opaque outside 0..Bytes-Media is clamped, so media and opaque bytes never exceed Bytes.
func TestOpaqueIsClamped(t *testing.T) {
	b := Body{Start: [3]int{0, 0, 0}, Pos: []PosHash{{Hash: 1, Bytes: 100, Media: 60, Opaque: 70}, {Hash: 2, Bytes: 10, Opaque: -5}}}
	if got := b.cumOpaque(1); got != 40 {
		t.Fatalf("cumOpaque = %d, want 40 (100 - 60, then 0)", got)
	}
	if got := b.withPrefix().cumOpaque(1); got != 40 {
		t.Fatalf("prefix cumOpaque = %d, want 40", got)
	}
}

// The case of the issue. Message 1 holds a 25,000-byte signature, priced at 25,000 / 250 = 100 tokens.
// The bill is 0.4 tokens for each text byte, plus those 100. A cut of 10,000 text bytes books
// 10,000 x 0.4 = 4,000 tokens. Before the fix the ratio counted the signature as text and booked about
// 20% too little.
func TestSignatureBytesStayOutOfTheRatio(t *testing.T) {
	think := thinkingBlock("t", 25000)
	pad := 5 - (len(think)-25000)%5 // make the forwarded text a multiple of 5, so 0.4 is exact
	client := [][]byte{text("m0", 70000+pad), think, text("m2", 20000)}
	fwd := [][]byte{text("m0", 70000+pad), think, text("m2", 10000)}
	fb := NewBody(nil, nil, fwd)
	textBytes := fb.cumBytes(2) - fb.cumOpaque(2)
	if fb.cumOpaque(2) != 25000 || textBytes%5 != 0 {
		t.Fatalf("opaque = %d text = %d", fb.cumOpaque(2), textBytes)
	}
	billed := textBytes*2/5 + 100
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "c", At: msg(2),
		SizeBefore: Size{Bytes: 20000}, SizeAfter: Size{Bytes: 10000}}
	res := priceBP(client, fwd, op, billed)
	noChecks(t, res)
	if got := tierSum(findOp(t, res, "o1").Saved); got != 4000 {
		t.Fatalf("saved = %d, want 4000 (10,000 text bytes at 0.4)", got)
	}
}

// A removal of a whole thinking block saves its text at the ratio and its signature at the opaque
// price (25,000 / 250 = 100 tokens). A host that marks OpaqueBytes gets the same figure.
func TestRemovedSignatureCostsTheOpaquePrice(t *testing.T) {
	think := thinkingBlock("t", 25000)
	marker := []byte(`{"role":"assistant","content":[{"type":"text","text":"ok"}]}`)
	client := [][]byte{text("m0", 80000), think, text("m2", 20000)}
	fwd := [][]byte{text("m0", 80000), marker, text("m2", 20000)}
	fb := NewBody(nil, nil, fwd)
	billed := fb.cumBytes(2) * 2 / 5 // the forwarded body holds no signature
	r := newRatio(billed, fb)
	want := r.of(len(think)-25000) - r.of(len(marker)) + 100
	for _, before := range []Size{{Bytes: len(think)}, {Bytes: len(think), OpaqueBytes: 25000}} {
		op := Operation{ID: "o1", Kind: KindRewrite, Operator: "strip", ChangeID: "c", At: msg(1),
			SizeBefore: before, SizeAfter: Size{Bytes: len(marker)}}
		res := priceBP(client, fwd, op, billed)
		noChecks(t, res)
		if got := tierSum(findOp(t, res, "o1").Saved); got != want {
			t.Fatalf("before %+v: saved = %d, want %d (the signature at 100 tokens)", before, got, want)
		}
	}
}

// A size that marks only one kind of non-text bytes gets the other kind from the body. A host built
// before OpaqueBytes marks only its media; before this rule the signature in its range was priced at
// the text ratio, and the saving was about 40 times too high. Each way to mark the size books the
// same saving.
func TestSizeWithOneMarkTakesTheOtherFromTheBody(t *testing.T) {
	sig := `{"type":"thinking","thinking":"t","signature":"` + strings.Repeat("S", 25000) + `"}`
	mixed := []byte(`{"role":"user","content":[` + string(imageBlock(25000)) + `,` + sig + `]}`)
	marker := []byte(`{"role":"user","content":[{"type":"text","text":"[removed]"}]}`)
	client := [][]byte{text("m0", 80000), mixed, text("m2", 20000)}
	fwd := [][]byte{text("m0", 80000), marker, text("m2", 20000)}
	fb := NewBody(nil, nil, fwd)
	billed := fb.cumBytes(2) * 2 / 5 // the forwarded body holds no media and no signature
	sizes := []Size{
		{Bytes: len(mixed)},
		{Bytes: len(mixed), MediaBytes: 25000},
		{Bytes: len(mixed), OpaqueBytes: 25000},
		{Bytes: len(mixed), MediaBytes: 25000, OpaqueBytes: 25000},
	}
	want := -1
	for _, before := range sizes {
		op := Operation{ID: "o1", Kind: KindRewrite, Operator: "strip", ChangeID: "c", At: msg(1),
			SizeBefore: before, SizeAfter: Size{Bytes: len(marker)}}
		res := priceBP(client, fwd, op, billed)
		noChecks(t, res)
		got := tierSum(findOp(t, res, "o1").Saved)
		if want < 0 {
			want = got
		}
		if got != want {
			t.Fatalf("before %+v: saved = %d, want %d (the same as an unmarked size)", before, got, want)
		}
	}
	if want > 1000 {
		t.Fatalf("saved = %d: the media and the signature must not be priced at the text ratio", want)
	}
}

func TestSizeWithTooManyNonTextBytesIsInvalid(t *testing.T) {
	if !(Size{Bytes: 10, MediaBytes: 6, OpaqueBytes: 5}).invalid() || !(Size{Bytes: 10, OpaqueBytes: -1}).invalid() {
		t.Fatal("media + opaque above Bytes, or a negative opaque count, must be invalid")
	}
	if !(Size{Bytes: 10, MediaBytes: math.MaxInt, OpaqueBytes: math.MaxInt}).invalid() {
		t.Fatal("media + opaque that wraps past MaxInt must be invalid")
	}
	if (Size{Bytes: 10, MediaBytes: 5, OpaqueBytes: 5}).invalid() {
		t.Fatal("media + opaque equal to Bytes is valid")
	}
}

func TestOpaqueSurvivesSaveAndRestore(t *testing.T) {
	b := NewBody(nil, nil, [][]byte{thinkingBlock("t", 3000)})
	got, err := fromWireBody(toWireBody(b))
	if err != nil {
		t.Fatal(err)
	}
	if got.Pos[0].Opaque != 3000 || got.cumOpaque(0) != 3000 {
		t.Fatalf("restored Opaque = %d, cumOpaque = %d, want 3000", got.Pos[0].Opaque, got.cumOpaque(0))
	}
}

// A host count for the removed block (Size.OpaqueTokens) is not the unit of the size bound. Here the host
// counts 1,000 thinking tokens for a signature of 4,000 bytes. The body holds no count for it, so
// MessageTokens prices it at the D15 price (16 tokens). The messages are about 520 tokens, so the host
// count alone would be above them. The saving is booked, and the bound measures at the body's price.
func TestWithholdKeepsAHostCountedSignatureOverAnUncountedBody(t *testing.T) {
	think := thinkingBlock("t", 4000)
	marker := []byte(`{"role":"assistant","content":[{"type":"text","text":"ok"}]}`)
	client := [][]byte{text("m0", 2000), think}
	fwd := [][]byte{text("m0", 2000), marker}
	fb := NewBody(nil, nil, fwd)
	billed := fb.cumBytes(2) / 4
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "strip", ChangeID: "c", At: msg(1),
		SizeBefore: Size{Bytes: len(think), OpaqueBytes: 4000, OpaqueTokens: 1000}, SizeAfter: Size{Bytes: len(marker)}}
	res := priceBP(client, fwd, op, billed)
	p := findOp(t, res, "o1")
	if got := tierSum(p.Saved); got < 1000 {
		t.Fatalf("Saved = %d, want the host's 1,000 and the text", got)
	}
	if got := tierSum(p.SavedMessages); got > res.MessageTokens {
		t.Fatalf("SavedMessages = %d, MessageTokens = %d, want the saved size within the messages", got, res.MessageTokens)
	}
	if got := Withhold(res); got != "" {
		t.Errorf("Withhold = %q, want \"\": a host count is not a size fault", got)
	}
}
