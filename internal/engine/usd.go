package engine

import "time"

// Tier is a price class of tokens. A host prices each tier with its own rate table.
type Tier string

const (
	TierInput        Tier = "input"
	TierCacheRead    Tier = "cache_read"
	TierCacheWrite5m Tier = "cache_write_5m"
	TierCacheWrite1h Tier = "cache_write_1h"
	TierOutput       Tier = "output"
)

// tierOrder fixes the order of every sum, so a figure is the same on every run.
var tierOrder = [...]Tier{TierInput, TierCacheRead, TierCacheWrite5m, TierCacheWrite1h, TierOutput}

// SizedPricer gives the host's rate for one token. The host owns the rate table. The library knows no
// price (D3).
//
// Some providers bill a large request at a higher rate for ALL its tokens, so the rate depends on the
// prompt size. promptTokens is the prompt size of the request the token belongs to: fresh input plus
// cache read plus cache write, as the provider counts it. promptTokens 0 means the size is unknown: the
// host then gives the rate of a small request. [Figure] never passes a negative size.
//
// estimated is true when the rate is a fallback or a guess. A rate of 0 with estimated false is a real
// free rate. An unknown model must return estimated true.
type SizedPricer interface {
	PriceAt(model string, at time.Time, tier Tier, promptTokens int64) (usdPerToken float64, estimated bool)
}

// FigureInput is what [Figure] needs besides the pricer: the billed usage of the request and its
// [Result].
type FigureInput struct {
	Model string
	At    time.Time
	// Billed is the usage the provider billed. The size of the request sent is Input + CacheRead +
	// CacheWrite. The size without the operations derives from it.
	Billed Usage
	// Output is the output tokens the provider billed. 0 means the host does not give it, and the output
	// tier has no re-rate. Output never counts toward the prompt size.
	Output int
	Result Result
	// Unsized says that the host's rates do not depend on the prompt size. [Figure] then derives no
	// size, passes 0 to the pricer, and books no re-rate.
	Unsized bool
}

// Direction of a [USDLine].
const (
	DirSaved = "saved"
	DirAdded = "added"
)

// USDLine is one priced amount of one operation in one tier. USD is never negative: Direction gives the
// sign. A line with Direction "added" is a cost.
type USDLine struct {
	Kind      Kind
	Operator  string
	Tier      Tier
	Direction string
	USD       float64
	// ReRate marks a line that is the operation's share of the re-rate, not the price of tokens that moved.
	ReRate bool
}

// USDEffect is one cache effect, priced. SavedUSD is its Saved tokens at the size without the
// operations. CausedUSD is its Caused tokens at the billed size. Both are never negative. The value of
// the effect is SavedUSD minus CausedUSD: a break, a markPremium and a ttlPremium come out negative.
// An effect that no figure books is not priced, so both are 0: an InBaseline effect that is not a
// break, and an effect of an unknown kind.
type USDEffect struct {
	Effect    CacheEffect
	SavedUSD  float64
	CausedUSD float64
	// Counted says that the effect is part of the bill difference. An InBaseline effect, an effect of an
	// unknown kind, and a break owned by a change no counted operation made are not counted.
	Counted bool
	// Owner is the Operator of the counted operation that owns the effect, or "".
	Owner string
}

