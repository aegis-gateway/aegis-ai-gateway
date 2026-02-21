package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/types"
)

// AnthropicAdapter handles communication with the Anthropic Messages API.
type AnthropicAdapter struct {
	cfg    config.ProviderConfig
	client *http.Client
}

func NewAnthropicAdapter(cfg config.ProviderConfig, client *http.Client) *AnthropicAdapter {
	return &AnthropicAdapter{cfg: cfg, client: client}
}

func (a *AnthropicAdapter) Name() string { return "anthropic" }

func (a *AnthropicAdapter) SupportsStreaming() bool { return true }

func (a *AnthropicAdapter) TransformRequest(ctx context.Context, req *types.AegisRequest) (*http.Request, error) {
	// Convert OpenAI-format messages to Anthropic format
	var system string
	var messages []anthropicMessage
	for _, m := range req.Messages {
		if m.Role == "system" {
			system = m.Content
			continue
		}
		messages = append(messages, anthropicMessage{
			Role:    m.Role,
			Content: m.Content,
		})
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
	for k, v := range a.cfg.Headers {
		if v != "" {
			httpReq.Header.Set(k, v)
		}
	}

	return httpReq, nil
}

func (a *AnthropicAdapter) TransformResponse(ctx context.Context, resp *http.Response) (*types.AegisResponse, error) {
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read anthropic response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic returned status %d: %s", resp.StatusCode, string(body))
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

	return &types.AegisResponse{
		Model:    antResp.Model,
		Provider: "anthropic",
		Choices: []types.Choice{
			{
				Index: 0,
				Message: types.Message{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: mapStopReason(antResp.StopReason),
			},
		},
		Usage: types.Usage{
			PromptTokens:     antResp.Usage.InputTokens,
			CompletionTokens: antResp.Usage.OutputTokens,
			TotalTokens:      antResp.Usage.InputTokens + antResp.Usage.OutputTokens,
		},
	}, nil
}

func (a *AnthropicAdapter) TransformStreamChunk(chunk []byte) ([]byte, error) {
	// Anthropic streaming format conversion will be implemented in the streaming step
	return chunk, nil
}

func mapStopReason(reason string) string {
	switch reason {
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

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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
}

type anthropicResponseBody struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Role       string `json:"role"`
	Model      string `json:"model"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}
