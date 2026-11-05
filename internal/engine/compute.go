package engine

import (
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"
)

// Machine-stable reasons in Result.Unpriced.
const (
	ReasonEndRequestMissing = "end_request_missing"
	ReasonPingResultUnknown = "ping_result_unknown" // a spend whose call has no usage: the host never read a response (§6.3)
	ReasonUnknownKind       = "unknown_kind"
	ReasonNegativeSize      = "negative_size"
	ReasonPenaltyUnknown    = "penalty_unknown" // the boundary is unknown, so no break penalty was charged (§6.4)
	ReasonNoUsage           = "no_usage"        // the response had no usage: nothing is priced (§8)
	ReasonNotSpend          = "not_spend"       // a standalone RequestOps holds only spend operations (§6.1)
	ReasonBadUsage          = "bad_usage"       // a usage count is negative: nothing is priced (§6.5, check 8)
	ReasonRatioRange        = "ratio_range"     // tokens per byte is outside the range: nothing is priced (§6.5, check 9)
	ReasonMediaTokens       = "media_tokens"    // the operation covers media, and the bill is below the media price: only this operation is unpriced (§6.5, check 11)
	ReasonBreakpoints       = "breakpoints"     // more than 4 breakpoints, or an unknown TTL: nothing is priced (§6.5, check 10)
	ReasonSizeRange         = "size_range"      // an operation reports more bytes than the client body holds: that operation is not priced (§6.5, check 13)
)

// Compute prices the request and then replaces the thread's ThreadState (§6.1). It is
// deterministic and does no I/O (R26). If it panics, the ThreadState is unchanged, because the
// replace is its last step. If another request replaced the ThreadState after EndRequest, Compute
// calculates again from the current one and sets Result.Recomputed (R53).
//
// Call it once for each request. A second call prices the request against the ThreadState the first
// call stored, so the active changes count again (the second result is usually phase hot). Do not
// call Compute again to retry after a transient error in the host: the first call already advanced
// the state. Use Preview to price a request again, because it stores nothing. Compute does
// not recover from a panic: the host wraps the call so that a failure leaves the request unpriced.
func (r *Request) Compute(usage Usage) Result {
	if !r.ended {
		return r.notEnded()
	}
	t := r.trackers.tracker(ThreadKey(r.Meta.Session, r.Thread), r.Meta.At)
	for {
		s := t.snapshot()
		res, next := r.calc(s, usage)
		if t.replace(s.Version, next) {
			r.warnChecks(res)
			return res
		}
	}
}

// warnChecks logs each failed check (§6.5) at warning level, once for each request: Compute calls it
// after the replace succeeded, so a retry does not log twice, and Preview never logs. It is the only
// output of the package. Detail holds numbers only, so no content is logged.
func (r *Request) warnChecks(res Result) {
	for _, f := range res.Checks {
		slog.Warn("accountant check failed", "check", f.Check, "operator", f.Operator,
			"detail", f.Detail, "request", r.Meta.RequestID, "session", r.Meta.Session, "thread", r.Thread.ID)
	}
}

// Preview does the same calculation as Compute and stores nothing (§6.1).
func (r *Request) Preview(usage Usage) Result {
	if !r.ended {
		return r.notEnded()
	}
	res, _ := r.calc(r.trackers.tracker(ThreadKey(r.Meta.Session, r.Thread), r.Meta.At).snapshot(), usage)
	return res
}

// ComputeStandalone prices a RequestOps that holds only spend operations (a keep-alive ping or a
// detached call). It reads and changes no ThreadState (§6.1). A spend is not priced in tokens: its
// Call is returned for the host to price (D4). Any other kind is unpriced.
func ComputeStandalone(ops RequestOps) Result {
	res := Result{Version: 1, Phase: PhaseUnknown}
	for _, op := range ops.Ops {
		res.Ops = append(res.Ops, priced(op, op.ID))
		if op.Kind != KindSpend {
			res.Unpriced = append(res.Unpriced, Unpriced{OpID: op.ID, Reason: ReasonNotSpend})
		} else if resultUnknown(op) {
			res.Unpriced = append(res.Unpriced, Unpriced{OpID: op.ID, Reason: ReasonPingResultUnknown})
		} else if badCall(op) {
			res.Unpriced = append(res.Unpriced, Unpriced{OpID: op.ID, Reason: ReasonBadUsage})
			res.Checks = append(res.Checks, CheckFailure{Check: ReasonBadUsage, Operator: op.Operator, Detail: callDetail(op)})
		}
	}
	return res
}

// maxTierTokens is the largest count one Usage field may hold (check 8). It is far above any real
// prompt, and three of them cannot overflow an int.
const maxTierTokens int64 = 1 << 40

// badUsage says whether a usage is negative, above the cap, or has CacheWrite1h above CacheWrite (check 8).
func badUsage(u Usage) bool {
	for _, n := range []int{u.Input, u.CacheRead, u.CacheWrite, u.CacheWrite1h} {
		if n < 0 || int64(n) > maxTierTokens {
			return true
		}
	}
	return u.CacheWrite1h > u.CacheWrite
}

// badCall says whether a spend's Call states a figure that cannot be real (check 8): a bad usage,
// a negative Output, or a CostUSD that is negative, NaN or infinite.
func badCall(op Operation) bool {
	if op.Kind != KindSpend || op.Call == nil {
		return false
	}
	u := op.Call.Usage
	return badUsage(u.Usage) || u.Output < 0 || int64(u.Output) > maxTierTokens || u.CostUSD < 0 || math.IsNaN(u.CostUSD) || math.IsInf(u.CostUSD, 0)
}

func callDetail(op Operation) string {
	u := op.Call.Usage
	return fmt.Sprintf("call usage=%d/%d/%d/%d output=%d costUSD=%g", u.Input, u.CacheRead, u.CacheWrite, u.CacheWrite1h, u.Output, u.CostUSD)
}

// limits are the two bounds of check 13, in bytes.
//
//   - before: the most an operation can have received. An operator receives the body the operators before
//     it left, which is the client body plus what the inserting operators added. So the bound is the
//     client total plus the sum of max(0, SizeAfter.Bytes - SizeBefore.Bytes) over the rewrites, those
//     reported now and those applied again.
//   - after: the most an operation can have produced. Its output either stays in the forwarded body or
//     is removed by a later operator. So the bound is the forwarded total plus the sum of
//     max(0, SizeBefore.Bytes - SizeAfter.Bytes) over the rewrites. Before EndRequest the client total
//     stands in for the forwarded one.
//
// A report that fails a bound adds to neither sum: one bogus report must not raise the bound for itself
// or for the others. So the bounds are found by repetition from the loose side: start with every active
// operation and every non-negative report, drop the reports that fail, and repeat until none drops (at
// most one drop for each operation). Every pass counts a set that holds all the honest operations, so its
// bound is never below the true one and a drop is always a wrong report. For that reason an active
// operation is never taken out of the sums, even when a report with its ChangeID replaces it: whether
// that report is honest is what the pass is deciding. The cost is a bound that is looser by the size of
// a replaced operation, and a loose bound only lets a wrong report through, never unprices an honest one.
// A DryRun operation changed no byte, so it adds to neither sum, and only its SizeBefore is bounded.
// Sums saturate.
type limits struct{ before, after int }

// storedCopy says whether the active operation is the copy EndRequest stored of a report of this request:
// a report with its ChangeID and the same sizes. The report counts in the sums, so the copy must not
// count twice. An active operation that a report with other sizes replaces still counts (see above).
func storedCopy(act Operation, reported []Operation) bool {
	same := func(a, b Operation) bool {
		if a.SizeBefore.Bytes != b.SizeBefore.Bytes || a.SizeAfter.Bytes != b.SizeAfter.Bytes || len(a.Spans) != len(b.Spans) {
			return false
		}
		for i := range a.Spans {
			if a.Spans[i].SizeBefore.Bytes != b.Spans[i].SizeBefore.Bytes || a.Spans[i].SizeAfter.Bytes != b.Spans[i].SizeAfter.Bytes {
				return false
			}
		}
		return true
	}
	for _, op := range reported {
		if op.ChangeID != "" && op.ChangeID == act.ChangeID && same(op, act) && !negative(op) {
			return true
		}
	}
	return false
}

func (r *Request) limits(s ThreadState) limits {
	client := r.Client.cumBytes(len(r.Client.Pos) - 1)
	fwd := client
	if r.ended {
		fwd = r.Forwarded.cumBytes(len(r.Forwarded.Pos) - 1)
	}
	type entry struct {
		op     Operation
		active bool
		out    bool // dropped: it failed a bound
	}
	var es []*entry // the rewrites: the active ones, then the reported ones
	for _, op := range s.Active {
		if !storedCopy(op, r.Ops) {
			es = append(es, &entry{op: op, active: true})
		}
	}
	for _, op := range r.Ops {
		es = append(es, &entry{op: op})
	}
	sat := func(a, d int) int {
		if a > math.MaxInt-d {
			return math.MaxInt
		}
		return a + d
	}
	var l limits
	for {
		l = limits{before: client, after: fwd}
		for _, e := range es {
			if e.out || e.op.Kind != KindRewrite || e.op.DryRun || negative(e.op) {
				continue
			}
			add := func(b, a Size) {
				if d := a.Bytes - b.Bytes; d > 0 {
					l.before = sat(l.before, d)
				} else if d < 0 {
					l.after = sat(l.after, -d)
				}
			}
			if len(e.op.Spans) == 0 {
				add(e.op.SizeBefore, e.op.SizeAfter)
			}
			for _, sp := range e.op.Spans {
				add(sp.SizeBefore, sp.SizeAfter)
			}
		}
		dropped := false
		for _, e := range es {
			if !e.out && !e.active && tooBig(e.op, l) {
				e.out, dropped = true, true
			}
		}
		if !dropped {
			return l
		}
	}
}

// tooBig says whether a rewrite reports a size above a bound of check 13, on the operation or on a span.
func tooBig(op Operation, l limits) bool {
	_, ok := bigSize(op, l)
	return ok
}

