# VERIFICATION.md

Verification of every factual claim on the landing page and in `README.md` against the
AEGIS gateway source.

**Baseline commit:** [`0344929`](https://github.com/aegis-gateway/aegis-ai-gateway/tree/0344929a98dae0377c0c974412d2ecdcf460a42a)
**Date:** 2026-08-23

All permalinks pin that SHA. None point at `main`. Where a finding has since been fixed
on this branch, the row says so and the baseline link still shows the defect.

## Status summary

| | Count |
|---|---|
| Claims confirmed | 19 |
| Claims wrong, corrected in `index.html` | 14 |
| Claims wrong, flagged for a decision rather than reworded | 3 (2 since resolved by building the missing capability) |
| Claims unverifiable in this environment | 4 |
| Launch blockers outstanding | 1 (the fail-closed Redis test and the six demos; see 5.4) |

---

## 0. Scope: what was and was not verifiable

### 0.1 Inputs

`index.html` was supplied mid-pass and is fully verified below. The corrected page is
committed to the site repository at
[`aegisgateway.ai/index.html`](https://github.com/aegis-gateway/aegisgateway.ai/blob/claude/aegis-verification-site-build-2g1ybj/index.html),
which is where the Astro port will consume it.

`aegis-design-system.html`, `aegis-request-lifecycle.svg` and `aegis-site-plan.md` were
**not supplied**. They are inputs to the site build, not to this verification, so their
absence does not limit anything below. The lifecycle diagram is inline SVG inside
`index.html` and was verified and corrected in place; the standalone `.svg` and any PNG
export of it still carry the old text and must be re-exported.

### 0.2 The launch tag

An earlier draft of this document said "no tag exists, `git tag` returns empty". That was
wrong, and wrong in a way worth recording: the working clone had never fetched tags, so a
local `git tag` was silently empty while the remote had one. That is the same class of
error as the shallow-clone false positives in §4.4, an environment artifact stated as a
fact about the repository.

What is actually true: a `v0.1.0` tag existed on `2809594a`, but it sat on
`chore/bump-vulnerable-deps` and was never on `main`. The two lines diverged at
`00b15bf2`, with 51 commits on that branch absent from `main` and 147 on `main` absent
from it, and trees differing across 207 files. It was a mis-tag on a feature branch.

By decision of the author it is being moved to `main` at `aea3168` as an annotated tag, so
that `v0.1.0` names the line that actually ships and matches the version every public
surface already claims.

### 0.3 What could not be executed

Docker image pulls are blocked by this environment's egress policy
(`docker pull postgres:16-alpine` returns `403 Forbidden`; the local cache is empty). The
proxy documentation is explicit that a 403 is an organisation policy denial to report
rather than route around.

**Update, 2026-08-25.** Network policy was widened. Docker Hub's blob CDN
(`production.cloudfront.docker.com`) is still denied, but `mirror.gcr.io` and
`public.ecr.aws` both serve the official images, which is enough. Most of this section has
now been executed against a real stack. See §5.

| Brief item | Status |
|---|---|
| A3, `AKIA` grep over DB, logs and stdout | **RUN. Zero hits, with a negative control.** See §5 |
| A3, real command output for the page | **CAPTURED.** See §5 |
| A3, audit row capture | **CAPTURED.** See §5 |
| A3, all six demos | **Still not run.** "All six demos are runnable" remains unverified |
| A2, fail-closed on Redis | **Still not tested.** The brief required testing rather than reading, and that stands |

**The canary is the exception.** It could not run here, but it demonstrably runs in CI on
every push, and the log evidence is in §2.10. The `AKIA` grep and the Redis test remain
genuinely open.

---

## 1. README.md

| Claim (quoted) | Where | Verified against | Verdict |
|---|---|---|---|
| `` `/aegis/v1/health` `` \| No \| Health check | `README.md:203` | [`main.go:343`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/cmd/gateway/main.go#L343) | **confirmed** |
| "View Prometheus metrics: `http://localhost:9090/metrics`" | `README.md:42` | [`main.go:176-178`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/cmd/gateway/main.go#L176-L178), [`gateway.yaml:29`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/configs/gateway.yaml#L29) | **confirmed** |
| `aegis-gpt4` \| *(deprecated alias → aegis-balanced)* | `README.md:57` | [`models.yaml:5`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/configs/models.yaml#L5) | **confirmed**, and more accurate than the page, which omitted it |
| "every denial ... is written to `audit_events` with request metadata, IP, and reason" | `README.md:149` | [`logger.go:92-99,186-202`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/audit/logger.go#L92-L99) | **confirmed**, exactly right |
| "Successful calls are recorded separately in `usage_records`" | `README.md:149` | [`internal/storage`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/storage) | **confirmed** |
| "**LiteLLM and Bifrost** route traffic and report what it cost." | `README.md:5` | Shared rule 3 | **RULE VIOLATION**, see below |
| "Teams that need SSO, multi-tenant policy management ... **can access** the AEGIS control plane." | `README.md:221` | Shared rule 6 | **RULE VIOLATION**, see below |

No capability claim in `README.md` overstates the code. Two shared-rule violations do
need fixing, and neither is a code question:

**Rule 3, naming a competitor.** Line 5 names two competing projects directly. The rule
is "never name a competitor in any user-facing text; frame the tension generically." The
landing page gets this right ("Gateways that monetize payload logging cannot make this
claim without deleting their core feature"), so the fix is to bring the README into line
with the page.

**Rule 6, commercial tier tense.** Line 221 says teams "can access the AEGIS control
plane", present tense, for something the page correctly labels "Not yet built". The
control plane repository exists but the tier does not.

Both are left for a decision rather than silently rewritten, per rule 9. They are copy
positioning, not defects in the code.

> `README.md` contains **no** zero-retention claim. Of the four surfaces named as
> carrying the falsified sentence, the README is not one of them: it never makes the
> claim. The other three are all inside `index.html` and are corrected in §3.1.

---

## 2. Source-side verification

### 2.1 Package paths

| Path as claimed on the page | Verdict |
|---|---|
| `internal/auth` | **confirmed** |
| `internal/validation` | **confirmed** |
| `internal/filter` | **confirmed** |
| `internal/policy` | **wrong.** No such package. Policy lives at [`internal/filter/policy`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/filter/policy) |
| `internal/ratelimit` | **confirmed** |
| `internal/router` | **confirmed** |
| `internal/cost` | **confirmed** |
| `internal/audit` | **confirmed** |

Two structural errors in the capability table, both corrected:

1. `internal/policy` does not exist. The OPA evaluator is a **subpackage of filter** and
   implements `filter.Filter` like any other filter.
2. The page attributed **classification gating** to `internal/policy` as well.
   Classification gating is not policy at all: it is
   [`router.routeEligible`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/router/provider.go),
   which skips any route whose `classification_ceiling` sits below the caller's
   clearance. Two separate mechanisms were shown as one package that does not exist.

The nine-distinct-locations implication is also false: secrets, injection, PII and policy
are four subpackages of `internal/filter` behind one `filter.Chain`, not four peers.

### 2.2 `filter.Result`

Real definition, [`filter.go:33-39`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/filter/filter.go#L33-L39):

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
| Fields `Action`, `Message`, `Detections`, `Score` | **confirmed** |
| Field named `Filter` | **wrong.** It is `FilterName` |
| The matched string is not a field | **confirmed at three levels** |

The page's *prose* description (receipt 02: "an action, a filter name, a message, a
detection count, and a score") is accurate. Only a struct reproduction would show the
wrong field name, and the supplied page does not reproduce the struct, so nothing needed
correcting here.

The no-matched-string assertion holds through the whole chain:

1. **Struct.** No field can hold matched text; `Detections` is a count.
2. **All 13 non-test construction sites.** The only free-text field is `Message`, and
   every value is built from pattern names and counts. The secrets filter is the sharp
   case: it collects `d.PatternName` into a set and formats
   `"Request blocked: detected %d secret(s) of type: %s"`, the type name, never the value
   ([`scanner.go:84-101`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/filter/secrets/scanner.go#L84-L101)).
3. **Every log and audit statement.** The block path
   ([`handler.go:152-165`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/gateway/handler.go#L152-L165))
   logs `FilterName`, `Detections`, `Score`, and passes `Message` to `LogFilterBlock`.

**Runtime corroboration.** The CI gateway log for the canary request shows exactly this
and nothing more:

```
{"level":"WARN","msg":"request blocked by filter","request_id":"req_canary_1787366159120745809",
 "filter":"secrets","detections":1,"score":0,"org_id":"ci-org"}
```

The AWS key that provoked the block does not appear. This is the one part of the `AKIA`
sweep that CI already covers; the database and stdout sweep is still open.

### 2.3 The schema claim

The page claimed: *`audit_logs` and `audit_events` have no column capable of holding
prompt or response text. Not a redacted column, not a nullable one, **not a JSON blob
that could quietly hold it**.*

**Verdict: wrong as worded.** Four columns are structurally capable of holding payload.

[Migration 002](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/migrations/002_create_audit_logs.up.sql):

```sql
filter_results  JSONB NOT NULL DEFAULT '{}',
```

[Migration 005](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/migrations/005_create_audit_events.up.sql):

```sql
user_agent      TEXT,
error_message   TEXT,
metadata        JSONB NOT NULL DEFAULT '{}'
```

No `CHECK` constraint, no domain, no trigger on any of them. `metadata` is GIN-indexed,
so it is built for arbitrary structure. The sentence explicitly denying a JSON blob is
the most precisely wrong part of the page.

**What is true, and is a strong claim in its own right:**

- No current code path writes payload into these columns.
- `filter_results` is written by **nothing at all** at this commit. The column is dead.
- A static test rejects payload-indicative column names in any migration (§2.10).
- An end-to-end canary asserts at runtime that a payload string reaches no audit row,
  JSONB included, and it runs in CI on every push (§2.10).

The guarantee is **behavioural and test-enforced, not structural**. Corrected on the page
per the agreed wording, with the schema work tracked separately in §4.2.

### 2.4 Endpoints

The brief anticipated an endpoint list on the page. **There is none**, so nothing needed
correcting. The ground truth, for the docs site:

| Endpoint | Auth | Server |
|---|---|---|
| `GET /aegis/v1/health` | none | main, :8080 |
| `POST /v1/chat/completions` | required | main, :8080 |
| `GET /v1/models` | required | main, :8080 |
| `GET /metrics` | none | **separate server, :9090** |

`/healthz` and `/readyz` **do not exist** anywhere in the codebase. `README.md` documents
the real health endpoint correctly.

### 2.5 The error envelope

The page showed `403` with
`{"error":{"message":"content blocked by secrets filter","type":"policy_violation"}}`.

**Wrong in four ways**
([`errors.go:41-90`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/httputil/errors.go#L41-L90)):

1. Status is **451**, not 403.
2. `type` is `content_filter_error`. There is no `policy_violation` type in the codebase.
3. `code` is `content_blocked`, a field the page omitted entirely.
4. `aegis_request_id` is also returned, and is the join key to the audit trail.

The page's message string, `content blocked by secrets filter`, is close to the
`error_message` written to the **audit row** (`Content blocked by secrets filter`) rather
than the API body. The two are different strings and the page used the wrong one.

Corrected block now on the page:

```json
{"error":{"message":"Request blocked: detected 1 secret(s) of type: aws_access_key",
          "type":"content_filter_error",
          "code":"content_blocked",
          "aegis_request_id":"req_..."}}
```

**451 is runtime-confirmed**, not read from source: the CI canary asserts
`gateway blocked the request with HTTP 451 as expected` against a live gateway (§2.10).
**Superseded by §5: the envelope is now observed, and the reconstruction was wrong in one
detail.** The real message is
`Request blocked: detected 1 secret(s) of type: AWS Access Key`. This document previously
guessed `aws_access_key`, taking the pattern's identifier rather than the human-readable
name the scanner carries, and the corrected landing page carried that same wrong value
until the live run exposed it. Reconstructing a string from a format specifier gets the
shape right and the content wrong, which is exactly the failure mode running it fixes.

### 2.6 The Rego sample and the deny chip

| Claim | Verdict |
|---|---|
| `deny contains msg if` v1 syntax | **confirmed.** `import rego.v1`, OPA [`v1.13.2`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/go.mod#L10) |
| `package aegis.authz` | **wrong.** The real package is `aegis.policy` |
| `input.filters.pii.detections` | **wrong.** `PolicyInput` has no `filters` field |
| `data.providers[input.route.provider].eu_hosted` | **wrong.** No `route` field, and no such data document |
| A rule named `deny_external_pii` | **wrong.** No such rule |
| Deny reason reads `policy denial: rule deny_external_pii` | **wrong.** A rule name cannot reach the reason |

The real input document is
`{user:{id,org,team}, request:{model,classification,provider_type}, messages, time:{hour,day}}`
([`opa.go:225-243`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/filter/policy/opa.go#L225-L243)).
The sample referenced three paths that do not exist, so it could not have fired as
printed.

**Rule names cannot reach the reason string.** The bundle aggregates deny *messages*:
`reason := concat("; ", deny)`. `deny contains msg if` rules are set-generating and are
not individually named in Rego, so no rule name is available to emit. Copy of the form
`policy denial: rule <name>` describes a mechanism the engine does not have.

**And the shipped rule was dead.** The sole deny rule in `default.rego` required
`input.request.provider_type == "external"`. That field is set from `adapter.Name()`
([`handler.go:190`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/gateway/handler.go#L190)),
which returns the adapter implementation, `"openai"` or `"anthropic"`, and never
`"external"`. `azure_openai` and `internal_vllm` both route through the OpenAI adapter and
both report `"openai"`. **The shipped default bundle could not deny anything.**

> **FIXED on this branch.** `configs/policies/default.rego` now gates on the model alias
> against an operator-controlled allowlist, empty as shipped. A new regression test,
> `TestShippedDefaultPolicy_CanActuallyDeny`, loads the real bundle and asserts it denies;
> it fails against the old rule and passes against the new one. Every other test in that
> file built its own inline fixture, which is how the dead rule survived.
>
> Worth recording, and stated carefully because an earlier draft of this document got it
> wrong: no route in `configs/models.yaml` declares a `classification_ceiling` of
> `RESTRICTED`, so the router already refused these requests. The dead rule was both
> unreachable **and** redundant.
>
> The new rule is **not** reachable on the shipped configuration either, for a different
> reason: policy is evaluated after routing, because it needs the resolved provider, so
> `ResolveRoute` fails first and returns `503 No provider available` before the rule is
> consulted ([`handler.go:178-181`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/c74fa7a13930b8fe0322b3eea067c8c78ba42e21/internal/gateway/handler.go#L178-L181)).
> It fires once an operator adds a route whose ceiling admits `RESTRICTED`.
>
> That is still a real improvement on what it replaced, which could not fire under **any**
> configuration. It is not the improvement an earlier draft claimed, which was that the
> rule converts that 503 into a 451. It does not. Caught in review, and removed from the
> policy comment, this document and the deny-reason catalogue.

### 2.7 Providers and aliases

| Claim | Verdict |
|---|---|
| Exactly four providers | **confirmed.** `openai`, `anthropic`, `azure_openai`, `internal_vllm` |
| "Three aliases" | **incomplete.** Four exist: `aegis-fast`, `aegis-balanced`, `aegis-reasoning`, `aegis-gpt4` |
| No vendor model name reachable through the public API surface | **confirmed** |

`aegis-gpt4` is documented in the README as deprecated, so "three" is defensible as a
product statement, but it is routable and `GET /v1/models` returns four. The demo's own
run script advertises it. Page corrected to say three current plus one deprecated.

On vendor names: `ListModels`
([`handler.go:441-446`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/gateway/handler.go#L441-L446))
emits only the alias as `id` with `owned_by: "aegis"`. The upstream mapping is never
serialised. The API surface is clean. The one cosmetic caveat is that the alias
`aegis-gpt4` embeds a vendor product name and is returned publicly; deprecating it out of
`models.yaml` would close that.

### 2.8 Observability

| Claim | Verdict |
|---|---|
| Prometheus via `client_golang` | **confirmed** |
| Structured logging via `slog` | **confirmed**, JSON handler |
| OpenTelemetry is transitive only | **confirmed** |

Every `go.opentelemetry.io/*` entry in
[`go.mod:52-56`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/go.mod#L52-L56)
is `// indirect`, pulled in by OPA. No tracer provider, exporter, span, or configuration
exists. `audit_logs.trace_id` is a plausible future hook, not OTel integration, and is not
described as such anywhere. **Nothing on the page implies tracing**, so nothing to flag.

### 2.9 Fail-closed on Redis

**NOT VERIFIED.** The brief required testing rather than reading, and the stack could not
be started (§0.3).

The code implements the documented asymmetry
([`middleware.go:78-155`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/ratelimit/middleware.go#L78-L155)):
Redis configured but unreachable yields `503`, a `redis_unavailable` metric, and a
`LogRedisFailure` audit row. Redis *not* configured skips limits entirely, which is the
fail-open development path.

The page states this correctly in "It does not fail open." **Reading the code is not the
test that was asked for**, and this stays open.

### 2.10 The no-payload conformance test

**It exists, it is well built, and it runs in CI on every push.**

| Test | File | Asserts |
|---|---|---|
| `TestNoPayload_SchemaIntrospection` | [`no_payload_test.go:69`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/audit/no_payload_test.go#L69) | Scans every `*.up.sql`; fails on payload-indicative column **names** in `audit_*` |
| `TestNoPayload_FilterResultStruct` | [`no_payload_test.go:160`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/audit/no_payload_test.go#L160) | Reflects over `filter.Result`; fails on payload-ish field names or JSON tags |
| `TestNoPayload_CanaryEndToEnd` | [`no_payload_integration_test.go:55`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/audit/no_payload_integration_test.go#L55) | Sends a canary, asserts `451`, asserts a row **was** written, then asserts the canary is in no row |

**Run-history evidence.** CI run
[`32546647855`](https://github.com/aegis-gateway/aegis-ai-gateway/actions/runs/32546647855),
job "Audit Conformance", on this exact baseline commit:

```
=== RUN   TestNoPayload_CanaryEndToEnd
    gateway blocked the request with HTTP 451 as expected
    positive control satisfied: 1 filter_block row(s) written for req_canary_1787366159120745809
    zero-retention confirmed: audit row written for req_canary_1787366159120745809,
      canary "CANARY_PAYLOAD_8f4a2b91e3c7d056" absent from all audit rows
--- PASS: TestNoPayload_CanaryEndToEnd (0.21s)
```

A dedicated job provisions Postgres and Redis, runs migrations, issues an API key, starts
a real gateway, and sets all three variables. **The canary has been running all along.**

Why the test is stronger than most of its kind:

- It proves by **contradiction, not absence**. The positive control means "canary not
  found" cannot be satisfied by an empty table, which is the failure mode that makes most
  zero-retention tests worthless.
- `assertCanaryAbsent` serialises **each row to JSON text**, so JSONB columns are covered.
- The schema test refuses to hardcode migrations 002 and 005, so a later
  `ALTER TABLE audit_logs ADD COLUMN payload TEXT` is caught.

**Two real gaps, both now closed on this branch:**

1. The canary **skipped silently** when its variables were absent. A green pipeline where
   it skipped looked identical to one where it passed.
   > **FIXED.** It now fails with a message naming the missing variables. The only way to
   > not run it is `AEGIS_SKIP_INTEGRATION=1`, which is explicit and visible in output.
   > The `integration` build tag already means nobody compiles it by accident.
2. The CI step trusted the **exit code**. `go test -run` exits 0 when the pattern matches
   nothing, so renaming the test would have left the step green having run nothing. The
   `[no tests to run]` line that `internal/audit/checkpoint` already prints in that step
   is that failure mode in its harmless form.
   > **FIXED.** The step now greps for `--- PASS: TestNoPayload_CanaryEndToEnd` by name
   > and fails if it is absent.

**One limit that remains, and must not be papered over:**
`TestNoPayload_SchemaIntrospection` checks column **names**, not types or contents. Its
own comments concede this: `error_message`, `user_agent`, `filter_results` and `metadata`
are on the allowed list. It does **not** support the §2.3 claim that no column *can* hold
payload. Only the canary speaks to runtime behaviour, and only for the paths it exercises.

---

## 3. index.html

### 3.1 The zero-retention sentence, on every surface that carried it

Three surfaces inside `index.html` carried the falsified claim. All three are corrected.

| # | Surface | Was | Now |
|---|---|---|---|
| 1 | Receipt 01 | "The schema has nowhere to put a prompt ... not a JSON blob that could quietly hold it" | "No audit column is written with payload", citing both test names |
| 2 | Ledger `.neg` closing note | "No column exists that could hold it, so no configuration flag ..." | "None of it is written, and two conformance tests fail the build if that changes" |
| 3 | Diagram never-panel (inline SVG) | "No column exists that could hold it, so no config flag ..." | "None of it is written, and two conformance tests fail the build if that changes" |
| 4 | Step 4 trailing note | "because no column exists that could have held it" | "because nothing on the write path ever carries it" |

Receipt 01's citation moved from `migrations/` to
`internal/audit/no_payload_test.go`, per the decision to cite the test rather than the
schema. After the schema work in §4.2 lands, both can be cited.

**Closed.** The standalone `aegis-request-lifecycle.svg` was never supplied, but it was
not needed: the inline SVG is self-contained, so it was extracted to
[`assets/aegis-request-lifecycle.svg`](https://github.com/aegis-gateway/aegisgateway.ai/blob/claude/aegis-verification-site-build-2g1ybj/assets/aegis-request-lifecycle.svg)
and rendered to PNG at 2x, both carrying the corrected content.

A copy of the stale diagram was reviewed and confirmed to carry four falsified elements:
the `deny_external_pii` chip, the "No column exists that could hold it" never-panel, an
audit column list in which **not one of the eleven names exists in either table**, and the
audit read API line. **Any copy of the diagram outside the site repository is stale and
must be replaced from `assets/`.**

`docs/COMPLIANCE-MAPPING.md` remains referenced inside the diagram. That is deliberate:
removing it quietly would resolve a flagged blocker by editing the evidence rather than
deciding what to do about it. A true Open Graph image is still outstanding, because the
diagram is 1280x930 against OG's roughly 1200x630, so it needs composition rather than a
crop, and that needs the design system.

### 3.2 Factual corrections applied

| Claim as published | Reality | Status |
|---|---|---|
| `git clone https://github.com/atlantic-frontier/aegis.git` | Repo is `aegis-gateway/aegis-ai-gateway` | **corrected**, 4 occurrences |
| `cd aegis/demos/00-quickstart` | Directory is `aegis-ai-gateway/` | **corrected** |
| `docker compose up` | Quickstart README documents `./run.sh` | **corrected** |
| No mention of a provider key | `run.sh` refuses to start without `OPENAI_API_KEY` or `ANTHROPIC_API_KEY` | **corrected.** "Four commands" as published could not work |
| `Authorization: Bearer $AEGIS_API_KEY` | `run.sh` issues the fixed demo key `aegis-demo-quickstart` | **corrected**, 2 occurrences |
| `# 403` + 2-field body | 451, `content_filter_error`, `content_blocked`, 4 fields | **corrected** |
| `select * from audit_logs order by ts desc` | Wrong table **and** wrong column. A filter block writes to `audit_events`; there is no `ts` column | **corrected** |
| Audit row fields: `principal`, `model_alias`, `outcome`, `deny_reason`, `filter_name`, `filter_action`, `detection_count`, `score`, `policy_decision`, `cost`, `latency_ms` | **None of these columns exist** in either table | **corrected** to the real row shape |
| Ledger "Written to the audit store", same invented names | As above | **corrected**, both in HTML and in the diagram |
| `policy_decision, rule_name` | No such columns, and a rule name cannot be produced at all | **corrected** |
| Deny chip `policy denial: rule deny_external_pii` | Not a string the gateway can emit | **corrected** to real literals, in HTML and diagram |
| Rego sample, `package aegis.authz` with `input.filters` / `input.route` | Package is `aegis.policy`; neither input path exists | **corrected** to a rule that compiles and fires |
| Capability table, `internal/policy` ×2 | Package does not exist; classification gating is in the router | **corrected** |
| "Three aliases" | Four, one deprecated but routable | **corrected** |
| Footer links `href="#"` ×7, citations `href="#"` ×5 | No targets | **corrected** to pinned-SHA permalinks where a target exists |

### 3.3 Flagged for a decision, not reworded

Per rule 9, these were positioning failures rather than copy errors, so each was marked
with a `BUILD NOTE, BLOCKER` comment in the corrected file and left otherwise intact.

**Two of the three have since been resolved by building the missing thing** rather than
by cutting the copy. They are kept below as findings, with their resolution, because this
record should show what was wrong at the baseline and what was done about it. One
remains.

**1. "The audit read API" does not exist.** The page claims, twice, that records are
"readable and exportable as JSON or CSV through the audit read API", and lists it among
capabilities that are "already free". The gateway serves three routes plus `/metrics`.
Neither this repository nor `aegis-control-plane` contains any audit read endpoint, JSON
export, or CSV export path. This is a rule 1 violation and cannot ship as written.
Options were: build the endpoint, or cut the clause and describe reading the tables
directly.

> **RESOLVED.** The endpoint was built: `GET /aegis/v1/audit/events` and
> `GET /aegis/v1/audit/logs`, authenticated and organization-scoped, with `?format=csv`.
> See [`internal/audit/reader.go`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/c74fa7a13930b8fe0322b3eea067c8c78ba42e21/internal/audit/reader.go)
> and [`internal/gateway/audit_handler.go`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/c74fa7a13930b8fe0322b3eea067c8c78ba42e21/internal/gateway/audit_handler.go).
> The `BUILD NOTE, BLOCKER` markers are removed and the claim now has code behind it.

**2. `docs/COMPLIANCE-MAPPING.md` does not exist.** Cited three times: as a receipt in the
governance section, inside the diagram, and in the footer. Two passages tell a compliance
team to start from it. A dead receipt is worse than no receipt, and this one is load
bearing for the "we do not claim compliance, we produce evidence" position. Options were: write the document, or cut all three citations.

> **RESOLVED.** The document was written:
> [`docs/COMPLIANCE-MAPPING.md`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/c74fa7a13930b8fe0322b3eea067c8c78ba42e21/docs/COMPLIANCE-MAPPING.md).
> All three citations resolve.

**3. Fonts load from the Google Fonts CDN.** Three tags in `<head>` fetch IBM Plex and
Source Serif from `fonts.googleapis.com` and `fonts.gstatic.com`, sending every visitor's
IP to a third party on first paint. The footer's "no third-party analytics and no tracking
cookies" stays literally true, but the site build requires self-hosted, subset fonts, and
this is the first thing a hostile reader checks on a data-minimisation argument. Flagged
for the Astro port; it needs the font files, which were not supplied.

### 3.4 Unverified claims left standing

| Claim | Why it stands |
|---|---|
| "All six demos are runnable" | Could not run any (§0.3). Plausible, unconfirmed |
| Digital Omnibus dates and article numbers | Regulatory facts, outside the scope of a source verification. The page frames them correctly, explicitly disclaiming deadline urgency, which satisfies rule 5 |
| "An offline sealer seals events into Merkle checkpoints that chain to each other" | **confirmed** in [`internal/audit/checkpoint`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/audit/checkpoint) (RFC 6962 Merkle root, `prev_checkpoint_hash` chain) |
| "It does not scan model responses" | **confirmed**, and correctly stated as a limitation. Only `ScanRequest` exists in the `filter.Filter` interface |

---

## 4. Launch blockers

### 4.1 The runtime verification, **PARTLY RESOLVED**

The `AKIA` sweep has been run, with a negative control, and returned zero hits across the
database, the gateway log, both container logs and the Redis keyspace (§5.3). The refusal
and the audit row were captured from behaviour (§5.1, §5.2). That closes the part the
landing page was blocked on.

**What remains, and still blocks a claim each:** the fail-closed Redis test, which the
brief explicitly required be tested rather than read, and the six demos behind the
"all six demos are runnable" line on the page.

### 4.2 The zero-retention guarantee is not yet structural

The copy now states what is true, so this no longer blocks publication. The schema work
agreed for v0.1.0, in the order decided:

1. **Drop `filter_results`.** Unused by any code path. Nothing to migrate.
2. **Remove or type the JSONB.** `audit_events.metadata` currently carries
   `{"filter_type","reason"}` from `LogFilterBlock`. Both are short and known, so they
   should be real columns and the JSONB should go. A `CHECK` on JSONB is the weakest
   option available: a key denylist is bypassed by renaming the key, and an allowlist
   needs an `IMMUTABLE` function because `CHECK` cannot hold a subquery, which relocates
   the guarantee into a function someone can alter.
3. **Bound the TEXT columns.** `error_message` and `user_agent` are short by nature.
   `varchar(128)` cannot hold a prompt, and a reader who knows nothing about the codebase
   can confirm that in ten seconds. This is the change that actually makes the claim
   structural.
4. **Constraints last**, as a backstop for anything left over.

Cannot be validated without a database, so it lands after §4.1 is unblocked.

### 4.3 Unsupported claims on the page, **RESOLVED**

The audit read API and `docs/COMPLIANCE-MAPPING.md` were both rule 1 violations at the
baseline: the page claimed capabilities that did not exist. Both were resolved by building
the missing thing rather than by cutting the copy (§3.3), so neither blocks launch.

The third item from §3.3 remains, and it is a port task rather than a launch blocker: the
page as supplied loads its fonts from the Google Fonts CDN. The site build self-hosts them,
and the built output makes no off-origin request.

### 4.4 Process

- **`v0.1.0` now names `main` at `aea3168`** (§0.2), replacing a mis-tag that sat on a
  feature branch. Citations across this repository and both sites are still pinned to
  `c74fa7a` and should be re-pinned to the tag. The citation checker fails on any that do
  not resolve, so the re-pin is verifiable.
- **`README.md` carries two shared-rule violations** (§1): a named competitor, and the
  commercial tier in present tense.
- **Any copy of the lifecycle diagram held outside the site repository is stale** (§3.1).
  The corrected SVG and PNG are in `aegisgateway.ai/assets/`, and were regenerated again
  on 2026-08-25 after the live run corrected the pattern name inside the diagram. An Open
  Graph card now exists at `aegisgateway.ai/public/assets/og.png`.

---

## 5. Runtime verification, executed 2026-08-25

The half of the brief that could not run when this document was first written. Executed
against `main` at `aea3168`, tagged `v0.1.0`.

**Stack.** Postgres 16 and Redis 7 as containers, pulled through `mirror.gcr.io` because
Docker Hub's blob CDN is still denied by egress policy. Migrations applied to version 11.
A key issued with `cmd/keygen` for `verify-org`. The gateway run from source with
deliberately fake provider credentials, since the request under test is refused before
routing and must never reach a provider. This mirrors the CI Audit Conformance job, which
is a configuration already known to work.

### 5.1 The refusal, observed

Request: `POST /v1/chat/completions`, model `aegis-fast`, content
`Deploy with AKIAIOSFODNN7EXAMPLE`.

```
HTTP 451
{
    "error": {
        "message": "Request blocked: detected 1 secret(s) of type: AWS Access Key",
        "type": "content_filter_error",
        "code": "content_blocked",
        "aegis_request_id": "req_akia_sweep_1787689981"
    }
}
```

Confirms, from behaviour rather than source: the status is **451** and not the `403` the
page claimed; the type is `content_filter_error` and not `policy_violation`; `code` and
`aegis_request_id` are both present, and the page omitted both.

**One correction to this document's own earlier work.** The message names the pattern as
`AWS Access Key`. §2.5 had reconstructed it as `aws_access_key` from the format string,
and the corrected landing page carried that wrong value until this run.

### 5.2 The audit row, captured verbatim

```
id              1
request_id      req_akia_sweep_1787689981
timestamp       2026-08-25 20:33:01.492124+00
event_type      filter_block
organization_id verify-org
team_id         verify-team
api_key_id      d84ae83a-4cac-4c39-b1af-99b35750fe50
ip_address      127.0.0.1:48640
endpoint        
method          
status_code     451
error_message   Content blocked by secrets filter
metadata        {"reason": "Request blocked: detected 1 secret(s) of type: AWS Access Key", "filter_type": "secrets"}
```

This is the **positive control**: an audit row demonstrably exists for this request, so
the absence of the key below is a fact about the write path rather than about an empty
table.

Two details worth noting, both now reflected on the page. `endpoint` and `method` are
genuinely empty, because `LogFilterBlock` does not set them. `ip_address` carries
`host:port` from `RemoteAddr`, not a bare address.

The `metadata` JSONB holds the filter's own message: a pattern name and a count. Not the
matched value.

### 5.3 The AKIA sweep

The claim is zero hits. The result is zero hits.

| Surface | Method | `AKIA` hits |
|---|---|---|
| Entire database | `pg_dump` of all 9 tables, schema and data | **0** |
| Gateway stdout and stderr | full log, 9 lines | **0** |
| Postgres container log | `docker logs` | **0** |
| Redis container log | `docker logs` | **0** |
| Redis keyspace | every key scanned, keys and values | **0** |

The block as the gateway logged it, carrying a filter name, a count and a score, and no
matched text:

```json
{"level":"WARN","msg":"request blocked by filter","request_id":"req_akia_sweep_1787689981",
 "filter":"secrets","detections":1,"score":0,"org_id":"verify-org"}
```

**Negative control, because a zero from a broken sweep is worthless.** A row was inserted
into `audit_events.metadata` containing `AKIAIOSFODNN7EXAMPLE`, the dump retaken, and the
grep returned **1 hit**, proving the method detects payload inside a JSONB column. The row
was then deleted and the dump returned to **0**.

So the zero is a measurement, not an absence of measurement.

### 5.4 What this does and does not establish

It establishes that on this stack, for this request, the key reached no persisted surface
and no log. It does not establish anything about the streaming path, about a successful
request that reaches a provider, or about the other five demos, none of which were run.

Still outstanding, unchanged: the fail-closed Redis test, which the brief required be
tested rather than read, and the six demos.
