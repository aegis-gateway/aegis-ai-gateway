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

// Package audittest serialises tests that share the audit tables.
//
// internal/audit/checkpoint and internal/audit/emitter both truncate
// audit_events and audit_checkpoints, seal them, and assert on the result.
// `go test` runs package binaries in parallel against one database, so without
// coordination each one can truncate the other's fixture mid-run. That was
// measured at roughly one failure in six full-suite runs: rare enough to look
// like a flake and frequent enough to erode trust in the suite.
//
// The alternative was to give each package its own database. These tests
// deliberately run against the migrated schema rather than one they create,
// because what they check includes the real column types and constraints, so
// a second database would mean migrating twice and keeping both current.
// Serialising is smaller and does not weaken what is being tested.
package audittest

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// lockKey is derived the way the sealer derives its own, from a distinct name.
//
// Distinct on purpose: taking the sealer's key would block the sealer itself,
// and these tests need to run it.
var lockKey int64

func init() {
	h := sha256.Sum256([]byte("aegis_audit_test"))
	lockKey = int64(binary.LittleEndian.Uint64(h[:8]))
}

// Serialise blocks until no other test process holds the audit tables, and
// releases on cleanup.
//
// The lock is session scoped, so it is held on one dedicated connection rather
// than on a pooled one that might be handed to someone else mid-test. A test
// that panics still releases it: the connection closes and PostgreSQL drops
// session locks with the session.
func Serialise(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquiring a connection for the audit test lock: %v", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		conn.Release()
		t.Fatalf("taking the audit test lock (key=%d): %v", lockKey, err)
	}

	t.Cleanup(func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer releaseCancel()
		if _, err := conn.Exec(releaseCtx, "SELECT pg_advisory_unlock($1)", lockKey); err != nil {
			t.Logf("releasing the audit test lock: %v", err)
		}
		conn.Release()
	})
}
