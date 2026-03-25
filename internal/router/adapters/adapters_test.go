package adapters

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/types"
)

// --- OpenAI Adapter Tests ---

func newOpenAICfg() config.ProviderConfig {
	return config.ProviderConfig{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "sk-test-key",
		Headers: map[string]string{"X-Custom": "val"},
	}
}

func TestOpenAIAdapter_Name(t *testing.T) {
	a := NewOpenAIAdapter(newOpenAICfg(), http.DefaultClient)
	if a.Name() != "openai" {
		t.Errorf("expected openai, got %s", a.Name())
	}
}

func TestOpenAIAdapter_SupportsStreaming(t *testing.T) {
	a := NewOpenAIAdapter(newOpenAICfg(), http.DefaultClient)
	if !a.SupportsStreaming() {
		t.Error("expected SupportsStreaming() = true")
	}
}

func TestOpenAIAdapter_TransformRequest(t *testing.T) {
	a := NewOpenAIAdapter(newOpenAICfg(), http.DefaultClient)
	temp := 0.7
	maxTok := 1000

	req := &types.AegisRequest{
		Model:       "gpt-4o",
		Messages:    []types.Message{{Role: "system", Content: "You help."}, {Role: "user", Content: "Hi"}},
		Stream:      false,
		Temperature: &temp,
		MaxTokens:   &maxTok,
	}

	httpReq, err := a.TransformRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check URL
	if httpReq.URL.String() != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("unexpected URL: %s", httpReq.URL.String())
	}

	// Check headers
	if httpReq.Header.Get("Authorization") != "Bearer sk-test-key" {
		t.Errorf("unexpected auth header: %s", httpReq.Header.Get("Authorization"))
	}
	if httpReq.Header.Get("Content-Type") != "application/json" {
		t.Error("missing Content-Type header")
	}
	if httpReq.Header.Get("X-Custom") != "val" {
		t.Error("custom header not set")
	}

	// Check body
	body, _ := io.ReadAll(httpReq.Body)
	var parsed openAIRequestBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if parsed.Model != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", parsed.Model)
	}
	if len(parsed.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(parsed.Messages))
	}
	if *parsed.Temperature != 0.7 {
		t.Errorf("expected temperature 0.7, got %f", *parsed.Temperature)
	}
	if *parsed.MaxTokens != 1000 {
		t.Errorf("expected max_tokens 1000, got %d", *parsed.MaxTokens)
	}
}

func TestOpenAIAdapter_TransformResponse_Success(t *testing.T) {
	a := NewOpenAIAdapter(newOpenAICfg(), http.DefaultClient)

	respBody := `{
		"id": "chatcmpl-123",
		"object": "chat.completion",
		"model": "gpt-4o",
		"choices": [
			{
				"index": 0,
				"message": {"role": "assistant", "content": "Hello!"},
				"finish_reason": "stop"
			}
		],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 5,
			"total_tokens": 15
		}
	}`

	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(respBody)),
	}

	aegisResp, err := a.TransformResponse(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if aegisResp.Provider != "openai" {
		t.Errorf("expected provider openai, got %s", aegisResp.Provider)
	}
	if aegisResp.Model != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", aegisResp.Model)
	}
	if len(aegisResp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(aegisResp.Choices))
	}
	if aegisResp.Choices[0].Message.Content != "Hello!" {
		t.Errorf("unexpected content: %s", aegisResp.Choices[0].Message.Content)
	}
	if aegisResp.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason stop, got %s", aegisResp.Choices[0].FinishReason)
	}
	if aegisResp.Usage.PromptTokens != 10 || aegisResp.Usage.CompletionTokens != 5 || aegisResp.Usage.TotalTokens != 15 {
		t.Errorf("unexpected usage: %+v", aegisResp.Usage)
	}
}

func TestOpenAIAdapter_TransformResponse_ErrorStatus(t *testing.T) {
	a := NewOpenAIAdapter(newOpenAICfg(), http.DefaultClient)

	resp := &http.Response{
		StatusCode: 429,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
	}

	_, err := a.TransformResponse(context.Background(), resp)
	if err == nil {
		t.Fatal("expected error for non-200 status")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should contain status code: %v", err)
	}
}

func TestOpenAIAdapter_TransformResponse_MultipleChoices(t *testing.T) {
	a := NewOpenAIAdapter(newOpenAICfg(), http.DefaultClient)

	respBody := `{
		"model": "gpt-4o",
		"choices": [
			{"index": 0, "message": {"role": "assistant", "content": "A"}, "finish_reason": "stop"},
			{"index": 1, "message": {"role": "assistant", "content": "B"}, "finish_reason": "stop"}
		],
		"usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}
	}`

	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(respBody)),
	}

	aegisResp, err := a.TransformResponse(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(aegisResp.Choices) != 2 {
		t.Errorf("expected 2 choices, got %d", len(aegisResp.Choices))
	}
}