// bigSize returns the first size pair above a bound: the operation's, or the span's that failed.
func bigSize(op Operation, l limits) ([2]Size, bool) {
	if op.Kind != KindRewrite {
		return [2]Size{}, false
	}
	// A DryRun operation read a real body, so its SizeBefore is bounded. Its SizeAfter is a guess that
	// never reaches the totals (R12), so only check 12 looks at it.
	over := func(b, a Size) bool { return b.Bytes > l.before || (!op.DryRun && a.Bytes > l.after) }
	if over(op.SizeBefore, op.SizeAfter) {
		return [2]Size{op.SizeBefore, op.SizeAfter}, true
	}
	for _, sp := range op.Spans {
		if over(sp.SizeBefore, sp.SizeAfter) {
			return [2]Size{sp.SizeBefore, sp.SizeAfter}, true
		}
	}
	return [2]Size{}, false
}

// malformed says whether a report is a bug: a negative size, or a size above limit. A malformed report
// is priced as nothing and replaces nothing.
func malformed(op Operation, l limits) bool { return negative(op) || tooBig(op, l) }

// resultUnknown says whether a spend's call has no usage at all: the call was sent and may have been
// billed, but the host has no response to read the bill from. The cost is then missing, not zero.
func resultUnknown(op Operation) bool {
	return op.Kind == KindSpend && op.Call != nil && op.Call.Usage == (CallUsage{})
}

func (r *Request) notEnded() Result {
	res := Result{Version: 1, Phase: PhaseUnknown, Unpriced: []Unpriced{{Reason: ReasonEndRequestMissing}}}
	for _, op := range r.Ops {
		res.Ops = append(res.Ops, priced(op, op.ID))
		res.Unpriced = append(res.Unpriced, Unpriced{OpID: op.ID, Reason: ReasonEndRequestMissing})
	}
	return res
}

func priced(op Operation, id string) PricedOp {
	return PricedOp{OpID: id, Operator: op.Operator, ChangeID: op.ChangeID, DryRun: op.DryRun, Call: op.Call}
}

// item is one operation of the request: reported now, or active from an earlier request.
type item struct {
	op     Operation
	id     string
	active bool
	spans  []span // in client space
	ended  bool   // a later splice removed it, or a client edit took its position away (§4.5)
	bad    string // the Unpriced reason when the operation cannot be priced
}

type span struct {
	from, to      Position
	before, after Size
}

func opSpans(op Operation) []span {
	if len(op.Spans) == 0 {
		return []span{{op.At, op.At, op.SizeBefore, op.SizeAfter}}
	}
	out := make([]span, len(op.Spans))
	for i, s := range op.Spans {
		out[i] = span{Position{s.Section, s.From}, Position{s.Section, s.To}, s.SizeBefore, s.SizeAfter}
	}
	return out
}

// calc prices the request against the state s and returns the result and the next state. It
// changes nothing it is given.
func (r *Request) calc(s ThreadState, usage Usage) (Result, ThreadState) {
	res := Result{Version: 1, Recomputed: s.Version != r.start.Version}
	s, seeded := r.seededFromParent(s)
	items := r.items(s)
	c := r.newCalc(s, items, usage)
	c.seeded = seeded

	c.phaseAndBoundary(&res)
	if !c.skipsPricing() {
		c.cacheEffects(&res)
	}
	c.price(&res)
	c.checks(&res)
	c.checkUsage(&res)
	c.conservation(&res)
	return res, c.nextState()
}

// items builds the operation list in pipeline order: the active operations first, as they ran on
// earlier requests, then the reported ones. A reported operation with the ChangeID of an active one
// replaces it (the observe-to-enforce switch, §4.6, or an operator that changes its own change).
func (r *Request) items(s ThreadState) []*item {
	l := r.limits(s)
	reported := map[string]bool{} // a spend may share the ChangeID of its change (§6.5 check 3)
	for _, op := range r.Ops {
		if isChange(op, l) {
			reported[op.ChangeID] = true
		}
	}
	var items []*item
	for _, op := range s.Active {
		if !reported[op.ChangeID] {
			items = append(items, &item{op: op, id: "active:" + op.ChangeID, active: true, spans: opSpans(op)})
		}
	}
	for _, op := range r.Ops {
		it := &item{op: op, id: op.ID}
		switch {
		case op.Kind != KindRewrite && op.Kind != KindSpend && op.Kind != KindSetting:
			it.bad = ReasonUnknownKind
		case negative(op):
			it.bad = ReasonNegativeSize
		case tooBig(op, l):
			it.bad = ReasonSizeRange
		case badCall(op):
			it.bad = ReasonBadUsage
		}
		items = append(items, it)
	}
	// A reported operation's positions are in its own output: undo the splices up to and
	// including its own to reach client space (§4.3). Active ones are stored in client space.
	for k, it := range items {
		if it.active {
			continue
		}
		it.spans = opSpans(it.op)
		for i := range it.spans {
			for j := k; j >= 0; j-- {
				if j < k && (items[j].op.DryRun || items[j].bad != "") {
					continue // it changed nothing upstream
				}
				it.spans[i].from = undo(it.spans[i].from, items[j].op.MessageSplice)
				it.spans[i].to = undo(it.spans[i].to, items[j].op.MessageSplice)
			}
		}
	}
	return items
}

// isChange reports whether op is a change the Tracker keeps and replays: a rewrite or a setting with a
// ChangeID, and a well-formed size (not negative, not above a bound of check 13). A malformed report is not a change, so it must not replace an
// active one (§4.6). items and EndRequest both use it, so they cannot disagree.
func isChange(op Operation, l limits) bool {
	return op.ChangeID != "" && (op.Kind == KindRewrite || op.Kind == KindSetting) && !malformed(op, l)
}

func negative(op Operation) bool {
	if op.SizeBefore.invalid() || op.SizeAfter.invalid() {
		return true
	}
	for _, s := range op.Spans {
		if s.SizeBefore.invalid() || s.SizeAfter.invalid() {
			return true
		}
	}
	return false
}

// undo maps a position in the output of a splice back to its input.
func undo(p Position, s *MessageSplice) Position {
	if s == nil || p.Section != SectionMessages || p.Index < s.At {
		return p
	}
	if p.Index >= s.At+s.Inserted {
		p.Index += s.Removed - s.Inserted
		return p
	}
	p.Index = s.At // inside what the splice inserted: the start of what it replaced
	return p
}

// apply maps a position in the input of a splice to its output. A position that the splice
// removed maps to the start of what it inserted when into is true, and has no output otherwise.
func apply(p Position, s *MessageSplice, into bool) (Position, bool) {
	if s == nil || p.Section != SectionMessages || p.Index < s.At {
		return p, true
	}
	if p.Index >= s.At+s.Removed {
		p.Index += s.Inserted - s.Removed
		return p, true
	}
	p.Index = s.At
	return p, into
}

// plausible says whether host tokens fit the bytes of the range. media is the range's media bytes.
func plausible(s Size, media int) bool {
	text := s.Bytes - media
	if float64(s.Tokens) > maxTokensPerByte*float64(s.Bytes) {
		return false
	}
	return text <= 0 || float64(s.Tokens) >= minTokensPerByte*float64(text)
}

// rangeMedia is the media bytes a size covers. The host's MediaBytes wins. When it is 0, the media
// comes from the body that holds the range: the client body for a "before" size, and the forwarded
// body, over the range mapped by toFwd, for an "after" size. The media is the size's bytes beyond the
// range's text, clamped to 0..media. Without this an unmarked size over an image would price its
// base64 at the text ratio.
func (c *calcState) rangeMedia(k int, s Size, sp span, isBefore bool) int {
	if s.MediaBytes > 0 || s.Bytes == 0 {
		return s.MediaBytes
	}
	body, from, to := c.client, sp.from, sp.to
	if !isBefore {
		body = c.fwd
		var ok1, ok2 bool
		if sp.before.Bytes == 0 && c.items[k].op.MessageSplice != nil { // an inserted item sits in its own slot
			from, ok1 = c.toFwdSkipping(k, sp.from)
			to, ok2 = c.toFwdSkipping(k, sp.to)
		} else {
			from, ok1 = c.toFwd(k, sp.from)
			to, ok2 = c.toFwd(k, sp.to)
		}
		if !ok1 || !ok2 {
			return 0
		}
	}
	f, ok1 := body.flat(from)
	t, ok2 := body.flat(to)
	if !ok1 || !ok2 || t < f {
		return 0
	}
	media := body.cumMedia(t) - body.cumMedia(f-1)
	text := body.cumBytes(t) - body.cumBytes(f-1) - media
	return min(max(s.Bytes-text, 0), media)
}

// sizeTokens converts one reported size to tokens. A plausible size the host counted (Tokens > 0) is
// exact. Any other size is converted from its bytes with the request ratio, and so is an estimate. A
// size of zero bytes is exactly zero tokens (§4.2 rule 7). bad is true when the host's tokens were
// ignored as implausible.
func (c *calcState) sizeTokens(s Size, media int) (tokens int, exact, bad bool) {
	if s.Tokens > 0 {
		if plausible(s, media) {
			return s.Tokens, true, false
		}
		bad = true
	}
	return c.ratio.tokensOf(s.Bytes, media), s.Bytes == 0, bad
}

