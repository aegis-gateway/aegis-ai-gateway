-- Reverses 018. Safe only in the direction 016's down is safe: these columns are
-- covered by the version-4 leaf hash, so dropping them makes version-4
-- checkpoints unverifiable exactly as dropping metadata did for version 1.
--
-- Rolling back therefore requires that no version-4 checkpoint exists, and the
-- check is first so a refusal leaves the schema untouched.
--
-- golang-migrate marks version 17 dirty on a refusal, as it does for 016's and
-- 017's: the flag records a migration that did not run, not one that ran
-- halfway, and the three columns are still there. Clear it with
-- UPDATE schema_migrations SET version=18, dirty=false. Verified through
-- cmd/migrate against PostgreSQL 16.
--
-- A build that seals at version 4 cannot run against schema 17 either: its seal
-- query selects these columns and fails. Roll the binary back before the schema.
DO $$
DECLARE
  v4_count bigint;
BEGIN
  SELECT count(*) INTO v4_count FROM audit_checkpoints WHERE hash_schema_version = 4;
  IF v4_count > 0 THEN
    RAISE EXCEPTION
      'refusing to roll back: % version-4 checkpoint(s) exist, and their leaf hashes cover the columns this migration drops. Verify and archive the chain first, or stay on schema 18.',
      v4_count;
  END IF;
END $$;

ALTER TABLE audit_events
  DROP CONSTRAINT IF EXISTS audit_events_unscanned_nonneg;

ALTER TABLE audit_events
  DROP COLUMN IF EXISTS unscanned_parts,
  DROP COLUMN IF EXISTS unscanned_bytes,
  DROP COLUMN IF EXISTS unscanned_part_types;
