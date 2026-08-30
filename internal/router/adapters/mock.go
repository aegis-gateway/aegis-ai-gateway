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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/redact"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// MockAdapter answers chat completions locally instead of calling a provider.
//
// It exists so the quickstart can run with no credentials at all. The most
// important thing AEGIS does, refusing a request that carries a credential,
// never reaches a provider. Requiring a provider key to see that put a live API
// key between a first-time reader and the one demo worth watching.
//
// What it does NOT do is short-circuit the request pipeline. The mock is a
// ProviderAdapter like any other and is reached only after auth, validation,
// the filter chain, policy evaluation, classification gating, and rate limits
// have all run and permitted the request. A denial is a real denial, decided by
// the same code and written to the same audit trail; only the upstream HTTP
// call is replaced.
//
// Requests are answered from the request itself, with no network call: the
// canned http.Response returned by SendRequest is what the OpenAI-format
// decoding path in the handler then parses, so response transformation, usage
// accounting, and cost calculation all still execute against real bytes.
type MockAdapter struct {
	cfg config.ProviderConfig
	// providerKey is the name this adapter is registered under in
	// providers.yaml ("anthropic", "openai"). The mock stands in for a
	// configured provider rather than adding one, so pricing, metrics, and
	// usage attribution keep keying off the real provider and exercise the
	// real pricing rows. See router.Route for why that distinction matters.
	providerKey string
}

// NewMockAdapter builds an adapter that stands in for the provider configured
// under providerKey.
func NewMockAdapter(providerKey string, cfg config.ProviderConfig) *MockAdapter {
	return &MockAdapter{cfg: cfg, providerKey: providerKey}
}

// MockAdapterName is the adapter type name the mock reports. It is deliberately
// distinct from any real provider type so that "am I talking to a mock?" is
// answerable from the adapter alone, which is what the health endpoint and the
// startup log both rely on.
const MockAdapterName = "mock"

// mockCompletionText is the canned answer. It is deterministic on purpose: the
// quickstart's verify mode asserts on the response, and a varying body would
// make that assertion either flaky or vacuous.
const mockCompletionText = "This is a canned response from the AEGIS mock provider. " +
	"No request left this machine. Set OPENAI_API_KEY or ANTHROPIC_API_KEY to use a real provider."

func (a *MockAdapter) Name() string { return MockAdapterName }

// ProviderKey reports the providers.yaml name this mock is standing in for.
func (a *MockAdapter) ProviderKey() string { return a.providerKey }

func (a *MockAdapter) SupportsStreaming() bool { return true }

// SupportsTools reports true. The mock speaks AEGIS's canonical OpenAI format,
// and answering a tool-bearing request with a tool call is what lets the
// agent-compatibility demo show the working path with no provider credential.
func (a *MockAdapter) SupportsTools() bool { return true }

// TransformRequest builds the same OpenAI-format request body a real adapter
// would, so a malformed canonical request fails here exactly as it would
// against a provider. The URL is a mock:// scheme that no HTTP client will
// dial; SendRequest intercepts before it can be sent.
func (a *MockAdapter) TransformRequest(ctx context.Context, req *types.AegisRequest) (*http.Request, error) {
	body := openAIRequestBody{
		Model:             req.Model,
		Messages:          req.Messages,
		Stream:            req.Stream,
		Temperature:       req.Temperature,
		MaxTokens:         req.MaxTokens,
		TopP:              req.TopP,
		Stop:              req.Stop,
		Tools:             req.Tools,
		ParallelToolCalls: req.ParallelToolCalls,
	}
	if req.ToolChoice.IsSet() {
		body.ToolChoice = &req.ToolChoice
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal mock request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"mock://"+a.providerKey+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create mock request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	return httpReq, nil
}

// TransformResponse decodes the canned body through the same OpenAI response
// path a real provider response takes.
func (a *MockAdapter) TransformResponse(ctx context.Context, resp *http.Response) (*types.AegisResponse, error) {
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read mock response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// redact.Excerpt, not string(body): this error is logged by the
		// handler, and a provider error body is unbounded text the gateway
		// does not control which routinely echoes the caller's own content
		// back. Logging it verbatim puts caller text into the log store.
		return nil, fmt.Errorf("mock provider returned status %d: %s", resp.StatusCode, redact.Excerpt(body))
	}

	var oaiResp openAIResponseBody
	if err := json.Unmarshal(body, &oaiResp); err != nil {
		return nil, fmt.Errorf("unmarshal mock response: %w", err)
	}

	aegisResp := &types.AegisResponse{
		Model:    oaiResp.Model,
		Provider: MockAdapterName,
		Usage: types.Usage{
			PromptTokens:     oaiResp.Usage.PromptTokens,
			CompletionTokens: oaiResp.Usage.CompletionTokens,
			TotalTokens:      oaiResp.Usage.TotalTokens,
		},
	}
	for _, c := range oaiResp.Choices {
		aegisResp.Choices = append(aegisResp.Choices, types.Choice{
			Index: c.Index,
			Message: types.Message{
				Role:      c.Message.Role,
				Content:   c.Message.Content,
				ToolCalls: c.Message.ToolCalls,
			},
			FinishReason: c.FinishReason,
		})
	}
	return aegisResp, nil
}

