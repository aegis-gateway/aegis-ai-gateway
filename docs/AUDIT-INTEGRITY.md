# AEGIS Audit Integrity: Merkle Checkpoint Design

**Status:** Approved design — ready for implementation (T24)
**Implements:** Tamper-evident audit trail for `audit_events`
**Interaction with:** Purge (T20), Audit read API (T23), Commercial anchoring (future)

---

## 1. Approach: Merkle checkpoints, not per-row chain

A per-row hash chain (`prev_hash` on every row) requires serialized appends — concurrent inserts race on `prev_hash`, and the fix is a global advisory lock on every audit write, which is a serialization point on the request path. That is not acceptable for a gateway.

Instead: events are written normally and unchained. A separate sealer process periodically computes a Merkle root over a contiguous range of event IDs and writes a checkpoint into `audit_checkpoints`, where each checkpoint chains to the previous one by binding the previous checkpoint's own `checkpoint_hash`. The checkpoint chain is tamper-evident at checkpoint granularity; a Merkle tree enables single-event inclusion proofs without shipping the full chain.

**Why Merkle over a running hash:** A running hash can verify the chain is intact but cannot prove a specific event is in it without re-hashing from genesis. A Merkle root lets an auditor verify one event in O(log n) steps with a proof path. That is what a compliance audit asks for: "prove this specific denial is in your record, unaltered."

**Why RFC 6962:** Hand-rolled Merkle trees have two known flaws — no domain separation (a crafted 64-byte blob can be presented as either a leaf or an internal node) and duplicate-last-leaf (distinct leaf sets can produce identical roots, CVE-2012-2459). RFC 6962 eliminates both with prefix bytes and odd-node promotion. It has published test vectors, which means independent verifiers can check results without reading Go source.

---

## 2. Schema

### `audit_checkpoints`

```sql
CREATE TABLE audit_checkpoints (
    id                     BIGSERIAL PRIMARY KEY,
    range_start            BIGINT NOT NULL,      -- first event_id covered (inclusive)
    range_end              BIGINT NOT NULL,      -- last event_id covered (inclusive)
    event_count            INTEGER NOT NULL,
    merkle_root            BYTEA NOT NULL,       -- 32-byte SHA-256 root (RFC 6962)
    prev_checkpoint_id     BIGINT REFERENCES audit_checkpoints(id),
    prev_checkpoint_hash   BYTEA,               -- denormalized from the previous checkpoint's checkpoint_hash
    checkpoint_hash        BYTEA NOT NULL,       -- see Section 3 for hash input
    sealed_at              TIMESTAMPTZ NOT NULL, -- included in checkpoint_hash; cannot be backdated
    hash_schema_version    INTEGER NOT NULL DEFAULT 1,
    sealer_version         TEXT NOT NULL         -- aegis-migrate semver; unauthenticated debug metadata
);

CREATE UNIQUE INDEX ON audit_checkpoints (range_start, range_end);
CREATE INDEX ON audit_checkpoints (prev_checkpoint_id);
```

`sealed_at` is inside the hash so checkpoint timestamps cannot be backdated by altering the row after sealing. `sealer_version` is outside the hash — it is debug metadata, not security-relevant.

### `audit_purges`

```sql
CREATE TABLE audit_purges (
    id                       BIGSERIAL PRIMARY KEY,
    purged_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    range_start              BIGINT NOT NULL,      -- lowest event_id deleted
    range_end                BIGINT NOT NULL,      -- highest event_id deleted
    rows_deleted             INTEGER NOT NULL,
    checkpoint_ids_affected  BIGINT[] NOT NULL,    -- checkpoint IDs whose ranges overlap this purge
    purge_event_id           BIGINT REFERENCES audit_events(id)  -- the audit_events row for this purge
);
```

`audit_purges` is **never purged**. `verify-chain --full` consults it to distinguish attested-but-unverifiable ranges from genuine chain breaks (see Section 7).

No changes to `audit_events`. Zero request-path cost.

---

## 3. Checkpoint hash construction

### Genesis constant

The first checkpoint has no predecessor. Its `prev_checkpoint_hash` is defined as 32 zero bytes: `0x0000000000000000000000000000000000000000000000000000000000000000`. This is a normative constant; do not substitute NULL or an empty value.

