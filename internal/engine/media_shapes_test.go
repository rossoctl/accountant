package engine

import (
	"strings"
	"testing"
)

// Issue 42: media shapes that were not detected, and a big image that skewed or dropped pricing.

// Every shape holds a 100,000-byte base64 payload. The expected Media is the payload length: 100,000.
func TestMediaShapesAreDetected(t *testing.T) {
	d := strings.Repeat("A", 100000)
	shapes := []struct {
		name string
		json string
		want int
	}{
		{"anthropic image", `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + d + `"}}`, 100000},
		{"anthropic image in tool_result", `{"type":"tool_result","content":[{"type":"image","source":{"type":"base64","data":"` + d + `"}}]}`, 100000},
		{"openai chat image_url", `{"type":"image_url","image_url":{"url":"data:image/png;base64,` + d + `"}}`, 100000},
		{"openai responses input_image", `{"type":"input_image","image_url":"data:image/png;base64,` + d + `"}`, 100000},
		{"openai input_audio", `{"type":"input_audio","input_audio":{"data":"` + d + `","format":"wav"}}`, 100000},
		{"gemini inlineData", `{"inlineData":{"mimeType":"image/png","data":"` + d + `"}}`, 100000},
		{"gemini inline_data", `{"inline_data":{"mime_type":"image/png","data":"` + d + `"}}`, 100000},
		{"bedrock converse image", `{"image":{"format":"png","source":{"bytes":"` + d + `"}}}`, 100000},
		{"bedrock converse document", `{"document":{"format":"pdf","name":"a","source":{"bytes":"` + d + `"}}}`, 100000},
		{"openai file_data pdf", `{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,` + d + `"}}`, 100000},
		{"uppercase data:IMAGE/", `{"type":"image_url","image_url":{"url":"data:IMAGE/png;base64,` + d + `"}}`, 100000},
		{"uppercase marker", `{"type":"image_url","image_url":{"url":"DATA:Image/PNG;BASE64,` + d + `"}}`, 100000},
		{"anthropic image url (no bytes)", `{"type":"image","source":{"type":"url","url":"https://x/y.png"}}`, 0},
		{"text that quotes a data field", `{"type":"text","data":"` + d + `"}`, 0},
		{"data url of another type", `{"url":"data:text/html;base64,` + d + `"}`, 0},
		{"bytes outside a source", `{"image":{"format":"png","bytes":"` + d + `"}}`, 0},
	}
	for _, s := range shapes {
		if got := NewBody(nil, nil, [][]byte{[]byte(s.json)}).Pos[0].Media; int(got) != s.want {
			t.Errorf("%s: Media = %d, want %d", s.name, got, s.want)
		}
	}
}

// one request with a breakpoint on msg(2); the bill is all CacheWrite.
func priceBP(client, fwd [][]byte, op Operation, billed int) Result {
	ts := NewTrackers()
	r := ts.BeginRequest(RequestMeta{RequestID: "r", Session: "s", At: t0}, NewBody(nil, nil, client), bps5(msg(2)))
	r.Add(op)
	r.EndRequest(Thread{ID: "main"}, NewBody(nil, nil, fwd), bps5(msg(2)), exact5m)
	return r.Compute(Usage{CacheWrite: billed})
}

// A 2 MB image beside text. The library prices the image at 1 token for 250 bytes: 8,000 tokens.
// Anthropic bills about 1,600. The bill (101,600) is above the library price, so the price is not
// cut and the text ratio still carries the 6,400 tokens of difference. This test pins that known limit
// (docs/design.md §4.2 rule 7) and the way out: the host's own Size.Tokens is exact.
//
// Hand computation. Forwarded text = 360,000 + 78 (the image wrapper) + 40,000 = 400,078 bytes.
// Text tokens = 101,600 - 8,000 = 93,600. Saving = 40,000 x 93,600 / 400,078 = 9,358.2 -> 9,358.
func TestLargeImageSkewIsKnownAndHostTokensAreExact(t *testing.T) {
	img := imageBlock(2_000_000)
	client := [][]byte{text("m0", 360000), img, text("m2", 80000)}
	fwd := [][]byte{text("m0", 360000), img, text("m2", 40000)}
	op := func(before, after Size) Operation {
		return Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "c", At: msg(2), SizeBefore: before, SizeAfter: after}
	}
	res := priceBP(client, fwd, op(Size{Bytes: 80000}, Size{Bytes: 40000}), 101600)
	noChecks(t, res)
	p := findOp(t, res, "o1")
	if got := tierSum(p.Saved); got != 9358 || !p.Estimated {
		t.Fatalf("saved = %d estimated = %v, want 9358 estimated (6.4%% under the true 10,000)", got, p.Estimated)
	}
	// The host counted 20,000 tokens before and 10,000 after: the library uses them exactly.
	res = priceBP(client, fwd, op(Size{Bytes: 80000, Tokens: 20000}, Size{Bytes: 40000, Tokens: 10000}), 101600)
	p = findOp(t, res, "o1")
	if got := tierSum(p.Saved); got != 10000 || p.Estimated {
		t.Fatalf("saved = %d estimated = %v, want the host's 10,000, exact", got, p.Estimated)
	}
}

