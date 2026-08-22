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

	// LastSealedEventID is the highest event ID covered by a checkpoint, zero
	// when none has been sealed.
	LastSealedEventID int64

	// FirstUnsealedEventID is the lowest event ID beyond the last checkpoint,
	// nil when nothing is unsealed.
	FirstUnsealedEventID *int64

	// GapAge is how long a gap has existed, nil when there is no gap.
	GapAge *time.Duration

	// LagSeconds is the window this status was judged against.
	LagSeconds int64
}

// ReadSealStatus reports the current sealing state, judged against the same
// lag window the sealer uses.
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
func ReadSealStatus(ctx context.Context, db *pgxpool.Pool, lagSeconds int64) (*SealStatus, error) {
	if lagSeconds < 0 {
		lagSeconds = 0
	}
	status := &SealStatus{LagSeconds: lagSeconds}

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
			return status, nil
		}

		// Nothing sealed, so the whole table is unattested. LastSealedEventID
		// stays zero and the first unsealed event is simply the lowest ID
		// present. Reported here as in every other state: a consumer asking
		// how far behind a gateway is should not have to special-case the one
		// that has not started.
		status.State = controlplanev1.SealStateNeverSealed
		var firstUnsealed int64
		if err := db.QueryRow(ctx, `SELECT MIN(id) FROM audit_events`).Scan(&firstUnsealed); err != nil {
			return nil, fmt.Errorf("finding the first unsealed event: %w", err)
		}
		status.FirstUnsealedEventID = &firstUnsealed
		return status, nil

	case err != nil:
		return nil, fmt.Errorf("reading the last checkpoint: %w", err)
	}

	status.LastCheckpointID = &lastCheckpointID
	status.LastSealedAt = &lastSealedAt
	status.LastSealedEventID = lastRangeEnd

	if err := db.QueryRow(ctx,
		`SELECT COUNT(*) FROM audit_events WHERE id > $1`, lastRangeEnd,
	).Scan(&status.UnsealedEventCount); err != nil {
		return nil, fmt.Errorf("counting unsealed events: %w", err)
	}

	if status.UnsealedEventCount == 0 {
		status.State = controlplanev1.SealStateAdvancing
		return status, nil
	}

	// Events remain. Find the first one and when it was written: the sealer
	// refuses to seal past a hole, so whether the next event continues the run
	// decides whether progress is possible at all.
	var nextVisible int64
	var nextVisibleAt time.Time
	if err := db.QueryRow(ctx, `
		SELECT id, "timestamp"
		FROM audit_events
		WHERE id > $1
		ORDER BY id ASC
		LIMIT 1
	`, lastRangeEnd).Scan(&nextVisible, &nextVisibleAt); err != nil {
		return nil, fmt.Errorf("finding the next unsealed event: %w", err)
	}
	status.FirstUnsealedEventID = &nextVisible

	if nextVisible == lastRangeEnd+1 {
		// Contiguous: not sealed yet, which is the ordinary state between
		// sealer runs and inside the lag window.
		status.State = controlplanev1.SealStateAdvancing
		return status, nil
	}

	// A gap. Its age is measured from the first event beyond it, because that
	// event's ID was allocated after the missing ones, so the gap has existed
	// at least since that row was written. That is a lower bound and it is
	// objective, which a bound derived from the sealer's own runs would not be.
	gapAge := time.Since(nextVisibleAt.UTC())
	if gapAge < 0 {
		gapAge = 0
	}
	status.GapAge = &gapAge

	// The sealer only considers events older than now minus the lag window, so
	// a gap whose far side is still inside that window has not been attempted
	// yet and may fill on its own. Reporting it as paused would be a false
	// positive, and a signal that cries wolf gets ignored.
	if gapAge < time.Duration(lagSeconds)*time.Second {
		status.State = controlplanev1.SealStateWaitingOnGap
		return status, nil
	}

	status.State = controlplanev1.SealStatePausedAtGap
	return status, nil
}

// ToReport converts a status into the wire message.
func (s *SealStatus) ToReport(gatewayID, gatewayVersion string, now time.Time) *controlplanev1.GatewayStatusReport {
	report := &controlplanev1.GatewayStatusReport{
		GatewayID:            gatewayID,
		ReportedAt:           controlplanev1.NewTimestamp(now),
		SealState:            s.State,
		LastCheckpointID:     s.LastCheckpointID,
		UnsealedEventCount:   s.UnsealedEventCount,
		LastSealedEventID:    s.LastSealedEventID,
		FirstUnsealedEventID: s.FirstUnsealedEventID,
		SealLagSeconds:       s.LagSeconds,
		GatewayVersion:       gatewayVersion,
	}
	if s.GapAge != nil {
		seconds := int64(s.GapAge.Seconds())
		report.GapAgeSeconds = &seconds
	}
	if s.LastSealedAt != nil {
		ts := controlplanev1.NewTimestamp(*s.LastSealedAt)
		report.LastSealedAt = &ts
	}
	return report
}
