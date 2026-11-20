package engine

import (
	"reflect"
	"testing"
	"time"
)

// readTTL scenarios (accountant#105): turn 9 writes 10,000 tokens at minute 20 with the given breakpoint TTL.
// A ping at pingAt reads them with pingTTL. Turn 10 arrives at turnAt.

func bpListTTL(ttl string, ps ...Position) []Breakpoint {
	out := make([]Breakpoint, len(ps))
	for i, p := range ps {
		out[i] = Breakpoint{Pos: p, TTL: ttl}
	}
	return out
}

type readTTLCase struct {
	readSets    bool
	writeTTL    string // TTL of turn 9's breakpoints; the entry TTL the host states
	pingAt      time.Duration
	pingTTL     time.Duration
	turnAt      time.Duration
	turn10Usage Usage
}

func (c readTTLCase) run() Result {
	m := msgs(6, 1000)
	sysB := []blk{b("sys", 4000)}
	entry := 5 * time.Minute
	if c.writeTTL == "1h" {
		entry = time.Hour
	}
	f := EntryFacts{TTL: entry, Lifetime: LifetimeExact, Provider: Provider{ReadSetsTTL: c.readSets}}
	ts := NewTrackers()
	req{at: t0.Add(20 * time.Minute), client: mk(nil, sysB, m), clientBPs: bpListTTL(c.writeTTL, sys(0), msg(5)), facts: f,
		usage: Usage{CacheWrite: 10000}}.run(ts)
	f.LastPing, f.PingRead, f.PingWindow, f.PingTTL = t0.Add(c.pingAt), ptr(msg(5)), "ka:main:w1", c.pingTTL
	return req{at: t0.Add(c.turnAt), client: mk(nil, sysB, with(m, b("m6", 500))), clientBPs: bpListTTL(c.writeTTL, sys(0), msg(6)),
		facts: f, usage: c.turn10Usage}.run(ts)
}

var (
	read10   = Usage{CacheRead: 10000, CacheWrite: 500}
	wrote10  = Usage{CacheWrite: 10500}
	hour     = time.Hour
	fiveMins = 5 * time.Minute
)

// (a) The property is off: PingTTL changes no figure. A 5m entry, a 1h ping, a return at minute 44 is a
// miss, with or without PingTTL.
func TestReadSetsTTLOffChangesNoFigure(t *testing.T) {
	with := readTTLCase{writeTTL: "5m", pingAt: 24 * time.Minute, pingTTL: hour, turnAt: 44 * time.Minute, turn10Usage: wrote10}.run()
	without := readTTLCase{writeTTL: "5m", pingAt: 24 * time.Minute, turnAt: 44 * time.Minute, turn10Usage: wrote10}.run()
	if !reflect.DeepEqual(with, without) {
		t.Fatalf("PingTTL changed the result with the property off:\n%+v\n%+v", with, without)
	}
	if with.Phase != PhaseCold {
		t.Fatalf("phase %q, want cold", with.Phase)
	}
	// and the 1h ping is no help when the provider billed a read: the usage beats the clock, with no rescue
	r := readTTLCase{writeTTL: "5m", pingAt: 24 * time.Minute, pingTTL: hour, turnAt: 44 * time.Minute, turn10Usage: read10}.run()
	for _, e := range r.CacheEffects {
		if e.Kind == EffectRescue {
			t.Fatalf("rescue with the property off: %+v", e)
		}
	}
}

