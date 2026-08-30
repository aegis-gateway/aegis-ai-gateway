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
	"errors"
	"fmt"
	"log/slog"
	"time"

	controlplanev1 "github.com/aegis-gateway/aegis-ai-gateway/api/controlplane/v1"
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
	// LagSeconds is the safety window: only seal events older than this.
	// Prevents sealing events still in-flight under concurrent load (BIGSERIAL gap issue).
	//
	// A pointer so that unset is distinguishable from deliberately zero. Nil
	// means DefaultLagSeconds; [SealLag] wraps an explicit value, including
	// zero for a caller that genuinely wants none.
	//
	// It is not a plain int because the zero value of one is the dangerous
	// setting. Sealing with no lag can exclude an in-flight event from the
	// chain permanently, and since seal state now feeds a control plane it
	// would also make a healthy gateway report gap states. A field whose
	// default is the hazardous option is a field that will eventually be left
	// unset by someone who did not know.
	LagSeconds *int
}

// DefaultLagSeconds is the window used when SealOptions.LagSeconds is unset.
//
// It is the protocol's [controlplanev1.DefaultSealLagSeconds], not a second
// copy of 300. `aegis-migrate seal` defaults to this and `aegis-migrate submit`
// defaults to the protocol constant directly; a sealer and a submitter
// disagreeing about the window describes a gateway that does not exist, so the
// two cannot be allowed to drift by editing one literal. ADR 0008.
//
// Kept as an int because SealOptions.LagSeconds is one, and as a named constant
// here so callers of this package need not import the protocol to seal.
const DefaultLagSeconds = int(controlplanev1.DefaultSealLagSeconds)

// SealLag returns a pointer to n, for setting SealOptions.LagSeconds
// explicitly. Use SealLag(0) to seal with no lag, which is a deliberate choice
// and not a default.
func SealLag(n int) *int { return &n }

func (o *SealOptions) applyDefaults() {
	if o.BatchSize <= 0 {
		o.BatchSize = 10000
	}
	if o.LagSeconds == nil {
		o.LagSeconds = SealLag(DefaultLagSeconds)
	}
	if *o.LagSeconds < 0 {
		o.LagSeconds = SealLag(0)
	}
}

