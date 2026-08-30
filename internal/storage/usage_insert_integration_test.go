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

//go:build integration

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRecordSync_PersistsEveryColumn writes a usage record and reads back every
// value it wrote.
//
// The rest of usage_test.go asserts on struct fields, which cannot fail when the
// INSERT is wrong: the column list, the $N placeholders and the Exec arguments
// are three parallel lists in one function, and adding a column means editing
// all three in step. A mismatch there is silent — RecordUsage swallows the error
// into a goroutine — so it is worth one test that actually reaches Postgres.
func TestRecordSync_PersistsEveryColumn(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		// Fails rather than skips: this test guards a write path whose failure
		// mode is a log line nobody reads. A skipped run would report success
		// while proving nothing.
		t.Fatal("TEST_DATABASE_URL is not set; this test needs a migrated database")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	// t.Cleanup rather than defer, and registered first: cleanups run LIFO after
	// the deferred calls, so `defer pool.Close()` would shut the pool before the
	// row cleanup below could use it.
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("reaching %s: %v", dbURL, err)
	}

	orgID := fmt.Sprintf("storage-test-%d", time.Now().UnixNano())

	// usage_records.api_key_id is a uuid with a foreign key onto api_keys.
	var keyID string
	err = pool.QueryRow(ctx, `
		INSERT INTO api_keys (key_hash, key_prefix, organization_id, team_id, user_id,
		                      name, max_classification, allowed_models, expires_at)
		VALUES ($1, 'aegis-test', $2, 'team', 'user', 'storage insert test',
		        'INTERNAL', '[]', now() + interval '1 hour')
		RETURNING id`,
		fmt.Sprintf("%064x", time.Now().UnixNano()), orgID,
	).Scan(&keyID)
	if err != nil {
		t.Fatalf("seeding api key: %v", err)
	}
	t.Cleanup(func() {
		// usage_records cascades from api_keys.
		if _, err := pool.Exec(context.Background(), "DELETE FROM api_keys WHERE id = $1", keyID); err != nil {
			t.Logf("cleaning up key %s: %v", keyID, err)
		}
	})

	// Every field distinct, so a mis-ordered placeholder shows up as a wrong
	// value rather than coinciding with the right one.
	want := UsageRecord{
		RequestID:          "req-persist-1",
		OrganizationID:     orgID,
		TeamID:             "team-2",
		UserID:             "user-3",
		APIKeyID:           keyID,
		ModelRequested:     "claude-sonnet-4-5",
		ModelServed:        "claude-sonnet-4-5-20250929",
		Provider:           "anthropic",
		Classification:     "INTERNAL",
		PromptTokens:       1101,
		CompletionTokens:   1202,
		TotalTokens:        2303,
		CachedTokens:       404,
		CacheWrite5mTokens: 505,
		CacheWrite1hTokens: 606,
		EstimatedCostUSD:   0.123456,
		DurationMs:         1707,
		StatusCode:         200,
		Project:            "proj-8",
		Stream:             true,
	}

	if err := NewUsageRecorder(pool).recordSync(ctx, want); err != nil {
		t.Fatalf("recordSync: %v", err)
	}

	var got UsageRecord
	err = pool.QueryRow(ctx, `
		SELECT request_id, organization_id, team_id, user_id, api_key_id,
		       model_requested, model_served, provider, classification,
		       prompt_tokens, completion_tokens, total_tokens,
		       cached_tokens, cache_write_5m_tokens, cache_write_1h_tokens,
		       estimated_cost_usd, duration_ms, status_code, project, stream
		  FROM usage_records WHERE organization_id = $1`, orgID).Scan(
		&got.RequestID, &got.OrganizationID, &got.TeamID, &got.UserID, &got.APIKeyID,
		&got.ModelRequested, &got.ModelServed, &got.Provider, &got.Classification,
		&got.PromptTokens, &got.CompletionTokens, &got.TotalTokens,
		&got.CachedTokens, &got.CacheWrite5mTokens, &got.CacheWrite1hTokens,
		&got.EstimatedCostUSD, &got.DurationMs, &got.StatusCode, &got.Project, &got.Stream,
	)
	if err != nil {
		t.Fatalf("reading the row back: %v", err)
	}

	if got != want {
		t.Errorf("round trip changed the record:\n got %+v\nwant %+v", got, want)
	}
}
