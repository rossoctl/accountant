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
		{"openai input_file", `{"type":"input_file","file_data":"data:application/pdf;base64,AAAAAA"}`, 6},
		{"html data url is text", `{"type":"tool_result","url":"data:text/html;base64,AAAAAAAA"}`, 0},
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
	res, p := priceOne(t, client, fwd, op, Usage{Input: 14000})
	noChecks(t, res)
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
	noChecks(t, res)
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

// An unmarked cut of an image message: the body holds the media, so the cut is priced at the media
// price. Before the fix the whole range was priced at the text ratio (100,016 tokens).
func TestUnmarkedCutOfAnImageMessageUsesTheBodysMedia(t *testing.T) {
	img := imageBlock(400000)
	client := [][]byte{img, text("t", 4000)}
	fwd := [][]byte{nil, text("t", 4000)}
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(0),
		SizeBefore: Size{Bytes: len(img)}}
	res, p := priceOne(t, client, fwd, op, Usage{Input: 1000})
	want := 1600 + wrapper/4
	if got := tierSum(p.Saved); got < want-2 || got > want+2 {
		t.Fatalf("saved = %d, want about %d", got, want)
	}
	noChecks(t, res)
}

// A host count far outside the bytes is a host bug: it is not used, and a check names it.
func TestImplausibleHostTokensFailACheckAndUseBytes(t *testing.T) {
	client := [][]byte{text("j", 10000), text("p", 40000)}
	fwd := [][]byte{nil, text("p", 40000)}
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(0),
		SizeBefore: Size{Bytes: 10000, Tokens: 500000}}
	res, p := priceOne(t, client, fwd, op, Usage{Input: 12500})
	found := false
	for _, c := range res.Checks {
		found = found || c.Check == "size_tokens"
	}
	if !found {
		t.Fatalf("Checks = %+v, want size_tokens", res.Checks)
	}
	if got := tierSum(p.Saved); got != 3125 || !p.Estimated {
		t.Fatalf("saved = %d estimated = %v, want 3125 from the bytes, estimated", got, p.Estimated)
	}
}

func TestNegativeHostMediaIsClamped(t *testing.T) {
	b := Body{Pos: []PosHash{{Hash: 1, Bytes: 100, Media: -5000}}}
	if got := b.cumMedia(0); got != 0 {
		t.Fatalf("cumMedia = %d, want 0", got)
	}
}

func TestPersistKeepsMedia(t *testing.T) {
	in := NewBody(nil, nil, [][]byte{imageBlock(1000), []byte("x")})
	out, err := fromWireBody(toWireBody(in))
	if err != nil {
		t.Fatal(err)
	}
	if out.Pos[0].Media != 1000 || out.Pos[1].Media != 0 {
		t.Fatalf("Media after a round trip = %d, %d, want 1000, 0", out.Pos[0].Media, out.Pos[1].Media)
	}
}

// mixed is one message with a text part of n bytes and a 400,000-byte image.
func mixed(n int) []byte {
	return []byte(`[{"type":"text","text":"` + strings.Repeat("t", n) + `"},` + string(imageBlock(400000)) + `]`)
}

// An unmarked after-size that adds an image: the forwarded body holds the media. Before the fix
// the 400,067 bytes priced at the text ratio (99,942 tokens).
func TestUnmarkedAfterSizeThatAddsAnImageUsesTheForwardedMedia(t *testing.T) {
	img := imageBlock(400000)
	client := [][]byte{text("t", 4000)}
	fwd := [][]byte{img, text("t", 4000)}
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "inject", ChangeID: "in", At: msg(0),
		SizeAfter:     Size{Bytes: len(img)},
		MessageSplice: &MessageSplice{At: 0, Removed: 0, Inserted: 1}}
	res, p := priceOne(t, client, fwd, op, Usage{Input: 2616})
	if got := tierSum(p.Caused); got < 1614 || got > 1620 {
		t.Fatalf("caused = %d, want about 1618", got)
	}
	noChecks(t, res)
}

// A rewrite that keeps the image and trims the text around it saves the text only.
func TestUnmarkedAfterSizeThatKeepsTheImageSavesTheTextOnly(t *testing.T) {
	before, after := mixed(1200), mixed(200)
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "trim", ChangeID: "tr", At: msg(0),
		SizeBefore: Size{Bytes: len(before)}, SizeAfter: Size{Bytes: len(after)}}
	billed := 1600 + (len(after)-400000)/4
	res, p := priceOne(t, [][]byte{before}, [][]byte{after}, op, Usage{Input: billed})
	if got := tierSum(p.Saved); got < 248 || got > 252 || tierSum(p.Caused) != 0 {
		t.Fatalf("saved = %d caused = %d, want about 250 and 0", got, tierSum(p.Caused))
	}
	noChecks(t, res)
}

