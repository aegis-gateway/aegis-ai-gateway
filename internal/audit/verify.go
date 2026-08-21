// Copyright 2026 Atlantic Frontier Corporations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package audit

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Checkpoint is a decoded row from audit_checkpoints, used for verification.
type Checkpoint struct {
	ID                   int64
	RangeStart           int64
	RangeEnd             int64
	EventCount           int
	MerkleRoot           []byte
	PrevCheckpointID     *int64
	PrevCheckpointHash   []byte
	CheckpointHash       []byte
	HashSchemaVersion    int
	SealedAt             time.Time
	SealerVersion        string
	CanonicalizationSpec string
}

// VerifyOptions controls the verify-chain command.
type VerifyOptions struct {
	Full           bool
	FromCheckpoint int64
	ToCheckpoint   int64
	EventID        int64 // 0 = no inclusion proof
}

// RangeStatus is emitted per checkpoint when running --full.
type RangeStatus struct {
	CheckpointID int64  `json:"checkpoint_id"`
	Status       string `json:"status"` // "ok" | "purged" | "chain_break" | "merkle_mismatch"
	Detail       string `json:"detail,omitempty"`
}

// VerifyResult summarizes verify-chain output.
type VerifyResult struct {
	CheckpointsExamined int           `json:"checkpoints_examined"`
	ChainOK             bool          `json:"chain_ok"`
	FullOK              *bool         `json:"full_ok,omitempty"`
	Ranges              []RangeStatus `json:"ranges,omitempty"`
	Proof               *EventProof   `json:"proof,omitempty"`
	FailedAt            *int64        `json:"failed_at_checkpoint,omitempty"`
	Detail              string        `json:"detail,omitempty"`
}

// EventProof is the RFC 6962 inclusion proof for a single event.
type EventProof struct {
	EventID              int64    `json:"event_id"`
	CheckpointID         int64    `json:"checkpoint_id"`
	CheckpointHash       string   `json:"checkpoint_hash"`
	MerkleRoot           string   `json:"merkle_root"`
	LeafHash             string   `json:"leaf_hash"`
	Siblings             []string `json:"siblings"`
	Directions           []string `json:"directions"` // "L"/"R" — sibling side
	HashSchemaVersion    int      `json:"hash_schema_version"`
	CanonicalizationSpec string   `json:"canonicalization_spec"`
}

