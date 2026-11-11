package engine

import (
	"testing"
	"time"
)

// LowerTTL after EndRequest gives the Result that the host's TTL given up front gives: a 1h ask
// billed at 5m is a 5m entry, so ten minutes later the phase is cold (accountant#56).
func TestLowerTTLEqualsGivingTheLowerTTLUpFront(t *testing.T) {
	c := []blk{b("a", 1000), b("b", 1000)}
	oneHour := []Breakpoint{bpTTL(msg(1), "1h")}
	run := func(lower bool) Result {
		ts := NewTrackers()
		first := req{at: t0, client: mk(nil, nil, c), clientBPs: oneHour, facts: ttlFacts(time.Hour), usage: Usage{CacheWrite: 2000}}
		if lower {
			r := first.begin(ts)
			r.LowerTTL(5 * time.Minute)
			r.Compute(first.usage)
		} else {
			first.facts = exact5m
			first.run(ts)
		}
		return req{at: t0.Add(10 * time.Minute), client: mk(nil, nil, c), clientBPs: oneHour, facts: exact5m,
			usage: Usage{CacheWrite: 2000}}.run(ts)
	}
	up, late := run(false), run(true)
	if late.Phase != PhaseCold || up.Phase != late.Phase {
		t.Fatalf("phase: up front %q, LowerTTL %q, want both cold", up.Phase, late.Phase)
	}
}

func TestLowerTTLOnlyLowersAndKeepsTheHostsSlice(t *testing.T) {
	ts := NewTrackers()
	oneHour := []Breakpoint{bpTTL(msg(1), "1h")}
	q := req{at: t0, client: mk(nil, nil, []blk{b("a", 1000), b("b", 1000)}), clientBPs: oneHour, facts: ttlFacts(time.Hour)}

	r := ts.BeginRequest(RequestMeta{RequestID: "r", Session: "s1", At: t0}, q.client, q.clientBPs)
	r.LowerTTL(5 * time.Minute) // before EndRequest: nothing
	r.EndRequest(Thread{ID: "main"}, q.client, oneHour, q.facts)
	r.LowerTTL(2 * time.Hour) // not shorter
	r.LowerTTL(0)
	if r.facts.TTL != time.Hour || r.breakpoints[0].TTL != "1h" {
		t.Fatalf("no-op calls changed the record: ttl %v, bp %q", r.facts.TTL, r.breakpoints[0].TTL)
	}
	r.LowerTTL(5 * time.Minute)
	if r.facts.TTL != 5*time.Minute || r.breakpoints[0].TTL != "5m" {
		t.Fatalf("ttl %v, bp %q, want 5m and 5m", r.facts.TTL, r.breakpoints[0].TTL)
	}
	if oneHour[0].TTL != "1h" {
		t.Fatalf("the host's slice was changed: %q", oneHour[0].TTL)
	}
}