func TestOpenAIAdapter_TransformStreamChunk(t *testing.T) {
	a := NewOpenAIAdapter(newOpenAICfg(), http.DefaultClient)
	chunk := []byte(`{"choices":[{"delta":{"content":"Hi"}}]}`)

	out, err := a.TransformStreamChunk(chunk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// OpenAI is passthrough
	if string(out) != string(chunk) {
		t.Error("expected passthrough for OpenAI stream chunks")
	}
}

// --- Anthropic Adapter Tests ---

func newAnthropicCfg() config.ProviderConfig {
	return config.ProviderConfig{
		BaseURL: "https://api.anthropic.com/v1",
		APIKey:  "sk-ant-test",
		Headers: map[string]string{"anthropic-version": "2023-06-01"},
	}
}

func TestAnthropicAdapter_Name(t *testing.T) {
	a := NewAnthropicAdapter(newAnthropicCfg(), http.DefaultClient)
	if a.Name() != "anthropic" {
		t.Errorf("expected anthropic, got %s", a.Name())
	}
}

func TestAnthropicAdapter_SupportsStreaming(t *testing.T) {
	a := NewAnthropicAdapter(newAnthropicCfg(), http.DefaultClient)
	if !a.SupportsStreaming() {
		t.Error("expected SupportsStreaming() = true")
	}
}

func TestAnthropicAdapter_TransformRequest_SystemMessage(t *testing.T) {
	a := NewAnthropicAdapter(newAnthropicCfg(), http.DefaultClient)

	req := &types.AegisRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []types.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hi"},
		},
	}

	httpReq, err := a.TransformRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check URL
	if httpReq.URL.String() != "https://api.anthropic.com/v1/messages" {
		t.Errorf("unexpected URL: %s", httpReq.URL.String())
	}

	// Check headers
	if httpReq.Header.Get("x-api-key") != "sk-ant-test" {
		t.Errorf("unexpected api key header: %s", httpReq.Header.Get("x-api-key"))
	}
	if httpReq.Header.Get("anthropic-version") != "2023-06-01" {
		t.Error("custom header not set")
	}

	// Check body - system should be extracted, messages should only contain user
	body, _ := io.ReadAll(httpReq.Body)
	var parsed anthropicRequestBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if parsed.System != "You are helpful." {
		t.Errorf("expected system message extracted, got %q", parsed.System)
	}
	if len(parsed.Messages) != 1 {
		t.Fatalf("expected 1 message (system extracted), got %d", len(parsed.Messages))
	}
	if parsed.Messages[0].Role != "user" {
		t.Errorf("expected user message, got %s", parsed.Messages[0].Role)
	}
}

func TestAnthropicAdapter_TransformRequest_DefaultMaxTokens(t *testing.T) {
	a := NewAnthropicAdapter(newAnthropicCfg(), http.DefaultClient)

	req := &types.AegisRequest{
		Model:    "claude-sonnet-4-5-20250929",
		Messages: []types.Message{{Role: "user", Content: "Hi"}},
		// MaxTokens is nil — should default to 4096
	}

	httpReq, err := a.TransformRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body, _ := io.ReadAll(httpReq.Body)
	var parsed anthropicRequestBody
	_ = json.Unmarshal(body, &parsed)
	if parsed.MaxTokens != 4096 {
		t.Errorf("expected default max_tokens 4096, got %d", parsed.MaxTokens)
	}
}

func TestAnthropicAdapter_TransformRequest_CustomMaxTokens(t *testing.T) {
	a := NewAnthropicAdapter(newAnthropicCfg(), http.DefaultClient)
	maxTok := 512

	req := &types.AegisRequest{
		Model:     "claude-sonnet-4-5-20250929",
		Messages:  []types.Message{{Role: "user", Content: "Hi"}},
		MaxTokens: &maxTok,
	}

	httpReq, err := a.TransformRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body, _ := io.ReadAll(httpReq.Body)
	var parsed anthropicRequestBody
	_ = json.Unmarshal(body, &parsed)
	if parsed.MaxTokens != 512 {
		t.Errorf("expected max_tokens 512, got %d", parsed.MaxTokens)
	}
}

