# VERIFICATION.md

Verification of factual claims against the AEGIS gateway source.

**Commit under verification:** [`0344929a98dae0377c0c974412d2ecdcf460a42a`](https://github.com/aegis-gateway/aegis-ai-gateway/tree/0344929a98dae0377c0c974412d2ecdcf460a42a)
**Date:** 2026-08-23
**Verifier:** automated pass over the working tree at that commit.

All permalinks below pin that SHA. None point at `main`.

---

## 0. Scope and what could not be verified

This pass is **incomplete**, and the gaps are structural, not oversights. Read this
section before treating any verdict below as a launch decision.

### 0.1 `index.html` was never supplied

The brief specifies `index.html` as the primary verification target: one row per
factual claim on the landing page. **That file does not exist** in this repository,
in its git history, in either site repository, or anywhere on the session filesystem.
The same is true of `aegis-design-system.html`, `aegis-request-lifecycle.svg`, and
`aegis-site-plan.md`.

Consequence: this document verifies the **source-side ground truth** for every item
in brief section A2, and every capability claim in `README.md`. Where the brief
quotes the page directly (the endpoint list, the error envelope, the `filter.Result`
struct, the Rego sample, the provider and alias counts), those quotes are treated as
the claim under test and are verified. Claims that appear only on the page and are
not quoted in the brief are **unverified and unlisted**, because their text is unknown.

`index.html` cannot be corrected, because it does not exist to correct.

### 0.2 No tag exists

The brief specifies "a clean checkout of the tag being launched". `git tag` returns
empty; the repository has no tags at any point in its history. Verification was
performed against the branch head SHA above. **A launch tag must be created and this
pass re-pinned to it before any citation link is considered stable.**

### 0.3 The runtime verification items could not be executed

Brief items A2 (fail-closed Redis, "do not read this one, test it"), A2 (canary
end-to-end run) and **all of A3** require Postgres, Redis, and the demo stack.

The Docker daemon starts, but **image pulls are blocked by this session's egress
policy**: `docker pull postgres:16-alpine` returns `403 Forbidden` from the proxy,
and the local image cache is empty. The agent proxy documentation is explicit that a
403 is an organisation policy denial and must be reported rather than routed around.

Therefore the following are **NOT VERIFIED** and carry no verdict below:

| Brief item | What it needs | Status |
|---|---|---|
| A2, fail-closed on Redis | Live Redis, stop it, capture status + body + audit row | **Not executed**, no images |
| A2, canary end-to-end | Postgres + running gateway | **Not executed**, skips cleanly without env vars |
| A3, all six demos | Docker Compose stack | **Not executed**, no images |
| A3, real command output for the page | Running gateway | **Not executed** |
| A3, audit row capture | Postgres | **Not executed** |
| A3, `AKIA` grep over DB, logs, stdout | Running stack | **Not executed** |

The zero-hits `AKIA` claim is therefore **unverified by execution**. It is
*partially* supported by static analysis (§2.2) and by a well-constructed
integration test that was not run (§2.10). That is not the same thing, and the
brief is right that it should be tested rather than read.

Static analysis did **not** find any code path that writes matched secret text to a
log line, an audit row, or an error body. See §2.2. That is a necessary but not
sufficient condition for the claim.

---

## 1. README.md claims

| Claim (quoted) | Where | Verified against | Verdict |
|---|---|---|---|
| `` `/aegis/v1/health` `` \| No \| Health check | `README.md:203` | [`cmd/gateway/main.go:343`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/cmd/gateway/main.go#L343) | **confirmed**, registered unauthenticated, exactly this path |
| "View Prometheus metrics: `http://localhost:9090/metrics`" | `README.md:42` | [`cmd/gateway/main.go:176-178`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/cmd/gateway/main.go#L176-L178), [`configs/gateway.yaml:29`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/configs/gateway.yaml#L29) | **confirmed**, separate mux, `metrics_port: 9090` |
| `aegis-fast`, `aegis-balanced`, `aegis-reasoning` each route to a different provider | `README.md:39` | [`configs/models.yaml`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/configs/models.yaml) | **confirmed**, all three aliases exist |
| `aegis-gpt4` \| *(deprecated alias → aegis-balanced)* | `README.md:57` | [`configs/models.yaml:5`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/configs/models.yaml#L5) | **confirmed**, present and routable, correctly marked deprecated |
| README documents the policy engine, PII filtering, injection detection, rate limiting and audit logging | `README.md` | packages in §2.1 | **confirmed**, the historical omission called out in the brief has been fixed; all five are documented |

No capability claim in `README.md` was found to overstate the code.

---

## 2. Brief section A2: items flagged as written from a brief rather than source

### 2.1 Package paths

The brief lists nine paths and warns the page's capability table implies nine distinct
locations. It does not.

| Path as claimed | Exists? | Actual location | Verdict |
|---|---|---|---|
| `internal/auth` | yes | [`internal/auth`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/auth) | **confirmed** |
| `internal/validation` | yes | [`internal/validation`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/validation) | **confirmed** |
| `internal/filter` | yes | [`internal/filter/filter.go`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/filter/filter.go) | **confirmed** |
| `internal/policy` | **no** | [`internal/filter/policy`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/filter/policy) | **wrong**, there is no top-level `internal/policy`. The OPA evaluator is a **subpackage of filter**, and implements `filter.Filter` like any other filter |
| `internal/ratelimit` | yes | [`internal/ratelimit`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/ratelimit) | **confirmed** |
| `internal/router` | yes | [`internal/router`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/router) | **confirmed** |
| `internal/cost` | yes | [`internal/cost`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/cost) | **confirmed** |
| `internal/audit` | yes | [`internal/audit`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/audit) | **confirmed** |

**Finding.** The nine-distinct-locations implication is false in two ways:

1. `internal/policy` does not exist. Policy lives at `internal/filter/policy`.
2. The three content filters are **not** separate top-level packages either. Secrets,
   injection, PII and policy are all subpackages of `internal/filter`
   (`secrets/`, `injection/`, `pii/`, `policy/`), unified behind the `filter.Filter`
   interface and run by one `filter.Chain`.

A capability table that presents these as nine peer subsystems misrepresents the
architecture. They are one filter chain with four implementations, plus five
genuinely separate packages.

### 2.2 `filter.Result`: struct shape and the matched-string assertion

**Real definition**, [`internal/filter/filter.go:33-39`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/filter/filter.go#L33-L39):

```go
type Result struct {
	Action     Action
	FilterName string
	Message    string
	Detections int
	Score      float64
}
```

| Claim | Verdict |
|---|---|
| Struct has field `Action` | **confirmed** |
| Struct has field `Filter` | **wrong**, the field is `FilterName`, not `Filter`. Any page reproducing this struct with a `Filter` field is showing code that does not compile |
| Struct has fields `Message`, `Detections`, `Score` | **confirmed**, `Detections` is an `int` count, `Score` a `float64` |
| The matched string is not a field | **confirmed**, see below |

**The assertion holds, and it holds through the whole chain.** Verified at three levels:

1. **Struct level.** No field can carry matched text. `Detections` is a count.
2. **Every construction site.** All 13 non-test `filter.Result{...}` literals were
   inspected ([`policy/opa.go:248,256,263`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/filter/policy/opa.go#L248),
   [`pii/client.go:72,74,95,97,107,115,124`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/filter/pii/client.go#L72),
   [`secrets/scanner.go:84,97`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/filter/secrets/scanner.go#L84),
   [`injection/heuristic.go:89,98,105`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/filter/injection/heuristic.go#L89)).
   The only free-text field is `Message`, and every value assigned to it is built from
   **pattern names and counts**, never from the matched span. The secrets filter is the
   sharpest case: it deliberately collects `d.PatternName` into a `seen` set and formats
   `"Request blocked: detected %d secret(s) of type: %s"`, the type name, not the value.
3. **Every log and audit statement touching a filter.** The block path at
   [`handler.go:152-165`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/gateway/handler.go#L152-L165)
   logs `blocked.FilterName`, `blocked.Detections`, `blocked.Score` and passes
   `blocked.Message` to `LogFilterBlock`. Since `Message` provably contains no matched
   text, no matched text reaches a log line or an audit row by this path.

**Caveat, stated plainly:** this is static analysis. The runtime `AKIA` grep in A3 that
would confirm it empirically was not executed (§0.3).

### 2.3 The schema claim: **REPORTED LOUDLY, AS INSTRUCTED**

The claim under test: *no column in `audit_logs` or `audit_events` can hold prompt or
response text, including JSON, JSONB, bytea, or a generic metadata column.*

**Verdict: the claim as worded is overstated. Three columns are structurally capable of
holding payload text.**

`audit_logs` ([migration 002](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/migrations/002_create_audit_logs.up.sql)):

```sql
filter_results  JSONB NOT NULL DEFAULT '{}',
```

`audit_events` ([migration 005](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/migrations/005_create_audit_events.up.sql)):

```sql
user_agent      TEXT,
error_message   TEXT,
metadata        JSONB NOT NULL DEFAULT '{}'
```

There is **no `CHECK` constraint, no domain, and no trigger** on any of them. A future
caller, or a careless one today, can write an entire prompt into
`audit_events.metadata` and the database will accept it. `metadata` is even
GIN-indexed, so it is built for arbitrary structure.

**What is actually true**, and what the copy should say instead:

- No column is *named* for payload, and a static test enforces that (§2.10).
- No current code path writes payload into these columns. Notably, **`filter_results`
  is never written by any non-test code at this commit**, the column is unused.
- An end-to-end canary test asserts at runtime that a payload string reaches no audit
  row, JSONB included (§2.10).

So the guarantee is **behavioural and test-enforced, not structural**. That is a
meaningfully weaker statement than "no column can hold it", and the difference is
exactly the kind of thing an auditor is paid to find.

**This is a positioning decision, not a copy tweak, so per the brief it is reported
rather than worked around.** Options in §4.

### 2.4 The endpoint list

Claimed: `POST /v1/chat/completions`, `GET /v1/models`, `GET /healthz`, `GET /readyz`,
`GET /metrics`.

**Actual router**, [`cmd/gateway/main.go:337-351`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/cmd/gateway/main.go#L337-L351):

| Endpoint | Auth | Verdict |
|---|---|---|
| `GET /aegis/v1/health` | none | **missing from the page's list** |
| `POST /v1/chat/completions` | required | **confirmed** |
| `GET /v1/models` | required | **confirmed** |
| `GET /metrics` | none, **separate server on port 9090** | **misleading as listed** |
| `GET /healthz` | n/a | **wrong, does not exist** |
| `GET /readyz` | n/a | **wrong, does not exist** |

Three errors:

1. **`/healthz` and `/readyz` do not exist anywhere in the codebase.** A grep across all
   Go, YAML, and demo files returns nothing. The health endpoint is `/aegis/v1/health`,
   which the README documents correctly and the page omits.
2. **`/metrics` is not on the API server.** It is served by a second `http.Server` on
   `metrics_port` (default 9090), not on the main port 8080. Listing it beside
   `/v1/chat/completions` implies one origin; a reader following the page will get a
   connection refused.
3. The unauthenticated/authenticated split is not represented. Two of the three real
   endpoints require `Authorization: Bearer`.

### 2.5 The error envelope

Claimed: `403` with `{"error":{"message":"content blocked by secrets filter","type":"policy_violation"}}`.

**Actual**, [`internal/httputil/errors.go:41-66,88-90`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/httputil/errors.go#L41-L66):

- **Status code is `451`, not `403`.** `WriteContentBlockedError` hardcodes `451`.
- The envelope has **four** body fields, not two.
- `type` is `"content_filter_error"`, not `"policy_violation"`. There is no
  `policy_violation` type anywhere in the codebase.
- `code` is `"content_blocked"`.
- The message is the filter's `Result.Message`, whose real secrets-filter form is
  `Request blocked: detected N secret(s) of type: <pattern_name>`.

Corrected sample, for a request containing one AWS key:

```json
{
  "error": {
    "message": "Request blocked: detected 1 secret(s) of type: aws_access_key",
    "type": "content_filter_error",
    "code": "content_blocked",
    "aegis_request_id": "req_..."
  }
}
```

Response also carries `Content-Type: application/json` and an `X-Request-ID` header.

> The exact `message` value is reconstructed from the format string at
> [`secrets/scanner.go:97`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/filter/secrets/scanner.go#L97)
> and the pattern name in `patterns.go`. It is **not** captured from a live run (§0.3).
> The status code `451` is independently corroborated by the integration test constant
> `blockedStatus = 451` at
> [`no_payload_integration_test.go:40`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/audit/no_payload_integration_test.go#L40).

### 2.6 The Rego sample: **the sample and the deny chip both need changing**

| Claim | Verdict |
|---|---|
| `deny contains msg if` v1 syntax | **confirmed**, [`default.rego:3`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/configs/policies/default.rego#L3) has `import rego.v1`, and OPA is [`v1.13.2`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/go.mod#L10) |
| A rule named `deny_external_pii` | **wrong**, no such rule exists. The only deny rule in the bundle is an anonymous `deny contains msg if` block about RESTRICTED data |
| Deny reason reads `policy denial: rule deny_external_pii` | **wrong**, a rule name cannot reach the reason string |

**Why the rule name can never appear.** The bundle aggregates deny *messages*, not rule
identifiers ([`default.rego:21-23`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/configs/policies/default.rego#L21-L23)):

```rego
reason := concat("; ", deny) if {
	count(deny) > 0
}
```

`deny` is a set of strings. The Go side then formats
`"Request denied by policy: " + reason` at
[`opa.go:256-259`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/filter/policy/opa.go#L256-L259).
`deny contains msg if` rules are set-generating and are **not individually named** in
Rego, so no rule name is available to emit even in principle. Any copy of the form
`policy denial: rule <name>` describes a mechanism the engine does not have.

**Worse, and this is the real finding:** the one deny rule that does exist is
**unreachable dead code**.

```rego
deny contains msg if {
	input.request.classification == "RESTRICTED"
	input.request.provider_type == "external"
	...
}
```

`provider_type` is set from `adapter.Name()` at
[`handler.go:190`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/gateway/handler.go#L190),
which returns the adapter **type**, `"openai"` or `"anthropic"`, never `"external"`,
even for `azure_openai` and `internal_vllm`, which both fall through to the OpenAI
adapter. The condition can never be satisfied.

**Therefore the shipped default policy bundle can never deny anything.** A deny chip on
the landing page implies a live enforcement path that, on the default configuration,
does not fire. This is a launch blocker (§4).

### 2.7 Provider list and aliases

| Claim | Verdict |
|---|---|
| Exactly four providers | **confirmed**, `openai`, `anthropic`, `azure_openai`, `internal_vllm` in [`configs/providers.yaml`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/configs/providers.yaml) |
| Three aliases | **wrong, with a caveat**, [`configs/models.yaml`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/configs/models.yaml) defines **four**: `aegis-fast`, `aegis-balanced`, `aegis-reasoning`, and `aegis-gpt4` |
| No vendor model name reachable through the public API surface | **confirmed, with one caveat** |

On the alias count: `aegis-gpt4` is documented in the README as a deprecated alias to
`aegis-balanced`, so "three aliases" is defensible as a *product* statement. But it is a
real, routable entry that `GET /v1/models` returns. If the page says "three", a reader
calling `/v1/models` sees four. Say "three current aliases plus one deprecated".

On vendor names: `ListModels` at
[`handler.go:441-446`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/gateway/handler.go#L441-L446)
emits only the alias as `id` with `owned_by: "aegis"`. The upstream mapping is never
serialised. **The API surface is clean.**

The caveat is cosmetic but worth a decision: the alias `aegis-gpt4` **itself embeds a
vendor product name**, and it is returned by the public `/v1/models`. Strictly, a vendor
model name *is* reachable through the public API surface, as an alias string the
gateway itself chose. Deprecating it out of `models.yaml` would close this cleanly.

### 2.8 Observability

| Claim | Verdict |
|---|---|
| Prometheus via `client_golang` | **confirmed**, [`go.mod`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/go.mod), `promhttp.Handler()` at [`main.go:178`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/cmd/gateway/main.go#L178) |
| Structured logging via `slog` | **confirmed**, JSON handler, used throughout |
| OpenTelemetry is transitive only | **confirmed** |

Every `go.opentelemetry.io/*` entry in
[`go.mod:52-56`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/go.mod#L52-L56)
is marked `// indirect`, pulled in by OPA. There is **no** OTel tracer provider,
exporter, span, or configuration anywhere in the source.

No doc, comment, or config was found claiming native OTel support. `audit_logs` has a
`trace_id VARCHAR(100)` column, which is a plausible future hook but is not OTel
integration and is not described as such anywhere. **Nothing to flag**, but the page
must not imply tracing.

### 2.9 Fail-closed on Redis

**NOT VERIFIED, the brief explicitly required this be tested, not read, and it could
not be executed (§0.3).**

For completeness, the code implements the asymmetry as documented
([`internal/ratelimit/middleware.go:78-155`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/ratelimit/middleware.go#L78-L155)):
on a Redis failure with Redis configured, it emits `503` with
`"Rate limiting service temporarily unavailable. Please try again in 30 seconds."`,
records a `redis_unavailable` metric, and calls `auditLogger.LogRedisFailure`. The
budget path mirrors this.

**Reading the code is not the test the brief asked for.** Until it is run, "fail closed
on Redis" should not be stated as verified. This remains an open launch item.

### 2.10 The no-payload conformance test

**It exists, it is well built, and it asserts more than most such tests.**

Three tests, in two files:

| Test | File | What it actually asserts |
|---|---|---|
| `TestNoPayload_SchemaIntrospection` | [`internal/audit/no_payload_test.go:69`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/audit/no_payload_test.go#L69) | Scans **every** `*.up.sql`, extracts columns from `CREATE TABLE`/`ALTER TABLE` on `audit_*`, fails on payload-indicative **names** |
| `TestNoPayload_FilterResultStruct` | [`internal/audit/no_payload_test.go:160`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/audit/no_payload_test.go#L160) | Reflects over `filter.Result` and nested structs, fails on payload-ish field names or JSON tags |
| `TestNoPayload_CanaryEndToEnd` | [`internal/audit/no_payload_integration_test.go:55`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/audit/no_payload_integration_test.go#L55) | Sends a canary + fake AWS key, asserts `451`, asserts an audit row **was** written, then asserts the canary appears in no row |

Both static tests **pass** at this commit (`go test ./internal/audit/`, ok).

Genuine strengths worth citing on the page:

- The canary test proves by **contradiction, not absence**. Its positive control
  ("assert an audit row *was* written") means "canary not found" cannot be satisfied by
  an empty table. This is the failure mode that makes most zero-retention tests
  worthless, and it is explicitly handled.
- `assertCanaryAbsent` serialises **each row to JSON text**, so it catches JSONB
  columns, not just declared text columns.
- The schema test refuses to hardcode migrations 002 and 005, so a later
  `ALTER TABLE audit_logs ADD COLUMN payload TEXT` is caught.

**Two limits the page must not paper over:**

1. `TestNoPayload_SchemaIntrospection` checks column **names**, not types or contents.
   Its own comments concede this: `error_message`, `user_agent`, `filter_results` and
   `metadata` are on the allowed list. It does **not** support the §2.3 claim that no
   column *can* hold payload.
2. `TestNoPayload_CanaryEndToEnd` **skips cleanly** when `TEST_DATABASE_URL`,
   `TEST_SERVER_URL` or `TEST_API_KEY` are absent. In this session it skipped. A green
   `go test ./...` therefore does **not** mean the runtime guarantee was checked. CI must
   be confirmed to set those three variables, or the strongest evidence AEGIS has is
   silently not running.

---

## 3. Deny reason catalogue and provenance gap

Delivered separately:

- [`docs/reference/deny-reasons.md`](docs/reference/deny-reasons.md), every deny,
  refusal and policy-violation string the gateway can emit (brief A4).
- [`docs/evidence/known-limitations.md`](docs/evidence/known-limitations.md), the
  sealer provenance gap, confirmed (brief A5).

---

## 4. Launch blockers

Ranked. The first three are hard blocks.

### 4.1 The default policy bundle can never deny anything: **BLOCKER**

§2.6. `provider_type` is set from `adapter.Name()`, which returns `"openai"` /
`"anthropic"`, never `"external"`. The sole deny rule in `default.rego` is unreachable.
A page that shows a deny chip and a Rego sample is advertising enforcement that does not
fire on the shipped configuration.

Options: (a) fix `provider_type` to carry a genuine external/internal distinction and
keep the copy; (b) ship a default bundle with a rule that can actually fire; (c) change
the copy. Only (a) or (b) preserve the positioning. **This needs a decision, not a copy
edit.**

### 4.2 Zero-retention is behavioural, not structural: **BLOCKER for the current wording**

§2.3. Two unconstrained JSONB columns and two TEXT columns. The claim "no column can
hold prompt or response text" is not true as worded.

Options: (a) add `CHECK` constraints or a domain making it structurally true, then keep
the claim; (b) reword to the accurate and still-strong "no audit column is written with
payload, enforced by a static schema test and an end-to-end canary"; (c) drop
`filter_results` entirely, since nothing writes it. **(a) plus (c) is the strongest
outcome and is a small change.** Not softened here, per the brief.

### 4.3 The runtime verification never ran: **BLOCKER**

§0.3. Fail-closed-on-Redis, the canary end-to-end, all six demos, and the `AKIA` grep
were all blocked by egress policy. The brief is explicit that the Redis behaviour must be
tested rather than read, and that one `AKIA` hit anywhere is a blocker. **Neither
question is currently answered.** This needs an environment with Docker Hub access.

Also confirm CI actually sets `TEST_DATABASE_URL`, `TEST_SERVER_URL` and `TEST_API_KEY`,
or the canary test has been silently skipping (§2.10).

### 4.4 Page facts that are simply wrong: must be corrected before publish

- `/healthz` and `/readyz` do not exist (§2.4)
- `/metrics` is on port 9090, not the API port (§2.4)
- `/aegis/v1/health` is missing from the list (§2.4)
- Status is `451`, not `403`; type is `content_filter_error`, not `policy_violation`;
  two body fields missing (§2.5)
- `filter.Result.Filter` is really `FilterName` (§2.2)
- `deny_external_pii` does not exist and a rule name cannot reach the reason (§2.6)
- Four aliases, not three (§2.7)
- `internal/policy` does not exist; the filters are not nine peer packages (§2.1)

### 4.5 Process blockers

- **No launch tag exists** (§0.2). Every citation link is pinned to a branch head SHA,
  which is correct but not a launch artifact.
- **`index.html` was never supplied** (§0.1), so it could not be verified row-by-row or
  corrected. The verified ground truth above is ready to be applied to it.