// A 2 MB image in a small conversation. The library price of the image is 8,000 tokens, and the bill
// is 2,350 (text 750 + image 1,600). Before the fix this dropped the whole request to zero.
//
// Hand computation. Forwarded text = 2,000 + 78 + 1,000 = 3,078 bytes. The floor of the text is
// ceil(0.05 x 3,078) = 154 tokens. The image gets 2,350 - 154 = 2,196 tokens. The text ratio is
// 154 / 3,078. The operation cuts message 2 from 8,000 to 1,000 bytes, with no media in it:
// saving = 7,000 x 154 / 3,078 = 350.2 -> 350. Conservation: client text 10,078 x 154 / 3,078 = 504,
// plus 2,196 = 2,700; 2,700 - 2,350 = 350.
func TestBigImageInSmallConversationDoesNotDropTheRequest(t *testing.T) {
	img := imageBlock(2_000_000)
	client := [][]byte{text("m0", 2000), img, text("m2", 8000)}
	fwd := [][]byte{text("m0", 2000), img, text("m2", 1000)}
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "c", At: msg(2),
		SizeBefore: Size{Bytes: 8000}, SizeAfter: Size{Bytes: 1000}}
	res := priceBP(client, fwd, op, 2350)
	if hasUnpriced(res, ReasonRatioRange) || hasUnpriced(res, ReasonMediaTokens) {
		t.Fatalf("Unpriced = %+v, the text operation must be priced", res.Unpriced)
	}
	if got := checkNames(res); len(got) != 1 || got[0] != "media_tokens" {
		t.Fatalf("Checks = %v, want only media_tokens", got)
	}
	p := findOp(t, res, "o1")
	if got := tierSum(p.Saved); got != 350 || !p.Estimated {
		t.Fatalf("saved = %d estimated = %v, want 350 estimated", got, p.Estimated)
	}
}

// An operation that covers the image itself is unpriced when the bill is below the media price,
// because the library cannot say how the bill splits. Only that operation is unpriced.
func TestOperationOverMediaIsUnpricedWhenBillIsBelowMediaPrice(t *testing.T) {
	img := imageBlock(2_000_000)
	client := [][]byte{text("m0", 2000), img, text("m2", 8000)}
	fwd := [][]byte{text("m0", 2000), []byte("[image removed]"), text("m2", 8000)}
	strip := Operation{ID: "strip", Kind: KindRewrite, Operator: "strip", ChangeID: "s", At: msg(1),
		SizeBefore: Size{Bytes: len(img)}, SizeAfter: Size{Bytes: 15}}
	cut := Operation{ID: "cut", Kind: KindRewrite, Operator: "mask", ChangeID: "c", At: msg(2),
		SizeBefore: Size{Bytes: 8000}, SizeAfter: Size{Bytes: 8000}}
	ts := NewTrackers()
	r := ts.BeginRequest(RequestMeta{RequestID: "r", Session: "s", At: t0}, NewBody(nil, nil, client), bps5(msg(2)))
	r.Add(strip)
	r.Add(cut)
	r.EndRequest(Thread{ID: "main"}, NewBody(nil, nil, fwd), bps5(msg(2)), exact5m)
	// The real bill: text 2,000 + 15 + 8,000 bytes at 0.25 = 2,503 tokens. The image is gone, so the
	// forwarded body has no media and nothing is capped: the strip is priced from the CLIENT media.
	res := r.Compute(Usage{CacheWrite: 2503})
	if hasUnpriced(res, ReasonMediaTokens) || hasUnpriced(res, ReasonRatioRange) {
		t.Fatalf("Unpriced = %+v: the forwarded body holds no media, nothing is capped", res.Unpriced)
	}
	// Now the image stays in the forwarded body and a second operation edits it: unpriced.
	fwd2 := [][]byte{text("m0", 2000), img, text("m2", 8000)}
	ts = NewTrackers()
	r = ts.BeginRequest(RequestMeta{RequestID: "r", Session: "s", At: t0}, NewBody(nil, nil, client), bps5(msg(2)))
	touch := Operation{ID: "touch", Kind: KindRewrite, Operator: "x", ChangeID: "t", At: msg(1),
		SizeBefore: Size{Bytes: len(img)}, SizeAfter: Size{Bytes: len(img)}}
	r.Add(touch)
	r.Add(cut)
	r.EndRequest(Thread{ID: "main"}, NewBody(nil, nil, fwd2), bps5(msg(2)), exact5m)
	res = r.Compute(Usage{CacheWrite: 2350})
	if !hasUnpricedOp(res, "touch", ReasonMediaTokens) {
		t.Fatalf("Unpriced = %+v, want touch unpriced with media_tokens", res.Unpriced)
	}
	if hasUnpricedOp(res, "cut", ReasonMediaTokens) || hasUnpriced(res, ReasonRatioRange) {
		t.Fatalf("Unpriced = %+v: only the media operation is unpriced", res.Unpriced)
	}
}
