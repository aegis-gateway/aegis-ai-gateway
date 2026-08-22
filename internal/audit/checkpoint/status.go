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
	"errors"
	"fmt"
	"time"

	controlplanev1 "github.com/aegis-gateway/aegis-ai-gateway/api/controlplane/v1"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SealStatus describes why this gateway's chain is or is not advancing.
type SealStatus struct {
	State              controlplanev1.SealState
	LastCheckpointID   *int64
	LastSealedAt       *time.Time
	UnsealedEventCount int64

	// GapAfterEventID and NextVisibleEventID are set only when sealing is
	// paused at a gap.
	GapAfterEventID    *int64
	NextVisibleEventID *int64
}

// ReadSealStatus reports the current sealing state.
//
// It exists because a gateway that has stopped sealing and a gateway that has
// stopped talking look identical from outside. The first is a known state with
// a named cause and a countable set of unattested events; the second is
// unknown. Anything assembling evidence has to be able to tell them apart, and
// only the gateway can say which it is.
//
// This is a read-only view. It takes no advisory lock and changes nothing, so
// it is safe to call while a sealer is running; what it returns is a snapshot
// that may be stale by the time it is read, which is the nature of the thing
// being reported.
func ReadSealStatus(ctx context.Context, db *pgxpool.Pool) (*SealStatus, error) {
	status := &SealStatus{}

	var lastCheckpointID, lastRangeEnd int64
	var lastSealedAt time.Time
	err := db.QueryRow(ctx, `
		SELECT id, range_end, sealed_at
		FROM audit_checkpoints
		ORDER BY id DESC
		LIMIT 1
	`).Scan(&lastCheckpointID, &lastRangeEnd, &lastSealedAt)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// No checkpoints. Whether that is healthy depends entirely on whether
		// there is anything to attest.
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_events`).
			Scan(&status.UnsealedEventCount); err != nil {
			return nil, fmt.Errorf("counting events with no checkpoints: %w", err)
		}
		if status.UnsealedEventCount == 0 {
			status.State = controlplanev1.SealStateEmpty
		} else {
			status.State = controlplanev1.SealStateNeverSealed
		}
		return status, nil

	case err != nil:
		return nil, fmt.Errorf("reading the last checkpoint: %w", err)
	}

	status.LastCheckpointID = &lastCheckpointID
	status.LastSealedAt = &lastSealedAt

	if err := db.QueryRow(ctx,
		`SELECT COUNT(*) FROM audit_events WHERE id > $1`, lastRangeEnd,
	).Scan(&status.UnsealedEventCount); err != nil {
		return nil, fmt.Errorf("counting unsealed events: %w", err)
	}

	if status.UnsealedEventCount == 0 {
		status.State = controlplanev1.SealStateCurrent
		return status, nil
	}

	// Events remain. The question is whether the next one continues the run or
	// sits beyond a hole, because the sealer refuses to seal past a hole and
	// will therefore make no further progress until it is resolved.
	var nextVisible int64
	if err := db.QueryRow(ctx,
		`SELECT MIN(id) FROM audit_events WHERE id > $1`, lastRangeEnd,
	).Scan(&nextVisible); err != nil {
		return nil, fmt.Errorf("finding the next unsealed event: %w", err)
	}

	if nextVisible == lastRangeEnd+1 {
		// Contiguous. These events are simply not sealed yet, which is the
		// ordinary state between sealer runs and inside the lag window.
		status.State = controlplanev1.SealStateCurrent
		return status, nil
	}

	status.State = controlplanev1.SealStatePausedAtGap
	status.GapAfterEventID = &lastRangeEnd
	status.NextVisibleEventID = &nextVisible
	return status, nil
}

// ToReport converts a status into the wire message.
func (s *SealStatus) ToReport(gatewayID, gatewayVersion string, now time.Time) *controlplanev1.GatewayStatusReport {
	report := &controlplanev1.GatewayStatusReport{
		GatewayID:          gatewayID,
		ReportedAt:         controlplanev1.NewTimestamp(now),
		SealState:          s.State,
		LastCheckpointID:   s.LastCheckpointID,
		UnsealedEventCount: s.UnsealedEventCount,
		GapAfterEventID:    s.GapAfterEventID,
		NextVisibleEventID: s.NextVisibleEventID,
		GatewayVersion:     gatewayVersion,
	}
	if s.LastSealedAt != nil {
		ts := controlplanev1.NewTimestamp(*s.LastSealedAt)
		report.LastSealedAt = &ts
	}
	return report
}