// calcState is the working state of one calculation.
type calcState struct {
	r        *Request
	s        ThreadState
	items    []*item
	splicers []int // indexes of the items with a MessageSplice; no other item moves a position
	usage    Usage
	noUsage  bool
	invalid  string // the reason the usage or breakpoints cannot be trusted; "" when they can (§6.5, checks 8 to 10)
	seeded   bool   // s.PrevPrint is the parent's fingerprint (seededFromParent), not this thread's own
	ratio    ratio
	mediaGap int // the ratio's figure for the ranges of operations unpriced for media_tokens (check 6)
	hostGap  int // saved tokens that come from Size.Tokens beyond the ratio's figure (check 6)

	client, fwd       Body
	clientBPs, fwdBPs []bp
	prevClientBPs     []bp // the previous request's client breakpoints, flat in this client body
	prevFwdBPs        []bp // the previous request's forwarded breakpoints, flat in this forwarded body

	phase      CachePhase
	unknown    bool // the phase was unknown before the usage was read: no break can be classified
	prevExt    int  // the previous extent the phase uses, flat in the previous forwarded body; -1 = none
	pingHot    bool // the entry is hot because a ping kept it (§5.2)
	bstar      int  // B*, flat in the client body; -1 = none
	bstarFwd   int  // B*, flat in the forwarded body; -1 = none
	readEnd    int  // what Usage.CacheRead covers, flat in the forwarded body; -1 = nothing
	cachedFwd  int  // what this request leaves cached, flat in the forwarded body; -1 = nothing
	fwdFD      int
	clientFD   int
	prevExtCli int // prevExt in client space, flat in the previous client body; -1 = none

	pass         int          // 0 before cacheEffects, 1 on the scratch pass, 2 on the real one
	countedBreak *CacheEffect // the break that is part of the bill difference, found on the scratch pass

	derivedPingRead *Position // pingRead's cache; derivedPingOK is false when the ping read nothing
	derivedPingOK   bool
}

func (r *Request) newCalc(s ThreadState, items []*item, usage Usage) *calcState {
	c := &calcState{r: r, s: s, items: items, usage: usage, client: r.Client, fwd: r.Forwarded,
		prevExt: -1, bstar: -1, bstarFwd: -1, readEnd: -1, cachedFwd: -1, prevExtCli: -1}
	c.noUsage = usage == (Usage{})
	for j, it := range items {
		if it.op.MessageSplice != nil {
			c.splicers = append(c.splicers, j)
		}
	}
	c.ratio = newRatio(usage.Input+usage.CacheRead+usage.CacheWrite, r.Forwarded)
	c.invalid = c.validate(usage)
	c.clientBPs = c.client.breakpoints(r.ClientBreakpoints)
	c.fwdBPs = c.fwd.breakpoints(r.Breakpoints)
	if p := s.PrevPrint; p != nil {
		// The previous breakpoints, as positions of this request's bodies: an entry the previous
		// request wrote at one can be read here.
		c.prevClientBPs = c.client.breakpoints(p.ClientBreakpoints)
		c.prevFwdBPs = c.fwd.breakpoints(p.ForwardedBreakpoints)
		c.fwdFD = firstDiff(p.Forwarded, c.fwd)
		c.clientFD = firstDiff(p.Client, c.client)
	}
	c.forwardPositions()
	return c
}

// forwardPositions ends the active operations whose position a later splice removed (§4.3), or
// that are no longer in the client body (a client compaction).
func (c *calcState) forwardPositions() {
	for k, it := range c.items {
		if !it.active || it.op.Kind != KindRewrite { // a setting has no position (§4.6)
			continue
		}
		for _, sp := range it.spans {
			if sp.before.Bytes == 0 {
				// Content the operator added: its position is in forwarded space, and the client body has
				// nothing there. It ends only when the client edited what precedes it (§4.3).
				if c.clientEdited(c.insertionPoint(sp.from) - 1) {
					it.ended = true
				}
			} else if i, ok := c.client.flat(sp.from); !ok || c.clientEdited(i) {
				it.ended = true // the client edited at or before it, or compacted (§4.3)
			}
			fp, ok := c.toFwd(k, sp.from)
			if !ok {
				it.ended = true
			} else if c.restored(it, sp, fp) {
				it.ended = true
			}
		}
	}
}

// clientEdited says whether the client changed something at or before flat position i, compared with
// the previous request's client body. Content the client only appended is not an edit: it matters when
// the previous fingerprint is older than the change, because a request between them never reached
// Compute (§4.6).
func (c *calcState) clientEdited(i int) bool {
	p := c.s.PrevPrint
	return p != nil && c.clientFD < len(p.Client.Pos) && c.clientFD <= i
}

// restored says whether the operator stopped applying a change that edited an item in place: the
// forwarded item at the change's position is the client's own item again, although the change made
// it smaller or larger. The end is inferred from the bodies, because the operator reports nothing
// after the first request (§4.5). A change with a splice ends through the splice (toFwd).
//
// The hashes are cumulative, so they match only while no other change sits earlier in the body. With
// two active in-place changes the later one is restored under the earlier one's smaller prefix and
// the hashes differ. The item's own size does not depend on the prefix, so the test is the size: the
// forwarded item has the client item's size again, and the change gave it another (sp.before differs
// from sp.after here). Another operator rewriting the item to exactly that size would look restored.
func (c *calcState) restored(it *item, sp span, fp Position) bool {
	if it.op.DryRun || it.op.MessageSplice != nil || sp.before.Bytes == 0 || sp.before.Bytes == sp.after.Bytes {
		return false // a DryRun forwards the client's own item by design
	}
	ci, ok1 := c.client.flat(sp.from)
	fi, ok2 := c.fwd.flat(fp)
	if !ok1 || !ok2 {
		return false
	}
	return c.client.Pos[ci].Bytes == c.fwd.Pos[fi].Bytes
}

// insertionPoint is the flat client position where content added at p sits: the position itself, or
// the end of its section when the client body has fewer items there.
func (c *calcState) insertionPoint(p Position) int {
	s := sectionIndex(p.Section)
	if s < 0 {
		return 0
	}
	return min(c.client.Start[s]+max(p.Index, 0), c.client.sectionEnd(s))
}

// toFwdSkipping is toFwd without item k's own splice. Content that k inserted is already at its
// forwarded position: applying the insertion to it would shift it past itself.
func (c *calcState) toFwdSkipping(k int, p Position) (Position, bool) {
	for _, j := range c.splicers {
		it := c.items[j]
		if j == k || it.ended || it.bad != "" || it.op.DryRun {
			continue
		}
		var ok bool
		if p, ok = apply(p, it.op.MessageSplice, j <= k || !c.sawOriginal(j)); !ok {
			return p, false
		}
	}
	return p, true
}

// toFwd maps a client position of item k into the forwarded body. The splices of the items up to
// and including k ran before or with it, so k saw what they inserted. A later splice that removed
// the position ended k, unless the splice did not see the original content (sawOriginal): then k
// ran before it, and its change is inside what the splice replaced.
func (c *calcState) toFwd(k int, p Position) (Position, bool) {
	for _, j := range c.splicers {
		it := c.items[j]
		if it.ended || it.bad != "" || it.op.DryRun {
			continue
		}
		var ok bool
		if p, ok = apply(p, it.op.MessageSplice, j <= k || !c.sawOriginal(j)); !ok {
			return p, false
		}
	}
	return p, true
}

// sawOriginal says whether the splicing item j received the client's own bytes over the range it
// removed. The library cannot see the pipeline order of an active operation and a reported one, so
// it reads it from the sizes (§4.5): an operator that ran after a reducer received fewer bytes than
// the client sent. If the guess is wrong, the sum check fails.
func (c *calcState) sawOriginal(j int) bool {
	it := c.items[j]
	sp := it.op.MessageSplice
	if it.active {
		return false // both still active: the order was decided on the first request
	}
	if sp == nil || sp.Removed == 0 {
		return true
	}
	from, to := Position{SectionMessages, sp.At}, Position{SectionMessages, sp.At + sp.Removed - 1}
	for i := j - 1; i >= 0; i-- {
		if e := c.items[i]; !e.ended && e.bad == "" && !e.op.DryRun {
			from, to = undo(from, e.op.MessageSplice), undo(to, e.op.MessageSplice)
		}
	}
	f, ok1 := c.client.flat(from)
	t, ok2 := c.client.flat(to)
	if !ok1 || !ok2 {
		return true
	}
	return it.op.SizeBefore.Bytes == c.client.cumBytes(t)-c.client.cumBytes(f-1)
}

func (c *calcState) clientToFwd(p Position) Position { q, _ := c.toFwd(len(c.items), p); return q }

func (c *calcState) fwdToClient(p Position) Position {
	for j := len(c.items) - 1; j >= 0; j-- {
		if it := c.items[j]; !it.ended && it.bad == "" && !it.op.DryRun {
			p = undo(p, it.op.MessageSplice)
		}
	}
	return p
}

func prevFwdToClient(p *Fingerprint, pos Position) Position {
	for j := len(p.splices) - 1; j >= 0; j-- {
		s := p.splices[j]
		pos = undo(pos, &s)
	}
	return pos
}

// candidates are the forwarded positions where a cache entry can end: the breakpoints of this and
// the previous request, and the previous extent. With none, every position is one.
func (c *calcState) candidates() []bp {
	if len(c.fwdBPs) == 0 {
		return nil // no forwarded breakpoint: every position is a candidate (see extent)
	}
	out := append(append([]bp(nil), c.fwdBPs...), c.prevFwdBPs...)
	if c.prevExt >= 0 {
		if i, ok := c.fwd.flat(c.s.PrevPrint.Forwarded.position(c.prevExt)); ok {
			out = append(out, bp{at: i})
		}
	}
	return out
}

// pingRead is the extent the last ping left cached, in the previous request's forwarded body:
// Facts.PingRead when the host set it, else derived from Facts.PingUsage. A ping replays that body,
// so its usage bills that body and gives its bytes-to-tokens ratio. The extent is the last position
// its CacheRead plus CacheWrite covers: a ping that found the entry gone writes it again, and that
// renews it as well as a read does (case keepalive/ping-miss-restarts-clock/5m). Every position is a
// candidate, as for a read with no breakpoint, because the ping's usage is the only evidence. Nil
// when neither is set or the ping cached nothing.
func (c *calcState) pingRead() *Position {
	f := c.r.Facts
	if f.PingRead != nil {
		return f.PingRead
	}
	prev := c.s.PrevPrint
	if f.PingUsage == nil || prev == nil || len(prev.Forwarded.Pos) == 0 {
		return nil
	}
	if c.derivedPingRead == nil {
		u := *f.PingUsage
		// CacheWrite1h is part of CacheWrite, never an addition to it.
		// Only the tokens the ping cached are in that body. Its Input after the prefix (a sweep question,
		// a summary request) is billed but is not part of the body, so it would raise the ratio and pull
		// the extent back to an earlier message.
		r := newRatio(u.CacheRead+u.CacheWrite, prev.Forwarded)
		i := -1
		if r.tokens > 0 { // a usage that bills no prompt token proves nothing about the extent
			i = extent(prev.Forwarded, r, u.CacheRead+u.CacheWrite, nil)
		}
		p := Position{}
		if i >= 0 {
			p = prev.Forwarded.position(i)
		}
		c.derivedPingRead, c.derivedPingOK = &p, i >= 0
	}
	if !c.derivedPingOK {
		return nil
	}
	return c.derivedPingRead
}

