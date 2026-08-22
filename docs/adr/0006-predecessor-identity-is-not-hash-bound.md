# 0006. Predecessor identity is not bound into the checkpoint hash

Status:   Proposed
Date:     2026-08-22
Tracked:  https://github.com/aegis-gateway/aegis-ai-gateway/issues/38
Decision: Record the gap. Propose binding `prev_checkpoint_id` into a future `hash_schema_version`. Do not implement now.

## Context

`docs/AUDIT-INTEGRITY.md` section 3 defines the checkpoint hash over the Merkle
root, the predecessor's hash, the range bounds, the event count, the schema
version, and the seal time. The predecessor's **identity** is not among them.

`internal/audit/checkpoint/verifier.go` already compensates for this. It checks
that `prev_checkpoint_id` names the row that actually precedes a checkpoint,
with a comment noting that an attacker can otherwise repoint a checkpoint at an
earlier one, leaving every hash valid and the foreign key satisfied while the
intervening checkpoints are silently detached from the chain.

The compensation works only while the verifier can see the original ordering.
It reads `prev_checkpoint_id` from the same table an attacker with write access
would have altered. If the ordering itself is rewritten, every hash still
verifies and the verifier has nothing left to compare against.

This is the part that is larger than a protocol field: **the gateway's offline
verifier cannot detect a repointed chain on its own.** Only a party that
witnessed the original ordering can. That is a structural limit of the current
hash construction, not a bug in the verifier.

## Decision

Two things, neither of which is implementing the fix now.

The wire protocol carries `prev_checkpoint_id` alongside `prev_checkpoint_hash`,
so a control plane records the identity a checkpoint claimed at the time it was
submitted, and can compare later submissions against it.

A future `hash_schema_version` should bind `uint64_le(prev_checkpoint_id)` into
the hash input, with the genesis case using a reserved value distinct from any
real id. That change makes a repointed chain detectable offline, by a verifier
holding nothing but the checkpoints. It is not made now because it invalidates
every existing checkpoint hash under the new version, needs a verifier that
handles both versions, and belongs with the other candidates for a version bump
rather than on its own.

## Consequences

- Until such a version exists, detection of a repointed chain depends on
  external witnessing. The control plane provides it by storing what each
  submission claimed, in a system with a different administrator and a
  different write path from the gateway's own database.
- This is a genuine property of the product rather than a marketing line, and
  it is stated as such in the control plane's README. A deployment that does
  not submit its checkpoints anywhere does not have it.
- The same future version bump is the natural home for binding the covered time
  range (see 0005), which is currently attested indirectly through the leaf
  hashes rather than directly.
