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

// AnthropicAdapter handles communication with the Anthropic Messages API.
type AnthropicAdapter struct {
	cfg    config.ProviderConfig
	client *http.Client
}

func NewAnthropicAdapter(cfg config.ProviderConfig, client *http.Client) *AnthropicAdapter {
	return &AnthropicAdapter{cfg: cfg, client: client}
}

// defaultAnthropicVersion is the API version the adapter speaks. configs/providers.yaml
// pins the same value; this is the floor if that block is ever absent.
const defaultAnthropicVersion = "2023-06-01"

func (a *AnthropicAdapter) Name() string { return "anthropic" }

func (a *AnthropicAdapter) SupportsStreaming() bool { return true }

// SupportsTools reports true.
//
// The Messages API expresses tools in a different shape from the OpenAI format
// AEGIS accepts: the schema lives under input_schema rather than in a function
// object under parameters, a tool call is a tool_use content block, a tool
// result is a tool_result block on a user turn, and in streaming the arguments
// arrive as input_json_delta fragments indexed against every content block
// rather than against the tool calls alone.
//
// All of that is translated in anthropic_tools.go and anthropic_stream.go,
// against behaviour probed from the live API rather than a remembered schema.
// Constructs that cannot be expressed are refused by name; see UnmappableError
// and docs/evidence/anthropic-tool-mapping.md.
func (a *AnthropicAdapter) SupportsTools() bool { return true }

func (a *AnthropicAdapter) TransformRequest(ctx context.Context, req *types.AegisRequest) (*http.Request, error) {
	system, messages, err := toAnthropicMessages(req.Messages)
	if err != nil {
		return nil, err
	}

	tools, err := toAnthropicTools(req.Tools)
	if err != nil {
		return nil, err
	}

	toolChoice, err := toAnthropicToolChoice(req.ToolChoice, req.ParallelToolCalls)
	if err != nil {
		return nil, err
	}

	// Anthropic requires max_tokens
	maxTokens := 4096
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	body := anthropicRequestBody{
		Model:       req.Model,
		Messages:    messages,
		System:      system,
		MaxTokens:   maxTokens,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
		Tools:       tools,
		ToolChoice:  toolChoice,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	url := a.cfg.BaseURL + "/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.cfg.APIKey)

	// anthropic-version is required by the API: without it every request is a
	// 400 saying so. It was supplied only by the headers block in
	// providers.yaml, which meant a deleted line or a typo there took out all
	// Anthropic traffic and the adapter itself was fine with that. Default it
	// here so the protocol requirement lives with the code that speaks the
	// protocol; an operator can still pin a different version below.
	httpReq.Header.Set("anthropic-version", defaultAnthropicVersion)

	for k, v := range a.cfg.Headers {
		if v != "" {
			httpReq.Header.Set(k, v)
		}
	}

	return httpReq, nil
}

func (a *AnthropicAdapter) TransformResponse(ctx context.Context, resp *http.Response) (*types.AegisResponse, error) {
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read anthropic response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// redact.Excerpt, not string(body): this error is logged by the
		// handler, and a provider error body is unbounded text the gateway
		// does not control which routinely echoes the caller's own content
		// back. Logging it verbatim puts caller text into the log store.
		return nil, fmt.Errorf("anthropic returned status %d: %s", resp.StatusCode, redact.Excerpt(body))
	}

	var antResp anthropicResponseBody
	if err := json.Unmarshal(body, &antResp); err != nil {
		return nil, fmt.Errorf("unmarshal anthropic response: %w", err)
	}

	// Convert Anthropic response to AEGIS canonical format
	var content string
	for _, block := range antResp.Content {
		if block.Type == "text" {
			content = block.Text
			break
		}
	}
	toolCalls := fromAnthropicToolUse(antResp.Content)

	out := &types.AegisResponse{
		Model:    antResp.Model,
		Provider: "anthropic",
		Choices: []types.Choice{
			{
				Index: 0,
				Message: types.Message{
					Role:      types.RoleAssistant,
					Content:   types.TextContent(content),
					ToolCalls: toolCalls,
				},
				FinishReason: mapStopReason(antResp.StopReason),
			},
		},
	}
	if antResp.Usage != nil {
		out.UsageReported = true
		out.Usage = anthropicUsageToCanonical(*antResp.Usage)
	}
	return out, nil
}