// phaseAndBoundary finds the cache phase from the gaps (§5.2) and B* (§5.1).
//
// The clock judges the entry the previous request WROTE, so it uses the TTL of each breakpoint of that
// request (Breakpoint.TTL) and EntryFacts.TTL of that request (CacheState.EntryTTL), not the TTL of this
// request (accountant#40, #41). With a 1h head and a 5m tail, the head can be alive after the tail has
// expired: the phase is then hot with the head as the previous extent.
func (c *calcState) phaseAndBoundary(res *Result) {
	prev, f := c.s.PrevPrint, c.r.Facts
	c.phase = PhaseUnknown
	entryTTL := c.s.Cache.EntryTTL
	if entryTTL <= 0 {
		entryTTL = f.TTL
	}
	// A request that started before the stored one has no valid gap: the clock says nothing about the
	// entry, so the phase is left to the usage (accountant#16). Hot from a negative gap would be a guess.
	if prev != nil && f.TTL > 0 && c.s.Cache.Extent != nil && !c.r.Meta.At.Before(prev.At) {
		gap := c.r.Meta.At.Sub(prev.At)
		last := prev.At
		if f.LastPing.After(last) {
			last = f.LastPing
		}
		entryGap := c.r.Meta.At.Sub(last)
		segs := c.entrySegments(entryTTL)
		longest := segs[0].ttl
		for _, sg := range segs {
			longest = max(longest, sg.ttl)
		}
		live, alive := liveExtent(segs, gap, nil)
		switch {
		case alive:
			// A part the clock calls dead can still be proven alive by a billed read. That read lengthens the extent
			// (it only lowers a saving, R45). `alive` itself stays clock-only, so a read does not preempt the ping rescue.
			if c.usage.CacheRead > 0 {
				if l2, ok := liveExtent(segs, gap, func(at int) bool { return c.readReaches(prev.Forwarded, at) }); ok {
					live = max(live, l2)
				}
			}
			c.phase, c.prevExt = PhaseHot, live
		case f.Lifetime == LifetimeUnknown:
			// an unknown lifetime cannot prove an expiry (R45)
		case !f.LastPing.IsZero() && entryGap <= longest && c.pingRead() != nil:
			// a minimum counts as the lifetime for a rescue: 30 minutes is 30 minutes
			c.phase, c.prevExt, c.pingHot = PhaseHot, mustFlat(prev.Forwarded, *c.pingRead()), true
		case f.Lifetime == LifetimeMinimum:
			// with no ping, a gap past a minimum proves no expiry (R45)
		default:
			c.phase = PhaseCold
			// The clock says expired, but the provider billed a read that covers the prefix of a breakpoint (clock
			// skew, queue time, a long streamed response that refreshed the entry late, a sibling that kept it
			// warm). The read is the evidence and it beats the clock at any gap; it only lowers a saving (R45).
			if c.usage.CacheRead > 0 {
				if live, ok := liveExtent(segs, gap, func(at int) bool { return c.readReaches(prev.Forwarded, at) }); ok {
					c.phase, c.prevExt = PhaseHot, live
				}
			}
		}
	} else if prev != nil && c.r.Meta.At.Before(prev.At) {
		// Out of order: the clock says nothing about the entry, so the phase is left to the usage below.
	} else if prev != nil && f.TTL > 0 && c.r.Meta.At.Sub(prev.At) <= entryTTL && c.s.Cache.CachePhase == PhaseCold {
		c.phase = PhaseHot // the previous request cached nothing: hot with an empty extent (§5.2)
	} else if prev != nil && f.TTL > 0 && f.Lifetime == LifetimeExact && c.r.Meta.At.Sub(prev.At) > entryTTL {
		c.phase = PhaseCold // the previous request cached nothing, and it would have expired anyway
	}

	if !c.skipsPricing() {
		c.readEnd = extent(c.fwd, c.ratio, c.usage.CacheRead, c.candidates())
		cached := c.usage.CacheRead + c.usage.CacheWrite
		if len(c.fwdBPs) == 0 && c.usage.Input+c.usage.CacheRead+c.usage.CacheWrite >= c.r.Facts.MinCacheable { // CacheWrite1h is part of CacheWrite
			cached += c.usage.Input // no breakpoint: the provider caches the whole prompt it processed (only when the whole prompt reaches the minimum cacheable length, §5.1)
		}
		c.cachedFwd = extent(c.fwd, c.ratio, cached, c.candidates())
	}

	switch c.phase {
	case PhaseHot:
		if c.prevExt >= 0 {
			c.prevExtCli = mustFlat(prev.Client, prevFwdToClient(prev, prev.Forwarded.position(c.prevExt)))
		}
		x := readable(min(c.prevExtCli, c.clientFD-1), c.clientBPs)
		c.bstar = -1
		if len(c.clientBPs) == 0 {
			c.bstar = x // no client breakpoint: every position is one
		} else {
			for _, cand := range append(append([]bp{{at: c.prevExtCli}}, c.clientBPs...), c.prevClientBPs...) {
				if cand.at <= x && cand.at > c.bstar {
					c.bstar = cand.at
				}
			}
		}
		// The usage is evidence of what the provider read. A client edit inside the cached prefix makes it read
		// less than the previous request cached. If it read ALL of that extent, nothing in it was broken, and the
		// difference the hashes show is not a difference in content: the host hashed something that moved
		// (a cache mark on an unchanged message). Then B* is the end of the extent, not the start of the body,
		// or every saving replayed on this request would be priced at the write rate although the provider read
		// those tokens.
		// The comparison is in tokens, not positions: a position is an index into one body, and the previous and
		// the current forwarded bodies may differ before the extent (a block a plugin added), so equal
		// indices would not mean equal prefixes.
		if c.prevExt >= 0 && !c.skipsPricing() && c.readCoversExtent(prev) && c.clientFD <= c.prevExtCli && c.prevExtCli > c.bstar && !c.splitChangedSystem() {
			c.bstar = c.prevExtCli
		}
	case PhaseUnknown:
		c.unknown = true
		res.Unpriced = append(res.Unpriced, Unpriced{Reason: ReasonPenaltyUnknown})
		if c.readEnd >= 0 { // a read in the usage is evidence (§5.2)
			c.phase = PhaseHot
			c.bstar = mustFlat(c.client, c.fwdToClient(c.fwd.position(c.readEnd)))
		}
	}
	res.Phase = c.phase
	if c.bstar >= 0 {
		p := c.client.position(c.bstar)
		res.Boundary = &p
		c.bstarFwd = mustFlat(c.fwd, c.clientToFwd(p))
	}
}

// entrySeg is the part of the cached entry that a breakpoint of the previous request wrote: the prefix up
// to flat position at (in the previous forwarded body), alive for ttl.
type entrySeg struct {
	at  int
	ttl time.Duration
}

// segTTL is the lifetime a breakpoint's TTL string gives. Any string other than "5m" and "1h" (an empty
// one, or a typo) takes the entry's TTL, with no warning. Compute rejects an unknown TTL on the current
// request's breakpoints (check "breakpoints"), but a stored one is not checked again.
func segTTL(s string, entryTTL time.Duration) time.Duration {
	switch s {
	case "1h":
		return time.Hour
	case "5m":
		return 5 * time.Minute
	}
	return entryTTL
}

// entrySegments splits the previous request's cached extent by the TTL of the breakpoints that wrote it,
// in flat order of the previous forwarded body. It never returns an empty list: the extent itself is the
// last part, at entryTTL, when no breakpoint ends it.
func (c *calcState) entrySegments(entryTTL time.Duration) []entrySeg {
	return entrySegs(c.s, entryTTL)
}

// entrySegs is entrySegments for a thread state. The part beyond the last breakpoint, up to the extent, has
// the entry TTL (EntryFacts.TTL of the writing request). A breakpoint TTL other than "5m" and "1h" (empty or
// unknown) also takes the entry TTL (segTTL). s must hold a previous fingerprint and an extent.
func entrySegs(s ThreadState, entryTTL time.Duration) []entrySeg {
	prev := s.PrevPrint
	ext := mustFlat(prev.Forwarded, *s.Cache.Extent)
	var segs []entrySeg
	for _, b := range prev.Forwarded.breakpoints(prev.ForwardedBreakpoints) {
		if b.at <= ext {
			segs = append(segs, entrySeg{b.at, segTTL(b.ttl, entryTTL)})
		}
	}
	if len(segs) == 0 || segs[len(segs)-1].at < ext {
		segs = append(segs, entrySeg{ext, entryTTL})
	}
	return segs
}

// liveExtent is the longest prefix of the previous entry that is alive after gap: the end of the last
// segment whose own TTL has not run out. A segment that has run out is alive too when read says the usage
// proves a read of its prefix (read may be nil). ok is false when no segment is alive.
func liveExtent(segs []entrySeg, gap time.Duration, read func(at int) bool) (int, bool) {
	live, ok := -1, false
	for _, sg := range segs {
		if gap <= sg.ttl || (read != nil && read(sg.at)) {
			live, ok = max(live, sg.at), true
		}
	}
	return live, ok
}

