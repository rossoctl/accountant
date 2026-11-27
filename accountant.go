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

import (
	"net/http"
	"time"

	"github.com/rossoctl/accountant/internal/engine"
)

// Entry points.
type (
	// Trackers holds the state of every thread. Make one with [NewTrackers], and share it.
	Trackers = engine.Trackers
	// Request is the record of one request, made by [Trackers.BeginRequest].
	Request = engine.Request
	// Result is what [Request.Compute] returns.
	Result = engine.Result
	// Ratio is the request's own tokens-per-byte ratio: see [NewRatio] and [Result].Ratio. Result.Ratio is nil when nothing was priced, and also when the ratio is not in range.
	Ratio = engine.Ratio
)

// NewRatio builds the ratio of a body that the provider billed for billed prompt tokens (input plus cache
// read plus cache write). It is the ratio [Request.Compute] builds for the request, so a host never
// writes the conversion again. See docs/design.md §4.2 rule 7.
func NewRatio(billed int, forwarded Body) Ratio { return engine.NewRatio(billed, forwarded) }

// RatioFromText rebuilds a text [Ratio] from the counts [Ratio.Text] and [Ratio.Capped] returned, such as
// a host stored.
func RatioFromText(tokens, bytes int, capped bool) Ratio {
	return engine.RatioFromText(tokens, bytes, capped)
}

// NewTrackers returns an empty set: every thread starts with no history.
func NewTrackers() *Trackers { return engine.NewTrackers() }

// ComputeStandalone prices operations that have no request record, such as a keep-alive ping.
func ComputeStandalone(ops RequestOps) Result { return engine.ComputeStandalone(ops) }

// PingOps turns a keep-alive [Ping] into a [RequestOps] with one spend, for [ComputeStandalone], and
// returns the [PingFacts] the thread's next real request passes in [EntryFacts].
func PingOps(p Ping, b Billing) (RequestOps, PingFacts) { return engine.PingOps(p, b) }

// PingWindowID gives the id of the keep-alive window that starts at start on thread: the ChangeID of
// the ping's spend, [PingFacts].PingWindow, and the change of the rescue on the thread's next request.
// The thread "" becomes "main". Use it for a ping that [PingOps] does not see, such as a refused one,
// so the host does not copy the rule. It does not check start: a zero start gives
// "ka:<thread>:0001-01-01T00:00:00Z".
func PingWindowID(thread string, start time.Time) string { return engine.PingWindowID(thread, start) }

// BillingFrom reads the client's upstream credential header (x-api-key or Authorization, as the
// request arrived, before any plugin rewrites it) and returns the [Billing] it implies: an API key
// gives [BillingAPI]; an OAuth token from a subscription login gives [BillingSubscription]; anything
// else, or no credential, gives [BillingUnknown]. It reads only the credential's type and returns no
// part of the credential (R2). Every host calls this, so the hosts share one rule (design §7.1).
func BillingFrom(h http.Header) Billing { return engine.BillingFrom(h) }

// SharedWarmFrom returns the [EntryFacts].SharedWarm of a request that forwarded fwd: the last
// position that one of the siblings kept warm (R54). The host chooses the [Sibling] list.
func SharedWarmFrom(fwd Body, siblings []Sibling) *Position {
	return engine.SharedWarmFrom(fwd, siblings)
}

// NewBody hashes the content of one request body, one entry for each position of each section.
func NewBody(tools, system, messages [][]byte) Body { return engine.NewBody(tools, system, messages) }

// NewBodyCounted is [NewBody] with the host's token count for each thinking block (design D16). It
// looks up each signature, and each redacted_thinking data string, by its [OpaqueKey]. A block with
// no count keeps the fixed price.
func NewBodyCounted(tools, system, messages [][]byte, count OpaqueCount) Body {
	return engine.NewBodyCounted(tools, system, messages, count)
}

// OpaqueKey is the key under which a host stores the token count of one thinking signature, or of
// the data string of a redacted_thinking block.
func OpaqueKey(s string) uint64 { return engine.OpaqueKey(s) }

// SplitOpaqueTokens splits the thinking tokens of one response between the thinking blocks it made,
// in proportion to the length of their signatures. The parts add up to tokens.
func SplitOpaqueTokens(opaques []string, tokens int) []int {
	return engine.SplitOpaqueTokens(opaques, tokens)
}

// Savings in money. A host prices a [Result] with [Figure], and never prices a saving itself.
type (
	// Tier is a price class of tokens.
	Tier = engine.Tier
	// SizedPricer is the host's rate table: a rate for one token, at the prompt size of its request.
	SizedPricer = engine.SizedPricer
	// FigureInput is the billed usage and the [Result] that [Figure] prices.
	FigureInput = engine.FigureInput
	// USDFigure is the savings of one request in dollars.
	USDFigure = engine.USDFigure
	// USDLine is one priced amount of one operation in one tier.
	USDLine = engine.USDLine
	// USDEffect is one priced cache effect.
	USDEffect = engine.USDEffect
	// RemovalInput is a change that took tokens out of the head of a request.
	RemovalInput = engine.RemovalInput
	// RemovalFigure is the saving of such a change in dollars.
	RemovalFigure = engine.RemovalFigure
)

const (
	TierInput        = engine.TierInput
	TierCacheRead    = engine.TierCacheRead
	TierCacheWrite5m = engine.TierCacheWrite5m
	TierCacheWrite1h = engine.TierCacheWrite1h
	TierOutput       = engine.TierOutput

	DirSaved = engine.DirSaved
	DirAdded = engine.DirAdded
)