### Hash input (schema version 1)

```
checkpoint_hash = SHA-256(
    merkle_root            ||   -- 32 bytes
    prev_checkpoint_hash   ||   -- 32 bytes (genesis constant for first checkpoint)
    uint64_le(range_start) ||   -- 8 bytes, little-endian
    uint64_le(range_end)   ||   -- 8 bytes, little-endian
    uint32_le(event_count) ||   -- 4 bytes, little-endian
    uint32_le(hash_schema_version) ||  -- 4 bytes, little-endian
    sealed_at_micros            -- 8 bytes, little-endian; microseconds since Unix epoch UTC
)
```

Where `||` is byte concatenation. Total input: 96 bytes for schema versions 1 and 2.

The chain binds `prev_checkpoint_hash` — the previous checkpoint's own hash — not its `merkle_root`. An attacker who alters a checkpoint's metadata fields (range bounds, event count) must recompute that checkpoint's `checkpoint_hash` and then every subsequent checkpoint's hash. Altering the Merkle root alone is not sufficient.

### Hash input (schema version 3)

```
checkpoint_hash = SHA-256(
    merkle_root            ||   -- 32 bytes
    prev_checkpoint_hash   ||   -- 32 bytes (genesis constant for first checkpoint)
    uint64_le(range_start) ||   -- 8 bytes, little-endian
    uint64_le(range_end)   ||   -- 8 bytes, little-endian
    uint32_le(event_count) ||   -- 4 bytes, little-endian
    uint32_le(hash_schema_version) ||  -- 4 bytes, little-endian
    sealed_at_micros       ||   -- 8 bytes, little-endian; microseconds since Unix epoch UTC
    int64_le(prev_checkpoint_id)  -- 8 bytes, little-endian; 0 for genesis
)
```

Total input: **104 bytes for schema version 3**. The first 96 bytes are byte-identical to versions 1 and 2, so an implementation of both shares everything but the tail.

`prev_checkpoint_id` is `0` for the genesis checkpoint, a normative constant in the same way the genesis `prev_checkpoint_hash` is 32 zero bytes: the input is fixed width, so "no predecessor" needs a defined encoding rather than an absent one.

**Why version 3 binds the predecessor's identity.** Versions 1 and 2 covered the predecessor's *hash* but not *which row it was*. A checkpoint could therefore be repointed at an earlier one, and if `prev_checkpoint_hash` was updated to match, every digest still verified and the foreign key was still satisfied while the checkpoints in between were silently detached. Nothing in the cryptography objected; only a comparison of the stored ordering against itself could notice, and that ordering lives in the same table an attacker with write access would have altered. Binding the id makes the detachment change the digest, so an offline verifier detects it without needing to have witnessed the original ordering. This closes ADR 0006 and issue #38.

---

## 4. Leaf and node hashing (RFC 6962)

**Leaf nodes:**
```
leaf_hash(event) = SHA-256(0x00 || JCS(event))
```

**Internal nodes:**
```
internal_hash(left, right) = SHA-256(0x01 || left || right)
```

The `0x00` and `0x01` prefix bytes provide domain separation. A 64-byte input cannot be ambiguously interpreted as either a leaf or an internal node.

**Odd node count:** Promote the unpaired node unchanged (pass it to the next level as-is). Do not duplicate it. This avoids the distinct-leaf-set/identical-root flaw.

---

## 5. Canonical serialization (JCS / RFC 8785, schema version 1)

`audit_events.metadata` is JSONB. PostgreSQL, Go's `encoding/json`, Python's `json`, and JavaScript's `JSON.stringify` all produce different byte sequences for the same data. Hand-rolling a canonical form that survives cross-language verification is fragile; RFC 8785 (JSON Canonicalization Scheme) specifies it with test vectors.

Apply JCS to the full event row serialized as a JSON object. Field rules:
- Include only columns present in `audit_events` at schema version 1 (see Section 8 for version handling)
- Keys in Unicode code point order (JCS requirement)
- Timestamps: ISO 8601, microsecond precision, UTC `Z` suffix — e.g., `"2026-08-21T14:32:00.123456Z"`. TIMESTAMPTZ carries microseconds; do not claim nanosecond precision.
- Numbers: ECMAScript number serialization as specified by JCS (finite values only; `NaN` and `Infinity` are a schema error)
- Null values: JSON `null`