// USDFigure is the savings of one request in money. [Figure] makes it. It is the only place in which
// the savings of a request are turned into dollars: a host maps its fields to its own rows and columns,
// and does not price a saving itself.
//
// Keys of the maps are the Operator of the operation, as the host gave it.
type USDFigure struct {
	// Lines is every priced amount of the counted operations: moved tokens (saved at the size without
	// the operations, caused at the billed size), the call usage of each spend, and the re-rate shares.
	Lines []USDLine
	// Effects is every cache effect of the Result, in order.
	Effects []USDEffect

	// ByComponent is the net dollars of each operation: saved minus caused, plus the breaks and the mark
	// effects it owns, plus its share of the re-rate.
	ByComponent map[string]float64
	// ByComponentFirst and ByComponentReplay split the SAVED tokens of each operation by tier: the write
	// and input tiers (content the provider had not cached: a first removal, with the re-rate share) and
	// the read tier (a replay of content in the cached prefix).
	ByComponentFirst, ByComponentReplay map[string]float64
	// ByComponentSaved is the dollars of the Saved tokens of each operation, at the size without the
	// operations. ByComponentSavedAsRead is the same tokens priced at the read tier. A host that wants a
	// lower bound of a figure it cannot trust uses the two.
	ByComponentSaved, ByComponentSavedAsRead map[string]float64

	// KeepAliveSavedUSD is the rescue effects. SplitUSD is the split effects. TTLUSD is the premium
	// (negative) and the rescue of the head TTL.
	KeepAliveSavedUSD, SplitUSD, TTLUSD float64
	// EndedBreakUSD is the break that an ended change left behind. No operation owns it, but the client
	// body would have read that prefix, so it is part of the bill difference. Negative: a cost.
	EndedBreakUSD float64
	// UnattributedBreakUSD is the breaks the client body has too (InBaseline), and the breaks of a change
	// no counted operation made. It is shown and never counted. A positive number is a cost.
	UnattributedBreakUSD float64
	// UnownedMarkUSD is the value of the markPremium and markPayback effects that no counted operation
	// owns. Counted in DeltaUSD. The host chooses its row.
	UnownedMarkUSD float64
	// UnownedReRateUSD is the re-rate when no operation moved the size to own it. Counted in DeltaUSD.
	UnownedReRateUSD float64
	// InteractionUSD is the net value of Result.Interaction minus Result.InteractionCaused, at the size
	// without the operations. Signed. Counted in TotalUSD, not in DeltaUSD.
	InteractionUSD float64
	// CallUSD is the cost of the spend calls (the usage of each model call, output included). It is a
	// separate bill, so it is in neither DeltaUSD nor TotalUSD.
	CallUSD float64

	// DeltaUSD is the sum of ByComponent, EndedBreakUSD, UnownedMarkUSD and UnownedReRateUSD. It leaves
	// out KeepAliveSavedUSD, SplitUSD, TTLUSD, InteractionUSD and UnattributedBreakUSD.
	DeltaUSD float64

	// Unpriced lists "opID:reason" for each operation the library did not price. Every entry has this
	// one form, so a host splits it at the first colon. A reason with no operation has an empty opID:
	// ":effect_<kind>_unknown_kind" for an effect of an unknown kind. A call on an operation that is not
	// a spend gives "opID:not_spend". An unpriced operation is not a zero saving.
	Unpriced []string
	// Estimated is true when a counted operation or counted effect is an estimate, or a rate the pricer used is.
	Estimated bool
}

// TotalUSD is the whole bill difference of the request: the bill without the operations minus the bill
// as sent. It is DeltaUSD plus the effects and the interaction.
func (f USDFigure) TotalUSD() float64 {
	return f.DeltaUSD + f.KeepAliveSavedUSD + f.SplitUSD + f.TTLUSD + f.InteractionUSD
}

