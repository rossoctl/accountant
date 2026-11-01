package accountant_test

import (
	"fmt"
	"strings"
	"time"

	"github.com/rossoctl/accountant"
)

// body hashes one request body: one entry per message, in order.
func body(sizes ...int) accountant.Body {
	msgs := make([][]byte, len(sizes))
	for i, n := range sizes {
		msgs[i] = []byte(fmt.Sprintf("m%d", i) + strings.Repeat(".", n))
	}
	return accountant.NewBody(nil, nil, msgs)
}

// An operator shortens a tool output from 8,000 bytes to 1,000 on the second request. The first
// two messages are already cached, so they are read in both bills. The new message would have been
// written to the cache, so the saving is the tokens it did not write, at the 5-minute write tier.
func Example() {
	ts := accountant.NewTrackers()
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	facts := accountant.EntryFacts{TTL: 5 * time.Minute, Lifetime: accountant.LifetimeExact}
	thread := accountant.Thread{ID: "main"}
	last := accountant.Position{Section: accountant.SectionMessages, Index: 1}

	// Request 1: nothing changed, the provider wrote the cache.
	first := body(2000, 2000)
	r1 := ts.BeginRequest(accountant.RequestMeta{RequestID: "r1", Session: "s1", At: t0}, first,
		[]accountant.Breakpoint{{Pos: last, TTL: "5m"}})
	r1.EndRequest(thread, first, []accountant.Breakpoint{{Pos: last, TTL: "5m"}}, facts)
	r1.Compute(accountant.Usage{CacheWrite: 4000})

	// Request 2: the client sent 8,000 bytes at message 2; an operator forwarded 1,000.
	client, forwarded := body(2000, 2000, 8000), body(2000, 2000, 1000)
	at := accountant.Position{Section: accountant.SectionMessages, Index: 2}
	r2 := ts.BeginRequest(accountant.RequestMeta{RequestID: "r2", Session: "s1", At: t0.Add(10 * time.Second)},
		client, []accountant.Breakpoint{{Pos: at, TTL: "5m"}})
	r2.Add(accountant.Operation{ID: "op1", Kind: accountant.KindRewrite, Operator: "mask", ChangeID: "mask:1", At: at,
		SizeBefore: accountant.Size{Bytes: 8000}, SizeAfter: accountant.Size{Bytes: 1000}})
	r2.EndRequest(thread, forwarded, []accountant.Breakpoint{{Pos: at, TTL: "5m"}}, facts)
	res := r2.Compute(accountant.Usage{CacheRead: 4000, CacheWrite: 1000})

	p := res.Ops[0]
	fmt.Printf("saved %+v\ncaused %+v\nchecks %d\n", p.Saved, p.Caused, len(res.Checks))
	// Output:
	// saved {Input:0 CacheWrite5m:6992 CacheWrite1h:0 CacheRead:0}
	// caused {Input:0 CacheWrite5m:0 CacheWrite1h:0 CacheRead:0}
	// checks 0
}