// A correct host count on an after-size over media must pass the size_tokens check.
func TestHostTokensOnAnAfterSizeOverMediaAreNotRejected(t *testing.T) {
	before, after := mixed(1200), mixed(200)
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "trim", ChangeID: "tr", At: msg(0),
		SizeBefore: Size{Bytes: len(before), Tokens: 1600 + 300},
		SizeAfter:  Size{Bytes: len(after), Tokens: 1600 + 50}}
	billed := 1600 + (len(after)-400000)/4
	res, p := priceOne(t, [][]byte{before}, [][]byte{after}, op, Usage{Input: billed})
	for _, c := range res.Checks {
		if c.Check == "size_tokens" {
			t.Fatalf("size_tokens failed: %+v", c)
		}
	}
	if got := tierSum(p.Saved); got != 250 || p.Estimated {
		t.Fatalf("saved = %d estimated = %v, want the host's 250, exact", got, p.Estimated)
	}
}

// run1 runs one request and returns the result, without looking for a priced operation.
func run1(client, fwd [][]byte, op Operation, usage Usage) Result {
	ts := NewTrackers()
	r := ts.BeginRequest(RequestMeta{RequestID: "r", Session: "s", At: t0}, NewBody(nil, nil, client), nil)
	r.Add(op)
	r.EndRequest(Thread{ID: "main"}, NewBody(nil, nil, fwd), nil, exact5m)
	return r.Compute(usage)
}

func checkNames(res Result) []string {
	var out []string
	for _, c := range res.Checks {
		out = append(out, c.Check)
	}
	return out
}

func noopOp() Operation {
	return Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(1),
		SizeBefore: Size{Bytes: 40}, SizeAfter: Size{Bytes: 40}}
}

// A bill below the media price fails media_tokens and ratio_range, and prices nothing.
func TestBillBelowTheMediaPricePricesNothing(t *testing.T) {
	body := [][]byte{imageBlock(400000), text("t", 40)} // 1600 media tokens
	res := run1(body, body, noopOp(), Usage{Input: 100})
	if !hasUnpriced(res, ReasonRatioRange) {
		t.Fatalf("Unpriced = %+v, want ratio_range", res.Unpriced)
	}
	got := checkNames(res)
	has := func(n string) bool {
		for _, g := range got {
			if g == n {
				return true
			}
		}
		return false
	}
	if !has("media_tokens") || !has("ratio_range") {
		t.Fatalf("Checks = %v, want media_tokens and ratio_range", got)
	}
}

// A bill equal to the media price leaves no text token: the text ratio is out of range (a body with
// text bytes and no text token), but media_tokens does not fail.
func TestBillEqualToTheMediaPriceDoesNotFailMediaTokens(t *testing.T) {
	body := [][]byte{imageBlock(400000), text("t", 40)}
	media := 400000 / mediaBytesPerToken
	res := run1(body, body, noopOp(), Usage{Input: media})
	for _, n := range checkNames(res) {
		if n == "media_tokens" {
			t.Fatalf("media_tokens failed at a bill equal to the media price: %v", checkNames(res))
		}
	}
	// With 4 text tokens for 40 text bytes + wrapper the request prices.
	res = run1(body, body, noopOp(), Usage{Input: media + (40+wrapper)/4})
	noChecks(t, res)
}

// A media-heavy request is priced at the text ratio (0.25), not refused by the raw formula.
func TestMediaHeavyRequestIsPriced(t *testing.T) {
	img := imageBlock(400000)
	client := [][]byte{text("a", 40000), img}
	fwd := [][]byte{text("a", 30000), img}
	op := Operation{ID: "o1", Kind: KindRewrite, Operator: "mask", ChangeID: "mk", At: msg(0),
		SizeBefore: Size{Bytes: 40000}, SizeAfter: Size{Bytes: 30000}}
	billed := 1600 + (30000+wrapper)/4
	res := run1(client, fwd, op, Usage{Input: billed})
	noChecks(t, res)
	if hasUnpriced(res, ReasonRatioRange) {
		t.Fatalf("Unpriced = %+v, the request must be priced", res.Unpriced)
	}
	if p := findOp(t, res, "o1"); tierSum(p.Saved) != 2500 {
		t.Fatalf("saved = %d, want 2500", tierSum(p.Saved))
	}
}

// A body whose positions are all media is not an empty body: it prices when the bill covers the
// media.
func TestAllMediaBodyIsNotRefused(t *testing.T) {
	b := Body{Pos: []PosHash{{Hash: 1, Bytes: 2500, Media: 2500}}}
	c := &calcState{fwd: b, usage: Usage{Input: 10}, ratio: newRatio(10, b)}
	if c.badRatio() {
		t.Fatal("badRatio refused a body of media only that the bill covers")
	}
	c = &calcState{fwd: b, usage: Usage{Input: 9}, ratio: newRatio(9, b)}
	if !c.badRatio() {
		t.Fatal("badRatio accepted a bill below the media price")
	}
}
