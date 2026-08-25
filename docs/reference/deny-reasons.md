# Deny reasons

Every refusal the gateway can return, what triggers it, and what to do about it.

Verified against commit
[`0344929`](https://github.com/aegis-gateway/aegis-ai-gateway/tree/0344929a98dae0377c0c974412d2ecdcf460a42a).
Strings are quoted literally from source. Where a message is a format string, the
literal template is given.

## The envelope

Every refusal uses the same OpenAI-shaped body
([`internal/httputil/errors.go`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/httputil/errors.go)):

```json
{
  "error": {
    "message": "<reason>",
    "type": "<error class>",
    "code": "<stable code>",
    "aegis_request_id": "req_..."
  }
}
```

`Content-Type: application/json` and an `X-Request-ID` header accompany every response.
`aegis_request_id` is omitted when empty. **Quote `aegis_request_id` in any support
request**, it is the join key to the audit trail.

## Status codes at a glance

| Status | Type | Meaning |
|---|---|---|
| 400 | `invalid_request_error` | Malformed or out-of-limits request |
| 401 | `authentication_error` | Key missing, malformed, or unknown |
| 402 | `budget_error` | Daily spend limit reached |
| 429 | `rate_limit_error` | Requests-per-minute limit reached |
| **451** | `content_filter_error` | **Blocked by a filter or by policy** |
| 500 | `server_error` | Internal fault |
| 503 | `server_error` | Dependency unavailable, failing closed |

> **451, not 403.** Content blocks return `451 Unavailable For Legal Reasons`. Clients
> that branch on `403` will not see a block.

---

## 1. Authentication: 401

Stage: [`internal/auth/middleware.go`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/auth/middleware.go).
All emit `type: authentication_error`, `code: invalid_api_key`.

| Literal string | Trigger | Operator action |
|---|---|---|
| `Missing Authorization header. Use: Authorization: Bearer <api-key>` | No `Authorization` header | Client bug. Add the header. |
| `Invalid Authorization format. Use: Authorization: Bearer <api-key>` | Header present but not `Bearer <key>` | Client bug. Check for a missing `Bearer ` prefix. |
| `Empty API key` | `Bearer` with nothing after it | Usually an unset env var interpolating to empty. |
| `Invalid API key` | SHA-256 of the key matches no active row in `api_keys` | Key is wrong, revoked, or expired. Reissue with `mise run keygen`. **The key is never logged**; identify it by `aegis_request_id`. |
| `Internal error during authentication` | Key store lookup failed (Redis **and** Postgres unreachable) | **Not a client error**, this is a 500. Check database and Redis health. |

## 2. Rate limit and budget: 429 / 402 / 503

Stage: [`internal/ratelimit/middleware.go`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/ratelimit/middleware.go).

| Literal string | Status / code | Trigger | Operator action |
|---|---|---|---|
| `Rate limit exceeded: %d requests per minute. Retry after %s` | 429 `rate_limit_exceeded` | Sliding-window RPM for the key exceeded | Expected under load. Back off until the RFC3339 time given. Raise the key's RPM if legitimate. |
| `Daily budget exceeded: spent %d of %d cents` | 402 `budget_exceeded` | Team's daily spend limit reached | Spend resets daily. Raise `daily_spend_limit_cents` on the key, or wait. |
| `Rate limiting service temporarily unavailable. Please try again in 30 seconds.` | 503 `service_unavailable` | **Redis configured but unreachable** | **This is a fail-closed refusal, not a fault in the request.** Restore Redis. Audited via `LogRedisFailure`; metric `redis_unavailable`. |
| `Budget tracking service temporarily unavailable. Please try again in 30 seconds.` | 503 `service_unavailable` | Same, on the budget path | As above. |

> **Fail-open vs fail-closed is deliberate and asymmetric.** Redis *not configured* →
> limits are skipped (fail open, for local development). Redis *configured but
> unreachable* → requests are refused (fail closed). Do not "fix" a 503 here by
> unsetting the Redis URL: that silently disables enforcement.

## 3. Validation: 400

Stage: [`internal/validation/validator.go`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/validation/validator.go),
plus two direct checks in the handler. Type `invalid_request_error`, code `invalid_request`.

Validation errors are `field: message`, joined with `; ` when several fail at once.

| Literal string | Trigger |
|---|---|
| `model is required` | `model` absent or empty |
| `messages is required` | `messages` absent or empty |
| `model name too long (max %d characters)` | Model name over the configured limit |
| `Invalid JSON: <parser error>` | Body is not valid JSON |
| `Failed to read request body` | Body could not be read (client disconnect, size cap) |

Fields validated with configurable limits: `model`, `messages`, `temperature`,
`max_tokens`, `top_p`, `stop`. Each failure also increments an invalid-field metric.

**Operator action:** all are client bugs. The message names the offending field.

## 4. Content filters: 451

Stage: the filter chain, [`internal/filter`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/filter/filter.go).
Order is **secrets → injection → PII**, stopping at the first block.
All emit `type: content_filter_error`, `code: content_blocked`.

| Literal string | Filter | Trigger | Operator action |
|---|---|---|---|
| `Request blocked: detected %d secret(s) of type: %s` | secrets | One or more credential patterns matched | **The `%s` is the pattern *name*** (e.g. `aws_access_key`), never the matched value. Rotate the leaked credential, then fix the caller. |
| `Request blocked: prompt injection detected (score %.2f)` | injection | Heuristic score ≥ `block_threshold` | Tune `block_threshold` / `flag_threshold` if legitimate traffic scores high. Scores between flag and block are flagged, not blocked. |
| `PII detected: %d entities found` | pii | Presidio found entities, and the request's classification makes the action a block | Count only, never the entities. Raise the key's classification or redact upstream. |
| `PII service not connected` | pii | gRPC channel to the filter service was never established | **Fail-closed refusal.** Start the service (`mise run services:up`) or disable the PII filter in config. |
| `PII service unavailable` | pii | gRPC call failed at request time | As above. Check the service on port 50051. |

> **No matched content appears in any of these strings.** Secrets emit a pattern name,
> injection a score, PII a count. This is what makes the message safe to log and to
> store in the audit trail, and it is asserted by
> `TestNoPayload_FilterResultStruct`.

## 5. Policy: 451

Stage: [`internal/filter/policy`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/filter/policy/opa.go).
Runs **after** routing, because it needs the resolved provider. OPA `v1.13.2`, Rego v1.

| Literal string | Trigger | Operator action |
|---|---|---|
| `Request denied by policy: <reason>` | A `deny` rule fired in the bundle | `<reason>` is the deny **message**, or several joined with `; `. See below. |
| `Policy evaluation failed: <error>` | The Rego query errored at evaluation time | **Fail-closed refusal.** Check the bundle. A failed *compile* leaves the last known-good query in place; this is an evaluation fault, which does not. |

### Deny reasons in the shipped bundle

[`configs/policies/default.rego`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/configs/policies/default.rego)
contains one deny message:

| Reason text | Condition |
|---|---|
| `RESTRICTED data cannot be routed through alias "<alias>": it is not cleared for RESTRICTED` | `classification == "RESTRICTED"` and the alias is not in `restricted_cleared_aliases` |

`restricted_cleared_aliases` is empty as shipped, because no route in
`configs/models.yaml` declares a `classification_ceiling` of `RESTRICTED`. Add an alias
there only once a route exists that is approved for it, and keep the two in step.

**When this rule actually fires.** Policy is evaluated after routing, because it needs the
resolved provider. On the configuration as shipped, no route declares a
`classification_ceiling` of `RESTRICTED`, so `routeEligible` skips every route and
`ResolveRoute` fails first: a `RESTRICTED` request receives `503 No provider available`
and never reaches this rule. It fires once an operator adds a route whose ceiling admits
`RESTRICTED`, at which point routing succeeds and the alias allowlist decides.

If you are debugging a refused `RESTRICTED` request, check the 503 path first.

> **Historical note, worth knowing if you inherit an older bundle.** This rule previously
> read `input.request.provider_type == "external"`. That field is populated from
> `adapter.Name()`, which reports the adapter implementation (`"openai"`, `"anthropic"`)
> and never a trust boundary: `azure_openai` and `internal_vllm` both route through the
> OpenAI adapter and both report `"openai"`. The rule compiled, read correctly, and could
> never fire, so the shipped bundle denied nothing. If you are carrying a local policy
> that tests `provider_type` against `"external"`, it is dead code. Gate on
> `input.request.model` or `input.request.classification` instead.

### The reason string carries messages, not rule names

`deny` is a set of strings, aggregated by `concat("; ", deny)`. Rego's
`deny contains msg if` rules are set-generating and are **not individually named**, so a
rule name never reaches the reason. A deny reason of the form `rule <name>` is not
something this engine can produce.

## 6. Routing: 503

Stage: [`internal/router`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/router/provider.go).
Type `server_error`, code `service_unavailable`.

| Literal string | Trigger | Operator action |
|---|---|---|
| `No provider available: no eligible provider for model %s at classification %s` | Every route for the alias was skipped | Three distinct causes, below. |

A route is skipped when: the model alias is unknown; the route's
`classification_ceiling` does not allow the key's classification; or the provider's
circuit breaker is open. **Check `/aegis/v1/health` first**, it reports circuit breaker
state per provider, which distinguishes "provider is down" from "key is not cleared for
this data".

`routeEligible` fails **open** on an unparseable request classification and **closed**
on an unparseable ceiling. That asymmetry is intentional.

## 7. Provider and streaming: 500 / 503

| Literal string | Status | Trigger | Operator action |
|---|---|---|---|
| `Failed to prepare provider request` | 500 | Adapter's `TransformRequest` failed | Gateway bug or malformed provider config. |
| `Provider request failed` | 503 | Upstream unreachable after all retries | Check provider status and credentials. Retries are exponential with jitter. |
| `Provider returned error` | 503 | Upstream returned an error status mid-stream | As above. |
| `Failed to process provider response` | 500 | `TransformResponse` failed | Usually an upstream response shape change. |
| `Streaming not supported` | 500 | `ResponseWriter` does not implement `http.Flusher` | A proxy in front of the gateway is buffering. Disable response buffering. |
| `Not authenticated` | 401 | Auth context missing at the handler | Should be unreachable; middleware runs first. Indicates a routing misconfiguration. |

---

## What is *not* here

**Response scanning does not exist.** Every filter above implements `ScanRequest` and
runs on the **inbound** request only. There is no `ScanResponse` in the `filter.Filter`
interface and no outbound filtering anywhere in the codebase. Model output is not
scanned for secrets, PII, or injection.

This is recorded because the project has previously shipped a security policy claiming
response scanning that did not exist. It still does not exist.
