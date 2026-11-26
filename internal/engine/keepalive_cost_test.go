package engine

import (
	"math"
	"testing"
	"time"
)

// tierPricer: a rate table with one price line. Above 200k tokens every rate is doubled.
type tierPricer struct {
	rates   map[Tier]float64
	est     map[Tier]bool
	sizes   []int64
	unknown bool
}

func (t *tierPricer) PriceAt(_ string, _ time.Time, tier Tier, n int64) (float64, bool) {
	t.sizes = append(t.sizes, n)
	if t.unknown {
		return 0, true
	}
	r := t.rates[tier]
	if n > 200_000 {
		r *= 2
	}
	return r, t.est[tier]
}

func newTierPricer() *tierPricer {
	return &tierPricer{rates: map[Tier]float64{
		TierInput: 1e-6, TierCacheRead: 1e-7, TierCacheWrite5m: 1.25e-6, TierCacheWrite1h: 2e-6, TierOutput: 5e-6,
	}, est: map[Tier]bool{}}
}

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-12 }

func TestPingCost(t *testing.T) {
	p := newTierPricer()
	usd, est := PingCost(PingInput{Model: "m", Prefix: 100_000, FreshInput: 7, Output: 1}, p)
	if want := 100_000*1e-7 + 7*1e-6 + 1*5e-6; !near(usd, want) || est {
		t.Fatalf("got %v %v, want %v false", usd, est, want)
	}
	// A prefix just below the line plus the fresh input crosses it: every rate is the upper one.
	p = newTierPricer()
	usd, _ = PingCost(PingInput{Model: "m", Prefix: 199_995, FreshInput: 7, Output: 1}, p)
	if want := 2 * (199_995*1e-7 + 7*1e-6 + 5e-6); !near(usd, want) {
		t.Fatalf("tier line: got %v want %v", usd, want)
	}
}

func TestPingCostPromptTokensPicksTheTier(t *testing.T) {
	p := newTierPricer()
	usd, _ := PingCost(PingInput{Model: "m", Prefix: 150_000, PromptTokens: 250_000}, p)
	if want := 150_000 * 1e-7 * 2; !near(usd, want) {
		t.Fatalf("got %v want %v", usd, want)
	}
}

func TestPingCostFreeReadRateStillPricesTheOutput(t *testing.T) {
	p := newTierPricer()
	p.rates[TierCacheRead] = 0
	usd, est := PingCost(PingInput{Model: "m", Prefix: 100_000, Output: 1}, p)
	if !near(usd, 5e-6) || est {
		t.Fatalf("got %v %v, want the output only", usd, est)
	}
}

func TestPingCostUnknownAndEstimate(t *testing.T) {
	p := newTierPricer()
	p.unknown = true
	if usd, est := PingCost(PingInput{Model: "x", Prefix: 1000, Output: 1}, p); usd != 0 || !est {
		t.Fatalf("unknown: got %v %v", usd, est)
	}
	p = newTierPricer()
	p.est[TierOutput] = true
	if _, est := PingCost(PingInput{Model: "m", Prefix: 1000, Output: 1}, p); !est {
		t.Fatal("an estimated output rate must mark the cost estimated")
	}
	p = newTierPricer()
	p.est[TierOutput] = true
	if _, est := PingCost(PingInput{Model: "m", Prefix: 1000}, p); est {
		t.Fatal("a tier with no tokens must not mark the cost estimated")
	}
}

func TestMissCost(t *testing.T) {
	p := newTierPricer()
	if usd, est := MissCost(MissInput{Model: "m", Prefix: 100_000}, p); !near(usd, 100_000*(1.25e-6-1e-7)) || est {
		t.Fatalf("5m miss: %v %v", usd, est)
	}
	if usd, _ := MissCost(MissInput{Model: "m", Prefix: 100_000, OneHour: true}, p); !near(usd, 100_000*(2e-6-1e-7)) {
		t.Fatalf("1h miss: %v", usd)
	}
	// PromptTokens picks the tier, Prefix counts the tokens.
	if usd, _ := MissCost(MissInput{Model: "m", Prefix: 199_000, PromptTokens: 200_007}, p); !near(usd, 199_000*2*(1.25e-6-1e-7)) {
		t.Fatalf("sized miss: %v", usd)
	}
}