// TransformStreamChunk converts an Anthropic SSE data payload to OpenAI streaming format.
// Anthropic events: message_start, content_block_start, content_block_delta, message_delta, message_stop
// We convert content_block_delta (text) → OpenAI delta chunk, and message_stop → [DONE].
func (a *AnthropicAdapter) TransformStreamChunk(chunk []byte) ([]byte, error) {
	var event struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Type       string `json:"type"`
			Text       string `json:"text"`
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(chunk, &event); err != nil {
		return nil, nil // skip unparseable chunks
	}

	switch event.Type {
	case "content_block_delta":
		if event.Delta.Type == "text_delta" {
			oaiChunk := openAIStreamChunk{
				Choices: []openAIStreamChoice{
					{
						Index: event.Index,
						Delta: openAIDelta{Content: event.Delta.Text},
					},
				},
			}
			data, err := json.Marshal(oaiChunk)
			if err != nil {
				return nil, fmt.Errorf("marshal openai chunk: %w", err)
			}
			return data, nil
		}
		return nil, nil

	case "message_delta":
		// Final chunk with stop reason and usage
		finishReason := mapStopReason(event.Delta.StopReason)
		oaiChunk := openAIStreamChunk{
			Choices: []openAIStreamChoice{
				{
					Index:        0,
					Delta:        openAIDelta{},
					FinishReason: &finishReason,
				},
			},
		}
		data, err := json.Marshal(oaiChunk)
		if err != nil {
			return nil, fmt.Errorf("marshal openai finish chunk: %w", err)
		}
		return data, nil

	case "message_stop":
		// Signal end of stream — caller should send [DONE]
		return []byte("[DONE]"), nil

	default:
		// message_start, content_block_start, content_block_stop, ping — skip
		return nil, nil
	}
}

func (a *AnthropicAdapter) SendRequest(req *http.Request) (*http.Response, error) {
	return a.client.Do(req)
}

// OpenAI streaming format types
type openAIStreamChunk struct {
	Choices []openAIStreamChoice `json:"choices"`
}

type openAIStreamChoice struct {
	Index        int         `json:"index"`
	Delta        openAIDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type openAIDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

func mapStopReason(reason string) string {
	switch reason {
	case "tool_use":
		// Confirmed against the live API: a response that calls a tool stops
		// with "tool_use", which is OpenAI's "tool_calls".
		return "tool_calls"
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	default:
		return reason
	}
}

// anthropicMessage carries either plain text or a list of content blocks. The
// two are different wire shapes, and a tool call or a tool result can only be
// expressed as blocks.
type anthropicMessage struct {
	Role    string
	Content string
	Blocks  []anthropicContentBlock
}

func (m anthropicMessage) MarshalJSON() ([]byte, error) {
	if len(m.Blocks) > 0 {
		return json.Marshal(struct {
			Role    string                  `json:"role"`
			Content []anthropicContentBlock `json:"content"`
		}{m.Role, m.Blocks})
	}
	return json.Marshal(struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{m.Role, m.Content})
}

type anthropicRequestBody struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Stream      bool               `json:"stream,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	Stop        []string           `json:"stop_sequences,omitempty"`

	// Tool calling. ToolChoice also carries disable_parallel_tool_use, which
	// is where Anthropic expresses OpenAI's top-level parallel_tool_calls.
	Tools      []anthropicTool      `json:"tools,omitempty"`
	ToolChoice *anthropicToolChoice `json:"tool_choice,omitempty"`
}

type anthropicResponseBody struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Model      string                  `json:"model"`
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	// A pointer for the reason the OpenAI body uses one: an absent usage object
	// and one carrying zeros are different facts, and audit_events seals the
	// counts. Anthropic does return usage on a successful message, so this is
	// belt and braces rather than a known case, but asserting it unconditionally
	// would seal three zeros as measured for any response that did not.
	Usage *anthropicUsage `json:"usage"`
}

// anthropicUsageToCanonical converts Anthropic's token counts to the canonical
// shape, which follows OpenAI's convention.
//
// The two providers disagree about what the prompt count means. Anthropic's
// input_tokens EXCLUDES anything served from or written to the cache and
// reports those separately; OpenAI's prompt_tokens INCLUDES the cached portion
// and breaks it out as a subset. Verified against the live API: a cached call
// returned input_tokens 8 alongside cache_read_input_tokens 4411, for a prompt
// of 4419 tokens.
//
// So the total has to be reassembled here. Carrying Anthropic's input_tokens
// through as the prompt count would have understated that request by 4411
// tokens, and handing the same figure to the calculator as a subset would have
// made uncached input negative.
func anthropicUsageToCanonical(u anthropicUsage) types.Usage {
	prompt := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	usage := types.Usage{
		PromptTokens:     prompt,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      prompt + u.OutputTokens,
	}
	write5m, write1h := u.CacheCreation.Ephemeral5m, u.CacheCreation.Ephemeral1h
	if write5m == 0 && write1h == 0 && u.CacheCreationInputTokens > 0 {
		// Older responses report only the sum. Attribute it to the 5-minute
		// tier, the default TTL and the cheaper of the two, so an
		// unattributable write is never over-charged.
		write5m = u.CacheCreationInputTokens
	}
	if u.CacheReadInputTokens > 0 || write5m > 0 || write1h > 0 {
		usage.PromptTokensDetails = &types.PromptTokensDetails{
			CachedTokens:       u.CacheReadInputTokens,
			CacheWrite5mTokens: write5m,
			CacheWrite1hTokens: write1h,
		}
	}
	return usage
}
