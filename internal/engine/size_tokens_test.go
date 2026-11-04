package engine

import (
	"strings"
	"testing"
)

// Issue 11: one bytes-per-token ratio misprices images and dense JSON.

// imageBlock is a message that holds a base64 image of n payload bytes.
func imageBlock(n int) []byte {
	return []byte(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` +
		strings.Repeat("A", n) + `"}}`)
}

// wrapper is the JSON text around the payload of imageBlock: text, 4 bytes a token in these tests.
var wrapper = len(imageBlock(0))

func text(id string, n int) []byte { return []byte(id + strings.Repeat(".", n-len(id))) }

// priceOne runs one request. The client body has the operation's range at its "before" size, and
// the forwarded body has it at its "after" size, so the sum and conservation checks hold.
func priceOne(t *testing.T, client, fwd [][]byte, op Operation, usage Usage) (Result, PricedOp) {
	t.Helper()
	ts := NewTrackers()
	r := ts.BeginRequest(RequestMeta{RequestID: "r", Session: "s", At: t0}, NewBody(nil, nil, client), nil)
	r.Add(op)
	r.EndRequest(Thread{ID: "main"}, NewBody(nil, nil, fwd), nil, exact5m)
	res := r.Compute(usage)
	return res, findOp(t, res, "o1")
}

func TestNewBodyCountsBase64MediaApart(t *testing.T) {
	body := NewBody(nil, nil, [][]byte{imageBlock(400000), []byte("plain text")})
	if got := body.Pos[0].Media; got != 400000 {
		t.Fatalf("Media = %d, want 400000", got)
	}
	if body.Pos[1].Media != 0 {
		t.Fatalf("text position Media = %d, want 0", body.Pos[1].Media)
	}
}

func TestMediaFormats(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"compact source block", `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}`, 4},
		{"pretty printed", "{\n\t\"type\": \"document\",\n\t\"source\": {\n\t\t\"type\": \"base64\",\n\t\t\"data\":\n\t\t\t\"AAAAAA\"\n\t}\n}", 6},
		{"source keys in other order", `{"source":{"data":"AAA","type":"base64"},"type":"image"}`, 3},
		{"block in a list", `[{"type":"text","text":"x"},{"type":"image","source":{"type":"base64","data":"AA"}}]`, 2},
		{"openai data url", `{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAAA"}}`, 5},
		{"plain url", `{"type":"image_url","image_url":{"url":"https://x/y.png"}}`, 0},
		{"tool result that quotes base64 and data", `{"encoding":"base64","data":"AAAAAAAAAA"}`, 0},
		{"schema enum", `{"enum":["base64","hex"],"properties":{"data":{"type":"string"}}}`, 0},
		{"text block that mentions both", `{"type":"text","text":"use \"base64\" then \"data\": \"AAAA\""}`, 0},
		{"url source", `{"type":"image","source":{"type":"url","data":"AAAA"}}`, 0},
		{"base64 source in a text block", `{"type":"text","source":{"type":"base64","data":"AAAA"}}`, 0},
		{"data outside an image block", `{"type":"tool_result","content":{"type":"base64","data":"AAAA"}}`, 0},
	}
	for _, c := range cases {
		if got := mediaBytes([]byte(c.in)); got != c.want {
			t.Errorf("%s: mediaBytes = %d, want %d", c.name, got, c.want)
		}
	}
}

// A 400,000-byte image (1600 tokens) and 3 text messages of 4000 bytes (1000 tokens each).
// A cut of 4000 text bytes saves 1000 tokens. Before the fix the library booked 54.
func TestImageDoesNotSkewTheTextRatio(t *testing.T) {
	img := imageBlock(400000)
	client := [][]byte{img, text("a", 4000), text("b", 4000), text("c", 4000)}
	fwd := [][]byte{img, text("a", 4000), text("b", 4000), nil}
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(3),
		SizeBefore: Size{Bytes: 4000}}
	billed := 1600 + (2*4000+wrapper)/4
	res, p := priceOne(t, client, fwd, op, Usage{Input: billed})
	if got := tierSum(p.Saved); got < 998 || got > 1000 { // the wrapper text rounds
		t.Fatalf("saved = %d tokens, want about 1000", got)
	}
	if !p.Estimated {
		t.Fatal("a bytes conversion must stay Estimated")
	}
	noChecks(t, res)
}

