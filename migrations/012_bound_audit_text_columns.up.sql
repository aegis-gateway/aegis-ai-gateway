-- Bounds the free-text columns on audit_events, drops an unused JSONB column,
-- and repairs a shipped defect that silently discarded audit rows.
--
-- Hash schema: this migration does NOT change hash_schema_version. Section 8 of
-- docs/AUDIT-INTEGRITY.md ties a version bump to a change in the set of columns
-- the leaf hash covers. The set is unchanged here: no column that the hash reads
-- is added, removed, or renamed, and no stored value is rewritten. Narrowing a
-- column's declared type does not alter the JCS encoding of the string it holds,
-- so every previously sealed checkpoint still verifies under version 1.

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
-- Truncating history is the lesser loss here. The alternative is an upgrade an
-- operator cannot complete, and the rows being shortened are user-agent strings
-- rather than anything the audit trail attests to. error_message gets the same
-- treatment for the same reason, though every value it holds is
-- gateway-generated and short.
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
