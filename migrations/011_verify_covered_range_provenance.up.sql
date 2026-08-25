-- Records how each checkpoint's covered time range was obtained, and repairs
-- the ranges migration 009 computed from incomplete data.
--
-- 009 backfilled covered_from and covered_to from whatever audit_events rows
-- survived. Where a purge had already removed part of a checkpoint's range,
-- that produced an interval narrower than the events the checkpoint actually
-- attests, and once written it is indistinguishable from one computed over a
-- complete set.
--
-- That is the failure worth avoiding. A missing range is a known unknown: the
-- emitter refuses to submit the checkpoint and names it. A narrowed range is a
-- silent falsehood in the exact field an auditor uses to scope what the
-- evidence covers, and it understates coverage while looking authoritative.
-- Availability is not worth that trade.
--
-- See docs/adr/0005 for why the range is required, and the control plane's
-- ADR 0009 for the provenance model.

-- covered_range_source distinguishes how the interval was arrived at.
--
--   sealed             computed by the sealer at seal time over the exact set
--                      of events it hashed. Authoritative.
--   verified_backfill  recomputed from surviving events, and proven complete:
--                      the surviving count equals the checkpoint's own
--                      event_count and no purge overlaps its id range.
--   unverified         the interval could not be proven complete. Set to NULL
--                      and left for the emitter to refuse by name.
ALTER TABLE audit_checkpoints
    ADD COLUMN IF NOT EXISTS covered_range_source TEXT;

-- Everything sealed from now on is computed at seal time.
ALTER TABLE audit_checkpoints
    ALTER COLUMN covered_range_source SET DEFAULT 'sealed';

-- Re-derive provenance for every row that already has a range.
--
-- Two conditions must both hold for a backfilled range to be trusted, and
-- neither is sufficient alone. The count check catches a purge that removed
-- rows; the recorded-purge check catches a purge that removed rows and left the
-- count matching by coincidence, and also covers a purge whose deletions this
-- query cannot see because the rows are gone.
WITH survival AS (
    SELECT cp.id AS checkpoint_id,
           cp.event_count AS attested_count,
           COUNT(e.id) AS surviving_count
    FROM audit_checkpoints cp
    LEFT JOIN audit_events e
      ON e.id >= cp.range_start AND e.id <= cp.range_end
    GROUP BY cp.id, cp.event_count
),
purged AS (
    -- Read the checkpoints the purge itself recorded, rather than re-deriving
    -- them from event_id_min/event_id_max.
    --
    -- Those two columns hold ids from whichever table the run targeted. A
    -- `purge --table audit_logs` records audit_logs ids, and that BIGSERIAL is
    -- unrelated to audit_events'. Joining them against range_start/range_end
    -- compares two independent id spaces, so a log-only purge whose ids happen
    -- to fall inside a checkpoint's event range would discard that
    -- checkpoint's coverage — and the emitter then refuses the checkpoint by
    -- name and cannot submit the rest of the chain. A retention run on a table
    -- no checkpoint attests must not invalidate audit evidence.
    --
    -- affected_checkpoint_ids is populated only when audit_events rows are in
    -- scope and is explicitly empty otherwise (internal/purge/purge.go), which
    -- is the distinction event_id_min/max cannot express. It is also the more
    -- accurate answer: it was computed while the rows still existed, and a
    -- checkpoint sealed after a purge cannot have been affected by it.
    SELECT DISTINCT cid AS checkpoint_id
    FROM audit_purges p
    CROSS JOIN LATERAL UNNEST(p.affected_checkpoint_ids) AS cid
    WHERE NOT p.dry_run
)
UPDATE audit_checkpoints c
SET covered_range_source = CASE
        WHEN s.surviving_count = s.attested_count AND p.checkpoint_id IS NULL
            THEN 'verified_backfill'
        ELSE 'unverified'
    END
FROM survival s
LEFT JOIN purged p ON p.checkpoint_id = s.checkpoint_id
WHERE c.id = s.checkpoint_id
  AND (c.covered_from IS NOT NULL OR c.covered_to IS NOT NULL);

-- Discard any interval that could not be proven complete. A narrowed range is
-- worse than none: the emitter refuses a missing one by name, and reports a
-- narrowed one as fact.
UPDATE audit_checkpoints
SET covered_from = NULL,
    covered_to = NULL,
    covered_range_source = NULL
WHERE covered_range_source = 'unverified';

ALTER TABLE audit_checkpoints
    ADD CONSTRAINT audit_checkpoints_covered_range_source CHECK (
        covered_range_source IS NULL
        OR covered_range_source IN ('sealed', 'verified_backfill')
    );

-- A source without a range, or a range without a source, is a row nobody can
-- interpret.
ALTER TABLE audit_checkpoints
    ADD CONSTRAINT audit_checkpoints_covered_range_complete CHECK (
        (covered_from IS NULL AND covered_to IS NULL AND covered_range_source IS NULL)
        OR
        (covered_from IS NOT NULL AND covered_to IS NOT NULL AND covered_range_source IS NOT NULL)
    );
