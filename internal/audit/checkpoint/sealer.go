// Copyright 2026 Atlantic Frontier Corporations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// aegisSealLockKey is the pg_advisory_lock key for the audit sealer.
// Precomputed from the first 8 bytes of SHA-256("aegis_seal") as int64 little-endian.
var aegisSealLockKey int64

func init() {
	h := sha256.Sum256([]byte("aegis_seal"))
	aegisSealLockKey = int64(binary.LittleEndian.Uint64(h[:8]))
}

// SealerVersion is embedded in checkpoint rows as unauthenticated debug metadata.
// Set via ldflags: -X github.com/aegis-gateway/aegis-ai-gateway/internal/audit/checkpoint.SealerVersion=1.2.3
var SealerVersion = "dev"

// SealOptions controls a sealer run.
type SealOptions struct {
	// SinceEvent is the minimum event ID to seal from (default 0 = full history).
	SinceEvent int64
	// BatchSize is the max events per checkpoint (default 10000).
	BatchSize int
	// LagSeconds is the safety window: only seal events older than this (default 300).
	// Prevents sealing events still in-flight under concurrent load (BIGSERIAL gap issue).
	LagSeconds int
}

func (o *SealOptions) applyDefaults() {
	if o.BatchSize <= 0 {
		o.BatchSize = 10000
	}
	if o.LagSeconds < 0 {
		o.LagSeconds = 0
	}
}

// RunSeal acquires the single-writer advisory lock and seals all outstanding events
// in batches until caught up. See docs/AUDIT-INTEGRITY.md §6 for the full algorithm.
func RunSeal(ctx context.Context, db *pgxpool.Pool, opts SealOptions) error {
	opts.applyDefaults()
	return NewSealer(db, opts).Run(ctx)
}

// Sealer executes Merkle checkpoint sealing under a pg_advisory_lock.
type Sealer struct {
	db   *pgxpool.Pool
	opts SealOptions
	log  *slog.Logger
}

// NewSealer creates a Sealer. Call Run() to execute the seal loop.
func NewSealer(db *pgxpool.Pool, opts SealOptions) *Sealer {
	opts.applyDefaults()
	return &Sealer{db: db, opts: opts, log: slog.Default()}
}

// Run acquires the advisory lock and seals all outstanding events.
func (s *Sealer) Run(ctx context.Context) error {
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("seal: acquire connection: %w", err)
	}
	defer conn.Release()

	// Single-writer enforcement via pg_try_advisory_lock.
	// Returns false immediately if another session holds the lock.
	var locked bool
	if err := conn.QueryRow(ctx,
		"SELECT pg_try_advisory_lock($1)", aegisSealLockKey,
	).Scan(&locked); err != nil {
		return fmt.Errorf("seal: advisory lock check: %w", err)
	}
	if !locked {
		return fmt.Errorf("seal: another sealer instance holds the advisory lock (key=%d); investigate before retrying", aegisSealLockKey)
	}
	s.log.Info("seal: advisory lock acquired", "key", aegisSealLockKey)
	defer conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", aegisSealLockKey) //nolint:errcheck

	for {
		sealed, err := s.sealBatch(ctx, conn.Conn())
		if err != nil {
			return err
		}
		if !sealed {
			s.log.Info("seal: all events sealed (caught up)")
			return nil
		}
	}
}