func TestAnthropicAdapter_TransformResponse_Success(t *testing.T) {
	a := NewAnthropicAdapter(newAnthropicCfg(), http.DefaultClient)

	respBody := `{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-4-5-20250929",
		"content": [
			{"type": "text", "text": "Hello there!"}
		],
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 20,
			"output_tokens": 10
		}
	}`

	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(respBody)),
	}

	aegisResp, err := a.TransformResponse(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if aegisResp.Provider != "anthropic" {
		t.Errorf("expected provider anthropic, got %s", aegisResp.Provider)
	}
	if aegisResp.Model != "claude-sonnet-4-5-20250929" {
		t.Errorf("expected model claude-sonnet-4-5-20250929, got %s", aegisResp.Model)
	}
	if len(aegisResp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(aegisResp.Choices))
	}
	if aegisResp.Choices[0].Message.Content != "Hello there!" {
		t.Errorf("unexpected content: %s", aegisResp.Choices[0].Message.Content)
	}
	if aegisResp.Choices[0].Message.Role != "assistant" {
		t.Errorf("expected role assistant, got %s", aegisResp.Choices[0].Message.Role)
	}
	// end_turn should map to "stop"
	if aegisResp.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason stop, got %s", aegisResp.Choices[0].FinishReason)
	}
	if aegisResp.Usage.PromptTokens != 20 {
		t.Errorf("expected prompt_tokens 20, got %d", aegisResp.Usage.PromptTokens)
	}
	if aegisResp.Usage.CompletionTokens != 10 {
		t.Errorf("expected completion_tokens 10, got %d", aegisResp.Usage.CompletionTokens)
	}
	if aegisResp.Usage.TotalTokens != 30 {
		t.Errorf("expected total_tokens 30, got %d", aegisResp.Usage.TotalTokens)
	}
}

func TestAnthropicAdapter_TransformResponse_ErrorStatus(t *testing.T) {
	a := NewAnthropicAdapter(newAnthropicCfg(), http.DefaultClient)

	resp := &http.Response{
		StatusCode: 500,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"internal error"}}`)),
	}

	_, err := a.TransformResponse(context.Background(), resp)
	if err == nil {
		t.Fatal("expected error for non-200 status")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should contain status code: %v", err)
	}
}

func TestAnthropicAdapter_TransformStreamChunk_ContentBlockDelta(t *testing.T) {
	a := NewAnthropicAdapter(newAnthropicCfg(), http.DefaultClient)

	chunk := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`)
	out, err := a.TransformStreamChunk(chunk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == nil {
		t.Fatal("expected output for content_block_delta")
	}

	// Verify it's valid OpenAI format
	var oai openAIStreamChunk
	if err := json.Unmarshal(out, &oai); err != nil {
		t.Fatalf("output is not valid OpenAI chunk: %v", err)
	}
	if len(oai.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(oai.Choices))
	}
	if oai.Choices[0].Delta.Content != "Hello" {
		t.Errorf("expected content Hello, got %s", oai.Choices[0].Delta.Content)
	}
	if oai.Choices[0].Index != 0 {
		t.Errorf("expected index 0, got %d", oai.Choices[0].Index)
	}
}

func TestAnthropicAdapter_TransformStreamChunk_MessageDelta(t *testing.T) {
	a := NewAnthropicAdapter(newAnthropicCfg(), http.DefaultClient)

	chunk := []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)
	out, err := a.TransformStreamChunk(chunk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == nil {
		t.Fatal("expected output for message_delta")
	}

	var oai openAIStreamChunk
	_ = json.Unmarshal(out, &oai)
	if oai.Choices[0].FinishReason == nil || *oai.Choices[0].FinishReason != "stop" {
		t.Error("expected finish_reason stop from end_turn mapping")
	}
}

func TestAnthropicAdapter_TransformStreamChunk_MessageStop(t *testing.T) {
	a := NewAnthropicAdapter(newAnthropicCfg(), http.DefaultClient)

	chunk := []byte(`{"type":"message_stop"}`)
	out, err := a.TransformStreamChunk(chunk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "[DONE]" {
		t.Errorf("expected [DONE], got %s", string(out))
	}
}

func TestAnthropicAdapter_TransformStreamChunk_SkippedEvents(t *testing.T) {
	a := NewAnthropicAdapter(newAnthropicCfg(), http.DefaultClient)

	skippable := []string{
		`{"type":"message_start"}`,
		`{"type":"content_block_start"}`,
		`{"type":"content_block_stop"}`,
		`{"type":"ping"}`,
	}

	for _, chunk := range skippable {
		out, err := a.TransformStreamChunk([]byte(chunk))
		if err != nil {
			t.Errorf("unexpected error for %s: %v", chunk, err)
		}
		if out != nil {
			t.Errorf("expected nil output for %s, got %s", chunk, string(out))
		}
	}
}

func TestAnthropicAdapter_TransformStreamChunk_InvalidJSON(t *testing.T) {
	a := NewAnthropicAdapter(newAnthropicCfg(), http.DefaultClient)

	out, err := a.TransformStreamChunk([]byte(`not json`))
	// Should skip gracefully (nil, nil)
	if err != nil {
		t.Errorf("expected nil error for invalid JSON, got %v", err)
	}
	if out != nil {
		t.Errorf("expected nil output for invalid JSON, got %s", string(out))
	}
}

func TestMapStopReason(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"end_turn", "stop"},
		{"max_tokens", "length"},
		{"stop_sequence", "stop"},
		{"unknown", "unknown"},
		{"", ""},
	}
	for _, tt := range tests {
		got := mapStopReason(tt.input)
		if got != tt.expected {
			t.Errorf("mapStopReason(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
