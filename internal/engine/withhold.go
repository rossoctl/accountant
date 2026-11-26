package engine

// The reasons [Withhold] returns. A host shows them as they are, so every host names a gate the same way.
const (
	WithheldCheckSum          = "check_sum"           // the sum check failed
	WithheldCheckUniqueChange = "check_unique_change" // two operations share a ChangeID
	WithheldCheckSpans        = "check_spans"         // an operation's spans do not add up to its sizes
	WithheldCheckOther        = "check_other"         // a fatal check with no reason of its own
	WithheldUnpriced          = "unpriced"            // an operation, a cache effect or the request is unpriced, for a reason other than penalty_unknown
	WithheldSizeUnknown       = "size_unknown"        // the operations saved tokens, and the size before them is unknown
	WithheldImplausibleSize   = "implausible_size"    // the operations saved more tokens than the request had plus what they caused
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
//  1. A fatal check ([CheckFailure.Fatal]). The list of operations or its attribution is wrong. One
//     exception: a sum failure where no applied operation has a span in the section (Claims is 0).
//     There, a change that no operation reported changed the section (a host plugin that rewrites the
//     body and reports nothing, or a change the lost thread state no longer applies). The reported
//     operations of the other sections are still right, so their savings are booked. A sum failure in a
//     section that operations claim (Claims above 0) gates, also when their bytes cancel to Got 0: two
//     operations claim one change, a claim is false, or a reported and an unreported change share it.
//  2. An entry in res.Unpriced with a reason other than [ReasonPenaltyUnknown], or a cache effect of a
//     kind the library does not know ([Figure] lists it in USDFigure.Unpriced and books nothing for it).
//     penalty_unknown does not gate: every operation is priced, and the figure is an estimate (§6.4).
//  3. The size bound. An operation receives the body the operations before it left: the client body plus
//     what the inserting operations added (check 13). So the operations cannot save more message tokens than
//     the client messages had plus the message tokens they caused. The library measures the client messages
//     itself ([Result.MessageTokens]), so a host gives no size. The bound adds the SavedMessages and the
//     CausedMessages of each operation that is not DryRun and not a spend, in every tier. Tokens of a tool
//     declaration or of the system are not counted: they are not in the messages, and a deferral removes
//     thousands of them (context-guru#907). It is tested only when the saved message tokens are above 0.
//
// Legacy form: a host can give tokensBefore, the prompt size of the body the client sent by its own measure.
// The bound then counts the Saved and the Caused tokens of every operation, and tokensBefore of 0 or less
// gives size_unknown. [SizeNotGiven] skips the gate. Only the first size is read. New callers give no size.
func Withhold(res Result, tokensBefore ...int) string {
	for _, c := range res.Checks {
		if !c.Fatal() || unreportedChange(c) {
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
	for _, ce := range res.CacheEffects {
		if !knownEffect(ce.Kind) {
			return WithheldUnpriced
		}
	}
	if len(tokensBefore) == 0 {
		saved, caused := 0, 0
		for _, op := range res.Ops {
			if op.DryRun || op.Kind == KindSpend {
				continue
			}
			saved += tierSum(op.SavedMessages)
			caused += tierSum(op.CausedMessages)
		}
		if saved > res.MessageTokens+caused {
			return WithheldImplausibleSize
		}
		return ""
	}
	before := tokensBefore[0]
	if before == SizeNotGiven {
		return ""
	}
	saved, caused := 0, 0
	for _, op := range res.Ops {
		if op.DryRun || op.Kind == KindSpend {
			continue
		}
		saved += tierSum(op.Saved)
		caused += tierSum(op.Caused)
	}
	switch {
	case saved <= 0:
		return ""
	case before <= 0:
		return WithheldSizeUnknown
	case saved > before+caused:
		return WithheldImplausibleSize
	}
	return ""
}

// unreportedChange reports whether c is a sum failure of a section that no applied operation claims:
// only a change no operation reported moved its bytes (Withhold, gate 1). It reads Claims, not Got: Got
// is a net, and a false claim that cancels a real one gives Got 0 too.
func unreportedChange(c CheckFailure) bool {
	return c.Check == CheckSum && c.Claims != nil && *c.Claims == 0
}
