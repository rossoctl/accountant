package engine

import (
	"bytes"
	"encoding/json"
	"math"
	"math/bits"
	"strings"
	"time"
)

// Fingerprint is the most recent request of one thread (§5.1). The library builds it inside
// Compute, because Cached comes from the usage, which arrives after the response.
type Fingerprint struct {
	Thread                                  string
	Model                                   string    // the model the request was sent for; "" when the host did not say
	ModelKey                                string    // RequestMeta.ModelKey; "" when the host gave none
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
	// cumOpq[i+1] is the opaque bytes of positions 0..i, each clamped to 0..Bytes-Media. Built with cum.
	cumOpq []int
	// cumCnt[i+1] and cumCtk[i+1] are the counted opaque bytes and their tokens over positions 0..i
	// (PosHash.OpaqueCounted, OpaqueTokens), clamped as opqCounted and opqTokens do. Built with cum.
	cumCnt, cumCtk []int
}

// PosHash holds hashes and sizes only (R2).
type PosHash struct {
	Hash  uint64 `json:"hash"` // cumulative: equal hashes mean an equal prefix up to this position
	Bytes int32  `json:"bytes"`
	// Media is the part of Bytes that is a base64 image or document block. NewBody finds it. The
	// token ratio leaves it out (§4.2 rule 7). It is not in the hash.
	Media int32 `json:"media,omitempty"`
	// Opaque is the part of Bytes that is an opaque string the client sends back and the provider
	// bills as few tokens: a thinking signature. NewBody finds it. The token ratio leaves it out, and it
	// has a fixed price of 1 token for each 250 bytes (§4.2 rule 7). It is not in the hash.
	Opaque int32 `json:"opaque,omitempty"`
	// OpaqueCounted is the part of Opaque whose tokens the host knows, and OpaqueTokens is those tokens:
	// the thinking tokens of the response that made each block (design D16). NewBodyCounted sets both.
	// The library prices the counted bytes at OpaqueTokens and the rest of Opaque at the fixed price.
	// OpaqueCounted 0 means no count, whatever OpaqueTokens says. Neither is in the hash.
	OpaqueCounted int32 `json:"opaqueCounted,omitempty"`
	OpaqueTokens  int32 `json:"opaqueTokens,omitempty"`
	// Unseen counts the media blocks of the position whose tokens the bytes do not show: an image or a
	// document given by url or by file id (no bytes at all), and a PDF (billed by page, not by byte).
	// NewBody finds them. They change no price. Only the check of the token ratio reads them, to name its
	// failure (media_unseen, §6.5 check 9). It is not in the hash, and Snapshot does not keep it: check 9
	// reads only the forwarded body of the current request, so a restored body with Unseen 0 is enough.
	Unseen int32 `json:"unseen,omitempty"`
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
	return NewBodyCounted(tools, system, messages, nil)
}

// OpaqueCount looks up the host's token count for one opaque string, by its OpaqueKey. ok false means
// the host has no count for it, and the library uses the fixed price.
type OpaqueCount func(key uint64) (tokens int, ok bool)