// TransformStreamChunk is a passthrough: SendRequest already emits chunks in
// OpenAI SSE format, which is AEGIS's canonical format.
func (a *MockAdapter) TransformStreamChunk(chunk []byte) ([]byte, error) {
	return chunk, nil
}

// SendRequest answers locally rather than dialling anything. It reads the
// outgoing body so the reply reflects the request that was actually made, and
// so a caller inspecting token counts sees numbers that move with input size.
func (a *MockAdapter) SendRequest(req *http.Request) (*http.Response, error) {
	var sent openAIRequestBody
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read mock request body: %w", err)
		}
		if err := json.Unmarshal(raw, &sent); err != nil {
			return nil, fmt.Errorf("unmarshal mock request body: %w", err)
		}
	}

	promptTokens := estimateTokens(sent.Messages) + estimateToolTokens(sent.Tools)

	// A tool-bearing request is answered with a tool call, so that the whole
	// agent loop is exercisable with no provider credential: offer a tool, get
	// a call back, return a result, get a final answer. Answering with prose
	// instead would reproduce the exact symptom this change fixes and make the
	// demo unable to tell a working gateway from a broken one.
	if call, ok := mockToolCall(sent); ok {
		completionTokens := estimateTokenCount(call.Function.Arguments) + estimateTokenCount(call.Function.Name)
		if sent.Stream {
			return a.streamToolCallResponse(req, sent.Model, call, promptTokens, completionTokens), nil
		}
		return a.toolCallResponse(req, sent.Model, call, promptTokens, completionTokens)
	}

	completionTokens := estimateTokenCount(mockCompletionText)

	if sent.Stream {
		return a.streamResponse(req, sent.Model, promptTokens, completionTokens), nil
	}
	return a.jsonResponse(req, sent.Model, promptTokens, completionTokens)
}

func (a *MockAdapter) jsonResponse(req *http.Request, model string, promptTokens, completionTokens int) (*http.Response, error) {
	payload := map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion",
		"created": mockCreatedUnix,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]string{"role": "assistant", "content": mockCompletionText},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal mock response: %w", err)
	}
	return newMockHTTPResponse(req, "application/json", data), nil
}

// streamResponse emits the canned answer as OpenAI-format SSE, one word per
// chunk, closing with a usage-bearing chunk and [DONE]. The streaming path in
// internal/gateway parses these exactly as it parses a provider's.
func (a *MockAdapter) streamResponse(req *http.Request, model string, promptTokens, completionTokens int) *http.Response {
	var buf bytes.Buffer

	writeChunk := func(delta map[string]any, finish any, usage map[string]int) {
		chunk := map[string]any{
			"id":      "chatcmpl-mock",
			"object":  "chat.completion.chunk",
			"created": mockCreatedUnix,
			"model":   model,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			}},
		}
		if usage != nil {
			chunk["usage"] = usage
		}
		data, _ := json.Marshal(chunk)
		buf.WriteString("data: ")
		buf.Write(data)
		buf.WriteString("\n\n")
	}

	writeChunk(map[string]any{"role": "assistant"}, nil, nil)
	for i, word := range strings.Fields(mockCompletionText) {
		text := word
		if i > 0 {
			text = " " + word
		}
		writeChunk(map[string]any{"content": text}, nil, nil)
	}
	writeChunk(map[string]any{}, "stop", map[string]int{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      promptTokens + completionTokens,
	})
	buf.WriteString("data: [DONE]\n\n")

	return newMockHTTPResponse(req, "text/event-stream", buf.Bytes())
}

// mockToolCallID is fixed for the same reason mockCreatedUnix is: the demo and
// the quickstart assert on the response, and a random id would make those
// assertions either flaky or vacuous.
const mockToolCallID = "call_aegis_mock_0"

// mockToolCall decides whether to answer with a tool call, and builds it.
//
// It calls the first offered tool with empty JSON arguments, or the named tool
// when tool_choice pins one. It declines once the conversation already contains
// a tool result, so the loop terminates rather than calling forever.
func mockToolCall(sent openAIRequestBody) (types.ToolCall, bool) {
	if len(sent.Tools) == 0 {
		return types.ToolCall{}, false
	}
	if sent.ToolChoice != nil && sent.ToolChoice.Mode == types.ToolChoiceNone {
		return types.ToolCall{}, false
	}
	for _, m := range sent.Messages {
		if m.Role == types.RoleTool {
			return types.ToolCall{}, false
		}
	}

	name := sent.Tools[0].Function.Name
	if sent.ToolChoice != nil && sent.ToolChoice.Function != "" {
		name = sent.ToolChoice.Function
	}

	return types.ToolCall{
		ID:       mockToolCallID,
		Type:     types.ToolTypeFunction,
		Function: types.FunctionCallSpec{Name: name, Arguments: "{}"},
	}, true
}

