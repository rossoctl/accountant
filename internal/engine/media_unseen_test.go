package engine

import (
	"strings"
	"testing"
)

// accountant#70, case D. A request can bill many more tokens than its bytes explain, because some media
// costs tokens that the bytes do not show: an image or document by url or file id (no bytes at all), and a
// PDF (billed by page). The ratio check then failed as `ratio_range`, which says the usage does not fit the
// body. With such media in the body it fails as `media_unseen`. Nothing is priced in either case.

func TestUnseenBlocksByShape(t *testing.T) {
	d := strings.Repeat("A", 20000)
	for _, c := range []struct {
		name string
		json string
		want int32
	}{
		{"anthropic image by url", `{"type":"image","source":{"type":"url","url":"https://x/y.png"}}`, 1},
		{"anthropic image by file id", `{"type":"image","source":{"type":"file","file_id":"file_1"}}`, 1},
		{"anthropic document by url", `{"type":"document","source":{"type":"url","url":"https://x/y.pdf"}}`, 1},
		{"anthropic document by file id", `{"type":"document","source":{"type":"file","file_id":"file_1"}}`, 1},
		{"anthropic pdf page, base64", `{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + d + `"}}`, 1},
		{"anthropic small jpeg, base64", `{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"` + d + `"}}`, 0},
		{"anthropic text document, base64", `{"type":"document","source":{"type":"base64","media_type":"text/plain","data":"` + d + `"}}`, 0},
		{"image by url inside a tool_result", `{"type":"tool_result","tool_use_id":"a","content":[{"type":"image","source":{"type":"url","url":"https://x/y.png"}}]}`, 1},
		{"two blocks", `{"role":"user","content":[` +
			`{"type":"image","source":{"type":"url","url":"https://x/a.png"}},{"type":"image","source":{"type":"file","file_id":"f"}}]}`, 2},
		{"openai chat image_url by url", `{"type":"image_url","image_url":{"url":"https://x/y.png"}}`, 1},
		{"openai chat image_url data URL", `{"type":"image_url","image_url":{"url":"data:image/png;base64,` + d + `"}}`, 0},
		{"openai chat file by id", `{"type":"file","file":{"file_id":"file_1"}}`, 1},
		{"openai chat file pdf data URL", `{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,` + d + `"}}`, 1},
		{"openai responses input_image by url", `{"type":"input_image","image_url":"https://x/y.png"}`, 1},
		{"openai responses input_image by file id", `{"type":"input_image","file_id":"file_1"}`, 1},
		{"openai responses input_image data URL", `{"type":"input_image","image_url":"DATA:image/png;base64,` + d + `"}`, 0},
		{"openai responses input_file by id", `{"type":"input_file","file_id":"file_1"}`, 1},
		{"openai responses input_file by url", `{"type":"input_file","file_url":"https://x/y.pdf"}`, 1},
		{"plain text", `{"type":"text","text":"see https://x/y.png and the pdf"}`, 0},
		{"text that quotes a block", `{"type":"text","text":"{\"type\":\"image\",\"source\":{\"type\":\"url\",\"url\":\"u\"}}"}`, 0},
		{"tool output string with the words", `{"type":"tool_result","tool_use_id":"a","content":"\"image\" \"url\" \"file_id\" pdf"}`, 0},
		{"not json", `{"type":"image","source":{"type":"url"`, 0},
	} {
		if got := NewBody(nil, nil, [][]byte{[]byte(c.json)}).Pos[0].Unseen; got != c.want {
			t.Errorf("%s: Unseen = %d, want %d", c.name, got, c.want)
		}
	}
}

// The count changes no hash and no media bytes: a base64 PDF is still media, and also unseen.
func TestUnseenDoesNotChangeTheHashOrTheMediaBytes(t *testing.T) {
	pdf := `{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + strings.Repeat("A", 100000) + `"}}`
	b := NewBody(nil, nil, [][]byte{[]byte(pdf)})
	if b.Pos[0].Media != 100000 || b.Pos[0].Unseen != 1 {
		t.Fatalf("Media = %d, Unseen = %d, want 100000 and 1", b.Pos[0].Media, b.Pos[0].Unseen)
	}
	b.Pos[0].Unseen = 0
	same := b
	same.Pos = append([]PosHash(nil), b.Pos...)
	if b.Pos[0].Hash != NewBody(nil, nil, [][]byte{[]byte(pdf)}).Pos[0].Hash {
		t.Fatal("the hash depends on the count")
	}
}

// computeUnseen prices one request with no operation over a body of one declaration, 3 system blocks, and
// one message of 617 bytes of text plus the given extra blocks (the shape of the case D table).
func computeUnseen(t *testing.T, extra []string, billed int, ops ...Operation) Result {
	t.Helper()
	text := `{"type":"text","text":"` + strings.Repeat("x", 617-26) + `"}`
	decl := `{"name":"t","description":"` + strings.Repeat("d", 250) + `","input_schema":{"type":"object"}}`
	sys := []byte(`{"type":"text","text":"` + strings.Repeat("s", 100) + `"}`)
	content := `{"role":"user","content":[` + strings.Join(append([]string{text}, extra...), ",") + `]}`
	body := NewBody([][]byte{[]byte(decl)}, [][]byte{sys, sys, sys}, [][]byte{[]byte(content)})
	ts := NewTrackers()
	r := ts.BeginRequest(RequestMeta{RequestID: "r", Session: "s", At: t0}, body, nil)
	for _, op := range ops {
		r.Add(op)
	}
	r.EndRequest(Thread{ID: "main"}, body, nil, exact5m)
	return r.Compute(Usage{Input: billed})
}

func TestTooManyTokensWithUnseenMediaFailsAsMediaUnseen(t *testing.T) {
	url := `{"type":"image","source":{"type":"url","url":"https://x/y.png"}}`
	pdf := `{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + strings.Repeat("A", 100000) + `"}}`
	for _, c := range []struct {
		name   string
		extra  []string
		billed int
		want   string
	}{
		{"8 images by url", []string{url, url, url, url, url, url, url, url}, 13500, "media_unseen"},
		{"1 pdf page", []string{pdf}, 3700, "media_unseen"},
		{"no media, the same bill", nil, 13500, "ratio_range"},
		{"one image by url, a bill that fits", []string{url}, 2300, ""},
	} {
		res := computeUnseen(t, c.extra, c.billed)
		got := checkNames(res)
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("%s: Checks = %v, want none", c.name, got)
			}
			continue
		}
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: Checks = %v, want [%s]", c.name, got, c.want)
		}
	}
	res := computeUnseen(t, []string{url, url}, 6000)
	if len(res.Checks) != 1 || !strings.Contains(res.Checks[0].Detail, "unseen media blocks=2") {
		t.Fatalf("Checks = %+v, want the count of unseen blocks in the detail", res.Checks)
	}
}