// Figure turns a [Result] into dollars. It is deterministic and does no I/O.
//
// Two prompt sizes matter. The request sent has the billed size. The request without the operations has
// the billed size plus the tokens the counted operations saved, minus the tokens they caused. A counted
// operation is a non-DryRun operation that is not a spend. A cache effect and the interaction move
// tokens between tiers, so they do not change the size. Saved tokens (of an operation or an effect) are
// priced at the size without the operations. Caused tokens are priced at the billed size.
//
// The tokens that stay in both requests change rate when the two sizes sit on different sides of a price
// line (the re-rate). Per tier the amount is stay x (rate without - rate billed). Stay is the billed
// tokens of the tier, minus the tokens the counted operations caused, minus the tokens the counted
// effects caused (an effect prices its own whole difference). The output tier counts too, with
// FigureInput.Output: all of it stays. The re-rate can be negative. The operations that moved the size
// own it. If the operations made the prompt smaller, each operation with net saved tokens (saved minus
// caused, above 0) takes a share in proportion to them. If they made it larger, each operation with net
// added tokens takes a share in proportion to those. So the components add up to the request total, and
// an operation that pushes a prompt over a price line pays the premium of the tokens that stay. With no
// such operation the re-rate is UnownedReRateUSD.
//
// A spend call is priced at its own model, and at the size of its own usage.
func Figure(in FigureInput, p SizedPricer) USDFigure {
	f := USDFigure{
		ByComponent: map[string]float64{}, ByComponentFirst: map[string]float64{}, ByComponentReplay: map[string]float64{},
		ByComponentSaved: map[string]float64{}, ByComponentSavedAsRead: map[string]float64{},
	}
	res := in.Result
	fp := figurePricer{p: p, model: in.Model, at: in.At}
	billed, base := int64(0), int64(0)
	if !in.Unsized {
		billed, base = sizes(in)
	}

	// An op the library refused to price must not book that call's usage.
	refused := map[string]bool{}
	listedNoID := false // the library listed an entry with no OpID: it may be for any op that has no id
	for _, u := range res.Unpriced {
		if u.OpID != "" {
			refused[u.OpID] = true
		} else {
			listedNoID = true
		}
	}
	// An unpriced op is also in Ops, with zero tokens. Skipping it keeps a "saved $0" off the figure for
	// something unmeasured. OpIDs are host-supplied and can be empty or duplicated, so an op is skipped
	// only when its OpID is listed AND it has no tokens: that can never drop a real dollar.

	var shares []opShare
	owner := map[string]string{} // ChangeID -> Operator; an empty ChangeID never owns
	for _, op := range res.Ops {
		if op.DryRun {
			continue
		}
		f.Estimated = f.Estimated || op.Estimated
		if op.Call != nil {
			if op.Kind != KindSpend && !refused[op.OpID] && !(op.OpID == "" && listedNoID) {
				// A call on an op that is not a spend has no price rule: say so, never skip it silently. The
				// library lists most of these itself. An op with no id cannot be matched to an entry, so any
				// entry with no OpID counts as its entry: no duplicate, and no reason of ours beside the library's.
				f.Unpriced = append(f.Unpriced, op.OpID+":"+ReasonNotSpend)
			}
			if op.Kind == KindSpend && !refused[op.OpID] {
				model := op.Call.Model
				if model == "" {
					model = in.Model
				}
				cp := fp
				cp.model = model
				f.addCall(&cp, op, callSize(op.Call.Usage))
			}
			continue
		}
		if op.ChangeID != "" {
			if _, dup := owner[op.ChangeID]; !dup { // first owner wins; the library flags duplicates
				owner[op.ChangeID] = op.Operator
			}
		}
		if refused[op.OpID] && op.Saved.IsZero() && op.Caused.IsZero() {
			continue
		}
		if n := tierSum(op.Saved) - tierSum(op.Caused); n != 0 {
			shares = append(shares, opShare{op.Operator, op.Kind, n})
		}
		saved, sEst := fp.lines(&f, op, op.Saved, DirSaved, base)
		caused, cEst := fp.lines(&f, op, op.Caused, DirAdded, billed)
		read, _ := fp.usd(TierTokens{CacheRead: op.Saved.CacheRead}, base)
		asRead, rEst := fp.usdAs(TierCacheRead, tierSum(op.Saved), base)
		f.Estimated = f.Estimated || sEst || cEst || rEst
		net := saved - caused
		f.ByComponent[op.Operator] += net
		f.ByComponentReplay[op.Operator] += read
		f.ByComponentFirst[op.Operator] += saved - read
		f.ByComponentSaved[op.Operator] += saved
		f.ByComponentSavedAsRead[op.Operator] += asRead
		f.DeltaUSD += net
	}

	var effCaused TierTokens // the Caused tokens of the counted effects
	for _, ce := range res.CacheEffects {
		// An effect of an unknown kind has no price rule: it is not priced.
		var saved, caused float64
		var e1, e2 bool
		// Price only what a figure books. An InBaseline break feeds UnattributedBreakUSD. Any other InBaseline
		// effect is in neither bill and nobody books it.
		if knownEffect(ce.Kind) && (!ce.InBaseline || ce.Kind == EffectBreak) {
			saved, e1 = fp.usd(ce.Saved, base)
			caused, e2 = fp.usd(ce.Caused, billed)
		}
		ue := USDEffect{Effect: ce, SavedUSD: saved, CausedUSD: caused}
		value := saved - caused
		counted := !ce.InBaseline
		switch ce.Kind {
		case EffectBreak:
			own, owned := owner[ce.ChangeID]
			switch {
			case ce.InBaseline: // the client body has this break too: shown, not counted
				f.UnattributedBreakUSD -= value
			case ce.ChangeID == "": // an ended change left it behind: counted, owned by no operation
				f.EndedBreakUSD += value
				f.DeltaUSD += value
			case owned:
				ue.Owner = own
				f.ByComponent[own] += value
				f.DeltaUSD += value
			default: // a ChangeID no counted operation owns (a dry run): shown, never charged
				counted = false
				f.UnattributedBreakUSD -= value
			}
		case EffectRescue, EffectSplit, EffectTTLPremium, EffectTTLRescue, EffectMarkPremium, EffectMarkPayback:
			if ce.InBaseline {
				break
			}
			switch ce.Kind {
			case EffectRescue:
				f.KeepAliveSavedUSD += value
			case EffectSplit:
				f.SplitUSD += value
			case EffectTTLPremium, EffectTTLRescue:
				f.TTLUSD += value
			default: // a mark effect: charged to the operation whose change it is, else to nobody
				if own, ok := owner[ce.ChangeID]; ok && ce.ChangeID != "" {
					ue.Owner = own
					f.ByComponent[own] += value
				} else {
					f.UnownedMarkUSD += value
				}
				f.DeltaUSD += value
			}
		default:
			counted = false
			// The same "opID:reason" form as every entry: no op, so the id is empty.
			f.Unpriced = append(f.Unpriced, ":effect_"+string(ce.Kind)+"_unknown_kind")
		}
		ue.Counted = counted
		if counted { // an effect that is never counted marks nothing as estimated
			f.Estimated = f.Estimated || ce.Estimated || e1 || e2
			effCaused = effCaused.Add(ce.Caused)
		}
		f.Effects = append(f.Effects, ue)
	}

	if !in.Unsized && base > 0 {
		f.reRate(&fp, in, shares, effCaused, billed, base)
	}

	// The tokens no operation owns on a rescued request. The ops already book InteractionCaused (the
	// read), so the figure holds the difference (design §6.2). Both sides are client-side figures, so
	// the whole net takes the one size without the operations.
	net := res.Interaction.Sub(res.InteractionCaused)
	if !net.IsZero() {
		usd, est := fp.usd(net, base)
		f.InteractionUSD = usd
		f.Estimated = f.Estimated || est
	}

	for _, u := range res.Unpriced {
		f.Unpriced = append(f.Unpriced, u.OpID+":"+u.Reason)
	}
	return f
}

