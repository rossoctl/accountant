package engine

import (
	"strings"
	"testing"
)

// Issue 11: one bytes-per-token ratio misprices images and dense JSON.

// wrapper is the JSON text around the payload of imageBlock: text, 4 bytes a token here.
var wrapper = len(imageBlock(0))

// imageBlock is a message that holds a base64 image of n payload bytes.
func imageBlock(n int) []byte {
	return []byte(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` +
		strings.Repeat("A", n) + `"}}`)
}

// oneOp runs one request with one operation and returns the priced operation.
func oneOp(t *testing.T, body Body, op Operation, usage Usage) (Result, PricedOp) {
	t.Helper()
	ts := NewTrackers()
	r := ts.BeginRequest(RequestMeta{RequestID: "r", Session: "s", At: t0}, body, nil)
	r.Add(op)
	r.EndRequest(Thread{ID: "main"}, body, nil, exact5m)
	res := r.Compute(usage)
	return res, findOp(t, res, "o1")
}

func TestNewBodyCountsBase64MediaApart(t *testing.T) {
	img := imageBlock(400000)
	body := NewBody(nil, nil, [][]byte{img, []byte("plain text")})
	if got := body.Pos[0].Media; got != 400000 {
		t.Fatalf("Media = %d, want 400000", got)
	}
	if body.Pos[1].Media != 0 {
		t.Fatalf("text position Media = %d, want 0", body.Pos[1].Media)
	}
}

// A 400,000-byte image (1600 tokens) and 3 text messages of 4000 bytes (1000 tokens each).
// A cut of 4000 text bytes saves 1000 tokens. Before the fix the library booked 54.
func TestImageDoesNotSkewTheTextRatio(t *testing.T) {
	text := func(id string) []byte { return []byte(id + strings.Repeat(".", 4000-len(id))) }
	body := NewBody(nil, nil, [][]byte{imageBlock(400000), text("a"), text("b"), text("c")})
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(1),
		SizeBefore: Size{Bytes: 4000}}
	_, p := oneOp(t, body, op, Usage{Input: 1600 + (3*4000+wrapper)/4})
	if got := tierSum(p.Saved); got < 998 || got > 1000 { // the wrapper text rounds
		t.Fatalf("saved = %d tokens, want 1000", got)
	}
	if !p.Estimated {
		t.Fatal("a bytes conversion must stay Estimated")
	}
}

// 10,000 bytes of dense JSON are 5000 tokens. The request ratio would book 3500.
func TestHostTokensAreUsedAsTheyAre(t *testing.T) {
	json := []byte("j" + strings.Repeat("{", 9999))
	prose := []byte("p" + strings.Repeat(".", 39999))
	body := NewBody(nil, nil, [][]byte{json, prose})
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(0),
		SizeBefore: Size{Bytes: 10000, Tokens: 5000}}
	_, p := oneOp(t, body, op, Usage{Input: 15000})
	if got := tierSum(p.Saved); got != 5000 {
		t.Fatalf("saved = %d, want the host's 5000", got)
	}
	if p.Estimated {
		t.Fatal("host-counted tokens must not be Estimated")
	}
}

func TestMediaBytesOnASizeUseTheFixedMediaPrice(t *testing.T) {
	img := imageBlock(400000)
	body := NewBody(nil, nil, [][]byte{img, []byte("t" + strings.Repeat(".", 3999))})
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(0),
		SizeBefore: Size{Bytes: len(img), MediaBytes: 400000}}
	_, p := oneOp(t, body, op, Usage{Input: 1600 + (4000+wrapper)/4})
	want := 1600 + wrapper/4 // the media at the fixed price, the JSON wrapper at the text ratio
	if got := tierSum(p.Saved); got < want-2 || got > want {
		t.Fatalf("saved = %d, want about %d", got, want)
	}
}

func TestSizeWithMoreMediaThanBytesIsRefused(t *testing.T) {
	body := NewBody(nil, nil, [][]byte{[]byte("abc")})
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(0),
		SizeBefore: Size{Bytes: 10, MediaBytes: 11}}
	ts := NewTrackers()
	r := ts.BeginRequest(RequestMeta{RequestID: "r", Session: "s", At: t0}, body, nil)
	r.Add(op)
	r.EndRequest(Thread{ID: "main"}, body, nil, exact5m)
	if !hasUnpriced(r.Compute(Usage{Input: 3}), ReasonNegativeSize) {
		t.Fatal("want negative_size")
	}
}