// (b) 5m write, 1h read (the ping), return read at +20 min: a rescue, and no premium.
func TestReadSetsTTLOneHourPingRescuesAReturnAfterFiveMinutes(t *testing.T) {
	for _, turnAt := range []time.Duration{44 * time.Minute, 84 * time.Minute} {
		res := readTTLCase{readSets: true, writeTTL: "5m", pingAt: 24 * time.Minute, pingTTL: hour, turnAt: turnAt, turn10Usage: read10}.run()
		if res.Phase != PhaseHot {
			t.Fatalf("at %v: phase %q, want hot", turnAt, res.Phase)
		}
		if len(res.CacheEffects) != 1 {
			t.Fatalf("at %v: effects %+v, want the rescue only (no premium)", turnAt, res.CacheEffects)
		}
		e := effect(t, res, EffectRescue)
		if e.ChangeID != "ka:main:w1" || !samePos(e.To, ptr(msg(5))) ||
			e.Saved != (TierTokens{CacheWrite5m: 10000}) || e.Caused != (TierTokens{CacheRead: 10000}) {
			t.Fatalf("at %v: rescue %+v, want 10000 write->read to messages[5]", turnAt, e)
		}
	}
}

// More than 1 hour after the 1h ping the entry is gone: a miss, no rescue.
func TestReadSetsTTLOneHourPingEndsAfterOneHour(t *testing.T) {
	res := readTTLCase{readSets: true, writeTTL: "5m", pingAt: 24 * time.Minute, pingTTL: hour, turnAt: 85*time.Minute + time.Second, turn10Usage: wrote10}.run()
	if res.Phase != PhaseCold {
		t.Fatalf("phase %q, want cold", res.Phase)
	}
	for _, e := range res.CacheEffects {
		if e.Kind == EffectRescue {
			t.Fatalf("rescue after the 1h ended: %+v", e)
		}
	}
}

// (c) 5m write, 1h read, then a 5m read of a real request, return at +7 min: the entry is 5m, so it is gone.
func TestReadSetsTTLALaterFiveMinuteReadSetsTheEntryBackToFiveMinutes(t *testing.T) {
	m := msgs(6, 1000)
	sysB := []blk{b("sys", 4000)}
	f := EntryFacts{TTL: fiveMins, Lifetime: LifetimeExact, Provider: Provider{ReadSetsTTL: true}}
	ts := NewTrackers()
	req{at: t0.Add(20 * time.Minute), client: mk(nil, sysB, m), clientBPs: bps5(sys(0), msg(5)), facts: f, usage: Usage{CacheWrite: 10000}}.run(ts)
	pf := f // the 1h ping at minute 22, then a real 5m read at minute 24
	pf.LastPing, pf.PingRead, pf.PingWindow, pf.PingTTL = t0.Add(22*time.Minute), ptr(msg(5)), "ka:main:w1", hour
	m7 := with(m, b("m6", 500))
	r10 := req{at: t0.Add(24 * time.Minute), client: mk(nil, sysB, m7), clientBPs: bps5(sys(0), msg(6)), facts: pf, usage: read10}.run(ts)
	noEffects(t, r10) // inside 5 minutes of the write: the entry was alive anyway
	r11 := req{at: t0.Add(31 * time.Minute), client: mk(nil, sysB, with(m7, b("m7", 500))), clientBPs: bps5(sys(0), msg(7)), facts: f,
		usage: Usage{CacheWrite: 11000}}.run(ts)
	if r11.Phase != PhaseCold {
		t.Fatalf("phase %q, want cold: the 5m read set the entry back to 5 minutes", r11.Phase)
	}
	for _, e := range r11.CacheEffects {
		if e.Kind == EffectRescue {
			t.Fatalf("rescue on a 5m entry: %+v", e)
		}
	}
}

// (d) A return within 5 minutes of the 1h read, and of the write: the entry was alive anyway, so no rescue.
func TestReadSetsTTLReturnWithinFiveMinutesIsNotARescue(t *testing.T) {
	res := readTTLCase{readSets: true, writeTTL: "5m", pingAt: 21 * time.Minute, pingTTL: hour, turnAt: 24 * time.Minute, turn10Usage: read10}.run()
	if res.Phase != PhaseHot {
		t.Fatalf("phase %q, want hot", res.Phase)
	}
	noEffects(t, res)
}

