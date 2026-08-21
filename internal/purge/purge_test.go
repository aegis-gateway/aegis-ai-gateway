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

package purge_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/purge"
	"github.com/jackc/pgx/v5/pgxpool"
)

// dbURL returns the TEST_DATABASE_URL or skips the test.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// setupSchema creates the tables required for purge tests and tears them down.
func setupSchema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS audit_events (
			id         BIGSERIAL PRIMARY KEY,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			event_type TEXT NOT NULL DEFAULT 'test',
			org_id     TEXT NOT NULL DEFAULT '',
			team_id    TEXT NOT NULL DEFAULT '',
			status_code INT NOT NULL DEFAULT 0
		)`)
	if err != nil {
		t.Fatalf("create audit_events: %v", err)
	}

	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS audit_logs (
			id         BIGSERIAL PRIMARY KEY,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			request_id TEXT NOT NULL DEFAULT ''
		)`)
	if err != nil {
		t.Fatalf("create audit_logs: %v", err)
	}

	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS audit_purges (
			id                       BIGSERIAL PRIMARY KEY,
			purged_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			window_start             TIMESTAMPTZ NOT NULL,
			window_end               TIMESTAMPTZ NOT NULL,
			event_id_min             BIGINT NOT NULL,
			event_id_max             BIGINT NOT NULL,
			rows_deleted             INTEGER NOT NULL,
			affected_checkpoint_ids  BIGINT[] NOT NULL,
			dry_run                  BOOLEAN NOT NULL DEFAULT FALSE
		)`)
	if err != nil {
		t.Fatalf("create audit_purges: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		pool.Exec(cleanupCtx, `DROP TABLE IF EXISTS audit_purges`)   //nolint:errcheck
		pool.Exec(cleanupCtx, `DROP TABLE IF EXISTS audit_events`)   //nolint:errcheck
		pool.Exec(cleanupCtx, `DROP TABLE IF EXISTS audit_logs`)     //nolint:errcheck
	})
}

// insertOldEvent inserts an audit_events row with a created_at in the past.
func insertOldEvent(t *testing.T, pool *pgxpool.Pool, age time.Duration) int64 {
	t.Helper()
	createdAt := time.Now().UTC().Add(-age)
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO audit_events (created_at) VALUES ($1) RETURNING id`,
		createdAt,
	).Scan(&id); err != nil {
		t.Fatalf("insert audit_events: %v", err)
	}
	return id
}

// TestDryRunProducesCorrectCount verifies that dry-run mode queries counts without
// deleting and writes a dry_run=true row to audit_purges.
func TestDryRunProducesCorrectCount(t *testing.T) {
	pool := testPool(t)
	setupSchema(t, pool)
	ctx := context.Background()

	// Insert 3 old events (> 400 days) and 1 recent event.
	cutoff := time.Now().UTC().Add(-365 * 24 * time.Hour)
	insertOldEvent(t, pool, 400*24*time.Hour)
	insertOldEvent(t, pool, 500*24*time.Hour)
	insertOldEvent(t, pool, 600*24*time.Hour)
	insertOldEvent(t, pool, 10*24*time.Hour) // recent, should NOT be counted

	opts := purge.Options{
		Before:  cutoff,
		DryRun:  true,
		Table:   purge.TableAuditEvents,
		BatchSz: 1000,
	}
	result, err := purge.Run(ctx, pool, opts)
	if err != nil {
		t.Fatalf("purge.Run dry-run: %v", err)
	}

	if result.RowsDeleted != 3 {
		t.Errorf("dry-run count: got %d, want 3", result.RowsDeleted)
	}
	if !result.DryRun {
		t.Error("result.DryRun should be true")
	}

	// Verify rows were NOT actually deleted.
	var total int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM audit_events`).Scan(&total); err != nil {
		t.Fatalf("count audit_events: %v", err)
	}
	if total != 4 {
		t.Errorf("rows after dry-run: got %d, want 4", total)
	}

	// Verify a dry_run=true row was written to audit_purges.
	var purgeCount int
	var isDryRun bool
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*), bool_or(dry_run) FROM audit_purges WHERE dry_run = true`,
	).Scan(&purgeCount, &isDryRun); err != nil {
		t.Fatalf("query audit_purges: %v", err)
	}
	if purgeCount == 0 {
		t.Error("expected dry_run=true row in audit_purges, found none")
	}
}

// TestPurgeDeletesInRangeAndWritesRecord verifies that a real purge deletes the
// correct rows and records the deletion in audit_purges.
func TestPurgeDeletesInRangeAndWritesRecord(t *testing.T) {
	pool := testPool(t)
	setupSchema(t, pool)
	ctx := context.Background()

	cutoff := time.Now().UTC().Add(-365 * 24 * time.Hour)

	// 2 old events to be deleted, 1 recent event to be preserved.
	insertOldEvent(t, pool, 400*24*time.Hour)
	insertOldEvent(t, pool, 500*24*time.Hour)
	recentID := insertOldEvent(t, pool, 10*24*time.Hour)

	opts := purge.Options{
		Before:  cutoff,
		DryRun:  false,
		Table:   purge.TableAuditEvents,
		BatchSz: 1000,
	}
	result, err := purge.Run(ctx, pool, opts)
	if err != nil {
		t.Fatalf("purge.Run: %v", err)
	}

	if result.RowsDeleted != 2 {
		t.Errorf("rows deleted: got %d, want 2", result.RowsDeleted)
	}
	if result.DryRun {
		t.Error("result.DryRun should be false")
	}

	// Only the recent event should remain.
	var remaining []int64
	rows, err := pool.Query(ctx, `SELECT id FROM audit_events ORDER BY id`)
	if err != nil {
		t.Fatalf("query remaining events: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		remaining = append(remaining, id)
	}
	if len(remaining) != 1 || remaining[0] != recentID {
		t.Errorf("remaining events: got %v, want [%d]", remaining, recentID)
	}

	// Verify audit_purges record.
	var count int
	var rowsDeleted int
	var isDryRun bool
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*), MAX(rows_deleted), bool_or(dry_run) FROM audit_purges WHERE dry_run = false`,
	).Scan(&count, &rowsDeleted, &isDryRun); err != nil {
		t.Fatalf("query audit_purges: %v", err)
	}
	if count == 0 {
		t.Error("expected audit_purges row, found none")
	}
	if rowsDeleted != 2 {
		t.Errorf("audit_purges.rows_deleted: got %d, want 2", rowsDeleted)
	}
}