func knownEffect(k CacheEffectKind) bool {
	switch k {
	case EffectBreak, EffectRescue, EffectSplit, EffectTTLPremium, EffectTTLRescue, EffectMarkPremium, EffectMarkPayback:
		return true
	}
	return false
}

// opShare is a counted operation with a net change of tokens: net saved, so net > 0, or net added, so
// net < 0. The operations on the side the size moved own the re-rate.
type opShare struct {
	op   string
	kind Kind
	net  int
}

// addCall books the usage of a spend call as added, per tier, output included.
func (f *USDFigure) addCall(fp *figurePricer, op PricedOp, size int64) {
	u := op.Call.Usage
	w5 := max(u.CacheWrite-u.CacheWrite1h, 0)
	for _, t := range tierOrder {
		n := 0
		switch t {
		case TierInput:
			n = u.Input
		case TierCacheRead:
			n = u.CacheRead
		case TierCacheWrite5m:
			n = w5
		case TierCacheWrite1h:
			n = u.CacheWrite1h
		case TierOutput:
			n = u.Output
		}
		if n <= 0 {
			continue
		}
		rate, est := fp.rate(t, size)
		cost := rate * float64(n)
		f.Estimated = f.Estimated || est
		f.CallUSD += cost
		f.Lines = append(f.Lines, USDLine{Kind: op.Kind, Operator: op.Operator, Tier: t, Direction: DirAdded, USD: cost})
	}
}

