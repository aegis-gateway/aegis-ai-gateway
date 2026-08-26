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
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"time"

	controlplanev1 "github.com/aegis-gateway/aegis-ai-gateway/api/controlplane/v1"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// VerifyOptions controls the verify-chain run.
type VerifyOptions struct {
	// Full re-hashes retained event rows against stored Merkle roots.
	Full bool
	// FromCheckpoint restricts verification to checkpoints with id >= FromCheckpoint.
	FromCheckpoint int64
	// ToCheckpoint restricts verification to checkpoints with id <= ToCheckpoint.
	ToCheckpoint int64
	// EventID, if > 0, produces a Merkle inclusion proof for that event.
	EventID int64
	// OutputJSON selects JSON output format instead of text.
	OutputJSON bool
}

// CheckpointRecord is one row from audit_checkpoints.
type CheckpointRecord struct {
	ID                   int64
	RangeStart           int64
	RangeEnd             int64
	EventCount           int32
	MerkleRoot           []byte
	PrevCheckpointID     *int64
	PrevCheckpointHash   []byte
	CheckpointHash       []byte
	HashSchemaVersion    int32
	SealedAt             time.Time
	SealerVersion        string
	CanonicalizationSpec string

	// CoveredFrom and CoveredTo are the earliest and latest event timestamp in
	// the covered range. Both are nil for a checkpoint sealed before migration
	// 009 whose events have since been purged, leaving nothing to read them
	// from. They are not hash inputs; the leaf hashes already attest each
	// event's timestamp, so these are an index over what the tree covers.
	CoveredFrom *time.Time
	CoveredTo   *time.Time
}

// VerifyResult summarises the verification outcome.
type VerifyResult struct {
	CheckpointsVerified int
	EventsCovered       int64
	SealedAtRange       [2]time.Time
	UnsealedCount       int64
	Anomalies           []string
}

// InclusionProofResult is the output of --event E.
type InclusionProofResult struct {
	EventID              int64    `json:"event_id"`
	CheckpointID         int64    `json:"checkpoint_id"`
	CheckpointHash       string   `json:"checkpoint_hash"`
	HashSchemaVersion    int32    `json:"hash_schema_version"`
	CanonicalizationSpec string   `json:"canonicalization_spec"`
	LeafIndex            int      `json:"leaf_index"`
	SiblingHashes        []string `json:"sibling_hashes"`
	MerkleRoot           string   `json:"merkle_root"`
}

// RunVerify verifies the checkpoint chain. Returns a VerifyResult or an error.
func RunVerify(ctx context.Context, db *pgxpool.Pool, opts VerifyOptions) (*VerifyResult, *InclusionProofResult, error) {
	conn, err := db.Acquire(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("verify: acquire connection: %w", err)
	}
	defer conn.Release()

	if opts.EventID > 0 {
		proof, err := buildInclusionProof(ctx, conn.Conn(), opts.EventID)
		return nil, proof, err
	}

	result, err := verifyChain(ctx, conn.Conn(), opts)
	return result, nil, err
}