func TestMissCostFloorAndUnknown(t *testing.T) {
	p := newTierPricer()
	p.rates[TierCacheWrite5m] = 5e-8 // below the read rate
	if usd, _ := MissCost(MissInput{Model: "m", Prefix: 1000}, p); usd != 0 {
		t.Fatalf("a write rate below the read rate must floor at 0, got %v", usd)
	}
	p.rates[TierCacheWrite5m] = 1e-7 // equal to the read rate
	if usd, _ := MissCost(MissInput{Model: "m", Prefix: 1000}, p); usd != 0 {
		t.Fatalf("a write rate equal to the read rate must give 0, got %v", usd)
	}
	p = newTierPricer()
	p.unknown = true
	if usd, est := MissCost(MissInput{Model: "m", Prefix: 1000}, p); usd != 0 || !est {
		t.Fatalf("unknown: %v %v", usd, est)
	}
	if usd, _ := MissCost(MissInput{Model: "m", Prefix: 0}, newTierPricer()); usd != 0 {
		t.Fatal("no prefix, no miss")
	}
}

func TestAvoidedMissesUSD(t *testing.T) {
	if got := AvoidedMissesUSD(3, 0.5); got != 1.5 {
		t.Fatal(got)
	}
	if AvoidedMissesUSD(-1, 0.5) != 0 || AvoidedMissesUSD(2, -1) != 0 {
		t.Fatal("never negative")
	}
}

func TestNegativeCountsCountAsZero(t *testing.T) {
	p := newTierPricer()
	p.est[TierCacheRead], p.est[TierInput], p.est[TierOutput] = true, true, true
	if usd, est := PingCost(PingInput{Model: "m", Prefix: -5, FreshInput: -5, Output: -5, PromptTokens: -5}, p); usd != 0 || est {
		t.Fatalf("PingCost with negative counts: %v %v, want 0 false", usd, est)
	}
	if usd, est := MissCost(MissInput{Model: "m", Prefix: -5}, p); usd != 0 || est {
		t.Fatalf("MissCost with a negative prefix: %v %v, want 0 false", usd, est)
	}
	// A negative PromptTokens falls back to Prefix + FreshInput.
	p = newTierPricer()
	usd, _ := PingCost(PingInput{Model: "m", Prefix: 199_995, FreshInput: 7, PromptTokens: -1}, p)
	if want := 2 * (199_995*1e-7 + 7*1e-6); !near(usd, want) {
		t.Fatalf("negative PromptTokens: %v want %v", usd, want)
	}
}

func TestPingCostWithNoPrefixMakesNoReadCall(t *testing.T) {
	p := newTierPricer()
	p.est[TierCacheRead] = true
	usd, est := PingCost(PingInput{Model: "m", FreshInput: 7, Output: 1}, p)
	if want := 7*1e-6 + 5e-6; !near(usd, want) || est {
		t.Fatalf("got %v %v, want %v false: an estimated read rate must not mark a ping that reads nothing", usd, est, want)
	}
}

func TestRequestCost(t *testing.T) {
	p := newTierPricer()
	u := Usage{Input: 100, CacheRead: 8000, CacheWrite: 1200, CacheWrite1h: 1000}
	usd, est := RequestCost(RequestCostInput{Model: "m", Billed: u, Output: 50}, p)
	// Every write token at the 5-minute rate, then the 1-hour premium on the 1-hour part.
	want := 100*1e-6 + 8000*1e-7 + 1200*1.25e-6 + 50*5e-6 + 1000*(2e-6-1.25e-6)
	if !near(usd, want) || est {
		t.Fatalf("got %v %v, want %v false", usd, est, want)
	}
	// PromptTokens picks the tier: above 200k every rate doubles.
	p = newTierPricer()
	usd, _ = RequestCost(RequestCostInput{Model: "m", Billed: Usage{Input: 10, CacheRead: 100}, PromptTokens: 250_000}, p)
	if want := 2 * (10*1e-6 + 100*1e-7); !near(usd, want) {
		t.Fatalf("sized: %v want %v", usd, want)
	}
	// The default size is Input + CacheRead + CacheWrite.
	p = newTierPricer()
	usd, _ = RequestCost(RequestCostInput{Model: "m", Billed: Usage{Input: 10, CacheRead: 199_990, CacheWrite: 0}}, p)
	if want := 10*1e-6 + 199_990*1e-7; !near(usd, want) {
		t.Fatalf("below the line: %v want %v", usd, want)
	}
	usd, _ = RequestCost(RequestCostInput{Model: "m", Billed: Usage{Input: 10, CacheRead: 200_000}}, p)
	if want := 2 * (10*1e-6 + 200_000*1e-7); !near(usd, want) {
		t.Fatalf("above the line: %v want %v", usd, want)
	}
}

