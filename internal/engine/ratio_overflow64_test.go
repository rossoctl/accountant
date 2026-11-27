//go:build amd64 || arm64

package engine

import (
	"math"
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

// mulDiv equals int64 math wherever int64 math does not wrap, and clamps where the quotient does not fit.
func TestMulDivMatchesInt64Math(t *testing.T) {
	vals := []int{0, 1, 2, 3, 7, 250, 1000, 123456789, math.MaxInt32 - 1, math.MaxInt32, math.MaxInt32 + 1, 3_000_000_000}
	for _, a := range vals {
		for _, b := range vals {
			for _, c := range vals[1:] {
				want := new64(a, b, c)
				if want < 0 {
					continue // the int64 product wrapped: that is the defect
				}
				if got := mulDiv(a, b, c); got != want {
					t.Fatalf("mulDiv(%d,%d,%d) = %d, want %d", a, b, c, got, want)
				}
			}
		}
	}
	// The fast path ends at MaxInt32: the largest product it takes is below 2^62.
	if got, want := mulDiv(math.MaxInt32, math.MaxInt32, 1), math.MaxInt32*math.MaxInt32; got != want {
		t.Fatalf("mulDiv at the fast-path limit = %d, want %d", got, want)
	}
	if got := mulDiv(-7, 3, 2); got != -10 {
		t.Fatalf("mulDiv(-7,3,2) = %d, want -10 (truncated toward zero)", got)
	}
	if got := mulDiv(math.MaxInt, math.MaxInt, 1); got != math.MaxInt {
		t.Fatalf("mulDiv above int = %d, want clamp to MaxInt", got)
	}
	if got := mulDiv(math.MinInt, 2, 1); got != math.MinInt {
		t.Fatalf("mulDiv below int = %d, want clamp to MinInt", got)
	}
	if got := addSat(math.MaxInt, 1); got != math.MaxInt {
		t.Fatalf("addSat = %d, want MaxInt", got)
	}
}

// new64 is the old formula: the product wraps for a body of about 4 GB.
func new64(a, b, c int) int {
	p := int64(a) * int64(b)
	if int64(a) != 0 && p/int64(a) != int64(b) {
		return -1
	}
	return int(p / int64(c))
}
