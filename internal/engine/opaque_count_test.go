package engine

import (
	"strings"
	"testing"
)

// Issue 77: the host gives each thinking block its own token count (design D16). The counted bytes cost
// the host's tokens; the rest of the opaque bytes keep the fixed price.

// counts is a host table keyed by OpaqueKey.
func counts(m map[string]int) OpaqueCount {
	t := map[uint64]int{}
	for s, n := range m {
		t[OpaqueKey(s)] = n
	}
	return func(k uint64) (int, bool) { n, ok := t[k]; return n, ok }
}

func TestNewBodyCountedLooksUpEachBlock(t *testing.T) {
	a, b := strings.Repeat("A", 3000), strings.Repeat("B", 1400)
	p := []byte(`{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"` + a +
		`"},{"type":"redacted_thinking","data":"` + b + `"},{"type":"text","text":"ok"}]}`)
	plain := NewBody(nil, nil, [][]byte{p})

	both := NewBodyCounted(nil, nil, [][]byte{p}, counts(map[string]int{a: 1000, b: 300}))
	if g := both.Pos[0]; g.Opaque != 4400 || g.OpaqueCounted != 4400 || g.OpaqueTokens != 1300 {
		t.Fatalf("both counted: %+v, want Opaque 4400, OpaqueCounted 4400, OpaqueTokens 1300", g)
	}
	one := NewBodyCounted(nil, nil, [][]byte{p}, counts(map[string]int{b: 300}))
	if g := one.Pos[0]; g.Opaque != 4400 || g.OpaqueCounted != 1400 || g.OpaqueTokens != 300 {
		t.Fatalf("one counted: %+v, want OpaqueCounted 1400, OpaqueTokens 300", g)
	}
	if g := NewBodyCounted(nil, nil, [][]byte{p}, nil).Pos[0]; g != plain.Pos[0] {
		t.Fatalf("a nil count must be NewBody: %+v, want %+v", g, plain.Pos[0])
	}
	if both.Pos[0].Hash != plain.Pos[0].Hash || both.Pos[0].Bytes != plain.Pos[0].Bytes {
		t.Fatalf("the counts changed the hash or the size")
	}
	zero := NewBodyCounted(nil, nil, [][]byte{p}, counts(map[string]int{a: 0}))
	if g := zero.Pos[0]; g.OpaqueCounted != 3000 || g.OpaqueTokens != 0 {
		t.Fatalf("a count of 0 is a count: %+v", g)
	}
}

// The key is of the string as JSON decodes it, so an escape in the request body finds the count the host
// stored from the response.
func TestOpaqueKeyIsOfTheDecodedString(t *testing.T) {
	p := []byte(`{"type":"thinking","signature":"\u0061bcd"}`)
	b := NewBodyCounted(nil, nil, [][]byte{p}, counts(map[string]int{"abcd": 7}))
	if g := b.Pos[0]; g.OpaqueCounted != 4 || g.OpaqueTokens != 7 {
		t.Fatalf("escaped signature: %+v, want the count of \"abcd\"", g)
	}
	if OpaqueKey("abcd") == OpaqueKey("abce") {
		t.Fatalf("two strings with one key")
	}
}

func TestSplitOpaqueTokens(t *testing.T) {
	for _, c := range []struct {
		name   string
		sigs   []string
		tokens int
		want   []int
	}{
		{"one block", []string{"xxxx"}, 62, []int{62}},
		{"by length", []string{strings.Repeat("x", 300), strings.Repeat("x", 100)}, 400, []int{300, 100}},
		{"remainder to the largest fraction", []string{"xx", "x"}, 10, []int{7, 3}},
		{"tie to the earlier", []string{"x", "x"}, 3, []int{2, 1}},
		{"empty strings split evenly", []string{"", "", ""}, 4, []int{2, 1, 1}},
		{"no tokens", []string{"x", "y"}, 0, []int{0, 0}},
		{"negative tokens", []string{"x"}, -5, []int{0}},
		{"no strings", nil, 10, []int{}},
	} {
		got := SplitOpaqueTokens(c.sigs, c.tokens)
		if len(got) != len(c.want) {
			t.Fatalf("%s: %v, want %v", c.name, got, c.want)
		}
		sum := 0
		for i := range got {
			sum += got[i]
			if got[i] != c.want[i] {
				t.Errorf("%s: %v, want %v", c.name, got, c.want)
				break
			}
		}
		if c.tokens > 0 && len(c.sigs) > 0 && sum != c.tokens {
			t.Errorf("%s: parts add up to %d, want %d", c.name, sum, c.tokens)
		}
	}
}