func TestRequestCostEdges(t *testing.T) {
	p := newTierPricer()
	p.est[TierOutput] = true
	if usd, est := RequestCost(RequestCostInput{Model: "m", Billed: Usage{Input: -5, CacheRead: -5, CacheWrite: -5, CacheWrite1h: -5}, Output: -5}, p); usd != 0 || est {
		t.Fatalf("negative counts: %v %v", usd, est)
	}
	// A 1-hour part larger than the write is clamped to the write.
	usd, _ := RequestCost(RequestCostInput{Model: "m", Billed: Usage{CacheWrite: 100, CacheWrite1h: 500}}, newTierPricer())
	if want := 100 * 2e-6; !near(usd, want) {
		t.Fatalf("clamp: %v want %v", usd, want)
	}
	// An estimated tier with tokens marks the cost. A tier with no tokens does not.
	if _, est := RequestCost(RequestCostInput{Model: "m", Billed: Usage{Input: 1}, Output: 1}, p); !est {
		t.Fatal("an estimated output rate must mark the cost")
	}
	if _, est := RequestCost(RequestCostInput{Model: "m", Billed: Usage{Input: 1}}, p); est {
		t.Fatal("a tier with no tokens must not mark the cost")
	}
	u := newTierPricer()
	u.unknown = true
	if usd, est := RequestCost(RequestCostInput{Model: "x", Billed: Usage{Input: 1}, Output: 1}, u); usd != 0 || !est {
		t.Fatalf("unknown model: %v %v", usd, est)
	}
}

func TestLatePingCost(t *testing.T) {
	p := newTierPricer()
	in := LatePingInput{PingInput: PingInput{Model: "m", Prefix: 100_000, FreshInput: 7, Output: 1}}
	usd, est := LatePingCost(in, p)
	if want := 100_000*1.25e-6 + 7*1e-6 + 1*5e-6; !near(usd, want) || est {
		t.Fatalf("5m: %v %v want %v", usd, est, want)
	}
	in.OneHour = true
	if usd, _ = LatePingCost(in, p); !near(usd, 100_000*1.25e-6+7*1e-6+5e-6+100_000*(2e-6-1.25e-6)) { // the prefix at 5m, plus the premium
		t.Fatalf("1h: %v", usd)
	}
	// Tier line: prefix 199,995 + 7 fresh = 200,002 is above it.
	in = LatePingInput{PingInput: PingInput{Model: "m", Prefix: 199_995, FreshInput: 7, Output: 1}}
	if usd, _ = LatePingCost(in, newTierPricer()); !near(usd, 2*(199_995*1.25e-6+7*1e-6+5e-6)) {
		t.Fatalf("tier line: %v", usd)
	}
	// A late ping costs at least the read ping on the same prefix when write >= read.
	rd, _ := PingCost(in.PingInput, newTierPricer())
	if lt, _ := LatePingCost(in, newTierPricer()); lt < rd {
		t.Fatalf("late %v below read %v", lt, rd)
	}
}

