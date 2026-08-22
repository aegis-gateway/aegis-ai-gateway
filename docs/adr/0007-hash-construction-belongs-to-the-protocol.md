# 0007. The hash construction belongs to the protocol package

Status:   Accepted
Date:     2026-08-22
Decision: Export the checkpoint hash construction from `api/controlplane/v1`, have the sealer use it, and pin the two together with a conformance test.

## Context

`docs/AUDIT-INTEGRITY.md` section 3 specifies the checkpoint hash. Before this
record it had two implementations: `computeCheckpointHash` in
`internal/audit/checkpoint`, and a second copy in the control plane's synthetic
client, written by reading the specification.

Two implementations of one specification is precisely the drift the
specification exists to prevent. The published spec is the contract for
independent verifiers, and an earlier defect in this very function is recorded
in its own comment: a length prefix that produced a 100-byte input no
spec-following verifier would reproduce.

A control plane that recomputes checkpoint hashes needs the construction. It
cannot import `internal/audit/checkpoint`, and reimplementing it means the
proprietary side and the open side can disagree about what a hash is.

## Decision

`api/controlplane/v1` exports `ComputeCheckpointHash` and `VerifyCheckpointHash`
as the single normative implementation. `internal/audit/checkpoint` calls it,
so the sealer and any verifier compute the same bytes by construction rather
than by review.

A conformance test in the gateway asserts that the sealer's stored
`checkpoint_hash` equals what the protocol function produces for the same
inputs, and that the function matches the published byte layout field by field.

## Consequences

- The construction is Apache 2.0 and public, which it should be: an independent
  verifier is supposed to be able to check a checkpoint without a commercial
  relationship or a Go toolchain.
- The control plane deletes its copy and calls the protocol package.
- `api/controlplane/v1` now contains behaviour, not only types. It remains
  stdlib only.
- A change to the construction is a change to a tagged contract and needs a new
  `hash_schema_version`, which is the correct amount of friction. See 0006 for
  the change already queued behind it.
