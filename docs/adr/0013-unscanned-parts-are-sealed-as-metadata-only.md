# 0013. Unscanned parts are sealed as metadata, never as content or a content hash

Status:   Accepted
Date:     2026-10-19
Decision: Seal the count, total decoded size and allowlisted media types of unscanned content parts at `hash_schema_version=4`. Do not seal the content or any hash of it.

## Context

The gateway is gaining image support. The secrets, PII and injection filters operate
on text, so an image part leaves for a provider having passed through no filter, and
it is admitted only on a route an operator explicitly opts in. Until now
`audit_events` could not distinguish a request whose every part was scanned from one
that carried images past the filters, so the sealed record was silent about exactly
the content it did not inspect.

Recording that fact means new leaf-hashed columns, which means a version bump: a
column outside the hash would be the first unattested column on the table, and the
fields carrying this evidence would be the ones nothing attests.

The obvious richer option was a content hash per part, which would let an operator
later prove *which* image went out. It was considered and rejected.

## Decision

Migration `018` adds three nullable columns, covered by the version-4 leaf
(`eventJCSV4` in `internal/audit/checkpoint/event.go`):

- `unscanned_parts`: how many parts were forwarded unscanned.
- `unscanned_bytes`: their total decoded size.
- `unscanned_part_types`: sorted, de-duplicated, comma-joined media types from the
  gateway allowlist, with anything else mapped to `(unlisted)` before the insert
  (`normalizeMediaTypes` in `internal/audit/unscanned.go`).

`NULL` means none. A sealed `0` is never written for "none".

**No content hash.** A digest of an image is a fingerprint. Anyone who holds a
candidate image can compute its hash and confirm, from a sealed and exported record,
that the image was sent through the gateway. That discloses content by another route,
and the zero-retention claim does not survive it: the claim is that the audit trail
cannot be used to recover or confirm what a caller sent, and a fingerprint is a
confirmation oracle. Salting or keying the hash moves the oracle to whoever holds the
key rather than removing it.

The checkpoint hash construction is unchanged from version 3. Only the leaf field set
differs.

## Consequences

The record can say that unscanned content left, how much, and of which kinds. It
cannot say which image, and an investigation that needs to know must get it from
somewhere other than the audit trail. That is the intended trade.

`unscanned_bytes` is weaker than a digest but not nothing: a mismatch can rule a
candidate out, and an unusual single-part size narrows the field.
`docs/evidence/known-limitations.md` §2.17 says so rather than describing the column
as content-free.

The verifier computes three leaf field sets (versions 2, 3 and 4), selected in one
place (`leafFieldSetFor` in `internal/audit/checkpoint/verifier.go`). Migration 018's
down refuses while any version-4 checkpoint exists.

The no-payload tests gain an allowlist of `unscanned_*` columns, each with its reason,
and a check that no `audit_events` column names a digest
(`internal/audit/unscanned_no_payload_test.go`). Adding a content hash later is a
decision against this record, not a schema edit.
