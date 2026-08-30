-- Adds the outcome columns the allow event needs, and cuts hash_schema_version 3.
--
-- audit_events said that a permitted request happened, under whose key, to which
-- requested alias. It did not say what actually ran: the alias is what the
-- caller asked for, not what served the request, and the volume and duration
-- lived only in usage_records and Prometheus, neither of which is sealed. An
-- assessor reading the trail could see that a request was allowed and not what
-- it consumed.
--
-- ---------------------------------------------------------------------------
-- Why this migration does NOT need to refuse, where 013 did
--
-- Migration 013 refused to run while any version-1 checkpoint existed because it
-- DROPPED metadata, a column the version-1 leaf hash covers: once gone, those
-- leaves could never be recomputed and every version-1 checkpoint would have
-- become permanently unverifiable.
--
-- This migration only ADDS columns. The version-2 leaf hash covers an explicit
-- field list (checkpoint.EventColumns and checkpoint.AuditEventRow), not
-- SELECT *, so every column it hashes still exists and still holds the same
-- value afterwards. A version-2 leaf recomputes byte-for-byte after this runs.
--
-- The verifier therefore computes two field sets rather than one, which costs
-- the single-field-set property ADR 0011 valued. That is the deliberate trade:
-- the alternative was refusing to run against existing version-2 chains, which
-- would force every deployment to verify and archive its chain before
-- upgrading. Adding a column does not invalidate old evidence and should not
-- cost old evidence.
-- ---------------------------------------------------------------------------
--
-- Every column here is gateway- or provider-derived. None carries caller text:
--   model_served      the resolved model from the provider response
--   classification    the tier on the presenting API key
--   *_tokens          counts reported by the provider
--   duration_ms       measured by the gateway
--
-- Tool names are deliberately NOT here. ADR 0011 proposed them as a rider on
-- this bump; they are caller-chosen strings, bounded by validation at 128 names
-- of 64 characters, so sealing them would admit up to 8 KB of caller-controlled
-- text per request into an immutable, exported record. internal/validation
-- already treats a tool name as potentially credential-bearing and keeps it out
-- of error bodies and log lines. That conflict needs deciding on its own terms,
-- not settling as a passenger on someone else's version bump.

ALTER TABLE audit_events
  ADD COLUMN model_served      VARCHAR(128),
  ADD COLUMN classification    VARCHAR(32),
  ADD COLUMN prompt_tokens     BIGINT,
  ADD COLUMN completion_tokens BIGINT,
  ADD COLUMN total_tokens      BIGINT,
  ADD COLUMN duration_ms       BIGINT;

-- Bounds, matching the pattern migration 012 established for the text columns:
-- a sealed column must not be able to grow without limit, and a negative count
-- or duration is a bug rather than a measurement.
ALTER TABLE audit_events
  ADD CONSTRAINT audit_events_token_counts_nonneg CHECK (
    (prompt_tokens     IS NULL OR prompt_tokens     >= 0) AND
    (completion_tokens IS NULL OR completion_tokens >= 0) AND
    (total_tokens      IS NULL OR total_tokens      >= 0)
  ),
  ADD CONSTRAINT audit_events_duration_nonneg CHECK (
    duration_ms IS NULL OR duration_ms >= 0
  );

COMMENT ON COLUMN audit_events.model_served IS
  'The model that served the request, from the provider response. Differs from model, which is the alias the caller requested.';
COMMENT ON COLUMN audit_events.classification IS
  'Maximum classification tier of the presenting API key, the authority the request ran under.';
COMMENT ON COLUMN audit_events.duration_ms IS
  'Gateway-measured wall time for the request, milliseconds.';
