package engine

import (
	"testing"
	"time"
)

func TestPingOpsMakesOneSpendWithTheWindowChangeID(t *testing.T) {
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	win := at.Add(-4 * time.Minute)
	ops, facts := PingOps(Ping{
		Session: "s1", ThreadID: "", Model: "m", Host: "h",
		Usage: CallUsage{Usage: Usage{CacheRead: 1000, Input: 10}, Output: 1},
		At:    at, Window: win,
	}, Billing{Mode: BillingAPI})

	if ops.Thread.ID != "main" {
		t.Fatalf("thread = %q, want main", ops.Thread.ID)
	}
	if ops.Session != "s1" || ops.Model != "m" || !ops.At.Equal(at) || ops.Billing.Mode != BillingAPI {
		t.Fatalf("request fields = %+v", ops)
	}
	if len(ops.Ops) != 1 || ops.Ops[0].Kind != KindSpend || ops.Ops[0].Call == nil {
		t.Fatalf("want one spend with a call, got %+v", ops.Ops)
	}
	if c := ops.Ops[0].Call; c.Host != "h" || c.Model != "m" || c.Usage.CacheRead != 1000 || c.Usage.Output != 1 {
		t.Fatalf("call = %+v", c)
	}
	wantID := "ka:main:" + win.UTC().Format(time.RFC3339)
	if ops.Ops[0].ChangeID != wantID || facts.PingWindow != wantID {
		t.Fatalf("ChangeID = %q, PingWindow = %q, want %q", ops.Ops[0].ChangeID, facts.PingWindow, wantID)
	}
	if !facts.LastPing.Equal(at) {
		t.Fatalf("LastPing = %v, want %v", facts.LastPing, at)
	}
}

// A named thread keeps its ID, and a window in another zone gives the same ChangeID (UTC).
func TestPingOpsKeepsANamedThreadAndNormalizesTheWindowToUTC(t *testing.T) {
	zone := time.FixedZone("x", 3*3600)
	win := time.Date(2026, 10, 28, 15, 0, 0, 0, zone)
	ops, facts := PingOps(Ping{Session: "s", ThreadID: "side", Window: win}, Billing{})
	if ops.Thread.ID != "side" {
		t.Fatalf("thread = %q, want side", ops.Thread.ID)
	}
	if want := "ka:side:2026-10-28T12:00:00Z"; facts.PingWindow != want {
		t.Fatalf("PingWindow = %q, want %q", facts.PingWindow, want)
	}
}

// The ping prices as a standalone spend with no ThreadState.
func TestPingOpsPricesStandaloneWithoutAnyThreadState(t *testing.T) {
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	ops, _ := PingOps(Ping{Session: "s", Model: "m", At: at, Window: at,
		Usage: CallUsage{Usage: Usage{CacheRead: 500}, Output: 1}}, Billing{Mode: BillingAPI})
	res := ComputeStandalone(ops)
	if len(res.Ops) != 1 || res.Ops[0].Call == nil || res.Ops[0].Call.Usage.CacheRead != 500 || res.Ops[0].ChangeID == "" {
		t.Fatalf("result = %+v, want the spend with its call, for the host to price", res.Ops)
	}
	if len(res.Unpriced) != 0 {
		t.Fatalf("unpriced = %+v", res.Unpriced)
	}
}
