# The events module: one structural record for each request and each ping

Status: design, not implemented. Epic: [rossoctl/accountant#85](https://github.com/rossoctl/accountant/issues/85).

`accountant/events` turns each request and each keep-alive ping that a proxy handles into one
**structural event**. Each proxy exports its events to **one central collector**. Researchers read the
collected events to study usage patterns, find bottlenecks, and compare caching strategies.

It is a nested Go module, like [`metrics`](metrics.md). The root module keeps importing only the standard
library (R3). The module imports the OpenTelemetry **logs** API only. The SDK, the exporter and the
collector address belong to the host.

## 1. Intent

- Researchers need per-request facts from real traffic: sizes, timings, token tiers, cache behaviour, and
  what context-guru and the keeper did. Counters (`metrics`) cannot answer questions about order, gaps or
  one thread's history. Events can.
- Two hosts produce the same kind of traffic: Cortex and the context-guru proxy. One schema, built in one
  module, keeps their events comparable.
- The data leaves the user's machine. So the events carry **structure only**: no text, no names that a
  user wrote, and no identifier in clear.

## 2. Goals, non-goals, anti-goals

Goals:

1. One event for each inference request, and one for each keep-alive ping decision (sent, refused, failed).
2. The same schema from Cortex and from the context-guru proxy.
3. A researcher can find every gap in the data: a lost event, a restart, a dropped batch.
4. A researcher can rebuild one thread's sequence of requests and pings, in order, with the idle gaps.
5. A researcher can join one user's sessions across machines, without knowing who the user is.

Non-goals:

1. Live dashboards and alerts. `metrics` does that.
2. Billing or the cost ledger. The event carries tokens; money is optional and never authoritative.
3. Replay of a request. No body is kept, so no request can be replayed.
4. A durable spool on disk. Version 1 buffers in memory only (§9). The schema does not depend on it.

Anti-goals (a failure even if the code works):

1. **Any text from a request or a response reaches the collector.** This includes tool names, file paths,
   model output, error messages from the provider, and host names of private gateways.
2. **A plain hash of an identifier.** The set of user ids is small, so a plain hash can be reversed by trying
   every candidate. All identifiers use a keyed hash (§5).
3. **The event changes the request path.** The emit must not block, must not do I/O, and must not fail a
   request.
4. **Silent gaps.** A drop that the data cannot show makes every rate wrong.

## 3. Constraints

- `Emit` is called after the response is finished, never on the request path, like `metrics.Observe`.
- `Emit` returns fast: no network call, no blocking lock. The OpenTelemetry batch processor does the export.
- The module does not keep slices of the `Result` or of the `Body` after `Emit` returns.
- The module imports `accountant` and `go.opentelemetry.io/otel/log`. Nothing else outside the standard
  library.
- Every timing that measures a duration comes from one process's monotonic clock. Wall-clock timestamps
  are for order and for joins only. Laptop clocks drift; durations across hosts are never computed.
- Off by default. The host turns it on with an endpoint and a hash key.

## 4. Approaches

| Approach | What it is | Trade-off |
|---|---|---|
| **A. OTLP log events (chosen)** | This module emits OTel log records. Each proxy exports OTLP. One central OpenTelemetry Collector writes columnar storage. | No collector code to write. Buffering is in memory only, so a long offline laptop loses events. `producer_seq` makes the loss visible. |
| B. Own exporter and own collector | HTTP/JSON batches, a disk spool, an ingest service. | Full control of buffering and exact drop counts. A service to write, run and secure, which repeats what the OTel Collector does. |
| C. Export Cortex session events | No new module. | The context-guru proxy gets nothing. The schema follows Cortex internals. |

Chosen: A. It needs no collector code, and both hosts already use OpenTelemetry. A disk spool can come later
without a schema change, because the gap fields (§6.1) do not depend on the transport.

## 5. Identifiers and the keyed hash

All identifiers that the user or the client chose go through one function:

```
id_hash(kind, value) = hex(HMAC-SHA256(key, kind + "\x00" + value)[:16])
```

- `kind` is the field name (`user`, `session`, `thread`, `provider_request`, `host`). So equal values of
  different kinds do not collide.
- `key` is one secret for each **deployment** (one research study). Every proxy in the deployment has the same
  key, so one user hashes to the same value on every machine. Researchers never get the key.
- `key_id` names the key on every event. A key rotation starts a new `key_id`; hashes do not join across
  key ids. That is intended.
- The module computes the hash (`events.NewHasher(key, keyID)`). Hosts never hash by themselves, so both hosts
  give the same value for the same input.

Where the user id comes from:

- Claude Code sends `metadata.user_id` in the request body. The host takes the account part of it.
- Other clients: the host may use the credential's stable id (an API key id, never the key). If there is none,
  `user_hash` is absent.

Thread ids: the accountant's `Thread.ID` and `Thread.Parent` may be host-made (`main`, `sub:3`) or may come
from the client. The module hashes both, except the literal `main`, which it keeps so a researcher can find
the primary thread. A host-made id is not stable across a restart: `sub:3` before and after a restart can be
two different threads. A researcher joins one conversation across a restart on `session_hash`, not on
`thread.id`.

## 6. The event schema

Each event is one OTel log record:

- `EventName` is `accountant.request`, `accountant.ping` or `accountant.producer`.
- `Timestamp` is the wall time the event describes (the request receive time, the ping send time).
- `ObservedTimestamp` is when `Emit` ran.
- The body is empty. All fields are log attributes. Nested groups are OTel map values. Lists are OTel slice
  values.

Notation: `?` marks a field that may be absent. Absent means **unknown**, never zero. A zero is a real zero.
Source: **A** the accountant (`Body`, `Breakpoint`, `Usage`, `Result`), **H** the host, **M** this module.

### 6.1 Envelope (every event)

| Field | Type | Source | Meaning |
|---|---|---|---|
| `schema_version` | int | M | `1`. A new field does not change it; a changed meaning does. |
| `event_id` | string | M | Random 128-bit id, hex. |
| `producer.id` | string | H | Random id of one install, kept on disk by the host. Not derived from the host name. |
| `producer.run_id` | string | M | Random id of one process start. |
| `producer.seq` | int | M | Starts at 1 for each `run_id`, plus 1 for each event the module receives, all event kinds, before validation (E7). A gap in `seq` is a lost or refused event. |
| `producer.kind` | enum | H | `cortex` \| `context-guru-proxy`. |
| `producer.version` | string | H | Host version (semver). |
| `accountant.version` | string | M | Accountant module version, read from `runtime/debug.ReadBuildInfo`. |
| `engine.version` | string? | H | context-guru engine version. |
| `engine.config_hash` | string? | H | `id_hash("config", canonical JSON of the engine config)`. Same config, same value. |
| `key_id` | string | H | The hash key in use (§5). |

### 6.2 Identity (request and ping)

| Field | Type | Source | Meaning |
|---|---|---|---|
| `user_hash` | string? | H→M | §5. |
| `session_hash` | string | A→M | `RequestMeta.Session`. |
| `thread.id` | string | A→M | `main`, or the hash of `Thread.ID`. |
| `thread.parent` | string? | A→M | Hash of `Thread.Parent`; absent when none. |
| `request_hash` | string | A→M | `id_hash("request", RequestMeta.RequestID)`. Hashed even though the proxy makes it, so a host that fills it from a header cannot leak that header. |
| `provider_request_hash` | string? | H→M | Hash of the provider's request id header. Lets a provider-side study join, with the key. |
| `turn` | int | H | Index of this request in its thread, from 1. It counts within one `producer.run_id` only, and starts again after a restart. |

### 6.3 Client and route (request)

| Field | Type | Source | Meaning |
|---|---|---|---|
| `client.agent` | enum | H | `claude-code` \| `codex` \| `other`. Parsed from `User-Agent`. |
| `client.version` | string? | H→M | Version of a known agent only. The module keeps it only if it matches `^\d+\.\d+\.\d+([-.][0-9A-Za-z.]{1,16})?$`; otherwise the field is absent. Absent for `other`. |
| `route.provider` | enum | H | `anthropic` \| `openai` \| `bedrock` \| `vertex` \| `gateway` \| `other`. |
| `route.dialect` | enum | H | `messages` \| `chat` \| `responses` \| `completions` \| `other`. |
| `route.host_hash` | string | H→M | Hash of the upstream host. A private gateway name never leaves in clear. |
| `model.requested` | string | H | Model the client asked for. |
| `model.sent` | string | A | `RequestMeta.Model`. |
| `model.key` | string? | A | `RequestMeta.ModelKey`. |
| `billing_mode` | enum | A | `api` \| `subscription` \| `unknown`. |
| `params.stream` | bool | H | |
| `params.max_tokens` | int? | H | |
| `params.effort` | enum? | A | `RequestMeta.Params["effort"]`: `low` \| `medium` \| `high` \| `xhigh` \| `max` \| `other`. |
| `params.thinking_budget` | int? | H | |
| `params.retry` | int? | H | Client retry count (`x-stainless-retry-count`), when the client sends it. |

Model ids are provider catalogue names, not user text. The module accepts only strings that match
`^[A-Za-z0-9._:/@-]{1,96}$`; any other value becomes `other`.

### 6.4 Timing (request)

All `_ms` fields are durations on the proxy's monotonic clock, from `t0` = the proxy read the first byte of
the request.

| Field | Type | Source | Meaning |
|---|---|---|---|
| `time.received` | timestamp | H | Wall time at `t0`. Also the record `Timestamp`. |
| `time.client_sent` | timestamp? | H | Only if the client sends its own send time. No client does today. |
| `time.body_read_ms` | int | H | `t0` to the last request byte. |
| `time.pipeline_ms` | int | H | Last request byte to the forward. Includes compaction. |
| `time.engine_ms` | int? | H | context-guru engine time inside `pipeline_ms`. |
| `time.forwarded` | timestamp | H | Wall time of the forward (`time.received` + offset). |
| `time.connect_ms` | int? | H | Dial and TLS to upstream; absent when a pooled connection was used. |
| `time.ttfb_ms` | int? | H | Forward to the first response byte. |
| `time.first_token_ms` | int? | H | Forward to the first content delta (stream only). |
| `time.response_end_ms` | int | H | Forward to the last response byte. |
| `time.total_ms` | int | H | `t0` to the last byte sent to the client. |
| `time.gap_prev_start_ms` | int? | H | This `t0` minus the previous request's `t0` in the same thread. The accountant measures the cache gap the same way. |
| `time.gap_prev_end_ms` | int? | H | This `t0` minus the end of the previous response in the thread. The user's think time plus tool time. |
| `time.gap_last_ping_ms` | int? | H | This `t0` minus the start of the last ping in the gap, on the monotonic clock. Not computed from `EntryFacts.LastPing`, which is a wall-clock time. |

The three gap fields use the monotonic clock of one process, so they are absent for the first request after a
restart.

### 6.5 Request structure (request)

Two copies: `client.*` is the body the client sent; `forwarded.*` is the body that went upstream. Both come
from the accountant `Body` values the host already builds with `NewBody`.

For each section `S` in `tools`, `system`, `messages`:

| Field | Type | Source | Meaning |
|---|---|---|---|
| `<copy>.S.count` | int | A | Positions in the section: the next section's `Body.Start` offset (or `len(Body.Pos)`) minus this one's. |
| `<copy>.S.bytes` | int | A | Sum of `PosHash.Bytes`. |
| `<copy>.S.media_bytes` | int | A | Sum of `PosHash.Media`. |
| `<copy>.S.tokens` | int? | H | Measured tokens (tokenizer or count call). Absent when not measured. |
| `<copy>.S.max_bytes` | int | A | Largest position in the section. |

Message shape, counts only (`client.*` and `forwarded.*`), source H:

| Field | Meaning |
|---|---|
| `<copy>.msg.user`, `.assistant` | Messages by role. |
| `<copy>.blk.text`, `.tool_use`, `.tool_result`, `.image`, `.document`, `.thinking` | Content blocks by type. |
| `<copy>.tool_result.bytes`, `.tool_result.max_bytes` | Size of tool results, total and largest. |

The role sequence and tool names are **not** recorded.

Cache structure:

| Field | Type | Source | Meaning |
|---|---|---|---|
| `<copy>.breakpoints` | list of `{section, index, ttl}` | A | `Breakpoint` values. Positions only. |
| `<copy>.prefix_at_breakpoints` | list of string | A→M | `id_hash("prefix", PosHash.Hash)` at each breakpoint. Equal values mean an equal prefix, across requests and users of the deployment. |
| `<copy>.common_prefix` | `{section, index, bytes}`? | M | Longest prefix shared with the previous forwarded body of the thread, from the cumulative hashes. Absent on the first request. |

### 6.5.1 Trigger (request)

What made the client send this request. The host derives it from the structure of the client body, and from
the previous request of the thread. It reads no text.

| Field | Type | Source | Meaning |
|---|---|---|---|
| `trigger.kind` | enum | H | See the table below. |
| `trigger.basis` | enum | H | `structure` (from the body shape), `header` (the client said so), `heuristic` (a size or timing rule). Researchers can filter out `heuristic`. |
| `trigger.tool_results` | int? | H | `tool_result` blocks in the last user message. Set for `tool_result`. |
| `trigger.tool_errors` | int? | H | Of those, how many have `is_error` set. |
| `trigger.tool_result_bytes` | int? | H | Bytes of those blocks. |
| `trigger.parallel` | bool? | H | The previous response asked for more than one tool call, and this request returns them together. |

`trigger.kind` values:

| Value | Rule (the host checks in this order; the first match wins) | Basis |
|---|---|---|
| `retry` | The client body hash equals the previous request's in the thread, or the client sent a retry count above 0. | `structure` or `header` |
| `compaction` | The host set `RequestMeta.Compaction` on this request. | as the host knows it |
| `subagent_start` | First request of a thread that has a `Thread.Parent`. | `structure` |
| `side_call` | A request outside the main loop: no tools and at most 2 messages in a thread that had tools, such as a title or summary call. | `heuristic` |
| `tool_result` | The last message is a user message whose blocks are all `tool_result`. | `structure` |
| `user_prompt` | The last message is a user message with at least one text or media block and no `tool_result`. | `structure` |
| `mixed` | The last user message has both `tool_result` and text blocks (a user interjection during a tool loop). | `structure` |
| `continuation` | The last message is an assistant message (prefill), or the previous response stopped with `pause_turn` or `max_tokens`. | `structure` |
| `other` | None of the above. | |

With `time.gap_prev_end_ms` (§6.4), the trigger separates two different waits:

- `tool_result`: the gap is mostly tool execution time.
- `user_prompt`: the gap is the user's think time.

Keep-alive pings are not requests and have their own event (§6.10). A ping never has a `trigger`.

### 6.6 Response (request)

| Field | Type | Source | Meaning |
|---|---|---|---|
| `resp.status` | int | H | Status sent to the client. |
| `resp.error_kind` | enum? | H | `upstream_timeout` \| `upstream_refused` \| `upstream_dns` \| `upstream_tls` \| `upstream_error` \| `client_canceled` \| `proxy_error` \| `rate_limited` \| `overloaded` \| `other`. Never the error text. |
| `resp.stop_reason` | enum? | H | `end_turn` \| `tool_use` \| `max_tokens` \| `stop_sequence` \| `pause_turn` \| `refusal` \| `other`. |
| `resp.bytes` | int | H | Response body bytes as received from upstream. |
| `resp.blk.text`, `.tool_use`, `.thinking` | int | H | Output blocks by type. |
| `resp.ratelimit.tokens_left_pct` | int? | H | Remaining input-token limit, percent, rounded to 10. |
| `resp.ratelimit.requests_left_pct` | int? | H | The same for requests. |

### 6.7 Usage (request)

| Field | Type | Source | Meaning |
|---|---|---|---|
| `usage.input` | int | A | `Usage.Input` (uncached). |
| `usage.cache_read` | int | A | `Usage.CacheRead`. |
| `usage.cache_write_5m` | int | A | `Usage.CacheWrite - Usage.CacheWrite1h`. |
| `usage.cache_write_1h` | int | A | `Usage.CacheWrite1h`. |
| `usage.output` | int | H | Output tokens, including reasoning. |
| `usage.reasoning` | int? | H | Reasoning part of output, when the provider says. |
| `usage.cost_usd` | float? | H | Only if the host has a `Pricer` and `billing_mode` is `api`. Never authoritative. |
| `usage.cost_estimated` | bool? | H | The `Pricer` said estimated. |

### 6.8 Cache state and accountant result (request)

| Field | Type | Source | Meaning |
|---|---|---|---|
| `cache.phase` | enum | A | `Result.Phase`: `hot` \| `cold` \| `unknown`. |
| `cache.entry_ttl_s` | int? | A | `EntryFacts.TTL` in seconds. |
| `cache.lifetime` | enum | A | `exact` \| `minimum` \| `unknown`. |
| `cache.min_cacheable` | int? | A | `EntryFacts.MinCacheable`. |
| `cache.boundary` | `{section, index}`? | A | `Result.Boundary`. |
| `cache.shared_warm` | `{section, index}`? | A | `EntryFacts.SharedWarm`. |
| `cache.effects` | list | A | One map per `CacheEffect`: `kind`, `cause?`, `ttl?`, `in_baseline`, `estimated`, `from?`, `to?`, `saved` and `caused` as tier maps, `change` (§6.9). |
| `acct.reliable` | bool | A | `Result.Reliable()`. |
| `acct.recomputed` | bool | A | `Result.Recomputed`. |
| `acct.checks` | list of string | A | `CheckFailure.Check` names only. `Detail` is dropped. |
| `acct.unpriced` | list of string | A | `Unpriced.Reason` values. |
| `acct.compaction` | enum? | A | `manual` \| `auto` \| `unknown`. |
| `acct.interaction` | tier map | A | `Result.Interaction`. |
| `acct.storage_tokens` | int? | A | `Result.StorageTokens`. |

A tier map is `{input, cache_write_5m, cache_write_1h, cache_read}`, the `TierTokens` fields.

### 6.9 Operations (request)

`ops` is a list, one map per `PricedOp`:

| Field | Type | Meaning |
|---|---|---|
| `kind` | enum | `rewrite` \| `spend` \| `setting`. |
| `operator` | string | Operator name. Accepted only from the host's allowlist (`WithOperators`), else `other`. |
| `change` | string | `id_hash("change", ChangeID)`. The same change on later requests has the same value. |
| `dry_run` | bool | |
| `saved`, `caused` | tier map | |
| `estimated` | bool | |
| `size_before`, `size_after` | `{bytes, tokens?, media_bytes}` | From the input `Operation` with the same `ID` (`Request.Ops`). |
| `at` | `{section, index}` | First touched position, from the same `Operation`. |
| `call` | map? | Spend only: `model`, `route.host_hash`, usage tiers, `output`, `detached`. |

The host also gives the context-guru summary, source H, from its saving event:

| Field | Type | Meaning |
|---|---|---|
| `cg.applied` | bool | The forwarded body is the compacted one (false under observe). |
| `cg.tokens_before`, `cg.tokens_after` | int | Engine counts, whole request. |
| `cg.frozen_tokens` | int | Prefix left alone for cache safety. |
| `cg.prefix_rewritten` | bool? | A change landed inside the previous turn's cached prefix. Absent when unknown. |
| `cg.saved_gross`, `cg.saved_unique` | int | Engine tokens. |
| `cg.preset` | enum | Engine preset: the list of preset names that the engine version exports, plus `custom` (no preset) and `other`. The host passes the list with `WithPresets`. |

### 6.10 Keep-alive ping (`accountant.ping`)

One event for each ping **decision** the keeper reaches, so a refusal is visible too. It carries §6.1 and
§6.2, without `turn` and `request_hash` (a ping has no client request), plus:

| Field | Type | Source | Meaning |
|---|---|---|---|
| `ping.window` | string | A→M | `id_hash("change", ChangeID)` of the keep-alive window (`ka:<thread>:<window>`). Equals the `change` of the rescue effect on the thread's next request. |
| `ping.index` | int | H | 1 for the first ping of the window, then 2, … |
| `ping.max` | int | H | `max_pings` in force. |
| `ping.outcome` | enum | H | `sent` \| `refused` \| `failed`. |
| `ping.refuse_reason` | enum? | H | `cost_ceiling` \| `below_min_prefix` \| `max_pings` \| `no_credential` \| `credential_expired` \| `model_unpriced` \| `unsupported_route` \| `shutdown` \| `other`. |
| `ping.idle_ms` | int | H | Idle time at the decision, from the previous request's `t0`. |
| `ping.scheduled_offset_ms` | int | H | When the timer was due, relative to the previous request's `t0`. With `idle_ms`, this shows timer lateness. |
| `ping.prefix_tokens` | int? | H | Billed prefix the ping would replay. |
| `ping.projected_cost_usd` | float? | H | Cost the keeper projected. |
| `ping.status` | int? | H | Upstream status (sent and failed only). |
| `ping.error_kind` | enum? | H | As `resp.error_kind`. |
| `ping.latency_ms` | int? | H | Send to last byte. |
| `ping.usage` | tier map + `output` | A | `Ping.Usage` (sent only). |
| `ping.cost_usd` | float? | H | As `usage.cost_usd`. |
| `model.sent`, `route.*`, `billing_mode` | | | As in §6.3. |

The outcome of a ping (did it save the next turn?) is on the **next request** of the thread: a `rescue`
effect in `cache.effects` with `change` equal to `ping.window`, and `time.gap_last_ping_ms`. A window whose
thread sends no next request has no rescue; a researcher sees that as a ping event with no matching request.

All pings of one window share one `ping.window`, and the next request has at most one rescue for it. So a
rescue belongs to the **window**, not to one ping. Analysis of keep-alive value is per window (`ping_windows`,
§9.1).

### 6.11 Producer stats (`accountant.producer`)

Every 60 seconds, and at shutdown. It carries §6.1 plus counters since `run_id` started:

| Field | Meaning |
|---|---|
| `stats.emitted` | Events handed to the OTel logger. |
| `stats.refused` | Events the module refused (a validation rule in §7 failed). |
| `stats.export_failed` | Records the exporter reported as failed, when the host wires the SDK's error handler to the module (`events.ExportErrorHandler`). |
| `stats.uptime_s` | |
| `stats.extra_dropped` | Extra values the module dropped (§6.12): an undeclared key, a wrong type, or a value out of range. |
| `extras` | The extra declarations of this run (§6.12): a list of `{key, type, enum?, unit?}`. |

`producer.seq` shows **that** events are missing. `stats.*` shows **where** they were lost: refused here,
or lost after `emitted`.

### 6.12 Host extras

A host can add its own fields to a request event or a ping event, without a change to this schema. These
fields are **extras**. They follow the same "structure only" rule as every other field.

1. **The host declares each extra once, at `New`.** A declaration has a key, a type and, optionally, a unit:

   ```go
   events.WithExtra("sweep_ms", events.Duration)
   events.WithExtra("tier", events.Enum("a", "b", "c"))
   events.WithExtra("tenant", events.Hashed)
   ```

2. **The host fills values for each event** in `Request.Extra` or `Ping.Extra`, a map from key to value.
3. **Types.** An extra is one of these:

   | Type | Value | Sent as |
   |---|---|---|
   | `Int` | int64 | int |
   | `Float` | float64, finite | double |
   | `Bool` | bool | bool |
   | `Duration` | `time.Duration` | int, milliseconds |
   | `Enum(values...)` | string from the list | string; a value outside the list becomes `other` |
   | `Hashed` | any string | `id_hash("x." + key, value)` (§5); the raw value never leaves the module |

   There is **no free string type**.
4. **Key format.** A key matches `^[a-z][a-z0-9_]{0,31}$`. An enum value matches the same pattern.
   `WithExtra` returns an error for any other key or enum value, so a bad declaration fails at startup, not
   per request.
5. **Namespace.** The module sends each extra as `x.<producer.kind>.<key>`. An extra cannot collide with a
   schema field, and two hosts' extras with the same key stay apart.
6. **Undeclared or wrong values are dropped, not the event.** The module drops a value whose key is
   undeclared, whose type does not match, or which is not finite. It counts each drop in
   `stats.extra_dropped`. The rest of the event goes out.
7. **The declarations are in the data.** Each `accountant.producer` event carries `extras`, the declarations
   of the run. A researcher reads there what an extra is and since which `run_id` it exists.
8. **Limits.** A host declares at most 64 extras. One event carries at most 64 extra values.
9. **Meaning.** Extras do not change `schema_version`. If the meaning of an extra changes, the host uses a new
   key. An extra that many hosts need becomes a schema field in a later version.

Storage keeps extras in two map columns, so a new extra needs no table change: `x_num Map(String, Float64)`
for `Int`, `Float`, `Bool` and `Duration`, and `x_str Map(String, String)` for `Enum` and `Hashed`.

## 7. Rules

1. **E1 Structure only.** No identifier leaves in clear: every id that a host or a client could fill is hashed (§5). A string field is one of: an enum value, a hash from §5, a version, a model id
   (§6.3), an operator name from the allowlist, or a random id that the module made. The module
   checks each string at `Emit`. An enum outside its list becomes `other`. A model id or operator that
   fails its check becomes `other`.
2. **E2 No free text type.** The host-facing input types have no field of type `string` for content. A test
   walks the input types by reflection and fails on a string field that is not listed in an allowlist with
   its rule (like `api_test.go`).
3. **E3 Collector allowlist.** The central collector drops every attribute that is not in the schema of this
   `schema_version` (a `transform` processor in front of storage). This is the second check of E1. For keys under
   `x.`, the collector checks the value, not the name: it keeps an int, a double, a bool, a string that matches
   `^[0-9a-f]{32}$` (a hash) or `^[a-z][a-z0-9_]{0,31}$` (an enum value), and drops anything else.
   Nested values (`ops`, `cache.effects`, the breakpoint lists) are checked one level later: the
   materialized views (§9.1) extract only the known keys of each nested map, with typed `JSONExtract` calls.
   An unknown nested key, or a value of the wrong type, is never stored.
4. **E4 Absent is unknown.** The module never writes a zero for a value the host did not give.
5. **E5 Fast emit.** `Emit` does no I/O and takes no blocking lock. It calls `log.Logger.Emit` and returns.
6. **E6 No retained slices.** The module copies what it needs from `Body` and `Result` before it returns.
7. **E7 One sequence.** The module takes the next `producer.seq` for every event it receives, **before** it
   validates the event. So a refused event uses a number, shows as a gap, and is matched by `stats.refused`.
8. **E8 Fail open.** A panic inside the module is recovered and counted as refused. The request is not affected.
9. **E9 Declared extras only.** A host field outside this schema is sent only as a declared extra (§6.12). The
   module drops an undeclared key or a value of the wrong type, and counts it. No extra type carries free text.

## 8. Module API (proposed)

```go
package events // github.com/rossoctl/accountant/events

type Hasher struct{ /* key, keyID */ }
func NewHasher(key []byte, keyID string) (*Hasher, error) // key at least 32 bytes

type Producer struct {
    ID, Kind, Version         string // §6.1
    EngineVersion, ConfigJSON string // optional; ConfigJSON is hashed, never sent
}

type Emitter struct{ /* logger, hasher, producer, run id, seq, counters */ }
func New(l log.Logger, h *Hasher, p Producer, opts ...Option) (*Emitter, error)
func WithOperators(names ...string) Option
func WithStatsInterval(d time.Duration) Option
func WithExtra(key string, t ExtraType) Option // §6.12; New returns its error
func WithPresets(names ...string) Option        // §6.9 cg.preset values
func WithMaxThreads(n int) Option               // bound of the per-thread state; default 1024

type ExtraType struct{ /* kind, enum values */ }
var Int, Float, Bool, Duration, Hashed ExtraType
func Enum(values ...string) ExtraType

// Request is what the host gives for one request, after Compute.
type Request struct {
    Meta                accountant.RequestMeta
    Thread              accountant.Thread
    Client, Forwarded   accountant.Body
    ClientBP, ForwardBP []accountant.Breakpoint
    Facts               accountant.EntryFacts
    Ops                 []accountant.Operation // the inputs, for §6.9 sizes and positions; matched to PricedOp.OpID
    Usage               accountant.Usage
    Result              accountant.Result
    Host                HostFacts // §6.2–§6.7 fields marked H; pointers for optional values
    Extra               map[string]any // §6.12; declared keys only
}
func (e *Emitter) Request(r Request)

type Ping struct {
    Ping accountant.Ping // nil-able fields for refused pings
    Host  PingFacts       // §6.10 fields marked H
    Extra map[string]any  // §6.12; declared keys only
}
func (e *Emitter) Ping(p Ping)

func (e *Emitter) ForgetThread(session, thread string) // the host calls it when it drops the thread
func (e *Emitter) Close() // emits the last accountant.producer event
func (e *Emitter) ExportErrorHandler() otel.ErrorHandler
```

The module computes `common_prefix`, so it keeps, for each thread, a copy of the cumulative hashes of the
last forwarded body. Two rules bound that state:

1. `WithMaxThreads(n)`: when a new thread would exceed `n`, the module drops the thread it used least
   recently. The next request of that thread has no `common_prefix`.
2. `ForgetThread`: the host calls it when it drops the thread from its own accountant state (§W5.3 in
   `cortex-wiring.md`), so the two stay matched.

## 9. Transport and the central collector

1. Each host builds an OTel `LoggerProvider` with a batch processor and the OTLP/HTTP exporter, and passes
   `provider.Logger("accountant/events")` to `New`.
2. Export goes over TLS to the collector endpoint, with a bearer token for each deployment.
3. The central collector is the OpenTelemetry Collector (contrib distribution), with:
   - the `otlp` receiver and the `bearertokenauth` extension,
   - a `transform`/`filter` processor that applies E3,
   - the `clickhouse` exporter, which writes to the database in §9.1.
4. Buffering is in memory, in the batch processor (default queue 2048 records). An offline laptop loses what
   overflows the queue; `producer.seq` shows the gap. A disk spool is later work (non-goal 4).

### 9.1 Storage and access

**Decided: one live ClickHouse database**, shared by all researchers of a deployment. The volume is small:
100 users at about 2,000 requests a day is about 200,000 events a day, under 0.5 GB before compression. So
ClickHouse is chosen for the queries (wide scans, window functions over threads, `Map` and array columns),
not for scale.

**Landing table.** The Collector's `clickhouse` exporter writes its own fixed table, `otel_logs`. Every
attribute goes into one `LogAttributes Map(String, String)` column, so numbers arrive as strings, and nested
groups arrive as JSON text. Researchers do not query this table.

**Typed tables.** Materialized views read each new `otel_logs` row and write it to one typed table for each
`EventName`:

| Table | From | Sort key |
|---|---|---|
| `request_events` | `accountant.request` | `(session_hash, thread_id, received)` |
| `ping_events` | `accountant.ping` | `(session_hash, thread_id, at)` |
| `producer_events` | `accountant.producer` | `(producer_id, run_id, seq)` |

- The sort keys favour reading one thread in order. A query that filters only on time gets monthly partition
  pruning and then scans the month. At this volume that is acceptable. If time-only queries become slow, add
  a `minmax` skip index on the time column.
- Dotted names become underscores: `usage.cache_read` → `usage_cache_read`.
- `ops` and `cache.effects` become arrays of named tuples, parsed with `JSONExtract`.
- Extras become two map columns, `x_num Map(String, Float64)` and `x_str Map(String, String)` (§6.12).
- Partition by month. Retention is a table `TTL` on the event time, set for each deployment. The `otel_logs`
  table has a short `TTL` (7 days), because the typed tables hold the data.

**Curated views.** Researchers query these views:

| View | One row is |
|---|---|
| `requests` | one request, all typed columns |
| `pings` | one ping decision |
| `ping_windows` | one keep-alive window: its pings (sent, refused, failed), their total cost, the idle time of the last sent ping, and the next request of its thread joined on `ping.window` = rescue `change`, with `rescued` (bool) and the rescued tokens. A window with no next request has `rescued` NULL. |
| `threads` | one thread: request count, first and last time, tokens by tier, cache hit ratio, trigger counts |
| `gaps` | one gap in `producer.seq`: `producer_id`, `run_id`, first missing seq, `missing` count |

**Access.**

1. Each researcher has one read-only ClickHouse account. Only the Collector's account can write.
2. Access is over TLS, and only through the deployment's VPN or SSO proxy. No public port.
3. Each researcher account has a settings profile: `max_execution_time`, `max_memory_usage`, and a quota on
   queries for each hour. So one heavy query cannot slow down ingest. If load grows, add a read replica for
   researchers.
4. The hash key (§5) is never stored in the database or given to a researcher account.

**Clients.** Researchers use any ClickHouse client:

- SQL: `clickhouse-client`, DataGrip or DBeaver.
- Notebooks: `clickhouse-connect` (Python), which returns pandas or Polars frames. R and Julia have drivers.
- Shared charts, optional: Grafana or Superset on the same database.

```python
import clickhouse_connect
db = clickhouse_connect.get_client(host="events.example", username="alice", password=..., secure=True)
df = db.query_df("SELECT * FROM requests WHERE received >= now() - INTERVAL 7 DAY")
```

**Example queries.**

Cache hit ratio by trigger, last 7 days:

```sql
SELECT trigger_kind,
       sum(usage_cache_read) / sum(usage_input + usage_cache_read
                                   + usage_cache_write_5m + usage_cache_write_1h) AS hit_ratio,
       count() AS requests
FROM requests
WHERE received >= now() - INTERVAL 7 DAY
GROUP BY trigger_kind ORDER BY requests DESC;
```

Keep-alive value: share of windows with a sent ping that rescued the next request, by idle time:

```sql
SELECT intDiv(last_sent_idle_ms, 60000) AS idle_min, avg(rescued) AS rescue_rate,
       count() AS windows, sum(ping_cost_usd) AS cost_usd
FROM ping_windows WHERE sent > 0 AND rescued IS NOT NULL
GROUP BY idle_min ORDER BY idle_min;
```

Lost events for each producer:

```sql
SELECT producer_id, run_id, sum(missing) AS lost
FROM gaps GROUP BY producer_id, run_id HAVING lost > 0;
```

## 10. Host work

| Host | Work |
|---|---|
| context-guru proxy | Already imports accountant. Build the `Emitter`; fill `HostFacts` from the proxy's request timing and response parse; emit a `Ping` event from the keeper for each decision; hook `Close` to shutdown. |
| Cortex | Imports accountant only after the wiring in `cortex-wiring.md` lands. Timing comes from the listener; response facts from `inference-parser`; ping decisions from the `context-guru` plugin's keep-alive. |

Both hosts need:

1. Config: `events.enabled`, `events.endpoint`, `events.token`, `events.key_file`, `events.key_id`.
2. A persisted `producer.id` (random, created on first start).
3. Monotonic timing points: `t0`, last request byte, forward, connect, first byte, first token, end.
4. Per-thread memory of the previous `t0` and response end, for the gaps.

## 11. Left to the builder

- The exact Go names of `HostFacts` and `PingFacts`, and which fields are pointers.
- How the module encodes nested groups (map values or flattened dotted keys), as long as the collector
  transform and the storage schema match it.
- The DDL of the typed tables, the materialized views and the curated views (§9.1).
- The exact rule for `side_call` for each client. The heuristic above is the default.
- Test fixtures. At least: one thread of three requests with one ping and a rescue; one request for each
  `trigger.kind`; a refused ping; two pings in one window and one rescue; a restart (new `run_id`); the E2 reflection test; a fuzz test that no input string reaches an attribute unchecked;
  an extra of each type, an undeclared extra and a wrong-type extra (dropped and counted).

## 12. Open questions

1. Is `model.requested`/`model.sent` acceptable under "structure only"? This design keeps model ids, with a
   format check, because caching and pricing research depends on them. The format check still lets a client put
   up to 96 characters of its own choosing there. The stricter option is an allowlist of known model ids, with
   `other` for the rest.
2. Consent: does each user opt in at install, or does the deployment owner decide? The design assumes the
   deployment owner turns it on.
3. Who holds the hash key, and is it per study or per organisation?
4. Deletion on request: a user may ask for deletion of their data (for example under GDPR). The key holder can
   compute that user's `user_hash`, and a delete by `user_hash` then removes the user's rows. Who does this,
   and within what time? Rows with no `user_hash` cannot be found this way.
