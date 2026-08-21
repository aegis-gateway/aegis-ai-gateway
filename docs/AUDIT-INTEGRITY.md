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

Where `||` is byte concatenation. Total input: 96 bytes for schema version 1.

The chain binds `prev_checkpoint_hash` — the previous checkpoint's own hash — not its `merkle_root`. An attacker who alters a checkpoint's metadata fields (range bounds, event count) must recompute that checkpoint's `checkpoint_hash` and then every subsequent checkpoint's hash. Altering the Merkle root alone is not sufficient.

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

## 6. Sealer (`aegis-migrate seal`)

### Visibility watermark

BIGSERIAL IDs are allocated at INSERT and become visible at COMMIT. A transaction holding ID 105 can commit after one holding ID 107. A sealer that selects `id > last_range_end` will seal IDs 100–110 while 105 is still in flight; ID 105 then lands permanently below the sealed watermark, invisible to every future checkpoint and indistinguishable from a deleted row.

**Fix:** Apply a conservative time lag. Seal only events older than a configurable safety window (default: 5 minutes, configurable as `audit.seal_lag_seconds`). Events within the lag window are left unsealed until the next run.

Operators who need tighter attestation windows (low-traffic deployments where the lag is unnecessary) may set `audit.seal_lag_seconds: 0` and accept the gap risk, but the documentation must state the risk explicitly.

### Concurrency

Two overlapping sealer runs (a cron retry, a slow first run) can compute overlapping ranges and insert two checkpoints claiming the same predecessor. The unique index on `(range_start, range_end)` does not prevent this — it only blocks exact duplicate ranges.

**Fix:** Take `pg_advisory_lock(AEGIS_SEAL_LOCK_KEY)` for the duration of the seal operation. The sealer is single-writer by construction. Document this explicitly; operators who see a blocked sealer should investigate the prior run rather than force-killing the lock.

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
- **When a new column is added to `audit_events`:** increment `hash_schema_version`, define the new serialization rules in a new section of this document, and note in the migration file which schema version takes effect for events from that migration forward.
- Events sealed before the schema change remain under their original version. The sealer writes new checkpoints under the new version.
- `verify-chain` reads each checkpoint's `hash_schema_version` and applies the matching spec. Version 1 checkpoints are always verifiable with version 1 rules, regardless of what columns exist in the current schema.

---

## 9. Prometheus observability

Add to `internal/telemetry/`:

| Gauge | Meaning |
|-------|---------|
| `aegis_audit_last_seal_age_seconds` | Seconds since the most recent checkpoint was written |
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
