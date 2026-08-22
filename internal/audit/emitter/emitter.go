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

package emitter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	controlplanev1 "github.com/aegis-gateway/aegis-ai-gateway/api/controlplane/v1"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GatewayVersion is the build version reported at registration and with each
// submission. Set via ldflags, as SealerVersion is.
var GatewayVersion = "dev"

// Options controls a submission run.
type Options struct {
	// Endpoint is the control plane base URL.
	Endpoint string

	// Token is the bearer credential. Never stored, never logged.
	Token string

	// GatewayName is the label this deployment registers under. Registration
	// is idempotent on it, so it must be stable across restarts: a changed
	// name registers a second gateway whose chain starts from nothing.
	GatewayName string

	// BatchSize bounds how many checkpoints one run submits. Zero means all
	// outstanding.
	BatchSize int

	// Timeout bounds a single HTTP request.
	Timeout time.Duration
}

// Result summarises a run.
type Result struct {
	Submitted  int
	Duplicates int
	GatewayID  string
	// HighestSubmitted is the checkpoint ID the cursor now sits at.
	HighestSubmitted int64
}

// ErrCoveredRangeUnknown reports a checkpoint whose wall-clock extent cannot be
// determined, so it cannot be submitted under protocol version 1.
//
// This happens only for a checkpoint sealed before migration 009 whose events
// have since been purged: the columns were backfilled from the events, and
// those rows are gone. The checkpoint is still valid and still attests its
// range. What is missing is the interval, which v1 requires.
//
// It is an error rather than a skip because the chain must be submitted without
// holes. Skipping one would make the next submission a gap, reported by the
// control plane as missing evidence, which is a worse and more confusing
// outcome than stopping here and naming the checkpoint.
var ErrCoveredRangeUnknown = errors.New("checkpoint has no recorded covered time range")

// Run registers the gateway if needed and submits every checkpoint the control
// plane has not yet acknowledged.
func Run(ctx context.Context, db *pgxpool.Pool, opts Options) (*Result, error) {
	client, err := NewClient(opts.Endpoint, opts.Token, opts.Timeout)
	if err != nil {
		return nil, err
	}

	state, err := ensureRegistered(ctx, db, client, opts)
	if err != nil {
		return nil, err
	}

	result := &Result{GatewayID: state.GatewayID, HighestSubmitted: state.LastSubmitted}

	for {
		rows, err := loadCheckpointsAfter(ctx, db, state.GatewayID, result.HighestSubmitted, batchLimit(opts.BatchSize))
		if err != nil {
			return result, err
		}
		if len(rows) == 0 {
			slog.Info("control plane: all sealed checkpoints submitted",
				"gateway_id", state.GatewayID,
				"last_submitted_checkpoint", result.HighestSubmitted)
			return result, nil
		}

		for i := range rows {
			sub := &rows[i]
			resp, err := client.SubmitCheckpoint(ctx, sub)
			if err != nil {
				return result, describeSubmitFailure(sub.CheckpointID, err)
			}

			if resp.Duplicate {
				result.Duplicates++
			} else {
				result.Submitted++
			}
			result.HighestSubmitted = sub.CheckpointID

			// The cursor advances per checkpoint, not per batch. An
			// interrupted run then resumes from the last checkpoint the
			// control plane actually acknowledged rather than replaying a
			// whole batch.
			if err := advanceCursor(ctx, db, sub.CheckpointID); err != nil {
				return result, err
			}
		}

		if opts.BatchSize > 0 && result.Submitted+result.Duplicates >= opts.BatchSize {
			slog.Info("control plane: batch limit reached, more checkpoints remain",
				"submitted", result.Submitted,
				"duplicates", result.Duplicates,
				"last_submitted_checkpoint", result.HighestSubmitted)
			return result, nil
		}
	}
}

func batchLimit(batchSize int) int {
	if batchSize > 0 {
		return batchSize
	}
	// A page size, not a limit on the run: the loop continues until the
	// gateway is caught up. It exists so a gateway with a long history does
	// not build every outstanding submission in memory at once.
	return 500
}

// describeSubmitFailure turns a transport or protocol failure into a message an
// operator can act on.
func describeSubmitFailure(checkpointID int64, err error) error {
	var remote *RemoteError
	if errors.As(err, &remote) && remote.IsChainDiscontinuity() {
		// Retrying cannot fix this, so say so rather than leaving an operator
		// to conclude it from repeated identical failures.
		slog.Error("control plane: the chain does not join up; submission stopped",
			"checkpoint_id", checkpointID,
			"code", string(remote.Body.Code),
			"message", remote.Body.Message)
		return fmt.Errorf(
			"submitting checkpoint %d: %w; this is a disagreement about what has been "+
				"recorded and will not resolve by retrying",
			checkpointID, err)
	}
	return fmt.Errorf("submitting checkpoint %d: %w", checkpointID, err)
}

// state is the row of control_plane_state.
type state struct {
	Endpoint      string
	GatewayID     string
	GatewayName   string
	LastSubmitted int64
}

