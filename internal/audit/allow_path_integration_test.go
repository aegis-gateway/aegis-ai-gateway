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

// TestNoPayload_AllowPathCanaryEndToEnd is the allow-path counterpart to
// TestNoPayload_CanaryEndToEnd.
//
// Until request_complete existed, audit_events held only refusals, and the
// canary covered exactly one path: secrets block, 451, filter_block row. Allows
// are now the majority of what gets sealed, and were the untested majority.
//
// The positive control is the load-bearing half. A test that asserts only "the
// canary is absent" passes against an empty table, and the entire point of this
// phase is that the row must EXIST. So this polls for the request_complete row
// first and fails if it never appears, and only then checks for payload.
//
// Requires TEST_DATABASE_URL, TEST_SERVER_URL, TEST_API_KEY and a gateway
// started with AEGIS_MOCK_PROVIDER=true. It fails rather than skips when they
// are absent, for the reason the sibling test gives at length.
func TestNoPayload_AllowPathCanaryEndToEnd(t *testing.T) {
	runAllowCanary(t, false)
}

// TestNoPayload_StreamingAllowPathCanaryEndToEnd is the same assertion for the
// streaming path.
//
// It is a separate test rather than a subtest because streaming is a separate
// code path with its own completion handling and its own emit point, and the
// pair of cost defects that motivated this work were both "the non-streaming
// path was corrected and the streaming path was not". CI greps for both names.
func TestNoPayload_StreamingAllowPathCanaryEndToEnd(t *testing.T) {
	runAllowCanary(t, true)
}

func runAllowCanary(t *testing.T, stream bool) {
	t.Helper()

	if os.Getenv("AEGIS_SKIP_INTEGRATION") == "1" {
		t.Skip("explicit opt-out: AEGIS_SKIP_INTEGRATION=1")
	}

	dbURL := os.Getenv("TEST_DATABASE_URL")
	serverURL := os.Getenv("TEST_SERVER_URL")
	apiKey := os.Getenv("TEST_API_KEY")

	var missing []string
	for _, v := range []struct{ name, value string }{
		{"TEST_DATABASE_URL", dbURL},
		{"TEST_SERVER_URL", serverURL},
		{"TEST_API_KEY", apiKey},
	} {
		if v.value == "" {
			missing = append(missing, v.name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("allow-path canary cannot run: %s not set.\n"+
			"This test backs the claim that the decision record covers permitted "+
			"requests, so a missing stack fails rather than skips. Start one "+
			"(mise run services:up, then run the gateway with AEGIS_MOCK_PROVIDER=true) "+
			"and set all three, or opt out deliberately with AEGIS_SKIP_INTEGRATION=1.",
			strings.Join(missing, ", "))
	}

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to database: %v", err)
	}
	// Ordering is load-bearing; see the sibling test for why this is t.Cleanup
	// registered before Serialise rather than a defer.
	t.Cleanup(pool.Close)

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping database: %v", err)
	}
	audittest.Serialise(t, pool)

	// A canary distinct from the refusal test's, so a stale row from that test
	// cannot satisfy or fail this one.
	canary := "CANARY_ALLOW_PAYLOAD_5d1c93af07be24"
	kind := "buffered"
	if stream {
		canary = "CANARY_STREAM_PAYLOAD_b70e4f28a1c956"
		kind = "stream"
	}

	requestID := fmt.Sprintf("req_allow_%s_%d", kind, time.Now().UnixNano())

	// Benign content: this request must be PERMITTED. Anything the filters
	// catch would produce a filter_block and test the path already covered.
	reqBody, _ := json.Marshal(map[string]any{
		"model":    canaryModel,
		"stream":   stream,
		"messages": []map[string]string{{"role": "user", "content": canary + " please summarise this sentence."}},
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
	_ = resp.Body.Close()

	// 1. The request must have been ALLOWED. A refusal would exercise the path
	//    the sibling test already covers and would prove nothing about allows.
	//    Requires the gateway to run with AEGIS_MOCK_PROVIDER=true; without it
	//    this is a provider call that will not succeed.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 for a benign %s request, got %d — the request was "+
			"not permitted, so this test cannot prove anything about the allow path. "+
			"Is the gateway running with AEGIS_MOCK_PROVIDER=true? body: %s",
			kind, resp.StatusCode, body)
	}
	t.Logf("gateway permitted the %s request with HTTP 200", kind)

	// 2. Positive control: the audit write is asynchronous, so poll.
	deadline := time.Now().Add(10 * time.Second)
	var found int
	for time.Now().Before(deadline) {
		err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM audit_events WHERE request_id = $1 AND event_type = 'request_complete'`,
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
		t.Fatalf("no request_complete audit row was written for permitted request %q. "+
			"This is the failure this test exists to catch: the request succeeded and "+
			"left no attested record, so the decision record is incomplete", requestID)
	}
	t.Logf("positive control satisfied: %d request_complete row(s) written for %s", found, requestID)

	// Exactly one. Two events for one request would double-count every
	// permitted request in the sealed record, and the streaming path has
	// several exits that could each emit.
	if found != 1 {
		t.Errorf("expected exactly 1 request_complete row for %s, got %d — a request "+
			"must be attested once", requestID, found)
	}

	// The event has to carry the routing facts, or it attests only that
	// something happened.
	var provider, model, mode string
	var status int
	err = pool.QueryRow(ctx, `
		SELECT COALESCE(provider,''), COALESCE(model,''), COALESCE(mode,''), COALESCE(status_code,0)
		  FROM audit_events WHERE request_id = $1 AND event_type = 'request_complete'`,
		requestID).Scan(&provider, &model, &mode, &status)
	if err != nil {
		t.Fatalf("reading the allow event: %v", err)
	}
	if provider == "" {
		t.Error("the allow event names no provider")
	}
	if model != canaryModel {
		t.Errorf("allow event model = %q, want the requested alias %q", model, canaryModel)
	}
	if mode != kind {
		t.Errorf("allow event mode = %q, want %q", mode, kind)
	}
	if status != http.StatusOK {
		t.Errorf("allow event status_code = %d, want 200", status)
	}
	t.Logf("allow event: provider=%s model=%s mode=%s status=%d", provider, model, mode, status)

	// 3. Only now is absence meaningful: the audit path demonstrably ran for
	//    this permitted request, and the payload still must not be in it.
	for _, table := range []string{"audit_logs", "audit_events"} {
		assertCanaryAbsent(ctx, t, pool, table, canary)
	}

	if !t.Failed() {
		t.Logf("allow path confirmed: request_complete row written for %s, canary %q absent from all audit rows",
			requestID, canary)
	}
}