// A 5m ping on a 1h entry sets it to 5 minutes: a return at minute 30 (ping at 21) finds it gone, as the
// property off finds it alive.
func TestReadSetsTTLAFiveMinutePingShortensAnHourEntry(t *testing.T) {
	on := readTTLCase{readSets: true, writeTTL: "1h", pingAt: 21 * time.Minute, pingTTL: fiveMins, turnAt: 30 * time.Minute, turn10Usage: wrote10}.run()
	if on.Phase != PhaseCold {
		t.Fatalf("on: phase %q, want cold", on.Phase)
	}
	off := readTTLCase{writeTTL: "1h", pingAt: 21 * time.Minute, pingTTL: fiveMins, turnAt: 30 * time.Minute, turn10Usage: read10}.run()
	if off.Phase != PhaseHot {
		t.Fatalf("off: phase %q, want hot (the entry is a 1h entry)", off.Phase)
	}
	inside := readTTLCase{readSets: true, writeTTL: "1h", pingAt: 21 * time.Minute, pingTTL: fiveMins, turnAt: 24 * time.Minute, turn10Usage: read10}.run()
	if inside.Phase != PhaseHot {
		t.Fatalf("inside: phase %q, want hot", inside.Phase)
	}
}

// PingTTL travels with the ping: Ping.TTL -> PingFacts.PingTTL -> EntryFacts.PingTTL.
func TestPingOpsCarriesThePingTTL(t *testing.T) {
	_, pf := PingOps(Ping{Session: "s", TTL: hour, At: t0, Window: t0}, Billing{})
	var f EntryFacts
	pf.Apply(&f)
	if pf.PingTTL != hour || f.PingTTL != hour {
		t.Fatalf("PingTTL = %v / %v, want 1h", pf.PingTTL, f.PingTTL)
	}
}

// mixedRun: turn 9 writes 10,000 tokens at minute 20 with the breakpoints bps (their TTLs mix). A ping at
// pingAt reads through pingRead with pingTTL. Turn 10 arrives at turnAt.
// w1h is the part of turn 9's write the provider billed at 1h (without it the library takes the 1h asks as
// billed at 5m).
func mixedRun(bps []Breakpoint, w1h int, pingRead Position, pingAt, pingTTL, turnAt time.Duration, u Usage) Result {
	m := msgs(6, 1000)
	sysB := []blk{b("sys", 4000)}
	f := EntryFacts{TTL: fiveMins, Lifetime: LifetimeExact, Provider: Provider{ReadSetsTTL: true}}
	ts := NewTrackers()
	req{at: t0.Add(20 * time.Minute), client: mk(nil, sysB, m), clientBPs: bps, facts: f, usage: Usage{CacheWrite: 10000, CacheWrite1h: w1h}}.run(ts)
	f.LastPing, f.PingRead, f.PingWindow, f.PingTTL = t0.Add(pingAt), ptr(pingRead), "ka:main:w1", pingTTL
	return req{at: t0.Add(turnAt), client: mk(nil, sysB, with(m, b("m6", 500))), clientBPs: []Breakpoint{bps[0], {Pos: msg(6), TTL: bps[1].TTL}},
		facts: f, usage: u}.run(ts)
}

var headHourTailFive = []Breakpoint{{Pos: sys(0), TTL: "1h"}, {Pos: msg(5), TTL: "5m"}}

// A 1h head and a 5m tail. A 1h ping reads both. The return at minute 45 is past the tail's 5m, so the ping
// kept the tail alive: one rescue for the tail only (6,000 tokens at the 5m write tier), not for the head.
func TestReadSetsTTLOneHourPingRescuesTheTailOfAMixedEntry(t *testing.T) {
	res := mixedRun(headHourTailFive, 4000, msg(5), 24*time.Minute, hour, 45*time.Minute, read10)
	if res.Phase != PhaseHot {
		t.Fatalf("phase %q, want hot", res.Phase)
	}
	if len(res.CacheEffects) != 1 {
		t.Fatalf("effects %+v, want the tail rescue only", res.CacheEffects)
	}
	e := effect(t, res, EffectRescue)
	if e.Saved != (TierTokens{CacheWrite5m: 6000}) || e.Caused != (TierTokens{CacheRead: 6000}) || !samePos(e.To, ptr(msg(5))) {
		t.Fatalf("rescue %+v, want 6000 write->read to messages[5]", e)
	}
}

