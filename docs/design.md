# Context operations: counterfactual accounting of cost and savings, design

**Status:** proposed.
**Date:** 2026-10-18, revised 2026-11-01.
**Specification:** [`spec.md`](spec.md) says what the system must do and why. It holds the problem, the
goal, the requirements (R) and what is out of scope. The test cases are in [`testing.md`](testing.md). Read the spec first. The words used here
(context operation, tier, counterfactual, B\*, and others) are defined in [its glossary](spec.md#words).
**This document:** how the system does it. It holds the decisions (D), the object model, the comparison
of requests, the library, billing plans, errors, and the open questions about the implementation.
**Worked examples:** [`examples.md`](examples.md).
**Wiring in hosts:** [`cortex-wiring.md`](cortex-wiring.md).

**Numbering.** The documents share one numbering, so a number is in exactly one document and the
numbers in a document are not consecutive. [The map is in `spec.md`](spec.md#numbering-map). In prose, an `R`
number links to its requirement in [`spec.md` §3](spec.md#3-requirements). Inside the code blocks of §4.1
and §6 the `R` numbers are written bare, to keep the comments short; they are the same requirements.

**Index**

- [2. Decisions](#2-decisions)
- [4. The object model](#4-the-object-model)
  - [4.1 Types](#41-types)
  - [4.2 Rules](#42-rules)
  - [4.3 Why positions are (section, index)](#43-why-positions-are-section-index)
  - [4.4 Cache effects](#44-cache-effects)
  - [4.5 Changes that end](#45-changes-that-end)
  - [4.6 Replays: who remembers past changes](#46-replays-who-remembers-past-changes)
- [5. Comparing requests](#5-comparing-requests)
  - [5.1 The counterfactual boundary B\*](#51-the-counterfactual-boundary-b)
  - [5.2 Classifying a break](#52-classifying-a-break)
  - [5.3 Tokens for each position](#53-tokens-for-each-position)
- [6. The library](#6-the-library)
  - [6.1 Call](#61-call)
  - [6.2 Tier rule](#62-tier-rule)
  - [6.3 Output](#63-output)
  - [6.4 Unknown boundary](#64-unknown-boundary)
  - [6.5 Checks](#65-checks)
- [7. Billing plans](#7-billing-plans)
  - [7.1 Billing plans](#71-billing-plans)
- [8. Errors](#8-errors)
- [11. Open questions](#11-open-questions)

## 2. Decisions

| # | Question | Decision |
|---|---|---|
| <a id="d1"></a>D1 | Does the library need to know that a change is applied for the first time? | **No.** The tier comes from the position and the cache state (§6.2). A report finds the first application as the first event with that `ChangeID`. A context operator reports a change once, and `Compute` applies it again on later requests (D14, §4.6). |
| <a id="d2"></a>D2 | Where is the savings logic? | **In one standalone library,** shared by context-guru and Cortex, and open to other AI frameworks. Hosts call it and keep no operator-specific pricing rules. |
| <a id="d3"></a>D3 | What does the library return? | **Tokens for each tier**, not dollars. Cortex prices them with its own rates (discounts, overrides, long-context tiers), so a saving and the request's spend use the same rates. |
| <a id="d4"></a>D4 | How is cg's own spend recorded? | **As context operations, in the same report as the savings.** A `spend` context operation carries the provider's billed usage of the call (`Call`). The host prices it with the savings, in one path. There is no second record and no `CostRecorder` (wiring document). `accountant.Compute` does not price the call; it returns `Call.Usage` for the host to price (D3). |
| <a id="d5"></a>D5 | Shape | **One typed context operation for each change unit**, in-process only. The recorded summary (`event.Saving`/`ChangeSaving`) has the aggregate fallback above a limit, not the context operation list itself ([R20](spec.md#r20); the recorded form is in the wiring document). |
| <a id="d6"></a>D6 | How is a saving across requests counted? | **Realized on each request, not projected.** Each request is priced from its own cache result. A context operator reports a change once, on its first request. The Tracker remembers the active changes of the thread, and `Compute` applies them again on each later request (D14, §4.6). **The risk:** a remembered change can be wrong, because the Tracker cannot see the content. The sum check (§6.5) can catch it, but a lost state does not heal by itself (§4.6, §11 question 13). |
| <a id="d7"></a>D7 | Where do the types live? | **A standalone module with no dependency on context-guru, on Cortex, or on any framework.** context-guru and Cortex are two users of it, and other frameworks can be too. The module is `github.com/rossoctl/accountant` (package `accountant`, "context optimization saving tracker"), in its own repository. |
| <a id="d8"></a>D8 | Which hosts compute dollars from context operations? | **Both: Cortex and the context-guru proxy.** The proxy stops using its own saving rules (`dash/event.go`) when it switches. The comparison of request fingerprints (§5) is inside `Compute`, so the proxy does not need Cortex to find B\*. |
| <a id="d9"></a>D9 | How is the implementation validated? | **Against an exhaustive list of test cases made with the current computation** ([§10](testing.md#10-test-cases)), before any host switches. |
| <a id="d10"></a>D10 | How is the cache state used? | **As an input to every price.** The tier of a content change depends on the state (§6.2). A change in how the provider bills content that is already there (a cache effect, §4.4) is priced as the difference between two states. |
| <a id="d11"></a>D11 | What happens to a change that a later change ends? | **`Compute` finds that it has ended and stops applying it.** The context operator reports nothing after the first request (D14), so it cannot say that a change ended. No link names the change that ended it. `Compute` infers the end from the bodies and the splice (§11, question 12). The measurement at each context operator's own input ([R29](spec.md#r29)) gives the right price and stops any double count, while the change is in the state. Each change keeps the total it had until then (§4.5). |
| <a id="d12"></a>D12 | Where does the thread ID come from? | **From the engine's `internal/thread`, in both hosts**, carried on the request. The host keeps one fingerprint per thread, keyed by `thread.Key`, and compares each request with its own thread's previous request (wiring document, W5.2). |
| <a id="d13"></a>D13 | How does the model serve a subscription user? | **The library does not change.** It already returns tokens for each tier (D3). The request carries a `Billing` fact. The host converts tokens to dollars for an API user, and to usage-limit units for a subscription user, with an API-equivalent dollar figure beside it, marked as such (§7.1). |
| <a id="d14"></a>D14 | Is `Compute` stateful? | **The library is stateful. `Compute` is deterministic.** A context operator reports a change once, on the first request. `Compute` applies it again on later requests until it ends, so a result depends on earlier requests. The state is a `ThreadState`, kept by the Tracker, one for each thread. `Compute` is a method of the request record. The record takes a copy of the `ThreadState` in `EndRequest`, because the thread is known only after the plugins ran (§6.1). As its last step `Compute` replaces the thread's `ThreadState` with the new one, so the host has nothing to commit and cannot forget to. A failure or a panic leaves the old `ThreadState` (see [§4.6, a request that does not reach `Compute`](#46-replays-who-remembers-past-changes)). Two requests of one thread at the same time (a retry, parallel calls of one agent) are safe: the replace is guarded by the `Version` of the `ThreadState` (optimistic locking, [R53](spec.md#r53), §6.1). `Preview` does the same calculation and stores nothing (it can differ from `Compute` only for a fork's first request, if the parent's state moved in between, [R26](spec.md#r26)), for pricing an old request again and for a look that must not move the thread forward. A second `Compute` of the same request prices against the state the first call stored (the result is usually phase `hot`), so a host must not call `Compute` again to retry after a transient error; it calls `Preview`. A test case is a sequence of requests. Rejected alternatives are listed below the table. What the state holds is open (§11, question 11). How a change ends is decided (§11, question 12). |

**D14, rejected alternatives.**

- A lock from `EndRequest` to `Compute`: requests of one thread would wait for each other.
- `Compute` returning the new state and a separate `Commit`: it is purely functional, but a host that forgets the commit gets a wrong figure with no error.
- `Compute` writing the state in the middle of the call: a failure would leave it half updated.

## 4. The object model

### 4.1 Types

**Public and internal.** The listing shows the whole model. In the code, package `accountant` (`accountant.go`)
re-exports only what a host needs to call it. `Tracker`, `ThreadState`, `ThreadKey`, `Fingerprint`,
`PosHash` and `CacheState` are in `internal/engine` and are not part of the API. A host saves and loads
the state of all threads with `Trackers.Snapshot` and `Trackers.Restore` (§6.1, Q13), not with `ThreadState`.

```go
package accountant // github.com/rossoctl/accountant

// RequestOps is the operations of one request, or of one call made outside a request.
type RequestOps struct {
    Version   int
    RequestID string
    Session   string
    Thread    Thread
    Model     string            // the model the request was sent for
    Params    map[string]string // other settings that change the cost: effort, thinking, max_output, ...
    At        time.Time
    // Billing prices every operation of the request, a `spend` included (R41, R42, §7.1).
    Billing Billing
    // Ops is in-process only: input to accountant.Compute for this request, never recorded (R20).
    Ops []Operation // in pipeline order
}

// Billing is a fact the host reads from the request's credential, not a price.
type Billing struct {
    Mode    BillingMode // api | subscription | unknown
}

// ModelCall is one upstream call an operator or the keeper made. The host's model client fills it
// from the call's own response, so the usage is what the provider billed.
type ModelCall struct {
    Host, Model string
    Params      map[string]string // the call's own settings: effort, thinking, ...
    Usage       CallUsage     // everything the call's response bills, as billed
    Detached    bool          // finished after the request it served was answered
}

// CallUsage is the whole bill of one call, as its response states it: the prompt side (Usage), the
// output tokens, and the dollar cost when the response states one (some gateways do). The cost is
// a fact the provider or gateway reported, not a rate of ours (R1, R49).
type CallUsage struct {
    Usage                // input, cache read, cache write (and its 1h part)
    Output  int          // output tokens, including the model's reasoning or thinking tokens
    CostUSD float64      // the cost the response stated; 0 when it stated none
}

// Thread identifies one conversation inside a session: the main thread, a subagent, or a fork.
// Each thread has its own provider cache entry.
type Thread struct {
    ID     string       // "main" for the primary thread. internal/thread gives the primary thread
                        // the ID "", and the host maps "" to "main"
    Parent string       // the thread this one was forked from; "" if none
}


type Operation struct {
    ID        string // unique inside the request only; across requests, use ChangeID (R6)
    Kind      Kind   // rewrite | spend | setting. There is no kind for a cache effect: only the library finds one (§4.4).
                     // setting: a request parameter that changes the cost or the cache and that the body does not show (§4.2)

    Operator  string // "context-guru.mask", "tool-prune"; the first part is set by the framework

    DryRun    bool // set by the framework: computed but not sent (R12)

    At     Position // first position the change touches, in this operator's output.
    Spans  []Span   // every touched range, when the change is not contiguous; nil otherwise
    SizeBefore Size // what the operator received over the touched ranges; the sum over Spans
    SizeAfter  Size // what it sent on over the same ranges

    // kind = rewrite | spend | setting
    ChangeID     string   // the same on every request that carries this change. On a spend: the change the call created, or the keep-alive window. 

    // kind = spend
    Call     *ModelCall // the call, with the provider's billed usage (wiring document)

    // kind = setting: one decision, any number of settings. The key is the setting name: model,
    // host, or a key of Params (effort, thinking, ...). One key cannot appear twice.
    Settings map[string]SettingChange

    // a change to the message list, for mapping positions
    MessageSplice *MessageSplice
}

type Position struct {
    Section Section // tools | system | messages
    Index   int     // block or message index in that section
}

// Span is a range of positions in one section: [From, To], both in this operator's output.
// SizeBefore and SizeAfter are the sizes over this range only. The operation's SizeBefore and SizeAfter are the
// sums over its Spans.
type Span struct {
    Section       Section
    From, To      int
    SizeBefore, SizeAfter Size
}

// SettingChange is one setting going from one value to another. A nil pointer means "not set":
// From nil is a setting that appears, To nil is a setting that is removed.
type SettingChange struct {
    From, To *string
    // At is the first position of the prefix that the provider renders this setting into: the first
    // position whose cache entry the change can invalidate (cached-prefix.md). A model change starts at
    // tools[0]; an effort change on a model that renders it ahead of the messages only starts at
    // messages[0]. The bodies do not differ where a setting differs, so the library cannot find
    // this position by comparing hashes (§5.2). Nil means the reporter does not know.
    At *Position
}

// Size is the bytes of a range, and optionally its tokens (§4.2 rule 7).
type Size struct {
    Bytes      int // required on rewrite; the sum check compares it with the Body sizes (§6.5)
    Tokens     int // the host's count; 0 = not measured, so the library converts Bytes and marks it Estimated
    MediaBytes int // the base64 part of Bytes; priced at a fixed 250 bytes a token, apart from the text ratio
}

type MessageSplice struct{ At, Removed, Inserted int } // messages only

// CacheState is the state of the thread's provider cache entry: its phase, the last position it
// covers, and its lifetime. It is what `Compute` reads, and what a cache effect moves from and to.
// It is internal: no input type carries one.
type CacheState struct {
    CachePhase CachePhase // hot | cold | unknown
    Extent     *Position  // the last position the entry covers; nil when nothing is covered
    TTL        string     // "5m" | "1h": the longest TTL of a breakpoint that wrote the entry; empty when cold or unknown
    // EntryTTL is EntryFacts.TTL of the request that wrote the entry. The next request judges the
    // entry with it, not with its own EntryFacts.TTL (§5.2). Zero when unknown: the next request's TTL is used.
    EntryTTL time.Duration
}

// EntryFacts are the facts that decide the cache phase and that only the host knows. The host passes
// them in `EndRequest`; the library reads them in its internal comparison (§5), and nowhere else.
// The library finds the two gaps itself (§5.2), from `ThreadState.PrevPrint.At`, `LastPing` and the
// time of the request, so a recompute after another request (§6.1) uses the current state.
type EntryFacts struct {
    TTL  time.Duration // lifetime of the entry THIS request writes: 5m or 1h, from the breakpoint that writes it.
                       // The next request judges the entry with it (CacheState.EntryTTL); this request is
                       // judged by what the previous request wrote, not by this field (§5.2)
    // Lifetime says whether TTL is a proven expiry (exact) or only a guaranteed floor
    // (minimum): Anthropic's TTL is exact; OpenAI GPT-5.6+ states only a 30-minute minimum
    // with no documented maximum (R45, §5.2). A gap past TTL proves expiry only when exact.
    Lifetime LifetimeKind
    // The last keep-alive ping of this thread since its previous request: when it started, the
    // extent it read, and the ChangeID of its keep-alive window. Zero values when no ping was sent.
    LastPing   time.Time
    PingRead   *Position
    PingWindow string
    // PingUsage is the last ping's billed usage. With PingRead nil, the library derives PingRead
    // from it (§5.2, the ping row): the extent its CacheRead + CacheWrite covers in the previous
    // forwarded body, which the ping replays, at the ping's own bytes-to-tokens ratio.
    PingUsage  *Usage
    // SharedWarm is the longest prefix of this request's forwarded body that another thread of the same
    // session read or wrote inside the TTL before this request (R54). Nil when none did.
    SharedWarm *Position
    // MinCacheable is the model's minimum cacheable length in tokens. 0 = not set: no minimum applies.
    MinCacheable int
}

type Kind string          // rewrite | spend | setting
type CachePhase string    // hot | cold | unknown
type LifetimeKind string  // exact | minimum | unknown — see apply.CacheLifetimeKind
type BillingMode string   // api | subscription | unknown
type Section string       // tools | system | messages
```

The enums are strings, not integers, so an unknown value from a newer context operator survives a decode
and can be reported as unpriced ([R19](spec.md#r19)).

### 4.2 Rules

1. **One context operation for each change unit.** A change unit is what the context operator decides, replays and
   reverses as one. A component that reduces 12 tool outputs with 12 decisions reports 12 `rewrite`
   context operations, each with its own `ChangeID`. tool-prune removes the declarations of one account
   list in one decision, so it reports one context operation for all of them. Its declarations are not
   next to each other, so it lists them in `Spans`. `At` is the first one. `coref` is the same: it
   plans many cuts across messages that are not next to each other, gates them as one batch, and
   reports one `rewrite` with one `ChangeID` and one `Span` for each cut. Reporting each cut alone
   would split one rewrite penalty across N changes, and no single cut's payback would then mean
   anything.
2. **A change is reported once; a later application is not a new report.** On later requests `Compute` applies
   the change again from the state, with the same `ChangeID` (§4.6). The context operator must not
   report it again: check 3 (§6.5) fails if a change is both reported and active. The one exception is
   the switch from observe to enforce (§4.6). A model call that a replay needs is a `spend` context
   operation, not part of the reduction.
3. **`SizeBefore` and `SizeAfter` are measured at the context operator's own input.** The direction of a `rewrite`
   is in the sizes, not in the kind. `SizeAfter < SizeBefore` is a saving, `SizeAfter > SizeBefore` is a cost (an
   injected tool declaration, a restore), and equal sizes change no token count. A restore is a
   `rewrite` with a `SizeAfter` larger than its `SizeBefore`. It names no earlier change.
4. **Only the library finds a cache effect**, inside `Compute` (§5). A context operator reports `rewrite`, `spend` and `setting`. The input type has no form for a break, a rescue or a change of breakpoint, so a host cannot report one by mistake. The output has a `CacheEffect` type for them (§6.3).
5. **A keep-alive ping is an ordinary `spend` context operation.** Its `Call` carries the ping's billed
   usage. Its `ChangeID` names the keep-alive window, and the rescue (a cache effect) on the
   request it rescued has the same `ChangeID`. So the cost and the value of a window add up
   (§4.4).
6. **`ChangeID` is derived from the context operator and the original content key only — never from the
   replacement bytes.** This matches the engine's own stable key (`frozenKey(session, component,
   contentKey(original))`, `components/offload/state.go`) rather than adding to it. Two
   context operators that remove the same content in different ways still get different IDs, because
   the context operator name is part of the key.
   `readlifecycle` and `skeleton` give different replacement bytes for the same decision on
   different turns, so keying on the replacement bytes would churn the ID.
   The content key alone is not unique in a session: the same tool output can appear twice, and
   the engine then replays one frozen decision at two positions. These are two changes, so the
   `ChangeID` also takes the ordinal of the occurrence (the first, the second, ... position in the
   client body with that content key). Check 3 (§6.5) then holds.
7. **A context operator reports bytes only. The library calculates the tokens.**
   Removed tokens are not in the forwarded body, so the library takes the bytes from the context
   operation's own `Size` and converts them. The conversion uses the request's own ratio: the
   bytes of the forwarded body divided by the billed input tokens of the request (input + cache read +
   cache write). The provider bills the forwarded body, so the ratio is taken on it. If the request has no usage, the library uses a fixed fallback ratio. The library
   marks every priced result that uses this conversion as `Estimated`. No operator names a tokenizer,
   so two operators give the same tokens for the same bytes.
   Two exceptions keep the ratio true (accountant#11):
   - **Base64 media stays out of the ratio.** `NewBody` parses each position and counts the `data`
     of an image or document block whose source has `type: base64`, and the payload of a `data:...;base64,` URL
     (OpenAI form), in `PosHash.Media`. It also counts the `data` string of a Gemini `inlineData` or `inline_data` object and of an OpenAI `input_audio` object, and the `source.bytes` string of a Bedrock Converse `image` or `document` block. The `data:` prefix and the `;base64,` marker match without regard to case (RFC 2045). Text that only quotes "base64" is not media. `Media` above `Bytes` is capped. The ratio uses the text bytes only, over the billed tokens
     less the media tokens. Media is priced at a fixed 1 token for 250 bytes. This is not a provider rule: Anthropic bills about width x height / 750 tokens and shrinks a large image, OpenAI bills by tile, and a PDF is billed by page. So the price is right only for a medium image, and a large image shifts the text ratio for the whole request (a 2 MB PNG that Anthropic bills at 1,600 tokens is priced at 8,000, and the text ratio then absorbs the 6,400 tokens of difference: a 10,000-token saving shows as 9,358). The library cannot know the real count from the bytes. A host that needs the exact figure sets `Size.Tokens` on the range, and the library uses it as it is. If the bill is below the media price, the library cuts the media to the bill less the least the text can cost (0.05 tokens a byte, the lower limit of check 9), and prices the text at that lowest ratio, so a text saving is the lowest the bill allows. It fails the check `media_tokens`. An operation whose range covers media goes to `Unpriced` with the reason `media_tokens`, unless the host gave `Size.Tokens` on both of its sizes. Other operations are still priced and are `Estimated`. A host that builds a
     `Body` itself sets `PosHash.Media`. A host marks media inside a `Size` with `Size.MediaBytes`. If a size does not mark it, the library takes the
     media of the range from the client body for a before-size and from the forwarded body, over the mapped range, for an after-size (the size's bytes beyond the range's text), so an unmarked cut of an image
     message is not priced at the text ratio.
   - **The host may pass tokens.** If a host knows the tokens of a range (a tokenizer, or a
     count_tokens call), it sets `Size.Tokens`. The library uses them as they are, and does not mark
     that size `Estimated`. `Tokens` 0 means not measured, and the bytes are converted.
8. **The engine reports a change that `apply` writes, under the context operator that decided it.**
   `toolschema`, `toolfilter` and `cachesplit` are marker components: their `Reformat()` does
   nothing, and `apply` performs the rewrite (`apply/envelope.go`, `apply/prefixsplit.go`). `apply`
   holds the only exact numbers (`Trace.FilteredDeclTokens`, `Trace.SplitStableTokens`). So `apply`
   builds the context operation and sets `Operator` to the marker component's name. The mixed-TTL head
   (`apply/headttl.go`) has no component; its context operations use `Operator = "context-guru.headttl"`.

**A `setting` context operation.** It holds all the settings one decision changed, as a map from the setting
name to its `from` and `to` values:

```json
{"kind": "setting", "operator": "inference-router",
 "settings": {"model":  {"from": "large-model", "to": "small-model", "at": {"section": "tools", "index": 0}},
              "effort": {"from": "high",        "to": "low",         "at": {"section": "messages", "index": 0}},
              "thinking": {"to": "8000",                             "at": {"section": "messages", "index": 0}}}}
```

`thinking` has no `from`: it was not set before, and it appears now.

**What a setting change is.** A setting is a request parameter that the provider uses to build the
prompt or to choose the model, and that the body the host sends does not show. Two requests with the
same body can have different settings, so a comparison of body hashes (§5) cannot see the change. A
change of content is not a setting: a different tool definition is a body change, and `Compute` finds
it by hash. The point of the `setting` kind is that it records a change the hashes cannot find, and
`At` says where in the prefix the provider would stop matching.

The settings, and where each provider renders them, differ by provider. [`cached-prefix.md`](cached-prefix.md)
holds the full lists with their sources (read 2026-10-25). This is what the model takes from them:

| Provider | Setting | First level that stops matching | `At` |
|---|---|---|---|
| Anthropic | Web search toggle, citations toggle, `speed` | `system`, and `messages` after it | `system[0]` |
| Anthropic | `tool_choice` | `messages` only | `messages[0]` |
| Anthropic | Thinking parameters (mode, `budget_tokens`) | `messages`; `tools` and `system` too on models that render the configuration ahead of them | `messages[0]`, or `tools[0]` on those models |
| Anthropic | `output_config.effort` | `messages`; the model default is the same as omitting it, so nothing breaks. `tools` and `system` too on some models | `messages[0]`, or `tools[0]` on those models |
| Anthropic | `model` | Not in the provider's table. The cache is keyed to the exact prompt prefix and the model is part of the request, so the design treats it as a break (an inference) | `tools[0]` |
| OpenAI | `model` | The guide lists it: a different model can use different weights and caching behaviour. It names no level, so the design starts the break at the start of the prompt | `tools[0]` |
| OpenAI | `reasoning.effort`, `text.verbosity`, `parallel_tool_calls`, `text.format` | The guide says the rendered prefix may change, and gives no level | nil |

Rules that follow from the table:

1. **A level that stops matching breaks every later level.** On Anthropic the cache has three levels in
   the order `tools`, `system`, `messages`. `At` is the first position of the first level that breaks.
2. **`At` is where a break would start, not a statement that one happened.** A provider does not always
   discard an entry when a setting changes. OpenAI says so, and for `reasoning.effort` on supported
   models a configuration update keeps the earlier prefix. The library adds a break only when
   `Usage.CacheRead` is below the read the forwarded body predicts (§5.2, row 3).
3. **Where the behaviour depends on the model, the reporter chooses.** A thinking or effort change
   starts at `tools[0]` on one model and at `messages[0]` on another. The reporter knows the model.
   When it does not know the behaviour, it leaves `At` nil.
4. **A nil `At` is safe.** The library then takes the read that the usage shows as the end of the valid
   prefix, and adds `penalty_unknown` to `Unpriced` (§6.4). This reports too little, never too much. OpenAI
   settings are nil for this reason: the guide names no level.
5. **A change that the client makes is not a `setting` operation.** The client's own change of a
   setting has no operator and no `ChangeID`. With equal bodies it reaches row 4 of §5.2: the entry
   was lost early, and the break has no `ChangeID`.
6. **Content that a provider also lists is not a setting.** `tools` on OpenAI, tool definitions and
   images on Anthropic, and OpenAI `context_management` (compaction) change the body. They are
   `rewrite` operations or client edits.

The table covers what `cached-prefix.md` has read. It does not cover `host`, `max_output`, other
providers, or settings added later. A setting that is not in the table has an unknown effect on the
cache, so its `At` is nil (rule 4).

### 4.3 Why positions are (section, index)

The provider hashes `tools`, then `system`, then `messages`. A comparison of two positions in that
order shows whether a change is before a cache boundary. For a change with `Spans`, each span is
compared on its own and priced with its own `SizeBefore − SizeAfter`. So a change with spans on both sides
of B\* gets the right tier for each part. A context operator reports positions in its own output. If a
later context operator adds or removes messages, it reports a `MessageSplice`, and the library maps earlier
positions into the forwarded body.

**Two coordinate spaces.** The library uses two spaces, and every rule names the one it uses:

- **Client space**, the client body. B\*, `clientFirstDiff`, the tier of saved tokens (§6.2), and
  the counterfactual bill are in client space, because the counterfactual is the body no context
  operator touched.
- **Forwarded space**, the forwarded body. `fwdFirstDiff`, the read extent of `Usage.CacheRead`,
  `L`, the forwarded breakpoints and the break penalty are in forwarded space, because that is
  what the provider cached.

To map an operation's position into client space, the library undoes the `MessageSplice` of every
operation before it in pipeline order. To map it into forwarded space, it applies the splices of
the operations after it. A position inside a range that a later splice removed has no forwarded
position: the change has ended there (§4.5).

**Active operations on a later request.** An active operation keeps the position it had in client
space on its first request. On a later request the client body can grow only at the end, or change
by a client edit, so its client-space position is still valid until the client edits before it.
Then the library maps it into forwarded space through this request's splices, as for a reported
operation. A client edit before an active change, or a client compaction, is a case for the end
rule (§11, question 12): it ends the change.

### 4.4 Cache effects

A cache effect does not change the content. It changes how the provider bills content that is already
there. **It is not an operation.** No context operator reports one: the library finds it by comparing
requests (§5), so it exists only in the output, as a `CacheEffect` in `Result.CacheEffects` (§6.3).
The input type has no kind for it. An earlier version had a `cacheChange` operation that only the
library was allowed to write. That rule was a convention a host could break, so the type no longer
allows it.

A `CacheEffect` has a kind, a prefix range, and a `ChangeID`. The range is `From` to `To`. A nil `From`
is the start of the prompt, and a nil `To` is the end of the body. One exception: a `break` with no B\*
(no client breakpoint at or before the first difference, §5.1) has an empty range. Its `To` is nil, it moves
no tokens, and the `Saved` and `Caused` tiers are zero. Six kinds cover it:

- **`break`: a prefix is broken.** The provider no longer reads content it read before. `From` is the
  end of what it still reads and `To` is B\* (§6.2). The `ChangeID` is the change that caused it (§5.2),
  or empty when nobody did. It is a cost.
- **`rescue`: a prefix is warmed.** The provider read an entry that a keep-alive ping kept alive, so it
  did not expire (`cold` to `hot`). The `ChangeID` is the keep-alive window.
- **`split`: a prefix is warmed by a split.** A split of the system block means the stable half is read,
  not written, when the volatile tail changed. If the client's system section did not change since the
  previous request, the client body would have read the same prefix, and the split saved nothing: no effect
  (accountant#20). The `ChangeID` is the split's `rewrite`.
- **`ttlPremium` and `ttlRescue`: a prefix is protected.** A breakpoint changes for how long content is
  cached. Example: the head of the request is held for 1h, not 5m. It has **two legs:** a cost on
  **every request that writes** the head at 1h (`ttlPremium`), and a saving on each request that reads
  the 1h entry after a gap longer than 5m (`ttlRescue`). `TTL` says which entry the effect is about.
- **`markPremium`: a breakpoint the host added is paid for.** The forwarded body has a breakpoint after the
  client's last one. The provider writes the tokens between the two at the write tier (1.25x for 5m, 2.0x for
  1h). The client body bills the same tokens as plain input (1.0x). The effect moves `n` tokens from
  `Saved.Input` to `Caused.CacheWrite5m` or `Caused.CacheWrite1h`, so it nets zero tokens and costs
  `0.25 n` input-token units at 5m (`1.0 n` at 1h). It is a cost only. See "A breakpoint the host adds" below.

**When the library adds a rescue.** All three conditions must be true. They are the
conditions of the current `keepaliveSavedUSD`:

1. The keeper sent at least one ping for this thread during the gap before this request
   (`EntryFacts.LastPing` is set).
2. The **request gap** (§5.2), from the previous real request of the thread, is longer than the
   entry's TTL, so without a ping the entry would have expired. The TTL is the lifetime of the
   breakpoint that wrote the entry, so a 1h entry is not rescued after 9 minutes. And the lifetime
   is `exact` or `minimum`. A `minimum` is taken as the lifetime when a ping is what kept the entry:
   30 minutes is 30 minutes, so a rescue is added after a gap past it. This matches the current
   `keepaliveCreditFloorMs`, which credits OpenAI GPT-5.6+ after its 30-minute minimum. With no ping,
   a gap past a `minimum` does not prove an expiry ([R45](spec.md#r45)), so it gives no rescue, and an `unknown` lifetime never gives a rescue.
   The current code uses the 5m TTL for a 1h entry, which is a `differs` entry ([§10.2](testing.md#102-one-case)).
3. The tokens are the smaller of what the last ping read and what this request read, less the prefix
   named by `EntryFacts.SharedWarm` ([R54](spec.md#r54)). A sibling thread that touched that prefix inside the
   TTL kept it warm without the ping. If nothing is left, the library adds no rescue, and the request stays `hot`.

What the request wrote does not enter the rule. An earlier version also required that the provider read
more than it wrote. A long user turn or a large tool result adds to the write and does not change what the
ping kept alive, so that condition removed a rescue the ping had earned (accountant#9).

**The ping and the rescue share one `ChangeID`,** the ID of the keep-alive window. The host passes
it in `EntryFacts.PingWindow`, built by the same rule as the ping's `spend` (wiring document, W4.6). A window whose
pings rescued nothing has only `spend` context operations, so its total is negative. That is the cost of a
ping that was not needed, and it is visible for each thread.

**A longer breakpoint TTL is a bet, and both legs are recorded.** The 1h write premium (2.0× input against
1.25× for 5m) is paid on every request that creates the head entry at 1h, whether or not a later
request ever reads it. The benefit appears only on requests that arrive more than 5 and less than
60 minutes later. Measured over 19,805 production requests, a blanket 1h head cost $773.00 extra on
14,499 creating requests to recover $754.66 on 290 — net −$18.34; size-gating turned it positive.
If the model priced only the reading request, every write that never paid off would be missing, and
the figure would overstate the bet. So, like a keep-alive window:

- **On each 1h-writing request,** the library adds a `ttlPremium` effect for the head's breakpoint TTL
  (`TTL` `1h`, in place of `5m`) and only a cost: `Caused.CacheWrite1h` for the head's tokens, less the 5m write it replaced. Its
  `ChangeID` names the head entry (`"headttl:" + Thread + ":" + head content key`).
- **On each request that reads that entry after the 5m TTL,** the library adds a `ttlRescue` effect with
  the same `ChangeID` and the saving (write → read, §6.2).

A head entry that is written at 1h and never read again has only the cost leg, so its total is
negative — the same visibility a keep-alive window that rescued nothing has.

**A breakpoint the host adds (`markPremium`, accountant#49).** Example: the client marks `messages[0]`, the
host also marks the last message. The client body bills what follows its last breakpoint as input. The
forwarded body writes it. The rule:

- `n` is the number of tokens the provider **wrote** between L_c and L_f, where L_c is the client's last
  breakpoint (mapped into the forwarded body) and L_f is the forwarded body's last breakpoint. "Wrote" means
  after the read (`Usage.CacheRead`, and B\*) and not past what the request cached. `n` is the smaller of the
  range's tokens in the forwarded body and in the client body. Content an operation added or removed in the
  range is priced by that operation, so the smaller figure leaves it out. If one request adds and removes in the
  range, `n` can be too large; the error can only lower a saving (R45).
- **The range is `(From, To]`.** `From` is the later of B\*, the end of the read, and L_c as forwarded. `To` is
  the earlier of L_f and what the request cached. The effect sets both, as `break` does. A nil `From` would mean
  the start of the prompt, and the effect prices none of the head before L_c.
- **Each part is written at its own TTL (§6.2).** The range is cut at every forwarded breakpoint. A part is
  written at the TTL of the first breakpoint at or after it. Example: client mark at `tools[0]`, host marks at
  `system[0]` (1h) and the last message (5m): the system tokens cost 1.0 each, the message tokens 0.25 each. The
  order 5m then 1h gives 0.25 and 1.0. The effect's `TTL` field is `1h` when any part is 1h. The range's tokens are cut
  to the smaller of the forwarded range, the client range and `Usage.CacheWrite`. The cut has two sources: the
  client range is smaller when an operation added content there (the operation prices that content), and the
  usage is smaller than the range. The cut takes the 5m parts first, so the dearer part stays booked.
- **Two breakpoints at one position with different TTLs:** the first in list order is booked, as `writeBP` does.
  The input is rare. A host that sends both gives the library no way to tell which the provider used.
- **Rounding:** the parts are converted one by one, so their sum can differ from the whole range by one token
  for each part. The conservation check allows that tolerance.
- **A 1h breakpoint counts as 1h only up to `Usage.CacheWrite1h`.** A bill with fewer 1h writes than the 1h
  breakpoints name shows the provider wrote at 5m, so the rest is booked at 5m. This books the lower premium.
  It is the bill's figure, not a guess: the provider charged 1.25x for those tokens.
- **No double count with `ttlPremium`.** `ttlPremium` moves a 5m write to 1h for the head held by a `headttl`
  change. `markPremium` moves input to a write. A token in the range is written at 1h from input, not from a 5m
  write, so `ttlPremium` leaves the 1h tokens that `markPremium` books out of its `n`
  (`Usage.CacheWrite1h` less `markPremium`'s 1h tokens). Arithmetic: head 1,000 (`headttl`), client mark at
  `messages[0]` (1,000, 5m), host 1h mark at `messages[2]`, 2,000 tail tokens; `CacheWrite1h` = 3,000.
  `ttlPremium` books 1,000 x 0.75 = 750. `markPremium` books 2,000 x 1.0 = 2,000. The bill difference is
  4,500 -> 7,250 units = 2,750 = 750 + 2,000. When only one effect applies, each is as before.
- **`ttlPremium` is bounded to the head.** Its `n` is also capped by the tokens of the head (the forwarded
  prefix up to the `headttl` operation's span, less what the provider read). An operation that adds content
  inside the marked range prices it itself at the write tier, and the client body has none of it, so it is no
  5m write moved to 1h. Arithmetic: the case above with an operation that adds 500 tokens at `messages[1]`:
  `CacheWrite1h` = 3,500. `markPremium` books 2,000 x 1.0 = 2,000. The operation books 500 x 2.0 = 1,000.
  The head books 1,000 x 0.75 = 750. The sum is 3,750 = the bill difference (4,500 -> 8,250). Without the cap `ttlPremium` would book
  1,500 and overstate by 375. If the client's last breakpoint is before the head, `markPremium` books the head
  (input to 1h) and `ttlPremium` is zero.
- Arithmetic: 3,016 tokens after the client's mark, 5m. Client body: 3,016 x 1.00 = 3,016. Forwarded body:
  3,016 x 1.25 = 3,770. The mark costs 754 input-token units. At 1h the write is 2.0x: it costs 3,016.
- **A cold request pays it too.** The client body writes up to its own last breakpoint and bills the rest as
  input, so the tail is still 1.0x there. The premium does not depend on the phase.
- **No premium** when: the provider wrote none of the range (the host's mark is inside the previous read, or
  the entry already held it); the forwarded body has no breakpoint after the client's last; the client has no
  breakpoint (every position is one, so the host adds none); or the usage shows no cache tokens at all.
- **The ChangeID** is the `ChangeID` of the applied operation whose forwarded span covers the host's breakpoint
  (the host's `rewrite` for the mark), or empty when none does. Check 5 allows both.
- **Relation to the next request.** The entry this request wrote up to L_f is read by the next request. That
  benefit is real, and the `rescue`, `break` and operation pricing of the next request already counts it,
  because the client body has no entry there: it is not counted a second time here. `markPremium` is the cost
  leg only, as `ttlPremium` is. A `ChangeID` that keeps only this leg shows a negative total until a later
  request reads the entry.
- **A removed client breakpoint is not priced.** The inverse (the client body writes the tokens, the forwarded
  body bills them as input) would be a saving for the host, not a cost. Leaving it out can only lower a saving,
  which is the safe direction (R45). It is a separate effect and out of scope here.

### 4.5 Changes that end

A change can remove content that earlier changes had already reduced. Example: on turn 12 a
summary replaces messages 0 to 8, and messages 3 and 5 were reduced by `mask` and `coref`.

**A change ends when `Compute` stops applying it.** The context operator reports a change once (D14), so
after that it cannot report that the change ended, and there is no field that names the change
that ended it. `Compute` has to find the end by itself, and drop the change from the state of the
thread. **The rule that finds the end is decided** (§11, question 12): `Compute` infers it from the
bodies and the splice. What follows describes what must hold once the end is found.

1. **The price.** Each request is priced from its own cache result (D6). A change that has ended saves
   nothing on later requests. Its total until then does not change, so a change that did not pay back
   before it ended shows a loss.
2. **No double count.** Each context operator's change is measured at its own input ([R29](spec.md#r29)), and the sum
   check tests the total (§6.5, check 1). So the tokens are counted once, in either pipeline order:

   | Pipeline order | `mask` | `coref` | summary | Total |
   |---|---|---|---|---|
   | Summarizer first (cg's `medium`, `high`, `xhigh` presets) | ended | ended | 40,000 → 1,500 = 38,500 | **38,500** |
   | Summarizer after the reducers | 3,940 | 19,950 | 16,110 → 1,500 = 14,610 | **38,500** |

   The order changes **who gets the credit**, not the total. Credit follows pipeline order ([R25](spec.md#r25)).
   In cg's presets the summarizer runs first, so it gets all of it: the reducers find their content
   gone, so `Compute` must not apply `mask` and `coref` again.
3. **A restore.** If the summary is reversed (a restore, or the summary is dropped), the earlier
   changes are back in the body. The context operator reported them once, and does not report them
   again, so `Compute` has to apply them again from the state, or they stay missing. This is the case
   that tests the end rule: a change that `Compute` dropped for good cannot come back (§11, question 13).

**What happens if `Compute` gets it wrong.** If it applies a change that has ended, the sum check fails
on that request, because the context operations remove more than the client body held. If it drops a
change that is still there, the sum check fails because the context operations remove less. Both are
reported in `Result.Checks`, and pricing goes on (§4.6).

**Why a change stopped** is not on the context operation. A report can find it: the change last appeared at
position P on request N, and request N+1 has a change whose span covers P (`At` and `MessageSplice`).
That is a question for a report, not for a price, so the report answers it.

### 4.6 Replays: who remembers past changes

**A context operator reports a change once, on the first request of the change. The Tracker remembers
it, for each thread. `Compute` applies it again on every later request of the thread, until it ends
(D14).** The context operators report less. They do not repeat a change on each request.

**What the old rule was, and why it is replaced.** The old rule was the opposite: each request listed
every change in its body, fresh or replayed, and the context operator was the only one that
remembered. The reason was that the context operator sees the content. It re-applies each change on
every request anyway, because the client sends its whole transcript on every turn, so it already knows
every change in the body. The cost of that rule was size: a replayed context operation repeated about
200 bytes on every request, and every context operator had to carry the replay in its report. The new
rule removes that cost. It moves the memory to a place that cannot see the content.

**The risk, stated plainly.** The old design rejected a register of past changes in the host,
for this reason: it must move positions after each `MessageSplice`, find which changes a
summary ended, and drop changes when the agent compacts its own transcript. It cannot see the content,
so it cannot tell whether a change is still in the body, and a wrong guess is silent. That reason still
holds for the Tracker. It is the risk of the new rule. What the new design does about it:

1. **A wrong guess is no longer silent in sizes.** `Compute` has the size of every position of the
   client body and of the forwarded body (taken in `BeginRequest` and `EndRequest`, §6.1). The sum check (§6.5, check 1)
   compares them with the sum of the context operations, those reported now and those applied again. If
   `Compute` applies a change that has ended, or misses one that is still there, the sums differ and
   the check fails on that request. The conservation check (check 6) fails too.
2. **What the checks do not catch.** A check works on sizes for each section. Two errors of the same
   size in one section cancel out. The checks also do not say which change is wrong. And a failed check
   does not stop pricing: the figure is still produced, with the failure in `Result.Checks`. (Exception: an operation with a size above the bounds of §6.5 check 13 is not priced.)
3. **The end rule is an inference.** `Compute` must find by itself that a change has ended, because the
   context operator reports nothing after the first request (§11, question 12). A change that ends
   with bytes identical to the changed form is not found. For that case the guard in point 1 is the
   only protection, and it detects an error after the fact. It does not prevent one.

**A lost state does not heal by itself.** This is new, and it is the main cost of the rule. If the
Tracker loses a thread's state (a restart, an eviction, a thread that is created again), the context
operator does not report the change again. The change stays missing for as long as it lasts. Every later
request of the thread fails the sum check, and its saving is too low, until the change ends. Under the
old rule, one missing request was only one missing figure, because the next request listed the change
again. How the state recovers is open (§11, question 13).

**Re-pricing an old request needs its state.** The old design could price an old request again from
that request alone. Now `Compute` also needs the state that the thread had at that time. A replay of
a session must start from the first request of the thread, or from a stored state. The session archive
has gaps (`droppedEvents`). One lost request there makes every later request in a replay wrong, so the
archive alone cannot rebuild the state.

**A request that does not reach `Compute`.** A request can fail before the response (an upstream error, a
client that hangs up), and then `Compute` is never called. The change it reported would be lost, and
the context operator does not report it again. So `EndRequest` stores the reported `rewrite` and
`setting` operations in the `ThreadState` as active, with a `Version` bump, and `Compute` does not
store them again. A reported operation replaces the active one with its `ChangeID`. So a retry that
reports the same change writes nothing and leaves `Version` alone, and a request that reports no change
writes nothing. The request's own copy of the state is taken after the store, so `Recomputed` (R53) is
true only when another request replaced the state after that. **Parallel requests:** a change is in the
state from `EndRequest`, so two requests of one thread in flight with different `ChangeID`s see each
other. When A calls `Compute` after B called `EndRequest`, A's result has `Recomputed` set and prices
B's change as active next to its own. If both touch the same content, A's sum check fails. A retry with
the same `ChangeID` does not do this (R53). The fingerprint and the cache state still
change only in `Compute`, because they need the usage. A request with no usage therefore advances the
active list and nothing else.

The fingerprint can be older than the stored change. So the end rule (§4.5) does not count content that
the client only appended after the end of the fingerprint's body as an edit.

**What stays in the state after a request.** The Tracker keeps the context operations that are still
applied on the next request, and drops the others:

| Context operation | Kept in `ThreadState.Active` | Why |
|---|---|---|
| `rewrite` (tool-prune, mask, a summary) | yes, until it ends | The client sends the same transcript again, so the change applies again on every later request |
| `setting` (model, effort) | yes, until the setting changes again | The setting still holds on the next request |
| `spend` | no | A model call happened once. Only its `ChangeID` is kept, to join it with the later saving |
| a cache effect | no | It is not an operation. `Compute` finds it for one request and returns it in `Result.CacheEffects` |
| a `rewrite` or `setting` with `DryRun` | yes, as `DryRun`, until it ends | A component in observe mode computes the same change on every request. If it were dropped, "would have saved" would show on the first request only, and an applied change would add up on every request, so the two could not be compared. It is never counted in totals ([R12](spec.md#r12)) |
| a `rewrite` that has ended | no | See §4.5 |

**From observe to enforce.** When the plugin leaves observe mode (a config reload), the change is
applied from the next request on, but the context operator does not report it again. So the
framework reports the switch: on the first request after it, it reports the plugin's active
`ChangeID`s again with `DryRun = false`. The Tracker replaces the `DryRun` entry with the applied
one, under the same `ChangeID`. The forwarded prefix changes at the first such position, so §5.2
row 1 adds a break with that `ChangeID`. That is the real cost of leaving observe mode.

So the state grows with the number of changes that are alive in the thread, and not with the number of
requests. If nothing ever ended, it would grow with the session. That is why the end rule (§11,
question 12) decides whether the memory is bounded: a change that is not found ending stays.

**What does not change.**

- The tier comes from the position and the cache state (§6.2, D1). The library does not need to know
  that a change is applied for the first time.
- `ChangeID` is the same on every request that carries the change ([R22](spec.md#r22)). It is the key that joins the
  first report to the later requests.
- A change keeps its context operator ([R51](spec.md#r51)), so its saving stays with the same plugin.

**A stateless context operator.** tool-prune keeps no state. It reads its `remove` list from config and
removes the same declarations from every request. It reports the change once. `ChangeID` is a hash of
the removed set and the config, so it is the same for the same config. The Tracker holds the rest.

**A config change of a stateless context operator.** A new `remove` list (for example, a hot reload) gives
a new `ChangeID`. The old change ends and a new one starts. `Compute` learns that the old change
ended by the end rule (§11, question 12). If the new list removes a declaration
that the old list kept, the forwarded body changes inside the cached prefix. The library then adds
a break that carries tool-prune's `ChangeID` (§5.2). That is the real cost of the config change.

## 5. Comparing requests

The comparison is **internal to the library**. It is not part of the public API. `accountant.Compute` runs it
first: it compares this thread's previous request with the current one, and finds B\*, a break and
the cache state. Then it prices with them. The host does not call it and cannot pass in a B\* or a
phase of its own, so every host derives them by the same rule. The host keeps the fingerprints,
finds the previous one by `thread.Key`, and keeps it in the thread's `ThreadState` (§6.1, wiring W5).

```go
// unexported
func compare(prev *Fingerprint, cur Fingerprint, ops []Operation, usage Usage, facts EntryFacts) (boundary *Position, effects []CacheEffect, phase CachePhase)
```

`prev` is the fingerprint of **this thread's** previous request, or nil. The library does no
matching of its own. `ops` are the request's context operations, reported and active: the break takes the `ChangeID`
of the one that changed the first differing position, or of the `setting` context operation. The results
come back in `Result` (§6.3): `Boundary`, `Phase`, and the break and any rescue as `CacheEffect`s.

### 5.1 The counterfactual boundary B\*

The provider reports this request's cache boundary as `cache_read`. But a plugin's cache break
moves it: in the coref example, `cache_read` stops at `messages[5]` because of the coref change.
Without it, the cache would have read through `messages[11]`. So the library needs **B\***, the
boundary that would have applied if no plugin had changed anything:

```
x  = min( prevCachedExtent,   // what this thread's previous request left cached, in client space
          clientFirstDiff )   // the last position before the client changed its own body
B* = the last client breakpoint at or before x   (with no client breakpoint, every position is one: B* = x)
B* = 0 when the cache expired, or when the body has breakpoints and none is at or before x
```

**The usage can overrule the hashes.** If `Usage.CacheRead` covers the whole of `prevCachedExtent`, the provider
read everything the previous request cached, so nothing in it was broken. A difference the hashes show inside that
extent is then not a difference in content. The host hashed something that moved, for example a cache mark on a
message the client did not change. In that case B* is the end of `prevCachedExtent`, not the start of the body.
Without this rule every saving replayed on the request is priced at the write rate, although the provider read
those tokens (accountant#21). A client edit inside the extent makes the provider read less, so the rule does not
apply to it.

The rule has one exception: a delivered cachesplit, a client diff before the end of the system section (the
tools section counts too), and a read that stops at the split. The split exists for the turn where the volatile
tail moved: the client body would miss from the system block on, and the forwarded body reads only the stable
half. On a short conversation that half is within the read tolerance (128 tokens plus 2%) of the whole previous
extent, so the read looks like a full read. It is not proof that nothing broke. The exception holds when the
billed read is at most the forwarded prefix before the first forwarded difference (`upTo(fwd, fwdFD-1)`), with
no tolerance. Then the full-read rule does not apply. B* is not moved to the end of the extent, so it stays where
the hashes put it (none, or before the changed block), and the split effect is booked (context-guru#909).

If the read is within the tolerance of the whole extent AND reaches past the forwarded first difference, the
exception does not hold. A real tail change always stops the read at the tail, short of the extent. A read that
passes it read text the hashes call changed, so the hash difference moved no content (a cache mark moved onto the
system block). The full-read rule applies: B* is the end of the extent, the split effect is not booked, and the
client body would have read the entry in full too (accountant#20, #21).

All three terms are in client space (§4.3). `prevCachedExtent` is the previous request's cached
extent, mapped from its forwarded body back into its client body through its splices. That is the
extent the client body would have left cached with no context operator: a change that shortened the
previous body does not shorten it.

**Why B\* snaps to a breakpoint.** The provider reads the cache only up to a breakpoint (§6.2). If
the client edits `messages[5]` and its last breakpoint before that is the end of `system`, the
counterfactual read ends at the end of `system`, not at `messages[4]`. The breakpoints are the
client's own (`BeginRequest`, §6.1), because the counterfactual is the client body as sent. Without
this rule, every saving between the breakpoint and `x` would be priced as a read when it would have
been a write.

**Provider details that lower B\* further.** The formula uses the breakpoints only. Two facts in
[`cached-prefix.md`](cached-prefix.md) can make the real read shorter: the Anthropic lookback of 20
blocks, and the minimum cacheable length. The library applies the minimum length in the tier rule (§6.2), not in B\*. When the host sets
`EntryFacts.MinCacheable` (512, 1024, 2048 or 4096 tokens, by model), content is written only at the first
breakpoint at or after it whose prefix reaches that length. Prefixes are cumulative, so a later breakpoint can
cache what an earlier, shorter one cannot. When no breakpoint reaches the minimum, the provider caches nothing
and the content is `Input`. With `MinCacheable` = 0 the host named no minimum and nothing changes. The library
also treats the **forwarded** body as uncacheable when its usage has `CacheRead` + `CacheWrite` +
`CacheWrite1h` = 0 and `Input` > 0. The usage says nothing about the client body, so that rule does not
apply to saved tokens. It does not model the lookback window. A
breakpoint more than 20 blocks after the previous entry can miss with an unchanged prefix, and the
library then prices too high a read. Shadow differences of this kind go to the case list ([§10.3](testing.md#103-what-makes-the-list-exhaustive) C).

On OpenAI models before GPT-5.6 there are no explicit breakpoints: the provider caches the longest
prefix it can, in steps of 128 tokens. For those models, the host passes no breakpoint. With no breakpoint
every position counts as a breakpoint (§6.2): B\* is the last position at or before x, and the read and
cached extents try every position. The extent tolerance absorbs the 128-token rounding. A host that passes
an implicit breakpoint gets the breakpoint pricing (a break moves read to write), which OpenAI does not bill.

Three conditions apply the rule, each with its own scope:
- **Candidates** (read and cached extents, setting-break start): this request has no *forwarded* breakpoint.
  The extents describe what the provider did with the forwarded body, so only that body matters.
- **Cached extent counts Input too**: this request has no *forwarded* breakpoint and the whole prompt (input + cache
  read + cache write) reaches `EntryFacts.MinCacheable`. The provider then caches the whole prompt it
  processed, read or not. A shorter prompt is processed without caching. The minimum applies to the prompt,
  not to the input part alone.
- **B\***: this request has no *client* breakpoint. B\* is a property of the client body, so B\* = x.

The host keeps one fingerprint for each thread: the most recent request of that thread (wiring document, W5.2).

```go
type Fingerprint struct {
    Thread    string
    At        time.Time // the start of the request; the request gap is measured from it (§5.2)
    Client    Body      // client body, before any plugin
    Forwarded Body      // body as sent upstream
    ClientBreakpoints, ForwardedBreakpoints []Breakpoint
    Cached    Position  // extent cached after its response: cache_read + cache_write (plus input when the request has no forwarded breakpoint and its whole prompt reaches `MinCacheable`), in forwarded space
}

// Body lists every position in hash order: tools, then system, then messages. The slice index is
// the position, so PosHash does not repeat it. Start gives the first index of each section.
type Body struct {
    Start [3]int
    Pos   []PosHash
}

type PosHash struct {
    Hash  uint64 // cumulative: equal hashes mean an equal prefix up to this position
    Bytes int32
    Media int32 // the base64 image/document part of Bytes (Anthropic source blocks and data URLs); not in the hash
}
```

```go
// NewBody hashes one body in hash order, so every host builds the same Body for the same bytes.
// The host calls it for the client body (before BeginRequest) and for the forwarded body (before
// EndRequest).
func NewBody(tools, system, messages [][]byte) Body
```

The fingerprint itself is built inside `Compute`. `Cached` comes from the usage, which arrives only
after the response, so no earlier call can build it. `Compute` keeps the result in
`ThreadState.PrevPrint` for the next request.

`PosHash` is 16 bytes in memory (8 for the hash, 4 for the size, 4 for the media size). It holds hashes and sizes only ([R2](spec.md#r2)).

### 5.2 Classifying a break

`fwdFirstDiff` is the first position where this thread's previous forwarded body differs from this
request's forwarded body, in forwarded space. `clientFirstDiff` is the same comparison on client
bodies, in client space. A body that only grew at the end has its first diff at the old end of the
body. `prevExtent` is the previous request's cached extent (`Fingerprint.Cached`).

Before a diff is compared with `clientFirstDiff`, it is mapped into the same space (§4.3).

**A diff breaks the cache only if it is inside what was cached.** A normal turn appends messages, so
both first diffs are at the old end of the body, after `prevExtent`. That is no break: the new tail
is written in both bills, and the tier rule (§6.2) already prices it. So rows 1 and 2 need a diff
**at or before `prevExtent`**.

**Two gaps.** The library finds two gaps from the times it holds. Both are measured from the
*start* of a request, because the provider measures the TTL from the start of the request that wrote
or read the entry:

- The **request gap**: from the start of the thread's previous real request (`PrevPrint.At`) to the
  start of this request. It says whether the entry would have expired **without** a ping.
- The **entry gap**: from the later of `PrevPrint.At` and `EntryFacts.LastPing` to the start of this
  request. It says whether the entry is alive **with** the pings that were sent.

Row 6 is checked first: with no previous fingerprint, no other row applies. The other rows are
checked in order, and the first match wins.

Rows 2, 4 and 5 set `InBaseline` on the break: the client body would have had the same break, so it
is on the record but is not part of the bill difference (§6.3). A row 1 break with no `ChangeID` (the
change ended, Q12) does not set it: the client body would have read that prefix, so the break counts.
A host that sums cache effects into a net skips each break with `InBaseline`.

| # | Comparison | Result |
|---|---|---|
| 1 | `fwdFirstDiff` ≤ `prevExtent`, and `fwdFirstDiff` < `clientFirstDiff` | A plugin broke the cache. Add a break with `at: fwdFirstDiff` and the `ChangeID` of the context operation, reported or active, that changed the position `fwdFirstDiff`. |
| 2 | `clientFirstDiff` ≤ `prevExtent`, and `fwdFirstDiff` ≥ `clientFirstDiff` | The client changed its body inside the cached prefix. Add a break with no `ChangeID`. |
| 3 | A `setting` context operation changed the model or a param (`effort`, `thinking`) since the previous request, and `Usage.CacheRead` is below the read that the forwarded body and the phase predict | Add a break with the `ChangeID` of the `setting` context operation. The break starts at the earliest `SettingChange.At` of the operation, snapped down to the last breakpoint before it (§6.2), because the bodies are equal there and no hash shows the position. If `At` is nil, the break starts at the read the usage shows and `penalty_unknown` is added (§6.4). If the operation changed several settings, the break names the operation, not one setting: the library cannot say which of them caused it. This is the one cost of a param change that can be measured. |
| 4 | No diff inside `prevExtent`, the phase is `hot`, but `Usage.CacheRead` is below the read the phase predicts: the end of `prevExtent`, snapped down to the last forwarded breakpoint at or before it. When the request is more than 5 minutes after the previous one, the prediction is cut at the head (the end of the first forwarded breakpoint group that is held at 1h): a 5m part past the head has expired | The entry was lost early (eviction, the lookback window, another reason the library cannot see). Add a break with no `ChangeID`; set `Cause` to `model_changed` when the model differs from the previous request's. A one-block tolerance absorbs the 128-token rounding of older OpenAI models. |
| 5 | No diff inside `prevExtent`, and the phase is `cold` | The cache expired. Add a break with no `ChangeID`, so the expiry is on the record ([R31](spec.md#r31)). It moves no tokens: B\* = 0 already prices every position as a write (§6.2), so the range from the read extent to B\* is empty, and the expiry is not counted twice. |
| 6 | No previous fingerprint for this thread | First turn of the thread with no `Inherits`, or a restart: no break is added. B\* is unknown (§6.4). |

The phase is found **before** the rows, from the gaps. It is the **cache state** that `Compute` uses ([R36](spec.md#r36)).

**Which TTL.** The clock judges the entry the previous request *wrote*. In the table, "TTL" is the TTL of that
entry, not `EntryFacts.TTL` of the current request (accountant#41). A 5m entry that a request asks to keep at 1h
is still expired after 30 minutes. A 1h entry is alive after 10 minutes, whatever TTL the new request asks for.
`CacheState.EntryTTL` keeps the `EntryFacts.TTL` of the writing request. A state from before that field (zero)
falls back to the current `EntryFacts.TTL`.

**Mixed TTLs.** The entry has one part for each breakpoint of the previous request, and each part lives for the TTL
of its breakpoint (`Breakpoint.TTL`, accountant#40). The prefix up to a 1h breakpoint is alive for 1h, the prefix up
to a 5m breakpoint for 5m. After 10 minutes a 1h head is alive and a 5m tail has expired. The phase is `hot` when any
part is alive, and the `Extent` is the end of the last live part (the head, here). The part beyond the last
breakpoint, up to the extent, has `EntryTTL` (`EntryFacts.TTL` of the writing request). A breakpoint whose TTL is
not "5m" or "1h" (empty, or a typo in a stored state) also takes `EntryTTL`, with no warning. With a ping, the entry
gap is compared with the longest TTL of the entry. If one part is alive on the clock and a ping kept another part
alive, the clock-alive part wins: the phase is hot, there is no ping rescue, and the window is under-credited (the
safe direction).

**A read extends a live head.** When some part is alive on the clock, a billed read that covers the prefix of a
clock-dead part (the 5m tail of a mixed entry) still extends the extent to that part. The 1,500 tokens of a tail change
that the provider read are priced at the read tier, not as a 5m write. The read only lowers a saving (R45).

**A fork** is seeded when the gap is within the longest part TTL of the parent's entry, the same rule. A fork that
keeps only the 1h head of a mixed entry (and changes the 5m tail) is not seeded: the seed needs the whole extent.
That is the safe direction (`unknown`).

**The current request's `Facts.TTL`** must be above 0 for the clock branch and for the fork seed. With 0 the phase is
`unknown` (a read can still prove it hot), whatever the state holds.

**A billed read beats the clock.** When `Usage.CacheRead` covers the prefix of a part (with the tolerance of
decision 7), that part was alive: the provider read it. The clock then loses at any gap, not only within 10 seconds.
Reasons: the provider measures the TTL from the end of the previous response, and the library measures from the
start of the previous request, so a long streamed response puts the gap past the TTL while the entry is alive.
Clock skew and queue time act in the same way. The usage is the provider's own evidence, and it can only lower a
saving (R45).

| Case | `CachePhase` | `Extent` |
|---|---|---|
| A previous fingerprint exists, and the request gap ≤ TTL of at least one part of the entry | `hot` | the end of the longest live part (`prevExtent` when all are live) |
| A previous fingerprint exists, the previous request cached nothing (its cache state was `cold`), and the request gap ≤ TTL | `hot` | empty: nothing can have been lost |
| A previous fingerprint exists, the previous request cached nothing, the lifetime is `exact`, and the request gap > TTL | `cold` | nil |
| A previous fingerprint exists, request gap > TTL, entry gap ≤ TTL, `PingRead` is set (or derived from `PingUsage`, below), and the lifetime is `exact` or `minimum`. The rescue also needs condition 3 of §4.4 | `hot`, and the library adds a rescue with `ChangeID = EntryFacts.PingWindow` (§4.4) | `PingRead` |
| A previous fingerprint exists, request gap > TTL of every part, the lifetime is `exact`, no ping qualifies, and `Usage.CacheRead` covers the prefix of a part | `hot`. The provider read the entry, so the clock is wrong (clock skew, queue time, a long response). No rescue | the end of the longest part the read covers |
| A previous fingerprint exists, request gap > TTL, the lifetime is `exact`, and no ping qualifies (none was sent, one was sent too late, or `PingRead` is unset and cannot be derived) | `cold`. A late ping gets no rescue | nil |
| A previous fingerprint exists, request gap > TTL, no qualifying ping, and the lifetime is `minimum` or `unknown` | `unknown`, unless `Usage.CacheRead` proves a read (below) | nil |
| No previous fingerprint for this thread | `unknown`, unless `Usage.CacheRead` proves a read (below) | nil |

**Out-of-order `At`.** A request whose `At` is before `PrevPrint.At` has no valid gap. Its phase is
`unknown`, unless a read in the usage proves the entry (below). `Compute` does not store its fingerprint: the
stored one is the newer request, so the next request is compared with that one (accountant#16).

**A fork reads its parent's entry.** A thread with no previous request and a `Thread.Parent` starts from the
parent's fingerprint and cache state, when the fork's forwarded body keeps the parent's whole cached extent
(equal hashes up to it) and the fork uses the parent's model. Otherwise the phase is `unknown`. The fork's
own fingerprint then replaces the seed, and the parent's state does not change. The seed is used only when the fork starts at or after the parent's last request and within the TTL, so it
can only give `hot`; otherwise the fork is `unknown` and a read in the usage makes it `hot`. A fork that
started before the parent's last completed request is not seeded. The seed is the parent's fingerprint at the
time of the call, and `Preview` and `Compute` can differ if the parent moved in between (R26, R54). The
out-of-order rule applies to a thread's own previous request, never to a seed.

**`PingRead` from the ping's usage.** A host may not know which position the ping read. The keeper
replays the previous request's forwarded body, so the ping's usage bills that body, and the library
derives `PingRead` when it is nil and `PingUsage` is set: the last position of the previous forwarded
body that `CacheRead + CacheWrite` covers, at the ping's own ratio (§5.3), with every position a
candidate. The ratio is `CacheRead + CacheWrite` over the bytes of that body. The ping's `Input` is left out:
a sweep question or a summary request is billed as input but is not in the previous body, so it would raise
the ratio and pull the extent back to an earlier message (accountant#14). A ping that found the entry gone and wrote it again renewed it, so its write counts. A ping
that cached nothing gives no extent, so the phase stays `cold`. An explicit `PingRead` wins. A host whose
ping does not replay the previous body, or that has no usage for the ping, sets `PingRead` itself.

**A read in the usage is evidence.** The provider bills a read only for an entry it holds. So when
the phase is `unknown` and `Usage.CacheRead` is larger than zero, the entry was alive up to the read
extent. The library then sets the phase to `hot` with `Extent` = the read extent, and uses it as
B\*. This is the safe direction: it lowers a saving (a read is priced below a write), and never
raises one. It does not give a rescue, because a read does not prove that the entry would have
expired without a ping. When `Usage.CacheRead` is zero, the phase stays `unknown`.

**A read from another thread's entry.** The provider keys its cache on content, not on threads. A
subagent that declares the same `tools` and `system` as the main thread can read the main thread's
entry on its first request. The rule above covers it: the read proves the entry, so the subagent's
first request is `hot` up to that extent, not `unknown`. The same sharing can inflate a rescue: a
sibling's request refreshes the shared head (the system prompt and the tool definitions, 20,093 tokens
in one measured session) for every other thread, so that head is warm without any ping.
The host reports it in `EntryFacts.SharedWarm`, and the rescue leaves it out ([R54](spec.md#r54), §4.4
condition 3). One measured session over-credited about $2.3 of $30.81 (about 7%) before this rule
(context-guru#853). The host, not the library, finds the prefix: only the host sees the other threads'
Trackers. A ping of one thread that keeps another thread's prefix warm is still not credited to the other thread.
`SharedWarm` reduces the keep-alive `rescue` only. The `ttlRescue` of a head held at 1h is not reduced: a sibling
that refreshed the head can over-credit it in the same way. That is a known gap, left out of R54 on purpose.

**Why `minimum` is not `cold` ([R45](spec.md#r45)).** `TTL` can be a proven expiry or only a guaranteed floor
(`apply.CacheLifetimeKind`: Anthropic's is `Exact`; OpenAI GPT-5.6+ states only a 30-minute
`Minimum`, with no documented maximum — "a minimum may prove an entry is still eligible, but
cannot prove it has expired"). On an `Exact` provider, a gap past `TTL` proves the entry gone, so
`cold` is correct and B\* = 0 is the honest answer. On a `Minimum` provider, the same gap proves
nothing — the entry may still be live — so scoring it `cold` invents a cache-write saving that may
not exist, in the unsafe direction. The host already carries part of this distinction
(`keepaliveCreditFloorMs`: pre-5.6 OpenAI is never credited, exactly because `Minimum` cannot stand
in for proof of expiry). The host sets `EntryFacts.Lifetime` from the same source
(`apply.CacheLifetime`), and the table above routes a `Minimum`-with-no-proof gap to `unknown`, not
`cold`. §6.4 then applies (`penalty_unknown`). A `minimum` provider does get a rescue
when a ping kept the entry (§4.4): the owner decision is that 30 minutes is 30 minutes.

### 5.3 Tokens for each position

These are the tokens of content that **was forwarded**: the library needs them for the penalty of a
cache break (§6.2), which counts forwarded content between the break and B\*. The tokens a change
**removed** are not in the forwarded body; their bytes come from the context operation's own `Size`
(§4.2 rule 7).

Both hosts send bytes for each forwarded position (`Body`) and for each operation (`Size`). The
library converts both with the request's own ratio (billed prompt tokens ÷ body bytes), as
`pricing.EstimateTokensFromBytes` does today. Results that use the ratio are marked `Estimated`.
A host may also send a token count in `Size.Tokens` (§4.2 rule 7). The same tokens in both hosts
holds whenever the two hosts receive the same bodies, the same operations and send the same token counts.

## 6. The library

### 6.1 Call

A **Tracker** keeps the state of one thread between requests. `Trackers` is the set of them,
kept by the host,
keyed by `thread.Key(session, thread)`. For each request the host calls four methods, in this order:
`BeginRequest`, `Add` (once for each reported context operation), `EndRequest`, and `Compute`.
`Compute` also stores the new thread state. There is no separate commit.

**The key has no tenant.** `ThreadKey` is the session ID and the thread ID. If two tenants can send the
same session ID, the host makes the session ID unique for each tenant (for example, it prefixes the
tenant). Otherwise one tenant's request replaces the other's `ThreadState`, and the next request shows a
break that nobody caused. The library cannot tell the two tenants apart.

**The thread is known only at the end of the pass.** The engine resolves the thread inside
`apply`, while the plugins run (wiring document, W5.2). So `BeginRequest` opens a record that
belongs to no thread yet. `EndRequest` names the thread, and only then does the record take a copy
of that thread's `ThreadState` and find `clientFirstDiff`. If no context operator resolved the thread
(context-guru is not in the pipeline), the host resolves it with `internal/thread` itself, from the
client body, before `EndRequest`.

```go
// ThreadState is kept between requests and replaced after each one.
type ThreadState struct {
    Active    []Operation  // rewrite and setting context operations still applied (§4.6)
    PrevPrint *Fingerprint // this thread's previous request; nil = none (§5.2)
    Cache     CacheState   // what the previous request left cached; zero value = unknown (question 11)
    Version   uint64       // +1 at each replace; the optimistic lock (R53)
}

// Trackers holds one Tracker for each thread. It is safe for concurrent use.
type Trackers struct{ /* map from thread.Key to *Tracker */ }

// Snapshot returns the state of every thread as JSON. It holds no content (R2): no message text.
// It holds hashes (as decimal strings), sizes, positions, and the IDs and names the state needs. It
// does no I/O. The same state gives the same bytes. It returns an error when a time cannot be encoded.
func (ts *Trackers) Snapshot() ([]byte, error)
// Restore replaces the state of every thread with a Snapshot. On an error it changes nothing. An
// EndRequest in flight during Restore can be lost.
func (ts *Trackers) Restore(data []byte) error
// Trimmed is the count of threads the trim has dropped since the set was made (a metric for the host).
func (ts *Trackers) Trimmed() uint64

// RequestMeta is what the host knows about the request before the pass: the fields of RequestOps
// that are not operations.
type RequestMeta struct {
    RequestID, Session string
    Model              string
    Params             map[string]string
    At                 time.Time
    Billing            Billing
}

// 1. Before the pass. The client body is hashed once, here (the body before any plugin), with the
//    client's own cache breakpoints, which B* needs (§5.1) and which cachesplit and headttl are
//    priced against.
func (ts *Trackers) BeginRequest(meta RequestMeta, client Body, clientBreakpoints []Breakpoint) *Request

// 2. During the pass. Each plugin reports; the framework sets Operator and DryRun.
func (r *Request) Add(op Operation)

// 3. End of the pass. The thread, the forwarded body, its cache breakpoints, and the facts only
//    the host knows. The record takes a copy of the thread's ThreadState here.
func (r *Request) EndRequest(thread Thread, forwarded Body, breakpoints []Breakpoint, facts EntryFacts)

// 4. After the response. The billed usage is the only input that arrives late.
//    Compute is deterministic and does no I/O (R26), except a slog warning for each failed check (§6.5). It prices the request, then replaces the
//    thread's ThreadState with the new one as its last step. If Compute fails or panics, the
//    ThreadState is unchanged.
func (r *Request) Compute(usage Usage) Result

// Preview does the same calculation and stores nothing. It is for pricing an old request again, and
// for a test or a tool that wants to look without moving the thread forward. A shadow run (migration
// step 5) calls Compute, because the ThreadState must advance on every request.
func (r *Request) Preview(usage Usage) Result

// A standalone RequestOps: a keep-alive ping or a detached call, with no request record. It holds
// only spend operations. It is priced without any ThreadState and changes none, so it is not one of
// the concurrent requests of R53. Its ChangeID joins it to the change or the window it belongs to.
func ComputeStandalone(ops RequestOps) Result

// Ping is a keep-alive ping in the library's own terms: the host copies its report into it. PingOps
// returns the one-spend RequestOps for ComputeStandalone, and the PingFacts for the thread's next
// real request (EntryFacts.LastPing and PingWindow). The ChangeID is "ka:" + thread + ":" + the window
// start in RFC 3339 UTC, and PingWindow is the same string, so a ping and its rescue share one change
// (§4.4). The thread "" becomes "main". PingFacts also carries the ping's usage, from which the
// library derives PingRead when the host does not set it.
type Ping struct {
    Session, ThreadID, Model, Host string
    Usage                          CallUsage
    At                             time.Time // when the ping started
    Window                         time.Time // the start of the idle gap the ping served
}
type PingFacts struct {
    LastPing   time.Time
    PingWindow string
    PingUsage  Usage
}
func (p PingFacts) Apply(f *EntryFacts) // copies LastPing, PingWindow and PingUsage (nil when zero)
func PingOps(p Ping, b Billing) (RequestOps, PingFacts)
```

**What each call writes.**

| Call | Who calls | What it writes |
|---|---|---|
| `BeginRequest(meta, client, clientBreakpoints)` | host, before the plugins | into the record: the request facts, the client sizes and fingerprint, the client breakpoints |
| `Add(op)` | the pipeline, once for each reported context operation | into the record: the reported context operations, in pipeline order |
| `EndRequest(thread, forwarded, ...)` | host, after the last plugin | into the record: the thread, a copy of its `ThreadState`, `clientFirstDiff`, the forwarded sizes and fingerprint, the breakpoints, the facts (TTL, lifetime kind, the last ping) |
| `Compute(usage)` | host, after the response | the thread's `ThreadState`: the only write to the part that lives between requests, and the last step of the call |
| `Preview(usage)` | host, when it must not change the state | nothing |
| `ComputeStandalone(ops)` | host, for a ping or a detached call | nothing |

**A ping is not a request of the thread.** The keeper's ping touches the thread's cache entry, but it
does not change the thread's content, and it reports only a `spend`. So it opens no record, and it
does not move `PrevPrint`. Its effect on the next real request comes only through
`EntryFacts.LastPing`, `PingRead` and `PingWindow`, which the host keeps beside the Tracker from the
`KeepAliveReport` (wiring document, W4.6).

The record of a request is private to that request. Two requests of one thread in flight at the same
time (a retry, parallel calls of one agent) each have their own record ([R53](spec.md#r53)). Each record keeps
the `Version` of the `ThreadState` it copied in `EndRequest`. `Compute` works like this:

1. Read the current `ThreadState` of the Tracker.
2. If its `Version` is the one the record started from, calculate and go to step 4.
3. If it is not, another request ended first. Take the current `ThreadState`, find `clientFirstDiff`
   again from the client fingerprint that the record keeps, find the two gaps again from the new
   `PrevPrint.At` (§5.2), and calculate. Set `Result.Recomputed`. The host facts (TTL, lifetime,
   the last ping) do not depend on the `ThreadState`, so they are still valid.
4. Replace the `ThreadState` only if its `Version` has not changed since step 1. If it has, go to step 1.
   The new `ThreadState` has `Version` + 1.

The compare and the replace are one atomic step. The calculation is outside the lock, so requests of one
thread do not wait for each other, except in a retry. A retry needs no I/O. `Preview` does steps 1 to 3
and does not replace.

The result depends on the order of the `Compute` calls. It is not the order in which the requests
reached the provider. When the two orders differ, the cache result of the later `Compute` can be wrong.
The host can reduce this by calling `Compute` in the order of the responses.

**Memory.** The Trackers bound themselves, and the host calls nothing for it. When more than 10,000
threads are held, `BeginRequest` drops the ones seen longest ago, down to 9,000. A thread's age is its
`PrevPrint.At`, or the `RequestMeta.At` of the request that made it while it has no previous request.
Equal ages drop in key order. The clock is the request's time, never the wall clock, so `Compute` stays
deterministic ([R26](spec.md#r26)). The limits are constants, not options.

**There is no idle timeout, on purpose.** A keep-alive ping keeps a cache entry alive after the last real
request, and the library learns of a ping only at the thread's next `EndRequest`
(`EntryFacts.LastPing`, §4.4). A thread idle for hours can still be hot because of its pings. If the
Trackers dropped it for being idle, its next request would find no previous request, the phase would be
unknown, and the rescue would be lost.

A dropped thread loses its active changes (§4.6). That is the cost described in Q13. It applies to the
oldest threads of a process that holds more than 10,000. A thread kept hot only by pings can be one of
them if it is the oldest. A request whose `At` is far before the others makes a Tracker that is among
the first to go. A `Request` in flight is not affected. It holds its own copy of the state, and
`Compute` makes a new, empty Tracker for the thread: `Result.Recomputed` is set and the phase is
unknown. A thread forked from a dropped parent is no different: with no parent state to seed from, the fork is
`unknown` (§5.2).

A missing call is not a compile error. If `EndRequest` was not called, or a fact is missing, `Compute` returns
the result with the missing items in `Unpriced` and a reason. It does not guess.

```go
// Usage is the provider's billed prompt usage. CacheWrite1h is the part of CacheWrite billed at
// the one-hour rate, never an addition to it.
type Usage struct {
    Input, CacheRead, CacheWrite, CacheWrite1h int
}

type Breakpoint struct {
    Pos Position
    TTL string // "5m" | "1h"
}
```

### 6.2 Tier rule

**The counterfactual.** The question for each context operation is: *if this change had never been made —
on this request or on any earlier request of the same conversation — how would the provider have
billed these tokens on this request?*

That is the question for a change across requests. A different question is possible: *what if the
change was not made on this request only?* For a replayed change, the answer to that question is a
cache break, because the earlier requests cached the body without the content. That figure is the
cost of **stopping** the change, not the value of the change, so the library does not use it.

**The rule.** `L` is the last breakpoint in the **client** body. The provider writes the cache only
up to a breakpoint, and bills content after `L` as input. The saved tokens are priced in client space
(§4.3), against the client's breakpoints, because they are tokens of the client body. The added
tokens of a `rewrite` and the break penalty are tokens of the forwarded body, so they are priced in
forwarded space, against the forwarded breakpoints.

| Position of the tokens | Tier |
|---|---|
| at or before B\* | **cache read** |
| after B\*, at or before `L` | **cache write**, at the TTL of the first breakpoint at or after the position (5m or 1h) |
| after `L` | **input** |
| any position, with no breakpoint in the body, after B\* | **input** |
| any position, when no breakpoint at or after it ends a prefix of at least `EntryFacts.MinCacheable` tokens (§5.1) | **input**, not write |
| a position in the forwarded body, when the usage has no cache tokens and some `Input` (§5.1) | **input**, even at or before B\* |

With no breakpoint in the body (automatic prefix caching, for example OpenAI), every position counts as a
breakpoint. B\* is then the end of the cached extent, and a position at or before it is a **cache read**. A cut
inside the cache is a read, and the break it causes moves the later tokens from read to **input**. The
other rows keep their meaning: the minimum length turns a write into input and, with no breakpoint, decides whether the prompt is cached at all, and a usage with no cache
tokens still prices the forwarded body as input.

On a cache miss, B\* = 0, so the first row matches nothing. A position is compared in hash order:
section first (tools, system, messages), then index. For a change with `Spans`, each span is
compared on its own (§4.3).

**The state.** B\* is an input (§5.1). When the phase is `cold`, B\* = 0, so every position takes the
write or input tier. So the same 8,000 tokens are worth 8,000 × read on a hot turn and 8,000 ×
write on a cold turn.

**Added tokens.** A `rewrite` with a `SizeAfter` larger than its `SizeBefore` (an injected tool
declaration, a restore) is a cost. Its added tokens take the tier of their position, by the same
rule: read before B\*, write after B\* and at or before `L`, input after `L`. They are `Caused`, not
`Saved`.
If the previous request already forwarded the added content and its prefix is unchanged, the provider reads it: it is priced at the read tier, whatever B\* says. Only the request that first adds it pays the write. The exception is a usage with no cache tokens and some `Input` (§5.1): the provider read nothing, so these added tokens are `Input` too.

**A break, and the breakpoints.** A break moves content from the read tier to the write tier. The content that moves is:

- **from** the end of what the provider still read on this request (`Usage.CacheRead`, mapped onto
  the forwarded positions),
- **to** B\*, as forwarded on this request.

The start is the read extent (`CacheEffect.From`), not the position of the first difference. The provider reads only up to a
breakpoint. If the last breakpoint before the break is the end of `system`, the provider reads only
`tools` and `system`, and every message before the break is written again too. So a break deep in
the messages costs the same as a break at `messages[0]` when there is no breakpoint between them.
The new tail after B\* is written anyway, so it is not counted.

**A rescue.** A `rescue` or `split` effect moves its tokens from the write tier to the read tier, over its range: for a keep-alive and a system split. For a breakpoint TTL there
are two legs (§4.4): on a reading request, from write (the 5m entry would have expired) to read; on
every writing request, the premium alone, as `Caused.CacheWrite1h` minus the 5m write it replaced.

**Changes to the breakpoints.** `cachesplit` and `headttl` change no content. They change the
breakpoints. So the library finds them by comparing the client breakpoints with the forwarded ones:
a breakpoint that a context operator added, moved or gave a longer TTL. Their saving is the
difference between the bill with the client breakpoints and the bill with the forwarded ones, on the
same content. `cachesplit` reports a `rewrite` with equal sizes, whose `ChangeID` names the split; the
library adds a `split` or `ttlPremium` effect with that `ChangeID`.

**The long-context tier.** Some models bill every token of a request at a higher rate when the
prompt is past a size threshold. A change that moves a request below the threshold changes the rate
of every token, not only of the removed ones. The library returns tokens and knows no thresholds
(D3). The host prices the forwarded tokens at the tier of the forwarded request, and the saved tokens
at the tier of the client request. A saving that crosses the threshold is marked `Estimated`, and
the size of the rate step on the forwarded tokens is not in the saving. That is an understatement,
the safe direction.

**The interaction.** On a rescued request, the content changes are priced in the hot state (rule
above). Without the ping they would have been priced cold, which is worth more. The difference is
neither the ping's nor the content changes' alone. The library reports it as
`Result.Interaction`, attributed to nobody, so the request total still equals the bill without
the context operations minus the bill with them.

**What v0 reports.** With no client breakpoint there is no write tier, so `Interaction` is empty. `Interaction` is the tokens of a saved span that the client body would have
written cold. `Result.InteractionCaused` is the same tokens at the read tier, as the operation
books them. The host prices `Interaction` minus `InteractionCaused`: for 1,201 tokens on Sonnet,
1,201 × (3.75 − 0.30)/Mtok = $0.00414. Both are zero unless all of these hold:

- the request has a `rescue` effect, and no `SharedWarm` (R54);
- the span is a non-`DryRun` `rewrite` that saved tokens;
- the span is at or before the ping's extent, and the operation books it at the read tier.

**The limit.** Any other case adds zero: a span after the ping's extent, a span after B\*, and a
rescue that is a TTL leg. A doubtful figure may only lower a saving (R45), so the library does not
guess. The figure is then too low, the safe direction. Conservation (check 6) counts tokens across
tiers, so it does not see the difference ([spec Q1](spec.md#q1)).

### 6.3 Output

```go
type Result struct {
    Version     int
    Ops         []PricedOp     // one for each input operation, same order: the reported operations,
                               // then the active ones applied again
    CacheEffects []CacheEffect // the breaks, rescues and breakpoint changes the library found (§4.4)
    Interaction TierTokens     // §6.2: on a rescued request; attributed to nobody
    InteractionCaused TierTokens // §6.2: the same tokens at the tier the operations book them
    Unpriced []Unpriced     // with a machine-stable reason
    Checks   []CheckFailure // §6.5
    Boundary *Position      // the B* used; nil when unknown
    Recomputed bool         // another request replaced the ThreadState first (R53, §6.1)
    Phase    CachePhase     // the cache phase it used (§5.2). For the host's own checks, never an input
}

type Unpriced struct {
    OpID   string
    Reason string // "unknown_kind", "negative_size", "penalty_unknown", "ping_result_unknown", "end_request_missing",
                  // "no_usage", "not_spend", "bad_usage", "ratio_range", "media_tokens", "breakpoints", "size_range", ...; the host adds
                  // "operator_not_allowed" from its own allow-list (wiring document, W5.1)
}

type CheckFailure struct {
    Check    string // "sum" | "order" | "unique_change" | "spans" | "attribution" |
                    // "conservation" | "shared_warm" | "bad_usage" | "ratio_range" | "breakpoints" |
                    // "media_tokens" | "size_tokens" | "size_range" (§6.5)
    Operator string // "" when the check is not about one operator
    Detail   string // numbers only, no content
}

// OpID is unique inside the Result. A reported operation keeps its own ID. An active operation
// applied again gets "active:" + its ChangeID, so it cannot collide with the IDs of this request (R6).
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

type TierTokens struct {
    Input, CacheWrite5m, CacheWrite1h, CacheRead int
}

type CacheEffectKind string // break | rescue | split | ttlPremium | ttlRescue | markPremium

// CacheEffect is a change in how the provider bills content that is already there (§4.4). It is
// output only. ID is unique inside the Result: "ce:" + a number.
type CacheEffect struct {
    ID        string
    Kind      CacheEffectKind
    ChangeID  string      // the cause of a break, the window of a rescue, the head entry of a TTL leg; empty on a break nobody caused
    From, To  *Position   // the prefix range; nil From = the start of the prompt, nil To = the end of the body
    TTL       string      // "5m" | "1h"; set on the TTL kinds
    Saved     TierTokens
    Caused    TierTokens
    Estimated bool
    Cause     CacheEffectCause // why a break nobody caused happened, when the library knows; "model_changed" is the only value
    InBaseline bool           // the client body has this break too (§5.2 rows 2, 4, 5): on the record, not in the bill difference
}
```

`Cause` is "model_changed" when row 4 (§5.2) finds a loss and the model differs from the previous
request's: the provider keeps a cache for each model, so the loss is explained. It is empty otherwise.

`Saved` and `Caused` are separate and both non-negative, because the money layer rejects negative
values (`pricing.MicrosFromUSD`). Cortex prices each side with the existing code.

A break penalty has both sides: the tokens are written (`Caused.CacheWrite5m`) instead of read
(`Saved.CacheRead`). The net is the rate difference, with no special rule.

A cache effect is priced as its own item, in `Result.CacheEffects`. A break's `ChangeID` is the one it was
given, so the host adds the penalty to the total of the change that caused it, without a lookup. The `Kind`
tells the host where to settle it: a break goes to its cause, a rescue to the keeper by its window.
`spend` context operations are not priced in tokens. `PricedOp.Call` returns their `Call.Usage`
unchanged, and the host prices it with the same resolver as the savings (wiring document).

**Conservation.** For each request, the priced `Saved − Caused` of all content changes, cache
breaks and state changes, plus `Interaction`, equals the bill of the client body in the state it
would have had without any context operation, minus the bill of the forwarded body. The library checks
this in tokens (§6.5, check 6).
A `break` with no `ChangeID` is in this sum only when the context operators caused it. A break that the client
body would have had too (an entry lost early, a client edit) is on the record and cancels out. A break
that a context operator left behind, after its change ended, is not in the baseline, so it counts.

### 6.4 Unknown boundary

If the boundary is unknown (`Result.Boundary` is nil), the library uses the extent `cache_read` covers as the boundary, charges no
penalty, and adds `penalty_unknown` to `Unpriced`.

This is the safe direction for the **gross** saving: removed tokens are priced at the read tier
whenever the request read them, and never higher. It is **not** the safe direction for the **net**
figure: if a plugin did break the cache on this request, the missing penalty makes the net saving
too high. So a host must not show a net figure from a request with `penalty_unknown` as a measured
one. It shows the gross saving, and marks the net as `Estimated`.

### 6.5 Checks

**Not run in v0:** check 2 (order). Attribution (check 5) holds by construction, because a break takes only
the `ChangeID` of an operation of the same request.

The library reports failed checks in `Result.Checks`. A failure does not stop pricing. The one exception is check 13 (`size_range`), which stops the pricing of the one operation it names. `Compute` also
logs each failure at warning level with `log/slog` (message `accountant check failed`, with the
check, the operator, the detail, and the request, session and thread IDs). `Preview` logs nothing.

1. **Sum:** for each section, the context operations that are applied, and not `DryRun` —
   `SizeBefore.Bytes − SizeAfter.Bytes` adds up to the client size minus the forwarded size, in
   bytes (`Body`). The context operations
   are those reported on this request and the active ones that `Compute` applies again. The host sends
   both sizes (taken in `BeginRequest` and `EndRequest`, §6.1). A failure means one of three things: a plugin changed
   the body and did not report it, `Compute` applied a change that has ended, or `Compute` dropped a
   change that is still in the body (§4.5, §4.6).
2. **Order:** the context operations are in pipeline order, so a context operator's output positions are mapped through the `MessageSplice` of the context operations before it. An active context operation keeps its client-space position and its pipeline position, and is mapped through this request's splices (§4.3).
3. **Unique `ChangeID`:** no two `rewrite` or `setting` context operations of one request, reported or active, have the same `ChangeID`. A `spend` or a cache effect may share the `ChangeID` of the change it belongs to.
4. **Spans:** for a context operation with `Spans`, its `SizeBefore` and `SizeAfter` equal the sums over its spans.
5. **Attribution:** a `break` effect's `ChangeID` is the `ChangeID` of a context operation of the same request, or empty. A `markPremium` has the `ChangeID` of the operation whose span covers the host's breakpoint, or none. A `rescue`, `ttlPremium` and `ttlRescue` are exempt: their `ChangeID` is a keep-alive window or a head entry, which names no operation of this request.
6. **Conservation:** the request total of the applied context operations and the cache effects equals the bill difference (§6.3). The ranges of operations unpriced for `media_tokens` are taken out of the bill difference (as `DryRun` operations are left out), because the operation prices none of it; every other operation stays in the check.
   Each size is compared at the request ratio, so a correct `Size.Tokens` passes (the gap between the host's tokens
   and the ratio's figure for the same bytes is taken out of the total). `DryRun` context operations are left out,
   because their content went upstream unchanged. A `markPremium` moves tokens between tiers and nets zero tokens, like every cache effect, so the check does not change: the bill difference is the client tokens less the billed tokens, and a premium changes neither.
7. **Shared warm:** `EntryFacts.SharedWarm`, when set, is a position in the forwarded body. If it is not, the check fails
   with the section and index, and the keep-alive rescue of that request is not credited ([R54](spec.md#r54)).
   A doubtful figure may only lower a saving.
8. **Usage sign and cap:** no count in `Usage` is negative or above 2^40 (about 1.1e12 tokens), and `CacheWrite1h` is not
   larger than `CacheWrite` (it is part of it, never an addition). On a 64-bit `int` the cap keeps the sum of the tiers from wrapping. On a 32-bit `int` the cap cannot be reached, and a sum that wraps gives a ratio that fails check 9, so nothing is priced. `Facts.PingUsage` is not checked here.
   If one count is out of range, the check `bad_usage` fails. The same rule applies to the `Usage` of a spend's `Call`;
   there `Output` must not be negative or above the cap, and `CostUSD` must not be negative, NaN or infinite. A spend
   whose `Call` fails goes to `Unpriced` with `bad_usage` (its other figures are not changed), also in `ComputeStandalone`.
   A valid `Call` is passed on as the host sent it.
9. **Ratio range:** the tokens per forwarded text byte, `(Input + CacheRead + CacheWrite - media price) / (bytes - media bytes)`,
   is from 0.05 to 2.0. When the bill is below the media price, the media price is first cut to the bill (check 11), so this check does not fail for that reason. Base64 media bytes are not in the denominator, and the media price (1 token per 250 bytes) is not in the numerator.
   If it is outside that range, the usage does not fit the body, and the check `ratio_range` fails. A ratio of 0
   (a body with text bytes and no billed prompt token) and an infinite ratio (billed tokens and an empty body) are outside
   the range too. A body of media only has no text bytes: it passes only when the bill is at most the media price (a smaller bill is cut, check 11). A bill above it fails this check, because the extra tokens fit no content. Without this
   check the ratio, and so the conservation check (check 6), agree with themselves for any usage.
10. **Breakpoints:** a request has at most 4 forwarded breakpoints (the Anthropic limit), and each TTL (client and
    forwarded) is `"5m"`, `"1h"` or empty (empty counts as `5m`). Otherwise the check `breakpoints` fails.

11. **Media tokens (`media_tokens`):** the fixed price of the forwarded body's media (1 token per 250 bytes) is not above the
    billed prompt tokens. If it is, the price is wrong, because the provider cannot bill less than the media costs (it
    downscaled a large image, for example). The check fails, and the library does not drop the request. It cuts the media to
    the bill less 0.05 tokens for each text byte, and gives the text the rest, which is the lowest text ratio that check 9
    allows. Every saving on text is then the lowest the bill allows. An operation whose range covers media goes to
    `Unpriced` with the reason `media_tokens`, because the bill does not say how it splits between that media and the text.
    The tokens of such an operation's ranges are taken out of the bill difference of check 6.
    A range with `Size.Tokens` on both sizes is exact and stays priced. Every other operation is priced and `Estimated`.
12. **Size tokens (`size_tokens`):** a `Size.Tokens` is between 0.05 tokens for each text byte and 2.0 tokens for each byte
    of its range (the limits of check 9). If it is not, the host's count is a bug: the library ignores it, converts the
    bytes, and reports the check.
13. **Size range (`size_range`):** the sizes of a rewrite are inside two bounds, on the operation and on each span.
    - `SizeBefore.Bytes` is at most the largest body the operator could have received: the client body total plus
      the sum of `max(0, SizeAfter.Bytes - SizeBefore.Bytes)` over the request's rewrites (an operator before it may
      have inserted content, for example retrieval).
    - `SizeAfter.Bytes` is at most the forwarded body total plus the sum of `max(0, SizeBefore.Bytes - SizeAfter.Bytes)`
      over the rewrites (a later operator may have removed part of its output).
    - A `DryRun` operation (observe mode) read a real body, so only its `SizeBefore` is bounded. Its `SizeAfter` is a
      guess that never reaches the totals (R12), so only check 12 looks at it.
    - Every active operation counts in the sums, even when a report with its `ChangeID` replaces it. Whether that
      report is honest is what the check decides, so the bound must not depend on it. The bound is found from
      the loose side: all operations and all non-negative reports count first, a failing report is dropped, and
      the sums are made again until none drops. So an honest report is never unpriced by the failure of another.
      The price is a bound that is loose by the size of a replaced operation. (The copy `EndRequest` stored of a
      report of this request has the same sizes as the report, and counts once.)
    - A report that fails a bound adds to neither sum, so one bogus report cannot raise the bound for itself. A
      `DryRun` operation adds to neither sum.

    If a bound fails, the report is a bug. That one operation goes to `Unpriced` with `size_range`, and the check
    reports the failing span's sizes and the two bounds. The other operations are priced as usual, and the `sum`
    check still runs. Like a negative size, a `size_range` report is not a change: it replaces no active change with
    its `ChangeID` and is not stored (§4.6). This is the narrow exception to "a failure does not stop pricing": the
    `sum` check alone cannot tell a wrong report from a wrong guess, but a size above every possible body is wrong
    in every case, and its tokens would otherwise reach the totals unbounded. Active operations are not checked
    again: if a client compacts its body below an active size, the `sum` check flags it.
    Positions: `NewBody` saturates the byte count of one position (and its media bytes) at the largest `int32`, so a
    position over 2 GiB does not wrap negative.

If check 8, 9 or 10 fails, the library prices nothing for the request. Each operation goes to `Unpriced` with the
reason `bad_usage`, `ratio_range` or `breakpoints` (the first check that fails, in that order), no cache effect is
added, and the cache state for the next request is unknown, as for a response with no usage (§8). The check name
equals the reason. A check on a request with no usage is not run: that request is `no_usage`.

## 7. Billing plans

How a host records the result, the size limit on recorded changes, and the totals over a session
or thread are in the wiring document (W7.1 to W7.3).

### 7.1 Billing plans

`accountant.Compute` returns the same tokens for each tier for an API user and for a subscription user. The
host converts them to a value by the request's `Billing.Mode`:

| `Billing.Mode` | Value of a saving | Value of a spend | Shown as |
|---|---|---|---|
| `api` | tokens × the provider's rates | `Call.Usage` × the provider's rates | money saved; money spent |
| `subscription` | tokens × the **weights**, in usage-limit units | `Call.Usage` × the same weights | usage-limit headroom; an API-equivalent dollar figure beside it, labelled "API equivalent" |
| `unknown` | as `api`, with every figure marked `Estimated`, recorded with `Basis = unknown` | as `api` | money, marked estimated, never in a money total |

**Weights.** A weight says how much one token of a tier counts against the usage limit, compared
with one input token. The provider does not publish weights for each tier. So the default weights
are the ratios of the API rates (input 1, cache write 5m 1.25, cache write 1h 2, cache read 0.1,
output 5), and every subscription figure is marked `Estimated`. A host can set measured weights in
its config ([§11, question 2](spec.md#q2)).

**Keep-alive on a subscription.** A ping on the user's own credential uses the user's usage limit,
not money. The keep-alive window still adds up (§4.4), in usage-limit units.

**Detection.** The host reads `Billing.Mode` from the credential type on the request: an API key
means `api`; an OAuth token from a subscription login means `subscription`. If it cannot tell, it
uses `unknown`. Cortex and the proxy must use one rule, so the rule is a function in `accountant`:
`accountant.BillingFrom(headers)`. It reads only the credential type and returns no part of the credential
([R2](spec.md#r2)). Both hosts must call it on the **client's** upstream credential header (`x-api-key` or
`Authorization`) before anything rewrites it — not on the proxy's control-plane login
(`proxy/authmode.go`), which is a different credential. A contract test in each host feeds the same
captured headers and expects the same `Billing.Mode`.

**Recording.** `event.Saving` and `SpendRecord` gain `Basis`: `usd`, `subscriptionEquivalent` or
`unknown`. `TotalAvoidedUSD` and the cost ledger count only `Basis = usd`. A subscription saving goes
in a new usage-limit column, so API-equivalent dollars never enter a money total. An `unknown` figure
is shown beside the totals and is not added to them, because it may be a subscription user's ([R43](spec.md#r43)).

## 8. Errors

The rule: an error in savings never changes the request and never invents money. If a fact is
missing, report too little and say why.

| Situation | Result |
|---|---|
| `accountant.Compute` panics | The host recovers, writes no saving for the request, and increments `ops_price_panics`. The request and its spend record are unchanged. |
| A malformed context operation (unknown kind, negative size, a size above the bounds of check 13, a spend whose `Call` has a negative or non-finite figure) | Goes to `Unpriced` with a reason (`unknown_kind`, `negative_size`, `size_range`, `bad_usage`). The other context operations are priced. |
| A `spend` whose `Call` has no usage (a ping that timed out: the host sent it and read no response) | `Unpriced` with `ping_result_unknown`. The cost is missing, not zero, so the window must not show its rescue as a net gain. The rescue is still credited from the read on the request that follows (§4.4) |
| Usage is negative, does not fit the body (tokens per byte outside 0.05 to 2.0), or the request has more than 4 breakpoints or an unknown TTL | Nothing is priced. Every operation goes to `Unpriced` with `bad_usage`, `ratio_range` or `breakpoints`, and a check of the same name fails (§6.5, checks 8 to 10). |
| Sum check fails | Pricing goes on. A warning is logged. The failure is counted for each plugin on `/v1/pipeline`. |
| No previous fingerprint for the thread | `Boundary` is nil, §6.4, unless `Usage.CacheRead` proves a read (§5.2). |
| Response has no usage | Nothing is priced, as today. The active changes are still kept (§4.6). |
| No rate for a tier | `Saving.Provenance` is empty, as today. Tokens are on the record. |
| Context operator not on the allow-list | Kept on the event, unpriced. |
| `on_error: observe` | `DryRun = true`. Priced and shown as "would have saved", never in totals. |
| `Billing.Mode` is `unknown` | Priced as `api`, every figure marked `Estimated`, recorded with `Basis = unknown`, never in a money total (§7.1). |

## 11. Open questions

These are the open questions about how the library does it. The open questions about behavior
([questions 1](spec.md#q1), 2, 9 and 10) are in [`spec.md` §11](spec.md#11-open-questions).
Where a question has test cases, a host that compares them with its own code marks them `differs` or `notCovered` until it is decided ([§10.3](testing.md#103-what-makes-the-list-exhaustive)).

- <a id="q3"></a>**Q3. Accuracy of the bytes ratio.** The request ratio is an average. Removed content (large JSON
   tool output) may have fewer bytes per token than the average request. Option: `Size` takes a
   content class (`text`, `json`, `binary`) and the library keeps a ratio for each class. Decide
   after measuring the error on real sessions. The optional `Tokens` field on `Size` is done
   (accountant#11), as is the exclusion of base64 media from the ratio. A content class and an error
   range for each `PricedOp` are still open.

- <a id="q4"></a>**Q4. Where the previous request's hashes live.** The counterfactual needs the previous request's
   hashes on the same thread. The current design (D12, wiring document) has the host keep one
   fingerprint for each thread. An alternative is a push model: components push facts with an
   identity, and the receiver keeps no state. Then the hashes travel in each push, or the context operator
   keeps them, and who classifies a cache effect changes with it. Not decided.

- <a id="q5"></a>**Q5. Label volume.** Pushing `change` and `request` as labels on every series gives many series in a
   long session. This must be measured before the push model is fixed.

- <a id="q6"></a>**Q6. Asynchronous recalculation.** A replay should not cause a recalculation every
   time. Recalculation should be async and buffered. A newer request for the same key replaces the
   queued one and keeps its queue position, so nothing starves. The queue design is not written.

- <a id="q7"></a>**Q7. Where the library lives.** Decided: its own repository, `github.com/rossoctl/accountant`, package
   `accountant` (D7).

- <a id="q8"></a>**Q8. Names in Cortex.** `pctx.ReportOps` and the Cortex name for the component that writes to the
   cost ledger have no chosen name (wiring document).

- <a id="q11"></a>**Q11. What the Tracker's state holds.** `ThreadState` has three parts (§6.1): the active context
    operations, the previous fingerprint (`PrevPrint`), and the cache state. The active context
    operations are decided. The other two are in `ThreadState` in this design, and not in what the host
    passes in. A restart keeps all three if the host saves and loads them with `Trackers.Snapshot` and
    `Restore` (question 13, §6.1). Without a snapshot a restart loses them. Whether the cache state belongs in `ThreadState` at all, or is only derived
    from `PrevPrint` and the gap, is not decided.

- <a id="q12"></a>**Q12. How a context operation ends (decided, kept here for the record).** Decided: `Compute` infers the end from the bodies and the splice.
    A change is reported once, and `Compute` applies it on later requests until it ends. It ends when:
    - **a splice removed its position** (a summary replaced it, or the client compacted), or the client
      edited at or before it (§4.3, §5.2);
    - **the client's own item is back at its position.** The forwarded item at the change's position has
      the client item's size again, although the change made it smaller or larger. This is a restore
      in place. The size is compared, not the hash, because the hashes are cumulative and differ when
      another change sits earlier in the body. A `DryRun` is exempt, because it forwards the client's item by design;
    - **for content the operator added** (a size before of zero), the client edited something before
      its position. Its position is in forwarded space, so the client body having nothing there does not
      end it.

    A break that a change leaves behind when it ends has no `ChangeID` (§5.2 row 1), because the change
    is no longer reported or active. It is part of the bill difference (§6.3).

    It does not find a change that ends with bytes identical to the changed form. The sum and
    conservation checks (§6.5) still catch a wrong guess after the fact. The proposal that the operator
    names its active `ChangeID`s on each request stays available if this rule proves too weak. It would
    also answer question 13.

- <a id="q13"></a>**Q13. How the state recovers.** A context operator reports a change once. If the Tracker loses a
    thread's state (a restart, an eviction, a thread created again), the change is not reported
    again, and it stays missing until it ends (§4.6). Options: the Tracker persists its state; the
    context operator reports the active changes again when it sees a new thread, or on request; or
    the host replays the session from the first request of the thread. Each has a cost. Persisting
    adds I/O to a library that does none today ([R26](spec.md#r26)). Asking the operator breaks the rule that it
    reports once. A replay needs the whole history, which the archive may not have. The second
    proposal of question 12 (the operator names its active `ChangeID`s on each request) detects the
    loss, and the operator can then report the full operations once more. Not decided.
    Partly handled: the Trackers drop the oldest threads on their own above 10,000 (§6.1, Memory), so a loss
    now also happens there. Partly decided: the host saves the state with `Trackers.Snapshot` and loads it
    with `Restore` (§6.1), so a restart keeps the active changes. The library does no I/O: the host
    chooses where the bytes go. `Trimmed` counts the threads the trim dropped. A trimmed thread is not
    recovered. How the state recovers after a crash with no snapshot is still not decided.

- <a id="q14"></a>**Q14. Two requests of one thread in flight.** A retry and parallel calls of one agent can overlap.
    Each opens its own request record, copies the thread's `ThreadState` in `EndRequest`, and each
    `Compute` replaces it when it ends. Decided: optimistic locking (D14, [R53](spec.md#r53), §6.1). Decided: a
    ping and a detached call open no record and change no `ThreadState` (`ComputeStandalone`,
    §6.1), and a subagent is its own thread. The order of the `Compute` calls can differ from the
    order at the provider. Open: whether the host must call `Compute` in the order of the responses.

- <a id="q15"></a>**Q15. Re-pricing with `Preview`.** `Preview` prices a request from the current `ThreadState`. To price
    an old request again it needs the state the thread had then (§4.6), which the Tracker does not
    keep. Open: whether `Preview` takes a stored `ThreadState`, or is only for the current request.
