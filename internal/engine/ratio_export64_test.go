//go:build amd64 || arm64

package engine

import "testing"

// Result.Ratio obeys the same bound as NewRatio: a body above it, with a bill that passes check 9, gives no ratio
// and nothing that can overflow. The engine's own ratio is in range there; the exported one is not. A 64-bit test:
// a 32-bit int cannot hold the body size.
func TestResultRatioObeysTheBound(t *testing.T) {
	const big = 4_000_000_000 // two positions of 2e9 bytes (PosHash.Bytes is an int32)
	b := Body{Start: [3]int{0, 0, 0}, Pos: []PosHash{{Hash: 1, Bytes: 2_000_000_000}, {Hash: 2, Bytes: 2_000_000_000}}}
	if r := newRatio(big, b); r.bytes != big || r.tokens != big {
		t.Fatalf("engine ratio = %+v, want %d over %d", r, big, big)
	}
	if r := NewRatio(big, b); r.InRange() || r.Tokens(1000) != 0 || !r.Capped() {
		t.Fatalf("NewRatio above the bound = %+v, want out of range, converts 0, capped", r)
	}
	res := req{at: t0, client: b, fwd: b, facts: exact5m, usage: Usage{Input: big}}.run(NewTrackers())
	if res.Ratio != nil {
		t.Fatalf("Result.Ratio = %+v above the bound, want nil", res.Ratio)
	}
}