The canonicalization spec identifier to include in API responses (Section 9) is `"rfc8785-v1"`.

---

## 5.1 Field set, schema version 2

Version 2 is the field set migration 013 established. It replaced the single
`metadata` JSONB field with twelve typed columns, so that no column on the audit
table is untyped or unbounded.

**Everything in Section 5 still applies unchanged**: RFC 8785, keys in Unicode code
point order, microsecond UTC timestamps, ECMAScript numbers, JSON `null` for
absent values. The canonicalization spec identifier is still `"rfc8785-v1"`; it
names the canonicalization, not the field set. The checkpoint hash construction of
Section 3 is also unchanged, the same 96 bytes in the same order, carrying `2` in
the version scalar.

The leaf is the SHA-256 of `0x00 || JCS(event)` over these twenty-six fields:

| Field | Source | Notes |
|---|---|---|
| `api_key_id` | v1 | |
| `api_key_prefix` | **v2** | promoted from `metadata."api_key_prefix"` |
| `endpoint` | v1 | |
| `error_detail` | **v2** | promoted from `metadata."error"`, renamed to stay distinct from `error_message` |
| `error_message` | v1 | |
| `event_type` | v1 | |
| `filter_type` | **v2** | promoted from `metadata."filter_type"` |
| `id` | v1 | |
| `ip_address` | v1 | |
| `limit_cents` | **v2** | promoted from `metadata."limit_cents"` |
| `limit_dimension` | **v2** | promoted from `metadata."dimension"`, renamed because a bare "dimension" does not say what it dimensions |
| `limit_value` | **v2** | promoted from `metadata."limit"`, renamed because `LIMIT` is a reserved word |
| `method` | v1 | |
| `mode` | **v2** | promoted from `metadata."mode"` |
| `model` | **v2** | promoted from `metadata."model"` |
| `operation` | **v2** | promoted from `metadata."operation"` |
| `organization_id` | v1 | |
| `provider` | **v2** | promoted from `metadata."provider"` |
| `reason` | **v2** | promoted from `metadata."reason"` |
| `request_id` | v1 | |
| `spent_cents` | **v2** | promoted from `metadata."spent_cents"` |
| `status_code` | v1 | |
| `team_id` | v1 | |
| `timestamp` | v1 | |
| `user_agent` | v1 | |
| `user_id` | v1 | |

`metadata` is gone. It is the one field of version 1 that version 2 does not carry.

The twelve counters and strings are nullable, and an absent value is JSON `null`
rather than `0` or `""`. That distinction is load-bearing: a rate-limit event
that carries no limit is not an event whose limit was zero, and encoding it as
zero would make two different events hash the same.

### Why version 2 supersedes version 1 rather than joining it

Section 8 says a version-1 checkpoint stays verifiable "regardless of what columns
exist in the current schema". That holds only while the columns a version-1 leaf
reads still exist, and version 2 drops one of them. A version-1 leaf hash cannot
be recomputed without `metadata`.

Migration 013 resolves this by refusing to run in a database that holds any
version-1 checkpoint, rather than by carrying `metadata` forever or by dropping it
and hoping. The consequence is worth stating, because it is what keeps the
verifier simple: **any database that has run migration 013 provably has no
version-1 checkpoints**, so a build that requires schema 13 never needs a
version-1 leaf implementation. `Verifier` still checks each checkpoint's
`hash_schema_version` and reports anything it cannot recompute as
attested-but-unverifiable, rather than hashing the wrong field set and reporting
the mismatch as tampering.

---

## 5.2 Field set, schema version 3

Version 3 is the field set migration 016 established. It adds six columns recording
what a permitted request actually did, and changes the checkpoint hash construction
of Section 3 to bind the predecessor's identity.

**Everything in Section 5 still applies unchanged**: RFC 8785, keys in Unicode code
point order, microsecond UTC timestamps, ECMAScript numbers, JSON `null` for absent
values. The canonicalization spec identifier is still `"rfc8785-v1"`.

