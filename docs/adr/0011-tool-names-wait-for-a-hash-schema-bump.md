# 0011. Tool names wait for a hash schema bump rather than causing one

Status:   Accepted
Date:     2026-08-29
Decision: Do not record tool names in `audit_events` yet. Add them as a rider when a `hash_schema_version=3` is cut for another reason, tracked on issue #38.

## Context

The audit trail records that a request happened and, if it was denied, why. It
does not record **which capabilities were put in front of the model**. For an
agent workload those are different facts: "a call was made" and "a shell tool
was offered and a file-read tool was called" answer different questions, and only
the first is in the evidence.

The data already exists. `AegisRequest.ToolNames()` and `CalledToolNames()`
produce it, it reaches the Rego policy input as `input.request.tools_offered`
and `tools_called`, and it appears on the completion log line as counts. Process
logs rotate; the audit trail does not.

**Safety under zero-retention is settled, not open.** A tool name is metadata of
the same kind as `model`; arguments and results are payload and stay out.
`internal/audit/tool_no_payload_test.go` drives both accessors with payload
planted in every adjacent field and fails if any of it appears. What defers this
is cost, not safety.

The cost is stated in `internal/audit/checkpoint/event.go`:

> Adding, removing or renaming any of them changes every leaf hash and therefore
> requires a version 3, not an edit here.

The verifier computes exactly one field set, and reports a checkpoint at any
other version as attested but unverifiable by that build. Migration 013 refused
to run against a database holding version-1 checkpoints precisely so that one
field set would be enough. That single-field-set property is what makes
checkpoint verification simple enough to be credible to an external anchoring
service, which is the party the artifact exists for.

## Decision

Defer, and ride the next bump.

Three alternatives were considered and rejected.

**Bump to `hash_schema_version=3` for this alone.** Costs either the
single-field-set property or a migration that refuses to run against existing
version-2 chains the way 013 refused version-1. For a released product the
second means operators verify and archive their whole chain before upgrading.
Not worth paying for additive metadata.

**Columns on `audit_events`, excluded from the leaf hash.** No bump, existing
chains unaffected. Rejected: a trail carrying data the checkpoint does not cover
undermines the claim the checkpoint makes about everything else. Tool names
could be altered without breaking a hash, and a reader has no way to tell which
columns are attested. Worse than not recording them.

**`usage_records` instead of `audit_events`.** No hash involvement. Rejected: it
is operational data rather than the tamper-evident trail, so it does not answer
the governance question, and it sits outside the no-payload schema guard, which
matches `audit_*` tables only. That guard does more work than any other check in
the repository and governance-relevant data should not be placed beyond it.

## Consequences

Until a version 3 arrives, an assessor cannot learn from the audit trail which
capabilities an agent was offered. That limit is recorded in
[known limitations §2.10](../evidence/known-limitations.md).

**The bump is not hypothetical, and this is why deferring is safe rather than a
euphemism.** [Issue #38](https://github.com/aegis-gateway/aegis-ai-gateway/issues/38)
already proposes binding `prev_checkpoint_id` into the checkpoint hash in a
future `hash_schema_version`, and that is a stronger item than this one: without
it a checkpoint can be repointed at an earlier predecessor, detaching the ones
between, with every hash still valid and the foreign key satisfied. A gateway's
own offline verifier cannot detect it.

So a version 3 already has a reason to exist that has nothing to do with tool
names, and the bump will buy two things rather than one. Tool names are recorded
as a rider on #38 rather than as an issue of their own, so there is a single
place to look when the bump is cut.

Note the two changes touch different hashes under the same version number: #38
changes the checkpoint hash input, tool names change the event leaf field set in
`eventJCS`. `hash_schema_version` governs both, so one bump covers them.

When it happens:

1. Two bounded columns on `audit_events`, `tools_offered` and `tools_called`,
   names only, bounded in `internal/audit/limits.go` like every other text
   column.
2. Add both to `eventJCS`; extend `docs/AUDIT-INTEGRITY.md` §5.1 with the
   version-3 field set.
3. Populate from `ToolNames()` / `CalledToolNames()` on the request-complete
   path.
4. **Decide about streaming before relying on it.** The streaming path
   reconstructs called tool names from the delta accumulator in
   `internal/gateway/tool_stream.go`, which
   [ADR 0009](0009-indexless-tool-call-deltas-are-dropped.md) documents as best
   effort and permits to drop a fragment. A streamed row may undercount. Either
   accept that and say so, or give it a stronger source before the audit trail
   depends on it.
