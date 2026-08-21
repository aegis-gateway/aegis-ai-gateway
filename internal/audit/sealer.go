// Copyright 2026 Atlantic Frontier Corporations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockKey is the constant handed to pg_advisory_lock. We derive it
// from a stable string via hashtext() so it is easy to reproduce in psql.
const advisoryLockKey int64 = 0x4145_4749_5f53_454c // "AEGI_SEL" as ASCII

// SealerOptions controls a single invocation of the sealer.
type SealerOptions struct {
	SinceEvent int64         // seal only events with id > this value (default 0)
	BatchSize  int           // events per checkpoint (default 10000)
	LagWindow  time.Duration // safety window before an event is eligible
	// Now returns the current time; injected for tests.
	Now func() time.Time
}

// SealerResult reports what a single Run call did.
type SealerResult struct {
	CheckpointsCreated int
	EventsSealed       int64
	FirstEventID       int64
	LastEventID        int64
}

// Sealer computes and inserts Merkle checkpoints over audit_events.
type Sealer struct {
	db   *pgxpool.Pool
	log  *slog.Logger
	opts SealerOptions
}

// NewSealer wraps a pgxpool with sealing behavior. Callers own the pool
// lifecycle.
func NewSealer(db *pgxpool.Pool, log *slog.Logger, opts SealerOptions) *Sealer {
	if opts.BatchSize <= 0 {
		opts.BatchSize = 10000
	}
	if opts.LagWindow <= 0 {
		opts.LagWindow = 300 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Sealer{db: db, log: log, opts: opts}
}

// ErrLockUnavailable is returned when another sealer is already running.
var ErrLockUnavailable = errors.New("audit: another sealer holds the advisory lock")

// Run seals eligible events until none remain. It acquires a session-level
// advisory lock via pg_try_advisory_lock and releases it on return. Only
// one sealer may run against a given database at a time.
func (s *Sealer) Run(ctx context.Context) (SealerResult, error) {
	var res SealerResult

	conn, err := s.db.Acquire(ctx)
	if err != nil {
		return res, fmt.Errorf("audit: acquire conn: %w", err)
	}
	defer conn.Release()

	var gotLock bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", advisoryLockKey).Scan(&gotLock); err != nil {
		return res, fmt.Errorf("audit: pg_try_advisory_lock: %w", err)
	}
	if !gotLock {
		return res, ErrLockUnavailable
	}
	defer func() {
		if _, uerr := conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", advisoryLockKey); uerr != nil {
			s.log.Warn("failed to release advisory lock", "error", uerr)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		default:
		}

		created, batchInfo, err := s.sealOne(ctx, conn.Conn())
		if err != nil {
			return res, err
		}
		if !created {
			return res, nil
		}
		res.CheckpointsCreated++
		res.EventsSealed += batchInfo.count
		if res.FirstEventID == 0 || batchInfo.firstID < res.FirstEventID {
			res.FirstEventID = batchInfo.firstID
		}
		if batchInfo.lastID > res.LastEventID {
			res.LastEventID = batchInfo.lastID
		}
	}
}

type batchInfo struct {
	count   int64
	firstID int64
	lastID  int64
}

