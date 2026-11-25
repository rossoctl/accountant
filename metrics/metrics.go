// Package metrics turns an accountant [accountant.Result] into OpenTelemetry counters. It imports
// the OpenTelemetry API only. The SDK, the exporter and the push settings belong to the host, so
// each proxy brings its own.
//
// A host builds one [Sink] with [New] and calls Observe after each Compute. Observe adds to
// in-memory counters. It makes no network call and takes a lock only to add one of the first
// maxComponents names, so it adds a small fixed time to the request path.
//
// The module knows no price. The host gives it a [Pricer] (metrics spec R47, context-guru#886), so
// each proxy keeps its own rate table.
package metrics

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rossoctl/accountant"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Tier is a price class of tokens (metrics spec R34, context-guru#886).
type Tier string

const (
	TierInput        Tier = "input"
	TierCacheRead    Tier = "cache_read"
	TierCacheWrite5m Tier = "cache_write_5m"
	TierCacheWrite1h Tier = "cache_write_1h"
	TierOutput       Tier = "output"
)

// Pricer gives the host's rate for one token. The host owns the rate table. If the rate is a
// fallback, or a guess, the Pricer sets estimated. A rate of 0 with estimated false is a real free
// rate. An unknown model must return estimated true.
type Pricer interface {
	Price(model string, at time.Time, tier Tier) (usdPerToken float64, estimated bool)
}

// SizedPricer is an optional extension of [Pricer]. Some providers bill a large request at a higher
// rate for ALL its tokens, so the rate depends on the prompt size. promptTokens is the prompt size of
// the request the token belongs to: fresh input plus cache read plus cache write, as the provider
// counts it. The sink never passes 0 or a negative size: when it does not know the size, it calls
// [Pricer.Price] instead.
//
// If the host's Pricer also implements SizedPricer, the sink prices each side at its own size. Saved
// tokens use the size the request would have had without the operations. Caused tokens use the billed
// size. A Pricer without PriceAt gets the same calls and figures as before.
type SizedPricer interface {
	PriceAt(model string, at time.Time, tier Tier, promptTokens int64) (usdPerToken float64, estimated bool)
}

// maxComponents bounds the label `component` when the host gives no list (metrics spec 5.7, context-guru#886).
const maxComponents = 64

// other is the value of `component` for a name outside the list.
const other = "other"

// Option changes a [Sink].
type Option func(*Sink)

// WithComponents sets the operator names that may appear as the `component` label. Any other name
// becomes "other". Names are given without the host prefix.
func WithComponents(names ...string) Option {
	return func(s *Sink) {
		s.allowed = make(map[string]bool, len(names))
		for _, n := range names {
			s.allowed[n] = true
		}
	}
}

// Sink is the OpenTelemetry [Observer] for the savings set (metrics spec 5.2, context-guru#886).
type Sink struct {
	pricer Pricer
	sized  SizedPricer // pricer, when it also implements SizedPricer; else nil

	saved, added             metric.Int64Counter
	savedUSD, addedUSD       metric.Float64Counter
	effectUSD                metric.Float64Counter
	requests, unpriced       metric.Int64Counter
	estimated, checkFailures metric.Int64Counter

	interactionUSD metric.Float64Counter

	allowed map[string]bool // nil: accept the first maxComponents names
	// seen is copy-on-write: the hot path reads it with no lock. mu serialises the add of a new name,
	// at most maxComponents times in the life of the sink. Once seen is full, no call takes mu.
	seen atomic.Pointer[map[string]bool]
	mu   sync.Mutex
}

var _ Observer = (*Sink)(nil)

