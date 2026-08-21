# Audit Integrity — Hash-chained Merkle checkpoints

Status: normative. This document is the source of truth for AEGIS audit
tamper-evidence. Implementation must match this spec byte-for-byte or
independent verification will fail.

## 1. Design summary

Events are appended to `audit_events` on the hot path with zero
cryptographic cost. A separate out-of-band sealer subcommand
(`aegis-migrate seal`) periodically computes a Merkle root over a
contiguous range of event IDs and writes a checkpoint into
`audit_checkpoints`. Each checkpoint chains to the previous
checkpoint's own `checkpoint_hash` (not its Merkle root), producing a
hash-chained ledger of Merkle roots.

The design keeps the request path fast and the sealer boring: single
writer, no external dependencies, deterministic output.

## 2. Canonical event serialization

Serialization is normative. Any deviation breaks external verification.

* **Canonical form**: RFC 8785 JSON Canonicalization Scheme (JCS) over
  the map of `audit_events` columns at `hash_schema_version = 1`. Keys
  are lower-case column names.
* **Timestamps**: RFC 3339, microsecond precision, UTC `Z` suffix.
  Example: `"2026-08-21T14:32:00.123456Z"`. TIMESTAMPTZ carries
  microseconds; do not emit nanoseconds.
* **`metadata` (JSONB)**: parsed and re-serialized as a canonical JSON
  value; the resulting canonical object appears under the `metadata`
  key.
* **`null` handling**: SQL NULL columns serialize to JSON `null`.
* **Numbers**: integer columns (id, status_code) serialize as JSON
  numbers with no decimal or exponent parts. Metadata numbers follow
  ECMAScript number formatting per JCS.

The columns covered at `hash_schema_version = 1` are:

```
id, request_id, timestamp, event_type, organization_id, team_id,
user_id, api_key_id, ip_address, user_agent, endpoint, method,
status_code, error_message, metadata
```

### 2.1 Leaf hash

```
leaf = SHA-256( 0x00 || jcs_bytes(event) )
```

The `0x00` prefix is domain separation (RFC 6962 §2.1).

## 3. Merkle tree

Construction follows RFC 6962 §2.1:

* Internal node: `SHA-256( 0x01 || left(32) || right(32) )`.
* Odd count: the odd node at any level is **promoted** to the next
  level unchanged. Do not duplicate it (this prevents CVE-2012-2459).
* Root of an empty range is undefined; sealers must never seal an
  empty range.

## 4. Checkpoint hash

```
checkpoint_hash = SHA-256(
    merkle_root                              -- 32 bytes
 || prev_checkpoint_hash                     -- 32 bytes; 32 zero bytes for genesis
 || uint64_le(range_start)
 || uint64_le(range_end)
 || uint32_le(event_count)
 || uint32_le(hash_schema_version)
 || int64_le(sealed_at_unix_microseconds)   -- UTC microseconds since epoch
)
```

* `sealed_at` is inside the hash, which prevents backdating.
* `sealer_version` is stored on the row but **not** hashed. It is
  explicitly unauthenticated debug metadata.
* The first checkpoint (genesis) uses 32 zero bytes for
  `prev_checkpoint_hash` and NULL for `prev_checkpoint_id`. All
  subsequent checkpoints set `prev_checkpoint_hash` to the previous
  row's `checkpoint_hash`.

## 5. Sealer subcommand

`aegis-migrate seal` flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--since-event` | `0` | Start from event ID N. |
| `--batch-size` | `10000` | Events per checkpoint. |
| `--lag-seconds` | `300` | Only seal events with `created_at < NOW() - lag_seconds`. |

Notes on the visibility watermark: `audit_events` uses `timestamp` as
its wall-clock column (there is no separate `created_at`); the sealer
uses that column when applying the lag watermark.

Algorithm:

1. Acquire `pg_advisory_lock(hashtext('aegis_seal'))`. If not
   immediately available, exit non-zero: another sealer is running.
2. Read the last checkpoint (`range_end`, `id`, `checkpoint_hash`) or
   zero-values if none.
3. Select up to `batch_size` events where
   `id > last_range_end AND id > since_event AND timestamp < NOW() - lag_seconds`
   ordered by `id ASC`.
4. If none: exit cleanly (caught up).
5. Compute the Merkle root over the leaf hashes.
6. Compute `checkpoint_hash` per §4.
7. In a transaction, insert the checkpoint row.
8. Repeat from step 3 until no more eligible events remain.

## 6. Verification subcommand

`aegis-migrate verify-chain` flags:

| Flag | Meaning |
| --- | --- |
| `--full` | Also re-hash retained events per checkpoint range. |
| `--from-checkpoint N` | Start from checkpoint id N. |
| `--to-checkpoint M` | Stop at checkpoint id M. |
| `--event E` | Emit RFC 6962 inclusion proof for event id E. |
| `--output json|text` | Output format. |

Behavior:

* Default (fast path): walk the checkpoint chain, recompute each
  `checkpoint_hash` from stored fields, compare, and detect chain
  breaks, genesis-constant mismatch, and duplicate ranges.
* `--full`: additionally re-hash retained events per range and rebuild
  Merkle roots. If `audit_purges` exists and covers a range, the range
  is reported as `attested-but-unverifiable` rather than broken.
* `--event E`: emit the RFC 6962 inclusion proof (sibling hashes,
  leaf-to-root) along with `checkpoint_id`, hex `checkpoint_hash`,
  `hash_schema_version`, and `canonicalization_spec: "rfc8785-v1"` so
  an auditor can verify independently.

## 7. Metrics

The gateway exposes:

* `aegis_audit_last_seal_age_seconds` — seconds since the most recent
  checkpoint's `sealed_at`.
* `aegis_audit_unsealed_events` — count of events beyond the last
  checkpoint's `range_end`.

Both are refreshed on startup and every five minutes.

## 8. Threat model notes

* An attacker with database write access can delete or modify events
  between seals. Sealing narrows the tamper window to `lag_seconds`
  plus the seal cadence for the affected events.
* An attacker who can also insert into `audit_checkpoints` can only
  forge chains going forward from the point of compromise; any
  external witness that recorded a prior `checkpoint_hash` will detect
  the fork immediately.
* The sealer stores `sealed_at` inside the hash so an attacker who
  gains write access later cannot backdate a forged checkpoint into
  an earlier position in the chain.
