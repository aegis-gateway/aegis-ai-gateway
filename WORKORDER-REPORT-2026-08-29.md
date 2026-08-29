# Work order report, 2026-08-29

Five phases, five commits, on `claude/aegis-codebase-review-nxtp3s`. Every phase ends
with `go build ./...` clean, `go vet ./...` clean, `go test ./...` passing, and the
database-gated tests passing against a live PostgreSQL 16.13.

| Commit | Phase |
|---|---|
| `ff960c6` | 1. Enforce the per-key model allowlist |
| `0cb3bee` | 2. Withdraw the dead `audit_logs` endpoint |
| `7972c5d` | 3. Stop logging provider error bodies verbatim |
| `48e1f09` | 4. Document the Rego deny-reason channel |
| `5089639` | 5. Attest permitted requests |

2,638 insertions, 143 deletions across 28 files.

**Test environment.** No Docker in this session, so I ran a local PostgreSQL 16.13
cluster on `127.0.0.1:5433` and pointed `TEST_DATABASE_URL` at it. The database-gated
tests in `internal/purge`, `internal/storage`, `internal/audit` and
`internal/audit/checkpoint` therefore ran rather than skipped, and the Phase 5 numbers
below are measured on it. For the canaries I built and ran a real gateway on port 8099
with `AEGIS_MOCK_PROVIDER=true` against that database, so the integration tests were
executed and not merely compiled. No Redis: the gateway logged
`redis not reachable (auth cache disabled)` and served requests, which is the documented
fail-open-when-unconfigured direction.

---

## Phase 1: enforce the per-key model allowlist

**Semantics found, and preserved.** `ListModels` at `internal/gateway/handler.go:500`
gated on `if len(authInfo.AllowedModels) > 0`, so **an empty allowlist permits every
configured alias**. That is preserved exactly. It also has to be: `cmd/keygen/main.go:96`
writes an empty JSON array for every key it issues, so reading empty as deny-all would
revoke every key in existence.

**What changed.** The predicate moved onto `AuthInfo.ModelAllowed`
(`internal/auth/context.go`) and both call sites use it, so a listing and an enforcement
carrying separate copies cannot drift. `ChatCompletions` consults it after the filter
chain and before `router.ResolveRoute`.

**Ordering, and why it is where it is.** Before routing, so a refused request reaches no
provider, is not priced, and opens no circuit. After the filters, because a request that
also carries a secret produces a `filter_block` event today, and checking the allowlist
first would replace that attested event rather than add to it. That is the same rule the
tool-capability refusal below it already states in its own comment.

**Refusal shape.** 503 `server_error` / `service_unavailable` with the same message the
classification-ceiling refusal produces, as specified. The audit row records the same 503.

**Audit event.** `audit.Logger.LogModelDenied` reuses `EventAuthFailure`, as instructed.
It does **not** reuse `LogAuthFailure`, which records `UnattributedOrg`: that constant is
the pre-identification sentinel, and `internal/audit/reader.go` explicitly refuses to
scope a query to it, so filing an identified caller's denial there would hide it from that
organization's own audit export.

**Tests.** `internal/gateway/model_allowlist_test.go`, table-driven over empty, nil,
matching, excluding, multi-entry, and the deprecated `aegis-gpt4` alias in both
directions. `TestModelAllowlist_ListAndCompletionAgree` pins the two call sites together.
Mutation-checked: with the enforcement disabled, the excluded-alias and deprecated-alias
cases return 200 from the mock provider and the agreement test reports
`/v1/models advertises aegis-balanced=false but /v1/chat/completions serves it=true`.

**Contradicting the brief.** `config.ModelMapping.DeprecatedAlias` is declared at
`internal/config/models.go:23` and **read by no code**. `aegis-gpt4` resolves on its own
primary and fallback rather than redirecting to `aegis-balanced`, so a key allowlisted for
`aegis-balanced` may not use it. The tests pin that behaviour rather than assuming a
redirect exists.

**Documents.** `docs/ARCHITECTURE.md` gains the enforcement step and states the empty-means-
unrestricted rule. `docs/COMPLIANCE-MAPPING.md` needed care: it pins every row to
`c74fa7a`, and enforcement does not exist in that tree. Rather than cite a commit where
the claim is false, the CC6.1 row cites paths and test names and the document now carries
a note saying that row is ahead of its pin and must not be quoted against it until
re-pinned.

**Not done, deliberately.** No Prometheus counter for this denial. Every sibling denial
path has one, and none fits, so adding one would have been a new metric outside the
phase's stated scope. **Recommend adding `aegis_model_denied_total{model}`**: without it
the denial is visible only by querying the audit trail.

---

## Phase 2: retire the dead `audit_logs` endpoint

