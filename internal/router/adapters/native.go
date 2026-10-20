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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// Native Messages API endpoints, relative to the provider base URL.
const (
	NativeMessages    = "/messages"
	NativeCountTokens = "/messages/count_tokens"
)

// AnthropicNative is implemented by an adapter that can carry an Anthropic
// Messages API body to its provider unchanged. /v1/messages routes only to
// adapters that implement it: translating that body for any other provider
// would lose thinking signatures and cache markers, so a route that cannot
// take it natively is not eligible rather than approximated.
type AnthropicNative interface {
	ProviderAdapter
	// NativeRequest builds a request for endpoint carrying body as is, with
	// the provider's credential, anthropic-version, and the given betas.
	NativeRequest(ctx context.Context, endpoint string, body []byte, betas []string) (*http.Request, error)
}

// SupportsAnthropicNative reports whether a can serve /v1/messages.
func SupportsAnthropicNative(a ProviderAdapter) bool {
	_, ok := a.(AnthropicNative)
	return ok
}

// NativeRequest implements AnthropicNative.
func (a *AnthropicAdapter) NativeRequest(ctx context.Context, endpoint string, body []byte, betas []string) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.BaseURL+endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", defaultAnthropicVersion)
	for k, v := range a.cfg.Headers {
		if v != "" {
			httpReq.Header.Set(k, v)
		}
	}
	// After the configured headers, so a providers.yaml entry cannot replace
	// the credential or drop the betas the gateway decided to forward.
	httpReq.Header.Set("x-api-key", a.cfg.APIKey)
	if len(betas) > 0 {
		httpReq.Header.Set("anthropic-beta", strings.Join(betas, ","))
	} else {
		httpReq.Header.Del("anthropic-beta")
	}
	return httpReq, nil
}

// NativeUsage is what the gateway reads back from a Messages API response:
// enough to price and attest it, nothing of the content.
type NativeUsage struct {
	Model    string
	Usage    types.Usage
	Reported bool
}

// ParseNativeResponse reads the model and usage from a non-streamed Messages
// API response body.
func ParseNativeResponse(body []byte) (NativeUsage, error) {
	var resp struct {
		Model string          `json:"model"`
		Usage *anthropicUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return NativeUsage{}, fmt.Errorf("parse messages response: %w", err)
	}
	out := NativeUsage{Model: resp.Model}
	if resp.Usage != nil {
		if anthropicUsageHasNegative(*resp.Usage) {
			return out, fmt.Errorf("messages response reports a negative usage count")
		}
		out.Usage, out.Reported = anthropicUsageToCanonical(*resp.Usage), true
	}
	return out, nil
}

// NativeStreamUsage accumulates model and usage from a Messages API event
// stream as it is relayed. message_start carries the model and the input-side
// counts; each message_delta carries the cumulative output count.
type NativeStreamUsage struct {
	Model string
	raw   anthropicUsage
	seen  bool
}

// Observe reads one SSE data payload. Payloads that are not usage-bearing
// events are ignored, so it can be fed every data line unconditionally.
func (s *NativeStreamUsage) Observe(data []byte) {
	var ev struct {
		Type    string `json:"type"`
		Message *struct {
			Model string          `json:"model"`
			Usage *anthropicUsage `json:"usage"`
		} `json:"message"`
		Usage *anthropicUsage `json:"usage"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			s.Model = ev.Message.Model
			if ev.Message.Usage != nil {
				s.raw = *ev.Message.Usage
				s.seen = true
			}
		}
	case "message_delta":
		if ev.Usage != nil {
			// Cumulative, so the latest value replaces the earlier one. Input
			// fields are present on newer API versions; keep the start values
			// where the delta omits them.
			s.raw.OutputTokens = ev.Usage.OutputTokens
			if ev.Usage.InputTokens > 0 {
				s.raw.InputTokens = ev.Usage.InputTokens
			}
			if ev.Usage.CacheReadInputTokens > 0 || ev.Usage.CacheCreationInputTokens > 0 {
				s.raw.CacheReadInputTokens = ev.Usage.CacheReadInputTokens
				s.raw.CacheCreationInputTokens = ev.Usage.CacheCreationInputTokens
				s.raw.CacheCreation = ev.Usage.CacheCreation
			}
			s.seen = true
		}
	}
}

// Result returns the accumulated usage.
func (s *NativeStreamUsage) Result() (NativeUsage, error) {
	out := NativeUsage{Model: s.Model}
	if !s.seen {
		return out, nil
	}
	if anthropicUsageHasNegative(s.raw) {
		return out, fmt.Errorf("messages stream reports a negative usage count")
	}
	out.Usage, out.Reported = anthropicUsageToCanonical(s.raw), true
	return out, nil
}

// mockNativeHeader marks a request built by MockAdapter.NativeRequest, so its
// SendRequest answers in Messages API shape rather than chat completions.
const mockNativeHeader = "X-Aegis-Mock-Native"

// NativeRequest implements AnthropicNative for the mock, so /v1/messages runs
// end to end with AEGIS_MOCK_PROVIDER=true and no credential.
func (a *MockAdapter) NativeRequest(ctx context.Context, endpoint string, body []byte, betas []string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://mock.invalid"+endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set(mockNativeHeader, endpoint)
	return req, nil
}

func (a *MockAdapter) sendNative(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read mock request body: %w", err)
	}
	var sent struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(raw, &sent); err != nil {
		return nil, fmt.Errorf("unmarshal mock request body: %w", err)
	}
	input := estimateTokenCount(string(raw))
	if req.Header.Get(mockNativeHeader) == NativeCountTokens {
		body, _ := json.Marshal(map[string]int{"input_tokens": input})
		return newMockHTTPResponse(req, "application/json", body), nil
	}
	output := estimateTokenCount(mockCompletionText)
	if !sent.Stream {
		body, _ := json.Marshal(map[string]any{
			"id": "msg_aegis_mock", "type": "message", "role": "assistant", "model": sent.Model,
			"content":     []map[string]string{{"type": "text", "text": mockCompletionText}},
			"stop_reason": "end_turn", "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": input, "output_tokens": output},
		})
		return newMockHTTPResponse(req, "application/json", body), nil
	}
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	event := func(name string, payload any) {
		data, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
	}
	event("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": "msg_aegis_mock", "type": "message", "role": "assistant", "model": sent.Model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": input, "output_tokens": 1},
	}})
	event("content_block_start", map[string]any{"type": "content_block_start", "index": 0,
		"content_block": map[string]string{"type": "text", "text": ""}})
	event("ping", map[string]string{"type": "ping"})
	for _, piece := range strings.SplitAfter(mockCompletionText, " ") {
		event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]string{"type": "text_delta", "text": piece}})
	}
	event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	event("message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": output}})
	event("message_stop", map[string]string{"type": "message_stop"})
	_ = w.Flush()
	return newMockHTTPResponse(req, "text/event-stream", buf.Bytes()), nil
}
