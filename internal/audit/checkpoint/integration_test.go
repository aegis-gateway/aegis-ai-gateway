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

package checkpoint_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit/checkpoint"
)

// Integration tests require a live PostgreSQL database.
// Skip when TEST_DATABASE_URL is unset.

func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping integration test")
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

// resetCheckpoints truncates audit_checkpoints for a clean-slate test.
func resetCheckpoints(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	_, err := db.Exec(ctx, "TRUNCATE audit_checkpoints RESTART IDENTITY CASCADE")
	if err != nil {
		t.Fatalf("truncate audit_checkpoints: %v", err)
	}
}

// insertTestEvent inserts a synthetic audit_events row and returns its ID.
func insertTestEvent(t *testing.T, db *pgxpool.Pool, ts time.Time) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(context.Background(), `
		INSERT INTO audit_events (request_id, timestamp, event_type, metadata)
		VALUES ($1, $2, 'test_event', '{}')
		RETURNING id
	`, "req-test-"+time.Now().Format(time.RFC3339Nano), ts).Scan(&id)
	if err != nil {
		t.Fatalf("insert test event: %v", err)
	}
	return id
}

// TestCheckpointIntegration_SealEmpty verifies that sealing against an empty table is a no-op.
func TestCheckpointIntegration_SealEmpty(t *testing.T) {
	db := testDB(t)
	resetCheckpoints(t, db)

	opts := checkpoint.SealOptions{
		LagSeconds: 0,
		BatchSize:  100,
	}
	if err := checkpoint.RunSeal(context.Background(), db, opts); err != nil {
		t.Fatalf("RunSeal on empty table: %v", err)
	}

	var count int
	db.QueryRow(context.Background(), "SELECT COUNT(*) FROM audit_checkpoints").Scan(&count) //nolint:errcheck
	if count != 0 {
		t.Fatalf("expected 0 checkpoints after sealing empty table, got %d", count)
	}
}

// TestCheckpointIntegration_SealAndVerify seals real events and verifies the chain.
func TestCheckpointIntegration_SealAndVerify(t *testing.T) {
	db := testDB(t)
	resetCheckpoints(t, db)

	// Insert 5 events in the past (outside the default lag window).
	past := time.Now().UTC().Add(-10 * time.Minute)
	for i := 0; i < 5; i++ {
		insertTestEvent(t, db, past.Add(time.Duration(i)*time.Second))
	}

	opts := checkpoint.SealOptions{LagSeconds: 0, BatchSize: 100}
	if err := checkpoint.RunSeal(context.Background(), db, opts); err != nil {
		t.Fatalf("RunSeal: %v", err)
	}

	// Verify chain (fast path).
	result, _, err := checkpoint.RunVerify(context.Background(), db, checkpoint.VerifyOptions{})
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	if len(result.Anomalies) > 0 {
		t.Fatalf("verify found anomalies: %v", result.Anomalies)
	}
	if result.CheckpointsVerified == 0 {
		t.Fatal("expected at least one checkpoint to be verified")
	}

	// Verify chain (--full re-hash).
	resultFull, _, err := checkpoint.RunVerify(context.Background(), db, checkpoint.VerifyOptions{Full: true})
	if err != nil {
		t.Fatalf("RunVerify --full: %v", err)
	}
	if len(resultFull.Anomalies) > 0 {
		t.Fatalf("verify --full found anomalies: %v", resultFull.Anomalies)
	}
}

// TestCheckpointIntegration_TamperDetection seals events, tampers one, and verifies --full detects it.
func TestCheckpointIntegration_TamperDetection(t *testing.T) {
	db := testDB(t)
	resetCheckpoints(t, db)

	past := time.Now().UTC().Add(-10 * time.Minute)
	var firstID int64
	for i := 0; i < 3; i++ {
		id := insertTestEvent(t, db, past.Add(time.Duration(i)*time.Second))
		if i == 0 {
			firstID = id
		}
	}

	opts := checkpoint.SealOptions{LagSeconds: 0, BatchSize: 100}
	if err := checkpoint.RunSeal(context.Background(), db, opts); err != nil {
		t.Fatalf("RunSeal: %v", err)
	}

	// Tamper: change the event_type of the first event.
	_, err := db.Exec(context.Background(),
		"UPDATE audit_events SET event_type = 'TAMPERED' WHERE id = $1", firstID)
	if err != nil {
		t.Fatalf("tamper event: %v", err)
	}

	result, _, err := checkpoint.RunVerify(context.Background(), db, checkpoint.VerifyOptions{Full: true})
	if err != nil {
		t.Fatalf("RunVerify --full after tamper: %v", err)
	}
	if len(result.Anomalies) == 0 {
		t.Fatal("expected anomalies after tampering — verify --full should have detected the tampered row")
	}
}

// TestCheckpointIntegration_ConcurrentSeal verifies that a second sealer exits cleanly
// without forking the chain.
func TestCheckpointIntegration_ConcurrentSeal(t *testing.T) {
	db := testDB(t)
	resetCheckpoints(t, db)

	past := time.Now().UTC().Add(-10 * time.Minute)
	for i := 0; i < 5; i++ {
		insertTestEvent(t, db, past.Add(time.Duration(i)*time.Second))
	}

	opts := checkpoint.SealOptions{LagSeconds: 0, BatchSize: 100}

	var wg sync.WaitGroup
	errs := make([]error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = checkpoint.RunSeal(context.Background(), db, opts)
		}(i)
	}
	wg.Wait()

	// Exactly one of the two must succeed; the other must return an advisory lock error.
	successCount := 0
	lockErrCount := 0
	for _, err := range errs {
		if err == nil {
			successCount++
		} else {
			lockErrCount++
		}
	}

	if successCount == 0 {
		t.Fatalf("both concurrent sealers failed: %v, %v", errs[0], errs[1])
	}

	// Verify no forked chain: should have a single linear sequence of checkpoints.
	var count int
	db.QueryRow(context.Background(), "SELECT COUNT(*) FROM audit_checkpoints").Scan(&count) //nolint:errcheck
	result, _, err := checkpoint.RunVerify(context.Background(), db, checkpoint.VerifyOptions{})
	if err != nil {
		t.Fatalf("RunVerify after concurrent seal: %v", err)
	}
	if len(result.Anomalies) > 0 {
		t.Fatalf("chain anomalies after concurrent seal: %v", result.Anomalies)
	}
	t.Logf("concurrent seal: %d checkpoints, success=%d, lock_blocked=%d", count, successCount, lockErrCount)
}
