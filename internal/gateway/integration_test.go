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
// +build integration

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/cost"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/filter"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/retry"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/storage"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/telemetry"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/validation"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// TestEnv holds the integration test environment.
type TestEnv struct {
	DB           *pgxpool.Pool
	Redis        *redis.Client
	MockProvider *MockProviderServer
	Handler      *Handler
	Metrics      *telemetry.Metrics
	Cleanup      func()

	// OrgID is unique per environment; use it as the organization on auth
	// contexts so usage rows can be attributed back to this test.
	OrgID string

	// KeyID is the seeded api_keys row. Auth contexts must carry it: usage
	// records reference it by foreign key.
	KeyID string
}

// MockProviderServer mocks an LLM provider API.
//
// The handler runs on httptest's own goroutines, one per connection, so every
// field it touches is shared with the test that configured it. mu guards them.
//
// Without it TestConcurrentRequests lost appends: ten requests arrived and nine
// were recorded, intermittently, because two handlers read and wrote the slice
// header at once. The race detector reports it, and it failed roughly one run
// in eight without.
type MockProviderServer struct {
	mu sync.Mutex

	Server        *httptest.Server
	Requests      []*http.Request
	Response      *types.AegisResponse
	StreamChunks  []string
	StatusCode    int
	ResponseDelay time.Duration
	ShouldFail    bool
}

// NewMockProviderServer creates a new mock provider server.
func NewMockProviderServer() *MockProviderServer {
	mock := &MockProviderServer{
		StatusCode: http.StatusOK,
		Response: &types.AegisResponse{
			Model:    "gpt-4",
			Provider: "openai",
			Choices: []types.Choice{
				{
					Message: types.Message{
						Role:    "assistant",
						Content: types.TextContent("Hello! How can I help you?"),
					},
					FinishReason: "stop",
				},
			},
			Usage: types.Usage{
				PromptTokens:     10,
				CompletionTokens: 8,
				TotalTokens:      18,
			},
		},
	}

	mock.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Snapshot everything under the lock, then serve without holding it, so
		// a ResponseDelay cannot serialise concurrent requests and turn the
		// concurrency test into a sequential one.
		mock.mu.Lock()
		mock.Requests = append(mock.Requests, r)
		delay := mock.ResponseDelay
		shouldFail := mock.ShouldFail
		chunks := append([]string(nil), mock.StreamChunks...)
		statusCode := mock.StatusCode
		response := mock.Response
		mock.mu.Unlock()

		if delay > 0 {
			time.Sleep(delay)
		}

		if shouldFail {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error": {"message": "Internal server error"}}`))
			return
		}

		// Check if streaming request
		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)

		if stream, ok := req["stream"].(bool); ok && stream {
			// Return SSE stream
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)

			for _, chunk := range chunks {
				fmt.Fprintf(w, "data: %s\n\n", chunk)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				time.Sleep(10 * time.Millisecond)
			}
			fmt.Fprintf(w, "data: [DONE]\n\n")
			return
		}

		// Return non-streaming response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		json.NewEncoder(w).Encode(response)
	}))

	return mock
}