// New builds every instrument once. It returns an error if the meter refuses one.
func New(m metric.Meter, p Pricer, opts ...Option) (*Sink, error) {
	s := &Sink{pricer: p}
	s.sized, _ = p.(SizedPricer)
	empty := map[string]bool{}
	s.seen.Store(&empty)
	for _, o := range opts {
		o(s)
	}
	var err error
	i64 := func(dst *metric.Int64Counter, name, desc string) {
		if err == nil {
			*dst, err = m.Int64Counter(name, metric.WithDescription(desc))
		}
	}
	f64 := func(dst *metric.Float64Counter, name, desc string) {
		if err == nil {
			*dst, err = m.Float64Counter(name, metric.WithDescription(desc))
		}
	}
	i64(&s.saved, "llm.savings.saved.tokens", "Tokens the provider did not bill, by tier.")
	i64(&s.added, "llm.savings.added.tokens", "Tokens the provider billed because of a change.")
	f64(&s.savedUSD, "llm.savings.saved.usd", "Saved tokens priced at the tier rate at request time.")
	f64(&s.addedUSD, "llm.savings.added.usd", "Added tokens priced the same way.")
	f64(&s.effectUSD, "llm.savings.cache_effect.usd", "Cache effects by effect, cause and direction. Not in saved or added: add it to them for the net saving (design §6.2).")
	f64(&s.interactionUSD, "llm.savings.interaction.usd", "Net value of the tokens no operation owns on a rescued request (Result.Interaction minus InteractionCaused). direction is saved or added. Not in saved or added: add it to them for the net saving (design §6.2).")
	i64(&s.requests, "llm.savings.requests", "Requests the accountant saw, by result.")
	i64(&s.unpriced, "llm.savings.unpriced", "Operations the accountant did not price, by reason.")
	i64(&s.estimated, "llm.savings.estimated", "Requests whose figure is marked estimated.")
	i64(&s.checkFailures, "llm.savings.check_failures", "Failed accountant checks.")
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Observe adds one request to the counters. It skips dry-run operations.
//
// Money follows Billing.Mode (design §7.1). Only api books money. For unknown, subscription or an unset
// mode, no USD counter moves, because that dollar may be a subscription user's and never enters a money total.
// Token counters always move. An unknown request counts as estimated.
func (s *Sink) Observe(o Observation) {
	ctx := context.Background()
	res, model, at := o.Result, o.Meta.Model, o.Meta.At
	mode := o.Meta.Billing.Mode
	money := mode == accountant.BillingAPI
	anyEstimated := !money // unknown, subscription's weights or an unset mode are all estimates

	// A spend whose call the library refused to price must not book that call's usage.
	refused := map[string]bool{}
	for _, u := range res.Unpriced {
		if u.OpID != "" {
			refused[u.OpID] = true
		}
	}

	sz := s.sizes(o)

	priced := 0
	for _, op := range res.Ops {
		if op.DryRun {
			continue
		}
		priced++
		anyEstimated = anyEstimated || op.Estimated
		attrs := func(t Tier) attribute.Set {
			return attribute.NewSet(
				attribute.String("kind", string(op.Kind)),
				attribute.String("tier", string(t)),
				attribute.String("component", s.component(op.Operator)),
			)
		}
		e1 := s.book(ctx, s.saved, s.savedUSD, tiers(op.Saved), attrs, model, at, money, sz.base)
		e2 := s.book(ctx, s.added, s.addedUSD, tiers(op.Caused), attrs, model, at, money, sz.billed)
		anyEstimated = anyEstimated || e1 || e2
		if op.Kind == accountant.KindSpend && op.Call != nil && !refused[op.OpID] {
			// The call names its own model. It can differ from the agent's model (metrics spec R49).
			callModel := op.Call.Model
			if callModel == "" {
				callModel = model
			}
			e3 := s.book(ctx, s.added, s.addedUSD, callTiers(op.Call.Usage), attrs, callModel, at, money, callSize(op.Call.Usage))
			anyEstimated = anyEstimated || e3
		}
	}

	for _, e := range res.CacheEffects {
		if e.InBaseline {
			continue // on the record, not part of the bill difference
		}
		anyEstimated = anyEstimated || e.Estimated
		if !money {
			continue
		}
		for dir, tt := range map[string]accountant.TierTokens{"saved": e.Saved, "added": e.Caused} {
			size := sz.billed
			if dir == "saved" {
				size = sz.base
			}
			usd, est := s.price(tiers(tt), model, at, size)
			anyEstimated = anyEstimated || est
			if usd == 0 {
				continue
			}
			s.effectUSD.Add(ctx, usd, metric.WithAttributeSet(attribute.NewSet(
				attribute.String("effect", effectName(e.Kind)),
				attribute.String("cause", effectCause(e)),
				attribute.String("direction", dir),
			)))
		}
	}

	// The tokens no operation owns on a rescued request. The ops already book InteractionCaused (the
	// read), so the host adds the difference (design §6.2).
	if money {
		net := map[Tier]int{}
		for t, n := range tiers(res.Interaction) {
			net[t] += n
		}
		for t, n := range tiers(res.InteractionCaused) {
			net[t] -= n
		}
		empty := true
		for _, n := range net {
			empty = empty && n == 0
		}
		usd, est := 0.0, false
		if !empty {
			// A positive tier is saved, so it takes the size without the operations. A negative tier
			// is added, so it takes the billed size.
			pos, neg := map[Tier]int{}, map[Tier]int{}
			for t, n := range net {
				if n > 0 {
					pos[t] = n
				} else if n < 0 {
					neg[t] = n
				}
			}
			usd, est = s.price(pos, model, at, sz.base)
			u2, e2 := s.price(neg, model, at, sz.billed)
			usd, est = usd+u2, est || e2
			anyEstimated = anyEstimated || est
		}
		if usd != 0 {
			dir := "saved"
			if usd < 0 {
				dir, usd = "added", -usd
			}
			s.interactionUSD.Add(ctx, usd, metric.WithAttributes(attribute.String("direction", dir)))
		}
	}

	for _, u := range res.Unpriced {
		s.unpriced.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", u.Reason)))
	}
	for _, c := range res.Checks {
		s.checkFailures.Add(ctx, 1, metric.WithAttributes(attribute.String("check", c.Check)))
	}
	if anyEstimated {
		s.estimated.Add(ctx, 1)
	}
	result := "priced"
	switch {
	case len(res.Unpriced) > 0:
		result = "unpriced"
	case priced == 0:
		result = "skipped"
	}
	s.requests.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

// book adds one op's tokens and their price, and says whether any rate was a fallback. A tier with
// no tokens adds nothing.
func (s *Sink) book(ctx context.Context, tok metric.Int64Counter, usd metric.Float64Counter,
	tt map[Tier]int, attrs func(Tier) attribute.Set, model string, at time.Time, money bool, size int64) (anyEst bool) {
	for t, n := range tt {
		if n <= 0 {
			continue
		}
		set := metric.WithAttributeSet(attrs(t))
		tok.Add(ctx, int64(n), set)
		if !money {
			continue
		}
		rate, est := s.rate(model, at, t, size)
		cost := rate * float64(n)
		usd.Add(ctx, cost, set)
		anyEst = anyEst || est
	}
	return anyEst
}

// rate prices one token. It uses PriceAt when the host's pricer has it and the size is known (size > 0).
// Else it uses Price.
func (s *Sink) rate(model string, at time.Time, t Tier, size int64) (float64, bool) {
	if s.sized != nil && size > 0 {
		return s.sized.PriceAt(model, at, t, size)
	}
	return s.pricer.Price(model, at, t)
}

func (s *Sink) price(tt map[Tier]int, model string, at time.Time, size int64) (usd float64, estimated bool) {
	for t, n := range tt {
		if n == 0 {
			continue // no tokens: no price call, and no estimated mark from an empty tier
		}
		rate, est := s.rate(model, at, t, size)
		usd += rate * float64(n)
		estimated = estimated || est
	}
	return usd, estimated
}

// reqSizes holds the two prompt sizes of one request. 0 means unknown: the sink then uses Price.
type reqSizes struct {
	billed int64 // the prompt size the provider billed
	base   int64 // the prompt size without the counted operations
}

// sizes derives the prompt sizes. The billed size comes from Observation.Usage. The size without the
// operations is the billed size plus the tokens the counted operations saved, minus the tokens they
// caused. Dry-run operations are not counted: they changed nothing. Cache effects and the interaction
// move tokens between tiers and net zero tokens, so they do not change the size. The sink does not
// call this for a pricer without PriceAt.
func (s *Sink) sizes(o Observation) reqSizes {
	if s.sized == nil {
		return reqSizes{}
	}
	billed := int64(o.Usage.Input) + int64(o.Usage.CacheRead) + int64(o.Usage.CacheWrite)
	if billed <= 0 {
		return reqSizes{}
	}
	base := billed
	for _, op := range o.Result.Ops {
		if op.DryRun {
			continue
		}
		base += int64(sum(op.Saved)) - int64(sum(op.Caused))
	}
	if base <= 0 {
		base = 0 // not credible: the saved side then uses Price
	}
	return reqSizes{billed: billed, base: base}
}

func sum(t accountant.TierTokens) int {
	return t.Input + t.CacheRead + t.CacheWrite5m + t.CacheWrite1h
}

// callSize is the prompt size of a spend call, from the call's own usage.
func callSize(u accountant.CallUsage) int64 {
	return int64(u.Input) + int64(u.CacheRead) + int64(u.CacheWrite)
}

// component strips the host prefix ("context-guru.mask" gives "mask") and bounds the value.
func (s *Sink) component(operator string) string {
	name := operator
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return other
	}
	if s.allowed != nil {
		if s.allowed[name] {
			return name
		}
		return other
	}
	cur := *s.seen.Load()
	if cur[name] {
		return name
	}
	if len(cur) >= maxComponents {
		return other // full: no lock, and every later call for this name agrees
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur = *s.seen.Load()
	if cur[name] {
		return name
	}
	if len(cur) >= maxComponents {
		return other
	}
	next := make(map[string]bool, len(cur)+1)
	for k := range cur {
		next[k] = true
	}
	next[name] = true
	s.seen.Store(&next)
	return name
}

func tiers(t accountant.TierTokens) map[Tier]int {
	return map[Tier]int{
		TierInput: t.Input, TierCacheRead: t.CacheRead,
		TierCacheWrite5m: t.CacheWrite5m, TierCacheWrite1h: t.CacheWrite1h,
	}
}

func addTiers(a, b map[Tier]int) map[Tier]int {
	for t, n := range b {
		a[t] += n
	}
	return a
}

// callTiers splits the usage of a component's own LLM call into tiers. CacheWrite1h is the part of
// CacheWrite billed at the one-hour rate, so the 5m part is the difference. A spend call books as added (metrics spec R45).
func callTiers(u accountant.CallUsage) map[Tier]int {
	w5 := u.CacheWrite - u.CacheWrite1h
	if w5 < 0 {
		w5 = 0
	}
	return map[Tier]int{
		TierInput: u.Input, TierCacheRead: u.CacheRead,
		TierCacheWrite5m: w5, TierCacheWrite1h: u.CacheWrite1h, TierOutput: u.Output,
	}
}

// effectName maps the library's camel case kinds to the snake case label values.
func effectName(k accountant.CacheEffectKind) string {
	switch k {
	case accountant.EffectTTLPremium:
		return "ttl_premium"
	case accountant.EffectTTLRescue:
		return "ttl_rescue"
	case accountant.EffectMarkPremium:
		return "mark_premium"
	case accountant.EffectMarkPayback:
		return "mark_payback"
	case accountant.EffectBreak, accountant.EffectRescue, accountant.EffectSplit:
		return string(k)
	}
	return other
}

// effectCause gives the `cause` label. The library names a cause only for a model change. A break
// with no ChangeID that is not in the baseline was left behind by an ended change.
func effectCause(e accountant.CacheEffect) string {
	switch {
	case e.Cause != "":
		return string(e.Cause)
	case e.Kind == accountant.EffectBreak && e.ChangeID == "":
		return "ended_change"
	}
	return "none"
}
