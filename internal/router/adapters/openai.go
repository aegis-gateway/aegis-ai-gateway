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

func (a *OpenAIAdapter) TransformRequest(ctx context.Context, req *types.AegisRequest) (*http.Request, error) {
	body := openAIRequestBody{
		Model:       req.Model,
		Messages:    req.Messages,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		TopP:        req.TopP,
		Stop:        req.Stop,
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
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read openai response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai returned status %d: %s", resp.StatusCode, string(body))
	}

	var oaiResp openAIResponseBody
	if err := json.Unmarshal(body, &oaiResp); err != nil {
		return nil, fmt.Errorf("unmarshal openai response: %w", err)
	}

	aegisResp := &types.AegisResponse{
		Model:    oaiResp.Model,
		Provider: "openai",
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
				Role:    c.Message.Role,
				Content: c.Message.Content,
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
}

type openAIResponseBody struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int    `json:"index"`
		Message      types.Message `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}
