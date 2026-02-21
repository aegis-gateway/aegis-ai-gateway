package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/af-corp/aegis-gateway/internal/types"
)

// mockAdapter implements adapters.ProviderAdapter for streaming tests.
type mockAdapter struct {
	name      string
	transform func([]byte) ([]byte, error)
}

func (m *mockAdapter) Name() string { return m.name }
func (m *mockAdapter) TransformRequest(_ context.Context, _ *types.AegisRequest) (*http.Request, error) {
	return nil, nil
}
func (m *mockAdapter) TransformResponse(_ context.Context, _ *http.Response) (*types.AegisResponse, error) {
	return nil, nil
}
func (m *mockAdapter) SupportsStreaming() bool { return true }
func (m *mockAdapter) SendRequest(req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}
func (m *mockAdapter) TransformStreamChunk(chunk []byte) ([]byte, error) {
	if m.transform != nil {
		return m.transform(chunk)
	}
	return chunk, nil
}

func TestStreamSSE_OpenAIPassthrough(t *testing.T) {
	// Mock OpenAI-style SSE server
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	}

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer mockServer.Close()

	adapter := &mockAdapter{name: "openai"}

	// Make a request to the mock server to get the SSE response
	req, _ := http.NewRequest("GET", mockServer.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to get SSE response: %v", err)
	}

	// Capture the streamed output
	w := httptest.NewRecorder()
	streamSSE(w, "test-req-123", resp, adapter)

	result := w.Body.String()

	// Verify headers
	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %s", w.Header().Get("Content-Type"))
	}
	if w.Header().Get("X-Request-ID") != "test-req-123" {
		t.Errorf("expected X-Request-ID test-req-123, got %s", w.Header().Get("X-Request-ID"))
	}

	// Verify all chunks were forwarded
	for _, chunk := range chunks {
		if !strings.Contains(result, chunk) {
			t.Errorf("expected output to contain chunk: %s", chunk)
		}
	}

	// Verify [DONE] signal
	if !strings.Contains(result, "data: [DONE]") {
		t.Error("expected output to contain data: [DONE]")
	}
}

func TestStreamSSE_AnthropicTransform(t *testing.T) {
	// Mock Anthropic-style SSE server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		events := []string{
			`{"type":"message_start","message":{"id":"msg_123","model":"claude-3"}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		}
		for _, event := range events {
			fmt.Fprintf(w, "data: %s\n\n", event)
			flusher.Flush()
		}
	}))
	defer mockServer.Close()

	// Simulate Anthropic transform: content_block_delta → OpenAI delta, message_stop → [DONE]
	adapter := &mockAdapter{
		name: "anthropic",
		transform: func(chunk []byte) ([]byte, error) {
			s := string(chunk)
			if strings.Contains(s, "content_block_delta") && strings.Contains(s, "text_delta") {
				// Extract text and convert to OpenAI format
				if strings.Contains(s, `"Hello"`) {
					return []byte(`{"choices":[{"index":0,"delta":{"content":"Hello"}}]}`), nil
				}
				if strings.Contains(s, `" world"`) {
					return []byte(`{"choices":[{"index":0,"delta":{"content":" world"}}]}`), nil
				}
			}
			if strings.Contains(s, "message_stop") {
				return []byte("[DONE]"), nil
			}
			return nil, nil // skip non-content events
		},
	}

	req, _ := http.NewRequest("GET", mockServer.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to get SSE response: %v", err)
	}

	w := httptest.NewRecorder()
	streamSSE(w, "test-req-456", resp, adapter)

	result := w.Body.String()

	// Should contain the transformed OpenAI-format chunks
	if !strings.Contains(result, `"content":"Hello"`) {
		t.Error("expected transformed Hello chunk")
	}
	if !strings.Contains(result, `"content":" world"`) {
		t.Error("expected transformed world chunk")
	}
	if !strings.Contains(result, "data: [DONE]") {
		t.Error("expected [DONE] signal")
	}

	// Should NOT contain raw Anthropic events
	if strings.Contains(result, "message_start") {
		t.Error("raw Anthropic message_start should be filtered out")
	}
	if strings.Contains(result, "content_block_start") {
		t.Error("raw Anthropic content_block_start should be filtered out")
	}
}
