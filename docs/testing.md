# Context operations: counterfactual accounting of cost and savings, test cases

**Status:** proposed.
**Date:** 2026-10-18, revised 2026-11-01.
**This document:** how a correct figure is defined and checked. It holds the exhaustive list of test cases and the tests that validate the implementation against it (§10).
**Specification:** [`spec.md`](spec.md) says what the system must do and why. The requirements (R) are in
[its §3](spec.md#3-requirements). **Design:** [`design.md`](design.md) says how. The decisions (D), the
object model and the library are there.
**Worked examples:** [`examples.md`](examples.md). **Wiring in hosts:** [`cortex-wiring.md`](cortex-wiring.md).

**Numbering.** Section §10 is in this document only. [The map](spec.md#numbering-map) says which document holds each section.

**Index**

- [10. Test cases](#10-test-cases)
  - [10.1 Where the current figures come from](#101-where-the-current-figures-come-from)
  - [10.2 One case](#102-one-case)
  - [10.3 What makes the list exhaustive](#103-what-makes-the-list-exhaustive)
  - [10.4 How the list validates the implementation](#104-how-the-list-validates-the-implementation)
  - [10.5 Other tests](#105-other-tests)

## 10. Test cases

Before the implementation starts, we make an **exhaustive list of test cases**. The inputs come from
the current saving computation and from real traffic. The `expected` values come from the specification and the
design, not from the current code. The list is the specification the implementation must meet. It also
records each place where the new figure differs from the current one on purpose.

### 10.1 Where the current figures come from

| Current rule | Repo | Used for |
|---|---|---|
| `dash.Event.Price`: `ComponentSavedUSD`, `uniqueRate`, `repeatRate`, `baselineDeltaUSD`, `cachesplitSavedUSD`, `keepaliveSavedUSD`, `BilledTokenFactor` | context-guru | every context-guru case |
| `settle`: `pruneDraft`, `compactionDraft`, `rewritePenalty` | Cortex (`feat/contextguru-keepalive`) | tool-prune cases, rewrite-penalty cases |

Cortex's `repeatUsage` is a copy of the proxy's `repeatRate`, so the proxy is the source for the
context-guru reductions. The library imports neither context-guru nor Cortex: the comparison with the Cortex rules
runs in Cortex (§10.4).

### 10.2 One case

Each case is a JSON file in `test/cases/`. The package embeds them and exports `All() []Case`,
so a test in Cortex can run the same list. A case holds only what the specification and the design decide: the input and
the expected result. It does not hold the current code's figure, nor a judgement about it.

`Compute` depends on the state of the thread ([D14](design.md#d14)), so a case is a **sequence of requests** on a
fresh `Trackers`. Each step holds the calls of one request, and the `Result` its `Compute` must
return. A step can also be a standalone `RequestOps` (a ping), priced with `ComputeStandalone`.

```json
{
  "id": "rewrite/applied-again/before-bstar/hit/5m",
  "dims": {"kind": "rewrite", "position": "beforeBStar",
           "cache": "hit", "ttl": "5m"},
  "rates": "fixture-v1",
  "steps": [
    {"begin": {"...": "RequestMeta, client Body, client breakpoints"},
     "add":   [{"...": "the operations reported on this request"}],
     "end":   {"...": "thread, forwarded Body, breakpoints, EntryFacts"},
     "usage": {"...": "Usage"},
     "expected": {"...": "the accountant.Result, tokens for each tier"}},
    {"...": "the next request of the thread"}
  ]
}
```

- `expected` is what `accountant.Compute` must return on that step, by [the specification](spec.md) and [the design](design.md).
- `rates` names a fixed rate table in the package. `fixture-v1` is: input $3.00/M, cache write 5m
  $3.75/M, cache write 1h $6.00/M, cache read $0.30/M. With one table, `expected` and the current
  code's figure can be compared in dollars.

The places where a host's current code is meant to give a different figure are not in this repository.
Each host keeps them in its own file, `differences.json` (context-guru: `dash/testdata/differences.json`),
because they compare the cases with that host's code. Each entry has a case ID, a kind, and a reason:

- `differs`: the figures differ on purpose. The reason cites the spec or design section that causes it.
- `notCovered`: the current code prices something the model does not. The case is listed in the open questions ([§11, spec](spec.md#11-open-questions) and [design](design.md#11-open-questions)).

Known `differs` entries from the specification and the design, for the keep-alive credit ([§4.4](design.md#44-cache-effects)): the current code credits a
1h entry after a 5m gap (`providerCacheTTLMs`, `dash/event.go`). This spec does not. Both agree on a
rescue on OpenAI GPT-5.6+ after the 30-minute minimum.

**Rounding.** The case list and the examples compute in micro-dollars, exactly, and round once, half
up, when a figure is shown. A total adds the exact amounts.

A case that is not in this file must agree with the current code. The current figure is never
stored: the agreement test computes it each time it runs (§10.4).

### 10.3 What makes the list exhaustive

**A. Generated cases.** A generator makes one case for each combination of these dimensions. It
drops the combinations that cannot occur, and states the reason for each drop in its code.

| Dimension | Values |
|---|---|
| Report | first report of the change; applied again from the state ([D14](design.md#d14)); applied again after the state was lost ([question 13](design.md#q13)) |
| Kind | `rewrite` smaller, `rewrite` larger (a restore is one), cache effect (`break`, `rescue`, `split`, `ttlPremium`, `ttlRescue`) |
| Position | before B\*; after B\* and at or before `L`; after `L`; no breakpoint in the body; spans on both sides of B\* |
| Cache state | hot; cold (expired, `Lifetime: exact`); unknown (gap past an unproven `Lifetime: minimum`, [R45](spec.md#r45); first request of a thread; restart); hot, kept by a ping |
| Cache result | full hit; partial hit; miss with write ≥ input; miss with write < input |
| Breakpoints | end of `system` + last message (Claude Code); one after `tools` only; none; one inside the messages before a break |
| TTL of the next breakpoint | 5m, 1h |
| Lifetime | `exact` (Anthropic); `minimum` (OpenAI GPT-5.6+); `unknown` |
| Tokens | request ratio (usage present); fallback ratio (no usage) |
| Billing | `api`, `subscription`, `unknown` ([§7.1](design.md#71-billing-plans)) |
| Model tier | below and above a long-context threshold (the host prices; the library returns the same tokens) |
| Provider rounding | explicit breakpoints; implicit, rounded to 128 tokens ([§5.1](design.md#51-the-counterfactual-boundary-b)) |
| Prefix length | at or above the model's minimum cacheable length; below it (B\* = 0) |

**B. Hand-written cases.** These test how context operations interact, which a cross product cannot do:

1. The worked examples: session `s1` (turn 5 and the turn-7 coref break) and session `s2` (the
   turn-10 keep-alive), with every figure in the examples file.
2. Cache breaks: one for each cause (context operator, client, setting, expired). The setting case also fixes the rate table of the break ([spec Q10](spec.md#q10)). A context operator break gets the `ChangeID` of the context operation that changed the first differing position. Two
   context operators whose changes are both before the break: the break names the one that changed the first
   differing position.
3. Two context operators on the same content ([R25](spec.md#r25)), and a message splice that moves positions (`MessageSplice`).
4. A `DryRun` context operation: priced, shown as "would have saved", and not counted. A reverted change
   that still made a model call: no reduction, and its `spend` context operation counted.
5. Each `Unpriced` reason, and each `CheckFailure`.
6. *(host test, not in this list)* `Saving.Changes` over the limit becomes an `Aggregate` ([`cortex-wiring.md`](cortex-wiring.md), W7.1).
7. `spend` and `setting` context operations, which pass through unpriced.
8. Keep-alive: a window that rescued its request; a window whose pings did not stop the expiry; a
   gap inside the TTL, where the ping is not credited; two threads with their own windows.
9. `cachesplit` and `breakpointTTL`, with the current `cachesplitSavedUSD` as the source.
10. Changes that end: a summary of reduced messages, in both pipeline orders, with the same total;
    an ended change that `Compute` no longer applies; a restore of the summary, after which the earlier
    changes must be applied again from the state. The end rule is decided ([§11, question 12](design.md#q12)); the case
    for a lost state waits for [question 13](design.md#q13).
11. A subagent thread: its context operations, its totals, and a session total that adds both threads.
12. The interaction on a rescued request, and the conservation check on every case.
13. A `coref` batch: three cuts in messages that are not next to each other, as one context operation with
    three spans, one `ChangeID` and one attributed `break` effect; its payback across later turns.
14. `extract_llm_sweep`: a prefix ask that reads the previous turn's cache, and a fallback ask that
    pays fresh input. Both are `spend` context operations; their `Call.Usage` differs.
15. `breakpointTTL`: a 1h head written and read after 20 minutes (both legs); a 1h head written and
    never read (cost leg only, negative total); the measured blanket-versus-gated case from
    `apply/headttl.go`.
16. `readlifecycle` and `skeleton`: the same decision with different replacement bytes on two turns
    keeps one `ChangeID` ([§4.2](design.md#42-rules) rule 6).
17. Interleaved threads: a main thread whose previous request is five requests back, behind four
    subagent requests, still compares with its own previous request ([`cortex-wiring.md`](cortex-wiring.md), W5.2).
18. *(host test, not in this list)* Keep-alive conversion: a `KeepAliveReport` from each host's emitter gives the same `spend`
    context operation.
19. A normal turn that only appends messages: no break ([§5.2](design.md#52-classifying-a-break), the diff is after `prevExtent`).
20. A client edit at `messages[5]` with breakpoints at the end of `system` and on the last message:
    B\* snaps to the end of `system` ([§5.1](design.md#51-the-counterfactual-boundary-b)).
21. A summary on turn N, then `mask` applied again on turn N+1 at a position that the summary's
    splice moved ([§4.3](design.md#43-why-positions-are-section-index)).
22. Observe mode: a `DryRun` change shown on every request; then the switch to enforce, with its
    break ([§4.6](design.md#46-replays-who-remembers-past-changes)).
23. The same tool output twice in one session: two `ChangeID`s ([§4.2](design.md#42-rules) rule 6).
24. A ping on a `minimum` provider past its minimum: a rescue ([§4.4](design.md#44-cache-effects)). A ping on a 1h entry after 9 minutes: no rescue.
25. A subagent's first request that reads the main thread's `tools` and `system`: `hot` from the
    usage, not `unknown` ([§5.2](design.md#52-classifying-a-break)).

26. A request that fails before `Compute`: the reported change is still active on the next request ([§4.6](design.md#46-replays-who-remembers-past-changes)). **Not covered in v0:** `EndRequest` does not store the change, so the host must call `Compute` for every request that ended. No test pins this case.
27. Two requests of one thread in flight: the second `Compute` sets `Recomputed` ([R53](spec.md#r53)); `Preview` returns the figure `Compute` returns and stores nothing ([R26](spec.md#r26)).
28. Two tenants with one session ID: the host's key prefix keeps the `ThreadState`s apart ([§6.1](design.md#61-call)).

**C. Cases from real traffic.** For each difference that a shadow step ([`cortex-wiring.md`](cortex-wiring.md), migration steps 5 and 8) logs and
no case matches, add a case. Then decide: it is a bug in `accountant`, or a new `differs` entry with a
reason.

### 10.4 How the list validates the implementation

| Test | Where | Passes when |
|---|---|---|
| `accountant` unit test | context-guru | `accountant.Compute` returns `expected` for every case, including the expected `Boundary`, `Phase` and break for every cache case ([§5.2](design.md#52-classifying-a-break)). The internal comparison has its own unit tests. |
| Agreement with the current code | context-guru | The test runs `dash.Event.Price` on each case's input. Each case not in `differences.json` equals `expected` within one micro-dollar. Each `differs` entry really differs, so a stale entry fails. |
| Agreement with the Cortex rules | Cortex, until step 10 | The same test with `pruneDraft`, `compactionDraft` and `rewritePenalty`, for the tool-prune and rewrite-penalty cases. It is removed with those rules. |
| Review of the differences | PR review | Every `differs` entry cites a spec or design section, and a reviewer approves `differences.json`. |
| Cortex contract test | Cortex | Cortex builds the same inputs through its own plugins and its Tracker, and gets `expected`. A type change in the library fails the Cortex build on upgrade. |
| Shadow steps | both | Every logged difference matches a `differs` entry. |
| Removal gate | both | No rule is removed while a `notCovered` entry uses it. |

### 10.5 Other tests

1. **Property tests in `accountant`:** `Saved` and `Caused` are never negative; an empty list gives an
   empty result; reordering context operations of different context operators does not change each one's price,
   except for break attribution.
2. **End-to-end:** the LiteLLM integration test (`06c712cb` on `feat/contextguru-keepalive`) runs a request
   through the laptop Cortex. The comparison of Cortex, the context-guru proxy and a control arm is
   `62333ee9` on the same branch. The test checks that the reported saving equals the change in billed
   cost against the control arm, within a tolerance.