// lagSeconds returns the resolved window. applyDefaults runs first, so it is
// never nil by the time this is called.
func (o *SealOptions) lagSeconds() int { return *o.LagSeconds }

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
// ErrSealPausedAtGap reports that sealing stopped because an id gap separates
// the last checkpoint from the next visible event. Unsealed events remain, so
// this is deliberately an error rather than a quiet return: a scheduled sealer
// that treated it as success would report a healthy chain while it stalled.
var ErrSealPausedAtGap = errors.New("seal: paused at an event id gap")

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
		if errors.Is(err, ErrSealPausedAtGap) {
			// Distinct from "caught up": unsealed events remain beyond the gap.
			// Returning an error means a cron invocation exits non-zero and is
			// visible, rather than reporting success while the chain stalls.
			return err
		}
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
	watermark := time.Now().UTC().Add(-time.Duration(s.opts.lagSeconds()) * time.Second)

	// Load up to BatchSize eligible events.
	rows, err := conn.Query(ctx, `
		SELECT `+EventColumnsV3+`
		FROM audit_events
		WHERE id > $1 AND timestamp < $2
		ORDER BY id ASC
		LIMIT $3
	`, effectiveStart, watermark, s.opts.BatchSize)
	if err != nil {
		return false, fmt.Errorf("seal: query events: %w", err)
	}

	events, err := scanEventRowsV3(rows)
	if err != nil {
		return false, fmt.Errorf("seal: scan events: %w", err)
	}
	if len(events) == 0 {
		return false, nil
	}

	// Establish the contiguous run BEFORE hashing anything.
	//
	// BIGSERIAL hands out an id before the inserting transaction commits, so a
	// transaction holding id N can still be in flight while N+1 commits and
	// becomes visible. Sealing to the highest *visible* id would checkpoint
	// N+1; once N commits it sits below effectiveStart forever and is silently
	// excluded from the chain — a valid audit event, permanently unattested.
	// The timestamp lag does not prevent this: `timestamp` defaults to the
	// transaction's start time, so a long transaction carries an old timestamp
	// and still commits late.
	//
	// Two conditions have to hold, and both must be checked here rather than
	// after the Merkle root is built, or the checkpoint would attest a root
	// computed over a different set of events than the range it claims.

	// 1. The batch must begin exactly where the last checkpoint ended.
	//    Otherwise a hole sits between the two and this run would seal past it,
	//    which is the same silent exclusion in a different place.
	if events[0].ID != effectiveStart+1 {
		slog.Warn("sealing paused: a gap separates the last checkpoint from the next visible event",
			"last_sealed_event_id", effectiveStart,
			"next_visible_event_id", events[0].ID,
			"reason", "an in-flight transaction may still commit into the gap")
		return false, fmt.Errorf("%w: last sealed event %d, next visible event %d",
			ErrSealPausedAtGap, effectiveStart, events[0].ID)
	}

	// 2. Stop at the first gap inside the batch.
	for i := 1; i < len(events); i++ {
		if events[i].ID != events[i-1].ID+1 {
			slog.Warn("sealing stopped at an id gap; events beyond it stay unsealed until it resolves",
				"gap_after_event_id", events[i-1].ID,
				"next_visible_event_id", events[i].ID,
				"reason", "an in-flight transaction may still commit into the gap")
			events = events[:i]
			break
		}
	}

	// Stopping at a gap is conservative: a hole left by a rolled-back insert
	// never fills, so sealing stalls until an operator resolves it. For a
	// tamper-evidence feature a visible stall beats a silent hole in the chain,
	// but a permanent gap needs a decision this code does not make.

	// Compute RFC 6962 leaf hashes and Merkle root over the events actually
	// being sealed, so the root always matches the committed range.
	leaves := make([][]byte, len(events))
	for i, ev := range events {
		lh, err := EventLeafHashV3(ev)
		if err != nil {
			return false, fmt.Errorf("seal: leaf hash event %d: %w", ev.ID, err)
		}
		leaves[i] = lh
	}
	merkleRoot := MerkleRoot(leaves)

	rangeStart := events[0].ID
	rangeEnd := events[len(events)-1].ID

	// Wall-clock extent of the batch, for answering coverage questions without
	// a lookup against the events themselves.
	//
	// A scan rather than first-and-last. Event IDs are allocated at insert and
	// become visible at commit, so a long transaction carries an older
	// timestamp and commits later: within one contiguous ID run the timestamps
	// are not necessarily ordered, and consecutive checkpoints can overlap in
	// time. Taking events[0] and events[len-1] would understate the extent
	// whenever that happened.
	coveredFrom, coveredTo := events[0].Timestamp.UTC(), events[0].Timestamp.UTC()
	for _, ev := range events[1:] {
		if ts := ev.Timestamp.UTC(); ts.Before(coveredFrom) {
			coveredFrom = ts
		} else if ts.After(coveredTo) {
			coveredTo = ts
		}
	}

	eventCount := int32(len(events))
	sealedAt := time.Now().UTC()

	// Read previous checkpoint hash (genesis constant if none).
	prevHash, prevID, err := readPrevCheckpointHash(ctx, conn)
	if err != nil {
		return false, fmt.Errorf("seal: read prev checkpoint: %w", err)
	}

	// Compute checkpoint hash per docs/AUDIT-INTEGRITY.md §3.
	// The version hashed and the version stored must be the same number. They
	// were both the literal 1 before migration 013; they are both this constant
	// now, so they cannot drift apart in a later edit.
	// readPrevCheckpointHash reports -1 for genesis; the hash input encodes
	// "no predecessor" as GenesisPrevCheckpointID, the same way prevHash
	// encodes it as 32 zero bytes rather than an absent value.
	prevIDHashed := prevID
	if prevIDHashed < 0 {
		prevIDHashed = controlplanev1.GenesisPrevCheckpointID
	}
	cpHash, err := controlplanev1.ComputeCheckpointHashV3(
		merkleRoot, prevHash, rangeStart, rangeEnd, eventCount,
		controlplanev1.HashSchemaVersion3, sealedAt, prevIDHashed)
	if err != nil {
		return false, fmt.Errorf("seal: compute checkpoint hash: %w", err)
	}

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
		     hash_schema_version, sealed_at, sealer_version, canonicalization_spec,
		     covered_from, covered_to, covered_range_source)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$12,$8,$9,'rfc8785-v1',$10,$11,'sealed')
	`, rangeStart, rangeEnd, eventCount, merkleRoot,
		prevIDArg, prevHashStored, cpHash,
		sealedAt, SealerVersion,
		coveredFrom, coveredTo,
		controlplanev1.HashSchemaVersion3,
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
		"covered_from", coveredFrom.Format(time.RFC3339Nano),
		"covered_to", coveredTo.Format(time.RFC3339Nano),
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
		// Genesis: 32 zero bytes per docs/AUDIT-INTEGRITY.md §3.
		return controlplanev1.GenesisPrevHashBytes(), -1, nil
	}
	if err != nil {
		return nil, 0, err
	}
	return prevHash, prevID, nil
}

// computeCheckpointHash returns the checkpoint hash for the given fields.
//
// The construction itself lives in api/controlplane/v1, which is the public
// protocol package and the single normative implementation. It is there rather
// than here because more than one party needs to produce these bytes: the
// sealer, a control plane confirming that what it stored can be re-derived,
// and an independent verifier checking an evidence bundle years later. Two
// implementations of one specification is the drift the specification exists
// to prevent.
//
// Callers must pass the 32-byte genesis constant for the first checkpoint,
// never nil. See docs/adr/0007-hash-construction-belongs-to-the-protocol.md.
func computeCheckpointHash(merkleRoot, prevHash []byte, rangeStart, rangeEnd int64, eventCount, schemaVersion int32, sealedAt time.Time) ([]byte, error) {
	return controlplanev1.ComputeCheckpointHash(
		merkleRoot, prevHash, rangeStart, rangeEnd, eventCount, schemaVersion, sealedAt)
}

// scanEventRowsV3 reads rows selected with [EventColumnsV3].
//
// Separate from scanEventRows because the column lists differ and pgx scans
// positionally: passing version-2 rows to this, or the reverse, silently binds
// the wrong columns rather than failing.
func scanEventRowsV3(rows pgx.Rows) ([]AuditEventRow, error) {
	defer rows.Close()
	var out []AuditEventRow
	for rows.Next() {
		var r AuditEventRow
		if err := rows.Scan(
			&r.ID, &r.RequestID, &r.Timestamp, &r.EventType,
			&r.OrganizationID, &r.TeamID, &r.UserID, &r.APIKeyID,
			&r.IPAddress, &r.UserAgent, &r.Endpoint, &r.Method,
			&r.StatusCode, &r.ErrorMessage,
			&r.APIKeyPrefix, &r.LimitDimension, &r.LimitValue,
			&r.SpentCents, &r.LimitCents, &r.FilterType, &r.Reason,
			&r.Provider, &r.Model, &r.Mode, &r.Operation, &r.ErrorDetail,
			&r.ModelServed, &r.Classification,
			&r.PromptTokens, &r.CompletionTokens, &r.TotalTokens, &r.DurationMs,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// scanEventRows reads all rows from pgx.Rows into a slice of AuditEventRow.
func scanEventRows(rows pgx.Rows) ([]AuditEventRow, error) {
	defer rows.Close()
	var out []AuditEventRow
	for rows.Next() {
		var r AuditEventRow
		if err := rows.Scan(
			&r.ID, &r.RequestID, &r.Timestamp, &r.EventType,
			&r.OrganizationID, &r.TeamID, &r.UserID, &r.APIKeyID,
			&r.IPAddress, &r.UserAgent, &r.Endpoint, &r.Method,
			&r.StatusCode, &r.ErrorMessage,
			&r.APIKeyPrefix, &r.LimitDimension, &r.LimitValue,
			&r.SpentCents, &r.LimitCents, &r.FilterType, &r.Reason,
			&r.Provider, &r.Model, &r.Mode, &r.Operation, &r.ErrorDetail,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
