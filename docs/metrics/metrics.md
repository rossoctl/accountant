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
2. **Model.** Saved and caused tokens are priced with the request's model. A `spend` call is priced with its
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