// readReaches reports whether Usage.CacheRead covers the first bytes bytes of the previous forwarded
// body up to position i, with the tolerance of extent (128 tokens plus 2%). The prefix is priced at the
// byte ratio of this request, its media at the fixed media price. It is the one tolerance for the read-beats-the-clock rule and for the B* rule (decision 7).
func (c *calcState) readReaches(b Body, i int) bool {
	want := c.ratio.upTo(b, i)
	return want > 0 && want <= c.usage.CacheRead+128+c.usage.CacheRead/50
}

// readCoversExtent reports whether the tokens the provider read reach the previous request's cached extent.
func (c *calcState) readCoversExtent(prev *Fingerprint) bool {
	return c.readReaches(prev.Forwarded, c.prevExt)
}

// readable is the last position up to x that a request with the breakpoints bps can read. The
// provider looks the cache up from a breakpoint, so a prefix that ends at x is readable when a
// breakpoint is at or after x. Otherwise the read snaps down to the last breakpoint at or before x
// (§5.1, §5.2 row 4). With no breakpoint the provider caches the prefix by itself (automatic prefix
// caching), so every position is a breakpoint and x is readable.
func readable(x int, bps []bp) int {
	if len(bps) == 0 {
		return x
	}
	for _, b := range bps {
		if b.at >= x {
			return x
		}
	}
	return lastAtOrBefore(bps, x)
}

// mustFlat is flat for a position the library derived itself. A position in an empty section is
// the start of the next section; one past the end of a section clamps to its last position; both
// clamp to the end of the body.
func mustFlat(b Body, p Position) int {
	if i, ok := b.flat(p); ok {
		return i
	}
	s := sectionIndex(p.Section)
	if s < 0 || p.Index < 0 {
		return -1
	}
	if b.sectionEnd(s) == b.Start[s] {
		return min(b.Start[s], len(b.Pos)-1)
	}
	return min(b.sectionEnd(s)-1, len(b.Pos)-1)
}

// cacheEffects classifies a break (§5.2) and adds a rescue (§4.4). The premiums need to know whether a
// counted break covers a position (it already moved the position from read to a write), so the effects
// are found twice: once on a scratch result to find that break, once for real.
func (c *calcState) cacheEffects(res *Result) {
	if c.pass == 0 {
		c.pass = 1
		var scratch Result
		c.cacheEffects(&scratch)
		c.countedBreak = nil
		for i := range scratch.CacheEffects {
			if e := scratch.CacheEffects[i]; e.Kind == EffectBreak && !e.InBaseline && e.To != nil {
				c.countedBreak = &e
			}
		}
		c.pass = 2
	}
	if c.pingHot {
		c.rescue(res)
	}
	ttlp, mark := c.premiums()
	c.headTTL(res, ttlp)
	c.split(res)
	if mark != nil {
		c.addEffect(res, *mark)
	}
	switch {
	case c.unknown || c.s.PrevPrint == nil:
		return // row 6
	case c.phase == PhaseCold:
		c.addEffect(res, CacheEffect{Kind: EffectBreak, InBaseline: true}) // row 5: the expiry, on the record, moving no tokens
		return
	}
	fwdFDCli := mustFlat(c.client, c.fwdToClient(c.fwd.position(c.fwdFD)))
	switch {
	case c.fwdFD <= c.prevExt && fwdFDCli < c.clientFD: // row 1
		c.addBreak(res, c.readEnd, c.changeAt(c.fwdFD))
		return
	case c.clientFD <= c.prevExtCli && fwdFDCli >= c.clientFD: // row 2
		c.addBreak(res, c.readEnd, "")
		c.inBaseline(res)
		return
	}
	if c.prevExt < 0 {
		return // nothing was cached, so nothing can have been lost
	}
	expect := readable(mustFlat(c.fwd, c.s.PrevPrint.Forwarded.position(c.prevExt)), c.fwdBPs)
	if c.r.Meta.At.Sub(c.s.PrevPrint.At) > 5*time.Minute {
		// past the 5m TTL only the entries held at 1h are still alive: a 5m part past the head expired
		expect = min(expect, c.headEnd())
	}
	predicted := c.ratio.upTo(c.fwd, expect)
	low := c.usage.CacheRead+128+predicted/50 < predicted // the one-block tolerance (row 4)
	if !low {
		return
	}
	if op := c.reportedSetting(); op != nil { // row 3
		from, known := c.settingFrom(*op)
		if !known {
			res.Unpriced = append(res.Unpriced, Unpriced{OpID: op.ID, Reason: ReasonPenaltyUnknown})
		}
		c.addBreak(res, from, op.ChangeID)
		return
	}
	c.addBreak(res, c.readEnd, "") // row 4: the entry was lost early
	c.inBaseline(res)
	if prev := c.s.PrevPrint; prev.Model != "" && c.r.Meta.Model != "" && prev.Model != c.r.Meta.Model {
		// the provider keeps a cache for each model, so this is not a loss nobody can explain
		res.CacheEffects[len(res.CacheEffects)-1].Cause = CauseModelChanged
	}
}

func (c *calcState) reportedSetting() *Operation {
	for _, it := range c.items {
		if !it.active && it.bad == "" && !it.op.DryRun && it.op.Kind == KindSetting {
			return &it.op
		}
	}
	return nil
}

// settingFrom is the start of a setting break: the earliest SettingChange.At, snapped down to the
// last forwarded breakpoint before it (§5.2 row 3). With no At, it is the read the usage shows.
func (c *calcState) settingFrom(op Operation) (int, bool) {
	at, known := len(c.fwd.Pos), false
	for _, sc := range op.Settings {
		if sc.At != nil {
			if i := mustFlat(c.fwd, *sc.At); i >= 0 && i < at {
				at, known = i, true
			}
		}
	}
	if !known {
		return c.readEnd, false
	}
	if len(c.fwdBPs) == 0 {
		return at - 1, true // no forwarded breakpoint: every position is one
	}
	return lastAtOrBefore(c.candidates(), at-1), true
}

// changeAt is the ChangeID of the applied operation whose forwarded span covers the flat position.
func (c *calcState) changeAt(at int) string {
	for k, it := range c.items {
		if it.ended || it.bad != "" || it.op.DryRun || it.op.Kind != KindRewrite {
			continue
		}
		for _, sp := range it.spans {
			from, ok1 := c.toFwd(k, sp.from)
			to, ok2 := c.toFwd(k, sp.to)
			if !ok1 || !ok2 {
				continue
			}
			if f, t := mustFlat(c.fwd, from), mustFlat(c.fwd, to); f <= at && at <= t {
				return it.op.ChangeID
			}
		}
	}
	return ""
}

// addBreak adds a break that moves the forwarded tokens after from, up to B*, from read to write.
func (c *calcState) addBreak(res *Result, from int, changeID string) {
	e := CacheEffect{Kind: EffectBreak, ChangeID: changeID, Estimated: true}
	if from >= 0 {
		p := c.fwd.position(from)
		e.From = &p
	}
	if c.bstarFwd >= 0 {
		p := c.fwd.position(c.bstarFwd)
		e.To = &p
		if n := c.ratio.between(c.fwd, from, c.bstarFwd); n > 0 {
			e.Saved.CacheRead = n
			ttl, anyBP, ok := c.writeBP(c.fwd, c.fwdBPs, c.bstarFwd)
			if c.noCacheTokens() || (anyBP && !ok) || len(c.fwdBPs) == 0 {
				e.Caused.Input = n // nothing is cacheable, or no breakpoint (no write tier): the moved tokens are input (§5.1)
			} else {
				addWrite(&e.Caused, ttl, n) // "" counts as 5m
			}
		}
	}
	c.addEffect(res, e)
}

// inBaseline marks the last effect as one the client body would have had too (§5.2 rows 2 and 4).
func (c *calcState) inBaseline(res *Result) {
	res.CacheEffects[len(res.CacheEffects)-1].InBaseline = true
}

// rescue adds the keep-alive rescue (§4.4). The tokens are the smaller of what the ping read and what
// this request read. What the request wrote does not enter the rule: a long turn or a large tool
// result adds to the write, and it does not change what the ping kept alive.
func (c *calcState) rescue(res *Result) {
	if c.usage.CacheRead <= 0 {
		return
	}
	n := min(c.ratio.upTo(c.fwd, mustFlat(c.fwd, *c.pingRead())), c.usage.CacheRead)
	if w := c.r.Facts.SharedWarm; w != nil {
		i, ok := c.fwd.flat(*w)
		if !ok { // a host bug: a doubtful figure may only lower a saving, so credit nothing (R54)
			res.Checks = append(res.Checks, CheckFailure{Check: "shared_warm",
				Detail: fmt.Sprintf("section=%q index=%d is not in the forwarded body", w.Section, w.Index)})
			return
		}
		// a sibling kept this part warm without the ping (R54). Two floors, so n can be 1 token off the single
		// conversion: the same tolerance the conservation check allows.
		n -= c.ratio.upTo(c.fwd, i)
	}
	if n <= 0 {
		return
	}
	e := CacheEffect{Kind: EffectRescue, ChangeID: c.r.Facts.PingWindow, To: c.pingRead(), Estimated: true}
	addWrite(&e.Saved, ttlName(c.r.Facts.TTL), n)
	e.Caused.CacheRead = n
	c.addEffect(res, e)
}

// headTTL adds the two legs of a head held at 1h (§4.4): the premium on every request that writes
// the head at 1h (found by premiums, which books each token once), and the rescue on a request that reads
// it after the 5m TTL. A premium that no headttl change owns keeps the ChangeID premiums found.
func (c *calcState) headTTL(res *Result, premium *CacheEffect) {
	id := ""
	for _, it := range c.items {
		if !it.ended && strings.HasPrefix(it.op.ChangeID, "headttl:") {
			id = it.op.ChangeID
		}
	}
	if premium != nil {
		if id != "" {
			premium.ChangeID = id
		}
		c.addEffect(res, *premium)
	}
	if id == "" {
		return
	}
	prev := c.s.PrevPrint
	if prev == nil || c.s.Cache.TTL != "1h" || c.r.Meta.At.Sub(prev.At) <= 5*time.Minute || c.usage.CacheRead == 0 {
		return
	}
	head := c.headEnd()
	if head < 0 {
		return
	}
	n := min(c.ratio.upTo(c.fwd, head), c.usage.CacheRead)
	e := CacheEffect{Kind: EffectTTLRescue, ChangeID: id, TTL: "1h", Estimated: true}
	e.Saved.CacheWrite5m, e.Caused.CacheRead = n, n
	c.addEffect(res, e)
}

