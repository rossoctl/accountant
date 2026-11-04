# Context operations: counterfactual accounting of cost and savings, specification

**Status:** proposed.
**Date:** 2026-10-18, revised 2026-11-01.
**This document:** what the system must do and why. It holds the problem, the goal, the requirements (R),
what is out of scope, and the open questions about behavior.
**Design:** [`design.md`](design.md) says how. It holds the decisions (D), the object model, the comparison
of requests, the library, billing plans, errors, and the open questions about the implementation.
**Test cases:** [`testing.md`](testing.md) holds §10. They define a correct figure.
**Worked examples:** [`examples.md`](examples.md).
Start with the basic example. Advanced examples follow in the same file.
**Wiring in hosts:** how Cortex and the context-guru proxy connect to this model is in a separate
document, [`cortex-wiring.md`](cortex-wiring.md).

<a id="numbering-map"></a>**Numbering.** The two documents share one numbering. A section, requirement (R), decision (D) or open
question has the same number wherever it is cited, so each number is in exactly one document and the
numbers in a document are not consecutive. A reference to the other document is a link. The map:

| Section | Title | Where |
|---|---|---|
| §0 | The problem | [spec.md](spec.md#0-the-problem) |
| §1 | Goal | [spec.md](spec.md#1-goal) |
| §2 | Decisions | [design.md](design.md#2-decisions) |
| §3 | Requirements | [spec.md](spec.md#3-requirements) |
| §4 | The object model | [design.md](design.md#4-the-object-model) |
| §5 | Comparing requests | [design.md](design.md#5-comparing-requests) |
| §6 | The library | [design.md](design.md#6-the-library) |
| §7 | Billing plans | [design.md](design.md#7-billing-plans) |
| §8 | Errors | [design.md](design.md#8-errors) |
| §9 | Out of scope | [spec.md](spec.md#9-out-of-scope) |
| §10 | Test cases | [testing.md](testing.md#10-test-cases) |
| §11 | Open questions | questions 1, 2, 9, 10 in [spec.md](spec.md#11-open-questions); 3 to 8 and 11 to 15 in [design.md](design.md#11-open-questions) |

`R` numbers are in §3 of this document. `D` numbers are in §2 of [`design.md`](design.md#2-decisions).

**Index**

- [0. The problem](#0-the-problem)
  - [The selected solution](#the-selected-solution)
  - [What changes the cached prefix](#what-changes-the-cached-prefix)
- [1. Goal](#1-goal)
- [3. Requirements](#3-requirements)
  - [A. Boundary](#a-boundary)
  - [B. Coverage](#b-coverage)
  - [C. Facts for pricing](#c-facts-for-pricing)
  - [D. Status and correctness](#d-status-and-correctness)
  - [E. Operability](#e-operability)
  - [F. Billing plans](#f-billing-plans)
- [9. Out of scope](#9-out-of-scope)
  - [9.1 What this design does not replace](#91-what-this-design-does-not-replace)
- [11. Open questions](#11-open-questions)

## Overview

**In one paragraph.** When a context operator shrinks the context sent to a model, we want to say how much money
that saved. Today every context operator answers on its own, in its own way, so the answers disagree and nobody
can check them. This design gives every context operator one small record of what it changed, and one shared
calculation that turns those records into tokens and money.

**The story.**

1. **A change is recorded as a fact.** A context operator that shortens a tool output says: "I took 7,000 tokens
   out of message 9." It says nothing about money. We call this record a **context operation**.
2. **The same tokens cost different amounts on different days.** A provider keeps the start of your
   prompt in a cache. Reading from the cache is cheap (about a tenth of the normal input price). Writing
   is expensive (more than the normal input price). So a token costs very different amounts
   depending on whether the cache is warm or cold.
3. **So a saving needs a "what if".** The saving is the bill we would have paid without the change,
   minus the bill we paid. To find the first bill we ask: where would these tokens have been billed
   if nobody had touched them? That is the **counterfactual**.
4. **One saving is spread over many requests.** The 7,000 tokens stay out of every later request.
   The value changes with each request, because the cache is warm on one and cold on another. We
   add them up over the **session**.
5. **One library does the calculation.** It takes context operations and the cache facts, and returns tokens
   for each billing class. Each host turns tokens into money with its own price list. The result is
   the same tokens for the same request, wherever it is computed.
6. **The library is shared.** It is a shared library for context-guru and Cortex, and we hope
   other AI frameworks use it too. It does not depend on context-guru or on Cortex. Anyone who can report
   context operations and the cache facts can get the same tokens.

**Some changes cost money first.** Not every context operation removes tokens. Some make a model call, and
that call is a cost. We record it as a **spend** context operation. It carries the tokens the provider
billed for the call.

- **Summarization.** A summarizer reads a long part of the context and writes a short summary. The
  call costs money (the spend). The summary then replaces the long part in every later request (the
  saving). The spend carries the `ChangeID` of the change it created, so the cost and the saving are added together.
  The summary pays back only if the saving is larger than the call.
- **Keep-alive.** A keep-alive sends a small request while the user is idle, so the provider does
  not drop the cache. The request costs money (the spend). Its value shows later, on the next real
  request, which reads a warm cache and not a cold one. The spend and the later rescue share one
  ID, so they are added together. If the user never comes back, the spend has no rescue, and the
  total for that keep-alive is negative.

In both cases the cost and the benefit land on different requests. This is why a spend is a
separate kind of context operation, and why we add up over the session.

**The packages.** Context operators report context operations to a host. Cortex is the host today. The
context operators and the host import the `accountant` library. The library imports none of them (R3).

![The accountant library, Cortex as its consumer, and the context operators that report context operations](packages.svg)

**Read next.** The basic example in
[`examples.md`](examples.md) shows the idea
with three requests and one change. The rest of this document is the specification: the problem (§0), the goal (§1),
the requirements (§3), what is out of scope (§9), the test cases ([§10](testing.md#10-test-cases)) and the open questions about behavior (§11).
The decisions ([§2](design.md#2-decisions)), the model ([§4](design.md#4-the-object-model)), the calculation ([§5](design.md#5-comparing-requests), [§6](design.md#6-the-library)), billing plans ([§7](design.md#7-billing-plans)) and errors ([§8](design.md#8-errors)) are in
[`design.md`](design.md).

<a id="words"></a>**Words used here.**

| Word | Meaning |
|---|---|
| Context operator | Anything that changes the context sent to a model or the cache around it: a summarizer, an output filter, a tool pruner, a keep-alive. |
| Context operation | A record of one change a context operator made to the context. It has sizes and a position, and no money. |
| Tier | The rate class a provider bills a token at: input, cache write (5m or 1h), or cache read. |
| Cache state | Hot (the provider still holds the start of the prompt), cold (it does not), or unknown. |
| Counterfactual | The bill we would have paid if the change had not been made. |
| Spend | A context operation that is a model call (a summarizer call, a keep-alive request). It carries the tokens the provider billed. It is a cost, and its benefit may come later. |
| ChangeID | The ID of one change. It is the same on every request that carries the change, and a break or a spend that belongs to it carries it too. |
| B\* | The last position the provider would have read from its cache on this request, if no context operator had changed anything ([§5.1](design.md#51-the-counterfactual-boundary-b)). |
| Breakpoint, `L` | A `cache_control` mark in the body. The provider caches up to a breakpoint. `L` is the last one in the body ([§6.2](design.md#62-tier-rule)). |
| Break | A cache change from hot to cold. The provider no longer reads content it read before. |
| Rescue | A cache change from cold to hot. A keep-alive kept the entry alive, so the next request reads it. |
| Host | Cortex or the context-guru proxy: the program that runs the pipeline, calls the library and turns tokens into money. |
| Tracker | A class of the library. It keeps the state of one thread, in memory or persisted: the context operations that are still active. A context operation is reported once, on the first request of its change. On each later request `Compute` applies the active ones again, with the same context operator, until they end. What else the state holds is open ([§11, question 11](design.md#q11)). The host's pipeline adds each reported context operation to it ([wiring W5](cortex-wiring.md#w5-the-tracker-and-the-host)). |
| Keeper | The context-guru component that sends keep-alive pings between turns. |
| Thread | One conversation inside a session: the main one, or a subagent's. |
| Session | All the requests of one working session. The period over which we add up. |

## 0. The problem

Context engineering changes what is sent to a model. Examples are summarization, tool-output
reduction, tool pruning, cache splitting and keep-alive. Each change has a cost and a saving.
Today each component computes its own cost and savings.

This causes three problems:

1. **The rules are duplicated.** Each component has its own pricing rule. Example: `extract_llm`
   tail and `cache_aware_summarization` both reduce context, but each prices its own saving in its
   own way. A rule that is wrong in one place is not found by checking another place.
2. **The figures cannot be compared.** Two components can count the same tokens in different ways.
   Nobody can verify one figure against another.
3. **A change to one rule is a change to every component.**

Two things make a correct calculation hard:

- **Aggregation over a period.** A saving is not one event. A change removes tokens from every
  later request that still carries it. To total it, we need one definition of the period. In this
  design the period is the **session**. A thread is a breakdown of a session.
- **Counterfactuals.** A saving is the difference between what happened and what would have
  happened without the change. We must compute the alternative bill. It depends on the cache
  state, and a change can alter the cache state itself.

This document defines one model for the context operations, and one way to compute their cost and savings.

### The selected solution

The [overview](#overview) tells the story. In short:

- Context operators report **facts** (sizes and positions, no money).
- One standalone library turns them into **tokens for each tier**. `Compute` is deterministic and is the
  only place that decides which tier a token falls in.
- Each host keeps its own **rates** and turns tokens into money.
- Totals are **sums over a session**. A list of test cases defines correct ([§10](testing.md#10-test-cases)).

### What changes the cached prefix

A provider reuses a cache entry only when the rendered prefix of a new request is the same as the
prefix of the entry, up to a cache breakpoint. A change before the breakpoint breaks the match from
the first changed token onward. The change can be in the content, or in a setting that the provider
renders into the prefix. The model keeps these changes as a `setting` context operation (R50) when a
context operator makes them, and as a client edit when the client makes them ([§5.2](design.md#52-classifying-a-break)).

The settings are different for each provider. The list is in [`cached-prefix.md`](cached-prefix.md).
It covers OpenAI and Anthropic so far, with the provider facts that the model depends on (lifetime,
write price, reporting, granularity). It grows as each provider is read.

## 1. Goal

- Each context operator reports the **context operations** it performed on the context, as **facts**.
- **One library**, standalone and usable by any AI framework, turns context operations into **tokens for each billing tier**.
  A **tier** is the rate class a provider bills a token at: input, cache write (5m or 1h), or cache
  read.
- **Each host** converts tokens to dollars with its own rates, and records them. There are two
  hosts: **Cortex** (`pricing.Resolver`) and the **context-guru proxy** (`internal/modelinfo`).
  For the same request, both get the same tokens for each tier.
- **What is centralized is the logic, not the rates.** The `accountant` library decides which tier every
  token falls in, in one place. The dollar rate for each tier stays in each host's own rate table,
  by design: Cortex has discounts, overrides and long-context tiers that the proxy does not. So the
  two hosts report the same tokens and can report different dollars for the same request, when
  their tables differ. Equal dollars are a property of the test cases, which use one fixed table
  ([§10.2](testing.md#102-one-case)), not of production.
- An **exhaustive list of test cases**, made with the current saving computation, validates the
  implementation before any host uses it ([§10](testing.md#10-test-cases)).
- The model supports savings **across requests**: removing K tokens from message *i* saves on
  every later request that still carries the removal.
- context-guru is **one plugin in a longer pipeline**. Other plugins (tool-prune, cpex,
  inference-router) also change the request. The model covers all of them.
- Every figure uses the **cache state** of the request: hot, cold, or kept hot by a keep-alive.
- The model covers the context operations that change the **cache state**, not only the content: keep-alive
  pings, `cachesplit`, and the TTL of a breakpoint.
- A later context operation can **end** the effect of earlier ones. Example: a summary replaces messages
  that `mask` had already reduced.
- Every figure can be summed **for each session and for each thread**, so a subagent's saving is
  separate from the main thread's.
- The model serves **API users and subscription users**. An API user pays for each token, so a
  saving is money. A subscription user pays a fixed price and has a usage limit, so a saving is
  usage-limit headroom. The context operations and the library are the same for both; only the conversion
  to a value differs ([§7.1](design.md#71-billing-plans)).

## 3. Requirements

The numbers are the order in which the requirements were found, so they are not in sequence. Some
numbers are not used: those requirements were dropped.

### A. Boundary

- <a id="r1"></a>**R1.** Context operations are facts. They carry no rates, no dollars and no tier choice. The one
  exception is a dollar cost that a call's own response reported (R49).
- <a id="r2"></a>**R2.** Context operations carry no content: no message text, no clipped before/after strings, no
  credentials. They reach the unauthenticated `:9094` API and the session archive. Content identity
  uses hashes only.
- <a id="r3"></a>**R3.** The model and the library are in one module. Both repos import it. The module must not
  import either host or any framework, so a third party can use it. Its public types are named for
  the model, not for context-guru or Cortex. A contract test in
  each repo pins it.
- <a id="r26"></a>**R26.** `Compute` is deterministic: its result depends only on its record, on the
  `ThreadState` that the record started from and, for a fork's first request, on its parent's `ThreadState` at the
  call, and it does no I/O. The one exception is a warning through
  `log/slog` for each failed check ([§6.5](design.md#65-checks)); it does not change the figure. The Tracker holds the `ThreadState`.
  As its last step `Compute` replaces it with the new one; a failure leaves the old one ([D14](design.md#d14)). The same
  `ThreadState` (and parent state) and the same request give the same figure in the context-guru proxy and in Cortex.
  One input is read from another thread: a fork's first request reads its parent's `ThreadState` as it is at the
  call ([§5.2](design.md#52-classifying-a-break)), so `Preview` and `Compute` can differ if the parent moved in between.
  `Preview` gives the same figure when the parent state did not move, and stores nothing.

### B. Coverage

- <a id="r4"></a>**R4.** Every context operation that changes money is covered, not only reductions: reductions,
  additions, cache breaks, model calls, restores.
- <a id="r49"></a>**R49.** A `spend` context operation names the model the call used. It can differ from the agent's model.
  If the call's response states a dollar cost, the context operation carries it as reported, inside the
  call's usage, beside the tokens. The host prices the tokens with its own rates for that model. The reported cost is kept as a
  fact and is not used in pricing (wiring document). Tokens stay the base fact, so the library still returns the
  same tokens in every host.
- <a id="r5"></a>**R5.** Spend that happens outside a request (a keep-alive ping, a detached summarizer call)
  carries its own attribution. It uses the same context operations and the same recording path as spend
  inside a request (wiring document).
- <a id="r47"></a>**R47.** A model call that any plugin makes outside the listener is a `spend` context operation, not
  only context-guru's. Example: the `ibac` judge calls a model through `core/llmclient`, so the call
  is not in the ledger today. It is reported the same way as the summarizer; the host wraps the plugin's model client so that the call's billed usage is read from the response (wiring W4.6). Its cost is not
  joined to a saving, because it creates none: its `ChangeID` is empty.
- <a id="r6"></a>**R6.** A context operation has an ID that is unique inside its request. Results and check failures refer to it.
  A reference to a change in another request, or from a break to its cause, uses `ChangeID` (R22).
- <a id="r27"></a>**R27.** The model is not specific to one context operator. Each context operation names its context operator. The
  context operations are in pipeline order, the order they ran.
- <a id="r33"></a>**R33.** A `rewrite` that makes the context larger is a cost: an injected tool declaration, injected text, a
  restore.
- <a id="r34"></a>**R34.** A change of a request setting is a separate kind, `setting`: the model, the host, or any
  param. Example: a session moves from one model to another, or from low to high effort. It is
  recorded as a fact, because it explains later figures. One context operation is one decision and holds
  any number of settings, as a map from the setting name to its `from` and `to` values. Example: a
  router moves a request to a smaller model and a lower effort in one context operation. A setting that
  appears or is removed has a nil `from` or `to`. Settings changed for different reasons, or by
  different context operators, are separate context operations. The saving or cost of the change itself is **not computed** (R50).
- <a id="r50"></a>**R50.** The library does not price a param change. Providers do not price `effort` or `thinking`
  per token. These params change how many output tokens the model writes, and sometimes the cache.
  The saving cannot be computed from facts, for four reasons:
  1. *The alternative is not observable.* The tokens the model would have written at the other
     setting were never generated.
  2. *The effect is not fixed.* It depends on the task, the model and the turn.
  3. *Quality can change.* A lower setting can need more turns, so a saving on one request can be a
     loss over the session.
  4. *The cache can break.* On both Anthropic and OpenAI, a change can invalidate cached content.

  What the library does instead: it keeps the `setting` context operation; it records a break (a cache effect) with
  the `setting` context operation's `ChangeID` when a request after the change is cold ([§5.2](design.md#52-classifying-a-break)), which is measurable; and a report
  can show output tokens per request before and after, labelled as an observation, never in
  `TotalAvoidedUSD`. A measured saving needs a control (a holdout that keeps the old setting). That
  is out of scope (§9).

### C. Facts for pricing

- <a id="r7"></a>**R7.** Each context operation has a position: section and index, in the provider's hash order.
- <a id="r8"></a>**R8.** Sizes are in bytes. The library converts bytes to tokens and marks the result `Estimated`
  ([§4.2](design.md#42-rules) rule 7). A host that knows the tokens of a range may send them in `Size.Tokens`. The
  library then uses them and does not mark that size `Estimated`. Base64 image and document bytes are not in the
  conversion ratio.
- <a id="r11"></a>**R11.** Model-call usage is the provider's billed usage, read from the call's own response by
  the model client the host gives the engine. The engine does not count it (wiring document).
- <a id="r22"></a>**R22.** A change has an ID that is the same on every request that carries it (`ChangeID`).
- <a id="r23"></a>**R23.** A one-time cost (a cache break) is linked to the change that caused it, so payback can
  be computed.
- <a id="r24"></a>**R24.** Cache facts are scoped to the conversation the provider cached, not to the session as a
  whole. Subagents share a session but not a cache entry.
- <a id="r29"></a>**R29.** Each context operator's change is measured against that context operator's own input.
- <a id="r30"></a>**R30.** Cache facts come from the forwarded body, not from one plugin's output.
- <a id="r31"></a>**R31.** A cache break with a cause outside the plugins (client edit, expiry, first turn) is not
  charged to a plugin.
- <a id="r32"></a>**R32.** Positions resolve in the forwarded body. A context operator that adds or removes messages
  reports that.
- <a id="r36"></a>**R36.** Every price uses the request's cache state: hot (the previous entry was read, or a keep-alive kept it), cold
  (expired), or unknown (the first request of a thread, or a gap past an unproven minimum TTL). The same change
  has a different value in each state.
- <a id="r37"></a>**R37.** Cache breakpoints are an input. The provider writes and reads the cache only at a
  breakpoint, so the tier of a position and the size of a cache-break penalty depend on where the
  breakpoints are, and on their TTL.
- <a id="r38"></a>**R38.** Context operations that change the cache state are covered: a keep-alive ping, `cachesplit`, and
  a breakpoint TTL change. Each one is priced as the difference between two states.
- <a id="r45"></a>**R45.** The cache state distinguishes a proven expiry from an unproven floor. A provider whose
  TTL is only a guaranteed minimum (no documented maximum) never scores `cold` from elapsed time
  alone — that would invent a saving the provider may not have billed. See [§5.2](design.md#52-classifying-a-break).
  The clock uses the TTL the entry was written with, for each breakpoint of the previous request, and not the
  TTL of the current request (accountant#40, #41). A billed `CacheRead` that covers a part of the entry proves
  it was alive, and beats the clock at any gap.

### D. Status and correctness

- <a id="r12"></a>**R12.** A context operation is applied or a `DryRun`. A `DryRun` is a change that a component
  computed but did not send: the original body went upstream. Example: a component in trial mode
  finds that it can cut 3,000 tokens from a tool output. The request still carries all 3,000 tokens.
  The context operation records the 3,000 as "would have saved". Only applied context operations count in totals. A
  change that is not in the forwarded body (reverted by the pipeline) is not reported.
- <a id="r15"></a>**R15.** A request is priced from its input and from the state of its thread. A request is **not**
  complete without history: the active context operations of the thread are part of the input. If the
  state is missing (a restart, an eviction), the library does not guess. A change it was never told
  about is not applied, so its saving is missing, and the sum check ([§6.5](design.md#65-checks), check 1) fails on that
  request. Prices that depend on the state say so ([§4.6](design.md#46-replays-who-remembers-past-changes)).
- <a id="r25"></a>**R25.** When two context operators change the same content on one request, each one is credited for
  its own change, in pipeline order.
- <a id="r51"></a>**R51.** The saving or cost of a request can be attributed to each context operator. The result
  gives tokens per tier for each context operator (R27, R29), and the sum over the context operators and
  the items attributed to nobody (a cache break with no plugin cause, R31, and the interaction, [§6.2](design.md#62-tier-rule))
  equals the request total. A context operation that is applied again on a later request keeps its
  context operator, so its saving stays with the same plugin.
- <a id="r53"></a>**R53.** Two requests of one thread can run at the same time (a retry, parallel calls of one agent).
  A subagent is not one of them: it is its own thread, with its own Tracker. A keep-alive ping and a
  detached call are not either: each is a standalone `RequestOps`, which reads and writes no
  `ThreadState` ([§6.1](design.md#61-call)).
  The Tracker is safe for concurrent use. No request loses the changes of another. A `Compute` that
  finds that the `ThreadState` changed since its `EndRequest` calculates again from the current
  `ThreadState` and says so in the `Result` (`Recomputed`). The order of the results is the order of the
  `Compute` calls ([D14](design.md#d14), [§6.1](design.md#61-call)).
  **A reported change is part of the thread's state from `EndRequest`** ([§4.6](design.md#46-replays-who-remembers-past-changes)).
  So a request that ended first and reported a different change can show up as an active change in the
  `Compute` of a request that is still running: that `Compute` sets `Recomputed` and prices both. When
  the two changes touch the same content, the sum check fails for that `Compute`. A retry that reports
  the same `ChangeID` does not do this.
- <a id="r52"></a>**R52.** The library never returns one token total. It returns tokens for each tier. Tiers differ in
  price by up to 20 times (a 1h cache write against a cache read), so a sum across them is not an invariant. A host that wants one figure
  prices the tiers and sums dollars. This is the rule of the tool-prune accounting (cortex#952).
- <a id="r54"></a>**R54.** A rescue credits only what the ping kept alive. Another thread of the same session can keep
  a shared prefix warm: subagents start with the same `system` and `tools`, and each request of one
  refreshes that entry for the others. The host reports the longest prefix of the request that another thread
  read or wrote inside the TTL before it, as `EntryFacts.SharedWarm`. The rescue leaves that prefix out. When
  nothing is left, there is no rescue. The request is still `hot`: the entry was alive. The library does not
  look at other threads, except that a fork's first request reads its parent's state (R26). The host finds the prefix from its own record of each thread's last request (metadata only:
  times, extents and hashes). A `SharedWarm` that is not in the forwarded body fails the `shared_warm` check and
  credits no rescue. R54 covers the keep-alive `rescue` only, not the `ttlRescue` of a head held at 1h (a known gap).
- <a id="r35"></a>**R35.** A later change can end earlier ones (a summary of reduced messages). The model shows
  this by `Compute` no longer applying the earlier change, not by a link and not by the context
  operator: after its first report the context operator says nothing about the change. `Compute`
  infers the end ([§11, question 12](design.md#q12)). The totals of the earlier changes until then do not
  change ([§4.5](design.md#45-changes-that-end)).

### E. Operability

- <a id="r17"></a>**R17.** Each context operation names its context operator in one field, as a dotted path: `context-guru.mask`,
  `tool-prune`. The first part is the product or plugin, and the framework sets it, so a plugin
  cannot report under another plugin's name. A later part, if any, is the context operation type. The
  taxonomy of a context operator (what kind of change it makes, and what made it decide) depends on the
  name only, so a report maps the name to it with a table in cg. The context operation does not carry it.
  Example: `context-guru.mask` truncates old tool output; it decides by age and size. Parts must
  not contain a `.`. The allow-list and the per-operator breakdown match on the first part.
- <a id="r18"></a>**R18.** Each request carries request ID, session, thread (if known), **model** and
  **params**. The model, and the parameters that change what a call costs, are facts of the
  request. Examples of params: `effort`, `thinking` (the thinking budget), `max_output`,
  `service_tier`. The library does not fix the list. A key it does not know is kept and passed on,
  and it is never dropped (R19).
- <a id="r46"></a>**R46.** Each recorded saving names the version of the `accountant` rules that computed it
  (`Saving.OpsVersion`). A context operator can then trace a figure to the rules and the case list that
  define it. A change to the rules changes the version, so figures from two versions are not
  compared as if they were the same.
- <a id="r39"></a>**R39.** Every figure can be summed for one session, for one thread, or for one subagent. The
  session and the thread are on the request (`RequestOps`); the recorded form carries them (wiring
  document).
- <a id="r19"></a>**R19.** The model is versioned. An unknown `Kind` is kept and reported as unpriced, never
  dropped.
- <a id="r20"></a>**R20.** `RequestOps`/`Operation` is request-scoped and in-process only: input to `accountant.Compute`,
  discarded once the request's `event.Saving` is written. The context operation list is never recorded
  (R2's `:9094` and archive concern, and the session store's own size history, apply to what IS
  recorded — `event.Saving`, `Saving.Changes`/`ChangeSaving` and `Event.Spends` — not to the raw
  context operation list). A `spend` is recorded as one `SpendRecord` (wiring document, W7.1), so a total
  can add it. The recorded summary has a size limit, with aggregation above it (wiring document).
- <a id="r21"></a>**R21.** Second-order effects are not context operations. Example: the agent compacts later because its
  requests are smaller.

### F. Billing plans

- <a id="r40"></a>**R40.** Context operations and the library are independent of the billing plan. They carry tokens and
  tiers only. The plan is a fact on the request, not a field of any context operation's price.
- <a id="r41"></a>**R41.** Each request names its billing: `api` (pay for each token) or `subscription` (fixed price
  with a usage limit).
- <a id="r42"></a>**R42.** A `spend` context operation inside a request is priced with the billing of its request.
  A standalone `RequestOps` (a ping, a detached call) carries its own `Billing`. The limit of this rule is in
  §9, known limitation.
- <a id="r43"></a>**R43.** A subscription saving is never reported as money saved. It is reported as usage-limit
  headroom, and an API-equivalent dollar figure can be shown beside it, labelled as an equivalent.

## 9. Out of scope

- Signed totals in the ledger.
- Pricing of `setting`: the saving or cost of a model or param change (R50). The cache break it
  causes is priced; the change in output tokens is shown as an observation, not as a saving.
- A measured saving of a param change. It needs a control that keeps the old setting.
- Writing the state to disk. The host saves and loads it with `Trackers.Snapshot` and `Restore`; the library does no I/O.
- A change that no context operator in the pipeline made, such as the client's own compaction. It is not
  recorded as a context operation. A client edit that breaks the cache appears as a `break` cache effect
  with no `ChangeID` ([§5.2](design.md#52-classifying-a-break)).
- **Known limitation: the payer of a call is the payer of its request.** A `spend` context operation has
  no payer of its own. It is priced with the `Billing` of its request. If a context operator calls a model
  on another credential (for example a summarizer on the deployer's API key, in a session on the
  user's subscription), the call is priced as the user's. A standalone `RequestOps`, such as a
  ping, carries the `Billing` of the credential it used.

### 9.1 What this design does not replace

`Operation`/`RequestOps` carries money facts only — tokens, tiers, the IDs needed to join and
price them. The engine and the proxy's dashboard also need facts an `Operation` deliberately does
not carry (R2: no content; no names): tool/MCP names, invocation signals, declaration ownership,
replay text for a holdout, per-ping strategy metadata, per-component decline counters. None of
that is rebuilt from `Operation`. This design changes nothing about where it lives:

- **Component `Gate`/`Event` counters** (`Report.Gates`, `Report.Events`) — the deployer's answer
  to "is this component declining, or just absent on this traffic" — are unaffected. They are not
  money, carry no `ChangeID`, and have no place in `Operation`; they keep rolling up into `/stats`
  the way they do today. Migration step 10 removes the pricing code this design replaces
  (`saving.go`, `unique.go`, the per-operator draft functions); it removes no gate/event path.
- **Tool/MCP advisory surfaces** (`dash/toolinventory.go`, `toolsuggest.go`, `toolremoval.go`,
  `adapttools.go`, `declcredit.go`) read declaration **names**, per-call invocation signals, and
  the fact that a declaration lives in `tools`. `tp:decl-set-7f3a`'s context operation (example 3 in the examples document) reports "3 declarations, 8,000 tokens," never which ones — these surfaces keep reading the
  engine's own declaration-scan data, not `Operation`.
- **Holdout/control-arm replay** (`dash/kvcachesuggest.go`, `kvcacheholdout.go`,
  `adaptcompaction.go`) needs the pre/post **text** at a message index to re-run a component under
  a different config. `Operation` carries no text (R2). These keep their own stored-text path;
  they are not driven by `ChangeID`/`Size`.
- **Keep-alive strategy attribution** (`dash/campaignsavings.go`) groups by strategy id, preset,
  stop reason — fields `components.KeepAliveReport` already carries (the wiring document keeps it as the
  keeper's report path) but a bare `spend` context operation does not. The dashboard columns these feed keep
  reading `KeepAliveReport`, not the `spend` context operation's `Call`.
- **The headline money total** (`dash/savings.go`'s `baseline − cost − cgllm + cachesplit + kaNet`)
  IS reconstructable from `event.Saving`/`ChangeSaving`, because the recorded form keeps the cachesplit,
  keep-alive and spend terms as separate, summable fields. Only the advisory/diagnostic detail
  above is out of this design's reach.

If a later design wants to drive any of the surfaces above from `accountant`, that is new scope, not an
extension of this one.

## 11. Open questions

These are the open questions about behavior: what a figure means. The open questions about how the
library does it ([questions 3 to 8](design.md#q3) and 11 to 15) are in [`design.md` §11](design.md#11-open-questions).
Where a question has test cases, a host that compares them with its own code marks them `differs` or `notCovered` until it is decided ([§10.3](testing.md#103-what-makes-the-list-exhaustive)).

- <a id="q1"></a>**Q1. The interaction on a rescued request ([§6.2](design.md#62-tier-rule)).** It is attributed to nobody. An alternative is
   to give it to the keep-alive window, because the window caused the state. That would make the
   keep-alive figure larger on every request with large reductions. A decision is needed before the
   test cases are frozen. v0 reports the interaction only for a saved span at or before the ping's
   extent, booked at the read tier ([design §6.2](design.md#62-tier-rule)). Other cases add zero, the safe direction (R45).

- <a id="q2"></a>**Q2. Usage-limit weights.** The provider publishes no weights for each tier, so the defaults ([§7.1](design.md#71-billing-plans))
   are the API rate ratios, an estimate. It is not verified whether a subscription response carries
   usage-limit headers that a host could use to measure the weights. If it does, a calibration
   could replace the defaults for each plan.

- <a id="q9"></a>**Q9. The payer of a call.** Open: whether a `spend` should carry its own payer. The limitation as it stands is in §9.

- <a id="q10"></a>**Q10. The rates of a setting break.** A break caused by a `setting` change (a new model) needs the
    rates of one model. The proposed rule, used by example 5a and its case: price the moved tokens at
    the rates of the model that serves the request after the change, so both sides of the break use
    one table and the figure is the cache effect alone. The alternative is the model before the change.
    Not decided.
