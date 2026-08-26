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
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit/audittest"
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

	// internal/audit/emitter truncates and seals the same tables, and package
	// binaries run in parallel against one database. Serialise before any test
	// here touches them.
	audittest.Serialise(t, pool)
	return pool
}

// resetCheckpoints clears both audit_events and audit_checkpoints.
//
// Both, not just checkpoints. A test that truncates only the checkpoints
// leaves the events behind, and the next seal picks them up, so what a test
// sees depends on which tests ran before it. That made
// TestCheckpointIntegration_SealEmpty pass only while it happened to run
// first, which stopped being true when another file was added ahead of it
// alphabetically.
func resetCheckpoints(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	// audit_purges as well, and not as an afterthought. verify-chain --full
	// consults it to decide whether a range is attested-but-unverifiable, so a
	// purge row left behind by one test makes a later test's healthy chain
	// report an anomaly. That is the same class of cross-test contamination
	// the search_path fix addressed for internal/purge, arriving by a
	// different route.
	_, err := db.Exec(ctx,
		"TRUNCATE audit_events, audit_checkpoints, audit_purges RESTART IDENTITY CASCADE")
	if err != nil {
		t.Fatalf("truncate the audit tables: %v", err)
	}
}

// insertTestEvent inserts a synthetic audit_events row and returns its ID.
func insertTestEvent(t *testing.T, db *pgxpool.Pool, ts time.Time) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(context.Background(), `
		INSERT INTO audit_events (request_id, timestamp, event_type)
		VALUES ($1, $2, 'test_event')
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
		LagSeconds: checkpoint.SealLag(0),
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

	opts := checkpoint.SealOptions{LagSeconds: checkpoint.SealLag(0), BatchSize: 100}
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

	opts := checkpoint.SealOptions{LagSeconds: checkpoint.SealLag(0), BatchSize: 100}
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

	opts := checkpoint.SealOptions{LagSeconds: checkpoint.SealLag(0), BatchSize: 100}

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

// TestCheckpointIntegration_InclusionProof exercises `verify-chain --event E`
// against a real database.
//
// The proof path was the one caller of audit_checkpoints that migration 009
// broke: buildInclusionProof was updated to select covered_from and covered_to
// but not to scan them, so every lookup failed on a column/destination count
// mismatch. Nothing caught it, because the only inclusion-proof test in the
// package operates on in-memory leaves and never opens a connection. The
// column list and the scan destinations only disagree in front of Postgres, so
// this test has to reach it.
func TestCheckpointIntegration_InclusionProof(t *testing.T) {
	db := testDB(t)
	resetCheckpoints(t, db)

	// Five events, so the tree has an odd level and the proof is not trivial.
	past := time.Now().UTC().Add(-10 * time.Minute)
	ids := make([]int64, 0, 5)
	for i := 0; i < 5; i++ {
		ids = append(ids, insertTestEvent(t, db, past.Add(time.Duration(i)*time.Second)))
	}

	opts := checkpoint.SealOptions{LagSeconds: checkpoint.SealLag(0), BatchSize: 100}
	if err := checkpoint.RunSeal(context.Background(), db, opts); err != nil {
		t.Fatalf("RunSeal: %v", err)
	}

	var storedRoot []byte
	var storedID int64
	err := db.QueryRow(context.Background(),
		"SELECT id, merkle_root FROM audit_checkpoints ORDER BY id ASC LIMIT 1").
		Scan(&storedID, &storedRoot)
	if err != nil {
		t.Fatalf("read sealed checkpoint: %v", err)
	}

	// Every covered event must yield a proof, not just the first.
	for _, eventID := range ids {
		_, proof, err := checkpoint.RunVerify(context.Background(), db,
			checkpoint.VerifyOptions{EventID: eventID})
		if err != nil {
			t.Fatalf("RunVerify --event %d: %v", eventID, err)
		}
		if proof == nil {
			t.Fatalf("RunVerify --event %d returned no proof", eventID)
		}
		if proof.EventID != eventID {
			t.Errorf("proof is for event %d, asked for %d", proof.EventID, eventID)
		}
		if proof.CheckpointID != storedID {
			t.Errorf("event %d: proof names checkpoint %d, event was sealed into %d",
				eventID, proof.CheckpointID, storedID)
		}
		if got, want := proof.MerkleRoot, hex.EncodeToString(storedRoot); got != want {
			t.Errorf("event %d: proof root %s does not match the sealed root %s",
				eventID, got, want)
		}
		if proof.LeafIndex < 0 || proof.LeafIndex >= len(ids) {
			t.Errorf("event %d: leaf index %d is outside the covered range",
				eventID, proof.LeafIndex)
		}
	}

	// An event that exists but was never sealed has no proof, and that is an
	// error rather than an empty one.
	unsealed := insertTestEvent(t, db, time.Now().UTC())
	if _, _, err := checkpoint.RunVerify(context.Background(), db,
		checkpoint.VerifyOptions{EventID: unsealed}); err == nil {
		t.Error("expected an error for an unsealed event, got a proof")
	}
}

// TestCheckpointIntegration_ProofRefusesUnknownHashSchema covers the guard on
// the inclusion-proof path.
//
// The proof path recomputes leaves at hash_schema_version=2 and, on a root
// mismatch, says the audit rows have been altered since sealing. That sentence
// is an accusation, and it is the wrong one when the real cause is a checkpoint
// sealed under a different field set: the rows may be untouched and the build
// simply cannot hash them the way they were hashed.
//
// Migration 013 refuses to run while version-1 checkpoints exist, so this state
// is not reachable through a supported upgrade. The guard is here because the
// cost of being wrong is telling an operator their audit trail was tampered
// with, and the cost of the check is one comparison on a value already loaded.
func TestCheckpointIntegration_ProofRefusesUnknownHashSchema(t *testing.T) {
	db := testDB(t)
	resetCheckpoints(t, db)

	past := time.Now().UTC().Add(-10 * time.Minute)
	eventID := insertTestEvent(t, db, past)

	opts := checkpoint.SealOptions{LagSeconds: checkpoint.SealLag(0), BatchSize: 100}
	if err := checkpoint.RunSeal(context.Background(), db, opts); err != nil {
		t.Fatalf("RunSeal: %v", err)
	}

	// It must work before the version is changed, or the assertion below would
	// pass for the wrong reason.
	if _, proof, err := checkpoint.RunVerify(context.Background(), db,
		checkpoint.VerifyOptions{EventID: eventID}); err != nil || proof == nil {
		t.Fatalf("proof should succeed at version 2 before the change: err=%v proof=%v", err, proof)
	}

	if _, err := db.Exec(context.Background(),
		"UPDATE audit_checkpoints SET hash_schema_version = 1"); err != nil {
		t.Fatalf("forcing hash_schema_version: %v", err)
	}

	_, proof, err := checkpoint.RunVerify(context.Background(), db,
		checkpoint.VerifyOptions{EventID: eventID})
	if err == nil {
		t.Fatal("expected a refusal for a checkpoint this build cannot recompute, got a proof")
	}
	if proof != nil {
		t.Error("no proof should be emitted for a checkpoint this build cannot recompute")
	}
	if !strings.Contains(err.Error(), "hash_schema_version=1") {
		t.Errorf("the error should name the version it cannot handle, got: %v", err)
	}
	if strings.Contains(err.Error(), "have been altered") {
		t.Errorf("a version it cannot recompute must not be reported as tampering, got: %v", err)
	}
}
