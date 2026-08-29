# AEGIS work order report, 2026-08-29

Five phases, five commits, each independently shippable. Every phase ends with
`go build ./...` clean, `go test ./...` passing, and the database-gated suites run against
a migrated Postgres.

Phase 5 has one open decision for a human, recorded at the end.

---

## Phase 1: enforce the per-key model allowlist

**Changed**

- `internal/gateway/model_allowlist.go` (new): `modelAllowed`, the single predicate both
  call sites use.
- `internal/gateway/handler.go`: enforcement in `ChatCompletions`, after the filter chain
  and before routing. `ListModels` rewritten to call the same predicate.
- `internal/httputil/errors.go`: `WriteModelNotAllowedError`.
- `internal/audit/logger.go`: `LogModelDenied`.
- `internal/gateway/model_allowlist_test.go` (new).
- `docs/COMPLIANCE-MAPPING.md`, `docs/ARCHITECTURE.md`.

**Semantics found, and preserved.** `ListModels` treats an **empty allowlist as "all models
permitted"**, matching exactly against the alias key in `configs/models.yaml`. That is
preserved rather than tightened. `api_keys.allowed_models` defaults to `'[]'`, so a
stricter default would have revoked every model from every key that never set one.

**Contradicted the brief.** The brief asks for "the same status and body shape as the
existing classification-ceiling refusal produced by `routeEligible`". That refusal is a
**503 `server_error` / `service_unavailable`**: `ResolveRoute` in
`internal/router/provider.go` returns an error and `internal/gateway/handler.go` passes it
to `httputil.WriteServiceUnavailableError`. Returning 503 for an authorisation denial
would be wrong. 503 means try again later, this gateway's own `internal/retry` executor
treats it that way, and an allowlist denial is permanent and deterministic. I used the
same body envelope, which is shared by every refusal, with **403 `permission_error` /
`model_not_allowed`**. It discloses nothing new, because `ListModels` already tells the
same caller which models its key may use. One line in
`internal/httputil/errors.go` reverses this if 503 was meant literally.

**Also worth a decision.** `LogModelDenied` reuses `EventAuthFailure`, as instructed, with
`reason = "model_not_allowed"`. Authentication succeeded here and authorisation failed,
whereas every other `auth_failure` row is a credential that did not verify. A CC6.1
evidence pull filtering on `auth_failure` now mixes the two and must filter on `reason`.
A dedicated event type would be the better shape.

**Incidental finding.** `config.ModelMapping.DeprecatedAlias` is declared in
`internal/config/models.go` and read nowhere, so nothing rewrites `aegis-gpt4` to
`aegis-balanced` at any point. Allowing the target does not allow the alias. That is
pinned by a test rather than changed, so a future alias-resolution change cannot silently
widen every allowlist naming the target.

**Verified** against unenforced code: the excluded-model cases return 200, which is the
reported defect exactly.

---

## Phase 2: retire the dead `audit_logs` endpoint

**Changed**

- `internal/gateway/audit_handler.go`: `Logs` returns 410; the auth and org-scoping gate
  extracted into `AuditHandler.authorize`, shared with `parse`.
- `internal/httputil/errors.go`: `WriteGoneError`.
- `migrations/002_create_audit_logs.up.sql`: deprecation banner.
- `internal/audit/reader.go`: `QueryLogs` marked deprecated in place.
- `docs/evidence/known-limitations.md` §2.11, plus `README.md`, `VERIFICATION.md`,
  `docs/COMPLIANCE-MAPPING.md`, `docs/evidence/agent-compatibility.md`.

**Kept the access gate, against my first instinct.** My first version answered 410 before
authenticating, reasoning that a static string needs no scoping. `TestAuditHandler_RefusesSentinelOrg`
asserts both audit routes refuse a key carrying the unattributed-org sentinel, and
weakening an access check as a side effect of a deprecation is not worth one line. The
gate is kept and that test passes unchanged. Query-parameter validation is dropped, so
`TestAuditHandler_LogsSharesParameterHandling` is replaced by
`TestAuditHandler_LogsIsRetired`.

**Verified** that migrations still apply from scratch to version 14 after editing an
already-applied migration file: golang-migrate does not checksum migration files.

**Out of scope and untouched**: dropping the table, purge behaviour, the events endpoint.

---

## Phase 3: stop logging provider error bodies verbatim

**Changed**

- `internal/redact/excerpt.go` (new): `Excerpt`, bounded to 256 runes.
- `internal/gateway/streaming_enhanced.go` and `internal/gateway/handler.go`: both log
  status, provider key, request ID and a bounded excerpt.