func TestLatePingCostUnknownWriteRate(t *testing.T) {
	in := LatePingInput{PingInput: PingInput{Model: "m", Prefix: 1000, FreshInput: 7, Output: 1}}
	p := newTierPricer()
	p.rates[TierCacheWrite5m] = 0
	if usd, est := LatePingCost(in, p); usd != 0 || !est {
		t.Fatalf("5m write rate 0: %v %v, want 0 true", usd, est)
	}
	p = newTierPricer()
	p.rates[TierCacheWrite1h] = 0
	in.OneHour = true
	if usd, est := LatePingCost(in, p); usd != 0 || !est {
		t.Fatalf("1h write rate 0: %v %v, want 0 true", usd, est)
	}
	// A 5-minute ping does not read the 1-hour rate.
	in.OneHour = false
	if usd, _ := LatePingCost(in, p); usd == 0 {
		t.Fatal("a 5-minute ping must not depend on the 1-hour rate")
	}
	// A ping with no prefix writes nothing: no write check.
	p = newTierPricer()
	p.rates[TierCacheWrite5m] = 0
	in.Prefix = 0
	if usd, est := LatePingCost(in, p); !near(usd, 7*1e-6+5e-6) || est {
		t.Fatalf("no prefix: %v %v", usd, est)
	}
}

// A host that prices the rewrite inside a larger request gives that request's size.
func TestLatePingCostWithHostPromptTokens(t *testing.T) {
	in := LatePingInput{PingInput: PingInput{Model: "m", Prefix: 150_000, FreshInput: 7, Output: 1, PromptTokens: 250_000}}
	usd, _ := LatePingCost(in, newTierPricer())
	if want := 2 * (150_000*1.25e-6 + 7*1e-6 + 5e-6); !near(usd, want) {
		t.Fatalf("got %v want %v: the host size 250,000 picks the upper tier", usd, want)
	}
	in.PromptTokens = 0 // Prefix + FreshInput = 150,007: the lower tier
	usd, _ = LatePingCost(in, newTierPricer())
	if want := 150_000*1.25e-6 + 7*1e-6 + 5e-6; !near(usd, want) {
		t.Fatalf("default size: %v want %v", usd, want)
	}
}

// countingPricer counts the price calls for each tier.
type countingPricer struct {
	inner *tierPricer
	calls map[Tier]int
}

func (c *countingPricer) PriceAt(m string, at time.Time, t Tier, n int64) (float64, bool) {
	c.calls[t]++
	return c.inner.PriceAt(m, at, t, n)
}

// LatePingCost prices each tier once: the write rate that it checks is the one it uses.
func TestLatePingCostPricesEachTierOnce(t *testing.T) {
	for _, oneHour := range []bool{false, true} {
		c := &countingPricer{inner: newTierPricer(), calls: map[Tier]int{}}
		in := LatePingInput{PingInput: PingInput{Model: "m", Prefix: 100_000, FreshInput: 7, Output: 1}, OneHour: oneHour}
		usd, _ := LatePingCost(in, c)
		write := 1.25e-6
		if oneHour {
			write = 2e-6
		}
		if want := 100_000*write + 7*1e-6 + 5e-6; !near(usd, want) {
			t.Fatalf("oneHour %v: got %v want %v", oneHour, usd, want)
		}
		for tier, n := range c.calls {
			if n != 1 {
				t.Errorf("oneHour %v: tier %s priced %d times, want 1", oneHour, tier, n)
			}
		}
		// The prefix is priced at the 5-minute rate; a 1-hour ping also reads the 1-hour rate for its premium.
		want := 3
		if oneHour {
			want = 4
		}
		if len(c.calls) != want {
			t.Errorf("oneHour %v: %d tiers priced, want %d: %v", oneHour, len(c.calls), want, c.calls)
		}
	}
}

// storedBill is the formula that a host stores as the bill of a request (the host's Price.Cost plus the
// 1-hour premium), written out here as the reference. Each product is rounded on its own with an explicit
// float64 conversion, so no CPU fuses a multiply and an add (#1128 of the host). Every write token is priced
// at the 5-minute rate, and the 1-hour part pays the premium after, when the premium is above 0.
func storedBill(in, read, write, out, w1h int64, rIn, rRead, rW5, rW1h, rOut float64) float64 {
	cost := float64(float64(in)*rIn) + float64(float64(read)*rRead) +
		float64(float64(write)*rW5) + float64(float64(out)*rOut)
	if w1h > 0 {
		if prem := rW1h - rW5; prem > 0 {
			cost += float64(float64(w1h) * prem)
		}
	}
	return cost
}

type flatSized struct{ in, read, w5, w1h, out float64 }

