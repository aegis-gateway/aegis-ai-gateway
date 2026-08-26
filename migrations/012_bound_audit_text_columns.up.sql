-- Bounds the free-text columns on audit_events, drops an unused JSONB column,
-- and repairs a shipped defect that silently discarded audit rows.
--
-- Hash schema: this migration does NOT change hash_schema_version. Section 8 of
-- docs/AUDIT-INTEGRITY.md ties a version bump to a change in the set of columns
-- the leaf hash covers, and that set is unchanged here: no column the hash reads
-- is added, removed, or renamed. Narrowing a column's declared type does not
-- alter the JCS encoding of the string it holds.
--
-- It does rewrite stored values, in one case: the USING clauses below truncate a
-- user_agent or error_message that exceeds the new width. Both of those columns
-- ARE covered by the leaf hash, so rewriting one that is already sealed would
-- change attested content and make verify-chain report a Merkle mismatch as
-- tampering. That is why section 0 refuses instead. Given it, every previously
-- sealed checkpoint still verifies under version 1.
--
-- (An earlier draft of this file asserted "no stored value is rewritten" and,
-- next to the user_agent ALTER, that these strings are not attested. Both were
-- false once the USING clauses were added, and both were caught in review. The
-- claim a migration makes about integrity has to survive the migration's own
-- later edits, which is the whole reason to state it here rather than assume it.)

-- ---------------------------------------------------------------------------
-- 0. Refuse rather than rewrite attested content.
--
-- Two requirements meet here and they conflict in exactly one case.
--
-- The USING clauses below have to exist, because user_agent was unbounded and
-- caller-controlled: without them one historical row longer than the new width
-- fails the migration and leaves schema_migrations dirty, blocking the upgrade
-- entirely.
--
-- But user_agent and error_message are both in the leaf hash field set, so
-- truncating a row that is already covered by a checkpoint changes attested
-- content. verify-chain then reports a Merkle root mismatch, and the words it
-- uses are "event rows have been tampered". Reproduced on PostgreSQL 16 by
-- truncating one sealed 200-character user_agent: OK before, that anomaly after.
--
-- The conflict is only over rows that are both over-long AND sealed. Rows that
-- are not yet covered by any checkpoint are not attested to anything, so
-- truncating those costs nothing and the USING clauses handle them. For the
-- intersection, refusing is the only honest option: silently rewriting attested
-- audit content to make an upgrade succeed is the failure this whole table
-- exists to make detectable.
-- ---------------------------------------------------------------------------
DO $$
DECLARE
    sealed_overlong BIGINT;
BEGIN
    SELECT count(*) INTO sealed_overlong
      FROM audit_events e
     WHERE (char_length(e.user_agent) > 256 OR char_length(e.error_message) > 128)
       AND EXISTS (
             SELECT 1 FROM audit_checkpoints c
              WHERE e.id >= c.range_start AND e.id <= c.range_end);

    IF sealed_overlong > 0 THEN
        RAISE EXCEPTION
            'refusing to truncate % sealed audit row(s): user_agent and error_message are covered by the leaf hash, so shortening a row already inside a checkpoint would change attested content and make verify-chain report tampering.',
            sealed_overlong
        USING ERRCODE = 'raise_exception',
              HINT = 'The schema is unchanged: this check runs before any DDL. Clear the dirty flag with UPDATE schema_migrations SET version=11, dirty=false. Then verify the chain (cmd/migrate verify-chain --full) and archive the affected checkpoints and events before re-running, or stay on schema 11.';
    END IF;
END
$$;

-- ---------------------------------------------------------------------------
-- 1. audit_logs.filter_results
--
-- A JSONB column no code path writes. Nothing in internal/ inserts into
-- audit_logs at all; the table is only purged and schema-guarded. An unwritten
-- JSONB column on an audit table is a standing invitation to put a filter's
-- matched text in it later, which is exactly the retention claim we do not want
-- to depend on someone's restraint.
-- ---------------------------------------------------------------------------
ALTER TABLE audit_logs DROP COLUMN IF EXISTS filter_results;

