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

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/redact"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// OpenAIAdapter handles communication with OpenAI-compatible APIs.
// Since AEGIS uses the OpenAI format as canonical, this adapter is mostly passthrough.
type OpenAIAdapter struct {
	cfg    config.ProviderConfig
	client *http.Client
}

func NewOpenAIAdapter(cfg config.ProviderConfig, client *http.Client) *OpenAIAdapter {
	return &OpenAIAdapter{cfg: cfg, client: client}
}

func (a *OpenAIAdapter) Name() string { return "openai" }

func (a *OpenAIAdapter) SupportsStreaming() bool { return true }

// SupportsTools reports that this adapter carries tool definitions, tool calls
// and tool results to the provider unchanged. AEGIS's canonical format is the
// OpenAI one, so there is nothing to translate.
func (a *OpenAIAdapter) SupportsTools() bool { return true }

func (a *OpenAIAdapter) TransformRequest(ctx context.Context, req *types.AegisRequest) (*http.Request, error) {
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

	// Always ask for usage on a stream.
	//
	// OpenAI omits the usage block from a streamed response unless asked, so
	// the gateway saw no token counts, recorded zero, and priced the request at
	// zero. That is not a reporting gap: the daily spend budget is computed
	// from those records, so streamed traffic cost real money and moved no
	// budget.
	//
	// This is the gateway asking for its own accounting data, not a client
	// field being honoured. stream_options stays refused on the way in, because
	// a caller cannot be allowed to turn the gateway's spend tracking off. The
	// client sees the same extra usage chunk it would have got by asking, which
	// is ordinary OpenAI behaviour.
	if req.Stream {
		body.StreamOptions = &openAIStreamOptions{IncludeUsage: true}
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}

	url := a.cfg.BaseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
	for k, v := range a.cfg.Headers {
		if v != "" {
			httpReq.Header.Set(k, v)
		}
	}

	return httpReq, nil
}

func (a *OpenAIAdapter) TransformResponse(ctx context.Context, resp *http.Response) (*types.AegisResponse, error) {
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read openai response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// redact.Excerpt, not string(body): this error is logged by the
		// handler, and a provider error body is unbounded text the gateway
		// does not control which routinely echoes the caller's own content
		// back. Logging it verbatim puts caller text into the log store.
		return nil, fmt.Errorf("openai returned status %d: %s", resp.StatusCode, redact.Excerpt(body))
	}

	var oaiResp openAIResponseBody
	if err := json.Unmarshal(body, &oaiResp); err != nil {
		return nil, fmt.Errorf("unmarshal openai response: %w", err)
	}

	aegisResp := &types.AegisResponse{
		Model:    oaiResp.Model,
		Provider: "openai",
	}
	if u := oaiResp.Usage; u != nil {
		aegisResp.UsageReported = true
		aegisResp.Usage = types.Usage{
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			TotalTokens:      u.TotalTokens,
		}
		// prompt_tokens already includes the cached portion, so this is carried
		// through as the subset it is.
		if d := u.PromptTokensDetails; d != nil && d.CachedTokens > 0 {
			aegisResp.Usage.PromptTokensDetails = &types.PromptTokensDetails{CachedTokens: d.CachedTokens}
		}
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

func (a *OpenAIAdapter) TransformStreamChunk(chunk []byte) ([]byte, error) {
	// OpenAI streaming chunks are already in the correct format
	return chunk, nil
}

func (a *OpenAIAdapter) SendRequest(req *http.Request) (*http.Response, error) {
	return a.client.Do(req)
}

type openAIRequestBody struct {
	Model       string          `json:"model"`
	Messages    []types.Message `json:"messages"`
	Stream      bool            `json:"stream,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stop        []string        `json:"stop,omitempty"`

	// Tool calling. ToolChoice is a pointer because its zero value marshals to
	// null, and a null tool_choice is not the same request as an absent one.
	Tools             []types.Tool      `json:"tools,omitempty"`
	ToolChoice        *types.ToolChoice `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`

	// StreamOptions is set by the gateway, never by the caller. See
	// TransformRequest.
	StreamOptions *openAIStreamOptions `json:"stream_options,omitempty"`
}

// openAIStreamOptions asks the provider to include a usage block in the stream.
type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIResponseBody struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int           `json:"index"`
		Message      types.Message `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	// A pointer so that a response carrying no usage object is distinguishable
	// from one carrying zeros. audit_events seals the counts and must not
	// record an absent measurement as a reported zero.
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
		// OpenAI caches prompts automatically above a size threshold, with no
		// opt-in from the caller, so this can be non-zero on any route.
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}
