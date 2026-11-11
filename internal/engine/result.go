package engine

// Result is the output of Compute (§6.3).
type Result struct {
	Version int
	Ops     []PricedOp // one for each input operation, same order: the reported ones, then the active ones applied again
	// CacheEffects are the breaks, rescues and breakpoint changes the library found. The library
	// writes them and a host never reports one, so they have no input form (§4.2 rule 4).
	CacheEffects []CacheEffect
	Interaction  TierTokens // §6.2: on a rescued request; attributed to nobody
	Unpriced     []Unpriced // with a machine-stable reason
	Checks       []CheckFailure
	Boundary     *Position  // the B* used; nil when unknown
	Recomputed   bool       // another request replaced the ThreadState first (R53, §6.1)
	Phase        CachePhase // the cache phase it used (§5.2); for the host's own checks, never an input
	// InteractionCaused is the other side of Interaction, as Caused is for Saved. Interaction is the
	// tokens the client body would have billed cold (write). InteractionCaused is what the ops
	// already book for them (read). The host prices Interaction minus InteractionCaused.
	InteractionCaused TierTokens
	// Compaction is set when the host reported a client compaction on this request
	// (RequestMeta.Compaction). It is a record for the host, which closes its own epoch on it. It
	// changes no figure.
	Compaction *CompactionRecord
	// StorageTokens is the tokens this request wrote into a cache that the provider bills by storage
	// (Provider.StorageBilled). 0 when storage is not billed or nothing was priced. The host prices
	// the hours; the library does not know how long the cache is kept.
	StorageTokens int
}

// CompactionRecord is what the library recorded for a client compaction.
type CompactionRecord struct {
	Trigger CompactionTrigger
	// Ended holds the ChangeID of each change that ended on this request, in pipeline order. A change
	// that was already over is not listed. It can be empty: a compaction before any change.
	Ended []string
}

// Unpriced names an operation the library did not price and why.
type Unpriced struct {
	OpID   string
	Reason string // "unknown_kind", "negative_size", "operator_not_allowed", "penalty_unknown", ...
}

// The names of the checks in CheckFailure.Check (§6.5). A host that switches on a check uses these.
// The five that equal a Reason are the same string by design (§6.5: the check name equals the reason).
const (
	CheckSum          = "sum"
	CheckUniqueChange = "unique_change"
	CheckSpans        = "spans"
	CheckConservation = "conservation"
	CheckSharedWarm   = "shared_warm"
	CheckBadUsage     = ReasonBadUsage
	CheckRatioRange   = ReasonRatioRange
	CheckBreakpoints  = ReasonBreakpoints
	CheckMediaTokens  = ReasonMediaTokens
	CheckSizeTokens   = "size_tokens"
	CheckSizeRange    = ReasonSizeRange
)

// CheckFailure is a failed check (§6.5). A failure does not stop pricing, except size_range, which unprices the one operation (§6.5, check 13).
type CheckFailure struct {
	Check    string // one of the Check constants
	Operator string // "" when the check is not about one operator
	Detail   string // numbers only, no content
}

// Fatal reports whether this failure means the list of operations or their attribution is wrong:
// sum, unique_change and spans. The figures of such a request follow whichever operation comes
// first, or add up to bytes the bodies do not hold, so a host must not show them as measured.
// Every other check is informational. The library decides this in one place, so a new fatal check
// gates every host and no host keeps a list of its own.
func (c CheckFailure) Fatal() bool {
	switch c.Check {
	case CheckSum, CheckUniqueChange, CheckSpans:
		return true
	}
	return false
}

// Reliable reports whether no check of the Result is Fatal. An unreliable Result still has
// figures, but they come from a wrong list of operations.
func (r Result) Reliable() bool {
	for _, c := range r.Checks {
		if c.Fatal() {
			return false
		}
	}
	return true
}

// PricedOp is one operation in tokens for each tier.
type PricedOp struct {
	OpID      string
	Operator  string
	ChangeID  string
	DryRun    bool
	Saved     TierTokens // tokens the provider did not bill, by tier; never negative
	Caused    TierTokens // tokens the provider billed because of this operation; never negative
	Call      *ModelCall // spend only: copied from the operation, for the host to price
	Estimated bool
}

// TierTokens is a token count for each billing tier.
type TierTokens struct {
	Input        int `json:"input,omitempty"`
	CacheWrite5m int `json:"cacheWrite5m,omitempty"`
	CacheWrite1h int `json:"cacheWrite1h,omitempty"`
	CacheRead    int `json:"cacheRead,omitempty"`
}

