package engine

import "testing"

// BenchmarkRatioOf measures one text conversion, the call that Compute, extent and the fingerprint walk make on
// every body range.
// benchRatio is a variable, so the compiler cannot fold the ratio into the loop, as in Compute.
var benchRatio = ratio{tokens: 90_000, bytes: 360_000}

func BenchmarkRatioOf(b *testing.B) {
	r := benchRatio
	sink := 0
	for i := 0; i < b.N; i++ {
		sink += r.of(i & 0xfffff)
	}
	_ = sink
}
