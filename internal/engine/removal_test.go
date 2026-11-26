package engine

import (
	"math"
	"testing"
	"time"
)

// linePricer has two price lines. A request above 200k tokens bills every token at 2x. The rate is
// 1e-6 per input token, 1e-7 per read, 1.25e-6 per 5m write and 2e-6 per 1h write, before the line.
type linePricer struct{ unknown bool }

func (l linePricer) PriceAt(_ string, _ time.Time, t Tier, size int64) (float64, bool) {
	if l.unknown {
		return 0, true
	}
	m := 1.0
	if size > 200_000 {
		m = 2
	}
	switch t {
	case TierInput:
		return 1e-6 * m, false
	case TierCacheRead:
		return 1e-7 * m, false
	case TierCacheWrite5m:
		return 1.25e-6 * m, false
	case TierCacheWrite1h:
		return 2e-6 * m, false
	}
	return 0, true
}

func removalNear(a, b float64) bool { return math.Abs(a-b) <= 1e-12 }

func TestHeadTier(t *testing.T) {
	for _, c := range []struct {
		u    Usage
		want Tier
	}{
		{Usage{Input: 5}, TierInput},
		{Usage{}, TierInput},
		{Usage{CacheRead: 1, CacheWrite: 9, CacheWrite1h: 9}, TierCacheRead},
		{Usage{CacheWrite: 10}, TierCacheWrite5m},
		{Usage{CacheWrite: 10, CacheWrite1h: 4}, TierCacheWrite1h},
		// CacheWrite1h is part of CacheWrite: with no CacheWrite the usage is bad, and the head is fresh.
		{Usage{CacheWrite1h: 4}, TierInput},
	} {
		if got := HeadTier(c.u); got != c.want {
			t.Errorf("HeadTier(%+v) = %s, want %s", c.u, got, c.want)
		}
	}
}

func TestRemovalAppliesTheFactorOnce(t *testing.T) {
	in := RemovalInput{Model: "m", Tier: TierCacheRead, Prompt: 10_000, Removed: 1000, Factor: 1.5}
	got := RemovalSaving(in, linePricer{})
	if want := 1000 * 1.5 * 1e-7; !removalNear(got.SavedUSD, want) {
		t.Fatalf("SavedUSD = %g, want %g", got.SavedUSD, want)
	}
	if !removalNear(got.PerTokenUSD, 1.5*1e-7) {
		t.Fatalf("PerTokenUSD = %g", got.PerTokenUSD)
	}
	in.Factor = 0
	if got := RemovalSaving(in, linePricer{}); !removalNear(got.SavedUSD, 1000*1e-7) {
		t.Fatalf("a factor of 0 must mean 1, got %g", got.SavedUSD)
	}
}

func TestRemovalSizeIncludesTheFactor(t *testing.T) {
	// 199_000 sent + 1000 removed = 200_000: on the line, not above it. With the factor 1.5 the request
	// without the change has 200_500 tokens: above the line, so the saving prices at 2x.
	in := RemovalInput{Model: "m", Tier: TierInput, Prompt: 199_000, Removed: 1000, Factor: 1}
	if got := RemovalSaving(in, linePricer{}); !removalNear(got.SavedUSD, 1000*1e-6) {
		t.Fatalf("at the line: SavedUSD = %g", got.SavedUSD)
	}
	in.Factor = 1.5
	if got := RemovalSaving(in, linePricer{}); !removalNear(got.SavedUSD, 1500*2e-6) {
		t.Fatalf("above the line: SavedUSD = %g", got.SavedUSD)
	}
}

func TestRemovalRoundsTheSizeUp(t *testing.T) {
	// 199_999 + ceil(1.0001) = 200_001: above the line. Truncating would give 200_000 and the cheap side.
	in := RemovalInput{Model: "m", Tier: TierInput, Prompt: 199_999, Removed: 1, Factor: 1.0001}
	if got := RemovalSaving(in, linePricer{}); !removalNear(got.PerTokenUSD, 1.0001*2e-6) {
		t.Fatalf("PerTokenUSD = %g, want the dearer side", got.PerTokenUSD)
	}
}

func TestRemovalAddedPricesAtTheBilledSize(t *testing.T) {
	// Sent 150_000. Removed 60_000, added 1000: the request without the change has 209_000 tokens (2x).
	in := RemovalInput{Model: "m", Tier: TierCacheRead, Prompt: 150_000, Removed: 60_000, Added: 1000, Factor: 1}
	got := RemovalSaving(in, linePricer{})
	if !removalNear(got.SavedUSD, 60_000*2e-7) || !removalNear(got.AddedUSD, 1000*1e-7) || !removalNear(got.NetUSD, got.SavedUSD-got.AddedUSD) {
		t.Fatalf("figure = %+v", got)
	}
}

func TestRemovalUnsizedAndUnknown(t *testing.T) {
	in := RemovalInput{Model: "m", Tier: TierInput, Prompt: 500_000, Removed: 1000, Unsized: true}
	if got := RemovalSaving(in, linePricer{}); !removalNear(got.SavedUSD, 1000*1e-6) {
		t.Fatalf("unsized must price at the base rate, got %g", got.SavedUSD)
	}
	if got := RemovalSaving(in, linePricer{unknown: true}); got.SavedUSD != 0 || !got.Estimated {
		t.Fatalf("unknown model: %+v", got)
	}
	if got := RemovalSaving(RemovalInput{Model: "m", Tier: TierInput}, linePricer{}); got.SavedUSD != 0 || !removalNear(got.PerTokenUSD, 1e-6) {
		t.Fatalf("nothing removed: %+v", got)
	}
}

func TestCountedUSDIsDeltaPlusInteraction(t *testing.T) {
	f := USDFigure{DeltaUSD: 3, InteractionUSD: -1, KeepAliveSavedUSD: 100, SplitUSD: 100, TTLUSD: 100}
	if f.CountedUSD() != 2 {
		t.Fatalf("CountedUSD = %g, want 2", f.CountedUSD())
	}
}

func TestRemovalNoPromptSizeAndAddedAboveRemoved(t *testing.T) {
	// Prompt 0 with Removed > 0 and Unsized false: the size is the removed tokens alone. 250k removed is above the
	// line, so the rate is 2x. (The true size is larger, so the rate can only be too low.)
	in := RemovalInput{Model: "m", Tier: TierInput, Prompt: 0, Removed: 250_000}
	if got := RemovalSaving(in, linePricer{}); !removalNear(got.PerTokenUSD, 2e-6) {
		t.Fatalf("PerTokenUSD = %g, want 2e-6", got.PerTokenUSD)
	}
	// Added above Removed: the size without the change would be below the billed size. It clamps at 0, not below.
	in = RemovalInput{Model: "m", Tier: TierInput, Prompt: 100, Removed: 10, Added: 1000}
	got := RemovalSaving(in, linePricer{})
	if !removalNear(got.SavedUSD, 10*1e-6) || !removalNear(got.AddedUSD, 1000*1e-6) || got.NetUSD >= 0 {
		t.Fatalf("figure = %+v", got)
	}
}

func TestRemovalFigureNamesTheRulesVersion(t *testing.T) {
	if got := RemovalSaving(RemovalInput{Model: "m", Tier: TierInput}, linePricer{}); got.Version != RulesVersion || RulesVersion == 0 {
		t.Fatalf("Version = %d, RulesVersion = %d", got.Version, RulesVersion)
	}
	if r := (Result{Version: RulesVersion}); r.Version != RemovalSaving(RemovalInput{}, linePricer{}).Version {
		t.Fatal("Result and RemovalFigure must carry one rules version")
	}
}