func (s *Sealer) sealOne(ctx context.Context, conn *pgx.Conn) (bool, batchInfo, error) {
	var (
		lastRange   int64
		lastID      int64
		lastHash    []byte
	)
	row := conn.QueryRow(ctx, `
		SELECT range_end, id, checkpoint_hash
		FROM audit_checkpoints
		ORDER BY id DESC
		LIMIT 1
	`)
	if err := row.Scan(&lastRange, &lastID, &lastHash); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return false, batchInfo{}, fmt.Errorf("audit: read last checkpoint: %w", err)
		}
	}
	startAfter := lastRange
	if s.opts.SinceEvent > startAfter {
		startAfter = s.opts.SinceEvent
	}
	watermark := s.opts.Now().Add(-s.opts.LagWindow).UTC()

	events, err := loadEvents(ctx, conn, startAfter, watermark, s.opts.BatchSize)
	if err != nil {
		return false, batchInfo{}, err
	}
	if len(events) == 0 {
		return false, batchInfo{}, nil
	}

	leaves := make([][]byte, 0, len(events))
	for i := range events {
		leaf, err := events[i].LeafHash()
		if err != nil {
			return false, batchInfo{}, err
		}
		leaves = append(leaves, leaf)
	}
	root, err := MerkleRoot(leaves)
	if err != nil {
		return false, batchInfo{}, err
	}

	prevHash := GenesisPrevHash
	var prevID *int64
	if lastID != 0 {
		prevHash = lastHash
		id := lastID
		prevID = &id
	}
	sealedAt := s.opts.Now().UTC().Truncate(time.Microsecond)
	rangeStart := events[0].ID
	rangeEnd := events[len(events)-1].ID
	cp := CheckpointInput{
		MerkleRoot:        root,
		PrevHash:          prevHash,
		RangeStart:        uint64(rangeStart),
		RangeEnd:          uint64(rangeEnd),
		EventCount:        uint32(len(events)),
		HashSchemaVersion: HashSchemaVersion,
		SealedAt:          sealedAt,
	}
	cpHash, err := cp.Hash()
	if err != nil {
		return false, batchInfo{}, err
	}

	var storedPrev interface{}
	if lastID == 0 {
		storedPrev = nil
	} else {
		storedPrev = prevHash
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, batchInfo{}, fmt.Errorf("audit: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO audit_checkpoints (
			range_start, range_end, event_count,
			merkle_root, prev_checkpoint_id, prev_checkpoint_hash,
			checkpoint_hash, hash_schema_version,
			sealed_at, sealer_version, canonicalization_spec
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`, rangeStart, rangeEnd, len(events),
		root, prevID, storedPrev,
		cpHash, HashSchemaVersion,
		sealedAt, SealerVersion, CanonicalizationSpec)
	if err != nil {
		return false, batchInfo{}, fmt.Errorf("audit: insert checkpoint: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, batchInfo{}, fmt.Errorf("audit: commit checkpoint: %w", err)
	}

	s.log.Info("audit checkpoint sealed",
		"range_start", rangeStart,
		"range_end", rangeEnd,
		"event_count", len(events),
	)
	return true, batchInfo{count: int64(len(events)), firstID: rangeStart, lastID: rangeEnd}, nil
}

// loadEvents fetches up to `limit` events with id > startAfter and
// timestamp < watermark, ordered by id ASC.
func loadEvents(ctx context.Context, conn *pgx.Conn, startAfter int64, watermark time.Time, limit int) ([]CanonicalEvent, error) {
	rows, err := conn.Query(ctx, `
		SELECT id, request_id, timestamp, event_type,
		       organization_id, team_id, user_id,
		       api_key_id::text, ip_address, user_agent,
		       endpoint, method, status_code,
		       error_message, metadata::text
		FROM audit_events
		WHERE id > $1 AND timestamp < $2
		ORDER BY id ASC
		LIMIT $3
	`, startAfter, watermark, limit)
	if err != nil {
		return nil, fmt.Errorf("audit: query events: %w", err)
	}
	defer rows.Close()

	var out []CanonicalEvent
	for rows.Next() {
		var (
			e            CanonicalEvent
			metaText     *string
		)
		if err := rows.Scan(
			&e.ID, &e.RequestID, &e.Timestamp, &e.EventType,
			&e.OrganizationID, &e.TeamID, &e.UserID,
			&e.APIKeyID, &e.IPAddress, &e.UserAgent,
			&e.Endpoint, &e.Method, &e.StatusCode,
			&e.ErrorMessage, &metaText,
		); err != nil {
			return nil, fmt.Errorf("audit: scan event: %w", err)
		}
		if metaText != nil {
			e.Metadata = []byte(*metaText)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