// Add returns t plus o, tier by tier. Use it, not a copy of the four fields, so a new tier is
// counted by every host that adds tokens.
func (t TierTokens) Add(o TierTokens) TierTokens {
	return TierTokens{Input: t.Input + o.Input, CacheWrite5m: t.CacheWrite5m + o.CacheWrite5m,
		CacheWrite1h: t.CacheWrite1h + o.CacheWrite1h, CacheRead: t.CacheRead + o.CacheRead}
}

// Sub returns t minus o, tier by tier. The result can be negative.
func (t TierTokens) Sub(o TierTokens) TierTokens {
	return TierTokens{Input: t.Input - o.Input, CacheWrite5m: t.CacheWrite5m - o.CacheWrite5m,
		CacheWrite1h: t.CacheWrite1h - o.CacheWrite1h, CacheRead: t.CacheRead - o.CacheRead}
}

// IsZero reports whether every tier is 0.
func (t TierTokens) IsZero() bool { return t == TierTokens{} }

// CacheEffectKind says what a CacheEffect is. The host needs it to settle the effect: a break goes to
// the change that caused it, a rescue goes to the keeper by its window.
type CacheEffectKind string

const (
	EffectBreak       CacheEffectKind = "break"       // a prefix the provider read is written again: a cost
	EffectRescue      CacheEffectKind = "rescue"      // a keep-alive ping kept an entry alive: write becomes read
	EffectSplit       CacheEffectKind = "split"       // cachesplit: the stable half is read, not written
	EffectTTLPremium  CacheEffectKind = "ttlPremium"  // headttl, cost leg: on every request that writes the head at 1h
	EffectTTLRescue   CacheEffectKind = "ttlRescue"   // headttl, read leg: a request reads the 1h entry after the 5m TTL
	EffectMarkPremium CacheEffectKind = "markPremium" // a breakpoint the host added: the new tail is written, not billed as input
	EffectMarkPayback CacheEffectKind = "markPayback" // the next request reads what a host-added breakpoint wrote: input becomes read
)

// CacheEffect is a change in how the provider bills content that is already there (§4.4). It
// changes no content. It is part of the output only: the library finds it by comparing requests
// (§5), so no operator reports one and the input type has no form for it.
type CacheEffect struct {
	// ID is unique inside the Result: "ce:" + a number.
	ID   string          `json:"id"`
	Kind CacheEffectKind `json:"kind"`
	// ChangeID is the change that caused a break, the keep-alive window of a rescue, or the head
	// entry of a TTL leg. Empty on a break that nobody caused (R31).
	ChangeID string `json:"changeId,omitempty"`
	// From and To are the ends of the prefix range the effect covers. A nil From is the start of the
	// prompt; a nil To is the end of the body. For a break, From is the end of what the provider
	// still read and To is B* (§6.2).
	From *Position `json:"from,omitempty"`
	To   *Position `json:"to,omitempty"`
	// TTL is the lifetime of the entry the effect is about, "5m" or "1h". Set on the TTL kinds.
	TTL string `json:"ttl,omitempty"`
	// Cause says why a break nobody reported happened, when the library can tell. Empty when it
	// cannot: an entry lost early has no visible reason. It changes no figure.
	Cause     CacheEffectCause `json:"cause,omitempty"`
	Saved     TierTokens       `json:"saved"`
	Caused    TierTokens       `json:"caused"`
	Estimated bool             `json:"estimated,omitempty"`
	// InBaseline marks a break that the client body would have had too: a client edit, an expiry,
	// or an entry lost early (§5.2 rows 2, 4 and 5). It is on the record, but it is not part of the
	// bill difference (§6.3), so a host does not count it. A break that an ended change left behind
	// has no ChangeID and is not InBaseline: it counts.
	InBaseline bool `json:"inBaseline,omitempty"`
}

// Net is the effect as one signed flow of tokens: Saved minus Caused, tier by tier. A host prices
// it with its rates and gets the dollar value of the effect with its sign: a break (a cost) and a
// markPremium come out negative, a rescue, a split and a markPayback positive. The host does not
// need the sign rule for each kind.
func (e CacheEffect) Net() TierTokens { return e.Saved.Sub(e.Caused) }

// CacheEffectCause is the reason the library can give for a break that no operation reported.
type CacheEffectCause string

// CauseModelChanged: this request names a different model from the thread's previous one, and
// the provider keeps a separate cache for each model, so nothing was read. Nobody reported the
// change, so the break has no ChangeID (R50 says the library does not price the change itself).
const CauseModelChanged CacheEffectCause = "model_changed"