// Verify walks the checkpoint chain and reports on integrity.
func Verify(ctx context.Context, db *pgxpool.Pool, opts VerifyOptions) (*VerifyResult, error) {
	checkpoints, err := loadCheckpoints(ctx, db, opts.FromCheckpoint, opts.ToCheckpoint)
	if err != nil {
		return nil, err
	}
	res := &VerifyResult{ChainOK: true, CheckpointsExamined: len(checkpoints)}
	if len(checkpoints) == 0 {
		res.Detail = "no checkpoints found in range"
		return res, nil
	}

	// Detect duplicate/overlapping ranges within the batch.
	for i := 1; i < len(checkpoints); i++ {
		if checkpoints[i].RangeStart <= checkpoints[i-1].RangeEnd {
			res.ChainOK = false
			id := checkpoints[i].ID
			res.FailedAt = &id
			res.Detail = fmt.Sprintf("range overlap: checkpoint %d starts at %d but %d ended at %d",
				checkpoints[i].ID, checkpoints[i].RangeStart,
				checkpoints[i-1].ID, checkpoints[i-1].RangeEnd)
			return res, nil
		}
	}

	// Chain fast-path: recompute checkpoint_hash and verify prev linkage.
	var prevHash []byte
	firstIsGenesis := opts.FromCheckpoint == 0 || checkpoints[0].PrevCheckpointID == nil
	for i, cp := range checkpoints {
		expectedPrev := GenesisPrevHash
		if i == 0 {
			if !firstIsGenesis {
				// verify against actually-stored prev_checkpoint_hash — we
				// can't recompute a hash from outside this window.
				expectedPrev = cp.PrevCheckpointHash
			}
		} else {
			expectedPrev = prevHash
		}
		if cp.PrevCheckpointHash != nil && !bytes.Equal(cp.PrevCheckpointHash, expectedPrev) {
			res.ChainOK = false
			id := cp.ID
			res.FailedAt = &id
			res.Detail = "prev_checkpoint_hash does not match previous checkpoint_hash"
			return res, nil
		}
		if cp.PrevCheckpointHash == nil && cp.PrevCheckpointID != nil {
			res.ChainOK = false
			id := cp.ID
			res.FailedAt = &id
			res.Detail = "prev_checkpoint_hash is NULL but prev_checkpoint_id is set"
			return res, nil
		}
		hashPrev := cp.PrevCheckpointHash
		if hashPrev == nil {
			hashPrev = GenesisPrevHash
		}
		input := CheckpointInput{
			MerkleRoot:        cp.MerkleRoot,
			PrevHash:          hashPrev,
			RangeStart:        uint64(cp.RangeStart),
			RangeEnd:          uint64(cp.RangeEnd),
			EventCount:        uint32(cp.EventCount),
			HashSchemaVersion: uint32(cp.HashSchemaVersion),
			SealedAt:          cp.SealedAt,
		}
		want, err := input.Hash()
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(want, cp.CheckpointHash) {
			res.ChainOK = false
			id := cp.ID
			res.FailedAt = &id
			res.Detail = "checkpoint_hash mismatch: stored fields do not match stored hash"
			return res, nil
		}
		prevHash = cp.CheckpointHash
	}

	// Optional inclusion proof.
	if opts.EventID > 0 {
		proof, err := buildInclusionProof(ctx, db, opts.EventID)
		if err != nil {
			return nil, err
		}
		res.Proof = proof
	}

	// Full mode: re-hash retained events, rebuild Merkle roots.
	if opts.Full {
		purgesExists, err := hasPurgesTable(ctx, db)
		if err != nil {
			return nil, err
		}
		fullOK := true
		for _, cp := range checkpoints {
			status := RangeStatus{CheckpointID: cp.ID, Status: "ok"}
			events, err := loadEventRange(ctx, db, cp.RangeStart, cp.RangeEnd)
			if err != nil {
				return nil, err
			}
			if len(events) != cp.EventCount {
				if purgesExists {
					covered, cerr := rangeCoveredByPurge(ctx, db, cp.RangeStart, cp.RangeEnd)
					if cerr != nil {
						return nil, cerr
					}
					if covered {
						status.Status = "purged"
						status.Detail = "range overlaps audit_purges; attested-but-unverifiable"
						res.Ranges = append(res.Ranges, status)
						continue
					}
				}
				status.Status = "merkle_mismatch"
				status.Detail = fmt.Sprintf("expected %d events, found %d", cp.EventCount, len(events))
				fullOK = false
				res.Ranges = append(res.Ranges, status)
				continue
			}
			leaves := make([][]byte, len(events))
			for i := range events {
				h, err := events[i].LeafHash()
				if err != nil {
					return nil, err
				}
				leaves[i] = h
			}
			root, err := MerkleRoot(leaves)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(root, cp.MerkleRoot) {
				status.Status = "merkle_mismatch"
				status.Detail = "recomputed Merkle root does not match stored root"
				fullOK = false
			}
			res.Ranges = append(res.Ranges, status)
		}
		res.FullOK = &fullOK
	}

	return res, nil
}