The leaf is the SHA-256 of `0x00 || JCS(event)` over **thirty-two** fields: the
twenty-six of version 2, unchanged in name and encoding, plus these six.

| Field | Source | Notes |
|---|---|---|
| `classification` | **v3** | the tier on the presenting API key, the authority the request ran under |
| `completion_tokens` | **v3** | provider-reported; `null` where the provider reported none |
| `duration_ms` | **v3** | gateway-measured wall time |
| `model_served` | **v3** | the model the provider returned, as distinct from `model`, which is the alias the caller requested |
| `prompt_tokens` | **v3** | provider-reported |
| `total_tokens` | **v3** | provider-reported |

Absent measurements are `null`, never `0`. A request whose provider reported no
usage did not consume zero tokens, and a row saying so would attest a measurement
nobody took.

**Every version-3 field is gateway- or provider-derived.** None carries caller text.
That is a requirement rather than an observation: `audit_events` is sealed and
exported, so a column a caller can write is a channel out of the zero-retention
guarantee. It is why `model_served` is safe where the requested `model` needed the
`(unconfigured)` sentinel, and why tool names are **not** in this version.

### Why version 3 does not force a re-seal, where version 2 did

Migration 013 refused to run while any version-1 checkpoint existed, because it
**dropped** `metadata`, a column the version-1 leaf covers: once gone, those leaves
could never be recomputed and every version-1 checkpoint would have become
permanently unverifiable.

Migration 016 only **adds**. The version-2 leaf covers an explicit field list rather
than every column of the row, so a version-2 leaf still recomputes byte-for-byte on
a database that has run 016. A chain may therefore hold version-2 and version-3
checkpoints side by side, and `verify-chain --full` verifies both.

The cost is that a verifier computes two field sets rather than one, which is the
simplicity version 2 was careful to preserve. The trade was made deliberately: the
alternative was refusing to run against existing chains, and adding a column should
not destroy old evidence.

---

## 6. Sealer (`aegis-migrate seal`)

### Visibility watermark

BIGSERIAL IDs are allocated at INSERT and become visible at COMMIT. A transaction holding ID 105 can commit after one holding ID 107. A sealer that selects `id > last_range_end` will seal IDs 100–110 while 105 is still in flight; ID 105 then lands permanently below the sealed watermark, invisible to every future checkpoint and indistinguishable from a deleted row.

**Fix:** Apply a conservative time lag. Seal only events older than a configurable safety window (default: 5 minutes, configurable as `audit.seal_lag_seconds`). Events within the lag window are left unsealed until the next run.

Operators who need tighter attestation windows (low-traffic deployments where the lag is unnecessary) may set `audit.seal_lag_seconds: 0` and accept the gap risk, but the documentation must state the risk explicitly.

### Concurrency

Two overlapping sealer runs (a cron retry, a slow first run) can compute overlapping ranges and insert two checkpoints claiming the same predecessor. The unique index on `(range_start, range_end)` does not prevent this — it only blocks exact duplicate ranges.

**Fix:** Take `pg_advisory_lock(AEGIS_SEAL_LOCK_KEY)` for the duration of the seal operation.

`AEGIS_SEAL_LOCK_KEY` is the first 8 bytes of `SHA-256("aegis_seal")` read as a
little-endian `int64` — `4367013267506373021`. It is stated here as a normative
constant because a maintenance script or an operator in psql must be able to take
*the same* lock; a key derived any other way (for example `hashtext('aegis_seal')`,
which is a 32-bit value) does not exclude the sealer, and both writers would run
concurrently and could produce competing checkpoints.

```sql
-- Block the sealer while doing manual maintenance:
SELECT pg_advisory_lock(4367013267506373021);
-- ... maintenance ...
SELECT pg_advisory_unlock(4367013267506373021);
``` The sealer is single-writer by construction. Document this explicitly; operators who see a blocked sealer should investigate the prior run rather than force-killing the lock.

### Algorithm

