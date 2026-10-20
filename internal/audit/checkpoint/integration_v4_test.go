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
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	controlplanev1 "github.com/aegis-gateway/aegis-ai-gateway/api/controlplane/v1"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit/checkpoint"
)

// insertUnscannedEvent inserts a request_complete row carrying unscanned-part
// metadata, as LogRequestComplete would for a request that forwarded images.
func insertUnscannedEvent(t *testing.T, db *pgxpool.Pool, ts time.Time, parts, size int64, types string) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(context.Background(), `
		INSERT INTO audit_events (request_id, timestamp, event_type,
		                          unscanned_parts, unscanned_bytes, unscanned_part_types)
		VALUES ($1, $2, 'request_complete', $3, $4, $5)
		RETURNING id
	`, "req-v4-"+time.Now().Format(time.RFC3339Nano), ts, parts, size, types).Scan(&id)
	if err != nil {
		t.Fatalf("insert unscanned event: %v", err)
	}
	return id
}

// A version-4 checkpoint over rows carrying unscanned-part metadata verifies in
// full and yields a proof, and altering any of the three new columns on a sealed
// row is detected. Without the last half the bump would be decorative: the
// columns would exist and nothing would attest them.
func TestCheckpointIntegration_V4SealsTheUnscannedColumns(t *testing.T) {
	db := testDB(t)
	resetCheckpoints(t, db)
	ctx := context.Background()

	past := time.Now().UTC().Add(-10 * time.Minute)
	first := insertUnscannedEvent(t, db, past, 2, 48213, "image/jpeg,image/png")
	insertTestEvent(t, db, past.Add(time.Second)) // NULLs: no unscanned parts
	insertUnscannedEvent(t, db, past.Add(2*time.Second), 1, 1024, "image/webp")

	if err := checkpoint.RunSeal(ctx, db, checkpoint.SealOptions{
		LagSeconds: checkpoint.SealLag(0), BatchSize: 100,
	}); err != nil {
		t.Fatalf("RunSeal: %v", err)
	}

	var version int32
	if err := db.QueryRow(ctx,
		"SELECT hash_schema_version FROM audit_checkpoints ORDER BY id DESC LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("reading version: %v", err)
	}
	if version != controlplanev1.HashSchemaVersion4 {
		t.Fatalf("sealed at hash_schema_version=%d, want %d", version, controlplanev1.HashSchemaVersion4)
	}

	result, _, err := checkpoint.RunVerify(ctx, db, checkpoint.VerifyOptions{Full: true})
	if err != nil {
		t.Fatalf("RunVerify --full: %v", err)
	}
	if len(result.Anomalies) != 0 {
		t.Fatalf("a freshly sealed version-4 chain reported anomalies: %v", result.Anomalies)
	}
	if _, proof, err := checkpoint.RunVerify(ctx, db, checkpoint.VerifyOptions{EventID: first}); err != nil || proof == nil {
		t.Fatalf("inclusion proof at version 4: err=%v proof=%v", err, proof)
	}

	for _, tamper := range []string{
		"UPDATE audit_events SET unscanned_parts = 3 WHERE id = $1",
		"UPDATE audit_events SET unscanned_bytes = 1 WHERE id = $1",
		"UPDATE audit_events SET unscanned_part_types = 'image/png' WHERE id = $1",
		"UPDATE audit_events SET unscanned_parts = NULL, unscanned_bytes = NULL, unscanned_part_types = NULL WHERE id = $1",
	} {
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if _, err := tx.Exec(ctx, tamper, first); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("tamper %q: %v", tamper, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit tamper: %v", err)
		}

		result, _, err := checkpoint.RunVerify(ctx, db, checkpoint.VerifyOptions{Full: true})
		if err != nil {
			t.Fatalf("RunVerify --full after %q: %v", tamper, err)
		}
		if !anyContains(result.Anomalies, "Merkle root mismatch") {
			t.Errorf("%q on a sealed row went undetected; anomalies: %v", tamper, result.Anomalies)
		}

		// Restore, so each tamper is tested in isolation.
		if _, err := db.Exec(ctx, `UPDATE audit_events
			SET unscanned_parts = 2, unscanned_bytes = 48213, unscanned_part_types = 'image/jpeg,image/png'
			WHERE id = $1`, first); err != nil {
			t.Fatalf("restore: %v", err)
		}
	}
}

