-- Reverses 016. Safe in the direction 013's down was not: these columns are
-- covered by the version-3 leaf hash, so dropping them makes version-3
-- checkpoints unverifiable exactly as dropping metadata did for version 1.
--
-- Rolling back therefore requires that no version-3 checkpoint exists, and the
-- check is first so a refusal leaves the schema untouched.
DO $$
DECLARE
  v3_count bigint;
BEGIN
  SELECT count(*) INTO v3_count FROM audit_checkpoints WHERE hash_schema_version = 3;
  IF v3_count > 0 THEN
    RAISE EXCEPTION
      'refusing to roll back: % version-3 checkpoint(s) exist, and their leaf hashes cover the columns this migration drops. Verify and archive the chain first, or stay on schema 16.',
      v3_count;
  END IF;
END $$;

ALTER TABLE audit_events
  DROP CONSTRAINT IF EXISTS audit_events_token_counts_nonneg,
  DROP CONSTRAINT IF EXISTS audit_events_duration_nonneg;

ALTER TABLE audit_events
  DROP COLUMN IF EXISTS model_served,
  DROP COLUMN IF EXISTS classification,
  DROP COLUMN IF EXISTS prompt_tokens,
  DROP COLUMN IF EXISTS completion_tokens,
  DROP COLUMN IF EXISTS total_tokens,
  DROP COLUMN IF EXISTS duration_ms;
