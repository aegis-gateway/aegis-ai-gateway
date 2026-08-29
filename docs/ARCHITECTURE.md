# AEGIS AI Gateway — Architecture

## Overview

AEGIS AI Gateway is an OpenAI-compatible reverse proxy that sits between client applications and LLM providers (OpenAI, Anthropic, Azure OpenAI, and self-hosted vLLM). Every request is authenticated, inspected by a content filter chain, evaluated against OPA policies, routed to an appropriate provider based on model alias and data classification, and then fully recorded to a PostgreSQL audit trail. The gateway exposes a single HTTP API at `:8080` and a Prometheus metrics endpoint at `:9090`.

---

## Request Lifecycle

The following steps describe a `POST /v1/chat/completions` request from ingress to audit write. The source references point to the package that owns each step.

### 1. Ingress — HTTP server (`cmd/`)

The gateway registers routes using the chi router. Before reaching a handler, every request passes through two shared middlewares:

- **Request ID middleware** — generates a unique `X-Request-ID` UUID and writes it to the response header. All downstream log entries and audit rows reference this ID.
- **Auth middleware** — see step 2.

### 2. Auth middleware (`internal/auth`)

`auth.Middleware` reads the `Authorization: Bearer <token>` header. The token is SHA-256 hashed and looked up in the `api_keys` table via a `KeyStore` interface. If the key is not found or is revoked/expired, a 401 is returned and an `auth_failure` event is written to `audit_events`.

On success, the middleware populates an `AuthInfo` struct and stores it in the request context:

```go
type AuthInfo struct {
    KeyID                string
    OrganizationID       string
    TeamID               string
    UserID               string
    MaxClassification    Classification
    AllowedModels        []string
    RPMLimit             int
    TPMLimit             int
    DailySpendLimitCents int
}
```

### 3. Request parsing and validation (`internal/gateway`, `internal/validation`)

`gateway.Handler.ChatCompletions` reads the request body, decodes it with `types.DecodeChatCompletion`, an allowlist that refuses any unsupported field, into `types.AegisRequest`, and enriches it with auth context fields (org, team, user, key ID, classification). Optional AEGIS-specific headers are read: `X-Aegis-Project` and `X-Aegis-Trace-Context`.

The `validation.Validator` then checks field constraints:

- `model` — required, max 256 chars, alphanumeric plus `-_.:`
- `messages` — required non-empty array; max 1,000 messages; max 100,000 chars per message; max 1,000,000 chars total
- `temperature` — optional, 0.0–2.0
- `max_tokens` — optional, 1–128,000
- `top_p` — optional, 0.0–1.0
- `stop` — optional, max 4 sequences of max 256 chars each

Validation failures return 400 with a structured error body.

### 4. Filter chain (`internal/filter`)

`filter.Chain.Run` executes every enabled `Filter` in registration order. Each filter implements:

```go
type Filter interface {
    Name() string
    Enabled() bool
    ScanRequest(ctx context.Context, req *types.AegisRequest) Result
}
```

`Result.Action` is one of `pass`, `flag`, `redact`, or `block`. The chain stops on the first `block` result and returns it to the caller. Currently registered filters (in order):

| Filter | Package | What it checks |
|--------|---------|---------------|
| Secrets | `internal/filter/secrets` | API keys, tokens, passwords in message content |
| Injection | `internal/filter/injection` | Prompt injection patterns; configurable block/flag thresholds |
| PII (optional) | `internal/filter/pii` | Calls the external NLP gRPC filter service (`aegis-filter-nlp:50051`) |

If a filter blocks, `audit.Logger.LogFilterBlock` writes a `filter_block` event to `audit_events` (async) and the handler returns HTTP 451.

### 5. Model allowlist enforcement (`internal/gateway`, `internal/auth`)

Before routing, `ChatCompletions` checks the requested alias against the calling key's
`allowed_models` using `AuthInfo.ModelAllowed` (`internal/auth/context.go`). An empty
allowlist permits every configured alias; a non-empty one permits exactly the aliases it
names, matched literally, so a deprecated alias is not implied by the alias it names.

The check applies **only to an alias that is a key of `configs/models.yaml`**. That is
load-bearing rather than defensive: the refusal writes the alias to `audit_events.model`,
which the leaf hash covers and the sealer seals, and `DecodeChatCompletion` accepts any
string as a model. Enforcing on an unconfigured alias would let a caller holding a
restricted key put caller-controlled text into the attested record. An unconfigured alias
falls through to `ResolveRoute`, which refuses it as an unknown model and writes no audit
row, exactly as it does for every key.
`TestChatCompletions_UnconfiguredAliasIsNotAttested` pins this.

A key that is refused gets HTTP 503 in the same envelope as the classification-ceiling
refusal in step 6, and an `auth_failure` event is written to `audit_events` carrying the
authenticated organization, the requested alias, and a fixed reason string. The two
refusals are deliberately indistinguishable to the caller: both mean the key may not reach
that route, and the specific cause is in the audit record rather than in the response.