// 10,000 bytes of dense JSON are 5000 tokens. The request ratio would book 3500.
func TestHostTokensAreUsedAsTheyAre(t *testing.T) {
	client := [][]byte{text("j", 10000), text("p", 40000)}
	fwd := [][]byte{nil, text("p", 40000)}
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(0),
		SizeBefore: Size{Bytes: 10000, Tokens: 5000}}
	_, p := priceOne(t, client, fwd, op, Usage{Input: 14000})
	if got := tierSum(p.Saved); got != 5000 {
		t.Fatalf("saved = %d, want the host's 5000", got)
	}
	if p.Estimated {
		t.Fatal("host-counted tokens must not be Estimated")
	}
}

// Check 6 must pass on a correct report that uses Size.Tokens. Request 1 has an unknown phase and
// skips the check, so the gap shows only on request 2.
func TestConservationHoldsWithHostTokensOverTwoRequests(t *testing.T) {
	dense, prose, extra := text("j", 10000), text("p", 40000), text("m", 400)
	ts := NewTrackers()
	r1 := ts.BeginRequest(RequestMeta{RequestID: "r1", Session: "s", At: t0}, NewBody(nil, nil, [][]byte{dense, prose}), nil)
	r1.EndRequest(Thread{ID: "main"}, NewBody(nil, nil, [][]byte{dense, prose}), nil, exact5m)
	noChecks(t, r1.Compute(Usage{Input: 14000}))

	small := text("j", 100)
	r2 := ts.BeginRequest(RequestMeta{RequestID: "r2", Session: "s", At: t0.Add(1e9)},
		NewBody(nil, nil, [][]byte{dense, prose, extra}), nil)
	r2.Add(Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(0),
		SizeBefore: Size{Bytes: 10000, Tokens: 5000}, SizeAfter: Size{Bytes: 100, Tokens: 50}})
	r2.EndRequest(Thread{ID: "main"}, NewBody(nil, nil, [][]byte{small, prose, extra}), nil, exact5m)
	res := r2.Compute(Usage{Input: 9500})
	noChecks(t, res)
	if p := findOp(t, res, "o1"); tierSum(p.Saved) != 4950 || p.Estimated {
		t.Fatalf("saved = %d estimated = %v, want the host's 4950, exact", tierSum(p.Saved), p.Estimated)
	}
}

func TestMediaBytesOnASizeUseTheFixedMediaPrice(t *testing.T) {
	img := imageBlock(400000)
	client := [][]byte{img, text("t", 4000)}
	fwd := [][]byte{nil, text("t", 4000)}
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(0),
		SizeBefore: Size{Bytes: len(img), MediaBytes: 400000}}
	res, p := priceOne(t, client, fwd, op, Usage{Input: 1000})
	want := 1600 + wrapper/4 // the media at the fixed price, the JSON wrapper at the text ratio
	if got := tierSum(p.Saved); got < want-2 || got > want+2 {
		t.Fatalf("saved = %d, want about %d", got, want)
	}
	_ = res
}

func TestSizeWithMoreMediaThanBytesIsRefused(t *testing.T) {
	// The reason name is negative_size; it covers any malformed Size. Left as is.
	body := [][]byte{[]byte("abc")}
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(0),
		SizeBefore: Size{Bytes: 10, MediaBytes: 11}}
	res, _ := priceOne(t, body, body, op, Usage{Input: 3})
	if !hasUnpriced(res, ReasonNegativeSize) {
		t.Fatal("want negative_size")
	}
}

func TestHostBuiltMediaAboveBytesIsCapped(t *testing.T) {
	b := Body{Start: [3]int{0, 0, 0}, Pos: []PosHash{{Hash: 1, Bytes: 100, Media: 5000}}}
	if got := newRatio(50, b); got.bytes < 0 {
		t.Fatalf("ratio bytes = %d, must not be negative", got.bytes)
	}
}

func TestBilledBelowMediaPriceIsAFailedCheck(t *testing.T) {
	img := imageBlock(400000) // 1600 media tokens
	body := [][]byte{img, text("t", 40)}
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(1),
		SizeBefore: Size{Bytes: 40}, SizeAfter: Size{Bytes: 40}}
	res, _ := priceOne(t, body, body, op, Usage{Input: 100})
	for _, c := range res.Checks {
		if c.Check == "media_tokens" {
			return
		}
	}
	t.Fatalf("Checks = %+v, want media_tokens", res.Checks)
}
