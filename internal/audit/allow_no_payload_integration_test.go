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
	"bufio"
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

// allowCanaryValue is embedded in a request that is permitted and served. It
// must appear in no audit row afterwards.
//
// Distinct from canaryValue in no_payload_integration_test.go so that a failure
// names which path leaked. The two tests run in the same package against the
// same database.
const allowCanaryValue = "CANARY_ALLOW_PAYLOAD_6b23f01d9e"

// allowCanaryModel routes through the mock provider under AEGIS_MOCK_PROVIDER,
// so the request is served without a provider credential and without leaving
// the machine.
const allowCanaryModel = "aegis-fast"

// TestNoPayload_AllowPathCanary is the allow-path counterpart of
// TestNoPayload_CanaryEndToEnd.
//
// It exists because the allow path became the majority of what gets sealed.
// Before request_complete events existed, audit_events held refusals only and
// this test would have had nothing to look for. Now nearly every row in a
// healthy deployment is one of these, and the retention claim rests on them as
// much as on the denial rows.
//
// The shape is the block canary's, and the positive control is the same
// load-bearing step: assert the row exists before concluding the canary is
// absent, because "absent from an empty table" proves nothing.
func TestNoPayload_AllowPathCanary(t *testing.T) {
	if os.Getenv("AEGIS_SKIP_INTEGRATION") == "1" {
		t.Skip("explicit opt-out: AEGIS_SKIP_INTEGRATION=1")
	}

	pool, serverURL, apiKey := allowCanaryStack(t)
	ctx := context.Background()
	audittest.Serialise(t, pool)

	requestID := fmt.Sprintf("req_allow_canary_%d", time.Now().UnixNano())

	body, err := json.Marshal(map[string]any{
		"model": allowCanaryModel,
		"messages": []map[string]string{{
			"role":    "user",
			"content": allowCanaryValue + " summarise this sentence please",
		}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp := postCanary(ctx, t, serverURL, apiKey, requestID, body)
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// 1. The request must actually have been served. A refusal would exercise
	//    the denial path this test is not about, and the canary would be
	//    trivially absent from a request_complete row that was never written.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 so that the allow path runs, got %d; body: %s",
			resp.StatusCode, respBody)
	}

	// 2. Positive control: the attesting row must exist.
	assertAuditRow(ctx, t, pool, requestID, "request_complete")

	// 3. Only now is absence meaningful.
	for _, table := range []string{"audit_logs", "audit_events"} {
		assertCanaryAbsent(ctx, t, pool, table, allowCanaryValue)
	}

	// 4. The response text must not be in the trail either. The mock provider's
	//    canned reply is the only response content this test can name, and a
	//    completion event that recorded what the model said would be the same
	//    violation from the other direction.
	assertCanaryAbsent(ctx, t, pool, "audit_events", "canned response")

	if !t.Failed() {
		t.Logf("allow path confirmed: request_complete row written for %s, canary %q absent",
			requestID, allowCanaryValue)
	}
}

// TestNoPayload_AllowPathCanaryStreaming is the same assertion against the
// streaming path, which has its own completion accounting, its own six exits,
// and its own audit emit. A guarantee that holds only on the non-streaming path
// is not a guarantee.
func TestNoPayload_AllowPathCanaryStreaming(t *testing.T) {
	if os.Getenv("AEGIS_SKIP_INTEGRATION") == "1" {
		t.Skip("explicit opt-out: AEGIS_SKIP_INTEGRATION=1")
	}

	pool, serverURL, apiKey := allowCanaryStack(t)
	ctx := context.Background()
	audittest.Serialise(t, pool)

	requestID := fmt.Sprintf("req_allow_stream_canary_%d", time.Now().UnixNano())

	body, err := json.Marshal(map[string]any{
		"model":  allowCanaryModel,
		"stream": true,
		"messages": []map[string]string{{
			"role":    "user",
			"content": allowCanaryValue + " summarise this sentence please",
		}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp := postCanary(ctx, t, serverURL, apiKey, requestID, body)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("expected HTTP 200 from the streaming path, got %d; body: %s", resp.StatusCode, b)
	}

	// Drain to completion. The audit event is emitted after the stream ends, so
	// a test that hung up early would be exercising the disconnect path.
	var chunks int
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data:") {
			chunks++
		}
	}
	resp.Body.Close()
	if chunks == 0 {
		t.Fatal("the stream carried no data chunks, so the completion path did not run")
	}

	assertAuditRow(ctx, t, pool, requestID, "request_complete")

	for _, table := range []string{"audit_logs", "audit_events"} {
		assertCanaryAbsent(ctx, t, pool, table, allowCanaryValue)
	}
	assertCanaryAbsent(ctx, t, pool, "audit_events", "canned response")

	if !t.Failed() {
		t.Logf("streaming allow path confirmed: %d chunk(s), request_complete row written for %s, "+
			"canary %q absent", chunks, requestID, allowCanaryValue)
	}
}

// allowCanaryStack resolves the stack these tests need, failing rather than
// skipping when it is absent, for the reason given on TestNoPayload_CanaryEndToEnd:
// a conformance test that can silently not run manufactures confidence.
func allowCanaryStack(t *testing.T) (*pgxpool.Pool, string, string) {
	t.Helper()

	dbURL := os.Getenv("TEST_DATABASE_URL")
	serverURL := os.Getenv("TEST_SERVER_URL")
	apiKey := os.Getenv("TEST_API_KEY")

	var missing []string
	for _, v := range []struct{ name, value string }{
		{"TEST_DATABASE_URL", dbURL}, {"TEST_SERVER_URL", serverURL}, {"TEST_API_KEY", apiKey},
	} {
		if v.value == "" {
			missing = append(missing, v.name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("allow-path canary cannot run: %s not set. This test backs the retention "+
			"claim on the majority of sealed rows, so a missing stack fails rather than skips. "+
			"Opt out deliberately with AEGIS_SKIP_INTEGRATION=1.", strings.Join(missing, ", "))
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping database: %v", err)
	}
	return pool, serverURL, apiKey
}

func postCanary(ctx context.Context, t *testing.T, serverURL, apiKey, requestID string, body []byte) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(serverURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
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
	return resp
}

// assertAuditRow is the positive control: it polls for the attesting row and
// fails if it never appears. The audit writer is asynchronous, so this polls
// rather than sleeping a fixed interval.
func assertAuditRow(ctx context.Context, t *testing.T, pool *pgxpool.Pool, requestID, eventType string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	var found int
	for time.Now().Before(deadline) {
		err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM audit_events WHERE request_id = $1 AND event_type = $2`,
			requestID, eventType).Scan(&found)
		if err != nil {
			t.Fatalf("querying audit_events for the positive control: %v", err)
		}
		if found > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if found == 0 {
		t.Fatalf("no %s audit row was written for request %q. Either the allow path is not "+
			"attested, in which case the sealed chain says nothing about permitted traffic, "+
			"or it is and this test cannot tell; both make the canary below meaningless",
			eventType, requestID)
	}
	// Exactly one. A duplicate would double-count every permitted request in
	// the evidence and inflate the sealed chain.
	if found != 1 {
		t.Errorf("%d %s rows were written for request %q, want exactly 1", found, eventType, requestID)
	}
	t.Logf("positive control satisfied: 1 %s row for %s", eventType, requestID)
}
