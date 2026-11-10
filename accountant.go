// Package accountant is the context accountant: it tracks what a context optimizer saves. It prices
// the changes that the optimizer makes to an LLM request, in tokens for each billing tier.
//
// A host calls it in four steps for each request. [Trackers.BeginRequest] takes the body the client
// sent. [Request.Add] records each change an operator reported. [Request.EndRequest] takes the body
// that was forwarded and the cache facts. [Request.Compute] takes the billed [Usage] and returns a
// [Result]: the tokens each change saved or caused. The host turns tokens into money. A keep-alive
// ping has no request record: use [ComputeStandalone]. [PingOps] builds its operations.
//
// # Building Usage from a response
//
// [Usage] counts each token once, in one tier. [Usage.Input] is the uncached input only. Providers do
// not agree on this, so a host must convert before it calls Compute. Output and thinking tokens go in
// [CallUsage.Output], not in Usage.
//
// Anthropic: the usage fields are already apart. Copy them.
//
//	Input        = usage.input_tokens                  (leaves out cached tokens)
//	CacheRead    = usage.cache_read_input_tokens
//	CacheWrite   = usage.cache_creation_input_tokens   (all lifetimes)
//	CacheWrite1h = usage.cache_creation.ephemeral_1h_input_tokens
//
// Example: input_tokens 50, cache_read 8,000, cache_creation 1,200 of which 1,000 are 1h gives
// Usage{Input: 50, CacheRead: 8000, CacheWrite: 1200, CacheWrite1h: 1000}.
//
// OpenAI: prompt_tokens (Responses API: input_tokens) includes the cached tokens. Subtract them.
//
//	CacheRead  = usage.prompt_tokens_details.cached_tokens   (Responses API: input_tokens_details.cached_tokens)
//	CacheWrite = usage.cache_write_tokens              (GPT-5.6 and later; 0 before)
//	Input      = usage.prompt_tokens - CacheRead - CacheWrite   (CacheWrite is 0 before GPT-5.6)
//
// Example: prompt_tokens 9,000, cached_tokens 8,000, no writes gives
// Usage{Input: 1000, CacheRead: 8000}. If Input is not reduced, the 8,000 cached tokens count twice.
// No check reports this: the ratio and every saving come out too high, so the host must subtract.
// Limits: this rule assumes that prompt_tokens includes the write tokens, which docs/ does not yet
// state. Usage has no 12-hour tier. Put all writes in CacheWrite and leave CacheWrite1h at 0. The
// library books a write by the TTL of the Breakpoint, not by CacheWrite1h: only "1h" reaches the long
// tier. The 2.5x price of a 12-hour write has no tier, so that extra price is not tracked.
//
// Gemini (not yet recorded in docs/cached-prefix.md): promptTokenCount includes the cached tokens. Subtract them.
//
//	CacheRead = usageMetadata.cachedContentTokenCount
//	Input     = usageMetadata.promptTokenCount - CacheRead
//
// Example: promptTokenCount 9,000, cachedContentTokenCount 8,000 gives
// Usage{Input: 1000, CacheRead: 8000}. Gemini bills no cache write per request, so CacheWrite is 0.
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
	Provider    = engine.Provider
	Usage       = engine.Usage
	Billing     = engine.Billing
	ModelCall   = engine.ModelCall
	CallUsage   = engine.CallUsage
	Ping        = engine.Ping
	PingFacts   = engine.PingFacts
)

// Parts of an operation.
type (
	Position          = engine.Position
	Span              = engine.Span
	Size              = engine.Size
	MessageSplice     = engine.MessageSplice
	SettingChange     = engine.SettingChange
	Kind              = engine.Kind
	Section           = engine.Section
	BillingMode       = engine.BillingMode
	LifetimeKind      = engine.LifetimeKind
	CachePhase        = engine.CachePhase
	CacheEffectKind   = engine.CacheEffectKind
	CacheEffectCause  = engine.CacheEffectCause
	CompactionTrigger = engine.CompactionTrigger
)

// What the library returns.
type (
	PricedOp         = engine.PricedOp
	TierTokens       = engine.TierTokens
	CacheEffect      = engine.CacheEffect
	Unpriced         = engine.Unpriced
	CheckFailure     = engine.CheckFailure
	CompactionRecord = engine.CompactionRecord
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
	EffectMarkPayback = engine.EffectMarkPayback

	CauseModelChanged = engine.CauseModelChanged

	CompactionManual  = engine.CompactionManual
	CompactionAuto    = engine.CompactionAuto
	CompactionUnknown = engine.CompactionUnknown
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