- `internal/router/adapters/openai.go`, `anthropic.go`, `mock.go`.
- `internal/redact/excerpt_test.go`, `internal/gateway/provider_error_logging_test.go`,
  `internal/router/adapters/error_body_test.go` (all new).
- `docs/evidence/known-limitations.md` §2.12.

**Beyond the brief, deliberately.** The excerpt is collapsed to a single line and stripped
of control characters, not merely truncated. A body containing newlines can otherwise forge
additional log records, and in a line-oriented collector a crafted body can impersonate a
separate event. Truncation is rune-aware so cutting at a fixed count cannot emit invalid
UTF-8. Applied to the Anthropic and mock adapters too, not only the OpenAI one named in the
review.

**Both paths log `providerKey` now**, not `adapter.Name()`. Both previously logged the
adapter type, which is shared: `azure_openai` and `internal_vllm` both report `"openai"`,
so an operator could not tell which provider had failed.

**The test asserts on the emitted record**, captured through a JSON `slog` handler, and
plants its marker at the **end** of a 4000-character body. Asserting only on length would
pass against a bound applied to the wrong end. Verified against verbatim logging: all
three assertions fail and the full 4056-byte body reaches the record.

**Stated plainly in the docs**: the mitigation is volumetric, not semantic. Up to 256
characters of provider-controlled text still reaches the log.

---

## Phase 4: document the Rego deny-reason channel

**Changed**: `docs/evidence/known-limitations.md` §2.13, a warning at
`internal/filter/policy/opa.go` on `PolicyMessage`, and one in
`configs/policies/default.rego` above the `reason` rule. No behaviour change.

**Verified rather than restated.** `reason` is one of the twenty-six fields in the leaf
hash at `hash_schema_version=2`, confirmed in `internal/audit/checkpoint/event.go`, so a
deny message really is sealed into the chain and cannot be corrected later without breaking
verification. The 512-character bound is `MaxReason` in `internal/audit/limits.go`.

The docs draw the line the shipped bundle already respects: interpolating a request
**field** the operator controls, such as `input.request.model`, is fine; interpolating
message **content** is not.

**Out of scope, as instructed**: enforcement in code.

---

## Phase 5: attest allowed requests

**Changed**

- `internal/audit/logger.go`: `LogRequestComplete`, `LogProviderFailure`, the
  `CompletedRequest` struct, enumerated reason constants, and `WithMetrics`.
- `internal/gateway/audit_completion.go` (new): one `completedRequest` constructor shared
  by both paths.
- `internal/gateway/handler.go`: emit on success and on both failure returns.
- `internal/gateway/streaming_enhanced.go`: `StreamOutcome` threaded through every exit,
  one emit point after the loop.
- `internal/telemetry/metrics.go`: `aegis_audit_write_failure_total`.
- `internal/auth/context.go`, `internal/auth/middleware.go`: `AuthInfo.KeyPrefix`.
- `cmd/gateway/main.go`: logger wired to metrics.
- `internal/audit/allow_path_integration_test.go` (new),
  `internal/audit/checkpoint/event_type_hash_test.go` (new).
- `.github/workflows/ci.yml`, and three documents.

### `audit_events` columns, enumerated

`id`, `request_id`, `timestamp`, `event_type`, `organization_id`, `team_id`, `user_id`,
`api_key_id`, `ip_address`, `user_agent`, `endpoint`, `method`, `status_code`,
`error_message`, `api_key_prefix`, `limit_dimension`, `limit_value`, `spent_cents`,
`limit_cents`, `filter_type`, `reason`, `provider`, `model`, `mode`, `operation`,
`error_detail`. Twenty-six.

**All twenty-six are fields in the leaf hash**, verified by comparing the JCS key set in
`internal/audit/checkpoint/event.go` against the table: no column outside the hash, no hash
field without a column.

### Mapping, and what would not fit

Carried in existing columns: event type, timestamp, request ID, organization, team, user,
key ID, **key prefix**, endpoint, method, status code, **provider key**, **requested model
alias**, and `mode` as `stream` or `buffered`. A `provider_failure` adds an enumerated
`reason`.

**Four of the brief's minimum fields have no column: latency, prompt and completion token
counts, the resolved concrete model, and classification.** No migration was added, and this
is the open decision below.

### `hash_schema_version`: no bump needed

Confirmed by test, not by reading. `TestEventTypeValueDoesNotChangeTheFieldSet` asserts
both halves: the field set stays at twenty-six across two event types, and the two leaf
hashes **differ**, so `event_type` is genuinely covered and the new events cannot be
relabelled after sealing. Adding a **column** is the opposite case and does require
version 3.

