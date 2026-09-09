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
func seedKey(t *testing.T, pool *pgxpool.Pool, id int, name, org, allowed, status string,
	expiresIn time.Duration, revokedAgo *time.Duration) {
	t.Helper()
	prefix := fmt.Sprintf("aegis-prod-ak%d", id)
	createdAgo := 30 * 24 * time.Hour
	var revokedAt interface{}
	if revokedAgo != nil {
		revokedAt = time.Now().Add(-*revokedAgo)
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO api_keys (key_hash, key_prefix, organization_id, team_id, name,
		                      max_classification, allowed_models, created_at, expires_at,
		                      hash_version, status, revoked_at)
		VALUES ($1, $2, $3, 'team', $4, 'INTERNAL', $5::jsonb, NOW() - $6::interval, NOW() + $7::interval, 2, $8, $9)`,
		"hash-"+name, prefix, org, name, allowed, createdAgo, expiresIn, status, revokedAt)
	if err != nil {
		t.Fatalf("seeding %s: %v", name, err)
	}
}

// The report exists to measure exposure, so what it counts has to be exactly
// what can still authenticate and reach every model.
//
// That is NOT the same as "active". A cache hit returns stored metadata without
// rechecking status or expiry, so a key revoked minutes ago still works until
// its entry ages out. Excluding it would let this command report no exposure
// while such a credential is live, which is the failure it exists to prevent.
func TestAuditKeys_CountsWhatCanStillAuthenticate(t *testing.T) {
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

	longAgo := 2 * time.Hour
	justNow := 30 * time.Second

	seedKey(t, pool, 1, "open-a", orgA, `[]`, "active", 24*time.Hour, nil)
	seedKey(t, pool, 2, "open-b", orgA, `[]`, "active", 24*time.Hour, nil)
	seedKey(t, pool, 3, "scoped", orgA, `["aegis-fast"]`, "active", 24*time.Hour, nil)
	// Revoked long enough ago that no cache entry can survive: not exposure.
	seedKey(t, pool, 4, "revoked-long-ago", orgA, `[]`, "revoked", 24*time.Hour, &longAgo)
	// Expired long enough ago for the same reason.
	seedKey(t, pool, 5, "expired-long-ago", orgA, `[]`, "active", -2*time.Hour, nil)
	// Revoked seconds ago, so a cache hit may still be serving it. This IS
	// exposure, and counting it is the whole point: nothing re-validates on a
	// cache hit, so the credential still works.
	seedKey(t, pool, 6, "revoked-just-now", orgA, `[]`, "revoked", 24*time.Hour, &justNow)
	seedKey(t, pool, 7, "other-org", orgB, `[]`, "active", 24*time.Hour, nil)
	// Revoked once, later reactivated, and revoked_at never cleared. lookupDB
	// filters on status and expiry alone, so this key authenticates today. A
	// window computed from the historical timestamp would hide a live,
	// unrestricted credential.
	longPast := 60 * 24 * time.Hour
	seedKey(t, pool, 8, "reactivated", orgA, `[]`, "active", 24*time.Hour, &longPast)

	t.Run("a key revoked inside the cache window is still exposure", func(t *testing.T) {
		rep, err := AuditKeys(ctx, pool, orgA, false)
		if err != nil {
			t.Fatalf("auditing: %v", err)
		}
		// Two active plus the one revoked seconds ago, which a cache hit may
		// still be serving. Excluding it would let this command report no
		// exposure while a credential that can use every model is live.
		if rep.Unrestricted != 4 {
			t.Errorf("unrestricted = %d, want 4: three active and one revoked inside the "+
				"%s cache window, which still authenticates because nothing re-validates "+
				"on a cache hit", rep.Unrestricted, rep.CacheWindow)
		}
		if rep.StillCacheable != 1 {
			t.Errorf("stillCacheable = %d, want 1", rep.StillCacheable)
		}
		if rep.Active != 4 {
			t.Errorf("active = %d, want 4", rep.Active)
		}
		if len(rep.Keys) != 4 {
			t.Errorf("listed %d keys, want 4", len(rep.Keys))
		}
		var flagged int
		for _, k := range rep.Keys {
			if k.StillCacheable {
				flagged++
				if k.Name != "revoked-just-now" {
					t.Errorf("flagged %q as still cacheable, want revoked-just-now", k.Name)
				}
			}
			if k.ID == "" {
				t.Error("a listed key carries no id; remediation by key_prefix can hit " +
					"another tenant's row because that column is not unique")
			}
		}
		if flagged != 1 {
			t.Errorf("%d keys flagged as still cacheable, want 1", flagged)
		}
	})

	t.Run("a key revoked before the window is not exposure", func(t *testing.T) {
		rep, err := AuditKeys(ctx, pool, orgA, false)
		if err != nil {
			t.Fatalf("auditing: %v", err)
		}
		for _, k := range rep.Keys {
			if k.Name == "revoked-long-ago" || k.Name == "expired-long-ago" {
				t.Errorf("%q was listed; it stopped being usable long before the %s "+
					"window and cannot authenticate", k.Name, rep.CacheWindow)
			}
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
		if len(rep.Keys) == 0 {
			t.Fatal("nothing listed")
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
		if len(rep.Keys) != 6 {
			t.Errorf("listed %d keys with -include-inactive, want 6 (3 active, 1 revoked "+
				"just now, 1 revoked long ago, 1 expired long ago)", len(rep.Keys))
		}
		if rep.Unrestricted != 4 {
			t.Errorf("unrestricted = %d with -include-inactive, want 4: the listing widens "+
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

// revoked_at is historical, not current state. A key revoked once and later
// reactivated still carries the old timestamp, and lookupDB authenticates it
// because it filters on status and expiry alone. Computing the cache window from
// that timestamp hid a live, unrestricted credential from both the count and the
// listing, and the command exited 0.
func TestAuditKeys_ReactivatedKeyIsNotHiddenByAStaleRevocation(t *testing.T) {
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

	const org = "audit-keys-reactivated"
	if _, err := pool.Exec(ctx, `DELETE FROM api_keys WHERE organization_id = $1`, org); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM api_keys WHERE organization_id = $1`, org)
	})

	longPast := 60 * 24 * time.Hour
	seedKey(t, pool, 90, "reactivated", org, `[]`, "active", 24*time.Hour, &longPast)

	rep, err := AuditKeys(ctx, pool, org, false)
	if err != nil {
		t.Fatalf("auditing: %v", err)
	}
	if rep.Unrestricted != 1 {
		t.Errorf("unrestricted = %d, want 1; a key that authenticates today was hidden by "+
			"a revocation timestamp from 60 days ago", rep.Unrestricted)
	}
	if len(rep.Keys) != 1 {
		t.Fatalf("listed %d keys, want 1", len(rep.Keys))
	}
	if rep.Keys[0].StillCacheable {
		t.Error("a currently valid key was marked as merely still-cacheable; it is live, " +
			"not lingering in a cache")
	}
}
