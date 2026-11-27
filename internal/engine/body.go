package engine

import (
	"math"
	"math/bits"
	"sort"
)

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

// unseen is the number of media blocks in b whose tokens the bytes do not show (PosHash.Unseen).
func (b Body) unseen() int {
	n := 0
	for _, p := range b.Pos {
		n += max(int(p.Unseen), 0) // a host-built body can hold a negative count; it is no block
	}
	return n
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

// buildPrefix fills the prefix sums (cum, cumMed, cumOpq, cumCnt, cumCtk) from Pos.
func (b *Body) buildPrefix() {
	n := len(b.Pos) + 1
	b.cum, b.cumMed, b.cumOpq, b.cumCnt, b.cumCtk = make([]int, n), make([]int, n), make([]int, n), make([]int, n), make([]int, n)
	for i, p := range b.Pos {
		b.cum[i+1] = b.cum[i] + int(p.Bytes)
		b.cumMed[i+1] = b.cumMed[i] + p.media()
		b.cumOpq[i+1] = b.cumOpq[i] + p.opaque()
		b.cumCnt[i+1] = b.cumCnt[i] + p.opqCounted()
		b.cumCtk[i+1] = b.cumCtk[i] + p.opqTokens()
	}
}

// media is Media clamped to 0..Bytes: a host-built Media outside that range is clamped.
func (p PosHash) media() int { return int(max(min(p.Media, p.Bytes), 0)) }

// opaque is Opaque clamped to 0..Bytes-Media, so the media and the opaque bytes never add up to more
// than Bytes. The media counts first.
func (p PosHash) opaque() int { return max(min(int(p.Opaque), int(p.Bytes)-p.media()), 0) }

// opqCounted is OpaqueCounted clamped to 0..opaque.
func (p PosHash) opqCounted() int { return max(min(int(p.OpaqueCounted), p.opaque()), 0) }

// opqTokens is OpaqueTokens when the position has counted bytes, else 0. A negative count is 0.
func (p PosHash) opqTokens() int {
	if p.opqCounted() == 0 {
		return 0
	}
	return max(int(p.OpaqueTokens), 0)
}

// withPrefix returns b with its prefix sums built. A Body from NewBody or Restore has them; one
// decoded from JSON does not.
func (b Body) withPrefix() Body {
	n := len(b.Pos) + 1
	if len(b.cum) != n || len(b.cumMed) != n || len(b.cumOpq) != n || len(b.cumCnt) != n || len(b.cumCtk) != n {
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
func (b Body) cumMedia(i int) int { return b.cumSum(b.cumMed, i, PosHash.media) }

// cumSum reads a prefix sum of positions 0..i (0 for i < 0). With no prefix table (a Body decoded
// from JSON) it sums f over the positions.
func (b Body) cumSum(table []int, i int, f func(PosHash) int) int {
	if len(table) == len(b.Pos)+1 { // built by NewBody or Restore
		return table[min(max(i+1, 0), len(b.Pos))]
	}
	n := 0
	for j := 0; j <= i && j < len(b.Pos); j++ {
		n += f(b.Pos[j])
	}
	return n
}

// cumOpaque returns the opaque bytes of positions 0..i; 0 for i < 0.
func (b Body) cumOpaque(i int) int { return b.cumSum(b.cumOpq, i, PosHash.opaque) }

// cumCounted returns the counted opaque bytes of positions 0..i; 0 for i < 0.
func (b Body) cumCounted(i int) int { return b.cumSum(b.cumCnt, i, PosHash.opqCounted) }

// cumCountTokens returns the host's tokens for the counted opaque bytes of positions 0..i; 0 for i < 0.
func (b Body) cumCountTokens(i int) int { return b.cumSum(b.cumCtk, i, PosHash.opqTokens) }

// nonText is the bytes of a range that the text ratio does not price: the media, the opaque bytes,
// and the part of the opaque bytes the host counted (counted bytes, ctok tokens).
type nonText struct{ media, opaque, counted, ctok int }

// parts is the bytes of a range and the non-text part of them.
type parts struct {
	bytes int
	nonText
}

// add adds q to p.
func (p *parts) add(q parts) {
	p.bytes += q.bytes
	p.media += q.media
	p.opaque += q.opaque
	p.counted += q.counted
	p.ctok += q.ctok
}

// minParts is the part that a and b both hold, field by field: a position the client and the
// forwarded body share.
func minParts(a, b parts) parts {
	return parts{min(a.bytes, b.bytes), nonText{min(a.media, b.media), min(a.opaque, b.opaque), min(a.counted, b.counted), min(a.ctok, b.ctok)}}
}

// between is the parts of positions from+1..to of b.
func (b Body) between(from, to int) parts {
	return parts{b.cumBytes(to) - b.cumBytes(from), nonText{b.cumMedia(to) - b.cumMedia(from), b.cumOpaque(to) - b.cumOpaque(from),
		b.cumCounted(to) - b.cumCounted(from), b.cumCountTokens(to) - b.cumCountTokens(from)}}
}

// opaqueBytesPerToken is the fixed price of opaque bytes (a thinking signature) that the host did not
// count: one token for each 250 bytes, the media price. It is a fallback, not a measurement (design
// D15). Measured on Opus 5.5, a signature bills 0.075 to 0.35 tokens a byte, more for a longer one;
// older Claude models strip signatures and bill 0 (design D16). A price above 0 that cannot fail a
// check removes the large error in the text ratio. The host's count for each block replaces it (D16).
const opaqueBytesPerToken = 250

// ratio converts forwarded text bytes to tokens with the request's own ratio (§5.3). The bytes and
// the tokens are those of the text only: media and opaque bytes (a thinking signature) are priced
// apart (§4.2 rule 7). A media block costs mediaTok tokens for each mediaPer bytes of its base64 text:
// 1 for each mediaBytesPerToken, unless the bill is below that price (newRatio). Opaque bytes with no
// host count cost opqTok tokens for each opqPer bytes in the same way. Counted opaque bytes cost the
// host's tokens when counts is true, and the fixed price when the bill had no room for them.
type ratio struct {
	tokens, bytes      int
	mediaTok, mediaPer int
	opqTok, opqPer     int
	capped             bool // the media price was above the bill and was cut to fit it
	counts             bool // the host's opaque counts are used (newRatio)
	countsDropped      bool // the body had counts, but they were above the room the bill leaves
}

// newRatio builds the ratio of a body that the provider billed for billed prompt tokens.
//
// Opaque bytes get their price first: the host's tokens for the counted bytes and the fixed price for
// the rest. The price never takes more than the bill leaves after the media price and the least the
// text can cost (minTokensPerByte for each text byte), the room. If the counts of the forwarded body,
// plus the fixed price of its uncounted opaque bytes, do not fit in the room, the provider did not bill
// them: the model strips thinking (Sonnet 4.5), or the host's counts are wrong. The library then drops
// the counts for the request, prices every opaque byte at the fixed price, and marks countsDropped
// (check 14). Only counts in the forwarded body can be tested: with none there, the counts of the
// client body are used as they are. If the fixed price is above the room, it is cut to the room. So
// the opaque price never cuts the media price, and it never fails check 9 or 11.
//
// When the fixed media price is above the bill, the price is wrong, because the provider cannot bill
// less than the media costs. The library then gives the media the bill less the least the text can cost,
// and the text that least. The text ratio is then at its floor, so a saving on text is the lowest it can
// be (a doubtful figure may only lower a saving). The ratio is marked capped: the operations that cover
// media are unpriced, and all others are estimates.
func newRatio(billed int, b Body) ratio {
	last := len(b.Pos) - 1
	all := b.between(-1, last)
	media, opaque := all.media, all.opaque
	text := all.bytes - media - opaque
	floor := int(math.Ceil(minTokensPerByte * float64(max(text, 0))))
	room := max(billed-media/mediaBytesPerToken-floor, 0)
	r := ratio{mediaTok: 1, mediaPer: mediaBytesPerToken, opqTok: 1, opqPer: opaqueBytesPerToken}
	// The counts are used unless the forwarded body proves them wrong. A forwarded body with no counted
	// block proves nothing, so the counts of the client body (a block the operator dropped) still apply.
	r.counts = true
	if all.counted > 0 && all.ctok+(opaque-all.counted)/opaqueBytesPerToken > room {
		r.counts, r.countsDropped = false, true
	}
	// The fixed price covers the uncounted opaque bytes (all of them when the counts are dropped). It is
	// cut to the room the counts leave. With a counted block in the forwarded body and the counts used,
	// the guard above already makes it fit, so the cut acts only when the forwarded body holds no count
	// or the counts are dropped.
	fixed, left := opaque, room
	if r.counts {
		fixed, left = opaque-all.counted, room-all.ctok
	}
	if fixed/opaqueBytesPerToken > left {
		r.opqTok, r.opqPer = left, fixed
	}
	billed -= r.opaqueOf(all.nonText)
	r.tokens, r.bytes = billed-media/mediaBytesPerToken, text
	if r.tokens >= 0 {
		return r
	}
	mediaTokens := max(billed-floor, 0)
	r.tokens, r.mediaTok, r.mediaPer, r.capped = max(billed, 0)-mediaTokens, mediaTokens, media, true
	return r
}

// mulDiv is a*b/c with a 128-bit product, truncated toward zero, for c > 0. Every ratio conversion uses it, so no
// product of two large counts can wrap (a body of about 4 GB). For a product that fits in int64 it gives the same
// figure as int64(a)*int64(b)/int64(c). A quotient outside int is clamped to the nearest int limit.
func mulDiv(a, b, c int) int {
	if uint64(a) <= math.MaxInt32 && uint64(b) <= math.MaxInt32 {
		return int(int64(a) * int64(b) / int64(c)) // the product is below 2^62: plain int64 math, the common case
	}
	return mulDivWide(a, b, c)
}

// mulDivWide is the 128-bit path of mulDiv.
func mulDivWide(a, b, c int) int {
	neg := (a < 0) != (b < 0)
	hi, lo := bits.Mul64(absU(a), absU(b))
	d := uint64(c)
	if hi >= d {
		return clampSigned(math.MaxUint64, neg)
	}
	q, _ := bits.Div64(hi, lo, d)
	return clampSigned(q, neg)
}

func absU(x int) uint64 {
	if x < 0 {
		return -uint64(x)
	}
	return uint64(x)
}

// clampSigned applies the sign to q, and clamps it to the range of int.
func clampSigned(q uint64, neg bool) int {
	if neg {
		if q > uint64(math.MaxInt)+1 {
			return math.MinInt
		}
		return int(-q)
	}
	if q > math.MaxInt {
		return math.MaxInt
	}
	return int(q)
}

// addSat is a+b, clamped to the range of int.
func addSat(a, b int) int {
	s := a + b
	if a > 0 && b > 0 && s < 0 {
		return math.MaxInt
	}
	if a < 0 && b < 0 && s >= 0 {
		return math.MinInt
	}
	return s
}

func (r ratio) of(bytes int) int {
	if r.bytes <= 0 {
		return 0
	}
	return mulDiv(bytes, r.tokens, r.bytes)
}

// mediaOf is the tokens of media bytes of base64 text.
func (r ratio) mediaOf(media int) int {
	if r.mediaPer <= 0 {
		return 0
	}
	return mulDiv(media, r.mediaTok, r.mediaPer)
}

// opaqueOf is the tokens of the opaque bytes of nt: the host's tokens for the counted bytes when the
// counts are used, and the fixed price for the rest.
func (r ratio) opaqueOf(nt nonText) int {
	fixed := nt.opaque
	tokens := 0
	if r.counts {
		fixed -= nt.counted
		tokens = nt.ctok
	}
	if r.opqPer <= 0 {
		return tokens
	}
	return addSat(tokens, mulDiv(max(fixed, 0), r.opqTok, r.opqPer))
}

// tokensOf converts the bytes of p: the text with the ratio, the media with the media price, and the
// opaque bytes with the opaque price.
func (r ratio) tokensOf(p parts) int {
	return addSat(addSat(r.of(p.bytes-p.media-p.opaque), r.mediaOf(p.media)), r.opaqueOf(p.nonText))
}

// sizeOf converts the bytes of a size whose non-text bytes are nt.
func (r ratio) sizeOf(bytes int, nt nonText) int { return r.tokensOf(parts{bytes, nt}) }

// upTo is the tokens of positions 0..i of b.
func (r ratio) upTo(b Body, i int) int { return r.tokensOf(b.between(-1, i)) }

// between is the tokens of positions from+1..to of b (the bytes of position from are not counted
// twice: the same difference as cumBytes(to) - cumBytes(from)).
func (r ratio) between(b Body, from, to int) int { return r.tokensOf(b.between(from, to)) }

// extent returns the last candidate position whose estimated prefix fits in tokens, or -1. The
// tolerance absorbs the error of the byte ratio and the read step of the provider (128 tokens for older
// OpenAI models, the default; Provider.ReadGranularity).
// With no candidate breakpoint every position is a candidate.
func extent(b Body, r ratio, tokens int, candidates []bp) int {
	return extentWith(b, r, tokens, candidates, Provider{})
}

// extentWith is extent with the read granularity of a provider profile.
func extentWith(b Body, r ratio, tokens int, candidates []bp, p Provider) int {
	if tokens <= 0 {
		return -1
	}
	limit := tokens + p.tolerance(tokens)
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

// Ratio is the request ratio that [Compute] used: the provider's billed tokens of the forwarded
// text, against its bytes (§5.3). A host that prices bytes of text outside a Result, such as a removed
// tool declaration, converts them with it. It wraps the one engine ratio: there is no second copy of
// the rule. The zero value converts every byte to 0 tokens.
type Ratio struct{ r ratio }

// NewRatio builds the ratio of a body that the provider billed for billed prompt tokens (fresh input
// plus cache read plus cache write). It is the ratio Compute builds for the request.
func NewRatio(billed int, forwarded Body) Ratio { return exportRatio(newRatio(billed, forwarded)) }

// exportRatio wraps the engine ratio for a host: the one constructor for NewRatio and Result.Ratio. A count above
// maxRatioCount gives a ratio that converts 0 and is capped, so Tokens cannot overflow for any Ratio. newRatio never
// returns a negative count, so there is no negative branch here (RatioFromText has one: its input is the host's).
func exportRatio(r ratio) Ratio {
	if r.tokens > maxRatioCount || r.bytes > maxRatioCount {
		return Ratio{ratio{capped: true}}
	}
	return Ratio{r}
}

// RatioFromText rebuilds a text ratio from the two counts that [Ratio.Text] returned and the flag that
// [Ratio.Capped] returned, for example from a stored row. Only the text conversion is rebuilt: media and
// opaque bytes are not priced by it.
//
// A count that is negative or above maxRatioCount (2^31-1, far above any request) is not a ratio: the result converts
// every byte to 0 tokens and is marked capped, so a figure that uses it is an estimate. This keeps the integer
// math of [Ratio.Tokens] inside int64.
func RatioFromText(tokens, bytes int, capped bool) Ratio {
	if tokens < 0 || bytes < 0 || tokens > maxRatioCount || bytes > maxRatioCount {
		return Ratio{ratio{capped: true}}
	}
	return Ratio{ratio{tokens: tokens, bytes: bytes, capped: capped}}
}

// maxRatioCount bounds the counts of every exported Ratio (NewRatio, RatioFromText and Result.Ratio) and the bytes
// Tokens converts, so tokens x bytes fits in int64.
const maxRatioCount = math.MaxInt32

// Tokens converts bytes of text to tokens with the request ratio, with the same integer math as Compute.
// It is 0 when the request had no text bytes.
func (x Ratio) Tokens(textBytes int) int {
	if textBytes < 0 || textBytes > maxRatioCount {
		return 0 // outside any real body: not converted, and no overflow
	}
	return x.r.of(textBytes)
}

// Text returns the tokens and the bytes of the text of the forwarded body, after the media and the
// opaque bytes were priced apart.
func (x Ratio) Text() (tokens, bytes int) { return x.r.tokens, x.r.bytes }

// Capped says that the media price was above the bill and was cut to fit it (§5.3). The text ratio is
// then at its floor, so a figure that uses it is the lowest it can be.
func (x Ratio) Capped() bool { return x.r.capped }

// InRange says whether the text ratio is inside the limits of check 9 (§6.5): from 0.05 to 2.0 tokens a byte. A
// host that builds a Ratio before Compute (with [NewRatio]) uses it to keep only a ratio that Compute would
// accept. Compute sets [Result].Ratio only for such a ratio, so a Result's ratio is always in range.
func (x Ratio) InRange() bool {
	t, n := float64(x.r.tokens), float64(x.r.bytes)
	return t > 0 && n > 0 && t >= minTokensPerByte*n && t <= maxTokensPerByte*n
}