// SetupTestEnv creates a test environment with all dependencies.
func SetupTestEnv(t *testing.T) *TestEnv {
	t.Helper()

	// Setup PostgreSQL (use testcontainers or local instance)
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/aegis_test?sslmode=disable"
	}

	db, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}
	// pgxpool.New is lazy: it does not open a connection. Without this ping a
	// missing database would show up only as a usage-write error inside a
	// goroutine, and every assertion below would still pass.
	if err := db.Ping(context.Background()); err != nil {
		t.Fatalf("Failed to reach test database at %s: %v", dbURL, err)
	}

	// Setup Redis (use testcontainers or local instance)
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		redisURL = "localhost:6379"
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr: redisURL,
	})

	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("Failed to connect to test Redis: %v", err)
	}

	// Each env gets its own organization id so that a usage-row lookup matches
	// only this test's writes, even against a database other runs have used.
	orgID := fmt.Sprintf("test-org-%d", time.Now().UnixNano())

	// Seed the API key the requests authenticate as. usage_records.api_key_id
	// is a uuid with a foreign key onto api_keys, so the old fixture's
	// KeyID: "test-key" could never have produced a usage row — the insert
	// failed inside RecordUsage's goroutine where nothing was watching.
	var keyID string
	err = db.QueryRow(context.Background(), `
		INSERT INTO api_keys (key_hash, key_prefix, organization_id, team_id, user_id,
		                      name, max_classification, allowed_models, expires_at)
		VALUES ($1, 'aegis-test', $2, 'test-team', 'test-user',
		        'gateway integration test', 'INTERNAL', '["gpt-4"]', now() + interval '1 hour')
		RETURNING id`,
		fmt.Sprintf("%064x", time.Now().UnixNano()), orgID,
	).Scan(&keyID)
	if err != nil {
		t.Fatalf("Failed to seed test API key: %v", err)
	}

	// Setup mock provider
	mockProvider := NewMockProviderServer()

	// Setup configuration.
	//
	// This block previously named config.RateLimitConfig, config.RetryConfig
	// and config.ValidationConfig, none of which exist. Retry and validation
	// limits are not part of config.Config: retry takes a retry.Config and
	// validation takes validation.Limits. The file had been written against an
	// earlier shape and never rebuilt under the integration tag, so it stopped
	// compiling silently.
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080},
	}

	modelsCfg := &config.ModelsConfig{
		Models: map[string]config.ModelMapping{
			"gpt-4": {
				Primary: config.ProviderRoute{
					Provider:              "openai",
					Model:                 "gpt-4",
					ClassificationCeiling: "RESTRICTED",
				},
			},
		},
	}

	pricingCfg := &config.PricingConfig{
		Providers: map[string]config.ProviderPricing{
			"openai": {Models: map[string]config.PriceEntry{
				"gpt-4": {Input: 30, Output: 60},
			}},
		},
	}

	// Setup components.
	//
	// getTestMetrics() rather than telemetry.NewMetrics(): the metrics are
	// registered on the default Prometheus registry, so a second SetupTestEnv
	// in the same binary panics with "duplicate metrics collector registration".
	metrics := getTestMetrics()
	costCalc := cost.NewCalculator(func() *config.PricingConfig { return pricingCfg })
	usageRecorder := storage.NewUsageRecorder(db)

	// Setup providers registry with mock provider
	registry := router.NewRegistry()
	mockAdapter := &mockProviderAdapter{
		name:   "openai",
		url:    mockProvider.Server.URL,
		client: http.DefaultClient,
	}
	registry.Register("openai", mockAdapter)

	healthTracker := router.NewHealthTracker(5, 15*time.Second)
	filterChain := filter.NewChain()
	retryExecutor := retry.NewExecutor(retry.Config{
		MaxRetries:        3,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        5 * time.Second,
		BackoffMultiplier: 2.0,
		JitterFraction:    0.1,
	}, metrics)
	contextMonitor := retry.NewContextMonitor(metrics)
	validator := validation.NewValidator(validation.DefaultLimits(), metrics)

	// Create handler
	handler := NewHandler(
		registry,
		healthTracker,
		func() *config.ModelsConfig { return modelsCfg },
		func() *config.Config { return cfg },
		filterChain,
		nil, // policyEvaluator
		metrics,
		costCalc,
		usageRecorder,
		nil, // auditLogger
		retryExecutor,
		contextMonitor,
		validator,
	)

	cleanup := func() {
		// Usage recording is fire-and-forget (storage.RecordUsage spawns a
		// goroutine), so a write may still be in flight here and lose the race
		// with db.Close(). Tests that care call WaitForUsageRecord first; the
		// rest may log "closed pool", which is the recorder behaving as
		// designed rather than a failure.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		// usage_records cascades from api_keys, so this clears both.
		if _, err := db.Exec(ctx, "DELETE FROM api_keys WHERE id = $1", keyID); err != nil {
			t.Logf("could not clean up seeded API key %s: %v", keyID, err)
		}
		cancel()
		db.Close()
		redisClient.Close()
		mockProvider.Server.Close()
	}

	return &TestEnv{
		OrgID:        orgID,
		KeyID:        keyID,
		DB:           db,
		Redis:        redisClient,
		MockProvider: mockProvider,
		Handler:      handler,
		Metrics:      metrics,
		Cleanup:      cleanup,
	}
}

