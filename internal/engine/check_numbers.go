package engine

import "fmt"

// The constructors of the failures that carry numbers (§6.5; docs/events/events.md §6.8.1). Each
// one sets the typed fields first and builds Detail from them, so the string and the fields cannot
// drift. Some numbers are in Detail only: they have no field (§6.8.1 lists them).

func intp(n int) *int { return &n }

func sumFailure(s Section, want, got int) CheckFailure {
	f := CheckFailure{Check: CheckSum, Section: s, Want: intp(want), Got: intp(got)}
	f.Detail = fmt.Sprintf("section=%s ops=%d bodies=%d bytes", f.Section, *f.Got, *f.Want)
	return f
}

// spansFailure: want is the span bytes, got the operation's bytes, each before and after.
func spansFailure(operator string, want, wantAfter, got, gotAfter int) CheckFailure {
	f := CheckFailure{Check: CheckSpans, Operator: operator,
		Want: intp(want), WantAfter: intp(wantAfter), Got: intp(got), GotAfter: intp(gotAfter)}
	f.Detail = fmt.Sprintf("spans=%d/%d op=%d/%d bytes", *f.Want, *f.WantAfter, *f.Got, *f.GotAfter)
	return f
}

// conservationFailure: want is the bill difference, got the tokens the operations claim.
func conservationFailure(want, got int) CheckFailure {
	f := CheckFailure{Check: CheckConservation, Want: intp(want), Got: intp(got)}
	f.Detail = fmt.Sprintf("bill difference=%d ops=%d tokens", *f.Want, *f.Got)
	return f
}

// sizeRangeFailure: got is the operation's bytes. The limit is a rule, not a claim, so it has its own
// field and the failure has no Want.
func sizeRangeFailure(operator string, got, gotAfter, limit, limitAfter int) CheckFailure {
	f := CheckFailure{Check: CheckSizeRange, Operator: operator,
		Got: intp(got), GotAfter: intp(gotAfter), Limit: intp(limit), LimitAfter: intp(limitAfter)}
	f.Detail = fmt.Sprintf("size bytes=%d/%d limits=%d/%d: the operation is not priced", *f.Got, *f.GotAfter, *f.Limit, *f.LimitAfter)
	return f
}

// sizeTokensFailure has two units and no difference, so its numbers are not in Want and Got.
func sizeTokensFailure(operator string, tokens, tokensAfter, bytes, bytesAfter int) CheckFailure {
	lo, hi := minTokensPerByte, maxTokensPerByte
	f := CheckFailure{Check: CheckSizeTokens, Operator: operator,
		Tokens: intp(tokens), TokensAfter: intp(tokensAfter), Bytes: intp(bytes), BytesAfter: intp(bytesAfter),
		RatioMin: &lo, RatioMax: &hi}
	f.Detail = fmt.Sprintf("tokens=%d/%d bytes=%d/%d: outside %g..%g tokens a byte, bytes used",
		*f.Tokens, *f.TokensAfter, *f.Bytes, *f.BytesAfter, *f.RatioMin, *f.RatioMax)
	return f
}

// mediaTokensFailure: want is the tokens billed, got the media price. The media tokens after the cap
// are in Detail only.
func mediaTokensFailure(billed, media, capped int, note string) CheckFailure {
	f := CheckFailure{Check: CheckMediaTokens, Want: intp(billed), Got: intp(media)}
	f.Detail = fmt.Sprintf("media price=%d tokens billed=%d: the bill is below the media price: the media is cut to %d tokens: %s",
		*f.Got, *f.Want, capped, note)
	return f
}

// opaqueTokensFailure: want is the tokens billed, got the tokens counted for the opaque bytes. The
// counted and the total opaque bytes are in Detail only.
func opaqueTokensFailure(billed, counts, counted, opaque int) CheckFailure {
	f := CheckFailure{Check: CheckOpaqueTokens, Want: intp(billed), Got: intp(counts)}
	f.Detail = fmt.Sprintf("opaque counts=%d tokens for %d of %d opaque bytes, tokens billed=%d: the counts are above what the bill leaves: the fixed price is used",
		*f.Got, counted, opaque, *f.Want)
	return f
}

func sharedWarmFailure(w Position) CheckFailure {
	f := CheckFailure{Check: CheckSharedWarm, Section: w.Section, Index: intp(w.Index)}
	f.Detail = fmt.Sprintf("section=%q index=%d is not in the forwarded body", f.Section, *f.Index)
	return f
}
