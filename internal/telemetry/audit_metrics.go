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

package telemetry

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// StartAuditMetricsRefresh updates audit integrity gauges on startup and every 5 minutes.
// It runs until ctx is cancelled. Errors are logged but do not stop the loop.
func (m *Metrics) StartAuditMetricsRefresh(ctx context.Context, db *pgxpool.Pool) {
	m.refreshAuditMetrics(ctx, db)

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.refreshAuditMetrics(ctx, db)
		}
	}
}

func (m *Metrics) refreshAuditMetrics(ctx context.Context, db *pgxpool.Pool) {
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var lastSealedAt *time.Time
	var lastRangeEnd int64

	err := db.QueryRow(rctx, `
		SELECT sealed_at, range_end
		FROM audit_checkpoints
		ORDER BY id DESC LIMIT 1
	`).Scan(&lastSealedAt, &lastRangeEnd)
	if err != nil && err.Error() != "no rows in result set" {
		// Table may not exist yet (pre-migration). Silently skip.
		slog.Debug("audit metrics: no checkpoints found", "err", err)
		return
	}

	if lastSealedAt != nil {
		age := time.Since(*lastSealedAt).Seconds()
		m.AuditLastSealAgeSeconds.Set(age)
	}

	var unsealedCount int64
	if err := db.QueryRow(rctx, `
		SELECT COUNT(*) FROM audit_events WHERE id > $1
	`, lastRangeEnd).Scan(&unsealedCount); err != nil {
		slog.Debug("audit metrics: count unsealed events", "err", err)
		return
	}
	m.AuditUnsealedEvents.Set(float64(unsealedCount))
}
