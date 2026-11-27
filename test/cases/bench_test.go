package cases

import (
	"testing"

	"github.com/rossoctl/accountant"
)

// BenchmarkComputeAllCases replays every case fixture through Compute, as TestComputeMeetsEveryCase does. It
// measures the whole path that the ratio conversion is on.
func BenchmarkComputeAllCases(b *testing.B) {
	all := All()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, c := range all {
			ts := accountant.NewTrackers()
			for _, s := range c.Steps {
				if s.IsStandalone() {
					accountant.ComputeStandalone(*s.Standalone)
					continue
				}
				r := ts.BeginRequest(s.Begin.Meta, s.Begin.Client, s.Begin.ClientBreakpoints)
				for _, op := range s.Add {
					r.Add(op)
				}
				r.EndRequest(s.End.Thread, s.End.Forwarded, s.End.Breakpoints, s.End.Facts.EntryFacts())
				r.Compute(s.Usage)
			}
		}
	}
}