// Figure turns a [Result] into dollars: for each operation, the effects, the breaks and the re-rate of
// the tokens that stay when the request crosses a price line. It is deterministic and does no I/O. See
// [USDFigure] for the fields, and docs/design.md for the rules.
func Figure(in FigureInput, p SizedPricer) USDFigure { return engine.Figure(in, p) }

// RulesVersion is the version of the accountant rules (R46). Result.Version and RemovalFigure.Version carry it. It stays 1
// with the Ratio API: the Ratio path is opt-in, and a nil Ratio changes no figure.
const RulesVersion = engine.RulesVersion

// HeadTier is the tier the head of a request's prefix was billed at, from its usage.
func HeadTier(u Usage) Tier { return engine.HeadTier(u) }

// RemovalSaving prices a change that took tokens out of the head of a request, such as a tool
// declaration filter or a tool deferral. The host's tokenizer factor is applied once when Ratio is nil. With a
// Ratio the tokens are the provider's, the factor is 1 and Factor is ignored. See docs/design.md §6.6.
func RemovalSaving(in RemovalInput, p SizedPricer) RemovalFigure { return engine.RemovalSaving(in, p) }

// Keep-alive costs. A host prices a ping, a cache miss, a billed request and a late ping with these, and
// never with its own formula.
type (
	// PingInput is one keep-alive ping: the prefix it reads, the fresh input it carries and its output.
	PingInput = engine.PingInput
	// MissInput is one cache miss on a prefix.
	MissInput = engine.MissInput
	// RequestCostInput is the billed usage of one request.
	RequestCostInput = engine.RequestCostInput
	// LatePingInput is a ping that fires after its entry expired.
	LatePingInput = engine.LatePingInput
)

// RequestCost is the dollars of one billed request, at the rates of its prompt size. See [RequestCostInput].
func RequestCost(in RequestCostInput, p SizedPricer) (usd float64, estimated bool) {
	return engine.RequestCost(in, p)
}

// LatePingCost is the dollars of a ping that arrives after its entry expired: it writes the prefix. See [LatePingInput].
func LatePingCost(in LatePingInput, p SizedPricer) (usd float64, estimated bool) {
	return engine.LatePingCost(in, p)
}

// PingCost is the dollars of one keep-alive ping, at the rates of its own prompt size. 0 with estimated
// true means the rate is unknown. See [PingInput].
func PingCost(in PingInput, p SizedPricer) (usd float64, estimated bool) {
	return engine.PingCost(in, p)
}

// MissCost is the extra dollars of one cache miss over a hit, never negative. See [MissInput].
func MissCost(in MissInput, p SizedPricer) (usd float64, estimated bool) {
	return engine.MissCost(in, p)
}

// AvoidedMissesUSD is the dollars saved when n misses do not happen: n x missUSD, never negative.
func AvoidedMissesUSD(n int64, missUSD float64) float64 { return engine.AvoidedMissesUSD(n, missUSD) }

// The reasons [Withhold] returns.
const (
	WithheldCheckSum          = engine.WithheldCheckSum
	WithheldCheckUniqueChange = engine.WithheldCheckUniqueChange
	WithheldCheckSpans        = engine.WithheldCheckSpans
	WithheldCheckOther        = engine.WithheldCheckOther
	WithheldUnpriced          = engine.WithheldUnpriced
	WithheldSizeUnknown       = engine.WithheldSizeUnknown
	WithheldImplausibleSize   = engine.WithheldImplausibleSize

	// SizeNotGiven is the legacy tokensBefore of a host with no measure of the request size: [Withhold]
	// skips the size gate.
	//
	// Deprecated: call Withhold(res) and the library measures the size.
	SizeNotGiven = engine.SizeNotGiven
)

// Withhold returns the first gate a [Result] fails, or "" when a host may book its savings. With a
// reason, the host books no saving for the request, only the calls of its spends.
//
// Call it as Withhold(res). The size bound then uses the library's own measure of the client messages
// ([Result.MessageTokens]) and counts message tokens only, so a removed tool declaration does not
// trip it. The optional size is the legacy form: the size of the body the client sent, by the host's
// own measure, or [SizeNotGiven], which skips the size bound. The optional size is deprecated: a variadic
// cannot carry its own marker, so new code must not pass it. See docs/design.md §6.7.
func Withhold(res Result, tokensBefore ...int) string { return engine.Withhold(res, tokensBefore...) }

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
	Sibling     = engine.Sibling
	OpaqueCount = engine.OpaqueCount
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

// Names of the checks in [CheckFailure].Check.
const (
	CheckSum          = engine.CheckSum
	CheckUniqueChange = engine.CheckUniqueChange
	CheckSpans        = engine.CheckSpans
	CheckConservation = engine.CheckConservation
	CheckSharedWarm   = engine.CheckSharedWarm
	CheckBadUsage     = engine.CheckBadUsage
	CheckRatioRange   = engine.CheckRatioRange
	CheckBreakpoints  = engine.CheckBreakpoints
	CheckMediaUnseen  = engine.CheckMediaUnseen
	CheckMediaTokens  = engine.CheckMediaTokens
	CheckSizeTokens   = engine.CheckSizeTokens
	CheckSizeRange    = engine.CheckSizeRange
	CheckOpaqueTokens = engine.CheckOpaqueTokens
)

// Units of [CheckFailure].Unit.
const (
	UnitBytes  = engine.UnitBytes
	UnitTokens = engine.UnitTokens
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
	ReasonMediaUnseen       = engine.ReasonMediaUnseen
	ReasonMediaTokens       = engine.ReasonMediaTokens
	ReasonBreakpoints       = engine.ReasonBreakpoints
	ReasonSizeRange         = engine.ReasonSizeRange
)
