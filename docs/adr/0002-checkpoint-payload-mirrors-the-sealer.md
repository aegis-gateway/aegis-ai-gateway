# 0002. The checkpoint payload mirrors what the sealer produces

Status:   Accepted
Date:     2026-08-21
Updated:  2026-08-22 (the density claim below was wrong and is withdrawn)
Decision: Carry every field of an `audit_checkpoints` row that a verifier needs to recompute `checkpoint_hash` without the gateway, including the two distinct hashes and `sealed_at` at microsecond precision.

## Context

The original brief for the control plane described a checkpoint submission as
carrying "the checkpoint root hash, the previous checkpoint root it chains to,
the covered event count, the covered time range", plus a sequence number.

Reading `internal/audit/checkpoint/sealer.go` and `docs/AUDIT-INTEGRITY.md`
against that description found three mismatches, each of which would have
shipped a protocol that cannot verify a chain.

**There is no sequence number.** Checkpoints are keyed by
`audit_checkpoints.id`, a BIGSERIAL, and uniquely indexed on
`(range_start, range_end)`. The `id` is the sequence.

An earlier version of this record added that the id "is dense rather than
merely increasing because the sealer holds a single-writer advisory lock".
**That is withdrawn. It is false, and something was built on it.**

PostgreSQL sequences are deliberately non-transactional, so that concurrent
writers do not serialise on the counter: a value handed to a transaction that
later rolls back is consumed and never reissued. The sealer writes each
checkpoint inside a transaction, so any failure between the insert and the
commit burns an id. The advisory lock serialises writers, which is a different
property and does not help.

A gateway's chain can therefore legitimately run 1, 3, 4. Nothing may require
the ids to be contiguous. The verifier in this repository never did: it compares
each checkpoint's `prev_checkpoint_id` against the row that actually precedes
it. A consumer that assumed density did, and the consequence is recorded in the
control plane's ADR 0007 - one transient error during a seal would have wedged
that gateway's submissions permanently.

**The chain does not link roots.** Per `docs/AUDIT-INTEGRITY.md` section 3,
each checkpoint binds its predecessor's `checkpoint_hash`, not its
`merkle_root`. These are two different 32-byte values with different jobs: the
Merkle root attests the covered events, the checkpoint hash chains this
checkpoint to the one before it. Binding the root would mean an attacker who
altered a checkpoint's range bounds or event count would not invalidate
anything downstream.

**`sealed_at` is a hash input**, as microseconds since the Unix epoch, and so
is `hash_schema_version`. Neither appeared in the original field list. Without
them a control plane holds bytes it can never recompute.

## Decision

The submission carries `checkpoint_id`, `range_start`, `range_end`,
`event_count`, `merkle_root`, `prev_checkpoint_id`, `prev_checkpoint_hash`,
`checkpoint_hash`, `hash_algorithm`, `hash_schema_version`,
`canonicalization_spec`, `sealed_at`, `sealer_version`, `gateway_version`, and
the covered time range (see 0005).

`hash_algorithm` is carried even though the sealer only emits `sha-256`. A
stored attestation that does not name the digest that produced it is one
migration away from being unverifiable by a tool that has never seen this
release.

`sealed_at` is transmitted in a format pinned to exactly six fractional digits
in UTC, and any other spelling of the same instant is rejected on decode. A
format that rounded or extended precision would make the hash unrecomputable
from the wire value.

## Consequences

- A control plane can recompute `checkpoint_hash` from a stored submission with
  no access to the gateway. That is what makes an aggregated chain independently
  checkable rather than merely stored.
- The payload is larger than the original sketch. It is still well under a
  kilobyte.
- `prev_checkpoint_id` travels alongside `prev_checkpoint_hash`. See 0006 for
  why the hash alone is not enough, and for the gap that reveals.