// split adds the saving of a delivered cachesplit (§4.4): the client body has one system block, the
// forwarded body two, so the stable half is read when only the volatile tail changed. The tokens are
// the forwarded prefix before the first difference, capped by what the provider read, less what the
// client body would have read up to B* anyway.
func (c *calcState) split(res *Result) {
	id := c.splitItem()
	if id == "" || !c.splitDelivered() || c.phase != PhaseHot || c.fwdFD <= 0 || c.usage.CacheRead == 0 {
		return
	}
	// The split pays only when the volatile tail changed: then the client body, with one block, would have
	// written the whole system section again, and the forwarded body reads its stable half. If the client's
	// system section did not change since the previous request (the first difference is after it), the
	// client body would have read the same prefix, and the split saved nothing (accountant#20).
	if c.s.PrevPrint == nil || c.clientFD >= c.client.sectionEnd(1) {
		return
	}
	n := min(c.ratio.upTo(c.fwd, c.fwdFD-1), c.usage.CacheRead)
	if c.bstarFwd >= 0 { // the client body would have read up to B* anyway: only the rest is the split's
		n -= c.ratio.upTo(c.fwd, c.bstarFwd)
	}
	if n <= 0 {
		return
	}
	e := CacheEffect{Kind: EffectSplit, ChangeID: id, Estimated: true}
	to := c.fwd.position(c.fwdFD - 1)
	e.To = &to
	addWrite(&e.Saved, ttlName(c.r.Facts.TTL), n)
	e.Caused.CacheRead = n
	c.addEffect(res, e)
}

// premiums books the tier moves that the forwarded breakpoints make on content both bodies hold (§4.4).
//
// THE IDENTITY (design §4.4, "The tier moves of a written token"). Take each forwarded position i that the
// provider wrote, after the read and not past what the request cached, and j, its position in the client
// body. f(i) is the tier the forwarded body billed it in. c(j) is the tier the client body would have billed
// it in: input after its last breakpoint, a write at the TTL of the first client breakpoint at or after j,
// or, under a counted break, the write that break priced. x is the tokens both bodies hold there: the
// smaller of the two sizes. An operation prices the rest, the content it added or removed.
//
//	c(j) input,    f(i) write 5m or 1h -> markPremium  Input     -> write  (0.25 or 1.0)
//	c(j) write 5m, f(i) write 1h       -> ttlPremium   write 5m  -> write 1h (0.75)
//	any other pair                     -> nothing (equal, or a saving that is left out)
//
// Every token is in one row at most, and no token an operation prices is in any row, so the operations,
// markPremium and ttlPremium together book each written token once. TestEveryTierMoveIsBookedOnce checks
// the sum with the tier rates against an independent oracle (check 6 sums tokens, so it cannot see this). A token the client body
// would have written at 1h and the forwarded body writes at 5m is not priced: that is a saving, and leaving
// it out can only lower one (R45). A removed client breakpoint is not priced for the same reason.
// The bytes of each row are added first and converted once, so rounding costs one token a row, not one a
// position. The 1h rows are capped by Usage.CacheWrite1h, the input-to-write row by Usage.CacheWrite: a
// bill shows what the provider wrote. When the cap cuts, the dearer premium goes last (a doubtful figure may
// only lower a saving). With no cache tokens in the usage, or nothing cached, nothing is booked.
// A position that no mapping reaches (content a splice inserted) is the operation's, so it is skipped.
func (c *calcState) premiums() (ttl, mark *CacheEffect) {
	if len(c.fwdBPs) == 0 || c.noCacheTokens() || c.cachedFwd < 0 {
		return nil, nil
	}
	lo := c.readEnd
	type acc struct{ bytes, media, first, last int }
	var in5, in1, up acc // input -> 5m, input -> 1h, 5m -> 1h
	in5.first, in1.first, up.first = -1, -1, -1
	add := func(a *acc, i, b, m int) {
		a.bytes += b
		a.media += m
		if a.first < 0 {
			a.first = i
		}
		a.last = i
	}
	hi := min(c.cachedFwd, len(c.fwd.Pos)-1)
	for i := lo + 1; i <= hi; i++ {
		ft, _, fok := c.writeBP(c.fwd, c.fwdBPs, i)
		if !fok {
			continue
		}
		j := mustFlat(c.client, c.fwdToClient(c.fwd.position(i)))
		if j < 0 || mustFlat(c.fwd, c.clientToFwd(c.client.position(j))) != i {
			continue
		}
		covered := i <= c.bstarFwd // the client body reads it
		if covered && c.countedBreak == nil {
			continue // nothing broke: the provider read it too, or the break is in the baseline
		}
		fb, cb := c.fwd.cumBytes(i)-c.fwd.cumBytes(i-1), c.client.cumBytes(j)-c.client.cumBytes(j-1)
		fm, cm := c.fwd.cumMedia(i)-c.fwd.cumMedia(i-1), c.client.cumMedia(j)-c.client.cumMedia(j-1)
		b, m := min(fb, cb), min(fm, cm)
		if covered { // the break priced every forwarded token here as a write, the added ones too
			b, m = fb, fm
		}
		var ct string
		var cok bool
		if covered {
			// the counted break moved it from read to a write; it priced that write at one TTL (addBreak)
			cok = c.countedBreak.Caused.Input == 0
			ct = "5m"
			if c.countedBreak.Caused.CacheWrite1h > 0 {
				ct = "1h"
			}
		} else if len(c.clientBPs) == 0 {
			ct, cok = "5m", true // with no breakpoint the provider caches the whole prompt (§5.1)
		} else {
			ct, _, cok = c.writeBP(c.client, c.clientBPs, j)
		}
		switch {
		case !cok && len(c.clientBPs) > 0:
			if ft == "1h" {
				add(&in1, i, b, m)
			} else {
				add(&in5, i, b, m)
			}
		case cok && ct != "1h" && ft == "1h":
			add(&up, i, b, m)
		}
	}
	n5, n1, t := c.ratio.tokensOf(in5.bytes, in5.media), c.ratio.tokensOf(in1.bytes, in1.media), c.ratio.tokensOf(up.bytes, up.media)
	if cut := n5 + n1 - c.usage.CacheWrite; cut > 0 {
		d := min(cut, n5)
		n5 -= d
		n1 -= min(cut-d, n1)
	}
	if over := n1 + t - c.usage.CacheWrite1h; over > 0 { // the bill shows fewer 1h writes than the breakpoints name
		d := min(over, n1)
		n5 += d // written at 5m instead
		n1 -= d
		t -= min(over-d, t)
	}
	span := func(a acc) (*Position, *Position) {
		var from *Position
		if a.first > 0 {
			p := c.fwd.position(a.first - 1)
			from = &p
		}
		to := c.fwd.position(a.last)
		return from, &to
	}
	if n5+n1 > 0 {
		first := in5
		if in5.first < 0 || (in1.first >= 0 && in1.first < in5.first) {
			first = in1
		}
		first.last = max(in5.last, in1.last)
		mark = &CacheEffect{Kind: EffectMarkPremium, ChangeID: c.changeAt(c.fwdBPs[len(c.fwdBPs)-1].at), TTL: "5m", Estimated: true}
		if n1 > 0 {
			mark.TTL = "1h"
		}
		mark.From, mark.To = span(first)
		mark.Saved.Input = n5 + n1
		mark.Caused.CacheWrite5m, mark.Caused.CacheWrite1h = n5, n1
	}
	if t > 0 {
		ttl = &CacheEffect{Kind: EffectTTLPremium, TTL: "1h", Estimated: true}
		ttl.From, ttl.To = span(up)
		ttl.Saved.CacheWrite5m, ttl.Caused.CacheWrite1h = t, t
		for k := len(c.fwdBPs) - 1; k >= 0; k-- { // the change of the last 1h breakpoint, if one reported it
			if b := c.fwdBPs[k]; b.ttl == "1h" && b.at <= up.last {
				ttl.ChangeID = c.changeAt(b.at)
				break
			}
		}
	}
	return ttl, mark
}

// headEnd is the last forwarded position that a 1h breakpoint of the previous request covers; -1
// when there is none.
func (c *calcState) headEnd() int {
	end := -1
	for _, b := range c.prevFwdBPs {
		if b.ttl == "1h" && b.at > end {
			end = b.at
		}
	}
	return end
}

func (c *calcState) addEffect(res *Result, e CacheEffect) {
	e.ID = fmt.Sprintf("ce:%d", len(res.CacheEffects)+1)
	res.CacheEffects = append(res.CacheEffects, e)
}

func ttlName(d time.Duration) string {
	if d >= time.Hour {
		return "1h"
	}
	return "5m"
}

// writeBP finds the breakpoint that writes the content at flat position p of body: the first breakpoint at
// or after p whose prefix reaches the minimum cacheable length (§5.1, §6.2). Prefixes are cumulative, so a
// later breakpoint can cache what an earlier, too short one cannot. ttl is that breakpoint's TTL; ok is
// false when none qualifies. anyBP says whether any breakpoint is at or after p at all.
func (c *calcState) writeBP(body Body, bps []bp, p int) (ttl string, anyBP, ok bool) {
	best := -1
	for _, x := range bps {
		if x.at < p {
			continue
		}
		anyBP = true
		if (best < 0 || x.at < best) && (c.r.Facts.MinCacheable <= 0 || c.ratio.upTo(body, x.at) >= c.r.Facts.MinCacheable) {
			best, ttl = x.at, x.ttl
		}
	}
	return ttl, anyBP, best >= 0
}

