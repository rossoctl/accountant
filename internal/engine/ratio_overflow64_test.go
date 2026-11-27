//go:build amd64 || arm64

package engine

import (
	"strings"
	"testing"
)

// A body of about 4 GB made the internal ratio multiply two counts of 4e9 in int64, and MessageTokens wrapped. The
// ratio now multiplies in 128 bits. A 64-bit test: a 32-bit int cannot hold the body size.
func TestMessageTokensDoesNotWrapAboveTheBound(t *testing.T) {
	const big = 4_000_000_000 // two positions of 2e9 bytes (PosHash.Bytes is an int32)
	b := Body{Start: [3]int{0, 0, 0}, Pos: []PosHash{{Hash: 1, Bytes: 2_000_000_000}, {Hash: 2, Bytes: 2_000_000_000}}}
	res := req{at: t0, client: b, fwd: b, facts: exact5m, usage: Usage{Input: big}}.run(NewTrackers())
	if res.MessageTokens != big {
		t.Fatalf("MessageTokens = %d, want %d (1 token a byte)", res.MessageTokens, big)
	}
	for _, c := range res.Checks {
		if c.Check == CheckRatioRange {
			t.Fatalf("check 9 failed for a ratio of 1 token a byte: %+v", c)
		}
	}
}

// rangeMedia scales the counted opaque bytes of a body by the share a size holds. A body of 3.2e9 counted opaque bytes
// made that product wrap in int64. PosHash.Opaque is an int32, so two positions hold the 3.2e9.
func TestRangeMediaDoesNotWrapAboveTheBound(t *testing.T) {
	const half, tokHalf = 1_600_000_000, 1_500_000_000
	pos := PosHash{Hash: 1, Bytes: half, Opaque: half, OpaqueCounted: half, OpaqueTokens: tokHalf}
	pos2 := pos
	pos2.Hash = 2
	body := Body{Pos: []PosHash{pos, pos2}}.withPrefix()
	c := &calcState{client: body, fwd: body}
	sp := span{from: msg(0), to: msg(1)}
	nt := c.rangeMedia(0, Size{Bytes: 2 * half}, sp, true)
	if nt.counted != 2*half || nt.ctok != 2*tokHalf {
		t.Fatalf("rangeMedia = counted %d, ctok %d, want %d and %d", nt.counted, nt.ctok, 2*half, 2*tokHalf)
	}
	// A size that holds half of the opaque bytes gets half of the counts.
	nt = c.rangeMedia(0, Size{Bytes: half}, sp, true)
	if nt.counted != half || nt.ctok != tokHalf {
		t.Fatalf("rangeMedia, half = counted %d, ctok %d, want %d and %d", nt.counted, nt.ctok, half, tokHalf)
	}
}

// SplitOpaqueTokens takes its tokens from the host. A count of 2^60 times a length of 300 wrapped in int64.
func TestSplitOpaqueTokensDoesNotWrapAboveTheBound(t *testing.T) {
	const tokens = 1 << 60
	got := SplitOpaqueTokens([]string{strings.Repeat("x", 300), strings.Repeat("x", 100)}, tokens)
	if len(got) != 2 || got[0] != tokens/4*3 || got[1] != tokens/4 {
		t.Fatalf("SplitOpaqueTokens = %v, want %d and %d", got, tokens/4*3, tokens/4)
	}
}
