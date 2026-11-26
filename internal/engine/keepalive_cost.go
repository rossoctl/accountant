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
}

// PingCost is the dollars of one keep-alive ping:
//
//	Prefix x read + FreshInput x input + Output x output
//
// Every rate is the rate at the prompt size of the ping, Prefix + FreshInput, so a ping near a price
// line gets the tier its request gets. A negative count counts as 0.
//
// A ping on a prefix with no known read rate (a rate of 0 or less) is unpriced: the result is 0 and
// estimated is true. The host reads 0 with estimated true as "unknown", never as free. A rate that the
// pricer marks as an estimate makes estimated true. A tier with no tokens makes no price call.
func PingCost(in PingInput, p SizedPricer) (usd float64, estimated bool) {
	prefix, fresh, out := max(in.Prefix, 0), max(in.FreshInput, 0), max(in.Output, 0)
	size := prefix + fresh
	read, est := p.PriceAt(in.Model, in.At, TierCacheRead, size)
	estimated = est
	if read <= 0 {
		return 0, true
	}
	usd = float64(prefix) * read
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
	// PromptTokens is the prompt size that picks the price tier. 0 means Prefix.
	PromptTokens int64
	// OneHour says that the entry was written at the 1-hour tier, so its rewrite is too.
	OneHour bool
}

// MissCost is the extra dollars of one cache miss over a hit: the prefix at the write rate minus the
// prefix at the read rate. It is never negative. The write rate is the 5-minute rate, or the 1-hour rate
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