// noCacheTokens says whether the usage shows a request that read and wrote nothing although it billed
// input: the forwarded body was uncacheable (§5.1).
func (c *calcState) noCacheTokens() bool {
	return !c.noUsage && c.usage.CacheRead+c.usage.CacheWrite+c.usage.CacheWrite1h == 0 && c.usage.Input > 0
}

func addWrite(t *TierTokens, ttl string, n int) {
	if ttl == "1h" {
		t.CacheWrite1h += n
	} else {
		t.CacheWrite5m += n
	}
}

// addTier adds n tokens at flat position p by the tier rule (§6.2): read at or before B*, write at the
// TTL of the first breakpoint at or after p that reaches the minimum cacheable length (§5.1), input
// otherwise. forwarded is true for tokens of the forwarded body: when the usage shows no cache tokens,
// that body was uncacheable and all its tokens are input, in the read range too. The client body is a counterfactual that the usage
// does not describe, so only the minimum length decides its tier.
func (c *calcState) addTier(t *TierTokens, body Body, forwarded bool, p, bstar int, bps []bp, n int) {
	ttl, anyBP, ok := c.writeBP(body, bps, p)
	switch {
	case forwarded && c.noCacheTokens():
		t.Input += n // the usage shows no read and no write: nothing was read, whatever B* says
	case p <= bstar: // with no breakpoint every position is one, so B* alone decides
		t.CacheRead += n
	case !anyBP || !ok:
		t.Input += n
	default:
		addWrite(t, ttl, n)
	}
}

// skipsPricing says whether the request prices nothing: it has no usage, or its usage or
// breakpoints failed check 8, 9 or 10 (§6.5).
func (c *calcState) skipsPricing() bool { return c.noUsage || c.invalid != "" }

// Limits of check 9 and check 10 (§6.5). Check 12 (size_tokens) uses the two token limits too.
const (
	minTokensPerByte = 0.05
	maxTokensPerByte = 2.0
	maxBreakpoints   = 4 // the Anthropic limit
)

// validate runs checks 8 to 10 (§6.5) on the usage and the breakpoints. It returns the reason
// for the first one that fails, or "". A request with no usage is left to ReasonNoUsage.
func (c *calcState) validate(u Usage) string {
	if c.noUsage {
		return ""
	}
	switch {
	case badUsage(u):
		return ReasonBadUsage
	case c.badRatio():
		return ReasonRatioRange
	case c.badBreakpoints():
		return ReasonBreakpoints
	}
	return ""
}

// badRatio says whether the tokens per forwarded text byte are outside 0.05 to 2.0 (check 9). Media
// bytes are not in the denominator and the media price is not in the numerator (newRatio). A media
// price above the bill is cut to the bill (newRatio), so it does not fail this check. A usage that
// bills no prompt token for a body with bytes (tokens per byte 0), or tokens for an empty body (infinite),
// is outside the range too.
func (c *calcState) badRatio() bool {
	last := len(c.fwd.Pos) - 1
	if c.fwd.cumBytes(last) == 0 {
		return true // an empty body billed for tokens, or for none
	}
	t, n := float64(c.ratio.tokens), float64(c.ratio.bytes)
	if n == 0 {
		return t != 0 // all media: the bill must be at most the media price (a larger one is cut to the bill), and no text is left to price
	}
	if t == 0 {
		return true
	}
	return t < minTokensPerByte*n || t > maxTokensPerByte*n
}

// badBreakpoints says whether the forwarded body has more than 4 breakpoints, or any breakpoint
// has a TTL other than "5m", "1h" or empty (empty is the 5m default).
func (c *calcState) badBreakpoints() bool {
	if len(c.r.Breakpoints) > maxBreakpoints {
		return true
	}
	for _, list := range [][]Breakpoint{c.r.ClientBreakpoints, c.r.Breakpoints} {
		for _, b := range list {
			if b.TTL != "" && b.TTL != "5m" && b.TTL != "1h" {
				return true
			}
		}
	}
	return false
}

// checkUsage reports the failed check 8, 9 or 10 in Result.Checks.
func (c *calcState) checkUsage(res *Result) {
	l := c.r.limits(c.s)
	for _, it := range c.items {
		switch {
		case it.ended || it.active:
		case it.bad == ReasonSizeRange:
			sz, _ := bigSize(it.op, l)
			res.Checks = append(res.Checks, CheckFailure{Check: ReasonSizeRange, Operator: it.op.Operator,
				Detail: fmt.Sprintf("size bytes=%d/%d limits=%d/%d: the operation is not priced", sz[0].Bytes, sz[1].Bytes, l.before, l.after)})
		case it.bad == ReasonBadUsage:
			res.Checks = append(res.Checks, CheckFailure{Check: ReasonBadUsage, Operator: it.op.Operator, Detail: callDetail(it.op)})
		}
	}
	if c.invalid == "" {
		return
	}
	detail := fmt.Sprintf("usage=%d/%d/%d/%d breakpoints=%d forwarded bytes=%d", c.usage.Input, c.usage.CacheRead,
		c.usage.CacheWrite, c.usage.CacheWrite1h, len(c.r.Breakpoints), c.ratio.bytes)
	res.Checks = append(res.Checks, CheckFailure{Check: c.invalid, Detail: detail})
}

// price prices each operation: the saved tokens in client space, the added tokens in forwarded
// space (§6.2). Output order: the reported operations, then the active ones (§6.3).
func (c *calcState) price(res *Result) {
	var reported, active []PricedOp
	for k, it := range c.items {
		if it.ended {
			continue
		}
		p := priced(it.op, it.id)
		estimated := false // true when a size is converted from bytes (§4.2 rule 7)
		switch {
		case it.bad != "":
			res.Unpriced = append(res.Unpriced, Unpriced{OpID: it.id, Reason: it.bad})
		case c.noUsage:
			res.Unpriced = append(res.Unpriced, Unpriced{OpID: it.id, Reason: ReasonNoUsage})
		case c.invalid != "":
			res.Unpriced = append(res.Unpriced, Unpriced{OpID: it.id, Reason: c.invalid})
		case resultUnknown(it.op):
			res.Unpriced = append(res.Unpriced, Unpriced{OpID: it.id, Reason: ReasonPingResultUnknown})
		case c.mediaUnpriced(k, it):
			c.leaveOutOfConservation(k, it)
			res.Unpriced = append(res.Unpriced, Unpriced{OpID: it.id, Reason: ReasonMediaTokens})
		case it.op.Kind == KindRewrite:
			for _, sp := range it.spans {
				bm, am := c.rangeMedia(k, sp.before, sp, true), c.rangeMedia(k, sp.after, sp, false)
				bt, bx, bbad := c.sizeTokens(sp.before, bm)
				at, ax, abad := c.sizeTokens(sp.after, am)
				if bbad || abad {
					res.Checks = append(res.Checks, CheckFailure{Check: "size_tokens", Operator: it.op.Operator,
						Detail: fmt.Sprintf("tokens=%d/%d bytes=%d/%d: outside %g..%g tokens a byte, bytes used",
							sp.before.Tokens, sp.after.Tokens, sp.before.Bytes, sp.after.Bytes, minTokensPerByte, maxTokensPerByte)})
				}
				if !bx || !ax {
					estimated = true
				}
				d := bt - at
				if !it.op.DryRun {
					// what the host's tokens add to the saving beyond the ratio's figure: the
					// conservation check compares at the ratio (check 6)
					c.hostGap += d - (c.ratio.tokensOf(sp.before.Bytes, bm) - c.ratio.tokensOf(sp.after.Bytes, am))
				}
				if d > 0 {
					c.addTier(&p.Saved, c.client, false, mustFlat(c.client, sp.from), c.bstar, c.clientBPs, d)
					if !it.op.DryRun {
						c.interaction(res, sp.from, d)
					}
				} else if d < 0 {
					fp, _ := c.toFwd(k, sp.from)
					if sp.before.Bytes == 0 && it.op.MessageSplice != nil {
						fp, _ = c.toFwdSkipping(k, sp.from) // an inserted item sits in the slot its own splice opened
					}
					at := mustFlat(c.fwd, fp)
					if sp.before.Bytes == 0 && c.phase == PhaseHot && c.prevExt >= 0 && at <= c.prevExt && at < c.fwdFD && !c.noCacheTokens() {
						// content the operator added, which the previous request already forwarded
						// and cached: the provider reads it, whatever B* says (§6.2)
						p.Caused.CacheRead += -d
					} else {
						c.addTier(&p.Caused, c.fwd, true, at, c.bstarFwd, c.fwdBPs, -d)
					}
				}
			}
		}
		p.Estimated = p.Estimated || estimated
		if it.active {
			active = append(active, p)
		} else {
			reported = append(reported, p)
		}
	}
	res.Ops = append(reported, active...)
}

// mediaUnpriced says whether the operation covers media while the bill is below the media price
// (check 11). The library does not know how the bill splits between that media and the text, so it
// does not price the operation. A span with the host's own tokens on both sizes is still exact, so
// it does not count. An operation that covers no media is priced with the text ratio.
func (c *calcState) mediaUnpriced(k int, it *item) bool {
	if !c.ratio.capped || it.op.Kind != KindRewrite {
		return false
	}
	for _, sp := range it.spans {
		bm, am := c.rangeMedia(k, sp.before, sp, true), c.rangeMedia(k, sp.after, sp, false)
		if bm == 0 && am == 0 {
			continue
		}
		hostCounted := sp.before.Tokens > 0 && plausible(sp.before, bm) && sp.after.Tokens > 0 && plausible(sp.after, am)
		if !hostCounted {
			return true
		}
	}
	return false
}

// leaveOutOfConservation adds the ratio's figure for the ranges of an operation unpriced for
// media_tokens to mediaGap. The bill difference holds those tokens and the operation prices none of
// them, so check 6 takes them out of the bill difference. Every other operation stays in the check
// at full strength. Taking the whole check away would also hide a fault in those operations.
func (c *calcState) leaveOutOfConservation(k int, it *item) {
	if it.op.DryRun {
		return
	}
	for _, sp := range it.spans {
		bm, am := c.rangeMedia(k, sp.before, sp, true), c.rangeMedia(k, sp.after, sp, false)
		c.mediaGap += c.ratio.tokensOf(sp.before.Bytes, bm) - c.ratio.tokensOf(sp.after.Bytes, am)
	}
}

