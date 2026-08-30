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

package audit_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
)

// An allowlist denial must be its own event type, not an auth_failure.
//
// The distinction is what an evidence pull depends on: an auth_failure is a
// credential that did not verify, a model_denied is a valid credential used
// outside its grant. Counting the first and getting the second means a control
// report overstates failed authentication, and an alert on auth_failure volume
// fires on an allowlist misconfiguration.
//
// Checked against the stored row rather than the constructed Event, because the
// column is what an assessor reads.
func TestLogModelDenied_WritesItsOwnEventType(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer pool.Close()

	reqID := "req_md_" + time.Now().UTC().Format("20060102150405.000000")
	logger := audit.NewLogger(pool)
	// api_key_id is a UUID column; a placeholder string fails the insert and the
	// event is lost, which is the asynchronous-write failure mode this row type
	// is otherwise vulnerable to.
	logger.LogModelDenied(reqID, "org-t", "team-t",
		"3f2a1c44-9b7e-4d21-8a55-0c9f7b1e4d33", "aegis-prod-testpfx",
		audit.UnconfiguredModel, "203.0.113.1:1234")

	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !logger.Drain(drainCtx) {
		t.Fatal("the audit write did not drain before the deadline")
	}

	var eventType, reason string
	var status int
	err = pool.QueryRow(ctx,
		`SELECT event_type, reason, status_code FROM audit_events WHERE request_id = $1`,
		reqID).Scan(&eventType, &reason, &status)
	if err != nil {
		t.Fatalf("reading the row back: %v", err)
	}

	if eventType != string(audit.EventModelDenied) {
		t.Errorf("event_type = %q, want %q; an authorisation denial recorded under the "+
			"authentication event type makes a count of credential failures include "+
			"policy outcomes", eventType, audit.EventModelDenied)
	}
	// The reason is unchanged, so a consumer that filtered on it keeps working.
	if reason != "model_not_allowed" {
		t.Errorf("reason = %q, want %q", reason, "model_not_allowed")
	}
	if status != 403 {
		t.Errorf("status_code = %d, want 403", status)
	}

	_, _ = pool.Exec(ctx, `DELETE FROM audit_events WHERE request_id = $1`, reqID)
}