`GET /v1/models` filters its listing through the same `ModelAllowed` method, so the
listing and the enforcement cannot disagree.
`TestModelAllowlist_ListAndCompletionAgree` (`internal/gateway/model_allowlist_test.go`)
asserts that.

### 6. Provider routing (`internal/router`)

`router.ResolveRoute` maps the requested model alias to a concrete `(provider, model)` pair using `configs/models.yaml`. The routing algorithm:

1. Look up the alias in `ModelsConfig.Models`.
2. Try the **primary** route: verify the route's `classification_ceiling` permits the request's classification tier and the provider's circuit breaker is closed.
3. If the primary is ineligible or unhealthy, try each **fallback** route in order.
4. If no route is found, return HTTP 503.

Classification levels are ordered `PUBLIC < INTERNAL < CONFIDENTIAL < RESTRICTED`. A route with `classification_ceiling: CONFIDENTIAL` accepts PUBLIC, INTERNAL, and CONFIDENTIAL requests but rejects RESTRICTED ones.

### 7. OPA policy evaluation (`internal/filter/policy`)

After routing (so `provider_type` is known), `policy.Evaluator.ScanRequest` builds a `PolicyInput` struct and evaluates it against compiled Rego modules querying `data.aegis.policy.allow` and `data.aegis.policy.reason`. If OPA returns `allow = false`, the request is blocked with HTTP 451 and a `filter_block` event is written to `audit_events`.

The policy evaluator fails closed: if no policies are loaded or evaluation times out (default 100 ms), the request is blocked.

### 8. Provider HTTP call (`internal/router/adapters`)

The resolved `adapters.ProviderAdapter` transforms the `AegisRequest` into the provider's native format and sends it. For non-streaming requests the call is wrapped in `retry.Executor`, which retries on 5xx, 429, 408, and network errors using exponential backoff with jitter (default: 2 retries, initial 100 ms, max 5 s).

Supported adapter types:
- `openai` — OpenAI chat completions API; also used for vLLM
- `anthropic` — Anthropic Messages API; response is normalized to OpenAI format
- `azure_openai` — Azure OpenAI deployments

For streaming requests (`stream: true`), `StreamingHandler.HandleStream` forwards SSE chunks directly to the client with per-chunk and total timeouts.

### 9. Response and cost calculation (`internal/cost`, `internal/gateway`)

The adapter's `TransformResponse` normalizes the provider response into `types.AegisResponse`. The `cost.Calculator` looks up per-token prices from `configs/models.yaml` and computes an estimated USD cost from prompt and completion token counts.

### 10. Audit write (`internal/audit`, `internal/storage`)

Two writes happen asynchronously (non-blocking) after the response is sent:

- **`audit.Logger`** — writes security-relevant events (auth failures, filter blocks, rate limit violations) to `audit_events`.
- **`storage.UsageRecorder`** — writes per-request token and cost data to `usage_records`.

Neither write blocks the response path. Failures are logged but do not affect the client.

---

## Data Persistence

### `api_keys`

Created by the `keygen` CLI tool. Stores a SHA-256 hash of the key (never the plaintext), along with organization/team/user attribution, classification ceiling, optional model allowlist, rate limits, and lifecycle timestamps (created, expires, last used, revoked).

`allowed_models` is a permission, enforced on both the completion path and the model
listing (step 5 above). `keygen` writes an empty JSON array for every key it issues, and
an empty array means unrestricted: reading it as "permits nothing" would revoke every key
already in existence.

### `audit_logs`

**Created by migration 002, never written, and deprecated.** No component
inserts into this table. `GET /aegis/v1/audit/logs` returned an empty list for
its whole existence and now returns **410 Gone**, pointing at
`/aegis/v1/audit/events`. The table is retained because `internal/purge` targets
it by name and two guards still sweep it; removal is tracked in
`docs/evidence/known-limitations.md` section 2.11. Per-request data lives in
`usage_records`, and the attested decision record in `audit_events`.

### `audit_events`

Written by `audit.Logger`, and the only table the sealer covers. **The decision
record covers both permitted and refused requests.**

Eight event types are emitted:

| Event type | Emitted when |
|---|---|
| `request_complete` | A request was permitted and served, on either the streaming or the non-streaming path |
| `provider_failure` | A request passed every gate and then failed at the provider |
| `auth_failure` | A key was rejected, or an authenticated key requested a model outside its allowlist |
| `rate_limit_violation` | The per-key RPM limit was exceeded |
| `budget_violation` | The daily team spend limit was exceeded |
| `filter_block` | The secrets, injection, PII or policy filter blocked the request |
| `pricing_denied` | The routed provider and model have no pricing entry |
| `redis_failure` | Redis was configured and unreachable, so the request failed closed |

`auth_success` is declared and deliberately not emitted: it is subsumed by
`request_complete` and `auth_failure`, and would double write volume for no
evidentiary gain.

**What a `request_complete` or `provider_failure` row carries.** Only these
columns, all of them identifiers, enumerated values or status codes:

