// +build integration

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/af-corp/aegis-gateway/internal/auth"
	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/cost"
	"github.com/af-corp/aegis-gateway/internal/filter"
	"github.com/af-corp/aegis-gateway/internal/ratelimit"
	"github.com/af-corp/aegis-gateway/internal/retry"
	"github.com/af-corp/aegis-gateway/internal/router"
	"github.com/af-corp/aegis-gateway/internal/router/adapters"
	"github.com/af-corp/aegis-gateway/internal/storage"
	"github.com/af-corp/aegis-gateway/internal/telemetry"
	"github.com/af-corp/aegis-gateway/internal/types"
	"github.com/af-corp/aegis-gateway/internal/validation"
	
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
}

// MockProviderServer mocks an LLM provider API.
type MockProviderServer struct {
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
						Content: "Hello! How can I help you?",
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
		mock.Requests = append(mock.Requests, r)

		if mock.ResponseDelay > 0 {
			time.Sleep(mock.ResponseDelay)
		}

		if mock.ShouldFail {
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
			
			for _, chunk := range mock.StreamChunks {
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
		w.WriteHeader(mock.StatusCode)
		json.NewEncoder(w).Encode(mock.Response)
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

	// Setup mock provider
	mockProvider := NewMockProviderServer()

	// Setup configuration
	cfg := &config.Config{
		Server: config.ServerConfig{
			Port: 8080,
		},
		RateLimit: config.RateLimitConfig{
			DefaultRequestsPerMinute: 100,
		},
		Retry: config.RetryConfig{
			MaxAttempts:     3,
			InitialBackoff:  100 * time.Millisecond,
			MaxBackoff:      5 * time.Second,
			BackoffMultiplier: 2.0,
			Jitter:          0.1,
		},
		Validation: config.ValidationConfig{
			MaxModelLength:      100,
			MaxMessagesCount:    1000,
			MaxMessageLength:    50000,
			MaxTemperature:      2.0,
			MinTemperature:      0.0,
			MaxTopP:             1.0,
			MinTopP:             0.0,
			MaxTokens:           100000,
			MaxStopSequences:    4,
			MaxStopSequenceLength: 100,
		},
	}

	modelsCfg := &config.ModelsConfig{
		Models: map[string]config.ModelMapping{
			"gpt-4": {
				Provider: "openai",
				Model:    "gpt-4",
			},
		},
	}

	// Setup components
	metrics := telemetry.NewMetrics()
	costCalc := cost.NewCalculator(modelsCfg)
	usageRecorder := storage.NewUsageRecorder(db)
	
	// Setup providers registry with mock provider
	registry := router.NewRegistry()
	mockAdapter := &mockProviderAdapter{
		name:   "openai",
		url:    mockProvider.Server.URL,
		client: http.DefaultClient,
	}
	registry.Register("openai", mockAdapter)
	
	healthTracker := router.NewHealthTracker()
	filterChain := filter.NewChain()
	retryExecutor := retry.NewExecutor(cfg.Retry, metrics)
	contextMonitor := retry.NewContextMonitor(metrics)
	validator := validation.NewValidator(cfg.Validation, metrics)

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
		db.Close()
		redisClient.Close()
		mockProvider.Server.Close()
	}

	return &TestEnv{
		DB:           db,
		Redis:        redisClient,
		MockProvider: mockProvider,
		Handler:      handler,
		Metrics:      metrics,
		Cleanup:      cleanup,
	}
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
		OrganizationID:    "test-org",
		TeamID:            "test-team",
		UserID:            "test-user",
		KeyID:             "test-key",
		MaxClassification: auth.ClassificationPublic,
		AllowedModels:     []string{"gpt-4"},
	}
	ctx := auth.NewContextWithAuth(req.Context(), authInfo)
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
	if len(env.MockProvider.Requests) != 1 {
		t.Errorf("Expected 1 provider request, got %d", len(env.MockProvider.Requests))
	}
}

// TestStreamingRequest tests streaming functionality.
func TestStreamingRequest(t *testing.T) {
	env := SetupTestEnv(t)
	defer env.Cleanup()

	// Setup streaming chunks
	env.MockProvider.StreamChunks = []string{
		`{"model":"gpt-4","choices":[{"delta":{"content":"Hello"}}]}`,
		`{"model":"gpt-4","choices":[{"delta":{"content":" world"}}]}`,
		`{"model":"gpt-4","usage":{"prompt_tokens":10,"completion_tokens":8,"total_tokens":18}}`,
	}

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
		OrganizationID: "test-org",
		TeamID:         "test-team",
		KeyID:          "test-key",
	}
	ctx := auth.NewContextWithAuth(req.Context(), authInfo)
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
	body = w.Body.String()
	if !bytes.Contains([]byte(body), []byte("Hello")) {
		t.Error("Expected streaming response to contain 'Hello'")
	}
	if !bytes.Contains([]byte(body), []byte("[DONE]")) {
		t.Error("Expected streaming response to end with [DONE]")
	}
}

// TestProviderFailure tests provider error handling.
func TestProviderFailure(t *testing.T) {
	env := SetupTestEnv(t)
	defer env.Cleanup()

	// Configure provider to fail
	env.MockProvider.ShouldFail = true

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
		OrganizationID: "test-org",
	}
	ctx := auth.NewContextWithAuth(req.Context(), authInfo)
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
				OrganizationID: "test-org",
			}
			ctx := auth.NewContextWithAuth(req.Context(), authInfo)
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
				OrganizationID: "test-org",
			}
			ctx := auth.NewContextWithAuth(req.Context(), authInfo)
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
	if len(env.MockProvider.Requests) != concurrency {
		t.Errorf("Expected %d provider requests, got %d", concurrency, len(env.MockProvider.Requests))
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
		OrganizationID: "test-org",
	}
	ctx := auth.NewContextWithAuth(req.Context(), authInfo)
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
