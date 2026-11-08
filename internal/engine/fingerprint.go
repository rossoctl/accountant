package engine

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"time"
)

// Fingerprint is the most recent request of one thread (§5.1). The library builds it inside
// Compute, because Cached comes from the usage, which arrives after the response.
type Fingerprint struct {
	Thread                                  string
	Model                                   string    // the model the request was sent for; "" when the host did not say
	At                                      time.Time // the start of the request; the request gap is measured from it (§5.2)
	Client                                  Body      // client body, before any plugin
	Forwarded                               Body      // body as sent upstream
	ClientBreakpoints, ForwardedBreakpoints []Breakpoint
	Cached                                  Position // extent cached after its response, in forwarded space

	// splices are the message splices that produced Forwarded from Client, in pipeline order. The
	// library needs them to map Cached back into client space (§5.1). Not part of the API.
	splices []MessageSplice
}

// Body lists every position in hash order: tools, then system, then messages. The slice index is
// the position. Start gives the first index of each section.
type Body struct {
	Start [3]int    `json:"start"`
	Pos   []PosHash `json:"pos"`

	// cum[i+1] is the bytes of positions 0..i. NewBody builds it. A Body made another way (decoded
	// from JSON) has none, and cumBytes sums instead. Not part of the API.
	cum []int
	// cumMed[i+1] is the media bytes of positions 0..i, each clamped to 0..Bytes. Built with cum.
	cumMed []int
}

// PosHash holds hashes and sizes only (R2).
type PosHash struct {
	Hash  uint64 `json:"hash"` // cumulative: equal hashes mean an equal prefix up to this position
	Bytes int32  `json:"bytes"`
	// Media is the part of Bytes that is a base64 image or document block. NewBody finds it. The
	// token ratio leaves it out (§4.2 rule 7). It is not in the hash.
	Media int32 `json:"media,omitempty"`
}

// Breakpoint is a cache breakpoint in a body.
type Breakpoint struct {
	Pos Position `json:"pos"`
	TTL string   `json:"ttl"` // "5m" | "1h"
}

// NewBody hashes one body in hash order, so every host builds the same Body for the same bytes.
// The host calls it for the client body (before BeginRequest) and for the forwarded body (before
// EndRequest).
func NewBody(tools, system, messages [][]byte) Body {
	b := Body{Start: [3]int{0, len(tools), len(tools) + len(system)}}
	var h uint64 = 14695981039346656037
	for _, section := range [][][]byte{tools, system, messages} {
		for _, p := range section {
			for _, c := range p { // FNV-1a, chained so the hash of a position covers its whole prefix
				h = (h ^ uint64(c)) * 1099511628211
			}
			h = (h ^ 0xff) * 1099511628211 // position separator
			b.Pos = append(b.Pos, PosHash{Hash: h, Bytes: clamp32(len(p)), Media: clamp32(mediaBytes(p))})
		}
	}
	b.buildPrefix()
	return b
}

// mediaBytes counts the base64 media payload in one position (R2: it keeps only the count). Two
// forms count:
//   - an image or document block whose "source" has "type":"base64": the length of the source's
//     "data" string (Anthropic form);
//   - a "url", "image_url" or "file_data" string that starts with "data:" and holds ";base64,": the bytes after
//     the comma (OpenAI data-URL form).
//
// Text that only quotes "base64" or "data" is not media. A position that is not valid JSON counts
// what was read before the error.
func mediaBytes(p []byte) int {
	if !mightHoldMedia(p) {
		return 0
	}
	return mediaBytesExact(p)
}

// mediaBytesExact is the exact detector: it decodes every token of the position.
func mediaBytesExact(p []byte) int {
	dec := json.NewDecoder(bytes.NewReader(p))
	n := 0
	for {
		if _, ok := walkValue(dec, &n); !ok {
			return n
		}
	}
}

// mightHoldMedia is a byte-level pre-filter for mediaBytes. It returns false only when the exact
// detector must count 0; true means "decode and decide". Every count comes from one of these:
//   - a "base64" source or data URL: the text "base64" (data URLs: ";base64," without case);
//   - a Gemini or OpenAI audio object: a key "inlineData", "inline_data" or "input_audio", and a "data" key;
//   - a Bedrock Converse source: the keys "source" and "bytes".
//
// A key or value compares after JSON decoding, so an escape could hide any of these. A letter,
// digit or ":;," can come from \u only, and "/" from \/ (a "/" alone never makes a marker). So a
// \u escape that decodes below 0x80, or one that is not four hex digits, makes the answer true.
// A \u escape of 0x80 or more cannot spell an ASCII marker: the keys match exactly, and the case fold
// of the data URL marker compares a slice of len(";base64,") BYTES (hasPrefixFold), so a rune that folds to
// an ASCII letter in more bytes (U+017F to "s", U+212A to "k") cuts the slice short and never matches. The
// tests pin both spellings.
func mightHoldMedia(p []byte) bool {
	if escapeMightSpellMarker(p) {
		return true
	}
	if hasBase64Fold(p) {
		return true
	}
	if bytes.Contains(p, []byte(`"source"`)) && bytes.Contains(p, []byte(`"bytes"`)) {
		return true
	}
	if bytes.Contains(p, []byte(`"data"`)) &&
		(bytes.Contains(p, []byte(`"inlineData"`)) || bytes.Contains(p, []byte(`"inline_data"`)) || bytes.Contains(p, []byte(`"input_audio"`))) {
		return true
	}
	return false
}

// hasBase64Fold reports whether p holds "base64" in any case (ASCII).
func hasBase64Fold(p []byte) bool {
	for off := 0; ; {
		i := bytes.Index(p[off:], []byte("64"))
		if i < 0 {
			return false
		}
		i += off
		if i >= 4 && foldASCII4(p[i-4:i], "base") {
			return true
		}
		off = i + 1
	}
}