`GET /aegis/v1/audit/logs` returns **410 Gone** with a body naming
`/aegis/v1/audit/events`. The refusal is returned before authentication and scoping,
because it is a statement about the resource and discloses nothing tenant specific; the
route sits behind `auth.Middleware` regardless.

The table stays, as instructed. `migrations/002_create_audit_logs.up.sql` gains a
deprecation header. That file has already applied everywhere it will apply, and
golang-migrate selects by version without checksumming contents, so a comment there is
inert; the header says so and says not to add DDL to it.

**Tests changed.** `TestAuditHandler_LogsSharesParameterHandling` is replaced by
`TestAuditHandler_LogsIsGone`, which asserts 410 and that the body names the replacement.
`TestAuditHandler_RefusesSentinelOrg` drops its `/logs` case: that endpoint no longer makes
a scoping decision, so there is none left there to get wrong.

**Follow-up recorded.** `docs/evidence/known-limitations.md` section 2.11 records that the
table and `audit.Reader.QueryLogs` remain, that three things still depend on the table
(`internal/purge` targets it by name, `schema_guard_test.go` asserts a purgeable time
column on it, and both the canary sweep and `TestNoPayload_AuditReadAPIStructs` would lose
a guard if `audit.LogRow` went away), and that removal is a separate change.

---

## Phase 3: stop logging provider error bodies verbatim

**Four sites, not two.** The brief named `streaming_enhanced.go:139` and
`openai.go:113`. `anthropic.go:137` and `mock.go:133` had the identical construction. A
repo-wide sweep for `string(body)` and `"body"` now returns only two unrelated hits
(`internal/auth/apikey.go:101`, `internal/audit/checkpoint/jcs.go:152`).

`adapters.RedactProviderError` replaces secret spans using the existing secrets scanner,
collapses C0 control characters and DEL to spaces, and truncates to 256 bytes on a rune
boundary, stating the full length. Redaction runs **before** truncation, so a secret
beginning inside the retained prefix and ending past it is replaced rather than clipped in
half and emitted; `TestRedactProviderError_SecretStraddlingTheBoundIsRedacted` pins that
ordering. The streaming path also reads the failed body under an 8 KiB limit, since it is
only used to build the excerpt.

Both log sites now carry `request_id`, `status`, the configured `provider` key and the
`adapter` type. `providerKey`, not `adapter.Name()`, which is shared across providers.

**Tests.** `internal/router/adapters/provider_error_test.go` covers the helper.
`internal/gateway/provider_error_log_test.go` asserts against the **emitted log record**,
decoding the JSON handler output line by line so that a value forging a record boundary
fails the test. Mutation-checked: with the redaction removed, the record carries a 28,132
byte body including `AKIAIOSFODNN7EXAMPLE` and the canary tail.

**Honest limit, stated in the code.** This is a reduction in what is disclosed, not a
guarantee. A 256 byte excerpt of provider text may still contain caller-derived words no
pattern matches. It is bounded, it cannot carry a payload, and it cannot forge a log
record. Anything stronger means not logging the body at all, which costs the ability to
diagnose a provider rejection.

---

## Phase 4: document the Rego deny-reason channel

Documentation only, as scoped. `docs/evidence/known-limitations.md` section 2.12 traces
the full path with file references, gives the correct pattern (match on content, return an
identifier), lists explicitly what is and is not safe to interpolate, and explains why the
three obvious enforcement options are each a design decision rather than a fix: a length
bound is already the bound, a denylist needs the request text at write time and puts
payload next to the thing it protects, and a registry of rule identifiers works but breaks
every existing custom bundle.

Warnings are placed where a rule author meets them: at the `PolicyMessage` definition in
`internal/filter/policy/opa.go`, and at the top of `configs/policies/default.rego`, which
is the file an operator copies first. The shipped RESTRICTED rule interpolates
`input.request.model`, an alias name from `configs/models.yaml` rather than caller text,
and now says so.

---

## Phase 5: attest allowed requests

### The 26 existing columns of `audit_events`

`id`, `request_id`, `timestamp`, `event_type`, `organization_id`, `team_id`, `user_id`,
`api_key_id`, `ip_address`, `user_agent`, `endpoint`, `method`, `status_code`,
`error_message`, `api_key_prefix`, `limit_dimension`, `limit_value`, `spent_cents`,
`limit_cents`, `filter_type`, `reason`, `provider`, `model`, `mode`, `operation`,
`error_detail`.

### What ships

`LogRequestComplete` and `LogProviderFailure` on `audit.Logger`, built from a
`CompletionEvent` struct. Emitted from the non-streaming path beside the `usage_records`
write, and from the streaming path exactly once. `auth_success` stays declared and
unemitted, as scoped.