```
1. Acquire pg_advisory_lock
2. Read last sealed range_end (0 if no checkpoints)
3. Compute visibility watermark = NOW() - seal_lag_seconds
4. Select events WHERE id > last_range_end AND created_at < watermark ORDER BY id ASC LIMIT batch_ceiling
5. If no events: release lock, exit (nothing to seal)
6. Compute leaf hashes (RFC 6962), build Merkle tree, compute root
7. Read previous checkpoint's checkpoint_hash (genesis constant if none)
8. Insert checkpoint row in a transaction
9. Release lock
10. If more events remain beyond the batch ceiling: repeat from step 2
```

**Cadence:** External scheduling — cron or Kubernetes CronJob. Recommended default: hourly, with `audit.seal_batch_ceiling: 10000` as a safety valve under burst. Whichever limit triggers first ends the run; the next run continues from the new watermark.

The hourly cadence is expressible as an SLO: "audit events are attested within one hour of being written." The batch ceiling exists only to bound proof size and memory under load.

**Idempotency:** Re-running the sealer on an already-caught-up state (no events beyond the watermark) is a no-op. The sealer never re-seals an existing range.

**At shutdown:** Unsealed events are fine. The request path does not depend on sealing. The next run picks them up. There is no partially-sealed state.

---

## 7. Purge interaction (T20)

The purge and seal commands are designed to compose cleanly. The required sequence to preserve full attestation is:

```
aegis-migrate seal          # seal all events including the upcoming purge window
aegis-migrate purge --before DATE   # delete events, write purge audit event
aegis-migrate seal          # seal the purge event itself
```

The `purge` command warns (but does not block) if it detects unsealed events in the target window and prints the recommended sequence.

**What gets written during purge:**
1. An `audit_events` row with `event_type: audit_purge`, recording the `range_start`, `range_end`, and `rows_deleted`
2. An `audit_purges` row (never deleted) recording the same information plus the affected `checkpoint_ids`

Both are written in the same transaction as the delete. If the transaction rolls back, no records were deleted and no purge log exists.

**After purge, `verify-chain --full` behavior:** For each checkpoint whose range overlaps an entry in `audit_purges`, the verifier reports the range as "attested-but-unverifiable (purged, see audit_purge event #N)" rather than a chain break. The checkpoint Merkle root still attests that the events existed; the rows are simply gone and cannot be re-hashed.

---

## 8. Schema versioning

`hash_schema_version` in `audit_checkpoints` records which serialization spec applies. Rules:

- **Version 1:** columns defined in the initial `audit_events` migration, using JCS/RFC 8785 with the field rules in Section 5.
- **Version 2:** the field set in Section 5.1, established by migration 013.
- **When a new column is added to `audit_events`:** increment `hash_schema_version`, define the new serialization rules in a new section of this document, and note in the migration file which schema version takes effect for events from that migration forward.
- **When a column the leaf hash reads is removed or renamed:** the same, plus one thing more. Older checkpoints become unverifiable rather than merely old, because their leaves cannot be recomputed from a schema that no longer has the column. Such a migration must refuse to run while checkpoints at the older version exist, as 013 does, so the loss is a blocked upgrade a human decides about rather than a chain break discovered months later.
- Events sealed before the schema change remain under their original version. The sealer writes new checkpoints under the new version.
- `verify-chain` reads each checkpoint's `hash_schema_version` and applies the matching spec, and reports a version it cannot recompute as attested-but-unverifiable rather than as a chain break. A version whose columns the current schema still has is verifiable with that version's rules; a version whose columns have been dropped is not, which is why the preceding rule exists.

---

## 9. Prometheus observability

Add to `internal/telemetry/`:

| Gauge | Meaning |
|-------|---------|
| `aegis_audit_last_seal_age_seconds` | Seconds since the most recent checkpoint was written. `+Inf` when no checkpoint exists yet, so a single `> threshold` alert covers both a stalled sealer and one that has never run |
| `aegis_audit_unsealed_events` | Count of events not yet covered by any checkpoint |

An unscheduled sealer is visible on a dashboard rather than silent until a disk-fills or an auditor asks. Both gauges use the same pattern as `aegis_pricing_age_days`.

---

## 10. Audit read API (T23 integration)

The `GET /aegis/v1/audit/checkpoints` response for each checkpoint must include:

