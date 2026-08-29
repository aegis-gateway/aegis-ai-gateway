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

### 5. Provider routing (`internal/router`)

`router.ResolveRoute` maps the requested model alias to a concrete `(provider, model)` pair using `configs/models.yaml`. The routing algorithm:

1. Look up the alias in `ModelsConfig.Models`.
2. Try the **primary** route: verify the route's `classification_ceiling` permits the request's classification tier and the provider's circuit breaker is closed.
3. If the primary is ineligible or unhealthy, try each **fallback** route in order.
4. If no route is found, return HTTP 503.

Classification levels are ordered `PUBLIC < INTERNAL < CONFIDENTIAL < RESTRICTED`. A route with `classification_ceiling: CONFIDENTIAL` accepts PUBLIC, INTERNAL, and CONFIDENTIAL requests but rejects RESTRICTED ones.

### 6. OPA policy evaluation (`internal/filter/policy`)

After routing (so `provider_type` is known), `policy.Evaluator.ScanRequest` builds a `PolicyInput` struct and evaluates it against compiled Rego modules querying `data.aegis.policy.allow` and `data.aegis.policy.reason`. If OPA returns `allow = false`, the request is blocked with HTTP 451 and a `filter_block` event is written to `audit_events`.

The policy evaluator fails closed: if no policies are loaded or evaluation times out (default 100 ms), the request is blocked.

### 7. Provider HTTP call (`internal/router/adapters`)

The resolved `adapters.ProviderAdapter` transforms the `AegisRequest` into the provider's native format and sends it. For non-streaming requests the call is wrapped in `retry.Executor`, which retries on 5xx, 429, 408, and network errors using exponential backoff with jitter (default: 2 retries, initial 100 ms, max 5 s).

Supported adapter types:
- `openai` — OpenAI chat completions API; also used for vLLM
- `anthropic` — Anthropic Messages API; response is normalized to OpenAI format
- `azure_openai` — Azure OpenAI deployments

For streaming requests (`stream: true`), `StreamingHandler.HandleStream` forwards SSE chunks directly to the client with per-chunk and total timeouts.

### 8. Response and cost calculation (`internal/cost`, `internal/gateway`)

The adapter's `TransformResponse` normalizes the provider response into `types.AegisResponse`. The `cost.Calculator` looks up per-token prices from `configs/models.yaml` and computes an estimated USD cost from prompt and completion token counts.

### 9. Audit write (`internal/audit`, `internal/storage`)

Two writes happen asynchronously (non-blocking) after the response is sent:

- **`audit.Logger`** — writes security-relevant events to `audit_events`: refusals (auth
  failures, filter blocks, rate limit and budget violations, pricing denials, model
  allowlist denials) and, since 2026-08-29, permitted requests as well. A request that
  passes every gate and completes writes `request_complete`; one that passes every gate
  and then fails at the provider writes `provider_failure`. Before that the sealed chain
  attested what was refused and nothing about what was allowed.

  An allow event carries identity, outcome and routing: request ID, organization, team,
  user, key ID and key prefix, endpoint, method, status code, the configured provider key,
  the requested model alias, and whether the response was streamed. It does **not** carry
  latency, token counts, the resolved concrete model, or classification: there are no
  columns for those, and adding one would require a `hash_schema_version` bump. Those
  fields live in `usage_records` for the same request ID, which is not sealed. See
  [known limitations §2.14](evidence/known-limitations.md).
- **`storage.UsageRecorder`** — writes per-request token and cost data to `usage_records`.

Neither write blocks the response path. Failures are logged but do not affect the client.

---

## Data Persistence

### `api_keys`

Created by the `keygen` CLI tool. Stores a SHA-256 hash of the key (never the plaintext), along with organization/team/user attribution, classification ceiling, optional model allowlist, rate limits, and lifecycle timestamps (created, expires, last used, revoked).

`allowed_models` is a permission, enforced by `modelAllowed` in
`internal/gateway/model_allowlist.go` on both `POST /v1/chat/completions` and
`GET /v1/models`. An **empty list permits every model**, which is the stored
default; a populated list is matched exactly against the alias in
`configs/models.yaml`, not against the resolved provider model. A request for an
alias outside the list is refused with 403 before routing, and the refusal is
written to `audit_events`.

### `audit_logs`

**Created by migration 002 but never written.** No component in the codebase
inserts into this table — a repository-wide search finds it only in the migration.
The schema anticipates per-request records (duration, status code, identity, model
requested vs. served, provider, tokens, cost, filter results, routing attempts),
but nothing populates it, so it stays empty. Per-request data lives in
`usage_records` instead.

### `audit_events`

Written by `audit.Logger`. Five event types are actually emitted, all of them
denials or failures: `auth_failure`, `rate_limit_violation`, `budget_violation`,
`filter_block`, `redis_failure`. The `auth_success`, `provider_failure`, and
`request_complete` constants are declared in `internal/audit/logger.go` but no
method emits them, so **successful requests produce no audit event**. Each row has
a structured `metadata` JSONB column for event-specific context (e.g. filter type,
spend amounts).

Writes are fire-and-forget (`Log()` spawns a goroutine) and are skipped silently
when the database handle is nil, so audit capture is best-effort rather than
guaranteed.

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