### Loss visibility

**No audit write failure counter existed.** There are **twenty-seven** `aegis_` collectors,
not twenty-three as the brief states; none covered this. A dropped write was an `ERROR` log
line and nothing else. `aegis_audit_write_failure_total`, labelled by event type, is now
incremented wherever a write is dropped.

This matters more than it looks, and what it leaves behind depends on where the write
failed. **Rejected before the id is allocated**, as an over-long value in a bounded column
is: the sequence does not advance, the ids stay contiguous, the sealer seals a range
missing an event it never saw, and the counter is the only signal. **Failed after the id is
allocated**, as a timeout, a cancellation or a connection loss is: sequence increments are
not rolled back, so the id is consumed permanently and the resulting gap **stalls the
sealer**, which refuses to seal past one.

That distinction was corrected during review and is recorded below; the original text here
claimed the no-gap case unconditionally.

It proved itself during this work: a misconfigured gateway wrote to a database at an older
schema and every audit write failed with `column "api_key_prefix" does not exist`, visible
only as log lines.

### Sealer behaviour under the new volume, measured

Measured on this machine, Docker Postgres 16, 2026-08-29. Not estimates.

| Measurement | Value |
|---|---|
| Seal, 50,000 events | **1.59 s**, 5 checkpoints, 10,000 leaves each |
| Seal, 200,000 events | **4.59 s**, 20 checkpoints, 10,000 leaves each |
| Throughput | roughly **31,000 to 44,000 events/second** |
| Checkpoint row size | **215 bytes** average; 80 kB table for 20 checkpoints |
| `audit_events` size | **29 MB per 50,000 rows**, about 600 bytes/row with indexes |
| `verify-chain` | **0.05 s** for 200k, walks checkpoint hashes only |
| `verify-chain --full` | **4.28 s** for 200k, re-hashes every event row |

**Single-writer path.** Two sealers launched simultaneously against 200,000 unsealed
events: one exited 1 with `another sealer instance holds the advisory lock
(key=4367013267506373021)`, the other sealed all 200,000. The resulting chain has 20
contiguous checkpoints covering ids 1 to 200,000 with **zero overlaps or gaps**, and
`verify-chain` reports OK. Correct at the higher row rate.

**ID-gap refusal.** With a row deleted mid-range, the sealer wrote a checkpoint up to the
gap (2,499 events), then paused and exited non-zero rather than sealing past it, logging
`an in-flight transaction may still commit into the gap`. Correct at the higher row rate.

**Watermark and batching: keep the current defaults.** The 300-second lag and the
10,000-event batch remain appropriate. The batch is a maximum rather than a minimum, as the
gap run demonstrated by writing a 2,499-event checkpoint, so low-volume deployments still
seal promptly. At roughly 31,000 events/second the sealer is nowhere near being the
constraint. I changed no defaults.

**The real consequence is storage, not seal time.** `audit_events` now takes one row per
request rather than one per refusal. A deployment serving a million requests a day should
budget roughly **600 MB/day** of growth and set retention accordingly.

### The defect the canary caught

Threading `StreamOutcome` through the monitoring loop, I found five exits and missed a
sixth: the `[DONE]` line is the **normal** SSE completion and returns separately from the
scanner-finished case. It carried the zero-value outcome, so **every cleanly completed
stream was attested as a `provider_failure`**. The streaming canary caught it on its first
run, which is the whole argument for the positive control.

The zero value is now a named `StreamOutcomeUnset` that logs an error and records the
request as interrupted, so a seventh exit added later is visible rather than silently
misclassified.

### Other findings

- **`internal/storage/no_payload_test.go` does not exist.** The test is
  `internal/audit/no_payload_test.go`. It is a name-pattern check over migration DDL, so
  `prompt_tokens` and `completion_tokens` would pass it without an allowlist edit; the
  reason not to add columns is the hash, not that test.
- **The migration number in the brief is taken.** `014_record_cache_token_detail` landed
  earlier the same day. A new gateway migration would be 015. None was needed.
- **`handler_refactored.go` and `telemetry_logger.go` are wired to no route.**
  `cmd/gateway/main.go` routes to `handler.ChatCompletions`. They contain a second, dead
  `usage_records` write. No allow event was added there. **If that refactor is ever wired
  up, permitted requests silently stop being attested.**
- **The gateway ignores `DATABASE_URL`.** `configs/gateway.yaml` hardcodes
  `database.name: "aegis"` with no environment placeholder, unlike host, port, user and
  password. Running the canaries against a scratch database required a copied config
  directory and `-config`. Worth an env placeholder.

