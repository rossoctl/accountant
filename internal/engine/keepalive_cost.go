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
//
// Each product is rounded on its own, with an explicit float64 conversion, as in [RequestCost]: no CPU
// fuses a multiply and an add, so every cost function of this file gives the same bits on every CPU.
func PingCost(in PingInput, p SizedPricer) (usd float64, estimated bool) {
	prefix, fresh, out := max(in.Prefix, 0), max(in.FreshInput, 0), max(in.Output, 0)
	size := in.PromptTokens
	if size <= 0 {
		size = prefix + fresh
	}
	if prefix > 0 {
		read, est := p.PriceAt(in.Model, in.At, TierCacheRead, size)
		estimated = est
		usd = float64(float64(prefix) * read)
	}
	if fresh > 0 {
		r, e := p.PriceAt(in.Model, in.At, TierInput, size)
		usd += float64(float64(fresh) * r)
		estimated = estimated || e
	}
	if out > 0 {
		r, e := p.PriceAt(in.Model, in.At, TierOutput, size)
		usd += float64(float64(out) * r)
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
	// HeadTokens is the part of Prefix that sits at the 1-hour tier in a mixed entry (a head-TTL thread: a
	// 1-hour head, then a 5-minute tail). 0 or less means no head: the whole Prefix is one entry, as before.
	// A value above Prefix counts as Prefix. The tail is Prefix - HeadTokens.
	HeadTokens int64
	// HeadAlive says that the 1-hour head still lives. It counts only when HeadTokens is above 0.
	// While the head lives, a miss rewrites only the tail. After the head expires, a miss rewrites the head
	// at the 1-hour rate and the tail at the 5-minute rate. OneHour does not count when HeadTokens is above 0.
	HeadAlive bool
}

// MissCost is the extra dollars of one cache miss over a hit: the prefix at the write rate minus the
// prefix at the read rate. It is never negative. When PromptTokens is 0 or less, the tier is the one of a prompt
// of Prefix tokens. The write rate is the 5-minute rate, or the 1-hour rate
// when OneHour is set. Both rates are the rates at PromptTokens.
//
// A write rate of 0 or less is unknown: the result is 0 and estimated is true. A write rate that is
// not above the read rate gives 0, a miss that costs nothing extra.
//
// With HeadTokens above 0 the entry is mixed. While HeadAlive is set, the cost is the tail only:
// (Prefix - HeadTokens) x (write5m - read). Otherwise it is the tail at that rate plus the head at the
// 1-hour rate: HeadTokens x (write1h - read). Each product is rounded on its own, and the head is added
// last. A write rate of 0 or less for a tier that the cost needs is unknown: the result is 0 and estimated is
// true. A mixed entry with no tail costs 0 while the head lives, and makes no price call.
func MissCost(in MissInput, p SizedPricer) (usd float64, estimated bool) {
	prefix := in.Prefix
	if prefix <= 0 {
		return 0, false
	}
	size := in.PromptTokens
	if size <= 0 {
		size = prefix
	}
	if in.HeadTokens > 0 {
		head := min(in.HeadTokens, prefix)
		tail := prefix - head
		if tail == 0 && in.HeadAlive {
			return 0, false // nothing is rewritten, so no rate is needed
		}
		read, re := p.PriceAt(in.Model, in.At, TierCacheRead, size)
		estimated = re
		if tail > 0 {
			w5, e := p.PriceAt(in.Model, in.At, TierCacheWrite5m, size)
			if w5 <= 0 {
				return 0, true
			}
			usd = max(0, float64(float64(tail)*(w5-read)))
			estimated = estimated || e
		}
		if !in.HeadAlive {
			w1, e := p.PriceAt(in.Model, in.At, TierCacheWrite1h, size)
			if w1 <= 0 {
				return 0, true
			}
			usd += max(0, float64(float64(head)*(w1-read)))
			estimated = estimated || e
		}
		return usd, estimated
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

// RequestCost is the dollars of one billed request, and it gives the same bits as the bill that a host
// stores for the request:
//
//	Input x input + CacheRead x read + CacheWrite x write5m + Output x output + CacheWrite1h x premium
//
// premium is the 1-hour write rate minus the 5-minute write rate, and only when it is above 0. So every
// write token is priced at the 5-minute rate, and a token billed at the 1-hour tier pays the premium on
// top. A 1-hour rate below the 5-minute rate has no premium: those tokens stay at the 5-minute rate. That is
// the rule of the stored bill. Every rate is the rate at PromptTokens. When it is 0 or less, it is the rate
// at the prompt Input + CacheRead + CacheWrite.
//
// Each product is rounded on its own, with an explicit float64 conversion, and the terms are added in the
// order above. A CPU that fuses a multiply and an add (FMA, as arm64 does) would otherwise give another
// last bit for the same request, so a host's stored bill and this figure would differ by one unit in the last
// place on some requests (#1128 of the host).
//
// A tier with no tokens makes no price call, so it cannot mark the cost estimated. A negative count counts
// as 0. CacheWrite1h counts only as far as CacheWrite: a larger value is clamped to CacheWrite. A model that
// the pricer does not know gives the pricer's dollars and estimated true.
func RequestCost(in RequestCostInput, p SizedPricer) (usd float64, estimated bool) {
	u := in.Billed
	size := in.PromptTokens
	if size <= 0 {
		size = int64(max(u.Input, 0)) + int64(max(u.CacheRead, 0)) + int64(max(u.CacheWrite, 0))
	}
	m := &rateMemo{p: p, model: in.Model, at: in.At, size: size}
	return m.bill(int64(max(u.Input, 0)), int64(max(u.CacheRead, 0)), int64(max(u.CacheWrite, 0)), int64(max(u.CacheWrite1h, 0)), int64(max(in.Output, 0)))
}

// rateMemo asks the pricer for each tier at most once, at one prompt size.
type rateMemo struct {
	p     SizedPricer
	model string
	at    time.Time
	size  int64
	rate  [5]float64
	est   [5]bool
	done  [5]bool
}

func (m *rateMemo) get(t Tier) (float64, bool) {
	i := 0
	switch t {
	case TierCacheRead:
		i = 1
	case TierCacheWrite5m:
		i = 2
	case TierCacheWrite1h:
		i = 3
	case TierOutput:
		i = 4
	}
	if !m.done[i] {
		m.rate[i], m.est[i] = m.p.PriceAt(m.model, m.at, t, m.size)
		m.done[i] = true
	}
	return m.rate[i], m.est[i]
}

// bill is the stored-bill formula of [RequestCost], on counts that are 0 or more. write1h may arrive above
// write: it is clamped to write here.
func (m *rateMemo) bill(input, read, write, write1h, output int64) (usd float64, estimated bool) {
	write1h = min(write1h, write)
	add := func(t Tier, n int64) {
		if n <= 0 {
			return
		}
		r, e := m.get(t)
		usd += float64(float64(n) * r)
		estimated = estimated || e
	}
	add(TierInput, input)
	add(TierCacheRead, read)
	add(TierCacheWrite5m, write)
	add(TierOutput, output)
	if write1h > 0 {
		r1h, e1h := m.get(TierCacheWrite1h)
		r5, _ := m.get(TierCacheWrite5m)
		estimated = estimated || e1h
		if prem := r1h - r5; prem > 0 {
			usd += float64(float64(write1h) * prem)
		}
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
// It is a [RequestCost] with the prefix as a write, so it follows the same stored-bill rule: the prefix is
// priced at the 5-minute rate, and at the 1-hour tier (OneHour) it pays the premium on top. Every rate is at
// PromptTokens, or at Prefix + FreshInput when it is 0 or less. A negative count counts as 0.
//
// A ping is a write here, so it follows the rule of [MissCost]: a write rate of 0 or less is unknown. The
// result is then 0 and estimated is true. With OneHour, the 1-hour rate is checked too. A ping with no
// prefix writes nothing and has no such check.
func LatePingCost(in LatePingInput, p SizedPricer) (usd float64, estimated bool) {
	prefix, fresh, out := max(in.Prefix, 0), max(in.FreshInput, 0), max(in.Output, 0)
	size := in.PromptTokens
	if size <= 0 {
		size = prefix + fresh
	}
	m := &rateMemo{p: p, model: in.Model, at: in.At, size: size}
	var w1h int64
	if prefix > 0 {
		if r, _ := m.get(TierCacheWrite5m); r <= 0 {
			return 0, true
		}
		if in.OneHour {
			if r, _ := m.get(TierCacheWrite1h); r <= 0 {
				return 0, true
			}
			w1h = prefix
		}
	}
	return m.bill(fresh, 0, prefix, w1h, out)
}
