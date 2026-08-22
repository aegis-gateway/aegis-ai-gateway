# 0005. The covered time range is required in v1

Status:   Accepted
Date:     2026-08-22
Decision: The sealer records the earliest and latest event timestamp in each checkpoint's range, and the protocol requires both fields.

## Context

The first version of this package grouped the covered time range with
`config_hash` and `policy_bundles` as an optional field nothing populates. That
grouping was wrong, because those are different kinds of field.

`config_hash` and `policy_bundles` need a capability the gateway does not have:
nothing computes a configuration digest, and policy bundles are neither
versioned nor digested. Building them is a design question.

The covered time range needs no new capability. A checkpoint already states its
extent as a range of audit event IDs, and the sealer already loads every
covered row, including its `timestamp` column, in order to hash it. The minimum
and maximum are a scan of a slice that is already in memory.

The difference that matters is what the field is for. An event ID range states
extent in the gateway's own terms. "Does this evidence cover the third quarter"
is the first question asked of any evidence artifact, and an ID range cannot
answer it without a lookup against every gateway that contributed.

## Decision

`covered_from` and `covered_to` are required fields of a v1 checkpoint
submission.

The gateway gains migration `009_add_checkpoint_covered_range`, which adds two
nullable `TIMESTAMPTZ` columns to `audit_checkpoints` and backfills them for
existing checkpoints from the events they cover. The sealer computes them for
each new checkpoint from the rows it has already loaded.

## Consequences

**The range is not a hash input, and does not need to be.** Adding it to the
checkpoint hash would mean a new `hash_schema_version` and a verifier that
handles both. It is unnecessary: the leaf hash of every audit event covers that
event's `timestamp`, so the covered range is provable against the Merkle root
at bundle-generation time. The stored values are an index over something the
tree already attests, not a fresh claim. A tampered `covered_from` is
detectable by the same inclusion proof that answers the coverage question.

**Consecutive checkpoints can have overlapping time ranges.** Event IDs are
allocated at insert and become visible at commit, so a long transaction carries
an older timestamp and commits later. `covered_from` is therefore the minimum
over the batch rather than the first row's timestamp, and checkpoint N+1 can
begin earlier in time than checkpoint N ended. Anything answering a coverage
question must treat these as intervals to be unioned, not as a partition.

**Two columns are nullable in the gateway, and the protocol field is still
required.** A checkpoint sealed before migration 009 whose events have since
been purged cannot have its range reconstructed: the rows are gone. The
backfill covers every checkpoint whose events are still present, which in
practice is all of them, because purge and checkpointing were introduced within
the same development cycle and no tagged release carries either. A checkpoint
in the residual set cannot be submitted under v1, and the emitter reports it by
name rather than skipping it silently.

**This is a schema edit, not a version bump.** No tagged release carries
`api/controlplane/v1` yet. After the first tag it would have been a v2.
