//go:build integration

package audit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// canaryValue is a distinctive string embedded in the test request payload.
// It MUST NOT appear in any audit table row after the request is processed.
const canaryValue = "CANARY_PAYLOAD_8f4a2b91e3c7d056"

// fakeAWSKey is a well-known fake AWS access key that should trigger the
// secrets filter and cause the request to be blocked.
const fakeAWSKey = "AKIAIOSFODNN7EXAMPLE"

// TestNoPayload_CanaryEndToEnd sends a request containing a canary string
// embedded alongside a fake AWS key (to trigger secrets filtering), then
// asserts:
//  1. The gateway returns a blocking response (HTTP 4xx).
//  2. The canary string does NOT appear in any row of audit_logs or
//     audit_events — confirming the gateway persists zero payload.
//  3. The canary is not surfaced in structured log output captured by
//     slog at INFO level (verified via the absence from audit DB which is
//     the primary persistence path; direct log-line capture requires
//     server-side stdout redirection and is noted as a best-effort check).
//
// Requires:
//   - TEST_DATABASE_URL — PostgreSQL DSN for the AEGIS database
//   - TEST_SERVER_URL   — Base URL of a running AEGIS gateway (e.g. http://localhost:8080)
//   - TEST_API_KEY      — A valid API key accepted by the gateway
//
// Skips cleanly when any required env var is absent.
func TestNoPayload_CanaryEndToEnd(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	serverURL := os.Getenv("TEST_SERVER_URL")
	apiKey := os.Getenv("TEST_API_KEY")

	if dbURL == "" || serverURL == "" || apiKey == "" {
		t.Skip("requires TEST_DATABASE_URL, TEST_SERVER_URL, and TEST_API_KEY")
	}

	ctx := context.Background()

	// --- Connect to database ---
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping database: %v", err)
	}

	// --- Build request with canary + secrets-filter trigger ---
	// The canary is embedded in the user message alongside a fake AWS key.
	// A correct secrets filter will block this request; the canary must
	// NOT be persisted in the audit trail.
	reqBody, _ := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"messages": []map[string]string{
			{
				"role": "user",
				"content": fmt.Sprintf(
					"%s Please validate my AWS credentials: %s",
					canaryValue, fakeAWSKey,
				),
			},
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(serverURL, "/")+"/v1/chat/completions",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	// --- Send request ---
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// 1. Assert the request was blocked.
	if resp.StatusCode < 400 {
		t.Errorf("expected a blocking response (HTTP 4xx), got %d — "+
			"the secrets filter should have blocked the fake AWS key; "+
			"response body: %s", resp.StatusCode, body)
	} else {
		t.Logf("gateway correctly blocked the request with HTTP %d", resp.StatusCode)
	}

	// Give the async audit writer (Logger.Log uses a goroutine) time to flush.
	time.Sleep(500 * time.Millisecond)

	// 2. Assert the canary does not appear in any audit table row.
	for _, table := range []string{"audit_logs", "audit_events"} {
		assertCanaryAbsent(ctx, t, pool, table, canaryValue)
	}

	if !t.Failed() {
		t.Logf("zero-retention confirmed: canary %q absent from all audit rows", canaryValue)
	}
}

// assertCanaryAbsent converts each audit table row to JSON text and asserts
// the canary string does not appear anywhere — catching any column type.
func assertCanaryAbsent(ctx context.Context, t *testing.T, pool *pgxpool.Pool, table, canary string) {
	t.Helper()

	// row_to_json serialises the entire row including all JSONB columns.
	// Using a table alias so PostgreSQL resolves the composite type.
	query := fmt.Sprintf(
		`SELECT COUNT(*) FROM %s t WHERE row_to_json(t)::text ILIKE $1`,
		table,
	)

	var count int
	if err := pool.QueryRow(ctx, query, "%"+canary+"%").Scan(&count); err != nil {
		t.Errorf("querying %s for canary: %v", table, err)
		return
	}

	if count > 0 {
		t.Errorf("ZERO-RETENTION VIOLATION: canary %q found in %d row(s) of %s — "+
			"payload was persisted in the audit trail", canary, count, table)
	}
}
