-- Adds the unscanned-part columns the request events need, and cuts
-- hash_schema_version 4.
--
-- The gateway is gaining image support. The filters operate on text, so an image
-- content part leaves the gateway for a provider having passed through no filter
-- at all, and it is admitted only on a route an operator explicitly opts in.
-- audit_events said that a permitted request happened and what it consumed. It
-- did not say that some of what it forwarded was never inspected. An assessor
-- reading the trail could not tell a request whose every part was scanned from
-- one that carried two images past the filters.
--
-- These columns record that fact, and only that fact: how many parts went out
-- unscanned, how many decoded bytes they came to, and which media types they
-- were. Not the content, and not a hash of the content. A content hash is a
-- fingerprint: anyone who holds a candidate image could compute its digest and
-- confirm from the sealed, exported record that it was sent. That is a disclosure
-- of content by another route, and the zero-retention claim does not survive it.
-- ADR 0013 records the decision.
--
-- ---------------------------------------------------------------------------
-- Why this migration does NOT need to refuse, where 013 did
--
-- The reasoning is migration 016's, unchanged. 013 refused to run while any
-- version-1 checkpoint existed because it DROPPED metadata, a column the
-- version-1 leaf hash covers. This migration only ADDS columns. The version-2
-- and version-3 leaf hashes each cover an explicit field list
-- (checkpoint.EventColumns and EventColumnsV3), not SELECT *, so every column
-- they hash still exists and still holds the same value afterwards. Version-2
-- and version-3 leaves recompute byte-for-byte after this runs, and
-- TestEventLeafHash_V2AndV3AreUnaffectedByTheV4Columns pins it.
--
-- The verifier therefore computes three field sets. That is the cost 016 already
-- accepted and for the same reason: adding a column does not invalidate old
-- evidence and should not cost old evidence.
--
-- The checkpoint hash construction does NOT change. Version 4 uses the 104-byte
-- version-3 input with 4 in the version scalar; only the leaf field set differs.
-- ---------------------------------------------------------------------------
--
-- Every column here is gateway-derived. None carries caller text:
--   unscanned_parts       a count the gateway took while decoding the request
--   unscanned_bytes       the decoded size the gateway measured, summed
--   unscanned_part_types  sorted, de-duplicated, comma-joined media types, each
--                         one from the gateway's allowlist (image/png,
--                         image/jpeg, image/gif, image/webp). The logger maps
--                         anything outside it to a fixed sentinel before the
--                         insert, so a caller's declared type cannot land here.
--
-- NULL means the request carried no unscanned parts, or the event type does not
-- record them. The gateway never writes a sealed 0 for "none": it leaves all
-- three NULL, so that "no unscanned parts" and "zero unscanned parts were
-- counted" cannot be two encodings of one fact.

ALTER TABLE audit_events
  ADD COLUMN unscanned_parts      BIGINT,
  ADD COLUMN unscanned_bytes      BIGINT,
  ADD COLUMN unscanned_part_types VARCHAR(256);

-- Bounds, matching the pattern migrations 012 and 016 established: a sealed
-- column must not be able to grow without limit, and a negative count is a bug
-- rather than a measurement. 256 is far above the longest value the allowlist
-- can produce (all four types joined is 41 characters), and is a ceiling rather
-- than a budget: limits.go clips to it before the insert, because PostgreSQL
-- rejects an over-long varchar and the whole audit row would be lost.
ALTER TABLE audit_events
  ADD CONSTRAINT audit_events_unscanned_nonneg CHECK (
    (unscanned_parts IS NULL OR unscanned_parts >= 0) AND
    (unscanned_bytes IS NULL OR unscanned_bytes >= 0)
  );

COMMENT ON COLUMN audit_events.unscanned_parts IS
  'Number of content parts forwarded to the provider without being scanned by the filters. NULL when there were none.';
COMMENT ON COLUMN audit_events.unscanned_bytes IS
  'Total decoded bytes of the unscanned parts. A size, not a digest: no content or content hash is recorded.';
COMMENT ON COLUMN audit_events.unscanned_part_types IS
  'Sorted, de-duplicated, comma-joined media types of the unscanned parts, from the gateway allowlist only.';