// reRate books the change of rate of the tokens that stay.
func (f *USDFigure) reRate(fp *figurePricer, in FigureInput, shares []opShare, effCaused TierTokens, billed, base int64) {
	u := in.Billed
	stay := map[Tier]int{TierInput: u.Input, TierCacheRead: u.CacheRead,
		TierCacheWrite5m: max(u.CacheWrite-u.CacheWrite1h, 0), TierCacheWrite1h: u.CacheWrite1h, TierOutput: in.Output}
	sub := func(t TierTokens) {
		stay[TierInput] -= t.Input
		stay[TierCacheRead] -= t.CacheRead
		stay[TierCacheWrite5m] -= t.CacheWrite5m
		stay[TierCacheWrite1h] -= t.CacheWrite1h
	}
	for _, op := range in.Result.Ops {
		if !op.DryRun && op.Call == nil {
			sub(op.Caused)
		}
	}
	// A counted effect moves tokens between tiers. Its Caused tokens are billed, but the request without
	// the operations holds them in another tier, and the effect already prices that whole difference. So
	// they do not stay. A break the client body has too (InBaseline) is booked nowhere: its tokens stay.
	sub(effCaused)

	amount := map[Tier]float64{}
	for _, t := range tierOrder {
		n := stay[t]
		if n <= 0 {
			continue
		}
		rb, e1 := fp.rate(t, base)
		rs, e2 := fp.rate(t, billed)
		f.Estimated = f.Estimated || e1 || e2
		if d := float64(n) * (rb - rs); d != 0 {
			amount[t] = d
		}
	}
	if len(amount) == 0 {
		return
	}
	// The size moved by the sum of the nets. The operations that moved it that way own the re-rate, by
	// their tokens: the net savers when the size went down, the net adders when it went up.
	sum := 0
	for _, s := range shares {
		sum += s.net
	}
	var owners []opShare
	total := 0
	for _, s := range shares {
		if (sum > 0 && s.net > 0) || (sum < 0 && s.net < 0) {
			s.net = max(s.net, -s.net)
			owners = append(owners, s)
			total += s.net
		}
	}
	shares = owners
	if total == 0 {
		for _, t := range tierOrder {
			f.UnownedReRateUSD += amount[t]
		}
		f.DeltaUSD += f.UnownedReRateUSD
		return
	}
	for _, t := range tierOrder {
		d, ok := amount[t]
		if !ok {
			continue
		}
		for _, s := range shares {
			part := d * float64(s.net) / float64(total)
			f.ByComponent[s.op] += part
			f.ByComponentFirst[s.op] += part
			f.DeltaUSD += part
			dir := DirSaved
			mag := part
			if part < 0 {
				dir, mag = DirAdded, -part
			}
			f.Lines = append(f.Lines, USDLine{Kind: s.kind, Operator: s.op, Tier: t, Direction: dir, USD: mag, ReRate: true})
		}
	}
}

// sizes derives the two prompt sizes. 0 means unknown. The size without the operations is the billed
// size plus the tokens the counted operations saved, minus the tokens they caused. A size that is not
// above 0 is not credible and is 0.
func sizes(in FigureInput) (billed, base int64) {
	billed = int64(in.Billed.Input) + int64(in.Billed.CacheRead) + int64(in.Billed.CacheWrite)
	if billed <= 0 {
		return 0, 0
	}
	base = billed
	for _, op := range in.Result.Ops {
		if op.DryRun || op.Call != nil {
			continue
		}
		base += int64(tierSum(op.Saved)) - int64(tierSum(op.Caused))
	}
	if base <= 0 {
		base = 0
	}
	return billed, base
}

func callSize(u CallUsage) int64 { return int64(u.Input) + int64(u.CacheRead) + int64(u.CacheWrite) }

// figurePricer prices tokens of one request with the host's pricer.
type figurePricer struct {
	p     SizedPricer
	model string
	at    time.Time
}

func (fp *figurePricer) rate(t Tier, size int64) (float64, bool) {
	return fp.p.PriceAt(fp.model, fp.at, t, size)
}

// usd prices the tokens of each tier. A tier with no tokens makes no price call and no estimated mark.
func (fp *figurePricer) usd(tt TierTokens, size int64) (usd float64, est bool) {
	for _, t := range tierOrder {
		n := 0
		switch t {
		case TierInput:
			n = tt.Input
		case TierCacheRead:
			n = tt.CacheRead
		case TierCacheWrite5m:
			n = tt.CacheWrite5m
		case TierCacheWrite1h:
			n = tt.CacheWrite1h
		}
		if n == 0 {
			continue
		}
		r, e := fp.rate(t, size)
		usd += r * float64(n)
		est = est || e
	}
	return usd, est
}

// usdAs prices n tokens at one tier.
func (fp *figurePricer) usdAs(t Tier, n int, size int64) (float64, bool) {
	if n == 0 {
		return 0, false
	}
	r, e := fp.rate(t, size)
	return r * float64(n), e
}

// lines prices the tokens of one operation and adds one line for each tier that has tokens. It returns
// the total.
func (fp *figurePricer) lines(f *USDFigure, op PricedOp, tt TierTokens, dir string, size int64) (usd float64, est bool) {
	for _, t := range tierOrder {
		n := 0
		switch t {
		case TierInput:
			n = tt.Input
		case TierCacheRead:
			n = tt.CacheRead
		case TierCacheWrite5m:
			n = tt.CacheWrite5m
		case TierCacheWrite1h:
			n = tt.CacheWrite1h
		}
		if n <= 0 {
			continue
		}
		r, e := fp.rate(t, size)
		cost := r * float64(n)
		usd += cost
		est = est || e
		f.Lines = append(f.Lines, USDLine{Kind: op.Kind, Operator: op.Operator, Tier: t, Direction: dir, USD: cost})
	}
	return usd, est
}