func (a *MockAdapter) toolCallResponse(req *http.Request, model string, call types.ToolCall, promptTokens, completionTokens int) (*http.Response, error) {
	payload := map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion",
		"created": mockCreatedUnix,
		"model":   model,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":       "assistant",
				"content":    nil,
				"tool_calls": []types.ToolCall{call},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]int{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal mock tool call response: %w", err)
	}
	return newMockHTTPResponse(req, "application/json", data), nil
}

// streamToolCallResponse emits the tool call the way a provider does: an
// index-keyed first delta carrying the id, the type and the function name, then
// the arguments split across further deltas that carry only the index. A client
// that does not accumulate by index sees a call with no name; that is what the
// index-based accumulation in the streaming path exists to handle, and
// splitting the arguments here is what makes the demo exercise it.
func (a *MockAdapter) streamToolCallResponse(req *http.Request, model string, call types.ToolCall, promptTokens, completionTokens int) *http.Response {
	var buf bytes.Buffer
	idx := 0

	write := func(delta map[string]any, finish any, usage map[string]int) {
		chunk := map[string]any{
			"id":      "chatcmpl-mock",
			"object":  "chat.completion.chunk",
			"created": mockCreatedUnix,
			"model":   model,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			}},
		}
		if usage != nil {
			chunk["usage"] = usage
		}
		data, _ := json.Marshal(chunk)
		buf.WriteString("data: ")
		buf.Write(data)
		buf.WriteString("\n\n")
	}

	write(map[string]any{"role": "assistant"}, nil, nil)
	write(map[string]any{"tool_calls": []map[string]any{{
		"index":    idx,
		"id":       call.ID,
		"type":     call.Type,
		"function": map[string]string{"name": call.Function.Name, "arguments": ""},
	}}}, nil, nil)
	for _, frag := range splitArguments(call.Function.Arguments) {
		write(map[string]any{"tool_calls": []map[string]any{{
			"index":    idx,
			"function": map[string]string{"arguments": frag},
		}}}, nil, nil)
	}
	write(map[string]any{}, "tool_calls", map[string]int{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      promptTokens + completionTokens,
	})
	buf.WriteString("data: [DONE]\n\n")

	return newMockHTTPResponse(req, "text/event-stream", buf.Bytes())
}

// splitArguments cuts the arguments string into single-character fragments so
// that even a two-character payload arrives across more than one delta.
func splitArguments(args string) []string {
	if args == "" {
		return nil
	}
	out := make([]string, 0, len(args))
	for _, r := range args {
		out = append(out, string(r))
	}
	return out
}

// mockCreatedUnix is a fixed timestamp. The mock's whole value is being
// reproducible, and a wall-clock value would make two otherwise identical
// responses differ.
const mockCreatedUnix = 1767225600 // 2026-01-01T00:00:00Z

func newMockHTTPResponse(req *http.Request, contentType string, body []byte) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", contentType)
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

// estimateTokens approximates the prompt token count for a message list.
//
// It is an approximation and is labelled as one wherever it surfaces. The point
// is not accuracy against a provider's tokeniser but that the number varies
// with the input, so the cost calculator and the usage_records write downstream
// are exercised with realistic values rather than a constant.
func estimateTokens(messages []types.Message) int {
	total := 0
	for i, m := range messages {
		for _, seg := range m.TextSegments(i) {
			total += estimateTokenCount(seg.Text)
		}
		total += 4 // per-message role and delimiter overhead, as OpenAI documents it
	}
	return total
}

// estimateToolTokens approximates the input tokens a tool definition consumes.
//
// Tool definitions are billed as input, so a mock that ignored them would
// report prompt token counts that do not move when a request carries fifty
// tools, and the cost figure derived from them would be wrong in the direction
// that matters. Cost on a real provider comes from provider-reported usage, so
// this approximation affects the mock only.
func estimateToolTokens(tools []types.Tool) int {
	total := 0
	for _, t := range tools {
		total += estimateTokenCount(t.Function.Name)
		total += estimateTokenCount(t.Function.Description)
		total += estimateTokenCount(string(t.Function.Parameters))
		total += 8 // per-tool schema overhead
	}
	return total
}

// estimateTokenCount uses the widely quoted four-characters-per-token rule of
// thumb, with a floor of one token for any non-empty string.
func estimateTokenCount(s string) int {
	if s == "" {
		return 0
	}
	n := len(s) / 4
	if n < 1 {
		n = 1
	}
	return n
}
