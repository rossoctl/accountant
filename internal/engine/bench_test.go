package engine

import (
	"testing"
	"time"
)

// BenchmarkExtentNoBreakpoints measures extent with no candidate breakpoint: every position is a
// candidate.
func BenchmarkExtentNoBreakpoints(b *testing.B) {
	for _, n := range []int{1000, 4000, 8000} {
		body := mk(nil, nil, msgs(n, 50))
		r := ratio{tokens: n * 50, bytes: n * 50}
		b.Run(itoa(n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				extent(body, r, n*25, nil)
			}
		})
	}
}

// BenchmarkComputeActiveChanges measures one request that replays many active changes.
func BenchmarkComputeActiveChanges(b *testing.B) {
	const n = 2000
	client := mk(nil, nil, msgs(n+10, 100))
	ts := NewTrackers()
	var ops []Operation
	for i := 0; i < n; i++ {
		ops = append(ops, rewrite("op"+itoa(i), "bench", "c"+itoa(i), msg(i), 100, 50))
	}
	first := req{at: t0, client: client, clientBPs: bps5(msg(n)), ops: ops, facts: exact5m,
		usage: Usage{CacheWrite: 100000}}
	first.run(ts)
	next := req{at: t0.Add(10 * time.Second), client: client, clientBPs: bps5(msg(n)), facts: exact5m,
		usage: Usage{CacheRead: 100000}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		next.begin(ts).Preview(next.usage)
	}
}
