package engine

import (
	"fmt"
	"testing"
	"time"
)

// The oracle prices a request by simulating the provider on two bodies: the forwarded one with the
// forwarded breakpoints (the bill), and the client one with the client breakpoints (the baseline). It
// shares no code with Compute. The true saving of the request is baseline minus bill, in input-token
// units (input 1.0, 5m write 1.25, 1h write 2.0, read 0.1). Compute must return the same figure as
// the sum of Saved - Caused over its operations and counted cache effects. Check 6 does not see a
// move between tiers, so this is the check for it (accountant#49).
//
// The scenario is fixed: five blocks, tools t (500), system sys (1000), messages m0, m1, m2 (1000 each).
// One token is one byte. A token amount is multiplied by 20 so that all rates are integers.

type oracleBP struct {
	at  int // 0 t, 1 sys, 2 m0, 3 m1, 4 m2
	ttl string
}

type tierCount struct{ in, w5, w1, rd int }

func (t tierCount) units() int { return 20*t.in + 25*t.w5 + 40*t.w1 + 2*t.rd }

func weightUnits(t TierTokens) int {
	return 20*t.Input + 25*t.CacheWrite5m + 40*t.CacheWrite1h + 2*t.CacheRead
}

// cacheEntry is what an earlier request left: the identity of each block, and the blocks it marked.
type cacheEntry struct {
	ident []string
	bps   []int
}

// provider simulates the cache of the provider for one request. A block is read if an earlier entry
// holds the prefix up to a block at or before the last breakpoint. A block up to the last breakpoint
// that is not read is written, at the TTL of the first breakpoint at or after it. The rest is input.
func provider(tok []int, ident []string, bps []oracleBP, prev *cacheEntry) tierCount {
	last := -1
	for _, b := range bps {
		last = max(last, b.at)
	}
	readEnd := -1
	if prev != nil {
		for _, p := range prev.bps {
			if p > last || p >= len(ident) {
				continue
			}
			ok := true
			for i := 0; i <= p; i++ {
				ok = ok && i < len(prev.ident) && prev.ident[i] == ident[i]
			}
			if ok {
				readEnd = max(readEnd, p)
			}
		}
	}
	var out tierCount
	for i, n := range tok {
		switch {
		case i <= readEnd:
			out.rd += n
		case i <= last:
			ttl, first := "", len(tok)
			for _, b := range bps {
				if b.at >= i && b.at < first {
					first, ttl = b.at, b.ttl
				}
			}
			if ttl == "1h" {
				out.w1 += n
			} else {
				out.w5 += n
			}
		default:
			out.in += n
		}
	}
	return out
}

var oracleNames = []string{"t", "sys", "m0", "m1", "m2"}

func oraclePos(i int) Position {
	switch i {
	case 0:
		return tool(0)
	case 1:
		return sys(0)
	}
	return msg(i - 2)
}

func oracleBody(tok []int, upTo int) Body {
	var tools, system, ms []blk
	for i := 0; i <= upTo && i < len(tok); i++ {
		x := blk{oracleNames[i], tok[i]}
		switch {
		case i == 0:
			tools = append(tools, x)
		case i == 1:
			system = append(system, x)
		default:
			ms = append(ms, x)
		}
	}
	return mk(tools, system, ms)
}

func oracleBPs(l []oracleBP) []Breakpoint {
	out := make([]Breakpoint, len(l))
	for i, b := range l {
		out[i] = Breakpoint{Pos: oraclePos(b.at), TTL: b.ttl}
	}
	return out
}

func oracleIdent(tok []int) []string {
	out := make([]string, len(tok))
	for i, n := range tok {
		out[i] = fmt.Sprintf("%s:%d", oracleNames[i], n)
	}
	return out
}

// oracleScenario is one combination of the table.
type oracleScenario struct {
	name      string
	client    []oracleBP
	headTTL   bool
	hostMark  *oracleBP
	op        string // "", "add-t", "add-m1", "rem-m1"
	warmUpTo  int    // -1 cold; else the previous request held blocks 0..warmUpTo and marked warmUpTo
	evict     string // "" the entry is intact; "lost" the provider read none of it; "partial" only up to the earlier mark
	again     bool   // the request is sent a second time: the cache then holds what the first one wrote
	clientTok []int
	fwdTok    []int
	fwdBPs    []oracleBP
}

