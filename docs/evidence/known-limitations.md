# Known limitations of the evidence AEGIS produces

This page states, plainly, what the audit trail does **not** establish. It exists so
that an operator building an evidence package finds these limits here rather than
discovering them during an audit.

Verified against commit
[`0344929`](https://github.com/aegis-gateway/aegis-ai-gateway/tree/0344929a98dae0377c0c974412d2ecdcf460a42a).

---

## 1. A checkpoint attests event integrity, not policy provenance

### The gap

A decision record names the rule that fired. It does **not** bind that decision to the
exact policy bundle and configuration in force at the moment it was made.

Concretely: a checkpoint proves that a set of audit events existed, in that order,
unaltered since sealing. It does **not** prove which version of `default.rego` was
loaded when those events were produced, nor which gateway configuration was active.

### Confirmed, and deliberate

Nothing in the gateway computes a configuration digest. There is no configuration hash
anywhere in the repository. Rego bundles are compiled from `configs/policies/`, but
they are **neither versioned nor digested**.

The control plane protocol reserves both field names,
[`ConfigHash` and `PolicyBundles`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/api/controlplane/v1/checkpoint.go#L160-L174)
in `api/controlplane/v1`, but their semantics are **unspecified in v1**, no v1 emitter
may populate them, and validation **actively rejects** a submission that does:

```
config_hash is reserved in v1: its semantics are unspecified and no v1 emitter may populate it
```

This is a considered decision, recorded in
[ADR 0004](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/docs/adr/0004-reserved-fields-must-not-be-populated.md).
Declaring the fields merely *optional* would invite a future emitter to fill them with
something plausible, letting whoever populated them first define their meaning inside
what is supposed to be a contract. Reserving them keeps the name available for a v2 that
defines the semantics properly, while ensuring **nothing may be read into a v1
submission about the configuration in force when its events were produced**.

The docstring states the limit in one line, and it is the honest summary:

> A v1 checkpoint attests event integrity, not policy provenance.

### What this means in practice

**What a v1 checkpoint does support:**

- That a given decision was recorded, with its request ID, timestamp, outcome, and the
  deny reason text.
- That the event sequence is unaltered since sealing, via a Merkle root (RFC 6962) and
  a hash chain linking each checkpoint to its predecessor.
- Inclusion proofs for any individual event within a sealed range.

**What it does not support:**

- "This decision was made under policy bundle version X." The bundle is not versioned.
- "The gateway configuration was unchanged across this reporting period." No digest is
  computed, so no comparison is possible.
- Reconstructing, from the evidence alone, why a rule that exists today did or did not
  fire on a request from six months ago.

### Compensating controls available today

Until a v2 defines these fields, operators needing configuration provenance should bind
it externally:

1. Keep `configs/` under version control and record the deploying commit SHA in your
   release process. The gateway logs its `version` at startup.
2. Treat policy changes as deployments, so the deployment record is the provenance
   record.
3. Correlate by time: checkpoints carry `covered_from` / `covered_to`, which can be
   intersected with a deployment timeline. Note these are **intervals to be unioned,
   not a partition**, so an event's covered range may overlap several checkpoints.

None of these are as good as a digest in the payload. They are what is available.

### Roadmap position

Deferred to **control plane protocol v2**. Both field names are already claimed, so
defining them is an addition rather than a breaking change. Two pieces of work are
required, and the protocol is the smaller of them:

1. **In the gateway:** compute a stable digest over the loaded configuration, and
   version and digest each compiled Rego bundle. This does not exist in any form today.
2. **In the protocol:** define the semantics of `config_hash` and `policy_bundles` in
   v2, and lift the v1 validation rejection for v2 emitters.

`PolicyBundleRef` already anticipates the shape: a name **and** a content digest,
because a name can be reused for changed content, and an evidence bundle assembled years
later needs to say which bytes were in force.

### This has no bearing on the zero-retention claim

Stated explicitly, because the two are easy to conflate.

The provenance gap is about **what a decision record can be bound to**. Zero-retention is
about **what a decision record contains**. They are independent:

- The audit trail stores metadata only. That property is enforced by
  `TestNoPayload_SchemaIntrospection`, `TestNoPayload_FilterResultStruct`, and the
  `TestNoPayload_CanaryEndToEnd` canary test, none of which involve configuration
  hashing.
- Adding a config hash and bundle versions in v2 would add **more metadata** about the
  gateway, not any data about the request. It would not weaken zero-retention, and it
  does not currently strengthen it.

An auditor should treat these as two separate questions, and this page as the answer to
only the first.

---

## 2. Related limits worth stating in the same breath

These are not the provenance gap, but an operator reading this page will want them.

### 2.1 Requests are scanned; responses are not

Every filter implements `ScanRequest` and runs on the **inbound** request only. There is
no `ScanResponse` in the
[`filter.Filter`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/internal/filter/filter.go#L43-L47)
interface and no outbound filtering in the codebase. **Model output is not scanned** for
secrets, PII, or injection.

This is recorded prominently because the project has previously published a security
policy claiming response scanning that did not exist.

### 2.2 The default policy bundle denies on alias, not on provider trust

`configs/policies/default.rego` keeps RESTRICTED data off any alias not listed in
`restricted_cleared_aliases`, which is empty as shipped.

It deliberately does **not** test `input.request.provider_type`. That field is populated
from `adapter.Name()`, which reports the adapter implementation (`"openai"`,
`"anthropic"`) and never a trust boundary: `azure_openai` and `internal_vllm` both route
through the OpenAI adapter and both report `"openai"`. **There is currently no input field
that distinguishes an external provider from a self-hosted one.** Any rule of the form
`provider_type == "external"` compiles, reads correctly, and can never fire. An earlier
version of this bundle contained exactly that rule, so the shipped default denied nothing
at all until it was replaced.

If you need a genuine external/internal distinction in policy, encode it in alias names
and gate on `input.request.model`, as the default bundle now does.

### 2.3 Zero-retention is enforced behaviourally, and now partly structurally

`audit_logs.filter_results`, `audit_events.metadata` (both `JSONB`),
`audit_events.error_message` and `audit_events.user_agent` (both `TEXT`) carry **no
`CHECK` constraint, domain, or trigger**. Nothing in the schema prevents a future caller
from writing payload into them.

The guarantee rests on: no current code path writing payload there
(`filter_results` is written by nothing at all at this commit), a static test rejecting
payload-indicative **column names**, and the end-to-end canary asserting at runtime that
a payload string reaches no audit row, including JSONB, which the canary catches by
serialising each row to JSON text.

That is a real and testable guarantee. It is **not** the same as the schema making
payload storage impossible, and it should not be described as though it were.

### 2.4 The canary test runs in CI, and can no longer skip quietly

`TestNoPayload_CanaryEndToEnd` runs on every push, in a dedicated "Audit Conformance" job
that provisions Postgres and Redis, runs migrations, issues an API key, and starts a real
gateway.

It previously skipped when `TEST_DATABASE_URL`, `TEST_SERVER_URL` or `TEST_API_KEY` were
absent, which meant a pipeline where it skipped looked identical to one where it passed.
It now **fails** with a message naming the missing variables; the only way to not run it is
`AEGIS_SKIP_INTEGRATION=1`, which is explicit and appears in the test output. The CI step
additionally greps for `--- PASS: TestNoPayload_CanaryEndToEnd` by name, because
`go test -run` exits 0 when its pattern matches nothing, so a rename would otherwise have
left the step green having run nothing.

A conformance test that can silently not run is worse than no test, because it
manufactures confidence. Both routes to that outcome are now closed.

### 2.5 Losing the Redis *address* is invisible on the health endpoint

The rate limiter's fail direction depends on which of two things went wrong, and the
asymmetry is deliberate: Redis configured but unreachable **fails closed**, and Redis not
configured at all **fails open** so a developer can run the gateway without one.

Both directions were tested on 2026-08-25 and both behave as documented
(`VERIFICATION.md` §6.1). The finding is about what an operator can see.

- Redis configured, server down: `/aegis/v1/health` reports `"status":"degraded"` with
  `"redis":{"connected":false,"circuit_breaker":"open"}`, requests get 503, and every
  refusal writes a `redis_failure` row to `audit_events`. This is loud, and correctly so.
- Redis **not configured**: `/aegis/v1/health` reports `"status":"healthy"` and omits the
  `redis` object entirely. Requests return 200 with rate-limit headers whose
  `X-RateLimit-Remaining-Requests` never decrements, because no counter exists.

So a deployment that loses its Redis *address*, through a dropped environment variable or
a bad config merge, serves traffic with rate limiting and daily spend budgets silently
disabled, behind a green health check. The fail-open path is intentional. Its
indistinguishability from a healthy configured deployment is not a property anyone chose,
and it is the failure mode most likely to survive unnoticed in production.

Until this changes, an operator who relies on rate limiting should assert on the presence
of the `redis` object in the health response, not just on `"status":"healthy"`, and should
treat a non-decrementing `X-RateLimit-Remaining-Requests` as an outage signal.


### 2.6 What the column bounds do and do not establish

Migration `012_bound_audit_text_columns` bounds the free-text columns on `audit_events`:
`error_message` to 128 characters, `user_agent` to 256, `ip_address` to 64, and the
`metadata` JSONB to 4096 bytes by CHECK constraint. `internal/audit/limits.go` clips every
value to those limits before the insert.

**What this establishes.** These columns cannot hold a document, a conversation, or a
transcript, and the limit is declared in the schema where a reader can check it in a few
seconds rather than asserted in prose they have to trust. Combined with the clipping, an
over-long value is now recorded truncated instead of costing the entire audit row.

**What it does not establish.** It does not make storing a prompt impossible. A
128-character prompt exists, and a bound tight enough to exclude every prompt would be
tighter than a legitimate browser `User-Agent`, which routinely runs past 200 characters. A
bound a real client trips is not a safety property, it is an audit-suppression bug.

So the bound raises the floor and narrows what a future mistake could retain. It is not a
proof that no payload can ever be stored, and it should not be described as one. The claim
that no payload *is* stored remains the behavioural one, tested by
`TestNoPayload_CanaryEndToEnd` and by the sweeps in `VERIFICATION.md` §5.3 and §6.3.

**`metadata` is now gone.** Migration 013 replaced it with twelve typed, bounded columns
and cut `hash_schema_version=2`. No column on `audit_events` is untyped or unbounded any
more, so the paragraph above now describes every column on the table rather than most of
them.

That change was possible in one release only because the migration refuses to run in a
database holding version-1 checkpoints: a version-1 leaf hash cannot be recomputed once
`metadata` is gone, so dropping it under an existing chain would have made every sealed
checkpoint permanently unverifiable. A database that has run 013 provably has no
version-1 chain, which is what lets the verifier compute one field set rather than two.

The integrity coverage is unchanged by this, and it is worth being precise about that.
Version 1 hashed the JSONB object, so its contents were already attested. Version 2 hashes
the same data as separate fields. The gain is typing and bounding, not more signed data.

### 2.7 Non-text content parts are refused, not filtered

`message.content` now accepts an array of content parts as well as a string. Only
`{"type": "text", "text": "…"}` is admitted. An `image_url`, `input_audio` or `file`
part returns HTTP 400 with `"code": "unsupported_content_part"`.

**This is a refusal, not a capability.** AEGIS cannot read an image. The secrets, PII
and injection filters operate on text, so an image part would be data that leaves the
gateway for a provider having passed through no filter at all. Widening `content` to
carry structured parts is what made image parts expressible for the first time;
admitting them as a side effect of that widening would have converted a compatibility
fix into a hole in the one claim this product is built on.

So an operator should read this as: **AEGIS does not support multimodal requests, and
says so at the gateway rather than forwarding what it cannot inspect.** If image
support is wanted it needs its own decision about what inspecting an image means, not
a struct field.

The refusal is asserted by `TestDecode_RejectsNonTextContentPart` and, through the
full handler, by `TestChatCompletions_RefusesImageContentPart`.

### 2.8 Some tool-calling constructs cannot be expressed against Anthropic

Tool calling now works on every shipped alias. The Anthropic adapter translates the
OpenAI tool surface in both directions, streaming included, against behaviour probed
from the live Messages API rather than a remembered schema
([the mapping](anthropic-tool-mapping.md) quotes the provider's own errors).

What does not translate is refused by name rather than approximated, because an
approximation is the same failure as a silent drop and harder to detect. Five
constructs are legal OpenAI and are rejected with a 400 naming the construct:

1. **A tool call the conversation never answers.** Anthropic requires every
   `tool_use` to be followed immediately by its `tool_result`.
2. **Anything between a tool call and its result.** This is the one an agent is
   most likely to hit: OpenAI tolerates an interleaved message, Anthropic does not,
   so a conversation an OpenAI-backed agent built happily can be rejected when the
   same conversation is replayed against an Anthropic route.
3. **A tool result answering no call in the preceding turn.**
4. **`strict: true` on a tool whose schema does not set `additionalProperties:
   false`.** Anthropic requires it. AEGIS will not rewrite a caller's schema to
   satisfy the provider, because the rewritten request is not the one they sent.
5. **Tool call arguments that are not valid JSON.** OpenAI carries arguments as an
   opaque string; Anthropic carries an object, so a malformed string has nowhere to go.

**What an operator should take from this.** The first three are properties of the
conversation an agent constructs, not of the gateway's configuration, and an agent
that switches between an OpenAI route and an Anthropic one may produce a history that
only one of them accepts. That is a real portability limit between providers, and
AEGIS surfaces it at the gateway with a named error rather than passing through an
opaque provider 400.

**One thing this does not do.** `is_error` on a tool result has no OpenAI equivalent.
Nothing is lost in the direction AEGIS translates, since it never sets the flag, but a
tool failure signalled Anthropic-side cannot be represented if the ingress direction is
ever added.
### 2.9 Streaming cost is recorded, and what still is not priced

Cost comes from provider-reported usage rather than a local estimate, so tool
definitions, tool call arguments and tool results are all accounted for with no work
on the gateway's part.

Streaming used to be the exception, and badly: a streamed request recorded zero
tokens and zero cost while the same request unstreamed recorded real figures. The
daily spend budget is computed from those rows, so streamed traffic moved no budget
at all. Both adapters now report usage on a stream, Anthropic from its native
`message_start` and `message_delta` events and OpenAI because the gateway sets
`stream_options` for itself.

**Cached input is priced at the cached rate.** Both adapters report the cached subset
of the prompt and both cost paths apply `cached_input`, which several models set an
order of magnitude below `input`. That was not true until 2026-08-29: the rates were
configured, `cost.Calculator` implemented them, and nothing ever populated the count,
so every cache read was billed at the full input rate.

Two things remain worth knowing.

**The two providers disagree about what a prompt count means, and the adapters
normalise.** Anthropic's `input_tokens` excludes anything served from or written to
the cache; OpenAI's `prompt_tokens` includes the cached portion and reports it as a
subset. AEGIS follows OpenAI's convention throughout, so a caller sees one meaning.
Verified against the live API: a cached Anthropic call returned `input_tokens` 8
alongside `cache_read_input_tokens` 4411, for a prompt of 4419 tokens.

**Cache writes are priced at the published write rate**, five-minute and one-hour
tiers separately, as of 2026-08-29. They were previously billed as ordinary input
because `cost.Calculator` had no field for the configured rate.

Fixing that surfaced a data error worth recording, because it is the reason the
validation now checks ratios rather than presence. Every Anthropic `cache_write_5m`
value in `configs/pricing.yaml` was a factor of ten low: 0.625 where the published
rate is 6.25, and the same slip on all four models. Coverage validation had nothing
to say about it, since each row had *a* price. `TestAnthropicCacheRatesMatchPublishedMultipliers`
now checks every Anthropic row against the published multipliers (a five-minute
write is 1.25x input, a one-hour write 2x, a read 0.1x), deriving its scope from the
file so a model added later is covered without anyone remembering.

**The cached count is not persisted.** `usage_records` stores `prompt_tokens` without
the breakdown, so the split is visible in the response and in the cost, but a later
reconciliation against a provider bill cannot see how much of a request was cached.
Adding it is a migration, and nothing else needs one right now.

### 2.10 Tool names are not in the audit record

`input.request.tools_offered` and `input.request.tools_called` are exposed to Rego, and
tool counts appear on the completion log line. **Neither is written to `audit_events`.**

So the audit trail records that a request happened and, if it was denied, why. It does
not record which capabilities were put in front of the model. For an agent workload
that is a real gap: "a call was made" and "a shell tool was offered and called" are
different facts, and only the first is in the evidence.

It is safe under the zero-retention rule. A tool name is metadata of the same kind as
`model`, while arguments and results are payload, and
`internal/audit/tool_no_payload_test.go` tests that boundary rather than asserting it.

It is deferred anyway, because adding a field to `audit_events` changes every leaf hash
and requires `hash_schema_version=3`, and the verifier deliberately computes one field
set. Spending that on additive metadata is a bad trade. The decision, the three
rejected alternatives, and the specific thing to watch for are recorded in
[ADR 0011](../adr/0011-tool-names-wait-for-a-hash-schema-bump.md), and the bump itself is tracked on [issue #38](https://github.com/aegis-gateway/aegis-ai-gateway/issues/38).

### 2.11 `audit_logs` still exists and is still never written

`audit_logs` is created by migration `002_create_audit_logs` and **nothing has ever
inserted into it**. A repository-wide search finds the table in that migration, in
`internal/audit/reader.go`, and in the purge code, and nowhere else. The per-request
decision record lives in `audit_events`, which is the table the sealer covers.

`GET /aegis/v1/audit/logs` was retired on 2026-08-29 and now returns **410 Gone** with a
body naming `GET /aegis/v1/audit/events` as the replacement. Until then it returned an
empty list, and in `?format=csv` it emitted a full 21-column header before the empty
body, so an operator exporting the decision record received a well-formed file with no
rows. That reads as "no activity in this window", which is a worse failure than an error:
it answers the question incorrectly instead of declining to answer.

The endpoint still authenticates and still refuses an unscoped or sentinel-organization
key, unchanged. Nothing behind it reads a row, so scoping has nothing left to protect,
but the access check was kept rather than dropped as a side effect of the deprecation.

**Removal is pending and is not scheduled here.** The table stays because purge, the
schema guard and migration history all reference it, and dropping it is a separate change
with its own risk. `Reader.QueryLogs` and the `LogRow` type stay with it; they are
unreachable from the HTTP surface and are marked deprecated in place rather than deleted
piecemeal, so that the table and its reader are removed together or not at all.

What a reader should take from this: the absence of rows in `audit_logs` is not evidence
about traffic. It has never held anything.

### 2.12 Provider error bodies reach the process log, bounded

When a provider returns a non-200, the gateway logs an excerpt of the response body so an
operator can tell a bad API key from a rate limit from a malformed request. Until
2026-08-29 it logged the body **verbatim**, at `ERROR` level, on both the streaming path
(`internal/gateway/streaming_enhanced.go`) and, indirectly, the non-streaming one, where
`internal/router/adapters/openai.go` wrapped the whole body into an error that
`internal/gateway/handler.go` then logged.

Provider error bodies are unbounded strings the gateway does not control, and they
routinely quote the offending request back. So caller-supplied text could reach whatever
collects the process logs. The zero-retention claim is about prompts and responses
reaching durable storage, and a log shipper is durable storage.

`internal/redact.Excerpt` now bounds every such excerpt to 256 characters, collapses it to
a single line so a crafted body cannot forge additional log records, and drops control
characters. The status code, provider key and request ID are logged as separate fields,
which is the part an operator acts on.

**What this does not do.** It does not detect secrets or PII inside the excerpt, and it
should not be described as though it did. Up to 256 characters of provider-controlled
text, which may include a fragment of the caller's request, still reaches the log. The
mitigation is volumetric, not semantic. Treat gateway logs as containing incidental
third-party text, and set retention on them accordingly.

### 2.13 A custom Rego rule can place message text into the sealed record

Policy evaluation receives the caller's message text in full. `PolicyMessage.Content` in
`internal/filter/policy/opa.go` carries each message flattened to a string, and `Parts`
carries the structured form. That is necessary rather than incidental: a policy that
cannot see the content cannot make a decision about it.

The deny message a rule produces does not stay inside the evaluation. The path is:

1. Rego builds a deny string, joined by `concat("; ", deny)` in the shipped bundle.
2. `Evaluator.ScanRequest` concatenates it into `filter.Result.Message`.
3. `internal/gateway/handler.go` passes that as the `reason` argument to the audit logger.
4. `internal/audit/logger.go` writes it to `audit_events.reason`, clipped to 512
   characters by `MaxReason` in `internal/audit/limits.go`.

`audit_events` is the table the checkpoint sealer covers, and `reason` is one of the
twenty-six fields in the leaf hash at `hash_schema_version=2`
(`internal/audit/checkpoint/event.go`). So a deny message is hashed into the chain, served
by the audit read API, and cannot be edited afterwards without breaking verification.

A rule as ordinary as

```rego
msg := sprintf("blocked: %s", [input.messages[0].content])
```

therefore writes up to 512 characters of the caller's prompt into the attested trail,
permanently, in a table this project describes as holding no payload.

**This is operator-caused and the gateway does not prevent it.** The gateway cannot tell an
interpolated prompt from a literal string: both arrive as a deny message and both are
written. The zero-retention guarantee covers the code paths this project controls, and a
custom Rego bundle is not one of them. Nothing in the shipped configuration does this; the
default bundle interpolates only `input.request.model`.

**What an operator should do instead.** Return a rule *identifier*, not interpolated
content:

```rego
deny contains "restricted_data_on_uncleared_alias" if { ... }
```

That tells an operator which rule fired, which is the question a denial record has to
answer, and quotes nothing the caller sent. Interpolating a request *field* the operator
controls, such as `input.request.model` or `input.request.provider_type`, is fine: those
are metadata of the same kind as provider or status code. Interpolating message *content*
is not.

**Not enforced in code, deliberately.** Detecting whether a deny string contains caller
text would mean substring-matching every deny message against the request, which is both
expensive on the request path and unreliable, and refusing to seal a denial because its
message looked suspicious would drop a governance record for a heuristic. Making the
constraint enforceable is a design decision that has not been taken. Until it is, this is
a documented operator responsibility, and a Rego bundle that will be sealed deserves the
same review as any other code that writes to the audit trail.

### 2.14 The allow event carries identity and outcome, not tokens or latency

`audit_events` records permitted requests as of 2026-08-29. `request_complete` is written
when a request passes every gate and completes; `provider_failure` when a request passes
every gate and then fails at the provider. Before this, only refusals were written, so the
sealed chain attested what was denied and nothing about what was allowed.

**What an allow event carries**, all of it in existing columns and therefore all of it
inside the leaf hash: `event_type`, `timestamp`, `request_id`, `organization_id`,
`team_id`, `user_id`, `api_key_id`, `api_key_prefix`, `endpoint`, `method`, `status_code`,
`provider` (the configured provider key, not the adapter type), `model` (the requested
alias), and `mode` (`stream` or `buffered`). A `provider_failure` additionally carries
`reason`, from a fixed set: `provider_unreachable`, `provider_error`, `stream_interrupted`,
`client_disconnected`.

**What it does not carry: latency, prompt and completion tokens, the resolved concrete
model, and classification.** There are no columns for them, and the reason not to add
columns is specific rather than conservative.

`audit_events` has twenty-six columns and all twenty-six are fields in the leaf hash at
`hash_schema_version=2`. That correspondence is exact, and it is what makes "the row is
attested" mean the whole row. Adding a column and putting it in the hash changes every
leaf hash and requires `hash_schema_version=3`, which is deferred under
[ADR 0011](../adr/0011-tool-names-wait-for-a-hash-schema-bump.md) and tracked on
[issue #38](https://github.com/aegis-gateway/aegis-ai-gateway/issues/38). Adding a column
and leaving it out of the hash would be worse than not adding it: the fields carrying the
evidence would be the only fields nothing attests, and the record would look more complete
than it is.

So the token counts and the latency for a given `request_id` are in `usage_records`, which
is **not sealed**. Joining the two gives the full picture of a request and gives it at two
different levels of assurance. An assessor should be told which half is attested.

If those fields need to be attested, they should be added in the same
`hash_schema_version=3` bump as the tool names in §2.10, because the bump is the expensive
part and doing it twice costs twice.

**Loss visibility.** A failed audit write leaves no row and no gap in the id sequence,
which only advances on a successful insert, so neither the sealer nor a reader can detect
it. `aegis_audit_write_failure_total`, labelled by event type, is the only signal that the
record is incomplete. **Any non-zero value means the completeness claim does not hold for
that window.** Alert on any increase.

**Volume.** `audit_events` now receives one row per request rather than one row per
refusal. Measured on 2026-08-29: 50,000 events occupy 29 MB including indexes, about
600 bytes per row, and seal at roughly 31,000 events per second into 10,000-leaf
checkpoints of about 215 bytes each. A deployment serving a million requests a day should
budget in the region of 600 MB a day of `audit_events` growth and plan retention
accordingly.
