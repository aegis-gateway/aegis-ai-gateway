package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/af-corp/aegis-gateway/internal/auth"
	"github.com/af-corp/aegis-gateway/internal/telemetry"
	"github.com/af-corp/aegis-gateway/internal/types"
)

var (
	testMetricsOnce sync.Once
	testMetrics     *telemetry.Metrics
)

func getTestMetrics() *telemetry.Metrics {
	testMetricsOnce.Do(func() {
		testMetrics = telemetry.NewMetrics()
	})
	return testMetrics
}

// mockAdapter implements ProviderAdapter for testing.
type mockStreamAdapter struct {
	name      string
	response  *http.Response
	sendError error
}

func (m *mockStreamAdapter) Name() string {
	return m.name
}

func (m *mockStreamAdapter) TransformRequest(ctx context.Context, req *types.AegisRequest) (*http.Request, error) {
	httpReq, _ := http.NewRequest("POST", "http://mock-provider.com/v1/chat", nil)
	return httpReq.WithContext(ctx), nil
}

func (m *mockStreamAdapter) SendRequest(req *http.Request) (*http.Response, error) {
	if m.sendError != nil {
		return nil, m.sendError
	}
	return m.response, nil
}

func (m *mockStreamAdapter) TransformResponse(ctx context.Context, resp *http.Response) (*types.AegisResponse, error) {
	return &types.AegisResponse{
		Provider: m.name,
		Model:    "test-model",
		Usage: types.Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}, nil
}

func (m *mockStreamAdapter) TransformStreamChunk(chunk []byte) ([]byte, error) {
	// Parse and return chunk as-is for testing
	return chunk, nil
}

func (m *mockStreamAdapter) SupportsStreaming() bool {
	return true
}

func TestStreamMetricsTracking(t *testing.T) {
	// Create mock streaming response
	streamData := `data: {"model":"gpt-4","choices":[{"delta":{"content":"Hello"}}]}

data: {"model":"gpt-4","choices":[{"delta":{"content":" world"}}]}

data: {"model":"gpt-4","usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}

data: [DONE]

`
	
	body := bytes.NewBufferString(streamData)
	mockResp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(body),
		Header:     make(http.Header),
	}
	mockResp.Header.Set("Content-Type", "text/event-stream")

	adapter := &mockStreamAdapter{
		name:     "openai",
		response: mockResp,
	}

	// Create streaming handler with short timeout for testing
	config := StreamingConfig{
		PerChunkTimeout: 5 * time.Second,
		TotalTimeout:    30 * time.Second,
		BufferSize:      64 * 1024,
		MaxBufferSize:   1024 * 1024,
	}
	
	handler := &Handler{
		metrics: getTestMetrics(),
	}
	streamingHandler := NewStreamingHandler(handler, config)

	// Create test request and response writer
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	
	authInfo := &auth.AuthInfo{
		OrganizationID: "test-org",
		TeamID:         "test-team",
		UserID:         "test-user",
		KeyID:          "test-key",
	}
	
	aegisReq := &types.AegisRequest{
		Model:   "gpt-4",
		Stream:  true,
		Project: "test-project",
	}
	
	// Create provider request
	providerReq, _ := http.NewRequest("POST", "http://mock-provider.com", nil)

	// Execute streaming
	streamingHandler.HandleStream(w, req, "test-req-id", providerReq, adapter, "gpt-4", authInfo, aegisReq)

	// Verify response
	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}
	
	// Verify streaming headers
	contentType := w.Header().Get("Content-Type")
	if contentType != "text/event-stream" {
		t.Errorf("Expected Content-Type text/event-stream, got %s", contentType)
	}
}