var oracleSizes = []int{500, 1000, 1000, 1000, 1000}

// oracleWarm is the state before the request: nothing (cold), an entry that the previous request left up to
// block `at`, that entry lost or cut by the provider, or a first request of the same shape (again).
type oracleWarm struct {
	at    int
	evict string
	again bool
}

var oracleWarms = func() []oracleWarm {
	out := []oracleWarm{{at: -1}, {at: -1, again: true}}
	for at := 0; at <= 4; at++ {
		out = append(out, oracleWarm{at: at}, oracleWarm{at: at, evict: "lost"}, oracleWarm{at: at, evict: "partial"})
	}
	return out
}()

func oracleScenarios() []oracleScenario {
	clients := map[string][]oracleBP{
		"c[sys,m0]": {{1, "5m"}, {2, "5m"}},
		"c[m0]":     {{2, "5m"}},
		"c[sys,m2]": {{1, "5m"}, {4, "5m"}},
		"c[t]":      {{0, "5m"}},
	}
	order := []string{"c[sys,m0]", "c[m0]", "c[sys,m2]", "c[t]"}
	var marks []*oracleBP
	marks = append(marks, nil)
	for at := 0; at < 5; at++ {
		for _, ttl := range []string{"5m", "1h"} {
			marks = append(marks, &oracleBP{at, ttl})
		}
	}
	var out []oracleScenario
	for _, cn := range order {
		for _, head := range []bool{false, true} {
			for _, mk := range marks {
				for _, op := range []string{"", "add-t", "add-m1", "rem-m1"} {
					for _, wv := range oracleWarms {
						warm, ev, again := wv.at, wv.evict, wv.again
						s := oracleScenario{client: clients[cn], headTTL: head, hostMark: mk, op: op, warmUpTo: warm, evict: ev, again: again}
						s.clientTok = append([]int(nil), oracleSizes...)
						s.fwdTok = append([]int(nil), s.clientTok...)
						switch op {
						case "add-t":
							s.fwdTok[0] += oracleSizes[0]
						case "add-m1":
							s.fwdTok[3] += oracleSizes[3] / 2
						case "rem-m1":
							s.fwdTok[3] -= oracleSizes[3] / 2
						}
						// forwarded breakpoints: the client's, the head at 1h, the host's
						seen := map[int]int{}
						for _, b := range s.client {
							s.fwdBPs = append(s.fwdBPs, b)
							seen[b.at] = len(s.fwdBPs) - 1
						}
						if head {
							if i, ok := seen[1]; ok {
								s.fwdBPs[i].ttl = "1h"
							} else {
								s.fwdBPs = append(s.fwdBPs, oracleBP{1, "1h"})
								seen[1] = len(s.fwdBPs) - 1
							}
						}
						if mk != nil {
							if _, dup := seen[mk.at]; dup {
								continue
							}
							s.fwdBPs = append(s.fwdBPs, *mk)
						}
						ms := "no-mark"
						if mk != nil {
							ms = fmt.Sprintf("mark-%s-%s", oracleNames[mk.at], mk.ttl)
						}
						w := "cold"
						if warm >= 0 {
							w = "warm-" + oracleNames[warm]
						}
						if ev != "" {
							w += "-" + ev
						}
						if again {
							w += "-again"
						}
						s.name = fmt.Sprintf("%s/head=%v/%s/op=%s/%s", cn, head, ms, op, w)
						out = append(out, s)
					}
				}
			}
		}
	}
	return out
}

