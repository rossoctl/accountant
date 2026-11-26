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
