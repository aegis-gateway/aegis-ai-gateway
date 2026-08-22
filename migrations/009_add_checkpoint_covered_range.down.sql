DROP INDEX IF EXISTS idx_audit_checkpoints_covered_range;

ALTER TABLE audit_checkpoints
    DROP COLUMN IF EXISTS covered_to,
    DROP COLUMN IF EXISTS covered_from;