| Column | Value |
|---|---|
| `event_type` | `request_complete` or `provider_failure` |
| `timestamp` | When the event was constructed |
| `request_id` | The `X-Request-ID` for the request |
| `organization_id`, `team_id`, `user_id`, `api_key_id` | The authenticated identity |
| `ip_address` | The caller's remote address |
| `endpoint`, `method` | `/v1/chat/completions`, `POST` |
| `status_code` | What the caller was sent. On a stream this is `StreamOutcome.HTTPStatus()`, the same value the Prometheus counter and the usage record take, so the three cannot disagree: 200 completed, 499 client disconnected, 504 stalled, 502 read error, 500 no flusher |
| `provider` | The **configured provider key** from the resolved route, not the adapter type |
| `model` | The **concrete model** that provider served, from `configs/models.yaml`, not the provider's echo of it |
| `operation` | `chat_completion` or `chat_completion_stream` |
| `reason` | On `provider_failure` only: one of six enumerated stage constants, never provider or caller text. A non-success status from the provider is `provider_http_error` on both the buffered and streamed paths; `provider_response_invalid` is reserved for a success that could not be read or decoded |

**What it does not carry, and why.** The requested model alias, the
classification tier, the request latency, and the prompt and completion token
counts. `audit_events` has no column for any of them, and adding one changes the
field set the leaf hash covers, which requires `hash_schema_version=3`. ADR 0011
records that decision and its cost. Until that bump is cut, those five facts live
in `usage_records`, joinable on `request_id` and **not attested**. See
`docs/evidence/known-limitations.md` section 2.13.

The exposure is deliberate on both sides: no caller-supplied text and no
provider-supplied text reaches a sealed column.
`TestCompletionEventCarriesNoFreeText` and `TestProviderFailureStageIsEnumerated`
enforce that, and `TestNoPayload_AllowPathCanary` plus
`TestNoPayload_AllowPathCanaryStreaming` assert it end to end against a live
gateway.

**Exactly one event per request.** The streaming path has six exits, including a
client disconnect and two timeouts, so `StreamMetrics.Outcome` records which one
ran and `HandleStream` emits a single event chosen from it.
`TestStreamingEmitsExactlyOneEvent` covers each. A client disconnect is recorded
as a completion, not a provider failure: the gateway did its work and the
provider was engaged.

Writes are fire-and-forget (`Log()` spawns a goroutine) and are dropped when the
database handle is nil, so audit capture is best-effort rather than guaranteed. A
dropped write leaves no row and, because `BIGSERIAL` allocates only on a
successful insert, no id gap either, so the sealer seals a contiguous run and
reports a healthy chain over an incomplete record.
`aegis_audit_write_failures_total`, labelled by event type and reason, is the
only signal that this happened. Alert on any increase.

### `usage_records`

Written by `storage.UsageRecorder` for every completed request. Stores per-request token counts, estimated cost in USD, provider, model requested vs. served, classification level, and project tag. Used for cost attribution and budgeting.

There is also a `usage_daily` table (migration 003) intended for daily aggregate roll-ups by org/team/model/provider. Like `audit_logs`, **it has no writer in the codebase** and stays empty; roll-ups would have to be derived from `usage_records`.

---

## Package Reference

| Package | Responsibility | Key types / interfaces |
|---------|---------------|----------------------|
| `internal/auth` | Bearer token authentication; key hash lookup; context enrichment | `Middleware`, `KeyStore`, `AuthInfo` |
| `internal/gateway` | HTTP handler for `/v1/chat/completions` and `/v1/models`; orchestrates all steps | `Handler`, `StreamingHandler` |
| `internal/filter` | Filter chain execution; action types | `Filter`, `Chain`, `Result`, `Action` |
| `internal/filter/policy` | OPA Rego policy evaluation; atomic hot-reload | `Evaluator`, `PolicyInput`, `PolicyUser`, `PolicyReq` |
| `internal/router` | Model alias resolution; provider registry; health tracking; circuit breaker | `Registry`, `HealthTracker`, `ResolveRoute` |
| `internal/router/adapters` | Per-provider HTTP adapters (OpenAI, Anthropic, Azure) | `ProviderAdapter`, `OpenAIAdapter`, `AnthropicAdapter` |
| `internal/audit` | Async security event writes to `audit_events` | `Logger`, `Event`, `EventType` |
| `internal/storage` | Async usage writes to `usage_records` | `UsageRecorder`, `UsageRecord` |
| `internal/cost` | Per-token cost calculation from pricing config | `Calculator` |
| `internal/retry` | Exponential backoff with jitter; context cancellation monitoring | `Executor`, `ContextMonitor` |
| `internal/validation` | Request field validation with configurable limits | `Validator`, `Limits` |
| `internal/telemetry` | Prometheus metrics registration and recording | `Metrics` |
| `internal/config` | YAML config loading; hot-reload watchers | `Config`, `ModelsConfig`, `ProvidersConfig` |
| `internal/types` | Shared request/response types | `AegisRequest`, `AegisResponse`, `Classification` |