-- ---------------------------------------------------------------------------
-- 2. audit_events.ip_address, VARCHAR(45) -> VARCHAR(64). A WIDENING, and a bug fix.
--
-- The column was sized for the longest IPv6 literal (45 characters), but it is
-- written from Go's http.Request.RemoteAddr, which is host:port, and for IPv6 the
-- host is bracketed. A routine full-form IPv6 client address plus a port is 47
-- characters, and the longest is 53:
--
--     [0000:0000:0000:0000:0000:ffff:255.255.255.255]:65535
--
-- PostgreSQL raises an error on varchar overflow rather than truncating, and
-- Logger.writeEvent only logs that error. The audit row is discarded. That path
-- includes LogAuthFailure, which is reachable unauthenticated, so before this
-- migration any IPv6 client failed authentication without leaving an audit row.
-- Reproduced against PostgreSQL 16 before the change:
--
--     ERROR:  value too long for type character varying(45)
--
-- 64 leaves headroom over the 53-character worst case. Go-side clipping in
-- internal/audit is the second line of defence, not the first.
-- ---------------------------------------------------------------------------
ALTER TABLE audit_events ALTER COLUMN ip_address TYPE VARCHAR(64);

-- ---------------------------------------------------------------------------
-- 3. audit_events.error_message, TEXT -> VARCHAR(128)
--
-- Every value is gateway-generated and the longest is 33 characters
-- ("Redis unavailable - failed closed"). No caller-supplied text reaches it: the
-- specific deny reason goes to metadata, not here.
--
-- What this bound is and is not: it makes the column unable to hold a document,
-- a conversation or a transcript, and it puts that limit in the schema where a
-- reader can see it rather than in prose where they have to trust it. It does
-- not make storing a prompt impossible, because 128-character prompts exist.
-- ---------------------------------------------------------------------------
ALTER TABLE audit_events ALTER COLUMN error_message TYPE VARCHAR(128)
    USING left(error_message, 128);

-- ---------------------------------------------------------------------------
-- 4. audit_events.user_agent, TEXT -> VARCHAR(256)
--
-- The only caller-controlled column of the three, taken from the User-Agent
-- header on the unauthenticated auth-failure path. 256 rather than 128 because
-- real browser user agents run past 200 characters, and a bound a legitimate
-- client trips is not a safety property: it is the audit-suppression bug in
-- section 2 with a different column name. Clipped in Go before the insert so
-- that a longer header is recorded truncated rather than costing the row.
--
-- USING left(...) is required, not decorative. This column was unbounded and
-- caller-controlled until now, so an upgrade from schema 11 can meet a stored
-- value longer than the new width, and PostgreSQL raises an error on a narrowing
-- ALTER rather than truncating. Without the USING clause one historical row is
-- enough to fail migration 012, leave schema_migrations dirty at 12, and block
-- migration 013 and the new binary from ever being deployed. Reproduced on
-- PostgreSQL 16 against a schema-11 database holding a 900-character user_agent:
-- the migration failed and left the version dirty.
--
-- The USING clause only ever fires on rows that section 0 has established are
-- not covered by any checkpoint. Those rows are not attested to anything yet, so
-- shortening them changes no hash and loses only the tail of a user-agent
-- string. Anything sealed reaches section 0 first and stops there.
-- error_message gets the same treatment for the same reason, though every value
-- it holds is gateway-generated and short enough that it should never fire.
-- ---------------------------------------------------------------------------
ALTER TABLE audit_events ALTER COLUMN user_agent TYPE VARCHAR(256)
    USING left(user_agent, 256);

-- ---------------------------------------------------------------------------
-- 5. Backstop on audit_events.metadata
--
-- metadata stays for now. It is one of the fifteen fields the leaf hash covers
-- at hash_schema_version=1, and dropping it would leave every sealed version-1
-- checkpoint unverifiable, because a version-1 leaf cannot be recomputed once
-- the column it hashes is gone. Replacing it with typed columns is a separate,
-- versioned change; see VERIFICATION.md 4.2.1.
--
-- Until then this bounds the one part of the row that is otherwise unbounded.
-- The limit is on serialized size rather than on keys: a key denylist is
-- defeated by renaming the key, and a key allowlist needs an IMMUTABLE function
-- because CHECK cannot hold a subquery, which moves the guarantee into a
-- function someone can later alter. A size limit has neither weakness.
--
-- 4096 bytes is far above the largest shape the six Log* methods produce (the
-- widest is a few hundred bytes) and far below anything worth calling a payload.
-- Go clips the values that feed it, so this constraint should never be the thing
-- that stops a write.
-- ---------------------------------------------------------------------------
ALTER TABLE audit_events
    ADD CONSTRAINT audit_events_metadata_bounded
    CHECK (pg_column_size(metadata) <= 4096);