// interaction adds the tokens of a saved span that the rescue turned from write into read (§6.2).
// Without the ping the span would have been written cold; with it the span is saved at the read
// tier. The difference is neither the ping's nor the operator's, so it is attributed to nobody.
// Only the exact case is counted, and anything doubtful is left out (R45): the request is rescued,
// no sibling kept the entry warm, the span is at or before the ping's extent, and the span is
// priced at the read tier.
func (c *calcState) interaction(res *Result, from Position, d int) {
	if len(c.clientBPs) == 0 { // no breakpoint, no write tier: the interaction is write -> read, so none (§6.2)
		return
	}
	if !c.pingHot || c.r.Facts.SharedWarm != nil || c.pingRead() == nil || c.usage.CacheRead <= 0 {
		return
	}
	rescued := false
	for _, e := range res.CacheEffects {
		rescued = rescued || e.Kind == EffectRescue
	}
	flat := mustFlat(c.client, from)
	if !rescued || flat > mustFlat(c.client, c.fwdToClient(*c.pingRead())) {
		return
	}
	var t TierTokens
	c.addTier(&t, c.client, false, flat, c.bstar, c.clientBPs, d)
	if t.CacheRead != d {
		return
	}
	addWrite(&res.Interaction, ttlName(c.r.Facts.TTL), d)
	res.InteractionCaused.CacheRead += d
}

// conservation is check 6 (§6.5), in tokens: the tokens the client body would have billed, less the
// tokens the forwarded body billed, equal what the applied content changes saved less what they
// caused. A cache effect moves tokens between tiers and nets zero in tokens, so only the content
// changes count. The byte ratio rounds each span down, so each one may differ by one token.
func (c *calcState) conservation(res *Result) {
	if c.skipsPricing() || c.unknown {
		return
	}
	var net, tol int
	for _, p := range res.Ops {
		if p.Call != nil || p.DryRun {
			continue
		}
		net += tierSum(p.Saved) - tierSum(p.Caused)
		tol++
	}
	billed := c.usage.Input + c.usage.CacheRead + c.usage.CacheWrite
	want := c.ratio.upTo(c.client, len(c.client.Pos)-1) - billed
	net -= c.hostGap   // the ratio's figure: host tokens are exact and the bodies are not
	want -= c.mediaGap // those operations are unpriced: their tokens are in the bill difference only
	if d := want - net; d > tol+1 || d < -(tol+1) {
		res.Checks = append(res.Checks, CheckFailure{Check: "conservation",
			Detail: fmt.Sprintf("bill difference=%d ops=%d tokens", want, net)})
	}
}

func tierSum(t TierTokens) int { return t.Input + t.CacheWrite5m + t.CacheWrite1h + t.CacheRead }

// checks runs the sum, spans and unique-ChangeID checks (§6.5). The order and attribution checks
// are not run here (conservation is, in calc): attribution holds by construction, because the library
// only gives a break the ChangeID of one of the request's operations.
func (c *calcState) checks(res *Result) {
	if c.ratio.capped && !c.noUsage {
		note := "the media is cut to the bill and an operation that covers media is unpriced"
		if c.invalid != "" {
			note = "nothing is priced"
		}
		media := c.fwd.cumMedia(len(c.fwd.Pos)-1) / mediaBytesPerToken
		res.Checks = append(res.Checks, CheckFailure{Check: "media_tokens",
			Detail: fmt.Sprintf("media price=%d tokens billed=%d: the bill is below the media price: the media is cut to %d tokens: %s",
				media, c.usage.Input+c.usage.CacheRead+c.usage.CacheWrite, c.ratio.mediaTok, note)})
	}
	var got [3]int
	seen := map[string]bool{}
	for _, it := range c.items {
		if it.ended || it.bad != "" {
			continue
		}
		op := it.op
		if (op.Kind == KindRewrite || op.Kind == KindSetting) && op.ChangeID != "" {
			if seen[op.ChangeID] {
				res.Checks = append(res.Checks, CheckFailure{Check: "unique_change", Operator: op.Operator})
			}
			seen[op.ChangeID] = true
		}
		if op.Kind != KindRewrite {
			continue
		}
		if len(op.Spans) > 0 {
			var b, a int
			for _, s := range op.Spans {
				b, a = b+s.SizeBefore.Bytes, a+s.SizeAfter.Bytes
			}
			if b != op.SizeBefore.Bytes || a != op.SizeAfter.Bytes {
				res.Checks = append(res.Checks, CheckFailure{Check: "spans", Operator: op.Operator,
					Detail: fmt.Sprintf("spans=%d/%d op=%d/%d bytes", b, a, op.SizeBefore.Bytes, op.SizeAfter.Bytes)})
			}
		}
		if op.DryRun {
			continue
		}
		for _, sp := range it.spans {
			if s := sectionIndex(sp.from.Section); s >= 0 {
				got[s] += sp.before.Bytes - sp.after.Bytes
			}
		}
	}
	for s := range 3 {
		want := c.client.sectionBytes(s) - c.fwd.sectionBytes(s)
		if got[s] != want {
			res.Checks = append(res.Checks, CheckFailure{Check: "sum",
				Detail: fmt.Sprintf("section=%s ops=%d bodies=%d bytes", sections[s], got[s], want)})
		}
	}
}

// activeOps is the rewrite and setting operations that are still applied, in client space (§4.3).
func (c *calcState) activeOps() []Operation {
	var out []Operation
	for _, it := range c.items {
		if it.ended || it.bad != "" || it.op.ChangeID == "" || (it.op.Kind != KindRewrite && it.op.Kind != KindSetting) {
			continue
		}
		op := it.op
		op.At = it.spans[0].from
		if len(op.Spans) > 0 {
			op.Spans = make([]Span, len(it.spans))
			for i, sp := range it.spans {
				op.Spans[i] = Span{Section: sp.from.Section, From: sp.from.Index, To: sp.to.Index, SizeBefore: sp.before, SizeAfter: sp.after}
			}
		}
		out = append(out, op)
	}
	return out
}

// nextState is the state after this request: the applied rewrite and setting operations in client
// space, this request's fingerprint, and what it left cached (§4.6, §6.1).
func (c *calcState) nextState() ThreadState {
	next := ThreadState{Version: c.s.Version, Active: c.activeOps()}
	fp := &Fingerprint{Thread: c.r.Thread.ID, Model: c.r.Meta.Model, At: c.r.Meta.At, Client: c.client, Forwarded: c.fwd,
		ClientBreakpoints: c.r.ClientBreakpoints, ForwardedBreakpoints: c.r.Breakpoints}
	for _, it := range c.items {
		if !it.ended && it.bad == "" && !it.op.DryRun && it.op.MessageSplice != nil {
			fp.splices = append(fp.splices, *it.op.MessageSplice)
		}
	}
	switch {
	case c.skipsPricing():
		next.Cache = CacheState{} // unknown
	case c.cachedFwd < 0:
		next.Cache = CacheState{CachePhase: PhaseCold, EntryTTL: c.r.Facts.TTL}
	default:
		fp.Cached = c.fwd.position(c.cachedFwd)
		ext := fp.Cached
		ttl := ttlName(c.r.Facts.TTL)
		for _, b := range c.fwdBPs { // a 1h breakpoint under the extent holds the head at 1h whatever Facts.TTL says
			if b.ttl == "1h" && b.at <= c.cachedFwd {
				ttl = "1h"
			}
		}
		next.Cache = CacheState{CachePhase: PhaseHot, Extent: &ext, TTL: ttl, EntryTTL: c.r.Facts.TTL}
	}
	next.PrevPrint = fp
	if p := c.s.PrevPrint; p != nil && !c.seeded && fp.At.Before(p.At) {
		// An out-of-order request must not replace a newer fingerprint: the next request would be
		// compared with an older body and a wrong gap (accountant#16).
		next.PrevPrint, next.Cache = p, c.s.Cache
	}
	return next
}

// splitItem is the id of the live cachesplit item, or "" when there is none. split and splitChangedSystem
// share it. headTTL scans for a different prefix ("headttl:"), so it keeps its own loop.
func (c *calcState) splitItem() string {
	id := ""
	for _, it := range c.items {
		if !it.ended && strings.HasPrefix(it.op.ChangeID, "cg:cachesplit:") {
			id = it.op.ChangeID
		}
	}
	return id
}

// splitDelivered says the forwarded system section has more positions than the client one: the split is in
// the forwarded body.
func (c *calcState) splitDelivered() bool {
	return c.fwd.sectionEnd(1)-c.fwd.Start[1] > c.client.sectionEnd(1)-c.client.Start[1]
}

// splitChangedSystem says a delivered cachesplit sits on a system section whose client text changed since
// the previous request, and the read stopped at the split. The split exists for that case: the volatile tail
// moved, the client body would have missed from the system block on, and the forwarded body read only the
// stable half. A read that comes within the tolerance of the previous extent (a short turn adds less than 128
// tokens plus 2%) is then no proof that nothing broke: the read is the stable half, and the extent is the
// whole previous entry (context-guru#909).
//
// A read that passes the forwarded first difference is different. It read text the hashes call changed, so
// the hash difference moved no content (a cache mark moved onto the block). The exception does not apply,
// and the full-read rule stands (accountant#20, #21). The comparison has no tolerance: the tolerance is
// the very window in which the two cases look alike.
func (c *calcState) splitChangedSystem() bool {
	if c.clientFD >= c.client.sectionEnd(1) || c.splitItem() == "" || !c.splitDelivered() {
		return false
	}
	return c.fwdFD > 0 && c.usage.CacheRead <= c.ratio.upTo(c.fwd, c.fwdFD-1)
}
