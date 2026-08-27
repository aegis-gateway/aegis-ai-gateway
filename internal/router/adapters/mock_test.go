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

package adapters

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

func newTestMock() *MockAdapter {
	return NewMockAdapter("anthropic", config.ProviderConfig{Type: "anthropic"})
}

func TestMockAdapter_ImplementsProviderAdapter(t *testing.T) {
	var _ ProviderAdapter = newTestMock()
}

func TestMockAdapter_NameIsDistinctFromRealProviders(t *testing.T) {
	m := newTestMock()
	if m.Name() != MockAdapterName {
		t.Fatalf("Name() = %q, want %q", m.Name(), MockAdapterName)
	}
	// The health endpoint and the startup warning both distinguish a mock from
	// a real provider by this string alone, so it must not collide.
	for _, real := range []string{"openai", "anthropic", "azure_openai"} {
		if m.Name() == real {
			t.Errorf("mock adapter reports the name of a real provider type %q", real)
		}
	}
	// Registered under a real provider key so pricing and usage keep attributing
	// to the real provider, which is the point of standing in rather than adding.
	if m.ProviderKey() != "anthropic" {
		t.Errorf("ProviderKey() = %q, want %q", m.ProviderKey(), "anthropic")
	}
}

func TestMockAdapter_RoundTripReturnsDeterministicCompletion(t *testing.T) {
	m := newTestMock()
	req := &types.AegisRequest{
		Model:    "claude-haiku-4-5-20251001",
		Messages: []types.Message{{Role: "user", Content: "Hello from AEGIS!"}},
	}

	first := mockRoundTrip(t, m, req)
	second := mockRoundTrip(t, m, req)

	if len(first.Choices) != 1 {
		t.Fatalf("got %d choices, want 1", len(first.Choices))
	}
	if first.Choices[0].Message.Content != mockCompletionText {
		t.Errorf("content = %q, want the canned completion", first.Choices[0].Message.Content)
	}
	if first.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want %q", first.Choices[0].FinishReason, "stop")
	}
	if first.Model != req.Model {
		t.Errorf("model = %q, want the requested model %q", first.Model, req.Model)
	}
	if first.Provider != MockAdapterName {
		t.Errorf("provider = %q, want %q", first.Provider, MockAdapterName)
	}

	// Deterministic: the quickstart's verify mode asserts on this response, and
	// a body that varied run to run would make that assertion worthless.
	if first.Choices[0].Message.Content != second.Choices[0].Message.Content ||
		first.Usage != second.Usage {
		t.Errorf("two identical requests produced different responses:\n%+v\n%+v", first, second)
	}
}

func TestMockAdapter_TokenCountsArePlausibleAndVaryWithInput(t *testing.T) {
	m := newTestMock()

	short := mockRoundTrip(t, m, &types.AegisRequest{
		Model:    "claude-haiku-4-5-20251001",
		Messages: []types.Message{{Role: "user", Content: "Hi"}},
	})
	long := mockRoundTrip(t, m, &types.AegisRequest{
		Model: "claude-haiku-4-5-20251001",
		Messages: []types.Message{{
			Role:    "user",
			Content: strings.Repeat("the quick brown fox jumps over the lazy dog. ", 40),
		}},
	})

	if short.Usage.PromptTokens <= 0 || short.Usage.CompletionTokens <= 0 {
		t.Errorf("token counts must be positive, got %+v", short.Usage)
	}
	if short.Usage.TotalTokens != short.Usage.PromptTokens+short.Usage.CompletionTokens {
		t.Errorf("total_tokens %d != prompt %d + completion %d",
			short.Usage.TotalTokens, short.Usage.PromptTokens, short.Usage.CompletionTokens)
	}
	// A constant token count would let the cost calculator return the same
	// number for every request, which would make cost tracking look like it
	// works while proving nothing.
	if long.Usage.PromptTokens <= short.Usage.PromptTokens {
		t.Errorf("prompt tokens did not grow with input: short=%d long=%d",
			short.Usage.PromptTokens, long.Usage.PromptTokens)
	}
}

