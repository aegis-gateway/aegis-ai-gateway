-- Repeatable seed for audit_events volume measurements.
--
-- Two ad hoc measurements of the same quantity disagreed by 1.4x because each
-- invented its own corpus, and audit_events row size with indexes depends on
-- the width of the indexed values (request_id above all) rather than on the row
-- count. This file exists so that a later measurement compares against the
-- earlier one instead of against a different corpus.
--
-- The generated row matches what audit.Logger.LogRequestComplete actually
-- stores, which is not the same as the columns it sets. Event.UserAgent and
-- Event.ErrorMessage are plain Go strings rather than pointers
-- (internal/audit/logger.go:56,60), so writeEvent passes "" straight through
-- and PostgreSQL stores '' rather than NULL. Every other column the event does
-- not set is a *string and really is NULL. Confirmed against a gateway-written
-- row: user_agent and error_message hold '', reason and error_detail hold NULL.
--
-- Every 50th event is a provider_failure rather than a request_complete, which
-- is the only shape a non-200 audit row takes: handler.go:638 passes
-- http.StatusOK literally to the completion call and routes failures to
-- LogProviderFailure, which sets a reason and a non-empty error_message. That
-- row is wider, 286 bytes against 246.
--
--   psql "$DATABASE_URL" -f scripts/measure-audit-volume.sql
--   psql "$DATABASE_URL" -c "select reset_audit_volume_corpus()"
--   psql "$DATABASE_URL" -c "select seed_audit_events(540500)"
--   psql "$DATABASE_URL" -c "select * from audit_volume_report"
--
-- Point it at a scratch database. seed_audit_events locks audit_events against
-- all other writers for the duration of the seed, so running it against a
-- database a gateway is using will block that gateway's audit writes.
--
-- seed_audit_events refuses to run against a non-empty audit_events, because
-- audit_volume_report divides whole-table size by whole-table count: appending
-- to an existing corpus silently reports a figure for neither the rows you asked
-- for nor the rows that were already there.
--
-- Timestamps land two days in the past so the events clear the 300-second lag
-- window; seeding at now() leaves the sealer nothing to do and it exits
-- reporting success.
--
-- They also ASCEND with g, which matters and is not cosmetic. Five of the seven
-- indexes on audit_events are ordered by timestamp DESC
-- (migrations/005_create_audit_events.up.sql:27-32), so whether rows arrive
-- oldest-first or newest-first changes how btree pages split, and with it the
-- index size. Seeding in descending order measures 552.1 bytes per row where
-- ascending measures 617.8, on byte-identical heap data. Production appends in
-- real time, so ascending is the representative order. This one detail accounts
-- for most of the disagreement between the two earlier ad hoc measurements.

-- Dropped by explicit signature first. CREATE OR REPLACE cannot change a
-- function's argument list, so re-running an updated copy of this file over an
-- older one leaves both versions registered, and a call that relies on defaults
-- then fails as ambiguous rather than picking the newer one.
DROP FUNCTION IF EXISTS seed_audit_events(bigint);
DROP FUNCTION IF EXISTS seed_audit_events(bigint, int);
DROP FUNCTION IF EXISTS seed_audit_events(bigint, int, int);

-- key_pool bounds how many distinct API keys the corpus uses. It matters:
-- idx_audit_events_api_key is (api_key_id, timestamp DESC), so a fresh random
-- uuid per event both makes the page layout nondeterministic between runs and
-- models a deployment where every request carries its own credential, which
-- inflates that index against any real one. A bounded pool reproduces the real
-- pattern, one credential appending many timestamp-ordered rows.
-- jitter_window models how audit.Logger.Log actually inserts. It starts one
-- goroutine per event (internal/audit/logger.go:139-149), so rows carry
-- ascending timestamps but reach the indexes in whatever order the pool
-- schedules them. Five of the seven indexes on audit_events are ordered by
-- timestamp DESC, which is why that order matters at all. 0 inserts in strict timestamp order, the idealisation; W > 0
-- permutes deterministically within each window of W rows.
CREATE OR REPLACE FUNCTION seed_audit_events(n bigint, key_pool int DEFAULT 500,
                                             jitter_window int DEFAULT 0)
RETURNS void AS $$
DECLARE
  existing bigint;
