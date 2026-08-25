ALTER TABLE audit_checkpoints
    DROP CONSTRAINT IF EXISTS audit_checkpoints_covered_range_complete,
    DROP CONSTRAINT IF EXISTS audit_checkpoints_covered_range_source;

ALTER TABLE audit_checkpoints
    DROP COLUMN IF EXISTS covered_range_source;
