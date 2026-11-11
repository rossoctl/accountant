package engine

// Sibling is another thread of the same session: the body it last forwarded and the breakpoints it
// sent with it. The host keeps one for each thread it has seen inside the TTL (R54). It holds
// hashes and sizes only (R2).
type Sibling struct {
	Body        Body
	Breakpoints []Breakpoint
}

// SharedWarmFrom returns the value of EntryFacts.SharedWarm for a request that forwarded fwd: the
// last position of fwd that a sibling kept warm, or nil when none did (R54).
//
// The host decides which siblings count (inside the TTL, another thread, not later than this
// request). This function decides only where the shared prefix ends. The provider caches at
// breakpoints, so a prefix counts as warm up to the last breakpoint the sibling sent inside the
// prefix both bodies share. A shared prefix with no breakpoint of the sibling in it was not kept
// warm. A sibling whose body has no breakpoint (automatic prefix caching, for example OpenAI) is the
// exception: with no breakpoint every position counts as one (design §5.1, §6.2), so the whole shared
// prefix was warm. With many siblings, the one that reaches furthest wins.
//
// It is pure: it reads the bodies and changes nothing.
func SharedWarmFrom(fwd Body, siblings []Sibling) *Position {
	best := -1
	for _, s := range siblings {
		common := firstDiff(s.Body, fwd) - 1 // the last index at which the two bodies agree
		bps := s.Body.breakpoints(s.Breakpoints)
		if len(bps) == 0 { // no breakpoint in the body: every position is one
			best = max(best, common)
			continue
		}
		for _, b := range bps {
			if b.at <= common && b.at > best {
				best = b.at
			}
		}
	}
	if best < 0 {
		return nil
	}
	p := fwd.position(best)
	return &p
}
