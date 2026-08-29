# Recording tool names in the audit trail

**Status:** Deferred. Do it when a `hash_schema_version=3` arrives for another
reason; do not bump for this alone.
**Date:** 2026-08-29

---

## The gap

The audit trail records that a request happened and, if it was denied, why. It
does not record **which capabilities were put in front of the model**.

For an agent workload those are different facts. "A call was made" and "a shell
tool was offered and a file-read tool was called" answer different questions, and
only the first is in the evidence today. An assessor asking what an agent was
permitted to do cannot get it from the audit trail.

The data exists. `AegisRequest.ToolNames()` and `CalledToolNames()` already
produce it, it already reaches the Rego policy input as
`input.request.tools_offered` and `tools_called`, and it already appears on the
completion log line as counts. Process logs rotate; the audit trail does not.

## Why it is safe under the no-payload rule

A tool name is metadata of the same kind as `model`: it says which capability
was exercised, not what was done with it. The arguments and the results are the
payload half, and they stay out.

That boundary is not asserted, it is tested.
`internal/audit/tool_no_payload_test.go` drives `ToolNames` and
`CalledToolNames` with payload planted in every adjacent field and fails if any
of it appears in the output. So the safety question is settled. The cost question
is not, and it is what defers this.

## The cost

`internal/audit/checkpoint/event.go` states the constraint:

> The twenty-six fields are the fourteen carried over from version 1 plus the
> twelve columns migration 013 promoted out of it. Adding, removing or renaming
> any of them changes every leaf hash and therefore requires a version 3, not an
> edit here.

The verifier computes exactly one field set. A checkpoint at any other version
is reported as attested but unverifiable by that build, which is deliberate:
migration 013 refused to run against a database holding version-1 checkpoints
precisely so that the verifier would never need two field sets.

That single-field-set property is the thing being spent. The checkpoint is the
artifact an external anchoring service consumes, and simple verification is what
makes it credible to the party you most want checking it.

## Options considered

**A. Columns on `audit_events`, bump to `hash_schema_version=3`.**
Tool names attested like every other field. Costs either the single-field-set
property, or a migration that refuses to run against existing version-2 chains
the way 013 refused version-1. For a released product the second means operators
must verify and archive their whole chain before upgrading.

**B. Columns on `audit_events`, excluded from the leaf hash.**
No version bump, existing chains unaffected. Rejected. A trail carrying data the
checkpoint does not cover undermines the claim the checkpoint makes about
everything else: tool names could be altered without breaking a hash, and a
reader has no way to tell which columns are attested. Worse than not recording
them.

**C. `usage_records` instead of `audit_events`.**
No hash involvement. Rejected. It is operational data rather than the
tamper-evident trail, so it does not answer the governance question. It also sits
outside the no-payload schema guard, which matches `audit_*` tables only, and
that guard is doing more work than any other check in the repository.

**D. Defer, and add it as a rider when a version 3 arrives for another reason.**
Chosen. The value is real but additive. Spending the single-field-set property on
additive metadata is a bad trade, and batching costs nothing except the wait.

## What to watch for

The trigger is not someone deciding to bump `hash_schema_version`. Nothing else
is parked on a version 3 as of this writing, so that will not happen on its own.

The trigger is **`config_hash` and `policy_bundles` being defined in control
plane protocol v2** ([ADR 0004](../adr/0004-reserved-fields-must-not-be-populated.md),
[known limitations §1](../evidence/known-limitations.md)). Those names are
reserved and actively rejected in v1, and their definition is the outstanding
piece of work on checkpoint provenance.

The connection is the reasoning above. The checkpoint hash today covers
`merkle_root`, `prev_checkpoint_hash`, the range bounds, `event_count`,
`hash_schema_version` and `sealed_at`. It does **not** cover `config_hash`. So a
v2 that merely carries a config hash in the payload without binding it into the
hash reproduces option B exactly, one level up: unattested provenance sitting
inside an artifact whose whole purpose is attestation. Whoever defines v2 will
have to face that, and the moment they bind it in is the moment the hash
construction changes anyway.

That is the moment to add tool names, because the bump has already been paid for.

## What to do then

1. Two columns on `audit_events`, `tools_offered` and `tools_called`, bounded
   like every other text column (see `internal/audit/limits.go`) and stored as
   names only.
2. Add both to `eventJCS` and cut `hash_schema_version=3`.
3. Populate from `AegisRequest.ToolNames()` and `CalledToolNames()` on the
   request-complete path. The streaming path reconstructs called names from the
   deltas; see `internal/gateway/tool_stream.go`, and note that accumulator is
   documented as best effort, so a streamed row may undercount. Decide whether
   that is acceptable before relying on it, or give it a stronger source.
4. Extend `docs/AUDIT-INTEGRITY.md` §5.1 with the version-3 field set.