// The same entry, but the ping asked 5m and the return comes 26 minutes after it: the ping set both parts to
// 5m, which ended. No tail rescue, and the head is dead too.
func TestReadSetsTTLAFiveMinutePingEndsBothPartsOfAMixedEntry(t *testing.T) {
	res := mixedRun(headHourTailFive, 4000, msg(5), 24*time.Minute, fiveMins, 50*time.Minute, wrote10)
	if res.Phase != PhaseCold {
		t.Fatalf("phase %q, want cold", res.Phase)
	}
	for _, e := range res.CacheEffects {
		if e.Kind == EffectRescue {
			t.Fatalf("rescue: %+v", e)
		}
	}
}

// A 5m ping reads the head only, and the return is 7 minutes after it. The head is dead (set to 5m). The tail
// the ping did not read keeps its own 1h and is alive, so the entry is hot. If the ping marked every part, the
// phase would be cold.
func TestReadSetsTTLAPingMarksOnlyThePartsItRead(t *testing.T) {
	bps := []Breakpoint{{Pos: sys(0), TTL: "1h"}, {Pos: msg(5), TTL: "1h"}}
	res := mixedRun(bps, 10000, sys(0), 24*time.Minute, fiveMins, 31*time.Minute, wrote10)
	if res.Phase != PhaseHot {
		t.Fatalf("phase %q, want hot: the unread 1h part is alive", res.Phase)
	}
}

// With the property on, a 5m ping on a 1h entry and a billed read: the read proves the entry was alive, and it
// beats the clock (R45). The phase is hot, with no rescue.
func TestReadSetsTTLABilledReadBeatsTheFiveMinutePingClock(t *testing.T) {
	res := readTTLCase{readSets: true, writeTTL: "1h", pingAt: 21 * time.Minute, pingTTL: fiveMins, turnAt: 30 * time.Minute, turn10Usage: read10}.run()
	if res.Phase != PhaseHot {
		t.Fatalf("phase %q, want hot", res.Phase)
	}
	noEffects(t, res)
}

// A ping older than the previous request kept nothing: that request wrote the entry after it. PingTTL is
// ignored, so the result equals the one with no PingTTL (R45).
func TestReadSetsTTLAStalePingIsIgnored(t *testing.T) {
	run := func(pingTTL time.Duration) Result {
		m := msgs(6, 1000)
		sysB := []blk{b("sys", 4000)}
		f := EntryFacts{TTL: fiveMins, Lifetime: LifetimeExact, Provider: Provider{ReadSetsTTL: true}}
		ts := NewTrackers()
		req{at: t0.Add(20 * time.Minute), client: mk(nil, sysB, m), clientBPs: bps5(sys(0), msg(5)), facts: f, usage: Usage{CacheWrite: 10000}}.run(ts)
		f.LastPing, f.PingRead, f.PingWindow, f.PingTTL = t0.Add(10*time.Minute), ptr(msg(5)), "ka:main:w1", pingTTL
		return req{at: t0.Add(45 * time.Minute), client: mk(nil, sysB, with(m, b("m6", 500))), clientBPs: bps5(sys(0), msg(6)),
			facts: f, usage: wrote10}.run(ts)
	}
	got, want := run(hour), run(0)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("a stale ping changed the result:\n%+v\n%+v", got, want)
	}
	if got.Phase != PhaseCold {
		t.Fatalf("phase %q, want cold", got.Phase)
	}
}