// R = (bill - counts - fixed price of the uncounted bytes) / text bytes, and a range prices its counted
// bytes at the count.
func TestRatioUsesTheCounts(t *testing.T) {
	b := Body{Pos: []PosHash{
		{Hash: 1, Bytes: 4000},
		{Hash: 2, Bytes: 3000, Opaque: 3000, OpaqueCounted: 3000, OpaqueTokens: 1000},
		{Hash: 3, Bytes: 2500, Opaque: 2500}, // not counted: 10 tokens at the fixed price
	}}
	r := newRatio(1000+1000+10, b)
	if !r.counts || r.countsDropped || r.tokens != 1000 || r.bytes != 4000 {
		t.Fatalf("ratio = %+v, want counts used, text 1000 tokens over 4000 bytes", r)
	}
	if got := r.between(b, 0, 1); got != 1000 {
		t.Fatalf("the counted block prices at %d, want its count 1000", got)
	}
	if got := r.upTo(b, 2); got != 2010 {
		t.Fatalf("the whole body prices at %d, want the bill 2010", got)
	}
}

// Counts above the room the bill leaves are not used: the model strips thinking.
func TestRatioDropsCountsTheBillCannotHold(t *testing.T) {
	b := Body{Pos: []PosHash{
		{Hash: 1, Bytes: 4000},
		{Hash: 2, Bytes: 5000, Opaque: 5000, OpaqueCounted: 5000, OpaqueTokens: 2000},
	}}
	r := newRatio(1000+20, b) // the provider billed the fixed price, not the count
	if r.counts || !r.countsDropped || r.tokens != 1000 {
		t.Fatalf("ratio = %+v, want counts dropped, text 1000 tokens", r)
	}
	if got := r.between(b, 0, 1); got != 20 {
		t.Fatalf("the block prices at %d, want the fixed price 20", got)
	}
}

// A host-built body can hold a count larger than its opaque bytes, or a negative one. The library
// clamps the counted bytes to the opaque bytes and a negative count to 0.
func TestCountsAreClamped(t *testing.T) {
	p := PosHash{Bytes: 100, Opaque: 50, OpaqueCounted: 80, OpaqueTokens: -3}
	if p.opqCounted() != 50 || p.opqTokens() != 0 {
		t.Fatalf("counted %d tokens %d, want 50 and 0", p.opqCounted(), p.opqTokens())
	}
	if (PosHash{Bytes: 100, Opaque: 50, OpaqueTokens: 9}).opqTokens() != 0 {
		t.Fatalf("tokens with no counted bytes must be 0")
	}
}

// Size.OpaqueTokens sets the count of a range's opaque bytes, whatever the body says.
func TestSizeOpaqueTokensWins(t *testing.T) {
	r := ratio{tokens: 1, bytes: 4, mediaTok: 1, mediaPer: mediaBytesPerToken, opqTok: 1, opqPer: opaqueBytesPerToken, counts: true}
	nt := nonText{opaque: 3000}
	if got := r.sizeOf(3400, nt); got != 100+12 {
		t.Fatalf("no count: %d, want 100 text + 12 fixed", got)
	}
	nt.counted, nt.ctok = 3000, 1000
	if got := r.sizeOf(3400, nt); got != 1100 {
		t.Fatalf("counted: %d, want 100 text + 1000", got)
	}
	if (Size{Bytes: 1, OpaqueTokens: -1}).invalid() == false {
		t.Fatalf("a negative OpaqueTokens must be invalid")
	}
}

// A restored body keeps the counts, so a drop after a restart still saves the block's own count.
func TestSnapshotKeepsTheCounts(t *testing.T) {
	b := NewBodyCounted(nil, nil, [][]byte{[]byte(`{"type":"thinking","signature":"` + strings.Repeat("S", 900) + `"}`)},
		counts(map[string]int{strings.Repeat("S", 900): 77}))
	got, err := fromWireBody(toWireBody(b))
	if err != nil {
		t.Fatal(err)
	}
	if got.Pos[0] != b.Pos[0] || got.cumCountTokens(0) != 77 || got.cumCounted(0) != 900 {
		t.Fatalf("restored %+v, want %+v with its prefix sums", got.Pos[0], b.Pos[0])
	}
}
