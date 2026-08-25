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
[`ConfigHash` and `PolicyBundles`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/api/controlplane/v1/checkpoint.go#L146-L160)
in `api/controlplane/v1`, but their semantics are **unspecified in v1**, no v1 emitter
may populate them, and validation **actively rejects** a submission that does:

```
config_hash is reserved in v1: its semantics are unspecified and no v1 emitter may populate it
```

This is a considered decision, recorded in
[ADR 0004](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/docs/adr/0004-reserved-fields-must-not-be-populated.md).
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
[`filter.Filter`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/0344929a98dae0377c0c974412d2ecdcf460a42a/internal/filter/filter.go#L43-L47)
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

### 2.3 Zero-retention is enforced behaviourally, not structurally

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