// ensureRegistered reads the stored identity, registering if there is none.
func ensureRegistered(ctx context.Context, db *pgxpool.Pool, client *Client, opts Options) (*state, error) {
	var s state
	err := db.QueryRow(ctx, `
		SELECT endpoint, gateway_id::text, gateway_name, last_submitted_checkpoint
		FROM control_plane_state
		WHERE singleton
	`).Scan(&s.Endpoint, &s.GatewayID, &s.GatewayName, &s.LastSubmitted)

	switch {
	case err == nil:
		// Registered already. Refuse to replay a cursor built against one
		// control plane at a different one: the identity was issued by the
		// first, and the second never saw the checkpoints the cursor claims
		// were accepted.
		if s.Endpoint != client.endpoint {
			return nil, fmt.Errorf(
				"this gateway is registered with the control plane at %s and has submitted up to "+
					"checkpoint %d, but was pointed at %s; the stored identity is not valid there. "+
					"Clear control_plane_state to register afresh, which submits the whole chain "+
					"from its genesis",
				s.Endpoint, s.LastSubmitted, client.endpoint)
		}
		if s.GatewayName != opts.GatewayName && opts.GatewayName != "" {
			return nil, fmt.Errorf(
				"this gateway registered as %q but was given the name %q; registration is "+
					"idempotent on the name, so a changed name registers a second gateway whose "+
					"chain starts from nothing",
				s.GatewayName, opts.GatewayName)
		}
		return &s, nil

	case errors.Is(err, pgx.ErrNoRows):
		if opts.GatewayName == "" {
			return nil, errors.New("a gateway name is required for the first registration")
		}
		resp, err := client.RegisterGateway(ctx, opts.GatewayName, GatewayVersion)
		if err != nil {
			return nil, fmt.Errorf("registering with the control plane: %w", err)
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO control_plane_state
			    (singleton, endpoint, gateway_id, gateway_name, last_submitted_checkpoint, registered_at)
			VALUES (TRUE, $1, $2::uuid, $3, 0, $4)
		`, client.endpoint, resp.GatewayID, resp.Name, resp.RegisteredAt.Time); err != nil {
			return nil, fmt.Errorf("recording the control plane registration: %w", err)
		}
		slog.Info("control plane: gateway registered",
			"endpoint", client.endpoint, "gateway_id", resp.GatewayID, "name", resp.Name)
		return &state{
			Endpoint:    client.endpoint,
			GatewayID:   resp.GatewayID,
			GatewayName: resp.Name,
		}, nil

	default:
		return nil, fmt.Errorf("reading control_plane_state: %w", err)
	}
}

// advanceCursor records that a checkpoint was accepted.
func advanceCursor(ctx context.Context, db *pgxpool.Pool, checkpointID int64) error {
	_, err := db.Exec(ctx, `
		UPDATE control_plane_state
		SET last_submitted_checkpoint = $1, last_submitted_at = NOW(), updated_at = NOW()
		WHERE singleton
	`, checkpointID)
	if err != nil {
		return fmt.Errorf("advancing the submission cursor to checkpoint %d: %w", checkpointID, err)
	}
	return nil
}

// loadCheckpointsAfter reads sealed checkpoints past the cursor and converts
// them into submissions.
func loadCheckpointsAfter(ctx context.Context, db *pgxpool.Pool, gatewayID string, after int64, limit int) ([]controlplanev1.CheckpointSubmission, error) {
	rows, err := db.Query(ctx, `
		SELECT id, range_start, range_end, event_count, merkle_root,
		       prev_checkpoint_id, prev_checkpoint_hash, checkpoint_hash,
		       hash_schema_version, canonicalization_spec, sealed_at, sealer_version,
		       covered_from, covered_to
		FROM audit_checkpoints
		WHERE id > $1
		ORDER BY id ASC
		LIMIT $2
	`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("reading checkpoints after %d: %w", after, err)
	}
	defer rows.Close()

	var out []controlplanev1.CheckpointSubmission
	for rows.Next() {
		var (
			id, rangeStart, rangeEnd      int64
			eventCount, hashSchemaVersion int32
			merkleRoot, prevHash, cpHash  []byte
			prevCheckpointID              *int64
			canonSpec, sealerVersion      string
			sealedAt                      time.Time
			coveredFrom, coveredTo        *time.Time
		)
		if err := rows.Scan(
			&id, &rangeStart, &rangeEnd, &eventCount, &merkleRoot,
			&prevCheckpointID, &prevHash, &cpHash,
			&hashSchemaVersion, &canonSpec, &sealedAt, &sealerVersion,
			&coveredFrom, &coveredTo,
		); err != nil {
			return nil, fmt.Errorf("scanning a checkpoint: %w", err)
		}

		if coveredFrom == nil || coveredTo == nil {
			return nil, fmt.Errorf(
				"%w: checkpoint %d covers events %d to %d, which were purged before migration 009 "+
					"backfilled the interval, so it cannot be submitted under protocol version 1",
				ErrCoveredRangeUnknown, id, rangeStart, rangeEnd)
		}

		out = append(out, controlplanev1.CheckpointSubmission{
			GatewayID:            gatewayID,
			CheckpointID:         id,
			RangeStart:           rangeStart,
			RangeEnd:             rangeEnd,
			EventCount:           eventCount,
			MerkleRoot:           controlplanev1.NewHashHex(merkleRoot),
			PrevCheckpointID:     prevCheckpointID,
			PrevCheckpointHash:   controlplanev1.NewHashHex(prevHash),
			CheckpointHash:       controlplanev1.NewHashHex(cpHash),
			HashAlgorithm:        controlplanev1.HashAlgorithmSHA256,
			HashSchemaVersion:    hashSchemaVersion,
			CanonicalizationSpec: controlplanev1.CanonicalizationSpec(canonSpec),
			SealedAt:             controlplanev1.NewTimestamp(sealedAt),
			SealerVersion:        sealerVersion,
			GatewayVersion:       GatewayVersion,
			CoveredFrom:          controlplanev1.NewTimestamp(*coveredFrom),
			CoveredTo:            controlplanev1.NewTimestamp(*coveredTo),
		})
	}
	return out, rows.Err()
}
