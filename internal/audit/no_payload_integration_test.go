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

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit/audittest"
	"github.com/jackc/pgx/v5/pgxpool"
)

// canaryValue is a distinctive string embedded in the test request payload.
// It MUST NOT appear in any audit table row after the request is processed.
const canaryValue = "CANARY_PAYLOAD_8f4a2b91e3c7d056"

// fakeAWSKey is AWS's own documentation example key. It matches the
// AKIA[0-9A-Z]{16} pattern in internal/filter/secrets/patterns.go, so the
// secrets filter blocks the request before it reaches a provider.
const fakeAWSKey = "AKIAIOSFODNN7EXAMPLE"

// canaryModel must be a real alias in configs/models.yaml. Using a model that
// does not exist would make the test pass for the wrong reason: routing would
// fail and the request would never traverse the filter → audit path.
const canaryModel = "aegis-fast"

// blockedStatus is what httputil.WriteContentBlockedError emits for a filter
// block. Asserting the exact status matters — a looser "any 4xx" check cannot
// tell a real filter block apart from a 401 auth failure, a 400 validation
// error, or a 503 unknown-model, none of which exercise the audited path, and
// all of which would make the canary trivially absent.
const blockedStatus = 451

// TestNoPayload_CanaryEndToEnd proves the zero-retention guarantee by
// contradiction rather than by absence:
//
//  1. Send a request carrying a canary string alongside a fake AWS key, under
//     a request ID we choose, so the row can be found deterministically.
//  2. Assert the gateway blocked it with exactly 451.
//  3. Assert an audit row WAS written for that request ID — the positive
//     control. Without this, "canary not found" is satisfied by an empty
//     table and the test proves nothing.
//  4. Only then assert the canary appears in no audit row.
//
// Requires TEST_DATABASE_URL, TEST_SERVER_URL, and TEST_API_KEY; skips cleanly
// when any is absent.
func TestNoPayload_CanaryEndToEnd(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	serverURL := os.Getenv("TEST_SERVER_URL")
	apiKey := os.Getenv("TEST_API_KEY")

	if dbURL == "" || serverURL == "" || apiKey == "" {
		t.Skip("requires TEST_DATABASE_URL, TEST_SERVER_URL, and TEST_API_KEY")
	}

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to database: %v", err)
	}
	// t.Cleanup rather than defer, and the ordering is load-bearing.
	//
	// pgxpool.Close blocks until every checked-out connection is returned, and
	// audittest.Serialise holds one for the length of the test so the advisory
	// lock stays on a connection nobody else can be handed. Deferred calls run
	// before cleanups, so `defer pool.Close()` would block waiting for a
	// connection that is only released by a cleanup that has not run yet: the
	// test would hang until the go test timeout rather than fail.
	//
	// Registered before Serialise so that LIFO ordering releases the lock
	// first and closes the pool second.
	t.Cleanup(pool.Close)

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping database: %v", err)
	}

	// This test polls audit_events for its own row over several seconds, while
	// internal/audit/emitter truncates that table to build its fixtures. Package
	// binaries run in parallel against one database, so without coordination the
	// canary's row can vanish mid-poll and the test fails reporting that the
	// gateway did not write it.
	//
	// The same lock the audit packages take, so all three serialise rather than
	// two of them serialising and the third racing both. It replaces relying on
	// a -p 1 flag in one CI step, which held this shut without saying so and
	// left `go test ./...` on a developer machine still racing.
	audittest.Serialise(t, pool)

	// The gateway echoes an X-Request-ID it is given, so choosing one up front
	// lets us look up this exact request's audit row instead of guessing.
	requestID := fmt.Sprintf("req_canary_%d", time.Now().UnixNano())

	reqBody, _ := json.Marshal(map[string]any{
		"model": canaryModel,
		"messages": []map[string]string{{
			"role": "user",
			"content": fmt.Sprintf("%s Please validate my AWS credentials: %s",
				canaryValue, fakeAWSKey),
		}},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(serverURL, "/")+"/v1/chat/completions",
		bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Request-ID", requestID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// 1. The request must be blocked by the secrets filter specifically.
	if resp.StatusCode != blockedStatus {
		t.Fatalf("expected HTTP %d from the secrets filter, got %d — the request did "+
			"not traverse the filter → audit path, so this test cannot prove anything "+
			"about retention; response body: %s", blockedStatus, resp.StatusCode, body)
	}
	t.Logf("gateway blocked the request with HTTP %d as expected", resp.StatusCode)

	// 2. Positive control: the audit writer is asynchronous, so poll for the
	//    row rather than sleeping a fixed interval.
	deadline := time.Now().Add(10 * time.Second)
	var found int
	for time.Now().Before(deadline) {
		err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM audit_events WHERE request_id = $1 AND event_type = 'filter_block'`,
			requestID).Scan(&found)
		if err != nil {
			t.Fatalf("querying audit_events for the positive control: %v", err)
		}
		if found > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if found == 0 {
		t.Fatalf("no filter_block audit row was written for request %q — without a "+
			"confirmed audit write, the canary being absent proves nothing", requestID)
	}
	t.Logf("positive control satisfied: %d filter_block row(s) written for %s", found, requestID)

	// 3. Now the absence check is meaningful: the audit path demonstrably ran
	//    for this request, and the payload still must not be in it.
	for _, table := range []string{"audit_logs", "audit_events"} {
		assertCanaryAbsent(ctx, t, pool, table, canaryValue)
	}

	if !t.Failed() {
		t.Logf("zero-retention confirmed: audit row written for %s, canary %q absent from all audit rows",
			requestID, canaryValue)
	}
}

// assertCanaryAbsent serialises each audit row to JSON text and asserts the
// canary string appears nowhere — catching any column, including JSONB.
func assertCanaryAbsent(ctx context.Context, t *testing.T, pool *pgxpool.Pool, table, canary string) {
	t.Helper()

	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s t WHERE row_to_json(t)::text ILIKE $1`, table)

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