// runOracle runs the scenario through Compute and returns the result, the library's net and the
// oracle's net, both in 1/20 input-token units.
//
// The previous request marks warmUpTo, and also the block before it when there is one (so that "partial"
// can cut the entry back to the earlier mark). An evicted entry is gone for the forwarded body and for the
// client body alike: the baseline is what the client body would have been billed on the same cache.
// With again, the request is first sent once with no operation reported, and the cache then holds what the
// first one wrote: for the forwarded body its own marks, for the client body the client's marks.
func runOracle(s oracleScenario) (res Result, got, want int) {
	ts := NewTrackers()
	var prevF, prevC *cacheEntry
	if s.warmUpTo >= 0 {
		tok := s.clientTok[:s.warmUpTo+1]
		var bps []oracleBP
		if s.warmUpTo > 0 {
			bps = append(bps, oracleBP{s.warmUpTo - 1, "5m"})
		}
		bps = append(bps, oracleBP{s.warmUpTo, "5m"})
		u := provider(tok, oracleIdent(s.clientTok)[:s.warmUpTo+1], bps, nil)
		b := oracleBody(s.clientTok, s.warmUpTo)
		req{at: t0, client: b, clientBPs: oracleBPs(bps), facts: exact5m,
			usage: Usage{Input: u.in, CacheWrite: u.w5 + u.w1, CacheWrite1h: u.w1, CacheRead: u.rd}}.run(ts)
		entry := &cacheEntry{ident: oracleIdent(s.clientTok)[:s.warmUpTo+1]}
		for _, x := range bps {
			entry.bps = append(entry.bps, x.at)
		}
		switch s.evict {
		case "lost":
			entry = nil
		case "partial":
			entry.bps = entry.bps[:len(entry.bps)-1]
			if len(entry.bps) == 0 {
				entry = nil
			}
		}
		prevF, prevC = entry, entry
	}
	var ops []Operation
	if s.headTTL {
		ops = append(ops, rewrite("head", "headttl", "headttl:s1:sys", sys(0), s.clientTok[1], s.clientTok[1]))
	}
	switch s.op {
	case "add-t":
		ops = append(ops, rewrite("add", "host.add", "host:add:t", tool(0), s.clientTok[0], s.fwdTok[0]))
	case "add-m1":
		ops = append(ops, rewrite("add", "host.add", "host:add:m1", msg(1), s.clientTok[3], s.fwdTok[3]))
	case "rem-m1":
		ops = append(ops, rewrite("rem", "host.rem", "host:rem:m1", msg(1), s.clientTok[3], s.fwdTok[3]))
	}
	facts := exact5m
	for _, b := range s.fwdBPs {
		if b.ttl == "1h" {
			facts = EntryFacts{TTL: time.Hour, Lifetime: LifetimeExact}
		}
	}
	at := t0.Add(10 * time.Second)
	step := func(prevF, prevC *cacheEntry, ops []Operation, at time.Time) (Result, tierCount, tierCount) {
		use := provider(s.fwdTok, oracleIdent(s.fwdTok), s.fwdBPs, prevF)
		base := provider(s.clientTok, oracleIdent(s.clientTok), s.client, prevC)
		r := req{at: at, client: oracleBody(s.clientTok, 4), clientBPs: oracleBPs(s.client), ops: ops,
			fwd: oracleBody(s.fwdTok, 4), fwdBPs: oracleBPs(s.fwdBPs), facts: facts,
			usage: Usage{Input: use.in, CacheWrite: use.w5 + use.w1, CacheWrite1h: use.w1, CacheRead: use.rd}}.run(ts)
		return r, use, base
	}
	if s.again {
		step(nil, nil, ops, at) // the first send: it reports the operations, and the cache keeps its marks
		prevF = &cacheEntry{ident: oracleIdent(s.fwdTok)}
		for _, b := range s.fwdBPs {
			prevF.bps = append(prevF.bps, b.at)
		}
		prevC = &cacheEntry{ident: oracleIdent(s.clientTok)}
		for _, b := range s.client {
			prevC.bps = append(prevC.bps, b.at)
		}
		ops, at = nil, at.Add(10*time.Second)
	}
	r, use, base := step(prevF, prevC, ops, at)
	want = base.units() - use.units()
	res = r
	for _, p := range res.Ops {
		if p.Call == nil {
			got += weightUnits(p.Saved) - weightUnits(p.Caused)
		}
	}
	for _, e := range res.CacheEffects {
		if !e.InBaseline {
			got += weightUnits(e.Saved) - weightUnits(e.Caused)
		}
	}
	return res, got, want
}

