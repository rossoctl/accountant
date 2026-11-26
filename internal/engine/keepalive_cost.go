package engine

import "time"

// PingInput is one keep-alive ping: a cache read of the prefix, the fresh input the ping carries past
// the prefix, and the output it bills. The host owns the shape of its ping body, so the host gives
// FreshInput and Output. The library only prices them.
type PingInput struct {
	Model string
	At    time.Time
	// Prefix is the cached tokens the ping reads.
	Prefix int64
	// FreshInput is the fresh input tokens the ping sends past the prefix.
	FreshInput int64
	// Output is the output tokens the ping bills.
	Output int64
	// PromptTokens is the prompt size that picks the price tier. 0 or less means Prefix + FreshInput. A host
	// that prices the read of a prefix inside a larger request gives that request's size.
	PromptTokens int64
}

// PingCost is the dollars of one keep-alive ping:
//
//	Prefix x read + FreshInput x input + Output x output
//
// Every rate is the rate at the prompt size of the ping, PromptTokens, or Prefix + FreshInput when it is 0 or less, so a
// ping near a price line gets the tier its request gets. A negative count counts as 0.
//
// A rate of 0 is a free rate, as in [SizedPricer]: it adds 0. A model with no rates gives 0 with
// estimated true, because the pricer must mark an unknown model as an estimate. The host reads 0 with
// estimated true as "unknown", never as free. A rate that the pricer marks as an estimate makes
// estimated true. A tier with no tokens makes no price call, so it cannot mark the cost estimated.
func PingCost(in PingInput, p SizedPricer) (usd float64, estimated bool) {
	prefix, fresh, out := max(in.Prefix, 0), max(in.FreshInput, 0), max(in.Output, 0)
	size := in.PromptTokens
	if size <= 0 {
		size = prefix + fresh
	}
	if prefix > 0 {
		read, est := p.PriceAt(in.Model, in.At, TierCacheRead, size)
		estimated = est
		usd = float64(prefix) * read
	}
	if fresh > 0 {
		r, e := p.PriceAt(in.Model, in.At, TierInput, size)
		usd += float64(fresh) * r
		estimated = estimated || e
	}
	if out > 0 {
		r, e := p.PriceAt(in.Model, in.At, TierOutput, size)
		usd += float64(out) * r
		estimated = estimated || e
	}
	return usd, estimated
}

// MissInput is one cache miss on a prefix.
type MissInput struct {
	Model string
	At    time.Time
	// Prefix is the cached tokens that the miss writes again.
	Prefix int64
	// PromptTokens is the prompt size that picks the price tier. 0 or less means Prefix.
	PromptTokens int64
	// OneHour says that the entry was written at the 1-hour tier, so its rewrite is too.
	OneHour bool
}

// MissCost is the extra dollars of one cache miss over a hit: the prefix at the write rate minus the
// prefix at the read rate. It is never negative. When PromptTokens is 0 or less, the tier is the one of a prompt
// of Prefix tokens. The write rate is the 5-minute rate, or the 1-hour rate
// when OneHour is set. Both rates are the rates at PromptTokens.
//
// A write rate of 0 or less is unknown: the result is 0 and estimated is true. A write rate that is
// not above the read rate gives 0, a miss that costs nothing extra.
func MissCost(in MissInput, p SizedPricer) (usd float64, estimated bool) {
	prefix := in.Prefix
	if prefix <= 0 {
		return 0, false
	}
	size := in.PromptTokens
	if size <= 0 {
		size = prefix
	}
	tier := TierCacheWrite5m
	if in.OneHour {
		tier = TierCacheWrite1h
	}
	write, we := p.PriceAt(in.Model, in.At, tier, size)
	if write <= 0 {
		return 0, true
	}
	read, re := p.PriceAt(in.Model, in.At, TierCacheRead, size)
	return max(0, float64(prefix)*(write-read)), we || re
}

// AvoidedMissesUSD is the dollars saved when n cache misses do not happen: n x missUSD. It is never
// negative, so a bad miss figure or a bad count books no loss.
func AvoidedMissesUSD(n int64, missUSD float64) float64 {
	if n <= 0 || missUSD <= 0 {
		return 0
	}
	return float64(n) * missUSD
}

// RequestCostInput is the billed usage of one request, for [RequestCost].
type RequestCostInput struct {
	Model string
	At    time.Time
	// Billed is the usage the provider billed, as in [FigureInput]: CacheWrite counts every lifetime, and
	// CacheWrite1h is the part of it billed at the 1-hour tier.
	Billed Usage
	// Output is the output tokens the provider billed.
	Output int
	// PromptTokens is the prompt size that picks the price tier. 0 or less means Input + CacheRead +
	// CacheWrite, the size the provider counts.
	PromptTokens int64
}

// RequestCost is the dollars of one billed request:
//
//	Input x input + CacheRead x read + (CacheWrite - CacheWrite1h) x write5m + CacheWrite1h x write1h + Output x output
//
// Every rate is the rate at PromptTokens. A tier with no tokens makes no price call, so it cannot mark the
// cost estimated. A negative count counts as 0. A model that the pricer does not know gives the pricer's
// dollars and estimated true. It uses the same token pricer as [Figure].
func RequestCost(in RequestCostInput, p SizedPricer) (usd float64, estimated bool) {
	u := in.Billed
	size := in.PromptTokens
	if size <= 0 {
		size = int64(max(u.Input, 0)) + int64(max(u.CacheRead, 0)) + int64(max(u.CacheWrite, 0))
	}
	fp := &figurePricer{p: p, model: in.Model, at: in.At}
	w1h := min(max(u.CacheWrite1h, 0), max(u.CacheWrite, 0))
	usd, estimated = fp.usd(TierTokens{Input: max(u.Input, 0), CacheRead: max(u.CacheRead, 0),
		CacheWrite5m: max(u.CacheWrite, 0) - w1h, CacheWrite1h: w1h}, size)
	if in.Output > 0 {
		r, e := fp.rate(TierOutput, size)
		usd += r * float64(in.Output)
		estimated = estimated || e
	}
	return usd, estimated
}

// LatePingInput is a keep-alive ping that fires after its entry expired. It writes the prefix again, it
// does not read it.
type LatePingInput struct {
	PingInput
	// OneHour says that the entry was written at the 1-hour tier, so its rewrite is too.
	OneHour bool
}

// LatePingCost is the dollars of a ping that arrives too late:
//
//	Prefix x write + FreshInput x input + Output x output
//
// The write rate is the 5-minute rate, or the 1-hour rate when OneHour is set. Every rate is at
// PromptTokens, or at Prefix + FreshInput when it is 0 or less. A negative count counts as 0.
func LatePingCost(in LatePingInput, p SizedPricer) (usd float64, estimated bool) {
	prefix, fresh := max(in.Prefix, 0), max(in.FreshInput, 0)
	size := in.PromptTokens
	if size <= 0 {
		size = prefix + fresh
	}
	b := Usage{Input: int(fresh), CacheWrite: int(prefix)}
	if in.OneHour {
		b.CacheWrite1h = int(prefix)
	}
	return RequestCost(RequestCostInput{Model: in.Model, At: in.At, Billed: b, Output: int(max(in.Output, 0)), PromptTokens: size}, p)
}
