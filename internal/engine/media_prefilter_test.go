package engine

import (
	"math/rand"
	"strings"
	"testing"
)

// Issue 39: mediaBytes skips the decode for a position that cannot hold media. The reference is
// mediaBytesExact, the decoder the pre-filter guards, which is the code from before the pre-filter.

func checkSame(t *testing.T, name, in string) {
	t.Helper()
	want := mediaBytesExact([]byte(in))
	if got := mediaBytes([]byte(in)); got != want {
		t.Errorf("%s: mediaBytes = %d, exact = %d, input %.200q", name, got, want, in)
	}
	if got := NewBody(nil, nil, [][]byte{[]byte(in)}).Pos[0].Media; int(got) != want {
		t.Errorf("%s: NewBody Media = %d, exact = %d", name, got, want)
	}
}

func TestMediaPrefilterMatchesExactTable(t *testing.T) {
	d := strings.Repeat("A", 1000)
	cases := map[string]string{
		"empty":                  ``,
		"whitespace only":        " \n\t",
		"invalid json":           `{"type":"image","source":{"type":"base64","data":"` + d,
		"garbage":                `}{][`,
		"no media":               `{"type":"text","text":"hello"}`,
		"tool_result text":       `{"type":"tool_result","tool_use_id":"x","content":[{"type":"text","text":"ok"}]}`,
		"image":                  `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + d + `"}}`,
		"image spaced":           `{ "type" : "image" , "source" : { "type" : "base64" , "data" : "` + d + `" } }`,
		"image newline":          "{\n\"source\":\n{\"data\":\"" + d + "\",\"type\":\"base64\"},\n\"type\":\"image\"}",
		"keys reversed":          `{"source":{"data":"` + d + `","type":"base64"},"type":"document"}`,
		"tool_result image":      `{"type":"tool_result","content":[{"type":"image","source":{"type":"base64","data":"` + d + `"}}]}`,
		"escaped base64":         `{"type":"image","source":{"type":"\u0062ase64","data":"` + d + `"}}`,
		"escaped base64 upper":   `{"type":"image","source":{"type":"\u0042ASE64","data":"` + d + `"}}`,
		"escaped digit":          `{"type":"image","source":{"type":"base\u0036\u0034","data":"` + d + `"}}`,
		"escaped key data":       `{"type":"image","source":{"type":"base64","\u0064ata":"` + d + `"}}`,
		"escaped image":          `{"type":"\u0069mage","source":{"type":"base64","data":"` + d + `"}}`,
		"escaped data url semi":  `{"url":"data:image/png\u003bbase64,` + d + `"}`,
		"escaped data url slash": `{"url":"data:image\/png;base64,` + d + `"}`,
		"data url":               `{"image_url":{"url":"data:image/png;base64,` + d + `"}}`,
		"data url upper":         `{"url":"DATA:Image/PNG;BASE64,` + d + `"}`,
		"file_data":              `{"file":{"file_data":"data:application/pdf;base64,` + d + `"}}`,
		"gemini":                 `{"inlineData":{"mimeType":"image/png","data":"` + d + `"}}`,
		"gemini snake":           `{"inline_data":{"mime_type":"image/png","data":"` + d + `"}}`,
		"gemini escaped key":     `{"inline\u005fdata":{"data":"` + d + `"}}`,
		"gemini key only":        `{"inlineData":{"mimeType":"image/png"}}`,
		"audio":                  `{"input_audio":{"data":"` + d + `","format":"wav"}}`,
		"bedrock image":          `{"image":{"format":"png","source":{"bytes":"` + d + `"}}}`,
		"bedrock document":       `{"document":{"source":{"bytes":"` + d + `"}}}`,
		"bedrock escaped bytes":  `{"image":{"source":{"\u0062ytes":"` + d + `"}}}`,
		"bytes outside source":   `{"image":{"format":"png","bytes":"` + d + `"}}`,
		"marker in text":         `{"type":"text","text":"say \"type\":\"image\" and base64 and \"source\""}`,
		"base64 in text only":    `{"type":"text","text":"base64 data ` + d + `"}`,
		"data text field":        `{"type":"text","data":"` + d + `"}`,
		"escaped backslash":      `{"text":"a\\u0062ase64"}`,
		"escaped backslash+real": `{"text":"\\","type":"\u0062ase64"}`,
		"non-ascii escape":       `{"text":"\u00e9\u4e2d"}`,
		"bad escape":             `{"text":"\u00zz"}`,
		"truncated escape":       `{"text":"\u00`,
		"lone backslash":         `{"text":"x\`,
		"image then garbage":     `{"type":"image","source":{"type":"base64","data":"` + d + `"}} }{`,
		"two values":             `{"a":1} {"image_url":{"url":"data:image/png;base64,` + d + `"}}`,
		"array top":              `[{"type":"image","source":{"type":"base64","data":"` + d + `"}}]`,
		"string top":             `"base64"`,
		"number top":             `12`,
	}
	for name, in := range cases {
		checkSame(t, name, in)
	}
}

func TestMediaPrefilterMatchesExactRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(39))
	frags := []string{
		`{`, `}`, `[`, `]`, `,`, `:`, ` `, "\n", `"type"`, `"image"`, `"document"`, `"base64"`, `"source"`,
		`"data"`, `"bytes"`, `"url"`, `"image_url"`, `"file_data"`, `"inlineData"`, `"inline_data"`, `"input_audio"`,
		`"content"`, `"text"`, `"tool_result"`, `"data:image/png;base64,QUJDRA=="`, `"DATA:application/PDF;BASE64,QQ"`,
		`"data:text/html;base64,QQ"`, `"QUJD"`, `"base"`, `"64"`, `"B"`, `"Ase64"`, `"\u0062ase64"`, `"\u0069mage"`,
		`"\u003b"`, `"\\"`, `"\\u0062"`, `"\/"`, `"\u00e9"`, `"\u00"`, `"\q"`, `\`, `12`, `null`, `true`,
	}
	for i := 0; i < 30000; i++ {
		var sb strings.Builder
		for j, k := 0, rng.Intn(24); j < k; j++ {
			sb.WriteString(frags[rng.Intn(len(frags))])
		}
		checkSame(t, "random", sb.String())
	}
	// Structured: a real media block with random order, spacing and escaped parts.
	esc := func(s string) string {
		var sb strings.Builder
		for _, c := range s {
			if rng.Intn(4) == 0 {
				sb.WriteString(`\u00` + string("0123456789abcdef"[c>>4]) + string("0123456789abcdef"[c&15]))
			} else {
				sb.WriteRune(c)
			}
		}
		return sb.String()
	}
	sp := func() string { return []string{"", " ", "\n", "\t "}[rng.Intn(4)] }
	for i := 0; i < 5000; i++ {
		d := strings.Repeat("Q", 1+rng.Intn(50))
		src := []string{`"type"` + sp() + `:` + sp() + `"` + esc("base64") + `"`, `"data":"` + d + `"`}
		rng.Shuffle(2, func(a, b int) { src[a], src[b] = src[b], src[a] })
		blk := []string{`"type":"` + esc("image") + `"`, `"source":` + sp() + `{` + src[0] + `,` + sp() + src[1] + `}`}
		rng.Shuffle(2, func(a, b int) { blk[a], blk[b] = blk[b], blk[a] })
		in := `{` + blk[0] + `,` + blk[1] + `}`
		if rng.Intn(2) == 0 {
			in = `{"type":"tool_result","content":[` + in + `]}`
		}
		checkSame(t, "structured", in)
		if mediaBytesExact([]byte(in)) == 0 {
			t.Fatalf("structured case has no media: %q", in)
		}
	}
}

// bodyNoMedia builds ~total bytes over `pos` positions of text and tool_result blocks.
func bodyNoMedia(total, pos int) [][]byte {
	out := make([][]byte, pos)
	each := total / pos
	for i := range out {
		txt := strings.Repeat("The quick brown fox, \\\"quoted\\\", jumps over the lazy dog.\\n", each/64)
		out[i] = []byte(`{"role":"user","content":[{"type":"text","text":"` + txt + `"},{"type":"tool_result","tool_use_id":"t` +
			itoa(i) + `","content":[{"type":"text","text":"ok"}]}]}`)
	}
	return out
}

func BenchmarkNewBodyNoMedia(b *testing.B) {
	msgs := bodyNoMedia(670_000, 24)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		NewBody(nil, nil, msgs)
	}
}

func BenchmarkNewBodyFewImages(b *testing.B) {
	msgs := bodyNoMedia(670_000, 24)
	for _, i := range []int{3, 11, 20} {
		msgs[i] = []byte(`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` +
			strings.Repeat("A", 20_000) + `"}}]}`)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		NewBody(nil, nil, msgs)
	}
}

func BenchmarkNewBodyNoMediaExact(b *testing.B) {
	msgs := bodyNoMedia(670_000, 24)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, p := range msgs {
			mediaBytesExact(p)
		}
	}
}
