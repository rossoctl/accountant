// Package accountant is the context accountant: it tracks what a context optimizer saves. It prices
// the changes that the optimizer makes to an LLM request, in tokens for each billing tier.
//
// A host calls it in four steps for each request. [Trackers.BeginRequest] takes the body the client
// sent. [Request.Add] records each change an operator reported. [Request.EndRequest] takes the body
// that was forwarded and the cache facts. [Request.Compute] takes the billed [Usage] and returns a
// [Result]: the tokens each change saved or caused. The host turns tokens into money. A keep-alive
// ping has no request record: use [ComputeStandalone]. [PingOps] builds its operations.
//
// The module imports only the standard library. Its design is in docs/design.md, and a complete
// program is in the package example.
//
// This file is the whole public API. The implementation is in internal/engine, with the state it
// keeps for each thread; none of that is part of the API.
package accountant

import "github.com/rossoctl/accountant/internal/engine"

// Entry points.
type (
	// Trackers holds the state of every thread. Make one with [NewTrackers], and share it.
	Trackers = engine.Trackers
	// Request is the record of one request, made by [Trackers.BeginRequest].
	Request = engine.Request
	// Result is what [Request.Compute] returns.
	Result = engine.Result
)

// NewTrackers returns an empty set: every thread starts with no history.
func NewTrackers() *Trackers { return engine.NewTrackers() }

// ComputeStandalone prices operations that have no request record, such as a keep-alive ping.
func ComputeStandalone(ops RequestOps) Result { return engine.ComputeStandalone(ops) }

// PingOps turns a keep-alive [Ping] into a [RequestOps] with one spend, for [ComputeStandalone], and
// returns the [PingFacts] the thread's next real request passes in [EntryFacts].
func PingOps(p Ping, b Billing) (RequestOps, PingFacts) { return engine.PingOps(p, b) }

// NewBody hashes the content of one request body, one entry for each position of each section.
func NewBody(tools, system, messages [][]byte) Body { return engine.NewBody(tools, system, messages) }

// What a host reports.
type (
	RequestMeta = engine.RequestMeta
	RequestOps  = engine.RequestOps
	Operation   = engine.Operation
	Thread      = engine.Thread
	Body        = engine.Body
	Breakpoint  = engine.Breakpoint
	EntryFacts  = engine.EntryFacts
	Usage       = engine.Usage
	Billing     = engine.Billing
	ModelCall   = engine.ModelCall
	CallUsage   = engine.CallUsage
	Ping        = engine.Ping
	PingFacts   = engine.PingFacts
)

// Parts of an operation.
type (
	Position         = engine.Position
	Span             = engine.Span
	Size             = engine.Size
	MessageSplice    = engine.MessageSplice
	SettingChange    = engine.SettingChange
	Kind             = engine.Kind
	Section          = engine.Section
	BillingMode      = engine.BillingMode
	LifetimeKind     = engine.LifetimeKind
	CachePhase       = engine.CachePhase
	CacheEffectKind  = engine.CacheEffectKind
	CacheEffectCause = engine.CacheEffectCause
)

// What the library returns.
type (
	PricedOp     = engine.PricedOp
	TierTokens   = engine.TierTokens
	CacheEffect  = engine.CacheEffect
	Unpriced     = engine.Unpriced
	CheckFailure = engine.CheckFailure
)

const (
	KindRewrite = engine.KindRewrite
	KindSpend   = engine.KindSpend
	KindSetting = engine.KindSetting

	SectionTools    = engine.SectionTools
	SectionSystem   = engine.SectionSystem
	SectionMessages = engine.SectionMessages

	PhaseHot     = engine.PhaseHot
	PhaseCold    = engine.PhaseCold
	PhaseUnknown = engine.PhaseUnknown

	LifetimeExact   = engine.LifetimeExact
	LifetimeMinimum = engine.LifetimeMinimum
	LifetimeUnknown = engine.LifetimeUnknown

	BillingAPI          = engine.BillingAPI
	BillingSubscription = engine.BillingSubscription
	BillingUnknown      = engine.BillingUnknown

	EffectBreak       = engine.EffectBreak
	EffectRescue      = engine.EffectRescue
	EffectSplit       = engine.EffectSplit
	EffectTTLPremium  = engine.EffectTTLPremium
	EffectTTLRescue   = engine.EffectTTLRescue
	EffectMarkPremium = engine.EffectMarkPremium

	CauseModelChanged = engine.CauseModelChanged
)

// Machine-stable reasons in [Result].Unpriced.
const (
	ReasonEndRequestMissing = engine.ReasonEndRequestMissing
	ReasonPingResultUnknown = engine.ReasonPingResultUnknown
	ReasonUnknownKind       = engine.ReasonUnknownKind
	ReasonNegativeSize      = engine.ReasonNegativeSize
	ReasonPenaltyUnknown    = engine.ReasonPenaltyUnknown
	ReasonNoUsage           = engine.ReasonNoUsage
	ReasonNotSpend          = engine.ReasonNotSpend
	ReasonBadUsage          = engine.ReasonBadUsage
	ReasonRatioRange        = engine.ReasonRatioRange
	ReasonMediaTokens       = engine.ReasonMediaTokens
	ReasonBreakpoints       = engine.ReasonBreakpoints
	ReasonSizeRange         = engine.ReasonSizeRange
)
