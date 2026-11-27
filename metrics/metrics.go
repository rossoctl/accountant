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

// Tier is a price class of tokens (metrics spec R34, context-guru#886). It is [accountant.Tier].
type Tier = accountant.Tier

const (
	TierInput        = accountant.TierInput
	TierCacheRead    = accountant.TierCacheRead
	TierCacheWrite5m = accountant.TierCacheWrite5m
	TierCacheWrite1h = accountant.TierCacheWrite1h
	TierOutput       = accountant.TierOutput
)

// Pricer gives the host's rate for one token. The host owns the rate table. If the rate is a
// fallback, or a guess, the Pricer sets estimated. A rate of 0 with estimated false is a real free
// rate. An unknown model must return estimated true.
type Pricer interface {
	Price(model string, at time.Time, tier Tier) (usdPerToken float64, estimated bool)
}

// SizedPricer is an optional extension of [Pricer]. It is [accountant.SizedPricer]. Some providers bill a
// large request at a higher rate for ALL its tokens, so the rate depends on the prompt size. promptTokens
// is the prompt size of the request the token belongs to: fresh input plus cache read plus cache write,
// as the provider counts it. The sink never passes 0 or a negative size: when it does not know the size,
// it calls [Pricer.Price] instead.
//
// If the host's Pricer also implements SizedPricer, the sink prices each side at its own size. Saved
// tokens use the size the request would have had without the operations. Caused tokens use the billed
// size. A Pricer without PriceAt gets the same calls and figures as before.
type SizedPricer = accountant.SizedPricer

// pricerAt gives [accountant.Figure] its pricer: PriceAt when the host has it and the size is known,
// else Price.
type pricerAt struct {
	p Pricer
	s SizedPricer // nil when the host's Pricer has no PriceAt
}

func (a pricerAt) PriceAt(model string, at time.Time, t Tier, size int64) (float64, bool) {
	if a.s != nil && size > 0 {
		return a.s.PriceAt(model, at, t, size)
	}
	return a.p.Price(model, at, t)
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
	withheld                 metric.Int64Counter

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
	i64(&s.withheld, "llm.savings.withheld", "Requests whose savings the sink did not book, because the figure failed a gate, by reason.")
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
//
// Every dollar comes from [accountant.Figure]. The sink only maps its lines to counters and labels.
//
// A request that fails a gate of [accountant.Withhold] books no saving: no saved or caused tokens of an
// operation, no cache effect and no interaction. The calls of its spends are real cost, so they are
// booked. The request adds 1 to llm.savings.withheld with the reason.
func (s *Sink) Observe(o Observation) {
	ctx := context.Background()
	res := o.Result
	money := o.Meta.Billing.Mode == accountant.BillingAPI
	anyEstimated := !money // unknown, subscription's weights or an unset mode are all estimates
	// A positive TokensBefore selects the deprecated host bound (kept until the field is removed). With none, the
	// library's own measure decides.
	var withheld string
	if o.TokensBefore > 0 {
		withheld = accountant.Withhold(res, o.TokensBefore)
	} else {
		withheld = accountant.Withhold(res)
	}

	// A spend whose call the library refused to price must not book that call's usage.
	refused := map[string]bool{}
	for _, u := range res.Unpriced {
		if u.OpID != "" {
			refused[u.OpID] = true
		}
	}

	priced := 0
	for _, op := range res.Ops {
		if op.DryRun {
			continue
		}
		priced++
		attrs := func(t Tier) attribute.Set { return s.opAttrs(op.Kind, t, op.Operator) }
		if withheld == "" {
			s.bookTokens(ctx, s.saved, tiers(op.Saved), attrs)
			s.bookTokens(ctx, s.added, tiers(op.Caused), attrs)
		}
		if op.Kind == accountant.KindSpend && op.Call != nil && !refused[op.OpID] {
			s.bookTokens(ctx, s.added, callTiers(op.Call.Usage), attrs)
		}
	}

	if money {
		fig := accountant.Figure(accountant.FigureInput{
			Model: o.Meta.Model, At: o.Meta.At, Billed: o.Usage, Output: o.Output, Result: res, Unsized: s.sized == nil,
		}, pricerAt{p: s.pricer, s: s.sized})
		anyEstimated = anyEstimated || fig.Estimated
		if withheld != "" {
			fig = spendsOnly(fig)
		}
		s.bookFigure(ctx, fig)
	}

	for _, u := range res.Unpriced {
		s.unpriced.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", u.Reason)))
	}
	for _, c := range res.Checks {
		s.checkFailures.Add(ctx, 1, metric.WithAttributes(attribute.String("check", c.Check)))
	}
	if withheld != "" {
		s.withheld.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", withheld)))
	}
	if anyEstimated {
		s.estimated.Add(ctx, 1)
	}
	result := "priced"
	switch {
	case len(res.Unpriced) > 0:
		result = "unpriced"
	case withheld != "":
		result = "withheld"
	case priced == 0:
		result = "skipped"
	}
	s.requests.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

// spendsOnly is the part of fig a withheld request books: the call of each spend. The operations'
// tokens, their re-rate shares, the cache effects and the interaction come from a figure that failed a
// gate, so they are dropped.
func spendsOnly(fig accountant.USDFigure) accountant.USDFigure {
	var lines []accountant.USDLine
	for _, l := range fig.Lines {
		if l.Kind == accountant.KindSpend && !l.ReRate {
			lines = append(lines, l)
		}
	}
	return accountant.USDFigure{Lines: lines}
}

func (s *Sink) opAttrs(k accountant.Kind, t Tier, operator string) attribute.Set {
	return attribute.NewSet(
		attribute.String("kind", string(k)),
		attribute.String("tier", string(t)),
		attribute.String("component", s.component(operator)),
	)
}

// bookTokens adds the tokens of one side of an op. A tier with no tokens adds nothing.
func (s *Sink) bookTokens(ctx context.Context, c metric.Int64Counter, tt map[Tier]int, attrs func(Tier) attribute.Set) {
	for t, n := range tt {
		if n > 0 {
			c.Add(ctx, int64(n), metric.WithAttributeSet(attrs(t)))
		}
	}
}

// bookFigure maps the lines, the effects and the unowned parts of a figure to the dollar counters.
func (s *Sink) bookFigure(ctx context.Context, fig accountant.USDFigure) {
	for _, l := range fig.Lines {
		c := s.savedUSD
		if l.Direction == accountant.DirAdded {
			c = s.addedUSD
		}
		c.Add(ctx, l.USD, metric.WithAttributeSet(s.opAttrs(l.Kind, l.Tier, l.Operator)))
	}
	for _, e := range fig.Effects {
		if !e.Counted {
			continue // on the record, not part of the bill difference
		}
		for dir, usd := range map[string]float64{accountant.DirSaved: e.SavedUSD, accountant.DirAdded: e.CausedUSD} {
			if usd == 0 {
				continue
			}
			s.effectUSD.Add(ctx, usd, metric.WithAttributeSet(attribute.NewSet(
				attribute.String("effect", effectName(e.Effect.Kind)),
				attribute.String("cause", effectCause(e.Effect)),
				attribute.String("direction", dir),
			)))
		}
	}
	// A re-rate no operation owns, and the net of the tokens no operation owns on a rescued request.
	for _, v := range []float64{fig.UnownedReRateUSD, fig.InteractionUSD} {
		if v == 0 {
			continue
		}
		dir := accountant.DirSaved
		if v < 0 {
			dir, v = accountant.DirAdded, -v
		}
		s.interactionUSD.Add(ctx, v, metric.WithAttributes(attribute.String("direction", dir)))
	}
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