// A chain that crosses the upgrade holds a version-3 checkpoint followed by
// version-4 ones, and verify-chain --full must verify both with their own field
// sets. This is the property migration 018 relies on to not refuse: the
// version-3 leaf ignores the new columns, so it recomputes byte-for-byte.
//
// The version-3 checkpoint is built by hand with the exported encoder, because
// this build's sealer only writes version 4.
func TestCheckpointIntegration_MixedV3AndV4ChainVerifies(t *testing.T) {
	db := testDB(t)
	resetCheckpoints(t, db)
	ctx := context.Background()

	past := time.Now().UTC().Add(-10 * time.Minute)
	insertTestEvent(t, db, past)
	insertTestEvent(t, db, past.Add(time.Second))

	// Seal events 1-2 at version 3, as a pre-upgrade sealer would have.
	rows, err := db.Query(ctx, `SELECT `+checkpoint.EventColumnsV3+`
		FROM audit_events ORDER BY id ASC`)
	if err != nil {
		t.Fatalf("reading events: %v", err)
	}
	var leaves [][]byte
	var lo, hi time.Time
	for rows.Next() {
		var r checkpoint.AuditEventRow
		if err := rows.Scan(
			&r.ID, &r.RequestID, &r.Timestamp, &r.EventType,
			&r.OrganizationID, &r.TeamID, &r.UserID, &r.APIKeyID,
			&r.IPAddress, &r.UserAgent, &r.Endpoint, &r.Method,
			&r.StatusCode, &r.ErrorMessage,
			&r.APIKeyPrefix, &r.LimitDimension, &r.LimitValue,
			&r.SpentCents, &r.LimitCents, &r.FilterType, &r.Reason,
			&r.Provider, &r.Model, &r.Mode, &r.Operation, &r.ErrorDetail,
			&r.ModelServed, &r.Classification,
			&r.PromptTokens, &r.CompletionTokens, &r.TotalTokens, &r.DurationMs,
		); err != nil {
			t.Fatalf("scanning: %v", err)
		}
		lh, err := checkpoint.EventLeafHashV3(r)
		if err != nil {
			t.Fatalf("leaf: %v", err)
		}
		leaves = append(leaves, lh)
		if lo.IsZero() || r.Timestamp.Before(lo) {
			lo = r.Timestamp
		}
		if r.Timestamp.After(hi) {
			hi = r.Timestamp
		}
	}
	rows.Close()
	if len(leaves) != 2 {
		t.Fatalf("expected 2 events, read %d", len(leaves))
	}
	root := checkpoint.MerkleRoot(leaves)
	sealedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	cpHash, err := controlplanev1.ComputeCheckpointHashV3(root, controlplanev1.GenesisPrevHashBytes(),
		1, 2, 2, controlplanev1.HashSchemaVersion3, sealedAt, controlplanev1.GenesisPrevCheckpointID)
	if err != nil {
		t.Fatalf("checkpoint hash: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO audit_checkpoints
		    (range_start, range_end, event_count, merkle_root,
		     prev_checkpoint_id, prev_checkpoint_hash, checkpoint_hash,
		     hash_schema_version, sealed_at, sealer_version, canonicalization_spec,
		     covered_from, covered_to, covered_range_source)
		VALUES (1, 2, 2, $1, NULL, $2, $3, 3, $4, 'test-v3', 'rfc8785-v1', $5, $6, 'sealed')
	`, root, controlplanev1.GenesisPrevHashBytes(), cpHash, sealedAt, lo, hi); err != nil {
		t.Fatalf("inserting the version-3 checkpoint: %v", err)
	}

	// Post-upgrade traffic, sealed by this build at version 4.
	insertUnscannedEvent(t, db, past.Add(2*time.Second), 1, 512, "image/png")
	insertTestEvent(t, db, past.Add(3*time.Second))
	if err := checkpoint.RunSeal(ctx, db, checkpoint.SealOptions{
		LagSeconds: checkpoint.SealLag(0), BatchSize: 100,
	}); err != nil {
		t.Fatalf("RunSeal: %v", err)
	}

	var versions []int32
	vrows, err := db.Query(ctx, "SELECT hash_schema_version FROM audit_checkpoints ORDER BY id")
	if err != nil {
		t.Fatalf("reading versions: %v", err)
	}
	for vrows.Next() {
		var v int32
		if err := vrows.Scan(&v); err != nil {
			t.Fatalf("scan version: %v", err)
		}
		versions = append(versions, v)
	}
	vrows.Close()
	if len(versions) != 2 || versions[0] != 3 || versions[1] != 4 {
		t.Fatalf("chain versions = %v, want [3 4]", versions)
	}

	result, _, err := checkpoint.RunVerify(ctx, db, checkpoint.VerifyOptions{Full: true})
	if err != nil {
		t.Fatalf("RunVerify --full: %v", err)
	}
	if len(result.Anomalies) != 0 {
		t.Fatalf("a chain crossing the version-3 to version-4 upgrade reported anomalies: %v", result.Anomalies)
	}
	if result.CheckpointsVerified != 2 {
		t.Errorf("verified %d checkpoints, want 2", result.CheckpointsVerified)
	}

	// Writing the new columns on a version-3 row changes nothing the version-3
	// leaf covers, so it must still verify. This is the stability claim, on a
	// real row rather than a struct.
	if _, err := db.Exec(ctx, `UPDATE audit_events
		SET unscanned_parts = 9, unscanned_bytes = 9, unscanned_part_types = 'image/gif'
		WHERE id = 1`); err != nil {
		t.Fatalf("update: %v", err)
	}
	result, _, err = checkpoint.RunVerify(ctx, db, checkpoint.VerifyOptions{Full: true})
	if err != nil {
		t.Fatalf("RunVerify --full: %v", err)
	}
	if len(result.Anomalies) != 0 {
		t.Errorf("the version-3 leaf moved when a version-4 column changed: %v", result.Anomalies)
	}
	for _, id := range []int64{1, 3} {
		if _, proof, err := checkpoint.RunVerify(ctx, db, checkpoint.VerifyOptions{EventID: id}); err != nil || proof == nil {
			t.Errorf("inclusion proof for event %d: err=%v proof=%v", id, err, proof)
		}
	}
}

func anyContains(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
