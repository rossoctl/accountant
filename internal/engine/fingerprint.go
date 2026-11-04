package engine

import (
	"bytes"
	"encoding/json"
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
			b.Pos = append(b.Pos, PosHash{Hash: h, Bytes: int32(len(p)), Media: int32(mediaBytes(p))})
		}
	}
	b.cum = make([]int, len(b.Pos)+1)
	for i, p := range b.Pos {
		b.cum[i+1] = b.cum[i] + int(p.Bytes)
	}
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
	dec := json.NewDecoder(bytes.NewReader(p))
	n := 0
	for {
		if _, ok := walkValue(dec, &n); !ok {
			return n
		}
	}
}

// valueInfo is what a parent needs to know about one parsed value.
type valueInfo struct {
	str    string // the value, when it is a string
	isStr  bool
	base64 int // the "data" length, when the value is a base64 source object; else 0
}

const dataURLMarker = ";base64,"

// dataURLPayload returns the payload length of a base64 image or PDF data URL, or 0. Another MIME
// type (data:text/html;base64,...) is text.
func dataURLPayload(s string) int {
	if !strings.HasPrefix(s, "data:image/") && !strings.HasPrefix(s, "data:application/pdf") {
		return 0
	}
	i := strings.Index(s, dataURLMarker)
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
	var srcLen int
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
		case "source":
			srcLen = child.base64
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
		return valueInfo{base64: len(data.str)}, true
	}
	return valueInfo{}, true
}
