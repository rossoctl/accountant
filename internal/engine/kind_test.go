package engine

import "testing"

// Every priced op carries the Kind of the operation it came from, so a consumer needs no join by OpID.
func TestPricedOpCarriesKind(t *testing.T) {
	ops := RequestOps{Ops: []Operation{{ID: "s", Kind: KindSpend, Call: &ModelCall{Model: "m",
		Usage: CallUsage{Usage: Usage{Input: 10}, Output: 5}}}}}
	res := ComputeStandalone(ops)
	if len(res.Ops) != 1 || res.Ops[0].Kind != KindSpend {
		t.Fatalf("Ops=%+v, want one op of kind spend", res.Ops)
	}
}