func TestStreamTimeouts(t *testing.T) {
	tests := []struct {
		name            string
		chunkDelay      time.Duration
		totalTimeout    time.Duration
		perChunkTimeout time.Duration
		expectTimeout   bool
	}{
		{
			name:            "no timeout",
			chunkDelay:      10 * time.Millisecond,
			totalTimeout:    1 * time.Second,
			perChunkTimeout: 500 * time.Millisecond,
			expectTimeout:   false,
		},
		{
			name:            "per-chunk timeout",
			chunkDelay:      600 * time.Millisecond,
			totalTimeout:    5 * time.Second,
			perChunkTimeout: 500 * time.Millisecond,
			expectTimeout:   true,
		},
		{
			name:            "total timeout",
			chunkDelay:      10 * time.Millisecond,
			totalTimeout:    100 * time.Millisecond,
			perChunkTimeout: 1 * time.Second,
			expectTimeout:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create slow streaming response
			body := &slowReader{
				data:  []byte(`data: {"content":"test"}\n\ndata: [DONE]\n\n`),
				delay: tt.chunkDelay,
			}
			
			mockResp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       body,
				Header:     make(http.Header),
			}

			adapter := &mockStreamAdapter{
				name:     "openai",
				response: mockResp,
			}

			config := StreamingConfig{
				PerChunkTimeout: tt.perChunkTimeout,
				TotalTimeout:    tt.totalTimeout,
				BufferSize:      64 * 1024,
				MaxBufferSize:   1024 * 1024,
			}

			handler := &Handler{
				metrics: getTestMetrics(),
			}
			streamingHandler := NewStreamingHandler(handler, config)

			req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			w := httptest.NewRecorder()
			
			authInfo := &auth.AuthInfo{
				OrganizationID: "test-org",
			}
			
			aegisReq := &types.AegisRequest{
				Model:  "gpt-4",
				Stream: true,
			}
			
			providerReq, _ := http.NewRequest("POST", "http://mock-provider.com", nil)

			// Execute streaming
			start := time.Now()
			streamingHandler.HandleStream(w, req, "test-req-id", providerReq, adapter, "gpt-4", authInfo, aegisReq)
			duration := time.Since(start)

			// Verify timeout behavior
			if tt.expectTimeout {
				if duration >= tt.totalTimeout+tt.perChunkTimeout {
					t.Errorf("Timeout not enforced: took %v, expected less than %v", duration, tt.totalTimeout+tt.perChunkTimeout)
				}
			}
		})
	}
}

func TestStreamTokenExtraction(t *testing.T) {
	tests := []struct {
		name               string
		chunk              string
		expectModel        string
		expectPrompt       int
		expectCompletion   int
		expectTotal        int
	}{
		{
			name:             "chunk with usage",
			chunk:            `{"model":"gpt-4","usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`,
			expectModel:      "gpt-4",
			expectPrompt:     100,
			expectCompletion: 50,
			expectTotal:      150,
		},
		{
			name:        "chunk without usage",
			chunk:       `{"model":"gpt-4","choices":[{"delta":{"content":"test"}}]}`,
			expectModel: "gpt-4",
		},
		{
			name:  "invalid json",
			chunk: `invalid json`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultStreamingConfig()
			handler := &Handler{}
			streamingHandler := NewStreamingHandler(handler, config)

			metrics := &StreamMetrics{}
			err := streamingHandler.extractTokensFromChunk([]byte(tt.chunk), metrics)

			if tt.name == "invalid json" {
				if err == nil {
					t.Error("Expected error for invalid JSON, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if metrics.Model != tt.expectModel {
				t.Errorf("Expected model %s, got %s", tt.expectModel, metrics.Model)
			}

			if metrics.PromptTokens != tt.expectPrompt {
				t.Errorf("Expected prompt tokens %d, got %d", tt.expectPrompt, metrics.PromptTokens)
			}

			if metrics.CompletionTokens != tt.expectCompletion {
				t.Errorf("Expected completion tokens %d, got %d", tt.expectCompletion, metrics.CompletionTokens)
			}

			if metrics.TotalTokens != tt.expectTotal {
				t.Errorf("Expected total tokens %d, got %d", tt.expectTotal, metrics.TotalTokens)
			}
		})
	}
}

func TestCalculateTokensPerSecond(t *testing.T) {
	config := DefaultStreamingConfig()
	handler := &Handler{}
	streamingHandler := NewStreamingHandler(handler, config)

	tests := []struct {
		name     string
		tokens   int
		duration time.Duration
		expected float64
	}{
		{
			name:     "normal rate",
			tokens:   100,
			duration: 10 * time.Second,
			expected: 10.0,
		},
		{
			name:     "fast rate",
			tokens:   1000,
			duration: 1 * time.Second,
			expected: 1000.0,
		},
		{
			name:     "zero duration",
			tokens:   100,
			duration: 0,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := streamingHandler.calculateTokensPerSecond(tt.tokens, tt.duration)
			if result != tt.expected {
				t.Errorf("Expected %.2f tokens/sec, got %.2f", tt.expected, result)
			}
		})
	}
}

// slowReader simulates a slow streaming response for timeout testing.
type slowReader struct {
	data  []byte
	pos   int
	delay time.Duration
}

func (s *slowReader) Read(p []byte) (n int, err error) {
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	
	// Simulate slow streaming
	time.Sleep(s.delay)
	
	// Copy one byte at a time to simulate chunked streaming
	n = copy(p, s.data[s.pos:s.pos+1])
	s.pos += n
	return n, nil
}

func (s *slowReader) Close() error {
	return nil
}