BEGIN
  -- Held to the end of this function's transaction. Without it the emptiness
  -- test below is only a snapshot: a gateway writing to the same database can
  -- commit an event between the check and the bulk insert, or while it runs,
  -- and the corpus is silently mixed while the guard reports success. Taking
  -- the lock also states the requirement, since a harness that has to exclude
  -- the application wants an isolated database.
  -- 7919 is prime, so (g * 7919) % jitter_window enumerates the window exactly
  -- once when the two are coprime, and degenerates when the window is a
  -- multiple of it: at jitter_window = 7919 every row in a window takes the
  -- same sort key and PostgreSQL is free to insert them in any order, which
  -- silently costs the reproducibility the window exists to provide.
  IF jitter_window > 1 AND jitter_window % 7919 = 0 THEN
    RAISE EXCEPTION
      'jitter_window % is a multiple of the 7919 multiplier, so the within-window ordering collapses instead of permuting. Choose a window coprime with 7919.',
      jitter_window;
  END IF;

  LOCK TABLE audit_events IN ACCESS EXCLUSIVE MODE;

  SELECT count(*) INTO existing FROM audit_events;
  IF existing > 0 THEN
    RAISE EXCEPTION
      'audit_events already holds % row(s); seeding would measure a mixed corpus. Run reset_audit_volume_corpus() first.',
      existing;
  END IF;

  INSERT INTO audit_events (
    request_id, timestamp, event_type, organization_id, team_id, user_id,
    api_key_id, api_key_prefix, ip_address, user_agent, endpoint, method,
    status_code, error_message, provider, model, mode, reason
  )
  SELECT
    'req_' || lpad(g::text, 13, '0') || '_' || md5(g::text)::varchar(16),
    now() - interval '2 days' + (g || ' milliseconds')::interval,
    -- request_complete is emitted only with 200; handler.go:638 passes
    -- http.StatusOK literally and routes every failure to LogProviderFailure,
    -- so a non-200 request_complete row cannot occur. Every 50th event is
    -- therefore a provider_failure, which is a wider row: it carries a reason
    -- and a non-empty error_message.
    CASE WHEN g % 50 = 0 THEN 'provider_failure' ELSE 'request_complete' END,
    'org-' || (g % 40),
    'team-' || (g % 120),
    CASE WHEN g % 4 = 0 THEN NULL ELSE 'user-' || (g % 900) END,
    md5((g % key_pool)::text)::uuid,
    'aegis-prod-' || substr(md5((g % key_pool)::text), 1, 8),
    -- completedRequest copies r.RemoteAddr unchanged
    -- (internal/gateway/audit_completion.go:149), so the column holds host:port
    -- rather than a bare address. A gateway-written row reads [::1]:59682.
    '203.0.113.' || (g % 254) || ':' || (30000 + (g % 35000)),
    '',   -- not NULL: Event.UserAgent is a string, so "" reaches the column
    '/v1/chat/completions',
    'POST',
    CASE WHEN g % 50 = 0 THEN 503 ELSE 200 END,
    -- LogProviderFailure sets this literal; LogRequestComplete leaves it "".
    CASE WHEN g % 50 = 0 THEN 'Provider request failed' ELSE '' END,
    (ARRAY['anthropic','openai','azure_openai'])[1 + g % 3],
    (ARRAY['aegis-fast','aegis-smart','aegis-balanced'])[1 + g % 3],
    CASE WHEN g % 3 = 0 THEN 'stream' ELSE 'buffered' END,
    -- Non-null only on failures, matching LogProviderFailure's strPtr(reason).
    CASE WHEN g % 50 = 0
         THEN (ARRAY['provider_unreachable','provider_error','stream_interrupted',
                     'stream_truncated','response_not_delivered'])[1 + (g / 50) % 5]
    END
  FROM generate_series(1, n) g
  ORDER BY CASE
             WHEN jitter_window <= 1 THEN g
             -- A permutation within each window, given the coprimality
             -- checked above.
             ELSE (g / jitter_window) * jitter_window + ((g * 7919) % jitter_window)
           END;
END;
$$ LANGUAGE plpgsql;

-- Explicit, so that clearing a corpus is never an accident of running the seed.
--
-- audit_purges is included for the same reason the checkpoint integration reset
-- includes it (internal/audit/checkpoint/integration_test.go:74). RESTART
-- IDENTITY reuses event ids from 1, so a purge recorded against an earlier
-- corpus still covers the new one: verifyFull consults those ranges
-- (internal/audit/checkpoint/verifier.go:234-249) and skips rehashing any
-- checkpoint they span, reporting it attested-but-unverifiable. Leaving the row
-- behind makes verify-chain --full exit non-zero over a perfectly healthy
-- corpus, which is exactly the benchmark this file documents.
CREATE OR REPLACE FUNCTION reset_audit_volume_corpus() RETURNS void AS $$
  TRUNCATE audit_events, audit_checkpoints, audit_purges RESTART IDENTITY CASCADE;
$$ LANGUAGE sql;

-- Report the figures the documentation quotes.
CREATE OR REPLACE VIEW audit_volume_report AS
SELECT
  count(*)                                                        AS events,
  pg_size_pretty(pg_relation_size('audit_events'))                AS heap,
  pg_size_pretty(pg_indexes_size('audit_events'))                 AS indexes,
  pg_size_pretty(pg_total_relation_size('audit_events'))          AS total,
  round(pg_relation_size('audit_events')::numeric / count(*), 1)  AS heap_bytes_per_row,
  round(pg_total_relation_size('audit_events')::numeric / count(*), 1) AS total_bytes_per_row
FROM audit_events;