// foldASCII4 reports whether the four bytes b equal the lower-case ASCII word w without case. It compares
// bytes, so it does not allocate.
func foldASCII4(b []byte, w string) bool {
	for i := 0; i < 4; i++ {
		c := b[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != w[i] {
			return false
		}
	}
	return true
}

// escapeMightSpellMarker reports whether p holds a backslash escape that could decode to an ASCII
// character other than "/". Plain escapes (\n, \", \\) and \u escapes of 0x80 or more cannot.
func escapeMightSpellMarker(p []byte) bool {
	for off := 0; ; {
		i := bytes.IndexByte(p[off:], '\\')
		if i < 0 {
			return false
		}
		i += off
		if i+1 >= len(p) {
			return false // a lone backslash ends the input: the decoder reads nothing past it
		}
		if p[i+1] != 'u' {
			off = i + 2 // \\ , \" , \/ , \n ...: skip the pair, so \\u is not read as \u
			continue
		}
		if i+6 > len(p) {
			return true
		}
		v := 0
		for _, c := range p[i+2 : i+6] {
			switch {
			case c >= '0' && c <= '9':
				v = v<<4 | int(c-'0')
			case c >= 'a' && c <= 'f':
				v = v<<4 | int(c-'a'+10)
			case c >= 'A' && c <= 'F':
				v = v<<4 | int(c-'A'+10)
			default:
				return true
			}
		}
		if v < 0x80 {
			return true
		}
		off = i + 6
	}
}

// valueInfo is what a parent needs to know about one parsed value.
type valueInfo struct {
	str      string // the value, when it is a string
	isStr    bool
	base64   int // the "data" length, when the value is a base64 source object; else 0
	data     int // the "data" string length, when the value is an object with one (Gemini, OpenAI audio)
	bytes    int // the "bytes" string length, when the value is an object with one (a Bedrock Converse source)
	srcBytes int // the length of this object's "source"."bytes" string (a Bedrock Converse image or document)
}

const dataURLMarker = ";base64,"

// hasPrefixFold is strings.HasPrefix without case: media types and URL schemes are case-insensitive
// (RFC 2045, RFC 3986).
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// indexFold is strings.Index without case, for an ASCII substr. It returns a byte index into s.
func indexFold(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if hasPrefixFold(s[i:], substr) {
			return i
		}
	}
	return -1
}

// dataURLPayload returns the payload length of a base64 image or PDF data URL, or 0. Another MIME
// type (data:text/html;base64,...) is text. The match is case-insensitive.
func dataURLPayload(s string) int {
	if !hasPrefixFold(s, "data:image/") && !hasPrefixFold(s, "data:application/pdf") {
		return 0
	}
	// Search for the marker without case and without changing the indices: lower-casing a string can
	// change its length (U+0130, U+212A). The marker is in the header, so no limit is needed: the
	// search stops at the first ";base64," and a payload holds no ";".
	i := indexFold(s, dataURLMarker)
	if i < 0 {
		return 0
	}
	return len(s) - i - len(dataURLMarker)
}

// walkValue reads one JSON value and adds the media it holds to *n. false means the input ended
// or is not valid JSON.
func walkValue(dec *json.Decoder, n *int) (valueInfo, bool) {
	tok, err := dec.Token()
	if err != nil {
		return valueInfo{}, false
	}
	switch v := tok.(type) {
	case string:
		return valueInfo{str: v, isStr: true}, true
	case json.Delim:
		if v == '[' {
			for dec.More() {
				if _, ok := walkValue(dec, n); !ok {
					return valueInfo{}, false
				}
			}
			_, err := dec.Token()
			return valueInfo{}, err == nil
		}
		return walkObject(dec, n)
	}
	return valueInfo{}, true
}

// walkObject reads the members of an object whose "{" is read. It counts the payload of an image
// or document block's base64 source, and of a data URL under "url", "image_url" or "file_data". It returns the
// payload length when the object itself is a base64 source, so its parent block can count it.
func walkObject(dec *json.Decoder, n *int) (valueInfo, bool) {
	var typ string
	var data valueInfo
	var srcLen, srcBytes, ownBytes, dataLen int
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return valueInfo{}, false
		}
		key, _ := kt.(string)
		child, ok := walkValue(dec, n)
		if !ok {
			return valueInfo{}, false
		}
		switch key {
		case "type":
			typ = child.str
		case "data":
			data = child
			if child.isStr {
				dataLen = len(child.str)
			}
		case "bytes":
			if child.isStr {
				ownBytes = len(child.str)
			}
		case "source":
			srcLen = child.base64
			srcBytes = child.bytes // Bedrock Converse: the block's "source"."bytes"
		case "inlineData", "inline_data", "input_audio":
			*n += child.data // Gemini, OpenAI audio: the object's "data" string
		case "image", "document":
			*n += child.srcBytes // Bedrock Converse: {"image":{"source":{"bytes":...}}}
		case "url", "image_url", "file_data":
			*n += dataURLPayload(child.str)
		}
	}
	if _, err := dec.Token(); err != nil {
		return valueInfo{}, false
	}
	if typ == "image" || typ == "document" {
		*n += srcLen
	}
	if typ == "base64" && data.isStr {
		return valueInfo{base64: len(data.str), data: dataLen}, true
	}
	return valueInfo{data: dataLen, bytes: ownBytes, srcBytes: srcBytes}, true
}

// clamp32 saturates n at the largest int32, so a position over 2 GiB does not wrap negative.
func clamp32(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n)
}
