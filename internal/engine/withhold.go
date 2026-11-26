package engine

// The reasons [Withhold] returns. A host shows them as they are, so every host names a gate the same way.
const (
	WithheldCheckSum          = "check_sum"           // the sum check failed
	WithheldCheckUniqueChange = "check_unique_change" // two operations share a ChangeID
	WithheldCheckSpans        = "check_spans"         // an operation's spans do not add up to its sizes
	WithheldCheckOther        = "check_other"         // a fatal check with no reason of its own
	WithheldUnpriced          = "unpriced"            // an operation or the request is unpriced, for a reason other than penalty_unknown
	WithheldSizeUnknown       = "size_unknown"        // the operations saved tokens, and the size before them is unknown
	WithheldImplausibleSize   = "implausible_size"    // the operations saved more tokens than the request had
)

// SizeNotGiven is the tokensBefore of a host that has no measure of its own of the request size.
// [Withhold] then skips the size gate.
const SizeNotGiven = -1

// Withhold returns the first gate that res fails, or "" when a host may book its savings (design §6.7).
// A host books no saving for a request with a reason: no saved or caused tokens of an operation, no cache
// effect and no interaction. The calls of its spends are real cost, so the host still books them.
//
// The gates, in order:
//
//  1. A fatal check ([CheckFailure.Fatal]). The list of operations or its attribution is wrong.
//  2. An entry in res.Unpriced with a reason other than [ReasonPenaltyUnknown]. penalty_unknown does not
//     gate: every operation is priced, and the figure is an estimate (§6.4).
//  3. The size bound. A request cannot remove more tokens than it had. tokensBefore is the prompt size,
//     in tokens, of the body the client sent, by the host's own measure. The library cannot give it:
//     its own size without the operations is the billed size plus the saved tokens, so a bound on it
//     never fails. The bound counts the Saved tokens of each operation that is not DryRun and not a
//     spend. It is tested only when they are above 0. tokensBefore 0 or less gives size_unknown, and
//     [SizeNotGiven] skips the gate.
func Withhold(res Result, tokensBefore int) string {
	for _, c := range res.Checks {
		if !c.Fatal() {
			continue
		}
		switch c.Check {
		case CheckSum:
			return WithheldCheckSum
		case CheckUniqueChange:
			return WithheldCheckUniqueChange
		case CheckSpans:
			return WithheldCheckSpans
		}
		return WithheldCheckOther
	}
	for _, u := range res.Unpriced {
		if u.Reason != ReasonPenaltyUnknown {
			return WithheldUnpriced
		}
	}
	if tokensBefore == SizeNotGiven {
		return ""
	}
	saved := 0
	for _, op := range res.Ops {
		if op.DryRun || op.Kind == KindSpend {
			continue
		}
		saved += op.Saved.Input + op.Saved.CacheWrite5m + op.Saved.CacheWrite1h + op.Saved.CacheRead
	}
	switch {
	case saved <= 0:
		return ""
	case tokensBefore <= 0:
		return WithheldSizeUnknown
	case saved > tokensBefore:
		return WithheldImplausibleSize
	}
	return ""
}
