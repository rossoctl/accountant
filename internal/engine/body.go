package engine

import "sort"

// Helpers on Body positions. Inside the library a position is a flat index into Body.Pos, because
// the flat order is the provider's hash order (§4.3). -1 means "before the first position".

func sectionIndex(s Section) int {
	switch s {
	case SectionTools:
		return 0
	case SectionSystem:
		return 1
	case SectionMessages:
		return 2
	}
	return -1
}

var sections = [3]Section{SectionTools, SectionSystem, SectionMessages}

func (b Body) sectionEnd(i int) int {
	if i < 2 {
		return b.Start[i+1]
	}
	return len(b.Pos)
}

// flat returns the flat index of p, and false when p is not in the body.
func (b Body) flat(p Position) (int, bool) {
	s := sectionIndex(p.Section)
	if s < 0 || p.Index < 0 || b.Start[s]+p.Index >= b.sectionEnd(s) {
		return -1, false
	}
	return b.Start[s] + p.Index, true
}

// position is the inverse of flat. An index past the end gives the last message after it.
func (b Body) position(i int) Position {
	for s := 2; s >= 0; s-- {
		if i >= b.Start[s] {
			return Position{Section: sections[s], Index: i - b.Start[s]}
		}
	}
	return Position{Section: SectionTools, Index: i}
}

// cumBytes returns the bytes of positions 0..i; 0 for i < 0.
func (b Body) cumBytes(i int) int {
	if len(b.cum) == len(b.Pos)+1 { // built by NewBody
		return b.cum[min(max(i+1, 0), len(b.Pos))]
	}
	n := 0
	for j := 0; j <= i && j < len(b.Pos); j++ {
		n += int(b.Pos[j].Bytes)
	}
	return n
}

// buildPrefix fills cum and cumMed from Pos.
func (b *Body) buildPrefix() {
	b.cum = make([]int, len(b.Pos)+1)
	b.cumMed = make([]int, len(b.Pos)+1)
	for i, p := range b.Pos {
		b.cum[i+1] = b.cum[i] + int(p.Bytes)
		b.cumMed[i+1] = b.cumMed[i] + int(max(min(p.Media, p.Bytes), 0)) // a host-built Media outside 0..Bytes is clamped
	}
}

// withPrefix returns b with its prefix sums built. A Body from NewBody or Restore has them; one
// decoded from JSON does not.
func (b Body) withPrefix() Body {
	if len(b.cum) != len(b.Pos)+1 || len(b.cumMed) != len(b.Pos)+1 {
		b.buildPrefix()
	}
	return b
}

func (b Body) sectionBytes(s int) int {
	n := 0
	for j := b.Start[s]; j < b.sectionEnd(s); j++ {
		n += int(b.Pos[j].Bytes)
	}
	return n
}

// firstDiff is the first flat index where a and b differ. A body that only grew at the end has
// its first diff at the old end (§5.2).
func firstDiff(a, b Body) int {
	i := 0
	for i < len(a.Pos) && i < len(b.Pos) && a.Pos[i].Hash == b.Pos[i].Hash {
		i++
	}
	return i
}

// bp is a breakpoint as a flat index.
type bp struct {
	at  int
	ttl string
}

// breakpoints returns the breakpoints of bps that are in b, in flat order.
func (b Body) breakpoints(bps []Breakpoint) []bp {
	var out []bp
	for _, x := range bps {
		if i, ok := b.flat(x.Pos); ok {
			out = append(out, bp{i, x.TTL})
		}
	}
	for i := 1; i < len(out); i++ { // insertion sort: the lists are short
		for j := i; j > 0 && out[j].at < out[j-1].at; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// lastAtOrBefore returns the last breakpoint at or before x, or -1.
func lastAtOrBefore(bps []bp, x int) int {
	r := -1
	for _, p := range bps {
		if p.at <= x && p.at > r {
			r = p.at
		}
	}
	return r
}

// mediaBytesPerToken is the fixed price of a media block (a base64 image or document): about one
// token for each 250 bytes of its base64 text. Media tokens do not follow the text ratio, so the
// request ratio leaves the media bytes out and takes the media tokens out of the billed total
// first (§4.2 rule 7).
const mediaBytesPerToken = 250

// cumMedia returns the media bytes of positions 0..i; 0 for i < 0.
func (b Body) cumMedia(i int) int {
	if len(b.cumMed) == len(b.Pos)+1 { // built by NewBody or Restore
		return b.cumMed[min(max(i+1, 0), len(b.Pos))]
	}
	n := 0
	for j := 0; j <= i && j < len(b.Pos); j++ {
		n += int(max(min(b.Pos[j].Media, b.Pos[j].Bytes), 0)) // a host-built Media outside 0..Bytes is clamped
	}
	return n
}

// ratio converts forwarded text bytes to tokens with the request's own ratio (§5.3). The bytes and
// the tokens are those of the text only: media is priced apart (mediaBytesPerToken).
type ratio struct{ tokens, bytes int }

// newRatio builds the ratio of a body that the provider billed for billed prompt tokens.
// The tokens clamp to 0 when the bill is below the media's fixed price; checks() reports that.
func newRatio(billed int, b Body) ratio {
	last := len(b.Pos) - 1
	media := b.cumMedia(last)
	return ratio{tokens: max(billed-media/mediaBytesPerToken, 0), bytes: b.cumBytes(last) - media}
}

func (r ratio) of(bytes int) int {
	if r.bytes <= 0 {
		return 0
	}
	return int(int64(bytes) * int64(r.tokens) / int64(r.bytes))
}

// tokensOf converts bytes of which media are media bytes: the text with the ratio, the media with
// the fixed price.
func (r ratio) tokensOf(bytes, media int) int {
	return r.of(bytes-media) + media/mediaBytesPerToken
}

// upTo is the tokens of positions 0..i of b.
func (r ratio) upTo(b Body, i int) int { return r.tokensOf(b.cumBytes(i), b.cumMedia(i)) }

// between is the tokens of positions from+1..to of b (the bytes of position from are not counted
// twice: the same difference as cumBytes(to) - cumBytes(from)).
func (r ratio) between(b Body, from, to int) int {
	return r.tokensOf(b.cumBytes(to)-b.cumBytes(from), b.cumMedia(to)-b.cumMedia(from))
}

// extent returns the last candidate position whose estimated prefix fits in tokens, or -1. The
// tolerance absorbs the error of the byte ratio and the 128-token rounding of older OpenAI models.
// With no candidate breakpoint every position is a candidate.
func extent(b Body, r ratio, tokens int, candidates []bp) int {
	if tokens <= 0 {
		return -1
	}
	limit := tokens + 128 + tokens/50
	b = b.withPrefix()
	if len(candidates) == 0 {
		// upTo grows with i (the prefix sums never fall), so the fitting positions are a prefix
		// of the body: binary search for its end.
		return sort.Search(len(b.Pos), func(i int) bool { return r.upTo(b, i) > limit }) - 1
	}
	best := -1
	for _, c := range candidates {
		if c.at > best && r.upTo(b, c.at) <= limit {
			best = c.at
		}
	}
	return best
}