func (f flatSized) PriceAt(_ string, _ time.Time, t Tier, _ int64) (float64, bool) {
	switch t {
	case TierInput:
		return f.in, false
	case TierCacheRead:
		return f.read, false
	case TierCacheWrite5m:
		return f.w5, false
	case TierCacheWrite1h:
		return f.w1h, false
	case TierOutput:
		return f.out, false
	}
	return 0, true
}

// The guarantee rests on the Go spec: an explicit floating-point conversion forces the value to be rounded
// to the type, so a compiler may not fuse the product into a multiply-add (FMA). storedBill and the accountant
// both use that rule, so the same bits follow on every CPU. This test runs on the CPU of the developer or of
// CI; on one that fuses (arm64) it fails if a product loses its conversion. The check against a host's
// real formula is the host's own stored-bill pin, which compares the host's old formula with this function.
//
// RequestCost must give the same bits as the stored bill, with a 1-hour part, with FMA-prone rates, and with
// a 1-hour rate below the 5-minute rate (which has no premium: those tokens stay at the 5-minute rate).
func TestRequestCostIsTheStoredBillBitForBit(t *testing.T) {
	rates := []flatSized{
		{3e-6, 0.3e-6, 3.75e-6, 6e-6, 15e-6},
		{5e-6, 0.5e-6, 6.25e-6, 10e-6, 25e-6},
		{1.1e-6, 0.11e-6, 1.375e-6, 2.2e-6, 5.5e-6},
		{2.5e-6, 0.25e-6, 2.5e-6, 5e-6, 10e-6},      // 5-minute write at the input rate (OpenAI form)
		{3e-6, 0.3e-6, 3.75e-6, 3e-6, 15e-6},        // 1-hour rate BELOW the 5-minute rate: no premium
		{3e-6, 0.3e-6, 3.75e-6, 3.75e-6, 15e-6},     // equal: no premium
		{1.0000001e-6, 0.1e-6, 1.25e-6, 2e-6, 5e-6}, // values that make a fused multiply-add differ
	}
	counts := [][5]int64{ // input, read, write, output, write1h
		{100, 8000, 1200, 50, 0}, {7, 271_993, 0, 1, 0}, {123_457, 98_765, 54_321, 4_567, 20_000},
		{1, 1, 1, 1, 1}, {999, 999_999, 33_333, 777, 33_333}, {0, 0, 5000, 0, 5000}, {10, 20, 30, 40, 50}, // 1h above write: clamped
	}
	for ri, r := range rates {
		for ci, c := range counts {
			w1h := min(c[4], c[2])
			want := storedBill(c[0], c[1], c[2], c[3], w1h, r.in, r.read, r.w5, r.w1h, r.out)
			got, _ := RequestCost(RequestCostInput{Model: "m", Billed: Usage{Input: int(c[0]), CacheRead: int(c[1]), CacheWrite: int(c[2]), CacheWrite1h: int(c[4])}, Output: int(c[3])}, r)
			if got != want {
				t.Errorf("rates %d counts %d: got %v want %v (diff %g)", ri, ci, got, want, got-want)
			}
			// A late ping is a request with the prefix as a write.
			for _, one := range []bool{false, true} {
				var lw int64
				if one {
					lw = c[2]
				}
				lwant := storedBill(c[0], 0, c[2], c[3], lw, r.in, r.read, r.w5, r.w1h, r.out)
				lgot, est := LatePingCost(LatePingInput{PingInput: PingInput{Model: "m", Prefix: c[2], FreshInput: c[0], Output: c[3]}, OneHour: one}, r)
				if c[2] == 0 {
					continue // no prefix: nothing is written, and the write rate is not checked
				}
				if lgot != lwant || est {
					t.Errorf("late ping rates %d counts %d oneHour %v: got %v want %v", ri, ci, one, lgot, lwant)
				}
			}
		}
	}
}

// A 1-hour rate below the 5-minute rate is priced at the 5-minute rate: the 1-hour part has no premium.
func TestRequestCostOneHourBelowFiveMinute(t *testing.T) {
	r := flatSized{in: 1e-6, w5: 4e-6, w1h: 2e-6}
	usd, _ := RequestCost(RequestCostInput{Model: "m", Billed: Usage{CacheWrite: 1000, CacheWrite1h: 600}}, r)
	if want := 1000 * 4e-6; !near(usd, want) {
		t.Fatalf("got %v want %v: all 1000 write tokens at the 5-minute rate", usd, want)
	}
}

