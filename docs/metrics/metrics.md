# The metrics module

`accountant/metrics` turns a `Result` into OpenTelemetry counters. It is a nested Go module, with its own
`go.mod`, so the root module keeps importing only the standard library (R3). The module imports the
OpenTelemetry API only. The SDK, the exporter and the collector settings belong to the host.

The names and labels follow the metrics spec of the host project (context-guru issue #886, section 5.2).
That spec is not in this repository. This page is the contract of the code.

## How a host uses it

1. Build the sink once: `metrics.New(meter, pricer, opts...)`.
2. After `Compute`, call `sink.Observe(metrics.Observation{Meta, Usage, Result})`.
3. Use `metrics.Multi` to give the same observation to several observers.

The hook lives in this module, not in the root. `Observer` is a one-method interface,
`Observe(Observation)`, and `Sink` is the OpenTelemetry one. A host with another consumer, such as a
ledger, declares the same interface itself and needs no import of this module. An `Observer` must return
fast, make no network call, and not keep the slices of the `Result`. `Multi` skips nil observers,
recovers a panic in one, and returns nil when none is left.

`Observe` only adds to in-memory counters. It makes no network call. It takes a lock only to add one of the first
64 component names. After that, no call takes a lock. The accountant never calls the sink: the host does, after the request is answered.

## The `Pricer`

The module has no rate table. The host gives it one method:

`Price(model string, at time.Time, tier Tier) (usdPerToken float64, estimated bool)`

- If the rate is a fallback or a guess, the `Pricer` returns `estimated` true.
- An unknown model must return `estimated` true.
- The `Pricer` must not block. `Observe` calls it on the request path.

Tiers are `input`, `cache_read`, `cache_write_5m`, `cache_write_1h` and `output`.

### The `SizedPricer` (optional)

Some providers bill a large request at a higher rate for **all** its tokens. Examples: GPT-5.4 and later above
272,000 input tokens, and Claude Haiku 5.5 above 100,000. A flat rate prices such a saving wrong, because the
request with the operation and the request without it can fall on opposite sides of the line.

A host adds one method to its `Pricer`:

`PriceAt(model string, at time.Time, tier Tier, promptTokens int64) (usdPerToken float64, estimated bool)`

`Pricer` does not change. The sink checks once, in `New`, if the host's pricer also has `PriceAt`.

- The prompt size is fresh input plus cache read plus cache write, as the provider counts it.
- **Saved** tokens use the size the request would have had without the operations.
- **Caused** (added) tokens use the billed size.
- The billed size is `Observation.Usage.Input + CacheRead + CacheWrite`. `CacheWrite` already holds the 1h part.
- The size without the operations is the billed size, plus the saved tokens of all counted operations, minus
  their caused tokens. A dry-run operation is not counted. A cache effect and the interaction move tokens
  between tiers and net zero tokens, so they do not change the size.
- **The re-rate.** The saving of a request is the bill of the request without the operations minus the bill of
  the request as sent, each priced at its own prompt size. Pricing the moved tokens is not enough: when the two
  sizes are on different sides of a line, the tokens that **stay** in both requests change rate too. Example,
  with a line at 272,000 and a 2x rate above it: a trim takes 290,000 tokens to 250,000. The moved side books
  40,000 x 2x. The 250,000 tokens that stay drop from 2x to 1x. The saving is 290,000 x 2x minus 250,000 x 1x.
  - Per tier, the re-rate is `stay x (rate at base size - rate at billed size)`. `stay` is the billed tokens of
    that tier (from `Observation.Usage`) minus the tokens the counted operations caused in it.
  - It books **to the operations**, so the per-component counters add up to the request total. Each operation
    takes a share in proportion to its net saved tokens (saved minus caused, only where above 0).
  - A positive amount books to `saved.usd` and a negative one to `added.usd`, with the operation's `kind`, the
    tier and the `component`. A negative amount happens when the sent request is above the line and the request
    without the operations is not, for example when an operation adds tokens. The component's net
    (`saved.usd` minus `added.usd`) can then be below 0.
  - If no operation has a net saving above 0, nobody owns the re-rate. It books to `interaction.usd`, with
    `direction` saved or added. The request total still holds it.
  - Why proportional: the line is crossed by the operations together, so one operation alone does not own the
    change. Net saved tokens is a fair, stable weight, and it needs no extra data.
  - **Output tokens.** The host gives them in `Observation.Output`. The output is the same with and without
    the operations, so all of it stays. Its re-rate is `output x (rate at base size - rate at billed size)` in
    the `output` tier, split like the other tiers. Example: GPT bills output at 1.5x above the line, so 2,000
    output tokens on the 290,000 to 250,000 trim add `2,000 x (1.5x - 1x)` to the saving. Output never counts
    toward the prompt size. A host that leaves `Output` at 0 gets no output re-rate.
  - No tokens move, so no token counter changes.
  - Only a host with `PriceAt` gets the re-rate. A request that crosses no line has a re-rate of 0.
- A cache effect and the interaction follow the same two sides. A cache effect priced as `saved` uses the size
  without the operations. One priced as `added` uses the billed size. The interaction splits by tier: a positive
  net tier is saved, a negative one is added.
- A `spend` call is priced at the prompt size of its own `Call.Usage`.
- The sink never passes 0 or a negative size. If `Usage` is empty, or the size without the operations is not
  above 0, the sink calls `Price` for that side.
- A pricer without `PriceAt` gets the same figures as before. (The sink now skips a tier with 0 tokens, so it can make fewer `Price` calls.)
- An operation the engine does not price (`media_tokens`, `unknown_kind`) has zero `Saved` and `Caused`. The
  derived size does not include its tokens. Near a line, the size can then fall on the lower tier.
- The size without the operations carries the engine's byte-to-token rounding, about one token for each span.
- **`estimated`.** The sink adds no new `estimated` mark for the derived size. The size is arithmetic
  on billed usage and on the tokens of the operations. It is exact only as far as those tokens are. When an operation's tokens are an estimate, the operation
  is `Estimated` and the request is already marked. The `estimated` that `PriceAt` returns is used as for `Price`.

## Counters

OTel names use dots. The collector converts them to Prometheus names (`llm_savings_saved_tokens_total`).

| Counter | Labels | Meaning |
|---|---|---|
| `llm.savings.saved.tokens` | `kind`, `tier`, `component` | Tokens the provider did not bill. |
| `llm.savings.added.tokens` | `kind`, `tier`, `component` | Tokens the provider billed because of an operation. A `spend` books its call usage here. |
| `llm.savings.saved.usd` | `kind`, `tier`, `component` | The saved tokens, priced. |
| `llm.savings.added.usd` | `kind`, `tier`, `component` | The added tokens, priced. |
| `llm.savings.cache_effect.usd` | `effect`, `cause`, `direction` | Cache effects, in money. Not in saved or added. **Add it** for the net saving (see below). |
| `llm.savings.interaction.usd` | `direction` | The net value of `Result.Interaction` minus `Result.InteractionCaused` (design §6.2). Not in saved or added. **Add it** for the net saving (see below). |
| `llm.savings.requests` | `result` | `priced`, `unpriced` or `skipped`. |
| `llm.savings.unpriced` | `reason` | Operations the library did not price. |
| `llm.savings.check_failures` | `check` | Failed library checks. |
| `llm.savings.estimated` | none | Requests with an estimated figure. |

## The net saving

The bill difference of a request is the applied operations plus the cache effects plus the interaction
(design §6.2, and check 6 in §6.5). Each part has its own counter, and no counter contains another. So the net
saving, in money, is:

```
  llm.savings.saved.usd
− llm.savings.added.usd
+ llm.savings.cache_effect.usd{direction="saved"}
− llm.savings.cache_effect.usd{direction="added"}
+ llm.savings.interaction.usd{direction="saved"}
− llm.savings.interaction.usd{direction="added"}
```

- A cache effect moves tokens between tiers. It nets zero tokens, so it has no token counter. It does not
  net zero money: a `rescue` moves tokens from the write rate to the read rate.
- `added.usd` contains every `spend`, and a keep-alive ping is a `spend` (design §4.2, rule 5). If you leave out the
  `rescue` effects, the ping is a cost with no saving.
- The `added` side of a `cache_effect` is real cost: a `break`, or the premium of a `markPremium` or a
  `ttlPremium`. Do not floor it at zero.

## Rules

1. **Dry runs.** An operation with `DryRun` set adds nothing.
2. **Model.** Saved and caused tokens are priced with the request's model (and, with a `SizedPricer`, at the size of their side). A `spend` call is priced with its
   own `Call.Model`, because it can differ from the agent's model. If `Call.Model` is empty, the request's
   model is used.
3. **Refused calls.** If the library listed a spend in `Result.Unpriced`, the sink books none of its call usage.
4. **Billing mode** (design §7.1). Token counters always move.
   - `api`: money counters move.
   - `unknown`: no money counter moves, and the request counts as estimated. The dollar may be a
     subscription user's, and design §7.1 keeps it out of every money total.
   - `subscription`: no money counter moves, and the request counts as estimated. An API-equivalent dollar
     never enters a money total.
   - Unset mode: treated as `unknown`.
5. **Cache effects.** An effect that is `InBaseline` is skipped. An effect with `Estimated` set, or priced from a
   fallback rate, marks the request estimated. The camel case kinds map to snake case values. `markPremium`
   becomes `mark_premium`, and `markPayback` becomes `mark_payback`.
6. **Interaction.** The operations already book `InteractionCaused` (the read). The sink books the difference,
   `Interaction` minus `InteractionCaused`, as `direction=saved` when it is positive and `added` when it is negative.
7. **Component label.** The name is the operator without its prefix (`context-guru.mask` gives `mask`). With
   `WithComponents`, any other name becomes `other`. Without it, the first 64 names are kept and the rest become
   `other`.
8. **Cause label.** `model_changed` when the library names it. `ended_change` for a break with no `ChangeID`.
   Otherwise `none`.

## Not in this module

The SDK, the OTLP exporter, the collector URL and the access token. Each host owns them, because they are
settings of the host and not of a pricing library.
