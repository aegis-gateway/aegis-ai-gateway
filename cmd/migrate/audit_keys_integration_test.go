//go:build integration

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

package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// key_prefix is varchar(20), so the prefix is short and the readable name is
// carried separately.
//
// An already-expired key needs created_at moved back with it: the valid_expiry
// CHECK is expires_at > created_at, so inserting a past expiry against a default
// created_at of NOW() is rejected rather than stored.
func seedKey(t *testing.T, pool *pgxpool.Pool, id int, name, org, allowed, status string, expiresIn time.Duration) {
	t.Helper()
	prefix := fmt.Sprintf("aegis-prod-ak%d", id)
	createdAgo := 30 * 24 * time.Hour
	_, err := pool.Exec(context.Background(), `
		INSERT INTO api_keys (key_hash, key_prefix, organization_id, team_id, name,
		                      max_classification, allowed_models, created_at, expires_at,
		                      hash_version, status)
		VALUES ($1, $2, $3, 'team', $4, 'INTERNAL', $5::jsonb, NOW() - $6::interval, NOW() + $7::interval, 2, $8)`,
		"hash-"+name, prefix, org, name, allowed, createdAgo, expiresIn, status)
	if err != nil {
		t.Fatalf("seeding %s: %v", name, err)
	}
}

// The report exists to measure exposure, so what it counts has to be exactly
// what can authenticate and reach every model.
func TestAuditKeys_CountsOnlyWhatCanAuthenticate(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `DELETE FROM api_keys WHERE key_prefix LIKE 'aegis-prod-ak%'`); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM api_keys WHERE key_prefix LIKE 'aegis-prod-ak%'`)
	})

	// Organizations unique to this test, so the counts are unaffected by
	// whatever else the database holds. The report is scoped by org, which makes
	// that isolation available without requiring a clean database.
	const orgA, orgB = "audit-keys-test-a", "audit-keys-test-b"

	seedKey(t, pool, 1, "open-a", orgA, `[]`, "active", 24*time.Hour)
	seedKey(t, pool, 2, "open-b", orgA, `[]`, "active", 24*time.Hour)
	seedKey(t, pool, 3, "scoped", orgA, `["aegis-fast"]`, "active", 24*time.Hour)
	// Neither of these can authenticate, so neither is exposure.
	seedKey(t, pool, 4, "revoked", orgA, `[]`, "revoked", 24*time.Hour)
	seedKey(t, pool, 5, "expired", orgA, `[]`, "active", -1*time.Hour)
	seedKey(t, pool, 6, "other-org", orgB, `[]`, "active", 24*time.Hour)

	t.Run("default scope counts active keys only", func(t *testing.T) {
		rep, err := AuditKeys(ctx, pool, orgA, false)
		if err != nil {
			t.Fatalf("auditing: %v", err)
		}
		if rep.Unrestricted != 2 {
			t.Errorf("unrestricted = %d, want 2; a revoked or expired key cannot "+
				"authenticate and counting it overstates the exposure", rep.Unrestricted)
		}
		if rep.Restricted != 1 {
			t.Errorf("restricted = %d, want 1", rep.Restricted)
		}
		if rep.Active != 3 {
			t.Errorf("active = %d, want 3", rep.Active)
		}
		if len(rep.Keys) != 2 {
			t.Errorf("listed %d keys, want 2", len(rep.Keys))
		}
	})

	t.Run("org filter excludes other tenants", func(t *testing.T) {
		rep, err := AuditKeys(ctx, pool, orgB, false)
		if err != nil {
			t.Fatalf("auditing: %v", err)
		}
		if rep.Unrestricted != 1 || len(rep.Keys) != 1 {
			t.Errorf("%s: unrestricted=%d listed=%d, want 1 and 1",
				orgB, rep.Unrestricted, len(rep.Keys))
		}
		if rep.Keys[0].Org != orgB {
			t.Errorf("listed a key from %q under an org filter of %q", rep.Keys[0].Org, orgB)
		}
	})

	t.Run("include-inactive lists but does not count them", func(t *testing.T) {
		rep, err := AuditKeys(ctx, pool, orgA, true)
		if err != nil {
			t.Fatalf("auditing: %v", err)
		}
		if len(rep.Keys) != 4 {
			t.Errorf("listed %d keys with -include-inactive, want 4 (2 active, 1 revoked, 1 expired)",
				len(rep.Keys))
		}
		if rep.Unrestricted != 2 {
			t.Errorf("unrestricted = %d with -include-inactive, want 2: the listing widens "+
				"but the exposure count must not", rep.Unrestricted)
		}
	})

	t.Run("is read only", func(t *testing.T) {
		var before, after int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_keys`).Scan(&before); err != nil {
			t.Fatalf("counting: %v", err)
		}
		if _, err := AuditKeys(ctx, pool, orgA, true); err != nil {
			t.Fatalf("auditing: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_keys`).Scan(&after); err != nil {
			t.Fatalf("counting: %v", err)
		}
		if before != after {
			t.Errorf("api_keys changed from %d to %d rows; this report must be safe to run "+
				"against a production database", before, after)
		}
	})
}
