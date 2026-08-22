-- Records the wall-clock extent of each checkpoint alongside its event ID range.
--
-- A checkpoint already states its extent as a range of audit event IDs, which
-- is what the Merkle root attests and what the checkpoint hash covers. That is
-- extent in the gateway's own terms. "Does this evidence cover the third
-- quarter" is the first question asked of any evidence artifact, and an ID
-- range cannot answer it without a lookup against every gateway involved.
--
-- These columns are not hash inputs and do not need to be. The leaf hash of
-- every audit event covers that event's timestamp, so the covered range is
-- provable against the Merkle root by inclusion proof. These columns are an
-- index over something the tree already attests, not a fresh claim.
--
-- See docs/adr/0005-covered-time-range-is-required.md.
ALTER TABLE audit_checkpoints
    ADD COLUMN IF NOT EXISTS covered_from TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS covered_to   TIMESTAMPTZ;

-- Backfill from the events each existing checkpoint covers.
--
-- Nullable rather than NOT NULL because this cannot always succeed: a
-- checkpoint whose events have since been purged has nothing left to read the
-- timestamps from. The checkpoint is still valid and still attests its range;
-- its wall-clock extent is simply no longer derivable. Anything consuming
-- these columns must handle the null rather than assume the backfill was total.
UPDATE audit_checkpoints c
SET covered_from = r.min_ts,
    covered_to   = r.max_ts
FROM (
    SELECT cp.id AS checkpoint_id,
           MIN(e."timestamp") AS min_ts,
           MAX(e."timestamp") AS max_ts
    FROM audit_checkpoints cp
    JOIN audit_events e
      ON e.id >= cp.range_start AND e.id <= cp.range_end
    GROUP BY cp.id
) r
WHERE c.id = r.checkpoint_id;

-- Answering a coverage question means finding every checkpoint whose interval
-- overlaps a window. Event IDs are allocated at insert and become visible at
-- commit, so a long transaction carries an older timestamp and commits later,
-- and consecutive checkpoints can overlap in time. These are intervals to be
-- unioned, not a partition, so both bounds are indexed.
CREATE INDEX IF NOT EXISTS idx_audit_checkpoints_covered_range
    ON audit_checkpoints (covered_from, covered_to);