// An unknown 5-minute rate makes a 1-hour late ping unknown, even when the 1-hour rate is known. A 1-hour
// part with an unknown 1-hour rate is priced at the 5-minute rate, and the result is an estimate.
func TestOneHourWithAnUnknownRate(t *testing.T) {
	p := newTierPricer()
	p.rates[TierCacheWrite5m] = 0
	in := LatePingInput{PingInput: PingInput{Model: "m", Prefix: 1000, FreshInput: 7, Output: 1}, OneHour: true}
	if usd, est := LatePingCost(in, p); usd != 0 || !est {
		t.Fatalf("5m unknown, 1h known: %v %v, want 0 true", usd, est)
	}
	p = newTierPricer()
	p.rates[TierCacheWrite1h] = 0
	p.est[TierCacheWrite1h] = true
	usd, est := RequestCost(RequestCostInput{Model: "m", Billed: Usage{CacheWrite: 1000, CacheWrite1h: 600}}, p)
	if want := 1000 * 1.25e-6; !near(usd, want) || !est {
		t.Fatalf("1h unknown: %v %v, want %v true: every write token at the 5-minute rate, marked estimated", usd, est, want)
	}
}

// PingCost rounds each product on its own, like the other cost functions, so FMA-prone values give the
// same bits as the sum of explicitly rounded products on every CPU.
func TestPingCostRoundsEachProductOnItsOwn(t *testing.T) {
	rates := []flatSized{{in: 3e-6, read: 0.3e-6, out: 15e-6}, {in: 1.1e-6, read: 0.11e-6, out: 5.5e-6},
		// Found by search: with these, a fused multiply-add of the fresh input on the read part gives another last bit.
		{in: 7.0980000000000005e-06, read: 2.53e-07, out: 1e-5}, {in: 6.061e-06, read: 1.48e-07, out: 1e-5},
		{in: 6.985e-06, read: 6.43e-07, out: 1e-5}, {in: 5.832e-06, read: 2.1100000000000002e-07, out: 1e-5}}
	for ri, r := range rates {
		for _, c := range [][3]int64{{123_457, 7, 1}, {98_765, 7, 16}, {54_321, 13, 3}, {6327, 24, 1}, {6256, 20, 1}, {20245, 23, 1}, {28768, 19, 1}} {
			want := float64(float64(c[0])*r.read) + float64(float64(c[1])*r.in) + float64(float64(c[2])*r.out)
			got, _ := PingCost(PingInput{Model: "m", Prefix: c[0], FreshInput: c[1], Output: c[2]}, r)
			if got != want {
				t.Errorf("rates %d counts %v: got %v want %v (diff %g)", ri, c, got, want, got-want)
			}
		}
	}
}

func TestMissCostHeadTail(t *testing.T) {
	p := newTierPricer()
	const h, tl = 20_000, 150
	wr5, wr1, rd := 1.25e-6, 2e-6, 1e-7
	tail := float64(float64(tl) * (wr5 - rd))
	head := float64(float64(h) * (wr1 - rd))
	alive, _ := MissCost(MissInput{Model: "m", Prefix: h + tl, HeadTokens: h, HeadAlive: true}, p)
	if !near(alive, tail) {
		t.Fatalf("head alive: got %v want %v", alive, tail)
	}
	gone, _ := MissCost(MissInput{Model: "m", Prefix: h + tl, HeadTokens: h}, p)
	if !near(gone, tail+head) {
		t.Fatalf("head expired: got %v want %v", gone, tail+head)
	}
	none, _ := MissCost(MissInput{Model: "m", Prefix: h + tl}, p)
	if !near(none, float64(h+tl)*(wr5-rd)) {
		t.Fatalf("no head: got %v", none)
	}
	if usd, _ := MissCost(MissInput{Model: "m", Prefix: h, HeadTokens: h + 5, HeadAlive: true}, p); usd != 0 {
		t.Fatalf("no tail, head alive: got %v want 0", usd)
	}
}