Every field maps onto an existing column. **No migration, and no hash schema bump.**
`provider` takes the resolved provider key, `model` the concrete model from
`configs/models.yaml` (not the provider's echo of it, which is text an upstream controls),
`operation` distinguishes `chat_completion` from `chat_completion_stream`, and `reason` on
a failure takes one of six enumerated stage constants. `validFailureStage` rejects
anything else and records `unknown` rather than dropping the event: `reason` is covered by
the leaf hash and sealed, so a Go error string or provider text there is the section 2.12
mechanism arriving by another door.

A real sealed row, captured from the running gateway:

```
event_type      | request_complete
request_id      | req_allow_stream_canary_1788031053201063043
organization_id | canary-org
team_id         | canary-team
api_key_id      | 7a710546-b852-43e8-9f3d-eed498907796
status_code     | 200
provider        | anthropic
model           | claude-haiku-4-5-20251001
operation       | chat_completion_stream
```

### Confirmations the phase asked for

**Adding an event type requires no `hash_schema_version` bump. Confirmed empirically.**
The leaf hash covers a fixed *set of columns* (`internal/audit/checkpoint/event.go`), not
a set of values, so a new `event_type` value changes nothing. 540,500 events including
`request_complete` and `provider_failure` rows sealed at `hash_schema_version=2`, and
`verify-chain --full` reported `result: OK`.

**Single-writer under load.** Two sealers launched concurrently against a 40,000 event
backlog: one acquired the advisory lock and wrote 4 checkpoints, the other exited 1 with
`another sealer instance holds the advisory lock (key=4367013267506373021)`. Chain
verified afterwards.

**Gap refusal under load.** Deleting event 140001 immediately after the sealed range
produced `sealing paused: a gap separates the last checkpoint from the next visible
event`, exit code **1** (non-zero, so a cron run is visibly failing), the chain unchanged
at 14 checkpoints, and `unsealed events: 499`.

### Measured numbers

Measured, not estimated. PostgreSQL 16.13, `fsync=off`, local socket.

| Measurement | Value |
|---|---|
| Events sealed | 540,500 |
| Checkpoints written | 55 |
| Leaves per checkpoint | 10,000 (the default batch), last one partial |
| Checkpoint row size | **215 bytes, constant** at both min and max event count |
| `audit_checkpoints` total size | 112 kB |
| `audit_events` heap per row | 232 bytes |
| `audit_events` total size | 221 MB with indexes |
| Seal, 100,000 events | **2.92 s** (~34,000 events/s) |
| Seal, 400,000 events | **11.72 s** (~34,000 events/s) |
| Full verify, 540,500 events | **15.36 s** (~35,000 events/s) |

Sealing and verification are linear across a 4x change in corpus size, and a checkpoint
row is fixed width because it holds digests rather than content.

### Lag watermark and batching: recommendations, no defaults changed

**Leave `seal_lag_seconds` at 300 and `BatchSize` at 10000.** The watermark exists to let
in-flight transactions commit, and a higher row rate does not make a transaction take
longer, so the window is still the right size. A 10,000 event batch is roughly 0.3 s of
work at the measured rate.

**One threshold does need retuning, and it is not a default in this repository.**
`aegis_audit_unsealed_events` now sits at approximately request rate multiplied by 300 at
steady state, instead of near zero. At 100 requests per second that is a standing 30,000.
An alert tuned when only denials were recorded will fire continuously. This is recorded in
known-limitations 2.13.

### Loss visibility

**No counter existed.** The three `Audit*` collectors in `internal/telemetry/metrics.go`
are integrity gauges (seal age, unsealed count, oldest event age), none of which counts a
failed write. Added `aegis_audit_write_failures_total{event_type,reason}`, incremented on
a nil database handle and on an insert error, wired in `cmd/gateway/main.go` via
`auditLogger.SetMetrics(metrics)`. Both label values come from fixed sets, so neither can
be pushed on to mint time series.

The reason this matters is specific: a dropped write leaves no row, and because `BIGSERIAL`
allocates only on a successful insert it leaves no id gap either, so the sealer seals a
contiguous run and reports a healthy chain over an incomplete record. Nothing else in the
system can detect it.

### A real defect found while doing this

The client-disconnect watcher watched the same derived context as the total-stream
deadline, so `<-ctx.Done()` and `<-clientDisconnected` were both ready on a single signal
and `select` chose between them at random. **A client disconnect was reported as a total
timeout roughly half the time.** My exactly-once test caught it. The redundant goroutine
is removed and the branch now reads `ctx.Err()`: `DeadlineExceeded` is a timeout,
`Canceled` is a disconnect.

Related, and also fixed by `StreamMetrics.Outcome`: everything after
`streamWithMonitoring` previously ran identically for all six exits, recording
`StatusCode: 200` and a usage row for timeouts and disconnects alike.

### Conformance tests

`TestNoPayload_AllowPathCanary` and `TestNoPayload_AllowPathCanaryStreaming`, following
the block canary's structure, each with the load-bearing positive control. Both **were
actually run** against the live gateway and passed:

```
positive control satisfied: 1 request_complete row for req_allow_canary_...
allow path confirmed: request_complete row written, canary "CANARY_ALLOW_PAYLOAD_6b23f01d9e" absent
streaming allow path confirmed: 27 chunk(s), request_complete row written, canary absent
```

They also assert the mock provider's canned reply text is absent, so a completion event
recording what the model said fails too. All eight `NoPayload` tests pass together.

**CI grep: it did not cover the new names, and now does.** The step named only
`TestNoPayload_CanaryEndToEnd`. It now loops over all three, with `\b` so that
`TestNoPayload_AllowPathCanary` does not match `...CanaryStreaming` (verified against a
crafted log). I also added `AEGIS_MOCK_PROVIDER: "true"` to the Audit Conformance job env:
without it, CI holds no provider credential, the allow request would fail at the provider,
and both new canaries would fail. It does not weaken the block canary, which is refused
before routing either way.

---

## Stopped on, and why

**The five fields Phase 5 lists as minimum that have no column: the requested model alias,
the classification tier, the latency, and the prompt and completion token counts.**

Phase 5 says to add migration 014 if a necessary field has no column, and separately to
stop and report if a hash schema bump is required. Adding a *column* to `audit_events` is
exactly what requires one, and three independent sources in the repository say so:

- `internal/audit/checkpoint/event.go`: "Adding, removing or renaming any of them changes
  every leaf hash and therefore requires a version 3, not an edit here."
- `docs/adr/0011-tool-names-wait-for-a-hash-schema-bump.md`, status **Accepted**, which
  faced this exact question for tool names and deferred. It also **considered and
  rejected** the obvious escape (columns excluded from the leaf hash), on the grounds that
  a trail carrying data the checkpoint does not cover undermines the claim the checkpoint
  makes about everything else.
- Migration 013, which refuses to run while any version-1 checkpoint exists. A version 3
  inherits that problem: for a released product it means operators verify and archive
  their whole chain before upgrading.

There is also a hard constraint from standing rule 3. `HashSchemaVersion1` and
`HashSchemaVersion2` are declared in `api/controlplane/v1/hash.go`, and the sealer imports
`controlplanev1.HashSchemaVersion2`. A version 3 constant belongs in that package by the
established pattern, and that package is out of bounds.

So I shipped everything that does not depend on the answer. Allows and provider failures
are attested, with the fields that fit. The five that do not fit are in `usage_records`,
joinable on `request_id` and **not sealed**, and `docs/ARCHITECTURE.md`,
`docs/COMPLIANCE-MAPPING.md` and known-limitations 2.13 all say which half of an answer
came from where.

---

## What a human still needs to decide

1. **Cut `hash_schema_version=3`, or leave the attested record narrow?** The cost is what
   ADR 0011 describes: either the single-field-set property that makes verification simple
   enough to be credible to an external anchoring service, or a migration that forces
   operators to archive their chain. If it is cut, tool names (issue #38) and these five
   fields should ride it together rather than triggering two bumps.

2. **Is 503 the right status for an allowlist denial?** I implemented what the work order
   specified, matching the classification-ceiling refusal. It is worth a second look: 503
   invites a retry, and this denial is permanent. `handler.go` already reasons this way for
   `UnmappableError` ("Reporting it as a 500 tells an agent to retry a request that can
   never succeed"). 403 would be more honest; indistinguishability from the ceiling
   refusal is the argument for keeping 503.

3. **`aegis_audit_unsealed_events` alert thresholds** need retuning against the new
   baseline before the volume change reaches an environment with alerting.

4. **`config.ModelMapping.DeprecatedAlias` is dead.** Either implement the redirect or
   delete the field. Right now `aegis-gpt4` is a wholly separate alias and the YAML comment
   implies otherwise.

5. **`internal/gateway/integration_test.go` does not compile under `-tags=integration`**,
   and did not at the base commit `caa5e39` either (`undefined: config.RateLimitConfig`,
   plus a `cost.NewCalculator` signature change and a mock missing `SupportsStreaming`).
   CI never compiles it, because the only tagged run is scoped to `./internal/audit/...`.
   Pre-existing, out of scope here, and quietly rotting.

6. **Branch naming.** `CONTRIBUTING`-level convention in `CLAUDE.md` says branches are
   `feature/...`, `fix/...` or `chore/...` and must not be prefixed with the name of the
   tool that wrote them. My instructions pinned me to
   `claude/aegis-codebase-review-nxtp3s`. The instruction won; the conflict is worth
   resolving in one direction or the other.