// forwardedHasExtraMark says whether the forwarded body carries a breakpoint at a position the client body
// does not mark.
func (s oracleScenario) forwardedHasExtraMark() bool {
	client := map[int]bool{}
	for _, b := range s.client {
		client[b.at] = true
	}
	for _, b := range s.fwdBPs {
		if !client[b.at] {
			return true
		}
	}
	return false
}

// knownLow says whether a combination is one where the library books less saving than the truth, by
// design or by an earlier limit that this change does not touch. In all of them the error is on the safe
// side (R45). Everything outside these classes must be exact, and no combination may be higher than the
// truth. The classes are narrow on purpose:
//
//  1. READ NOT BOOKED. The cache holds an entry that the forwarded body can read and the client body
//     cannot: the previous request ended at a block after the client's last breakpoint (warm), or it is the
//     same request sent again and the host's mark is one the client lacks (again). The read is a saving
//     the library does not book (the inverse effect, accountant#49 open questions).
//  2. ADDED CONTENT AT THE FIRST BLOCK of a warm request. It sits inside a prefix the break already moved,
//     and the break and the operation price it at different tiers. The base commit books the same figures.
//  3. EVICTION BESIDE AN OPERATION IN THE PREFIX. The entry was lost for both bodies, and an operation
//     changes m1. The usage (a write where a read was expected) looks the same as a break the operation
//     caused, so the library books that break. The usage cannot tell the two apart. The base commit books
//     the same break.
func knownLow(s oracleScenario) bool {
	if s.again {
		// A repeat leaves the client body unable to read the entry only when the forwarded body has a
		// breakpoint the client lacks: the host's mark, or the head mark of a headttl change.
		return s.forwardedHasExtraMark()
	}
	if s.warmUpTo < 0 {
		return false
	}
	last := 0
	for _, b := range s.client {
		last = max(last, b.at)
	}
	switch {
	case s.warmUpTo > last:
		return true
	case s.op == "add-t":
		return true
	case s.evict != "" && (s.op == "add-m1" || s.op == "rem-m1"):
		return true
	}
	return false
}

// TestEveryTierMoveIsBookedOnce is the conservation check in input-token units with the tier rates:
// the library's net equals the true bill difference for every combination of the table. A combination in
// knownLow may only be lower than the truth. No combination may be higher: that would overstate a saving.
func TestEveryTierMoveIsBookedOnce(t *testing.T) {
	runOracleTable(t, []int{500, 1000, 1000, 1000, 1000})
}

// TestEveryTierMoveIsBookedOnceForOtherSizes runs the same table with block sizes drawn from a fixed seed,
// so the identity is not tied to one set of round numbers. The sizes are multiples of 100 to keep the
// operations' halves whole.
func TestEveryTierMoveIsBookedOnceForOtherSizes(t *testing.T) {
	seed := uint64(49)
	next := func() int {
		seed = seed*6364136223846793005 + 1442695040888963407
		return 200 + 100*int((seed>>33)%30)
	}
	for k := 0; k < 4; k++ {
		sizes := []int{next(), next(), next(), next(), next()}
		t.Run(fmt.Sprint(sizes), func(t *testing.T) { runOracleTable(t, sizes) })
	}
}

func runOracleTable(t *testing.T, sizes []int) {
	oracleSizes = sizes
	defer func() { oracleSizes = []int{500, 1000, 1000, 1000, 1000} }()
	var exact, low int
	total := 0
	for _, s := range oracleScenarios() {
		total++
		res, got, want := runOracle(s)
		for _, c := range res.Checks {
			t.Errorf("%s: check %s failed: %s", s.name, c.Check, c.Detail)
		}
		switch {
		case got == want:
			exact++
		case got > want:
			t.Errorf("%s: net %d/20 units is above the truth %d/20 (saving overstated by %d)", s.name, got, want, got-want)
		case knownLow(s):
			low++
		default:
			t.Errorf("%s: net %d/20 units, want %d/20 (understated by %d)", s.name, got, want, want-got)
		}
	}
	t.Logf("%d combinations: %d exact, %d lower than the truth in the three known classes, 0 higher", total, exact, low)
}