func loadCheckpoints(ctx context.Context, db *pgxpool.Pool, from, to int64) ([]Checkpoint, error) {
	q := `
		SELECT id, range_start, range_end, event_count, merkle_root,
		       prev_checkpoint_id, prev_checkpoint_hash, checkpoint_hash,
		       hash_schema_version, sealed_at, sealer_version,
		       canonicalization_spec
		FROM audit_checkpoints
		WHERE ($1 = 0 OR id >= $1) AND ($2 = 0 OR id <= $2)
		ORDER BY id ASC
	`
	rows, err := db.Query(ctx, q, from, to)
	if err != nil {
		return nil, fmt.Errorf("audit: query checkpoints: %w", err)
	}
	defer rows.Close()
	var out []Checkpoint
	for rows.Next() {
		var cp Checkpoint
		if err := rows.Scan(&cp.ID, &cp.RangeStart, &cp.RangeEnd, &cp.EventCount,
			&cp.MerkleRoot, &cp.PrevCheckpointID, &cp.PrevCheckpointHash,
			&cp.CheckpointHash, &cp.HashSchemaVersion, &cp.SealedAt,
			&cp.SealerVersion, &cp.CanonicalizationSpec); err != nil {
			return nil, fmt.Errorf("audit: scan checkpoint: %w", err)
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}

func loadEventRange(ctx context.Context, db *pgxpool.Pool, start, end int64) ([]CanonicalEvent, error) {
	rows, err := db.Query(ctx, `
		SELECT id, request_id, timestamp, event_type,
		       organization_id, team_id, user_id,
		       api_key_id::text, ip_address, user_agent,
		       endpoint, method, status_code,
		       error_message, metadata::text
		FROM audit_events
		WHERE id BETWEEN $1 AND $2
		ORDER BY id ASC
	`, start, end)
	if err != nil {
		return nil, fmt.Errorf("audit: query event range: %w", err)
	}
	defer rows.Close()
	var out []CanonicalEvent
	for rows.Next() {
		var (
			e        CanonicalEvent
			metaText *string
		)
		if err := rows.Scan(&e.ID, &e.RequestID, &e.Timestamp, &e.EventType,
			&e.OrganizationID, &e.TeamID, &e.UserID, &e.APIKeyID,
			&e.IPAddress, &e.UserAgent, &e.Endpoint, &e.Method,
			&e.StatusCode, &e.ErrorMessage, &metaText); err != nil {
			return nil, err
		}
		if metaText != nil {
			e.Metadata = []byte(*metaText)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func hasPurgesTable(ctx context.Context, db *pgxpool.Pool) (bool, error) {
	var exists bool
	err := db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_name = 'audit_purges'
		)
	`).Scan(&exists)
	return exists, err
}

func rangeCoveredByPurge(ctx context.Context, db *pgxpool.Pool, start, end int64) (bool, error) {
	var covered bool
	err := db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM audit_purges
			WHERE range_start <= $1 AND range_end >= $2
		)
	`, start, end).Scan(&covered)
	return covered, err
}

func buildInclusionProof(ctx context.Context, db *pgxpool.Pool, eventID int64) (*EventProof, error) {
	var cp Checkpoint
	err := db.QueryRow(ctx, `
		SELECT id, range_start, range_end, event_count, merkle_root,
		       prev_checkpoint_id, prev_checkpoint_hash, checkpoint_hash,
		       hash_schema_version, sealed_at, sealer_version,
		       canonicalization_spec
		FROM audit_checkpoints
		WHERE range_start <= $1 AND range_end >= $1
		ORDER BY id ASC
		LIMIT 1
	`, eventID).Scan(&cp.ID, &cp.RangeStart, &cp.RangeEnd, &cp.EventCount,
		&cp.MerkleRoot, &cp.PrevCheckpointID, &cp.PrevCheckpointHash,
		&cp.CheckpointHash, &cp.HashSchemaVersion, &cp.SealedAt,
		&cp.SealerVersion, &cp.CanonicalizationSpec)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("audit: no checkpoint contains event %d", eventID)
		}
		return nil, err
	}
	events, err := loadEventRange(ctx, db, cp.RangeStart, cp.RangeEnd)
	if err != nil {
		return nil, err
	}
	leaves := make([][]byte, len(events))
	idx := -1
	for i := range events {
		if events[i].ID == eventID {
			idx = i
		}
		h, err := events[i].LeafHash()
		if err != nil {
			return nil, err
		}
		leaves[i] = h
	}
	if idx < 0 {
		return nil, fmt.Errorf("audit: event %d not found in range [%d,%d]", eventID, cp.RangeStart, cp.RangeEnd)
	}
	steps, err := InclusionProof(leaves, idx)
	if err != nil {
		return nil, err
	}
	siblings := make([]string, len(steps))
	directions := make([]string, len(steps))
	for i, s := range steps {
		siblings[i] = hex.EncodeToString(s.Sibling)
		if s.IsLeft {
			directions[i] = "L"
		} else {
			directions[i] = "R"
		}
	}
	return &EventProof{
		EventID:              eventID,
		CheckpointID:         cp.ID,
		CheckpointHash:       hex.EncodeToString(cp.CheckpointHash),
		MerkleRoot:           hex.EncodeToString(cp.MerkleRoot),
		LeafHash:             hex.EncodeToString(leaves[idx]),
		Siblings:             siblings,
		Directions:           directions,
		HashSchemaVersion:    cp.HashSchemaVersion,
		CanonicalizationSpec: cp.CanonicalizationSpec,
	}, nil
}

// UnsealedEventCount returns the count of events strictly beyond the last
// sealed range. Used by the gateway to publish the audit metrics.
func UnsealedEventCount(ctx context.Context, db *pgxpool.Pool) (int64, error) {
	var lastEnd int64
	err := db.QueryRow(ctx, `SELECT COALESCE(MAX(range_end), 0) FROM audit_checkpoints`).Scan(&lastEnd)
	if err != nil {
		return 0, err
	}
	var count int64
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_events WHERE id > $1`, lastEnd).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// LastSealAge returns the duration since the most recent checkpoint was
// sealed. Returns (0, false, nil) if no checkpoints exist.
func LastSealAge(ctx context.Context, db *pgxpool.Pool, now time.Time) (time.Duration, bool, error) {
	var sealedAt time.Time
	err := db.QueryRow(ctx, `SELECT sealed_at FROM audit_checkpoints ORDER BY id DESC LIMIT 1`).Scan(&sealedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return now.Sub(sealedAt), true, nil
}
