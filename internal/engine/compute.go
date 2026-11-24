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
	ReasonMediaUnseen       = "media_unseen"    // tokens per byte is too high and the body holds media that its bytes do not price: nothing is priced (§6.5, check 9)
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
	t := r.trackers.tracker(ThreadKey(r.meta.Session, r.thread), r.meta.At)
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
			"detail", f.Detail, "request", r.meta.RequestID, "session", r.meta.Session, "thread", r.thread.ID)
	}
}

// Preview does the same calculation as Compute and stores nothing (§6.1).
func (r *Request) Preview(usage Usage) Result {
	if !r.ended {
		return r.notEnded()
	}
	res, _ := r.calc(r.trackers.tracker(ThreadKey(r.meta.Session, r.thread), r.meta.At).snapshot(), usage)
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
			res.Checks = append(res.Checks, CheckFailure{Check: CheckBadUsage, Operator: op.Operator, Detail: callDetail(op)})
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
	client := r.client.cumBytes(len(r.client.Pos) - 1)
	fwd := client
	if r.ended {
		fwd = r.forwarded.cumBytes(len(r.forwarded.Pos) - 1)
	}
	type entry struct {
		op     Operation
		active bool
		out    bool // dropped: it failed a bound
	}
	var es []*entry // the rewrites: the active ones, then the reported ones
	for _, op := range s.Active {
		if !storedCopy(op, r.ops) {
			es = append(es, &entry{op: op, active: true})
		}
	}
	for _, op := range r.ops {
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
	for _, op := range r.ops {
		res.Ops = append(res.Ops, priced(op, op.ID))
		res.Unpriced = append(res.Unpriced, Unpriced{OpID: op.ID, Reason: ReasonEndRequestMissing})
	}
	return res
}

func priced(op Operation, id string) PricedOp {
	return PricedOp{OpID: id, Kind: op.Kind, Operator: op.Operator, ChangeID: op.ChangeID, DryRun: op.DryRun, Call: op.Call}
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

	res.Compaction = c.compaction()
	c.phaseAndBoundary(&res)
	if !c.skipsPricing() {
		c.cacheEffects(&res)
	}
	c.price(&res)
	c.checks(&res)
	c.checkUsage(&res)
	c.conservation(&res)
	next := c.nextState()
	res.ActiveOps, res.OldestActiveTurns = activeAge(next)
	return res, next
}

// compaction records the host's verdict of a client compaction and the changes that ended on this
// request. It is nil when the host reported none.
func (c *calcState) compaction() *CompactionRecord {
	if c.r.meta.Compaction == "" {
		return nil
	}
	rec := &CompactionRecord{Trigger: c.r.meta.Compaction, Ended: []string{}} // [] and not null in JSON
	for _, it := range c.items {
		if it.active && it.ended && it.op.ChangeID != "" {
			rec.Ended = append(rec.Ended, it.op.ChangeID)
		}
	}
	return rec
}

// items builds the operation list in pipeline order: the active operations first, as they ran on
// earlier requests, then the reported ones. A reported operation with the ChangeID of an active one
// replaces it (the observe-to-enforce switch, §4.6, or an operator that changes its own change).
func (r *Request) items(s ThreadState) []*item {
	l := r.limits(s)
	reported := map[string]bool{} // a spend may share the ChangeID of its change (§6.5 check 3)
	for _, op := range r.ops {
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
	for _, op := range r.ops {
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

// plausible says whether host tokens fit the bytes of the range. nt is the range's media and opaque
// bytes.
func plausible(s Size, nt nonText) bool {
	text := s.Bytes - nt.media - nt.opaque
	if float64(s.Tokens) > maxTokensPerByte*float64(s.Bytes) {
		return false
	}
	return text <= 0 || float64(s.Tokens) >= minTokensPerByte*float64(text)
}

// rangeMedia is the media and opaque bytes a size covers. Each mark the host sets (MediaBytes,
// OpaqueBytes) wins for its own kind. A kind the host leaves at 0 comes from the body that holds the
// range: the client body for a "before" size, and the forwarded body, over the range mapped by toFwd,
// for an "after" size. The body's part is the size's bytes beyond the range's text and beyond the
// host's mark: with no mark, the media first, clamped to 0..media, then the opaque bytes, clamped to
// 0..opaque. So a host that marks only its media, as hosts did before OpaqueBytes, still gets the
// opaque price for a signature in the range, and the conservation check (which takes the opaque bytes
// from the body) agrees with the size. Without this an unmarked size over an image would price its
// base64 at the text ratio.
//
// The counted part of the opaque bytes (design D16) comes from Size.OpaqueTokens when the host sets it:
// then all the size's opaque bytes are counted at those tokens. Otherwise it comes from the body's
// counts over the range, in proportion when the size holds less opaque bytes than the range does.
func (c *calcState) rangeMedia(k int, s Size, sp span, isBefore bool) nonText {
	nt := nonText{media: s.MediaBytes, opaque: s.OpaqueBytes}
	var body nonText
	text, ok := 0, false
	if s.Bytes > 0 {
		body, text, ok = c.rangeBody(k, sp, isBefore)
	}
	if ok {
		switch {
		case s.MediaBytes > 0 && s.OpaqueBytes > 0:
		case s.MediaBytes > 0:
			nt.opaque = min(max(s.Bytes-text-s.MediaBytes, 0), body.opaque)
		case s.OpaqueBytes > 0:
			nt.media = min(max(s.Bytes-text-s.OpaqueBytes, 0), body.media)
		default:
			extra := max(s.Bytes-text, 0)
			nt.media = min(extra, body.media)
			nt.opaque = min(extra-nt.media, body.opaque)
		}
		if body.opaque > 0 && body.counted > 0 {
			share := min(nt.opaque, body.opaque)
			nt.counted = int(int64(body.counted) * int64(share) / int64(body.opaque))
			nt.ctok = int(int64(body.ctok) * int64(share) / int64(body.opaque))
		}
	}
	if s.OpaqueTokens > 0 && nt.opaque > 0 {
		nt.counted, nt.ctok = nt.opaque, s.OpaqueTokens
	}
	return nt
}

// rangeBody is the non-text and the text bytes of a range in the body that holds it (see
// rangeMedia). ok is false when the range does not map onto that body.
func (c *calcState) rangeBody(k int, sp span, isBefore bool) (nt nonText, text int, ok bool) {
	// In tools or system the range counts content that earlier changes added: clientSide and fwdSide take
	// it out (accountant#96).
	body, from, to := c.client, c.clientSide(k, sp.from), c.clientSide(k, sp.to)
	if !isBefore {
		body = c.fwd
		var ok1, ok2 bool
		if sp.before.Bytes == 0 && c.items[k].op.MessageSplice != nil { // an inserted item sits in its own slot
			from, ok1 = c.toFwdSkipping(k, sp.from)
			to, ok2 = c.toFwdSkipping(k, sp.to)
		} else {
			from, ok1 = c.fwdSide(k, sp.from)
			to, ok2 = c.fwdSide(k, sp.to)
		}
		if !ok1 || !ok2 {
			return nonText{}, 0, false
		}
	}
	f, ok1 := body.flat(from)
	t, ok2 := body.flat(to)
	if !ok1 || !ok2 || t < f {
		return nonText{}, 0, false
	}
	p := body.between(f-1, t)
	return p.nonText, p.bytes - p.media - p.opaque, true
}

// sizeTokens converts one reported size to tokens. A plausible size the host counted (Tokens > 0) is
// exact. Any other size is converted from its bytes with the request ratio, and so is an estimate. A
// size of zero bytes is exactly zero tokens (§4.2 rule 7). bad is true when the host's tokens were
// ignored as implausible.
func (c *calcState) sizeTokens(s Size, nt nonText) (tokens int, exact, bad bool) {
	if s.Tokens > 0 {
		if plausible(s, nt) {
			return s.Tokens, true, false
		}
		bad = true
	}
	return c.ratio.sizeOf(s.Bytes, nt), s.Bytes == 0, bad
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

	phase   CachePhase
	unknown bool // the phase was unknown before the usage was read: no break can be classified
	prevExt int  // the previous extent the phase uses, flat in the previous forwarded body; -1 = none
	pingHot bool // the entry is hot because a ping kept it (§5.2)
	// tailFrom and tailTo bound the part of a mixed-TTL entry that only a ping kept alive: the clock calls the
	// 1h head alive, the 5m tail expired, and a ping inside the tail's TTL renewed it (flat, previous forwarded
	// body). tailTo is -1 when there is none.
	tailFrom, tailTo int
	tailTTL          time.Duration
	bstar            int // B*, flat in the client body; -1 = none
	bstarFwd         int // B*, flat in the forwarded body; -1 = none
	readEnd          int // what Usage.CacheRead covers, flat in the forwarded body; -1 = nothing
	cachedFwd        int // what this request leaves cached, flat in the forwarded body; -1 = nothing
	fwdFD            int
	clientFD         int
	prevExtCli       int // prevExt in client space, flat in the previous client body; -1 = none

	pass          int          // 0 before cacheEffects, 1 on the scratch pass, 2 on the real one
	countedBreak  *CacheEffect // the break that is part of the bill difference, found on the scratch pass
	baselineBreak bool         // a break in the baseline covers the positions after the read: the client body lost them too

	derivedPingRead *Position // pingRead's cache; derivedPingOK is false when the ping read nothing
	derivedPingOK   bool
}

func (r *Request) newCalc(s ThreadState, items []*item, usage Usage) *calcState {
	c := &calcState{r: r, s: s, items: items, usage: usage, client: r.client, fwd: r.forwarded,
		tailTo: -1, prevExt: -1, bstar: -1, bstarFwd: -1, readEnd: -1, cachedFwd: -1, prevExtCli: -1}
	c.noUsage = usage == (Usage{})
	for j, it := range items {
		if it.op.MessageSplice != nil {
			c.splicers = append(c.splicers, j)
		}
	}
	c.ratio = newRatio(usage.Input+usage.CacheRead+usage.CacheWrite, r.forwarded)
	c.invalid = c.validate(usage)
	c.clientBPs = c.client.breakpoints(r.clientBreakpoints)
	c.fwdBPs = c.fwd.breakpoints(r.breakpoints)
	if c.downgraded() {
		for _, bps := range [][]bp{c.clientBPs, c.fwdBPs} {
			for i := range bps {
				bps[i].ttl = "5m"
			}
		}
	}
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

// downgraded says whether the provider billed a 1h ask as 5m: the request wrote, read nothing, the bill has
// no 1h write, and the host says the entry lives 5m (EntryFacts.TTL). Every breakpoint was written then, so
// each 1h mark held its entry for 5m only (accountant#56). The usage alone does not decide it: a host that
// leaves CacheWrite1h unset would look the same, so the host's own TTL must agree. With a read in the bill
// some marks may be old 1h entries that were only read, so the usage proves nothing.
func (c *calcState) downgraded() bool {
	if c.noUsage || c.usage.CacheRead > 0 || c.usage.CacheWrite == 0 || c.usage.CacheWrite1h > 0 || c.r.facts.TTL != 5*time.Minute {
		return false
	}
	for _, b := range c.r.breakpoints {
		if b.TTL == "1h" {
			return true
		}
	}
	return false
}

// forwardPositions ends the active operations whose position a later splice removed (§4.3), or
// that are no longer in the client body (a client compaction). It also ends an active model setting
// on the request that ends the model change (modelSettingEnded).
func (c *calcState) forwardPositions() {
	for _, it := range c.items {
		if it.active && c.modelSettingEnded(it) {
			it.ended = true
		}
	}
	// Added content first. An in-place change after added content in its section has a position that counts
	// that content, so it needs to know which added content the host still forwards (accountant#94).
	for _, added := range [2]bool{true, false} {
		for k, it := range c.items {
			if !it.active || it.op.Kind != KindRewrite { // a setting has no position (§4.6)
				continue
			}
			c.endSpans(k, it, added)
		}
	}
	// A change that ended can free the one before it in a chain, so repeat until none ends.
	for ended := true; ended; {
		ended = false
		for k, it := range c.items {
			if it.active && !it.ended && c.takenOver(k, it) {
				it.ended, ended = true, true
			}
		}
	}
}

// endSpans applies the end rules to the spans of active item k that added content (added) or edited client
// content in place (not added).
func (c *calcState) endSpans(k int, it *item, added bool) {
	for _, sp := range it.spans {
		if (sp.before.Bytes == 0) != added {
			continue
		}
		if added {
			// Content the operator added: its position is in forwarded space, and the client body has
			// nothing there. It ends only when the client edited what precedes it (design Q12, §4.3). That
			// item is measured in the previous client body: when the client removed it, the new body has no
			// item there to compare (accountant#70, case A). The position counts the content that changes
			// before it in pipeline order added before it in its section, which the client body does not have. In the first position of a section the
			// item before it is the last one of the section before, and that one precedes it in the
			// provider's order too, so an edit there ends the change.
			if c.clientEdited(c.prevInsertionPoint(c.clientSide(k, sp.from)) - 1) {
				it.ended = true
			} else if !it.op.DryRun && c.addedContentGone(sp.from.Section) {
				// The host stopped adding it (accountant#89). A DryRun forwards the client's own body by
				// design, so an equal section is no proof for it.
				it.ended = true
			}
			if _, ok := c.toFwd(k, sp.from); !ok {
				it.ended = true
			}
			continue
		}
		if i, ok := c.client.flat(c.clientSide(k, sp.from)); !ok || c.clientEdited(i) {
			it.ended = true // the client edited at or before it, or compacted (§4.3)
		}
		fp, ok := c.fwdSide(k, sp.from)
		if !ok {
			it.ended = true
		} else if c.restored(k, it, sp, fp) {
			it.ended = true
		}
	}
}

// modelSettingEnded says whether this request ends an active model setting (§4.6): the request does
// not run on its To, so the plugin no longer applies it. The operation leaves Active as a whole, with
// any other key it changed, so a later model switch by the client cannot match its To again
// (accountant#80). Whether the ending request's break counts is case 2 of "A model change" (§5.2).
//
// Nothing ends when the model of this request is unknown, or when the previous request ran on the
// same model by ModelKey: a new spelling of one model is not a change ("The model differs", §6.3).
// With the previous model unknown, the setting ends on the first known model that is not its To, so a
// request with no model does not keep it alive. A DryRun changed nothing, so the client may have
// picked its To: it does not end here.
func (c *calcState) modelSettingEnded(it *item) bool {
	sc, ok := it.op.Settings["model"]
	if !ok || it.op.Kind != KindSetting || it.op.DryRun || sc.To == nil {
		return false
	}
	to, cur := *sc.To, c.r.meta
	if cur.modelID() == "" || to == cur.Model || to == cur.modelID() {
		return false
	}
	pf := c.s.PrevPrint
	return pf == nil || pf.modelID() == "" || pf.modelID() != cur.modelID()
}

// clientEdited says whether the client changed something at or before flat position i, compared with
// the previous request's client body. Content the client only appended is not an edit: it matters when
// the previous fingerprint is older than the change, because a request between them never reached
// Compute (§4.6).
func (c *calcState) clientEdited(i int) bool {
	p := c.s.PrevPrint
	return p != nil && c.clientFD < len(p.Client.Pos) && c.clientFD <= i
}

// addedContentGone says whether the forwarded body carries no added content in section s: the forwarded
// section has as many items as the client section, and each item has the client item's size or the size an
// in-place change, active or reported, gives it. Then the host stopped adding what an active change added
// there, for example after its configuration changed, and the client did not edit anything, so the Q12 rule
// cannot end the change (accountant#89). If the change stayed, the sum check would count its bytes on every later
// request of the thread.
//
// The rule ends a change only on proof. The messages section is left out: its positions go through the
// splices. A host that drops only some of the added items leaves the section larger than the client's, and
// the rule keeps every change there. An in-place change with spans says nothing about one item's size, so an
// item it shaped keeps the added changes too. Another operator that removes items and adds others of exactly
// the same sizes would look like no change. The caller exempts a DryRun.
func (c *calcState) addedContentGone(s Section) bool {
	i := sectionIndex(s)
	if s == SectionMessages || i < 0 || len(c.fwd.Pos) == 0 {
		return false
	}
	cs, ce := c.client.Start[i], c.client.sectionEnd(i)
	fs, fe := c.fwd.Start[i], c.fwd.sectionEnd(i)
	if ce-cs != fe-fs {
		return false
	}
	for j := 0; j < ce-cs; j++ {
		if got := c.fwd.Pos[fs+j].Bytes; got != c.client.Pos[cs+j].Bytes && !c.inPlaceSize(Position{s, j}, int(got)) {
			return false
		}
	}
	return true
}

// inPlaceSize says whether an in-place change on the whole item at client position p gives it the size n.
// The change can be active or reported: a host that stops adding content and first trims an item in the same
// request forwards the trimmed size there (accountant#96). With the counts equal and no added content in the
// section, the client and the forwarded index are the same. An active change's position counts the content
// that changes before it added (clientSide). A reported change's position is in this request's forwarded
// section, which holds no added content if the rule holds, so it is a client index as it is.
func (c *calcState) inPlaceSize(p Position, n int) bool {
	for k, it := range c.items {
		if it.ended || it.bad != "" || it.op.Kind != KindRewrite || it.op.DryRun ||
			it.op.MessageSplice != nil || len(it.op.Spans) > 0 {
			continue
		}
		at := it.spans[0].from
		if it.active {
			at = c.clientSide(k, at)
		}
		if sp := it.spans[0]; at == p && sp.before.Bytes > 0 && sp.after.Bytes == n {
			return true
		}
	}
	return false
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
// from sp.after here). Another operator rewriting the item to exactly that size would look restored. The
// client item is at the change's position without the content that changes before it added (clientSide).
func (c *calcState) restored(k int, it *item, sp span, fp Position) bool {
	if it.op.DryRun || it.op.MessageSplice != nil || sp.before.Bytes == 0 || sp.before.Bytes == sp.after.Bytes {
		return false // a DryRun forwards the client's own item by design
	}
	ci, ok1 := c.client.flat(c.clientSide(k, sp.from))
	fi, ok2 := c.fwd.flat(fp)
	if !ok1 || !ok2 {
		return false
	}
	return c.client.Pos[ci].Bytes == c.fwd.Pos[fi].Bytes
}

// takenOver says whether another operator now shapes the item that an active in-place change edited
// (accountant#70). The forwarded item has neither the client's size (restored ends that) nor the size the
// change gave it, and no other change could have used the change's output. The change no longer shapes the
// item, and keeping it would count the same bytes twice in the sum check. This also frees a stale change
// that is already in a saved state.
//
// The rule ends a change only on proof, and keeps it when it cannot tell:
//   - Another change whose range covers the item and whose SizeBefore is not the client's own bytes there
//     saw an earlier operator's output, so it may have used this change's. That is a chain, and it stays.
//     The partner can be a rewrite with spans or a wider range.
//   - With a splice in the state, positions of the changes are in different spaces. The rule does not run.
//   - A change with spans or a splice, or a DryRun, says nothing about the whole item's size.
func (c *calcState) takenOver(k int, it *item) bool {
	if it.op.Kind != KindRewrite || it.op.DryRun || it.op.MessageSplice != nil || len(it.op.Spans) > 0 {
		return false
	}
	sp := it.spans[0]
	if sp.before.Bytes == 0 || c.hasSplice() {
		return false
	}
	fp, ok := c.fwdSide(k, sp.from)
	if !ok {
		return false
	}
	fi, ok := c.fwd.flat(fp)
	if !ok || int(c.fwd.Pos[fi].Bytes) == sp.after.Bytes {
		return false
	}
	cp := c.clientSide(k, sp.from)
	for j, o := range c.items {
		if j == k || o.ended || o.bad != "" || o.op.Kind != KindRewrite || o.op.DryRun {
			continue
		}
		for _, os := range o.spans {
			if os.before.Bytes == 0 {
				continue // added content measured no client bytes, so it cannot have used this change's output
			}
			from, to := c.clientSide(j, os.from), c.clientSide(j, os.to)
			if from.Section == cp.Section && from.Index <= cp.Index && cp.Index <= to.Index &&
				!c.sawClientBytes(span{from, to, os.before, os.after}) {
				return false // it measured another operator's output: a chain
			}
		}
	}
	return true
}

// hasSplice says whether a change that is not ended replaced a range of messages.
func (c *calcState) hasSplice() bool {
	for _, j := range c.splicers {
		if it := c.items[j]; !it.ended && it.bad == "" && !it.op.DryRun {
			return true
		}
	}
	return false
}

// sawClientBytes says whether the span measured the client's own bytes over its range. It says no when
// the sizes cannot be compared, so a caller that wants proof keeps the change.
func (c *calcState) sawClientBytes(sp span) bool {
	f, ok1 := c.client.flat(sp.from)
	t, ok2 := c.client.flat(sp.to)
	if !ok1 || !ok2 || t < f {
		return false
	}
	return sp.before.Bytes == c.client.cumBytes(t)-c.client.cumBytes(f-1)
}

// insertionPoint is the flat position in client body b where content added at p sits: the position itself,
// or the end of its section when b has fewer items there.
func insertionPoint(b Body, p Position) int {
	s := sectionIndex(p.Section)
	if s < 0 {
		return 0
	}
	return min(b.Start[s]+max(p.Index, 0), b.sectionEnd(s))
}

// clientSide takes out of the forwarded position of item k's added content the items that changes before it
// in pipeline order added before it in the same section. The messages section is not mapped here: its positions go through the splices.
// An in-place change in tools or system has its position in the same space, so it maps the same way
// (accountant#94).
func (c *calcState) clientSide(k int, p Position) Position {
	p.Index -= c.addedBefore(k, p, false)
	return p
}

// fwdSide maps the position of item k's in-place change into the forwarded body. In the messages section that
// is toFwd. In tools or system the position is in k's input, which has the content that changes before k
// added; the forwarded body has that content too, except what ended, for example because the host stopped
// adding it (accountant#94). Content that later changes added is not counted, as in clientSide.
func (c *calcState) fwdSide(k int, p Position) (Position, bool) {
	if p.Section == SectionMessages {
		return c.toFwd(k, p)
	}
	p.Index -= c.addedBefore(k, p, true)
	return p, true
}

// addedBefore counts the items that changes up to k in pipeline order added before p in its section; with
// endedOnly, only those of changes that ended. It is 0 in the messages section. For a change reported in this
// request, content that ended is never counted: the change ran after the host stopped adding it, so its input
// did not have it (accountant#100). Only active changes end in this request, and they come first in pipeline
// order, so this holds for every ended item before k.
func (c *calcState) addedBefore(k int, p Position, endedOnly bool) int {
	if p.Section == SectionMessages {
		return 0
	}
	reported := !c.items[k].active
	n := 0
	// Only items up to k: a position is in the operator's own output, and that has only the content of the
	// changes before it in pipeline order (§4.3). c.items is in pipeline order, and item k's own earlier
	// spans are in its output too.
	for _, it := range c.items[:k+1] {
		if it.op.Kind != KindRewrite || it.op.DryRun || it.bad != "" || (endedOnly && !it.ended) || (reported && it.ended) {
			continue
		}
		for _, sp := range it.spans {
			if sp.before.Bytes == 0 && sp.from.Section == p.Section && sp.from.Index < p.Index {
				n += max(min(sp.to.Index, p.Index-1)-sp.from.Index+1, 0) // a span can add several items
			}
		}
	}
	return n
}

// prevInsertionPoint is the insertion point in the previous request's client body. The client items before
// it are the ones the added content followed there. clientFD is a flat index that both bodies share up to
// the first difference, so it can be compared with this point. Without a previous body it is the point in
// the current client body; clientEdited is false then, so that value never decides the end rule.
func (c *calcState) prevInsertionPoint(p Position) int {
	if c.s.PrevPrint == nil {
		return insertionPoint(c.client, p)
	}
	return insertionPoint(c.s.PrevPrint.Client, p)
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
	f := c.r.facts
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
			i = extentWith(prev.Forwarded, r, u.CacheRead+u.CacheWrite, nil, f.Provider)
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
	prev, f := c.s.PrevPrint, c.r.facts
	c.phase = PhaseUnknown
	entryTTL := c.s.Cache.EntryTTL
	if entryTTL <= 0 {
		entryTTL = f.TTL
	}
	// A request that started before the stored one has no valid gap: the clock says nothing about the
	// entry, so the phase is left to the usage (accountant#16). Hot from a negative gap would be a guess.
	if prev != nil && f.TTL > 0 && c.s.Cache.Extent != nil && !c.r.meta.At.Before(prev.At) {
		gap := c.r.meta.At.Sub(prev.At)
		last := prev.At
		if f.LastPing.After(last) {
			last = f.LastPing
		}
		entryGap := c.r.meta.At.Sub(last)
		segs := c.entrySegments(entryTTL)
		longest := segs[0].ttl
		for _, sg := range segs {
			longest = max(longest, sg.ttl)
		}
		// A ping that read the entry set the TTL of what it read, when the provider lets a read do so. The
		// ping keeps the entry for pingTTL from the ping (a 1h ping on a 5m entry), or ends it earlier (a 5m
		// ping on a 1h entry).
		keep := longest
		if pt := c.pingTTL(); pt > 0 && c.pingRead() != nil {
			keep = pt
			pr := mustFlat(prev.Forwarded, *c.pingRead())
			for i := range segs {
				if segs[i].at <= pr {
					segs[i].pingTTL = pt
				}
			}
		}
		live, alive := liveExtent(segs, gap, entryGap, nil)
		switch {
		case alive:
			c.pingedTail(segs, gap, entryGap, live)
			// A part the clock calls dead can still be proven alive by a billed read. That read lengthens the extent
			// (it only lowers a saving, R45). `alive` itself stays clock-only, so a read does not preempt the ping rescue.
			if c.usage.CacheRead > 0 {
				if l2, ok := liveExtent(segs, gap, entryGap, func(at int) bool { return c.readReaches(prev.Forwarded, at) }); ok {
					live = max(live, l2)
				}
			}
			c.phase, c.prevExt = PhaseHot, live
		case f.Lifetime == LifetimeUnknown:
			// an unknown lifetime cannot prove an expiry (R45)
		case !f.LastPing.IsZero() && entryGap <= keep && c.pingRead() != nil:
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
				if live, ok := liveExtent(segs, gap, entryGap, func(at int) bool { return c.readReaches(prev.Forwarded, at) }); ok {
					c.phase, c.prevExt = PhaseHot, live
				}
			}
		}
	} else if prev != nil && c.r.meta.At.Before(prev.At) {
		// Out of order: the clock says nothing about the entry, so the phase is left to the usage below.
	} else if prev != nil && f.TTL > 0 && c.r.meta.At.Sub(prev.At) <= entryTTL && c.s.Cache.CachePhase == PhaseCold {
		c.phase = PhaseHot // the previous request cached nothing: hot with an empty extent (§5.2)
	} else if prev != nil && f.TTL > 0 && f.Lifetime == LifetimeExact && c.r.meta.At.Sub(prev.At) > entryTTL {
		c.phase = PhaseCold // the previous request cached nothing, and it would have expired anyway
	}

	if !c.skipsPricing() {
		c.readEnd = extentWith(c.fwd, c.ratio, c.usage.CacheRead, c.candidates(), c.r.facts.Provider)
		cached := c.usage.CacheRead + c.usage.CacheWrite
		if len(c.fwdBPs) == 0 && c.usage.Input+c.usage.CacheRead+c.usage.CacheWrite >= c.r.facts.MinCacheable { // CacheWrite1h is part of CacheWrite
			cached += c.usage.Input // no breakpoint: the provider caches the whole prompt it processed (only when the whole prompt reaches the minimum cacheable length, §5.1)
		}
		c.cachedFwd = extentWith(c.fwd, c.ratio, cached, c.candidates(), c.r.facts.Provider)
	}

	switch c.phase {
	case PhaseHot:
		if c.prevExt >= 0 {
			c.prevExtCli = mustFlat(prev.Client, prevFwdToClient(prev, prev.Forwarded.position(c.prevExt)))
		}
		ext := c.clientExtent()
		x := readable(min(ext, c.clientFD-1), c.clientBPs)
		c.bstar = -1
		if len(c.clientBPs) == 0 {
			c.bstar = x // no client breakpoint: every position is one
		} else {
			for _, cand := range append(append([]bp{{at: ext}}, c.clientBPs...), c.prevClientBPs...) {
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
		// The full read proves that no content changed. It does not give the client body an entry past its own
		// last breakpoint, so B* is the client's extent (clientExtent), not the host's (accountant#52).
		if c.prevExt >= 0 && !c.skipsPricing() && c.readCoversExtent(prev) && c.clientFD <= c.prevExtCli && ext > c.bstar && !c.splitChangedSystem() {
			c.bstar = ext
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
	if c.r.facts.Provider.StorageBilled && !c.skipsPricing() {
		res.StorageTokens = c.usage.CacheWrite
	}
	if c.bstar >= 0 {
		p := c.client.position(c.bstar)
		res.Boundary = &p
		c.bstarFwd = mustFlat(c.fwd, c.clientToFwd(p))
	}
}

// clientExtent is the previous extent as the client body would have cached it, flat in the client body; -1 =
// none. It is prevExtCli, except where a breakpoint the host added wrote the part past the previous client's
// last breakpoint: the client body has no entry there, so its extent ends at that breakpoint (accountant#52).
// Without the cap a client that moves its own breakpoint to the newest message can look back to the host's
// entry, B* lands on the host's mark, and the next request books no markPayback for the premium it paid.
//
// prevExtCli is flat in the previous client body, and prevClientBPs and prevFwdBPs are flat in this one. The two
// bodies agree before clientFD, where the comparisons here are made. B* used prevExtCli as a position of this
// body before the cap, so the cap adds no new mix of the two.
func (c *calcState) clientExtent() int {
	ext := c.prevExtCli
	if ext < 0 || len(c.prevClientBPs) == 0 {
		return ext // with no client breakpoint the provider caches the whole prompt (§5.1)
	}
	last := -1
	for _, b := range c.prevClientBPs {
		last = max(last, b.at)
	}
	if ext <= last {
		return ext
	}
	for _, b := range c.prevFwdBPs {
		if j := mustFlat(c.client, c.fwdToClient(c.fwd.position(b.at))); j > last && j <= ext {
			return last // the host's mark wrote the extent past the client's last breakpoint
		}
	}
	return ext
}

// pingedTail finds the part of a mixed-TTL entry that the clock calls expired and that a ping kept alive:
// the parts past the live head whose own TTL is shorter than the request gap and not shorter than the gap
// from the last ping, up to what the ping read (accountant#56). The head was alive without the ping, so the
// rescue is the tail only.
func (c *calcState) pingedTail(segs []entrySeg, gap, entryGap time.Duration, live int) {
	f := c.r.facts
	if f.LastPing.IsZero() || f.Lifetime == LifetimeUnknown || c.pingRead() == nil {
		return
	}
	limit := mustFlat(c.s.PrevPrint.Forwarded, *c.pingRead())
	for _, sg := range segs {
		eff := sg.ttl // the TTL the ping gave this part, when it read it and the provider lets a read set the TTL
		if sg.pingTTL > 0 {
			eff = sg.pingTTL
		}
		if sg.at > live && sg.at <= limit && gap > sg.ttl && entryGap <= eff && sg.at > c.tailTo {
			c.tailFrom, c.tailTo, c.tailTTL = live, sg.at, sg.ttl
		}
	}
}

// pingTTL is the TTL the last ping asked, when the provider lets a read set the TTL of an entry
// (Provider.ReadSetsTTL) and the host gave it (EntryFacts.PingTTL), and the ping came after the previous request. 0 otherwise: nothing changes then.
func (c *calcState) pingTTL() time.Duration {
	f := c.r.facts
	if !f.Provider.ReadSetsTTL || f.LastPing.IsZero() || f.PingTTL <= 0 {
		return 0
	}
	// A ping from before the previous request kept nothing: that request wrote the entry after it. A doubtful
	// fact may only lower a saving (R45), so the ping TTL is ignored.
	if prev := c.s.PrevPrint; prev == nil || !f.LastPing.After(prev.At) {
		return 0
	}
	return f.PingTTL
}

// entrySeg is the part of the cached entry that a breakpoint of the previous request wrote: the prefix up
// to flat position at (in the previous forwarded body), alive for ttl.
type entrySeg struct {
	at  int
	ttl time.Duration
	// pingTTL is the TTL of the last ping that read this part, when the provider lets a read set the TTL
	// (Provider.ReadSetsTTL). 0 = no such ping. The part then lives pingTTL from the ping, not ttl from the
	// request.
	pingTTL time.Duration
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
			segs = append(segs, entrySeg{at: b.at, ttl: segTTL(b.ttl, entryTTL)})
		}
	}
	if len(segs) == 0 || segs[len(segs)-1].at < ext {
		segs = append(segs, entrySeg{at: ext, ttl: entryTTL})
	}
	return segs
}

// liveExtent is the longest prefix of the previous entry that is alive after gap: the end of the last
// segment whose own TTL has not run out. A segment that has run out is alive too when read says the usage
// proves a read of its prefix (read may be nil). ok is false when no segment is alive.
//
// A part that a 5m ping read (pingTTL set) is not alive once entryGap, the time since the ping, is past
// pingTTL, even when gap is still inside its own ttl: the read set the TTL back (Provider.ReadSetsTTL).
func liveExtent(segs []entrySeg, gap, entryGap time.Duration, read func(at int) bool) (int, bool) {
	live, ok := -1, false
	for _, sg := range segs {
		if (gap <= sg.ttl && (sg.pingTTL == 0 || entryGap <= sg.pingTTL)) || (read != nil && read(sg.at)) {
			live, ok = max(live, sg.at), true
		}
	}
	return live, ok
}

// readReaches reports whether Usage.CacheRead covers the first bytes bytes of the previous forwarded
// body up to position i, with the tolerance of extent (one read step plus 2%; the step is Provider.ReadGranularity, default 128). The prefix is priced at the
// byte ratio of this request, its media at the fixed media price. It is the one tolerance for the read-beats-the-clock rule and for the B* rule (decision 7).
func (c *calcState) readReaches(b Body, i int) bool {
	want := c.ratio.upTo(b, i)
	return want > 0 && want <= c.usage.CacheRead+c.r.facts.Provider.tolerance(c.usage.CacheRead)
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
		c.countedBreak, c.baselineBreak = nil, false
		for i := range scratch.CacheEffects {
			if e := scratch.CacheEffects[i]; e.Kind == EffectBreak && e.To != nil {
				if e.InBaseline {
					c.baselineBreak = true
				} else {
					c.countedBreak = &e
				}
			}
		}
		c.pass = 2
	}
	if c.pingHot {
		c.rescue(res)
	} else if c.tailTo >= 0 {
		c.rescueTail(res)
	}
	ttlp, mark := c.premiums()
	c.headTTL(res, ttlp)
	c.split(res)
	if mark != nil {
		c.addEffect(res, *mark)
	}
	c.markPayback(res)
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
		if !c.modelChangeBreak(res) {
			id := ""
			if k := c.itemAt(c.fwdFD); k >= 0 && !(c.items[k].active && c.endedAddedAt(c.fwdFD)) {
				// An active change on the item that moved into the slot of added content that ended forwards
				// the same bytes as before: the ended change left the break, and it has no ChangeID (design
				// Q12, accountant#96). A change reported now changed the position too.
				id = c.items[k].op.ChangeID
			}
			c.addBreak(res, c.readEnd, id)
		}
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
	if c.r.meta.At.Sub(c.s.PrevPrint.At) > 5*time.Minute {
		// past the 5m TTL only the entries held at 1h are still alive: a 5m part past the head expired
		expect = min(expect, c.headEnd())
	}
	predicted := c.ratio.upTo(c.fwd, expect)
	low := c.usage.CacheRead+c.r.facts.Provider.tolerance(predicted) < predicted // the one-block tolerance (row 4)
	if !low || c.modelChangeBreak(res) {
		return
	}
	if op := c.reportedSetting(); op != nil { // row 3
		c.settingBreak(res, *op)
		return
	}
	c.addBreak(res, c.readEnd, "") // row 4: the entry was lost early
	c.inBaseline(res)
}

// modelChangeBreak adds the break of rows 1, 3 and 4 when the model differs from the thread's previous
// request's ("A model change", §5.2), and reports whether it did. The provider keeps a cache for each
// model, so the whole prefix misses, and the operations decide who pays:
//   - a model setting reported on this request changed the model: that operation pays;
//   - the change ends an active model setting, or a model setting failed a check: the break counts,
//     with no ChangeID (an ended change, Q12; a doubtful figure only lowers a saving, R45);
//   - else the client changed the model: the client body misses too, so the break is in the baseline.
func (c *calcState) modelChangeBreak(res *Result) bool {
	prev, cur := c.s.PrevPrint.modelID(), c.r.meta.modelID()
	if prev == "" || cur == "" || prev == cur {
		return false
	}
	if op := c.modelSetting(); op != nil {
		c.settingBreak(res, *op)
		return true
	}
	if c.pluginModelChange() {
		c.addBreak(res, c.readEnd, "")
		return true
	}
	c.addBreak(res, c.readEnd, "")
	c.inBaseline(res)
	res.CacheEffects[len(res.CacheEffects)-1].Cause = CauseModelChanged
	return true
}

// pluginModelChange reports a model setting, with no well-formed report on this request, that still
// explains the change: one reported now that failed a check, or an active one that this request
// ends. An active setting ends when the previous request ran on its To and this one does not; that
// request also removes it from Active (modelSettingEnded). An active one whose To was not the previous
// model does not explain this change: its To is written in another spelling than the model, or an
// older version saved the state. A DryRun changed nothing.
func (c *calcState) pluginModelChange() bool {
	pf := c.s.PrevPrint
	for _, it := range c.items {
		sc, ok := it.op.Settings["model"]
		if !ok || it.op.Kind != KindSetting || it.op.DryRun {
			continue
		}
		if !it.active || (sc.To != nil && (*sc.To == pf.Model || *sc.To == pf.modelID())) {
			return true
		}
	}
	return false
}

// modelSetting is the well-formed setting operation reported on this request that changes the model.
func (c *calcState) modelSetting() *Operation { return c.setting("model") }

// reportedSetting is the first well-formed setting operation reported on this request (§5.2 row 3).
func (c *calcState) reportedSetting() *Operation { return c.setting("") }

// setting is the first well-formed setting operation reported on this request; with a key, the first
// one that changes that setting.
func (c *calcState) setting(key string) *Operation {
	for _, it := range c.items {
		if !it.active && it.bad == "" && !it.op.DryRun && it.op.Kind == KindSetting {
			if _, ok := it.op.Settings[key]; key == "" || ok {
				return &it.op
			}
		}
	}
	return nil
}

// settingBreak adds the break of a setting operation (§5.2 row 3): from settingFrom, with the
// operation's ChangeID, and penalty_unknown when no SettingChange.At gives the position.
func (c *calcState) settingBreak(res *Result, op Operation) {
	from, known := c.settingFrom(op)
	if !known {
		res.Unpriced = append(res.Unpriced, Unpriced{OpID: op.ID, Reason: ReasonPenaltyUnknown})
	}
	c.addBreak(res, from, op.ChangeID)
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
	if k := c.itemAt(at); k >= 0 {
		return c.items[k].op.ChangeID
	}
	return ""
}

// itemAt is the index of the applied operation whose forwarded span covers the flat position, or -1.
func (c *calcState) itemAt(at int) int {
	for k, it := range c.items {
		if it.ended || it.bad != "" || it.op.DryRun || it.op.Kind != KindRewrite {
			continue
		}
		for _, sp := range it.spans {
			from, ok1 := c.fwdSide(k, sp.from)
			to, ok2 := c.fwdSide(k, sp.to)
			if !ok1 || !ok2 {
				continue
			}
			if f, t := mustFlat(c.fwd, from), mustFlat(c.fwd, to); f <= at && at <= t {
				return k
			}
		}
	}
	return -1
}

// endedAddedAt says whether content that an active change added in tools or system ended in this request
// with its slot at the flat forwarded position at. The previous request forwarded that content, so its end
// changes the forwarded prefix there. The slot is mapped as fwdSide maps any position of the change.
func (c *calcState) endedAddedAt(at int) bool {
	for k, it := range c.items {
		if !it.ended || !it.active || it.bad != "" || it.op.DryRun || it.op.Kind != KindRewrite {
			continue
		}
		for _, sp := range it.spans {
			if sp.before.Bytes != 0 || sp.from.Section == SectionMessages {
				continue
			}
			if p, _ := c.fwdSide(k, sp.from); insertionPoint(c.fwd, p) == at {
				return true
			}
		}
	}
	return false
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
	if w := c.r.facts.SharedWarm; w != nil {
		i, ok := c.fwd.flat(*w)
		if !ok { // a host bug: a doubtful figure may only lower a saving, so credit nothing (R54)
			res.Checks = append(res.Checks, sharedWarmFailure(*w))
			return
		}
		// a sibling kept this part warm without the ping (R54). Two floors, so n can be 1 token off the single
		// conversion: the same tolerance the conservation check allows.
		n -= c.ratio.upTo(c.fwd, i)
	}
	if n <= 0 {
		return
	}
	e := CacheEffect{Kind: EffectRescue, ChangeID: c.r.facts.PingWindow, To: c.pingRead(), Estimated: true}
	addWrite(&e.Saved, ttlName(c.r.facts.TTL), n)
	e.Caused.CacheRead = n
	c.addEffect(res, e)
}

// rescueTail adds the keep-alive rescue of a mixed-TTL entry (accountant#56): only the part past the head
// that the ping kept alive. The tokens are that part, less what the request did not read beyond the head.
func (c *calcState) rescueTail(res *Result) {
	if c.usage.CacheRead <= 0 || c.r.facts.SharedWarm != nil {
		return
	}
	prev := c.s.PrevPrint.Forwarded
	head := c.ratio.upTo(prev, c.tailFrom)
	n := min(c.ratio.upTo(prev, c.tailTo)-head, c.usage.CacheRead-head)
	if n <= 0 {
		return
	}
	to := prev.position(c.tailTo)
	e := CacheEffect{Kind: EffectRescue, ChangeID: c.r.facts.PingWindow, To: &to, Estimated: true}
	addWrite(&e.Saved, ttlName(c.tailTTL), n)
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
	if prev == nil || c.s.Cache.TTL != "1h" || c.r.meta.At.Sub(prev.At) <= 5*time.Minute || c.usage.CacheRead == 0 {
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
	addWrite(&e.Saved, ttlName(c.r.facts.TTL), n)
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
	type acc struct {
		parts
		first, last int
	}
	var in5, in1, up acc // input -> 5m, input -> 1h, 5m -> 1h
	in5.first, in1.first, up.first = -1, -1, -1
	add := func(a *acc, i int, p parts) {
		a.parts.add(p)
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
		covered := i <= c.bstarFwd // the client body reads it, and a break moved it to a write
		if covered && c.countedBreak == nil && !c.baselineBreak {
			continue // no break covers it: the provider read it too
		}
		counted := covered && c.countedBreak != nil
		fp := c.fwd.between(i-1, i)
		p := minParts(fp, c.client.between(j-1, j))
		if counted { // the break priced every forwarded token here as a write, the added ones too
			p = fp
		}
		var ct string
		var cok bool
		if counted {
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
				add(&in1, i, p)
			} else {
				add(&in5, i, p)
			}
		case cok && ct != "1h" && ft == "1h":
			add(&up, i, p)
		}
	}
	n5, n1, t := c.ratio.tokensOf(in5.parts), c.ratio.tokensOf(in1.parts), c.ratio.tokensOf(up.parts)
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
		for k := len(c.fwdBPs) - 1; k >= 0; k-- { // the last 1h breakpoint that a reported operation covers
			if b := c.fwdBPs[k]; b.ttl == "1h" && b.at <= up.last {
				if id := c.changeAt(b.at); id != "" {
					ttl.ChangeID = id
					break
				}
			}
		}
	}
	return ttl, mark
}

// markPayback adds the return on a breakpoint the host added on an earlier request (§4.4). The previous
// request wrote the tail up to that breakpoint (a markPremium, a cost). This request reads it, and the
// client body would have billed it as input, or, when the client moved its own breakpoint past the host's,
// written it again: B* stops at the client's own entry (clientExtent), so it has none there to read. Each
// forwarded position after B* that the provider read and that both bodies hold moves from the client body's
// tier, input or a write at that client breakpoint's TTL, to read. No operation prices these tokens, and
// the split and every rescue book their own tokens (a ping rescue, a mixed-TTL tail rescue, a head's
// ttlRescue), so a request that already holds a split or a rescue does not get a payback: a token is in one
// effect only.
func (c *calcState) markPayback(res *Result) {
	if c.phase != PhaseHot || c.unknown || c.pingHot || c.readEnd < 0 || len(c.clientBPs) == 0 || len(c.prevFwdBPs) == 0 ||
		c.noCacheTokens() || c.splitItem() != "" || c.bstarFwd >= c.readEnd {
		return
	}
	for _, e := range res.CacheEffects {
		if e.Kind == EffectRescue || e.Kind == EffectTTLRescue || e.Kind == EffectSplit {
			return
		}
	}
	// The read past B* caps the moves: up to B* the client body reads too. The read may end before readEnd,
	// which extentWith takes within a tolerance, so the cap cuts at the end: the provider read a prefix, and
	// the tokens past it were written, not read. The bytes are added in position order and converted once
	// per position as a running total, so rounding costs one token in all, not one a position.
	left := c.usage.CacheRead
	if c.bstarFwd >= 0 {
		left -= c.ratio.upTo(c.fwd, c.bstarFwd)
	}
	var cum parts
	var n, n5, n1, done int
	first, last := -1, -1
	for i := c.bstarFwd + 1; i <= min(c.readEnd, len(c.fwd.Pos)-1) && done < left; i++ {
		j := mustFlat(c.client, c.fwdToClient(c.fwd.position(i)))
		if j < 0 || mustFlat(c.fwd, c.clientToFwd(c.client.position(j))) != i {
			continue
		}
		cum.add(minParts(c.fwd.between(i-1, i), c.client.between(j-1, j)))
		d := min(c.ratio.tokensOf(cum), left) - done
		done += d
		switch ttl, _, ok := c.writeBP(c.client, c.clientBPs, j); {
		case !ok:
			n += d // the client body would have billed it as input
		case ttl == "1h":
			n1 += d // the client body would have written it: after B* it has no entry to read
		default:
			n5 += d
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	if done <= 0 {
		return
	}
	e := CacheEffect{Kind: EffectMarkPayback, Estimated: true}
	for _, b := range c.prevFwdBPs { // the host's breakpoint that the read reaches
		if b.at >= first && b.at <= last {
			e.ChangeID = c.changeAt(b.at)
		}
	}
	if first > 0 {
		p := c.fwd.position(first - 1)
		e.From = &p
	}
	to := c.fwd.position(last)
	e.To = &to
	e.Saved.Input, e.Saved.CacheWrite5m, e.Saved.CacheWrite1h = n, n5, n1
	e.Caused.CacheRead = n + n5 + n1
	c.addEffect(res, e)
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
		if (best < 0 || x.at < best) && (c.r.facts.MinCacheable <= 0 || c.ratio.upTo(body, x.at) >= c.r.facts.MinCacheable) {
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
		if c.ratioTooHigh() && c.fwd.unseen() > 0 {
			return ReasonMediaUnseen // the extra tokens may be that media: say so, not that the usage is wrong
		}
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

// ratioTooHigh says whether the text carries more than 2.0 tokens a byte (the upper limit of check 9).
// A body with no text bytes and tokens left after the media price is too high (an infinite ratio).
func (c *calcState) ratioTooHigh() bool {
	t, n := float64(c.ratio.tokens), float64(c.ratio.bytes)
	if n == 0 {
		return t > 0
	}
	return t > maxTokensPerByte*n
}

// badBreakpoints says whether the forwarded body has more than 4 breakpoints, or any breakpoint
// has a TTL that EntryFacts.Provider does not allow: by default "5m", "1h" or empty (empty is the 5m
// default); Provider.TTLs can only narrow that list.
func (c *calcState) badBreakpoints() bool {
	if len(c.r.breakpoints) > maxBreakpoints {
		return true
	}
	for _, list := range [][]Breakpoint{c.r.clientBreakpoints, c.r.breakpoints} {
		for _, b := range list {
			if !c.r.facts.Provider.allowsTTL(b.TTL) {
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
			res.Checks = append(res.Checks, sizeRangeFailure(it.op.Operator, sz[0].Bytes, sz[1].Bytes, l.before, l.after))
		case it.bad == ReasonBadUsage:
			res.Checks = append(res.Checks, CheckFailure{Check: CheckBadUsage, Operator: it.op.Operator, Detail: callDetail(it.op)})
		}
	}
	if c.invalid == "" {
		return
	}
	detail := fmt.Sprintf("usage=%d/%d/%d/%d breakpoints=%d forwarded bytes=%d", c.usage.Input, c.usage.CacheRead,
		c.usage.CacheWrite, c.usage.CacheWrite1h, len(c.r.breakpoints), c.ratio.bytes)
	if c.invalid == ReasonMediaUnseen {
		detail += fmt.Sprintf(" unseen media blocks=%d: their tokens are not in the bytes", c.fwd.unseen())
	}
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
					res.Checks = append(res.Checks, sizeTokensFailure(it.op.Operator, sp.before, sp.after, bbad, abad))
				}
				if !bx || !ax {
					estimated = true
				}
				d := bt - at
				if !it.op.DryRun {
					// what the host's tokens add to the saving beyond the ratio's figure: the
					// conservation check compares at the ratio (check 6)
					c.hostGap += d - (c.ratio.sizeOf(sp.before.Bytes, bm) - c.ratio.sizeOf(sp.after.Bytes, am))
				}
				if d > 0 {
					// in tools or system the position counts content that earlier changes added (accountant#96)
					from := c.clientSide(k, sp.from)
					c.addTier(&p.Saved, c.client, false, mustFlat(c.client, from), c.bstar, c.clientBPs, d)
					if !it.op.DryRun {
						c.interaction(res, from, d)
					}
				} else if d < 0 {
					fp, _ := c.fwdSide(k, sp.from)
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
		if bm.media == 0 && am.media == 0 {
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
		c.mediaGap += c.ratio.sizeOf(sp.before.Bytes, bm) - c.ratio.sizeOf(sp.after.Bytes, am)
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
	if !c.pingHot || c.r.facts.SharedWarm != nil || c.pingRead() == nil || c.usage.CacheRead <= 0 {
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
	addWrite(&res.Interaction, ttlName(c.r.facts.TTL), d)
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
		res.Checks = append(res.Checks, conservationFailure(want, net))
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
		res.Checks = append(res.Checks, mediaTokensFailure(c.usage.Input+c.usage.CacheRead+c.usage.CacheWrite, media, c.ratio.mediaTok, note))
	}
	// With the media capped (check 11) the room is 0, so any count is dropped. The cause is the media,
	// and media_tokens already reports it.
	if c.ratio.countsDropped && !c.ratio.capped && !c.noUsage && c.invalid == "" {
		all := c.fwd.between(-1, len(c.fwd.Pos)-1)
		res.Checks = append(res.Checks, opaqueTokensFailure(c.usage.Input+c.usage.CacheRead+c.usage.CacheWrite, all.ctok, all.counted, all.opaque))
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
				res.Checks = append(res.Checks, CheckFailure{Check: CheckUniqueChange, Operator: op.Operator})
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
				res.Checks = append(res.Checks, spansFailure(op.Operator, b, a, op.SizeBefore.Bytes, op.SizeAfter.Bytes))
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
			res.Checks = append(res.Checks, sumFailure(sections[s], want, got[s]))
		}
	}
}

// activeOps is the rewrite and setting operations that are still applied, in client space (§4.3).
func (c *calcState) activeOps() []Operation {
	var out []Operation
	for k, it := range c.items {
		if it.ended || it.bad != "" || it.op.ChangeID == "" || (it.op.Kind != KindRewrite && it.op.Kind != KindSetting) {
			continue
		}
		op := it.op
		spans := c.storedSpans(k, it)
		op.At = spans[0].from
		if len(op.Spans) > 0 {
			op.Spans = make([]Span, len(spans))
			for i, sp := range spans {
				op.Spans[i] = Span{Section: sp.from.Section, From: sp.from.Index, To: sp.to.Index, SizeBefore: sp.before, SizeAfter: sp.after}
			}
		}
		out = append(out, op)
	}
	return out
}

// storedSpans is the spans of item k as the next request reads them. A position in tools or system counts the
// content that changes before k added before it. A change that ended leaves Active, so the next request no
// longer takes its content out of the position: take it out here (accountant#94). Content that stays active
// stays counted, because the next request takes it out itself. A change reported in this request never
// counted the content that ended, so addedBefore gives 0 for it (accountant#100).
func (c *calcState) storedSpans(k int, it *item) []span {
	if it.op.Kind != KindRewrite {
		return it.spans
	}
	out := make([]span, len(it.spans))
	for i, sp := range it.spans {
		sp.from.Index -= c.addedBefore(k, sp.from, true)
		sp.to.Index -= c.addedBefore(k, sp.to, true)
		out[i] = sp
	}
	return out
}

// nextState is the state after this request: the applied rewrite and setting operations in client
// space, this request's fingerprint, and what it left cached (§4.6, §6.1).
func (c *calcState) nextState() ThreadState {
	next := ThreadState{Version: c.s.Version, Active: c.activeOps(), Requests: c.s.Requests + 1}
	next.FirstApplied = withFirstApplied(next.Active, c.s.FirstApplied, next.Requests)
	fp := &Fingerprint{Thread: c.r.thread.ID, Model: c.r.meta.Model, ModelKey: c.r.meta.ModelKey, At: c.r.meta.At, Client: c.client, Forwarded: c.fwd,
		ClientBreakpoints: c.r.clientBreakpoints, ForwardedBreakpoints: c.r.breakpoints}
	if c.downgraded() { // the entry was written at 5m: the next request must not judge it by the 1h it asked for
		fp.ForwardedBreakpoints = append([]Breakpoint(nil), c.r.breakpoints...)
		for i := range fp.ForwardedBreakpoints {
			fp.ForwardedBreakpoints[i].TTL = "5m"
		}
	}
	for _, it := range c.items {
		if !it.ended && it.bad == "" && !it.op.DryRun && it.op.MessageSplice != nil {
			fp.splices = append(fp.splices, *it.op.MessageSplice)
		}
	}
	switch {
	case c.skipsPricing():
		next.Cache = CacheState{} // unknown
	case c.cachedFwd < 0:
		next.Cache = CacheState{CachePhase: PhaseCold, EntryTTL: c.r.facts.TTL}
	default:
		fp.Cached = c.fwd.position(c.cachedFwd)
		ext := fp.Cached
		ttl := ttlName(c.r.facts.TTL)
		for _, b := range c.fwdBPs { // a 1h breakpoint under the extent holds the head at 1h whatever Facts.TTL says
			if b.ttl == "1h" && b.at <= c.cachedFwd {
				ttl = "1h"
			}
		}
		next.Cache = CacheState{CachePhase: PhaseHot, Extent: &ext, TTL: ttl, EntryTTL: c.r.facts.TTL}
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
// stable half. A read that comes within the tolerance of the previous extent (a short turn adds less than one read
// step, 128 tokens by default, plus 2%) is then no proof that nothing broke: the read is the stable half, and the extent is the
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