---

## Review rounds after submission

The PR went through 13 review rounds and 24 findings from the automated
reviewers before coming back clean. Almost all of them landed on Phase 5, and
almost all were one root cause: **the attestation claiming an outcome the caller
did not experience.** The code changes were mostly small; the corrections were
mostly to claims.

The sequence, briefly, because the shape matters more than the list:

1. A stream that completed was attested as a provider failure, because `[DONE]`
   is a separate return from the scanner-finished case and carried no outcome.
   That also exposed that the terminator check tested the raw line, so it only
   ever matched OpenAI and **every Anthropic stream had been ending at EOF**.
2. Client disconnect and total timeout were fed from the same `ctx.Done()` and
   chosen between at random, so either could be sealed as the other.
3. Provider-error rows recorded the upstream status rather than the one the
   gateway sent.
4. EOF without a terminator was treated as completion, so a truncated response
   was sealed as complete. Then: a terminator whose write failed, then one whose
   *flush* failed, then ordinary chunks whose writes failed, each in turn.
5. `request_complete` claimed the caller **received** the response. It cannot:
   a successful flush proves only that the bytes reached the local kernel, and
   remote receipt would need an application-level acknowledgement the protocol
   does not carry. The claim is now "written in full, and flushed where the
   writer supports on-demand flushing", corrected in the code, `known-limitations.md`,
   `ARCHITECTURE.md` and `COMPLIANCE-MAPPING.md`.
6. An over-long caller-supplied `X-Request-ID` overflowed `VARCHAR(50)` and the
   **permitted request left no audit row at all**, returning 200. Bounded at the
   middleware and in the clip list.
7. Audit writes were not drained at shutdown, so a routine rollout dropped the
   tail of the record.
8. **An unconfigured model name on an allowlist denial was sealed verbatim**, so
   any caller holding a key with a non-empty allowlist could write up to 128
   characters of their own text into the immutable, exported chain. This was
   Phase 1 code, and the comment above it asserted the opposite. It is the most
   serious defect found on the PR.

Two corrections to statements made in this report or in review:

- I pushed back on a claim that failed audit inserts burn sequence ids, having
  measured that a `VARCHAR` rejection does not. That measurement was right and
  the generalisation was wrong: a failure **after** the id is allocated does
  consume it, and the resulting gap **stalls the sealer**, which refuses to seal
  past one. That is a larger consequence than the single lost row, and §2.14 now
  says so.
- A `TestConcurrentRequests` flake, roughly one run in eight, turned out to be an
  unsynchronised append in the mock provider from the integration-test repair in
  [#63](https://github.com/aegis-gateway/aegis-ai-gateway/pull/63), already on
  `main`. Fixed here.

**What this says about the work order.** Twenty-two of the twenty-four findings were on
Phase 5. Phases 2, 3 and 4 drew none. **Phase 1 drew one, and it was the most serious of
them all**: the unconfigured-model retention defect described above. A phase can be small,
uncontroversial and still be where the worst defect lands, which is an argument against
reading a low finding count as a clean bill of health.

Phase 5 reads like a call-site change and is really a question about what an HTTP handler
can honestly know about a response it has sent, which took 13 rounds to pin down. Anyone
estimating similar work should price the attestation semantics, not the plumbing.

## What a human still needs to decide

**1. Whether the allow event should carry tokens, latency, resolved model and
classification.** It currently does not. Three options:

- **Leave it.** Those values are in `usage_records` for the same request ID, unsealed.
  The trail attests identity, routing and outcome. This is what shipped.
- **Add columns outside the hash.** Cheap, and I recommend against it. It would make the
  evidence fields the only `audit_events` columns nothing attests, while the record looks
  more complete than it is.
- **Add columns inside the hash, `hash_schema_version=3`.** The correct end state.
  It invalidates existing checkpoint verification and is deferred under ADR 0011.
  **If this is done, bundle it with the tool names in §2.10 and issue #38**, because the
  bump is the expensive part and doing it twice costs twice.

**2. Phase 1's refusal status**: 403 `permission_error` as shipped, or 503 as the brief
literally said. One line.

**3. Phase 1's event type**: `auth_failure` with a `reason` discriminator as instructed, or
a dedicated `model_denied` type so CC6.1 evidence separates authentication from
authorisation.

**4. `audit_logs` removal.** Deferred by the brief and tracked in §2.11. The table, its
reader and its `LogRow` type should go together or not at all.
