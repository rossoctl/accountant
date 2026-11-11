package engine

import (
	"reflect"
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

// A gateway bills a 1h ask as 5m. The replay of a change 30 minutes later is cold, and its tokens are
// written again. LowerTTL after EndRequest must give the whole Result that the lower TTL given up
// front gives: the saving is booked at the 5m tier, not at the 1h tier of the client's own mark.
func TestLowerTTLGivesTheWholeResultOfTheLowerTTLUpFront(t *testing.T) {
	oneHour := func(p Position) []Breakpoint { return []Breakpoint{bpTTL(p, "1h")} }
	run := func(lower bool) Result {
		ts := NewTrackers()
		f1 := ttlFacts(5 * time.Minute)
		req{at: t0, client: mk(nil, nil, []blk{b("a", 2000), b("b", 3000), b("c", 2000)}), clientBPs: bps5(msg(2)),
			ops: []Operation{rewrite("o", "mask", "c1", msg(1), 3000, 1000)},
			fwd: mk(nil, nil, []blk{b("a", 2000), b("b2", 1000), b("c", 2000)}), fwdBPs: bps5(msg(2)),
			facts: f1, usage: Usage{CacheWrite: 5000}}.run(ts)
		q := req{at: t0.Add(30 * time.Minute),
			client: mk(nil, nil, []blk{b("a", 2000), b("b", 3000), b("c", 2000), b("d", 500)}), clientBPs: oneHour(msg(3)),
			fwd: mk(nil, nil, []blk{b("a", 2000), b("b2", 1000), b("c", 2000), b("d", 500)}), fwdBPs: oneHour(msg(3)),
			facts: ttlFacts(5 * time.Minute), usage: Usage{CacheWrite: 5500}}
		if !lower {
			return q.run(ts)
		}
		q.facts = ttlFacts(time.Hour)
		r := q.begin(ts)
		r.LowerTTL(5 * time.Minute)
		return r.Compute(q.usage)
	}
	up, late := run(false), run(true)
	if op := opC1(up); op.Saved != (TierTokens{CacheWrite5m: 2000}) {
		t.Fatalf("up front: saved %+v, want 2000 at the 5m write tier (the control)", op.Saved)
	}
	if !reflect.DeepEqual(up, late) {
		t.Fatalf("LowerTTL differs from the TTL given up front:\n up   %+v\n late %+v", up, late)
	}
}
