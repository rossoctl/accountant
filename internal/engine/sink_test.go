package engine

import "testing"

type countSink struct{ n int }

func (c *countSink) Observe(Observation) { c.n++ }

type panicSink struct{}

func (panicSink) Observe(Observation) { panic("boom") }

func TestMultiCallsEverySinkInOrder(t *testing.T) {
	a, b := &countSink{}, &countSink{}
	Multi(a, nil, b).Observe(Observation{})
	if a.n != 1 || b.n != 1 {
		t.Fatalf("calls: a=%d b=%d, want 1 and 1", a.n, b.n)
	}
}

func TestMultiSurvivesAPanickingSink(t *testing.T) {
	b := &countSink{}
	Multi(panicSink{}, b).Observe(Observation{})
	if b.n != 1 {
		t.Fatalf("the sink after a panic ran %d times, want 1", b.n)
	}
}

func TestMultiOfNothingIsSafe(t *testing.T) {
	Multi().Observe(Observation{})
	Multi(nil).Observe(Observation{})
}

// Every priced op carries the Kind of the operation it came from, so a sink needs no join by OpID.
func TestPricedOpCarriesKind(t *testing.T) {
	ops := RequestOps{Ops: []Operation{{ID: "s", Kind: KindSpend, Call: &ModelCall{Model: "m",
		Usage: CallUsage{Usage: Usage{Input: 10}, Output: 5}}}}}
	res := ComputeStandalone(ops)
	if len(res.Ops) != 1 || res.Ops[0].Kind != KindSpend {
		t.Fatalf("Ops=%+v, want one op of kind spend", res.Ops)
	}
}
