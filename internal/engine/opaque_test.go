package engine

import (
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
		{"escaped key", `{"type":"thinking","signature":"` + s + `"}`, 25000},
		{"escaped type", `{"type":"thinking","signature":"` + s + `"}`, 25000},
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
	if r.capped || r.opaqueOf(25000) != 45 || r.tokens != 5 {
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
func TestRemovedSignatureCostsNoToken(t *testing.T) {
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

func TestSizeWithTooManyNonTextBytesIsInvalid(t *testing.T) {
	if !(Size{Bytes: 10, MediaBytes: 6, OpaqueBytes: 5}).invalid() || !(Size{Bytes: 10, OpaqueBytes: -1}).invalid() {
		t.Fatal("media + opaque above Bytes, or a negative opaque count, must be invalid")
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