```json
{
  "id": 42,
  "range_start": 1,
  "range_end": 10000,
  "event_count": 10000,
  "merkle_root": "a3f2...",
  "checkpoint_hash": "b7c1...",
  "sealed_at": "2026-08-21T14:00:00.000000Z",
  "hash_schema_version": 1,
  "canonicalization_spec": "rfc8785-v1"
}
```

`hash_schema_version` and `canonicalization_spec` are required so an external verifier knows how to interpret bytes without reading AEGIS source code.

**Inclusion proof endpoints:**

- `GET /aegis/v1/audit/events/{event_id}/proof` — primary endpoint; server resolves which checkpoint covers this event and returns the Merkle sibling path from leaf to root, plus the checkpoint ID and root. This is what an auditor uses.
- `GET /aegis/v1/audit/checkpoints/{checkpoint_id}/proof?event_id={event_id}` — checkpoint-scoped form for anchoring consumers who are iterating over checkpoints.

Both endpoints are read-only, authenticated, and themselves audited. The gateway's audit API connection should use a read-only PostgreSQL role — a separate role from the write-capable role used for the request path. If the audit API shares a connection pool with the gateway's write path (because they run in the same process), document this explicitly as a deployment constraint rather than claiming a separate credential that does not exist.

**Commercial anchoring hook:** The `/checkpoints` list response provides everything a control-plane service needs to post roots to an external anchor. The default recommended anchor is an RFC 3161 timestamp authority — it is cheap, widely accepted by enterprise procurement, and does not require explaining a ledger to a compliance officer. Blockchain notary is an option but is not the default recommendation.

---

## 11. `aegis-migrate verify-chain`

```
aegis-migrate verify-chain
  --full                  Re-hash retained rows against stored Merkle roots (slow)
  --from-checkpoint N     Start verification from checkpoint N
  --to-checkpoint M       End at checkpoint M (pair with --from-checkpoint for range audits)
  --output json|text      Default: text
  --event E               Produce inclusion proof for event ID E (see /audit/events/{id}/proof)
```

**Default (fast):** Walks the checkpoint chain, verifies each `checkpoint_hash` against stored fields. Does not re-hash event rows. Detects: checkpoint tampering, chain breaks, wrong `prev_checkpoint_hash`. Does not detect: individual `audit_events` row tampering.

**`--full`:** Re-hashes every retained event in each checkpoint's range and compares to stored `merkle_root`. Consults `audit_purges` for any overlap and reports those ranges as attested-but-unverifiable rather than broken. Suitable for periodic audits, not routine monitoring. Always pair with `--from-checkpoint` and `--to-checkpoint` on large tables.

**Output on success:** checkpoint count, total events covered, sealed_at range, unsealed event count.
**Output on first anomaly:** checkpoint ID, what was expected versus what was found, count of remaining checkpoints not verified.

---

## 12. Threat model

**What this detects:** Any modification to `audit_events` rows or `audit_checkpoints` rows by a party who cannot also recompute and rewrite the entire downstream checkpoint chain — including recomputing every `checkpoint_hash` from the altered point forward.

**What this does not prevent:** An attacker with write access to PostgreSQL and the sealing binary can delete rows, recompute Merkle roots, re-seal, and produce a chain that verifies cleanly. The chain proves internal consistency, not that the data was never altered by someone with full database access.

**Mitigation (core):** Restrict PostgreSQL write access to the `aegis-gateway` service account and `aegis-migrate` only. Audit API read access uses a separate read-only role. Document and enforce this in `docs/DEPLOYMENT.md`. An attacker who cannot write to the database cannot silently alter the record.

**External anchoring (commercial tier):** Periodically posting checkpoint hashes to an external system — an RFC 3161 timestamp authority, write-once object storage, or a ledger — means an attacker must also compromise that external system to cover their tracks. The core seals and verifies locally; the control plane anchors off-box. This is the clean commercial boundary.

**What to say to an auditor:** "The audit log is tamper-evident with respect to parties who do not have write access to the database. Anyone with write access and the sealing binary can rewrite history undetected without external anchoring. Our deployment guide restricts that access to two service accounts. External anchoring, which adds a second system an attacker would also need to compromise, is available in the commercial tier."

Say that plainly. Publishing the limits of your own tamper-evidence is rare in this category, and it is what a technical evaluator reads first.