func TestMockAdapter_StreamingEmitsOpenAIFormatSSE(t *testing.T) {
	m := newTestMock()
	if !m.SupportsStreaming() {
		t.Fatal("SupportsStreaming() = false, want true")
	}

	httpReq, err := m.TransformRequest(context.Background(), &types.AegisRequest{
		Model:    "claude-haiku-4-5-20251001",
		Messages: []types.Message{{Role: "user", Content: "stream please"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	resp, err := m.SendRequest(httpReq)
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	body := string(raw)

	if !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Errorf("stream does not terminate with data: [DONE]:\n%s", body)
	}

	var assembled strings.Builder
	var sawUsage bool
	for _, line := range strings.Split(body, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Object  string `json:"object"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("chunk is not valid JSON (%v): %s", err, payload)
		}
		if chunk.Object != "chat.completion.chunk" {
			t.Errorf("chunk object = %q, want chat.completion.chunk", chunk.Object)
		}
		for _, c := range chunk.Choices {
			assembled.WriteString(c.Delta.Content)
		}
		if chunk.Usage != nil && chunk.Usage.TotalTokens > 0 {
			sawUsage = true
		}
	}

	if assembled.String() != mockCompletionText {
		t.Errorf("assembled stream = %q, want %q", assembled.String(), mockCompletionText)
	}
	if !sawUsage {
		t.Error("no chunk carried usage; streaming cost recording would have nothing to price")
	}
}

func TestMockAdapter_TransformStreamChunkIsPassthrough(t *testing.T) {
	m := newTestMock()
	in := []byte(`{"object":"chat.completion.chunk"}`)
	out, err := m.TransformStreamChunk(in)
	if err != nil {
		t.Fatalf("TransformStreamChunk: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("chunk = %q, want passthrough %q", out, in)
	}
}

// TestMockAdapter_MakesNoOutboundRequest is the claim the notice in the canned
// completion makes: nothing leaves the machine. The adapter holds no
// http.Client at all, so there is nothing that could dial, and the request URL
// it builds carries a scheme no client would accept if one tried.
func TestMockAdapter_MakesNoOutboundRequest(t *testing.T) {
	m := newTestMock()
	httpReq, err := m.TransformRequest(context.Background(), &types.AegisRequest{
		Model:    "claude-haiku-4-5-20251001",
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	if httpReq.URL.Scheme != "mock" {
		t.Errorf("request scheme = %q, want mock so no transport will dial it", httpReq.URL.Scheme)
	}
	if _, err := http.DefaultClient.Do(httpReq); err == nil {
		t.Error("an HTTP client accepted the mock request URL; it must not be dialable")
	}
}

// TestMockAdapter_CarriesNoRequestPayloadIntoMetadata guards the same boundary
// the audit conformance tests guard, at the adapter. The mock is the one
// adapter that holds the request body in process memory rather than handing it
// to a socket, so it is the one place a well-meaning change could start
// attaching prompt text to something durable.
func TestMockAdapter_CarriesNoRequestPayloadIntoMetadata(t *testing.T) {
	const canary = "CANARY_PAYLOAD_8f4a2b91e3c7d056"

	m := newTestMock()
	resp := mockRoundTrip(t, m, &types.AegisRequest{
		Model:    "claude-haiku-4-5-20251001",
		Messages: []types.Message{{Role: "user", Content: canary + " please echo that"}},
	})

	// Everything the handler carries forward from a response into usage_records
	// and the audit trail is metadata: model, provider, token counts, cost. The
	// completion text itself is written to the client and nowhere else. Assert
	// the canary is not smuggled into any of the metadata fields.
	metadata := struct {
		Model    string
		Provider string
		Usage    types.Usage
	}{resp.Model, resp.Provider, resp.Usage}

	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if strings.Contains(string(encoded), canary) {
		t.Errorf("ZERO-RETENTION VIOLATION: request payload reached response metadata: %s", encoded)
	}
}

func mockRoundTrip(t *testing.T, m *MockAdapter, req *types.AegisRequest) *types.AegisResponse {
	t.Helper()

	httpReq, err := m.TransformRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	httpResp, err := m.SendRequest(httpReq)
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}
	resp, err := m.TransformResponse(context.Background(), httpResp)
	if err != nil {
		t.Fatalf("TransformResponse: %v", err)
	}
	return resp
}
