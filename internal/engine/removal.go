package engine

import (
	"math"
	"time"
)

// HeadTier is the tier the head of a request's prefix was billed at. Any cache read means the head was
// read. Else any cache write means the request created it, at the one-hour rate when CacheWrite1h is
// above 0 (CacheWrite1h is part of CacheWrite, never an addition to it). Else the head was fresh input.
//
// Why 1h on a mixed write: the provider's constraint is that longer lifetimes come before shorter ones in
// prompt order (tools, system, messages; docs/cached-prefix.md). So when any 1h tokens were written,
// the first breakpoint, which ends the head, is 1h. See docs/design.md §6.6.
//
// A change that takes tokens out of the head, such as a tool declaration, saves them at this tier.
func HeadTier(u Usage) Tier {
	switch {
	case u.CacheRead > 0:
		return TierCacheRead
	case u.CacheWrite > 0 && u.CacheWrite1h > 0:
		return TierCacheWrite1h
	case u.CacheWrite > 0:
		return TierCacheWrite5m
	}
	return TierInput
}

// RemovalInput is one change that took tokens out of the head of a request, and maybe put some back
// (a list of names that stands for the removed definitions).
type RemovalInput struct {
	Model string
	At    time.Time
	// Tier is the tier the head was billed at: [HeadTier] of the usage of the request sent.
	Tier Tier
	// Prompt is the billed size of the request sent: input plus cache read plus cache write. 0 means
	// the host does not know it.
	Prompt int64
	// Removed is the tokens the change took out, in the host's own tokenizer units.
	Removed int
	// Added is the tokens the change put back at the same tier. 0 for a plain removal.
	Added int
	// Factor is the host's tokenizer correction: the ratio of the provider's token count to the
	// host's. The figure applies it once, to Removed and to Added, in size and in dollars. 0 or less
	// means 1. The library knows no tokenizer, so the host gives it.
	Factor float64
	// Unsized says that the host has no prompt size. The pricer then gets size 0, the rate of a small
	// request, for both sides.
	Unsized bool
}

// RemovalFigure is [RemovalSaving] in money.
type RemovalFigure struct {
	// SavedUSD is the Removed tokens at the size without the change: Prompt plus the net tokens removed.
	SavedUSD float64
	// AddedUSD is the Added tokens at the billed size (the size of the request sent).
	AddedUSD float64
	// NetUSD is SavedUSD minus AddedUSD.
	NetUSD float64
	// PerTokenUSD is the price of one removed host token, the factor included, at the size without the
	// change. A host that splits a removal between names uses it. It does not depend on Removed, except
	// through the size.
	PerTokenUSD float64
	// Tier is the tier the tokens were priced at.
	Tier Tier
	// Estimated is true when a rate the pricer gave is an estimate.
	Estimated bool
	// Version is RulesVersion, the version of the rules that made the figure (R46). A host that records the
	// figure stores it as OpsVersion.
	Version int
}

// RulesVersion is the version of the accountant rules (R46). Result.Version and RemovalFigure.Version carry it.
const RulesVersion = 1

// RemovalSaving prices a change that took tokens out of the head of a request. It is the one formula for
// a tool declaration filter and for tool deferral, so a host never prices such a saving itself.
//
// The tokens are Removed times Factor. They are priced at the tier the head was billed at. The size
// that picks the rate is the billed size plus the net tokens removed, rounded up: the request without
// the change. Rounding up errs to the larger size, so a token at a price line is never put in the
// cheaper side. Added tokens price at the billed size, because the request sent carried them.
//
// It is deterministic and does no I/O. A model the pricer does not know gives rate 0 and Estimated.
func RemovalSaving(in RemovalInput, p SizedPricer) RemovalFigure {
	f := in.Factor
	if f <= 0 {
		f = 1
	}
	var billed, without int64
	if !in.Unsized {
		billed = max(in.Prompt, 0)
		without = max(billed+int64(math.Ceil(f*float64(in.Removed)-f*float64(in.Added))), 0)
	}
	out := RemovalFigure{Tier: in.Tier, Version: RulesVersion}
	rs, es := p.PriceAt(in.Model, in.At, in.Tier, without)
	out.SavedUSD = f * float64(in.Removed) * rs
	out.Estimated = es
	out.PerTokenUSD = f * rs
	if in.Added > 0 {
		ra, ea := p.PriceAt(in.Model, in.At, in.Tier, billed)
		out.AddedUSD = f * float64(in.Added) * ra
		out.Estimated = out.Estimated || ea
	}
	out.NetUSD = out.SavedUSD - out.AddedUSD
	return out
}