// sealBatch seals one batch of events. Returns true when a checkpoint was written.
func (s *Sealer) sealBatch(ctx context.Context, conn *pgx.Conn) (bool, error) {
	// Find last sealed range_end (0 if no checkpoints exist).
	var lastRangeEnd int64
	if err := conn.QueryRow(ctx,
		"SELECT COALESCE(MAX(range_end), 0) FROM audit_checkpoints",
	).Scan(&lastRangeEnd); err != nil {
		return false, fmt.Errorf("seal: read last range_end: %w", err)
	}

	// Honour --since-event: the effective lower bound is max(lastRangeEnd, sinceEvent-1).
	effectiveStart := lastRangeEnd
	if s.opts.SinceEvent > 0 && s.opts.SinceEvent-1 > effectiveStart {
		effectiveStart = s.opts.SinceEvent - 1
	}

	// Visibility watermark: only seal events older than LagSeconds.
	watermark := time.Now().UTC().Add(-time.Duration(s.opts.LagSeconds) * time.Second)

	// Load up to BatchSize eligible events.
	rows, err := conn.Query(ctx, `
		SELECT id, request_id, timestamp, event_type,
		       organization_id, team_id, user_id, api_key_id,
		       ip_address, user_agent, endpoint, method,
		       status_code, error_message, metadata
		FROM audit_events
		WHERE id > $1 AND timestamp < $2
		ORDER BY id ASC
		LIMIT $3
	`, effectiveStart, watermark, s.opts.BatchSize)
	if err != nil {
		return false, fmt.Errorf("seal: query events: %w", err)
	}

	events, err := scanEventRows(rows)
	if err != nil {
		return false, fmt.Errorf("seal: scan events: %w", err)
	}
	if len(events) == 0 {
		return false, nil
	}

	// Compute RFC 6962 leaf hashes and Merkle root.
	leaves := make([][]byte, len(events))
	for i, ev := range events {
		lh, err := EventLeafHash(ev)
		if err != nil {
			return false, fmt.Errorf("seal: leaf hash event %d: %w", ev.ID, err)
		}
		leaves[i] = lh
	}
	merkleRoot := MerkleRoot(leaves)

	rangeStart := events[0].ID
	rangeEnd := events[len(events)-1].ID
	eventCount := int32(len(events))
	sealedAt := time.Now().UTC()

	// Read previous checkpoint hash (genesis constant if none).
	prevHash, prevID, err := readPrevCheckpointHash(ctx, conn)
	if err != nil {
		return false, fmt.Errorf("seal: read prev checkpoint: %w", err)
	}

	// Compute checkpoint hash per docs/AUDIT-INTEGRITY.md §3.
	cpHash := computeCheckpointHash(merkleRoot, prevHash, rangeStart, rangeEnd, eventCount, 1, sealedAt)

	// Insert checkpoint in a transaction.
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("seal: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var prevIDArg *int64
	if prevID >= 0 {
		prevIDArg = &prevID
	}
	// For genesis, prev_checkpoint_hash is stored as 32 zero bytes (not NULL).
	prevHashStored := prevHash // prevHash is already 32 zero bytes for genesis.

	_, err = tx.Exec(ctx, `
		INSERT INTO audit_checkpoints
		    (range_start, range_end, event_count, merkle_root,
		     prev_checkpoint_id, prev_checkpoint_hash, checkpoint_hash,
		     hash_schema_version, sealed_at, sealer_version, canonicalization_spec)
		VALUES ($1,$2,$3,$4,$5,$6,$7,1,$8,$9,'rfc8785-v1')
	`, rangeStart, rangeEnd, eventCount, merkleRoot,
		prevIDArg, prevHashStored, cpHash,
		sealedAt, SealerVersion,
	)
	if err != nil {
		return false, fmt.Errorf("seal: insert checkpoint: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("seal: commit: %w", err)
	}

	s.log.Info("seal: checkpoint written",
		"range_start", rangeStart,
		"range_end", rangeEnd,
		"event_count", eventCount,
	)
	return true, nil
}

// readPrevCheckpointHash returns the checkpoint_hash of the most recent checkpoint
// and its ID. Returns (32-zero-bytes, -1, nil) for genesis (no prior checkpoint).
func readPrevCheckpointHash(ctx context.Context, conn *pgx.Conn) ([]byte, int64, error) {
	var prevHash []byte
	var prevID int64
	err := conn.QueryRow(ctx,
		"SELECT id, checkpoint_hash FROM audit_checkpoints ORDER BY id DESC LIMIT 1",
	).Scan(&prevID, &prevHash)
	if err == pgx.ErrNoRows {
		// Genesis: use 32 zero bytes per docs/AUDIT-INTEGRITY.md §3.
		return make([]byte, 32), -1, nil
	}
	if err != nil {
		return nil, 0, err
	}
	return prevHash, prevID, nil
}

// computeCheckpointHash returns SHA-256 over the following length-prefixed input:
//
//	merkle_root (32 bytes)
//	|| uint32_le(len(prev_checkpoint_hash))   // 0 for genesis, 32 for non-genesis
//	|| prev_checkpoint_hash (0 or 32 bytes)
//	|| uint64_le(range_start)
//	|| uint64_le(range_end)
//	|| uint32_le(event_count)
//	|| uint32_le(hash_schema_version)
//	|| int64_le(sealed_at_unix_microseconds)
//
// The uint32 length prefix lets verifiers distinguish a genesis checkpoint
// (empty/nil prevHash) from a non-genesis checkpoint whose hash happens to be
// 32 zero bytes, preventing an otherwise identical hash input.
func computeCheckpointHash(merkleRoot, prevHash []byte, rangeStart, rangeEnd int64, eventCount, schemaVersion int32, sealedAt time.Time) []byte {
	h := sha256.New()
	h.Write(merkleRoot)
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(prevHash)))
	h.Write(lenBuf[:])
	h.Write(prevHash)
	var scalars [32]byte
	binary.LittleEndian.PutUint64(scalars[0:8], uint64(rangeStart))
	binary.LittleEndian.PutUint64(scalars[8:16], uint64(rangeEnd))
	binary.LittleEndian.PutUint32(scalars[16:20], uint32(eventCount))
	binary.LittleEndian.PutUint32(scalars[20:24], uint32(schemaVersion))
	binary.LittleEndian.PutUint64(scalars[24:32], uint64(sealedAt.UnixMicro()))
	h.Write(scalars[:])
	return h.Sum(nil)
}

// scanEventRows reads all rows from pgx.Rows into a slice of AuditEventRow.
func scanEventRows(rows pgx.Rows) ([]AuditEventRow, error) {
	defer rows.Close()
	var out []AuditEventRow
	for rows.Next() {
		var r AuditEventRow
		var meta []byte
		if err := rows.Scan(
			&r.ID, &r.RequestID, &r.Timestamp, &r.EventType,
			&r.OrganizationID, &r.TeamID, &r.UserID, &r.APIKeyID,
			&r.IPAddress, &r.UserAgent, &r.Endpoint, &r.Method,
			&r.StatusCode, &r.ErrorMessage, &meta,
		); err != nil {
			return nil, err
		}
		r.Metadata = meta
		out = append(out, r)
	}
	return out, rows.Err()
}