// NewBodyCounted is NewBody with the host's token counts for opaque strings (design D16). For each
// thinking signature and each redacted_thinking data string it finds, it calls count with the string's
// OpaqueKey. A known count goes to PosHash.OpaqueCounted and PosHash.OpaqueTokens. A negative count is
// no count: that block keeps the fixed price. A nil count is NewBody.
// The hashes and the sizes do not depend on count.
func NewBodyCounted(tools, system, messages [][]byte, count OpaqueCount) Body {
	b := Body{Start: [3]int{0, len(tools), len(tools) + len(system)}}
	var h uint64 = 14695981039346656037
	for _, section := range [][][]byte{tools, system, messages} {
		for _, p := range section {
			for _, c := range p { // FNV-1a, chained so the hash of a position covers its whole prefix
				h = (h ^ uint64(c)) * 1099511628211
			}
			h = (h ^ 0xff) * 1099511628211 // position separator
			o := opaqueOf(p, count)
			b.Pos = append(b.Pos, PosHash{Hash: h, Bytes: clamp32(len(p)), Media: clamp32(mediaBytes(p)), Opaque: clamp32(o.bytes),
				OpaqueCounted: clamp32(o.counted), OpaqueTokens: clamp32(o.tokens), Unseen: clamp32(unseenBlocks(p))})
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

// opaqueBytes counts the opaque strings in one position (R2: it keeps only the count). Two forms
// count, both from the Anthropic Messages API (also Claude on Bedrock and Vertex):
//   - a "thinking" block: the length of its "signature" string;
//   - a "redacted_thinking" block: the length of its "data" string.
//
// The provider bills these strings apart from text, so they stay out of the text ratio. The
// thinking text itself is text. Other providers' opaque fields (OpenAI "encrypted_content", Gemini
// "thoughtSignature") are not counted until their billing is known. A position that is not valid
// JSON counts what was read before the error.
func opaqueBytes(p []byte) int { return opaqueOf(p, nil).bytes }

// opaqueSum is the opaque bytes of one position, and the part of them that the host counted.
type opaqueSum struct{ bytes, counted, tokens int }

// opaqueOf counts the opaque strings in one position, as opaqueBytes does, and looks up each one in
// count. The strings are read only to take their length and their key; neither is kept (R2).
func opaqueOf(p []byte, count OpaqueCount) opaqueSum {
	var o opaqueSum
	if !mightHoldOpaque(p) {
		return o
	}
	each := func(s string) {
		o.bytes += len(s)
		if count == nil || s == "" {
			return
		}
		if t, ok := count(OpaqueKey(s)); ok && t >= 0 {
			o.counted += len(s)
			o.tokens += t
		}
	}
	dec := json.NewDecoder(bytes.NewReader(p))
	for {
		if _, ok := walkOpaque(dec, each); !ok {
			return o
		}
	}
}

// OpaqueKey is the key of one opaque string (a thinking signature, or the data of a redacted_thinking
// block) in the host's table of counts: the 64-bit FNV-1a hash of the string, as JSON decodes it. A host
// stores a count under the key of the string it read in a response, and NewBodyCounted looks it up
// under the key of the same string in a later request (design D16).
func OpaqueKey(s string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h = (h ^ uint64(s[i])) * 1099511628211
	}
	return h
}

// SplitOpaqueTokens splits the thinking tokens of one response between the opaque strings it made, in
// proportion to their lengths (design D16). Anthropic reports one count for each response
// (usage.output_tokens_details.thinking_tokens), so a response with two blocks needs a split. The
// split is an estimate. The parts add up to tokens exactly: the remainder goes to the largest
// fractions first, and to the earlier string on a tie. With no tokens, or no strings, every part is 0.
// When every string is empty, the tokens are split evenly.
func SplitOpaqueTokens(opaques []string, tokens int) []int {
	out := make([]int, len(opaques))
	if tokens <= 0 || len(opaques) == 0 {
		return out
	}
	w := make([]int64, len(opaques))
	var total int64
	for i, s := range opaques {
		w[i] = int64(len(s))
		total += w[i]
	}
	if total == 0 {
		for i := range w {
			w[i] = 1
		}
		total = int64(len(w))
	}
	rem := make([]int64, len(w))
	given := 0
	for i := range w {
		// tokens x w[i] is a 128-bit product: the tokens come from the host, and a body can be 4 GB. w[i] is at most
		// total, so the quotient is at most tokens and the division cannot overflow.
		hi, lo := bits.Mul64(uint64(tokens), uint64(w[i]))
		q, r := bits.Div64(hi, lo, uint64(total))
		out[i] = int(q)
		rem[i] = int64(r)
		given += out[i]
	}
	for ; given < tokens; given++ {
		best := 0
		for i := range rem {
			if rem[i] > rem[best] {
				best = i
			}
		}
		out[best]++
		rem[best] = -1
	}
	return out
}

// mightHoldOpaque is a byte-level pre-filter for opaqueBytes. It returns false only when the exact
// count is 0. Every count needs the key "signature" or the value "redacted_thinking". An escape could
// hide either one, so an escape that could spell an ASCII letter makes the answer true
// (escapeMightSpellMarker).
func mightHoldOpaque(p []byte) bool {
	return bytes.Contains(p, []byte(`"signature"`)) || bytes.Contains(p, []byte(`"redacted_thinking"`)) ||
		escapeMightSpellMarker(p)
}

// walkOpaque reads one JSON value and calls each for every opaque string it holds. It returns the
// value when it is a string. false means the input ended or is not valid JSON.
func walkOpaque(dec *json.Decoder, each func(string)) (str string, ok bool) {
	tok, err := dec.Token()
	if err != nil {
		return "", false
	}
	switch v := tok.(type) {
	case string:
		return v, true
	case json.Delim:
		if v == '[' {
			for dec.More() {
				if _, ok := walkOpaque(dec, each); !ok {
					return "", false
				}
			}
			_, err := dec.Token()
			return "", err == nil
		}
		var typ, sig, data string
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return "", false
			}
			key, _ := kt.(string)
			child, ok := walkOpaque(dec, each)
			if !ok {
				return "", false
			}
			switch key {
			case "type":
				typ = child
			case "signature":
				sig = child
			case "data":
				data = child
			}
		}
		if _, err := dec.Token(); err != nil {
			return "", false
		}
		switch typ {
		case "thinking":
			each(sig)
		case "redacted_thinking":
			each(data)
		}
	}
	return "", true
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

// modelID is the string that identifies the fingerprint's model in a comparison (RequestMeta.modelID).
func (fp *Fingerprint) modelID() string {
	if fp.ModelKey != "" {
		return fp.ModelKey
	}
	return fp.Model
}

// unseenBlocks counts the media blocks of a position whose tokens its bytes do not show (PosHash.Unseen):
//   - an Anthropic image or document block with a "url" or "file" source;
//   - an Anthropic document block with a base64 PDF (a PDF is billed by page);
//   - an OpenAI chat "image_url" block whose url is not a data URL, and a "file" block with a "file_id" or a
//     PDF data URL;
//   - an OpenAI Responses "input_image" block with a url that is not a data URL, or a "file_id", and an
//     "input_file" block with a "file_id", a "file_url" or a PDF data URL.
//
// Gemini (fileData, an inlineData PDF) and Bedrock (an s3Location source) are not read.
//
// Text that only quotes these words is not a block: a tool output is a string, and the detector reads
// JSON objects only. A position that is not valid JSON counts 0. A key written with \u escapes is not
// seen. The count only names a failed check, so a miss costs a less exact reason and no price.
func unseenBlocks(p []byte) int {
	if !mightHoldUnseen(p) {
		return 0
	}
	var v any
	if json.Unmarshal(p, &v) != nil {
		return 0
	}
	n := 0
	countUnseen(v, &n)
	return n
}

// mightHoldUnseen is a byte-level pre-filter for unseenBlocks: it needs the name of a block type and a
// word that makes it a reference or a PDF. It returns false only when unseenBlocks must count 0.
func mightHoldUnseen(p []byte) bool {
	hasType := false
	for _, w := range [][]byte{[]byte(`"image"`), []byte(`"document"`), []byte(`"image_url"`),
		[]byte(`"input_image"`), []byte(`"input_file"`), []byte(`"file"`)} {
		if bytes.Contains(p, w) {
			hasType = true
			break
		}
	}
	if !hasType {
		return false
	}
	for _, w := range [][]byte{[]byte(`"url"`), []byte(`"image_url"`), []byte(`"file_id"`),
		[]byte(`"file_url"`)} {
		if bytes.Contains(p, w) {
			return true
		}
	}
	// "application/pdf" without case, as isPDFDataURL and the media_type match read it. The full MIME
	// type, not "pdf": a large base64 payload holds "pdf" by chance and would be parsed a second time.
	return containsFoldASCII(p, "application/pdf")
}

// containsFoldASCII is bytes.Contains without ASCII case, for a lower-case ASCII substr. It does not copy p.
func containsFoldASCII(p []byte, substr string) bool {
	for i := 0; i+len(substr) <= len(p); i++ {
		j := 0
		for ; j < len(substr); j++ {
			c := p[i+j]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != substr[j] {
				break
			}
		}
		if j == len(substr) {
			return true
		}
	}
	return false
}

func countUnseen(v any, n *int) {
	switch x := v.(type) {
	case map[string]any:
		if unseenBlock(x) {
			*n++
		}
		for _, e := range x {
			countUnseen(e, n)
		}
	case []any:
		for _, e := range x {
			countUnseen(e, n)
		}
	}
}

// unseenBlock says whether one JSON object is a media block of the kinds in unseenBlocks.
func unseenBlock(m map[string]any) bool {
	str := func(v any) string { s, _ := v.(string); return s }
	switch str(m["type"]) {
	case "image", "document":
		src, _ := m["source"].(map[string]any)
		switch str(src["type"]) {
		case "url", "file":
			return true
		case "base64":
			return str(m["type"]) == "document" && hasPrefixFold(str(src["media_type"]), "application/pdf")
		}
	case "image_url":
		return urlByReference(m["image_url"])
	case "input_image":
		return urlByReference(m["image_url"]) || str(m["file_id"]) != ""
	case "file":
		f, _ := m["file"].(map[string]any)
		return str(f["file_id"]) != "" || isPDFDataURL(str(f["file_data"]))
	case "input_file":
		return str(m["file_id"]) != "" || str(m["file_url"]) != "" || isPDFDataURL(str(m["file_data"]))
	}
	return false
}

// urlByReference says whether v (a url string, or an object with a "url" string) names a place to fetch
// the image from, and is not a data URL that holds the bytes.
func urlByReference(v any) bool {
	s, ok := v.(string)
	if m, isMap := v.(map[string]any); isMap {
		s, ok = m["url"].(string)
	}
	return ok && s != "" && !hasPrefixFold(s, "data:")
}

func isPDFDataURL(s string) bool { return hasPrefixFold(s, "data:application/pdf") }