func verifyChain(ctx context.Context, conn *pgx.Conn, opts VerifyOptions) (*VerifyResult, error) {
	checkpoints, err := loadCheckpoints(ctx, conn, opts.FromCheckpoint, opts.ToCheckpoint)
	if err != nil {
		return nil, fmt.Errorf("verify: load checkpoints: %w", err)
	}

	result := &VerifyResult{}
	if len(checkpoints) == 0 {
		// No checkpoints is only healthy if there is nothing to attest. Count
		// the events that exist so an unsealed deployment is reported as
		// unsealed rather than as OK.
		var unsealed int64
		if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM audit_events`).Scan(&unsealed); err != nil {
			return nil, fmt.Errorf("verify: count events with no checkpoints: %w", err)
		}
		result.UnsealedCount = unsealed
		if unsealed > 0 {
			result.Anomalies = append(result.Anomalies,
				fmt.Sprintf("no checkpoints exist, but %d audit event(s) are present — the chain attests nothing", unsealed))
		}
		return result, nil
	}

	result.SealedAtRange[0] = checkpoints[0].SealedAt
	result.SealedAtRange[1] = checkpoints[len(checkpoints)-1].SealedAt

	for i, cp := range checkpoints {
		// Determine expected prev_checkpoint_hash.
		var expectedPrev []byte
		if cp.PrevCheckpointID == nil {
			// Genesis: must be 32 zero bytes.
			expectedPrev = controlplanev1.GenesisPrevHashBytes()
		} else {
			if i == 0 {
				// First in our window but not genesis — load the actual previous.
				var ph []byte
				err := conn.QueryRow(ctx,
					"SELECT checkpoint_hash FROM audit_checkpoints WHERE id = $1",
					*cp.PrevCheckpointID,
				).Scan(&ph)
				if err != nil {
					result.Anomalies = append(result.Anomalies,
						fmt.Sprintf("checkpoint %d: cannot read predecessor %d: %v", cp.ID, *cp.PrevCheckpointID, err))
					continue
				}
				expectedPrev = ph
			} else {
				expectedPrev = checkpoints[i-1].CheckpointHash
			}
		}

		// Verify prev_checkpoint_hash stored in the row matches what we expect.
		if !bytes.Equal(cp.PrevCheckpointHash, expectedPrev) {
			result.Anomalies = append(result.Anomalies,
				fmt.Sprintf("checkpoint %d: prev_checkpoint_hash mismatch (chain break)", cp.ID))
		}

		// Verify the predecessor's *identity*, not just its hash.
		// computeCheckpointHash does not cover prev_checkpoint_id, so an
		// attacker can repoint a checkpoint at an earlier one, leaving every
		// hash valid and the foreign key satisfied, and silently detach the
		// intervening checkpoints from the chain.
		if i > 0 {
			prev := checkpoints[i-1]
			switch {
			case cp.PrevCheckpointID == nil:
				result.Anomalies = append(result.Anomalies,
					fmt.Sprintf("checkpoint %d: claims to be genesis but follows checkpoint %d", cp.ID, prev.ID))
			case *cp.PrevCheckpointID != prev.ID:
				result.Anomalies = append(result.Anomalies,
					fmt.Sprintf("checkpoint %d: prev_checkpoint_id is %d but the preceding checkpoint is %d (chain re-pointed)",
						cp.ID, *cp.PrevCheckpointID, prev.ID))
			}
		}

		// Recompute checkpoint_hash from stored fields.
		computed, err := computeCheckpointHash(
			cp.MerkleRoot, expectedPrev,
			cp.RangeStart, cp.RangeEnd, cp.EventCount,
			cp.HashSchemaVersion, cp.SealedAt,
		)
		if err != nil {
			// A stored digest of the wrong length. The row cannot be verified,
			// and reporting it as an anomaly is the honest outcome: silently
			// skipping it would count the checkpoint as verified.
			result.Anomalies = append(result.Anomalies,
				fmt.Sprintf("checkpoint %d: cannot recompute checkpoint_hash: %v", cp.ID, err))
			continue
		}
		if !bytes.Equal(computed, cp.CheckpointHash) {
			result.Anomalies = append(result.Anomalies,
				fmt.Sprintf("checkpoint %d: checkpoint_hash mismatch (stored=%s, computed=%s)",
					cp.ID, hex.EncodeToString(cp.CheckpointHash), hex.EncodeToString(computed)))
		}

		result.CheckpointsVerified++
		result.EventsCovered += int64(cp.EventCount)
	}

	if opts.Full {
		if err := verifyFull(ctx, conn, checkpoints, result); err != nil {
			return nil, err
		}
	}

	// Count unsealed events.
	lastEnd := int64(0)
	if len(checkpoints) > 0 {
		lastEnd = checkpoints[len(checkpoints)-1].RangeEnd
	}
	err = conn.QueryRow(ctx,
		"SELECT COUNT(*) FROM audit_events WHERE id > $1", lastEnd,
	).Scan(&result.UnsealedCount)
	if err != nil {
		return nil, fmt.Errorf("verify: count unsealed events: %w", err)
	}

	return result, nil
}

// verifyFull re-hashes retained event rows for each checkpoint and compares to the
// stored Merkle root. Ranges covered by audit_purges are reported as attested-but-unverifiable.
func verifyFull(ctx context.Context, conn *pgx.Conn, checkpoints []CheckpointRecord, result *VerifyResult) error {
	// Check if audit_purges table exists.
	purgesExist, err := tableExists(ctx, conn, "audit_purges")
	if err != nil {
		return err
	}

	for _, cp := range checkpoints {
		// Check if this range is covered by a purge.
		if purgesExist {
			var purgeID int64
			err := conn.QueryRow(ctx, `
				SELECT id FROM audit_purges
				WHERE NOT dry_run
				  AND event_id_min <= $1 AND event_id_max >= $2
				LIMIT 1
			`, cp.RangeEnd, cp.RangeStart).Scan(&purgeID)
			if err == nil {
				result.Anomalies = append(result.Anomalies,
					fmt.Sprintf("checkpoint %d: attested-but-unverifiable (range %d–%d purged, see audit_purge event #%d)",
						cp.ID, cp.RangeStart, cp.RangeEnd, purgeID))
				continue
			}
			if err != pgx.ErrNoRows {
				return fmt.Errorf("verify: query audit_purges for checkpoint %d: %w", cp.ID, err)
			}
		}

		// Load and re-hash events.
		rows, err := conn.Query(ctx, `
			SELECT `+EventColumns+`
			FROM audit_events
			WHERE id >= $1 AND id <= $2
			ORDER BY id ASC
		`, cp.RangeStart, cp.RangeEnd)
		if err != nil {
			return fmt.Errorf("verify: query events for checkpoint %d: %w", cp.ID, err)
		}
		events, err := scanEventRows(rows)
		if err != nil {
			return fmt.Errorf("verify: scan events for checkpoint %d: %w", cp.ID, err)
		}

		if int32(len(events)) != cp.EventCount {
			result.Anomalies = append(result.Anomalies,
				fmt.Sprintf("checkpoint %d: expected %d events, found %d in range %d–%d",
					cp.ID, cp.EventCount, len(events), cp.RangeStart, cp.RangeEnd))
			continue
		}

		// This binary can only recompute leaves at hash_schema_version=2, the
		// field set migration 013 established. A version-1 checkpoint cannot be
		// reached from a schema that has run 013, because 013 refuses to run
		// while one exists, so this is a can't-happen. Report it as unverifiable
		// anyway: the failure mode worth designing against is not the impossible
		// state, it is silently hashing the wrong field set and calling the
		// resulting mismatch tampering.
		if cp.HashSchemaVersion != controlplanev1.HashSchemaVersion2 {
			result.Anomalies = append(result.Anomalies,
				fmt.Sprintf("checkpoint %d: sealed at hash_schema_version=%d, which this build cannot recompute (it computes version %d). Not a chain break: this checkpoint is attested but unverifiable here.",
					cp.ID, cp.HashSchemaVersion, controlplanev1.HashSchemaVersion2))
			continue
		}

		leaves := make([][]byte, len(events))
		for i, ev := range events {
			lh, err := EventLeafHash(ev)
			if err != nil {
				return fmt.Errorf("verify: leaf hash event %d: %w", ev.ID, err)
			}
			leaves[i] = lh
		}
		computed := MerkleRoot(leaves)
		if !bytes.Equal(computed, cp.MerkleRoot) {
			result.Anomalies = append(result.Anomalies,
				fmt.Sprintf("checkpoint %d: Merkle root mismatch — event rows have been tampered (range %d–%d)",
					cp.ID, cp.RangeStart, cp.RangeEnd))
		}
	}
	return nil
}

// buildInclusionProof produces an RFC 6962 inclusion proof for a single event.
func buildInclusionProof(ctx context.Context, conn *pgx.Conn, eventID int64) (*InclusionProofResult, error) {
	// Find which checkpoint covers this event.
	var cp CheckpointRecord
	err := conn.QueryRow(ctx, `
		SELECT id, range_start, range_end, event_count, merkle_root,
		       prev_checkpoint_id, prev_checkpoint_hash, checkpoint_hash,
		       hash_schema_version, sealed_at, sealer_version, canonicalization_spec,
		       covered_from, covered_to
		FROM audit_checkpoints
		WHERE range_start <= $1 AND range_end >= $1
		ORDER BY id ASC LIMIT 1
	`, eventID).Scan(
		&cp.ID, &cp.RangeStart, &cp.RangeEnd, &cp.EventCount, &cp.MerkleRoot,
		&cp.PrevCheckpointID, &cp.PrevCheckpointHash, &cp.CheckpointHash,
		&cp.HashSchemaVersion, &cp.SealedAt, &cp.SealerVersion, &cp.CanonicalizationSpec,
		&cp.CoveredFrom, &cp.CoveredTo,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("verify: event %d is not yet sealed into any checkpoint", eventID)
	}
	if err != nil {
		return nil, fmt.Errorf("verify: find checkpoint for event %d: %w", eventID, err)
	}

	// Load all events in the checkpoint range.
	rows, err := conn.Query(ctx, `
		SELECT `+EventColumns+`
		FROM audit_events
		WHERE id >= $1 AND id <= $2
		ORDER BY id ASC
	`, cp.RangeStart, cp.RangeEnd)
	if err != nil {
		return nil, fmt.Errorf("verify: load events for proof: %w", err)
	}
	events, err := scanEventRows(rows)
	if err != nil {
		return nil, fmt.Errorf("verify: scan events for proof: %w", err)
	}

	// Find leaf index.
	leafIdx := -1
	leaves := make([][]byte, len(events))
	for i, ev := range events {
		lh, err := EventLeafHash(ev)
		if err != nil {
			return nil, fmt.Errorf("verify: leaf hash event %d: %w", ev.ID, err)
		}
		leaves[i] = lh
		if ev.ID == eventID {
			leafIdx = i
		}
	}
	if leafIdx < 0 {
		return nil, fmt.Errorf("verify: event %d not found in checkpoint %d range", eventID, cp.ID)
	}

	// The proof is rebuilt from the audit_events rows as they are *now*, while
	// merkle_root below comes from the checkpoint as it was sealed. If a row in
	// the range has since been altered or removed, the rebuilt tree no longer
	// reproduces the stored root — and emitting the proof anyway hands the
	// caller something that cannot verify, from a command that exits 0.
	//
	// Recompute the root and refuse to emit a proof that does not match. This
	// is the single check that makes `verify-chain --event E` meaningful
	// without --full: chain hashes alone say nothing about the leaves.
	if computed := MerkleRoot(leaves); !bytes.Equal(computed, cp.MerkleRoot) {
		return nil, fmt.Errorf(
			"verify: event %d is in checkpoint %d, but the events in range %d–%d no longer "+
				"reproduce the sealed merkle_root (recomputed %x, sealed %x) — the audit rows "+
				"have been altered since sealing; run verify-chain --full to locate the damage",
			eventID, cp.ID, cp.RangeStart, cp.RangeEnd, computed, cp.MerkleRoot)
	}

	siblings := InclusionProof(leaves, leafIdx)
	sibHex := make([]string, len(siblings))
	for i, s := range siblings {
		sibHex[i] = hex.EncodeToString(s)
	}

	return &InclusionProofResult{
		EventID:              eventID,
		CheckpointID:         cp.ID,
		CheckpointHash:       hex.EncodeToString(cp.CheckpointHash),
		HashSchemaVersion:    cp.HashSchemaVersion,
		CanonicalizationSpec: cp.CanonicalizationSpec,
		LeafIndex:            leafIdx,
		SiblingHashes:        sibHex,
		MerkleRoot:           hex.EncodeToString(cp.MerkleRoot),
	}, nil
}

func loadCheckpoints(ctx context.Context, conn *pgx.Conn, fromID, toID int64) ([]CheckpointRecord, error) {
	query := `
		SELECT id, range_start, range_end, event_count, merkle_root,
		       prev_checkpoint_id, prev_checkpoint_hash, checkpoint_hash,
		       hash_schema_version, sealed_at, sealer_version, canonicalization_spec,
		       covered_from, covered_to
		FROM audit_checkpoints
		WHERE ($1 = 0 OR id >= $1)
		  AND ($2 = 0 OR id <= $2)
		ORDER BY id ASC
	`
	rows, err := conn.Query(ctx, query, fromID, toID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CheckpointRecord
	for rows.Next() {
		var cp CheckpointRecord
		if err := rows.Scan(
			&cp.ID, &cp.RangeStart, &cp.RangeEnd, &cp.EventCount, &cp.MerkleRoot,
			&cp.PrevCheckpointID, &cp.PrevCheckpointHash, &cp.CheckpointHash,
			&cp.HashSchemaVersion, &cp.SealedAt, &cp.SealerVersion, &cp.CanonicalizationSpec,
			&cp.CoveredFrom, &cp.CoveredTo,
		); err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}

func tableExists(ctx context.Context, conn *pgx.Conn, table string) (bool, error) {
	var exists bool
	err := conn.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM information_schema.tables
		    WHERE table_schema = 'public' AND table_name = $1
		)
	`, table).Scan(&exists)
	return exists, err
}