// Too FEW tokens cannot come from media, so the reason stays ratio_range.
func TestTooFewTokensWithUnseenMediaStaysRatioRange(t *testing.T) {
	url := `{"type":"image","source":{"type":"url","url":"https://x/y.png"}}`
	res := computeUnseen(t, []string{url}, 10)
	if got := checkNames(res); len(got) != 1 || got[0] != "ratio_range" {
		t.Fatalf("Checks = %v, want [ratio_range]", got)
	}
}

// The name changes, the pricing does not: nothing is priced, and each operation is unpriced with the new reason.
func TestMediaUnseenPricesNothing(t *testing.T) {
	url := `{"type":"image","source":{"type":"url","url":"https://x/y.png"}}`
	op := rewrite("o1", "cmdfilter", "cg:cmdfilter:a", msg(0), 600, 300)
	res := computeUnseen(t, []string{url, url, url, url, url, url, url, url}, 13500, op)
	if !hasUnpriced(res, ReasonMediaUnseen) {
		t.Fatalf("Unpriced = %+v, want media_unseen", res.Unpriced)
	}
	if p := findOp(t, res, "o1"); tierSum(p.Saved) != 0 || tierSum(p.Caused) != 0 {
		t.Fatalf("Saved = %+v Caused = %+v, want none", p.Saved, p.Caused)
	}
	if (CheckFailure{Check: CheckMediaUnseen}).Fatal() {
		t.Fatal("media_unseen is informational, like ratio_range")
	}
}

// A host that builds its Body itself and does not set Unseen keeps the old name.
func TestAHostBodyWithNoUnseenKeepsRatioRange(t *testing.T) {
	b := NewBody(nil, nil, [][]byte{[]byte(`{"type":"text","text":"` + strings.Repeat("x", 600) + `"}`)})
	ts := NewTrackers()
	r := ts.BeginRequest(RequestMeta{RequestID: "r", Session: "s", At: t0}, b, nil)
	r.EndRequest(Thread{ID: "main"}, b, nil, exact5m)
	res := r.Compute(Usage{Input: 13500})
	if got := checkNames(res); len(got) != 1 || got[0] != "ratio_range" {
		t.Fatalf("Checks = %v, want [ratio_range]", got)
	}
}
