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
	want := 100*1e-6 + 8000*1e-7 + 200*1.25e-6 + 1000*2e-6 + 50*5e-6
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
	if usd, _ = LatePingCost(in, p); !near(usd, 100_000*2e-6+7*1e-6+5e-6) {
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
		if len(c.calls) != 3 {
			t.Errorf("oneHour %v: %d tiers priced, want 3 (write, input, output): %v", oneHour, len(c.calls), c.calls)
		}
	}
}
