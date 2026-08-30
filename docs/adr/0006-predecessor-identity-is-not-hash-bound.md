# 0006. Predecessor identity is not bound into the checkpoint hash

Status:   Partly addressed in `hash_schema_version=3`, 2026-08-30; the structural limit stands
Date:     2026-08-22
Tracked:  https://github.com/aegis-gateway/aegis-ai-gateway/issues/38
Decision: Record the gap. Bind `prev_checkpoint_id` into a future `hash_schema_version`.

## Resolution, 2026-08-30

Version 3 appends `int64_le(prev_checkpoint_id)` to the checkpoint hash input,
making it 104 bytes where versions 1 and 2 are 96. See
`docs/AUDIT-INTEGRITY.md` section 3.

**The structural limit this ADR describes is NOT removed by version 3, and an
earlier draft of this resolution claimed otherwise.** The checkpoint hash is
unkeyed SHA-256 over stored fields, so an attacker with write access to
`audit_checkpoints` recomputes any digest and every successor. No version lets an
offline verifier detect a rewritten chain on its own. The sentence in this ADR's
context section stands unaltered: only a party that witnessed the original
ordering can.

What version 3 changes is narrower and still worth having. The chain reduces
"detect any tampering" to "hold a small number of digests externally".
`prev_checkpoint_id` was the one structural field that reduction did not cover:
under versions 1 and 2 it could be altered while every digest, including one held
by an anchoring party, continued to verify. Only the gateway's own ordering check
could notice, and that check compares the stored ordering against itself.

- **Version 3.** The ordering is inside the digest, so it is covered by whatever
  external party holds that digest. There is no longer a structural field outside
  the commitment.
- **Versions 1 and 2.** Unchanged and unchangeable; those digests are sealed. A
  chain sealed under version 2 keeps the gap for as long as those checkpoints are
  the evidence.

Neither version removes the need for external anchoring. The gap is closed in the
sense that anchoring now reaches the whole structure, not in the sense that a
lone verifier can now detect a rewrite.

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