// WaitForUsageRecord blocks until a usage row exists for this env's org, or
// the deadline passes. RecordUsage returns before the row is written, so
// asserting immediately after the handler returns would be a coin flip.
func (e *TestEnv) WaitForUsageRecord(t *testing.T) storage.UsageRecord {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var rec storage.UsageRecord
		err := e.DB.QueryRow(context.Background(), `
			SELECT model_requested, model_served, provider,
			       prompt_tokens, completion_tokens, total_tokens,
			       estimated_cost_usd, status_code, stream
			  FROM usage_records
			 WHERE organization_id = $1`, e.OrgID).Scan(
			&rec.ModelRequested, &rec.ModelServed, &rec.Provider,
			&rec.PromptTokens, &rec.CompletionTokens, &rec.TotalTokens,
			&rec.EstimatedCostUSD, &rec.StatusCode, &rec.Stream,
		)
		if err == nil {
			rec.OrganizationID = e.OrgID
			return rec
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("querying usage_records: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("no usage row was written within 5s")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// RequestCount reports how many requests the mock has served.
//
// Reading len(Requests) directly races the handler, which is what the
// concurrency test was doing while asserting on the very field being mutated.
func (m *MockProviderServer) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Requests)
}

// SetStreamChunks and SetShouldFail configure the mock from a test goroutine
// while the handler may already be serving.
func (m *MockProviderServer) SetStreamChunks(chunks []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.StreamChunks = chunks
}

func (m *MockProviderServer) SetShouldFail(fail bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ShouldFail = fail
}

// mockProviderAdapter implements ProviderAdapter for testing.
type mockProviderAdapter struct {
	name   string
	url    string
	client *http.Client
}

func (m *mockProviderAdapter) Name() string {
	return m.name
}

func (m *mockProviderAdapter) TransformRequest(ctx context.Context, req *types.AegisRequest) (*http.Request, error) {
	body, _ := json.Marshal(req)
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", m.url+"/v1/chat/completions", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	return httpReq, nil
}

func (m *mockProviderAdapter) SendRequest(req *http.Request) (*http.Response, error) {
	return m.client.Do(req)
}

func (m *mockProviderAdapter) TransformResponse(ctx context.Context, resp *http.Response) (*types.AegisResponse, error) {
	var aegisResp types.AegisResponse
	if err := json.NewDecoder(resp.Body).Decode(&aegisResp); err != nil {
		return nil, err
	}
	aegisResp.Provider = m.name
	return &aegisResp, nil
}

func (m *mockProviderAdapter) TransformStreamChunk(chunk []byte) ([]byte, error) {
	return chunk, nil
}

// SupportsStreaming reports true: TestStreamingRequest drives the streaming
// path through this adapter, and the handler consults this before taking it.
func (m *mockProviderAdapter) SupportsStreaming() bool {
	return true
}

// SupportsTools reports false: none of these scenarios send tool definitions,
// and claiming tool support the mock does not implement would let a
// tool-stripping regression pass here unnoticed.
func (m *mockProviderAdapter) SupportsTools() bool {
	return false
}

// TestFullRequestLifecycle tests the complete request flow.
func TestFullRequestLifecycle(t *testing.T) {
	env := SetupTestEnv(t)
	defer env.Cleanup()

	// Create test request
	reqBody := map[string]interface{}{
		"model": "gpt-4",
		"messages": []map[string]string{
			{"role": "user", "content": "Hello, world!"},
		},
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "test-req-123")

	// Set auth context
	authInfo := &auth.AuthInfo{
		OrganizationID:    env.OrgID,
		TeamID:            "test-team",
		UserID:            "test-user",
		KeyID:             env.KeyID,
		MaxClassification: types.ClassPublic,
		AllowedModels:     []string{"gpt-4"},
	}
	ctx := auth.ContextWithAuth(req.Context(), authInfo)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	// Execute request
	env.Handler.ChatCompletions(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var response types.AegisResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify response content
	if response.Model != "gpt-4" {
		t.Errorf("Expected model gpt-4, got %s", response.Model)
	}
	if response.Provider != "openai" {
		t.Errorf("Expected provider openai, got %s", response.Provider)
	}
	if len(response.Choices) == 0 {
		t.Fatal("Expected at least one choice")
	}
	if response.Usage.TotalTokens == 0 {
		t.Error("Expected non-zero token usage")
	}
	if response.EstimatedCostUSD == 0 {
		t.Error("Expected non-zero cost estimate")
	}

	// Verify provider received request
	if env.MockProvider.RequestCount() != 1 {
		t.Errorf("Expected 1 provider request, got %d", env.MockProvider.RequestCount())
	}

	// The usage row is the reason this test needs a database at all. Without
	// this assertion the whole harness could run against a dead Postgres and
	// still report success, which is how it went unnoticed that the file had
	// stopped compiling.
	rec := env.WaitForUsageRecord(t)
	if rec.ModelServed != "gpt-4" || rec.Provider != "openai" {
		t.Errorf("usage row recorded %s/%s, want gpt-4/openai", rec.Provider, rec.ModelServed)
	}
	if rec.TotalTokens != response.Usage.TotalTokens {
		t.Errorf("usage row has %d total tokens, response reported %d",
			rec.TotalTokens, response.Usage.TotalTokens)
	}
	if rec.EstimatedCostUSD != response.EstimatedCostUSD {
		t.Errorf("usage row has cost %v, response reported %v",
			rec.EstimatedCostUSD, response.EstimatedCostUSD)
	}
	if rec.StatusCode != http.StatusOK {
		t.Errorf("usage row has status %d, want 200", rec.StatusCode)
	}
	if rec.Stream {
		t.Error("usage row marked as streaming for a non-streaming request")
	}
}

// TestStreamingRequest tests streaming functionality.
func TestStreamingRequest(t *testing.T) {
	env := SetupTestEnv(t)
	defer env.Cleanup()

	// Setup streaming chunks
	env.MockProvider.SetStreamChunks([]string{
		`{"model":"gpt-4","choices":[{"delta":{"content":"Hello"}}]}`,
		`{"model":"gpt-4","choices":[{"delta":{"content":" world"}}]}`,
		`{"model":"gpt-4","usage":{"prompt_tokens":10,"completion_tokens":8,"total_tokens":18}}`,
	})

	// Create streaming request
	reqBody := map[string]interface{}{
		"model":  "gpt-4",
		"stream": true,
		"messages": []map[string]string{
			{"role": "user", "content": "Hello!"},
		},
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("X-Request-ID", "test-stream-123")

	authInfo := &auth.AuthInfo{
		OrganizationID: env.OrgID,
		TeamID:         "test-team",
		KeyID:          env.KeyID,
	}
	ctx := auth.ContextWithAuth(req.Context(), authInfo)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	// Execute streaming request
	env.Handler.ChatCompletions(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "text/event-stream" {
		t.Errorf("Expected Content-Type text/event-stream, got %s", contentType)
	}

	// Verify streaming chunks were sent
	streamed := w.Body.String()
	if !strings.Contains(streamed, "Hello") {
		t.Error("Expected streaming response to contain 'Hello'")
	}
	if !strings.Contains(streamed, "[DONE]") {
		t.Error("Expected streaming response to end with [DONE]")
	}
}

// TestProviderFailure tests provider error handling.
func TestProviderFailure(t *testing.T) {
	env := SetupTestEnv(t)
	defer env.Cleanup()

	// Configure provider to fail
	env.MockProvider.SetShouldFail(true)

	reqBody := map[string]interface{}{
		"model": "gpt-4",
		"messages": []map[string]string{
			{"role": "user", "content": "Test"},
		},
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("X-Request-ID", "test-fail-123")

	authInfo := &auth.AuthInfo{
		OrganizationID: env.OrgID,
	}
	ctx := auth.ContextWithAuth(req.Context(), authInfo)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	// Execute request
	env.Handler.ChatCompletions(w, req)

	// Verify error response
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", w.Code)
	}
}

// TestValidationFailure tests input validation.
func TestValidationFailure(t *testing.T) {
	env := SetupTestEnv(t)
	defer env.Cleanup()

	tests := []struct {
		name       string
		request    map[string]interface{}
		expectCode int
	}{
		{
			name: "missing model",
			request: map[string]interface{}{
				"messages": []map[string]string{
					{"role": "user", "content": "Test"},
				},
			},
			expectCode: http.StatusBadRequest,
		},
		{
			name: "missing messages",
			request: map[string]interface{}{
				"model":    "gpt-4",
				"messages": []map[string]string{},
			},
			expectCode: http.StatusBadRequest,
		},
		{
			name: "invalid temperature",
			request: map[string]interface{}{
				"model":       "gpt-4",
				"temperature": 3.0, // > max 2.0
				"messages": []map[string]string{
					{"role": "user", "content": "Test"},
				},
			},
			expectCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.request)
			req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("X-Request-ID", "test-validation-"+tt.name)

			authInfo := &auth.AuthInfo{
				OrganizationID: env.OrgID,
			}
			ctx := auth.ContextWithAuth(req.Context(), authInfo)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			env.Handler.ChatCompletions(w, req)

			if w.Code != tt.expectCode {
				t.Errorf("Expected status %d, got %d: %s", tt.expectCode, w.Code, w.Body.String())
			}
		})
	}
}

// TestConcurrentRequests tests handling of multiple simultaneous requests.
func TestConcurrentRequests(t *testing.T) {
	env := SetupTestEnv(t)
	defer env.Cleanup()

	concurrency := 10
	done := make(chan bool, concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			reqBody := map[string]interface{}{
				"model": "gpt-4",
				"messages": []map[string]string{
					{"role": "user", "content": fmt.Sprintf("Request %d", id)},
				},
			}
			body, _ := json.Marshal(reqBody)

			req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("X-Request-ID", fmt.Sprintf("concurrent-%d", id))

			authInfo := &auth.AuthInfo{
				OrganizationID: env.OrgID,
			}
			ctx := auth.ContextWithAuth(req.Context(), authInfo)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			env.Handler.ChatCompletions(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Request %d failed with status %d", id, w.Code)
			}

			done <- true
		}(i)
	}

	// Wait for all requests to complete
	timeout := time.After(30 * time.Second)
	for i := 0; i < concurrency; i++ {
		select {
		case <-done:
			// Request completed
		case <-timeout:
			t.Fatal("Timeout waiting for concurrent requests")
		}
	}

	// Verify all requests reached provider
	if env.MockProvider.RequestCount() != concurrency {
		t.Errorf("Expected %d provider requests, got %d", concurrency, env.MockProvider.RequestCount())
	}
}

// TestRetryLogic tests automatic retry on transient failures.
func TestRetryLogic(t *testing.T) {
	env := SetupTestEnv(t)
	defer env.Cleanup()

	// Configure provider to fail first attempt, then succeed
	attemptCount := 0
	env.MockProvider.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++
		if attemptCount == 1 {
			// First attempt fails with 503
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error": {"message": "Service unavailable"}}`))
			return
		}
		// Second attempt succeeds
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(env.MockProvider.Response)
	})

	reqBody := map[string]interface{}{
		"model": "gpt-4",
		"messages": []map[string]string{
			{"role": "user", "content": "Test retry"},
		},
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("X-Request-ID", "test-retry-123")

	authInfo := &auth.AuthInfo{
		OrganizationID: env.OrgID,
	}
	ctx := auth.ContextWithAuth(req.Context(), authInfo)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	// Execute request
	env.Handler.ChatCompletions(w, req)

	// Verify success after retry
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 after retry, got %d", w.Code)
	}

	// Verify retry happened
	if attemptCount != 2 {
		t.Errorf("Expected 2 attempts (1 failure + 1 retry), got %d", attemptCount)
	}
}
