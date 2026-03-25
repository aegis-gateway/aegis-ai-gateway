package types

import (
	"encoding/json"
	"testing"
)

func TestAegisRequest_JSONRoundTrip(t *testing.T) {
	temp := 0.7
	maxTok := 1000
	req := AegisRequest{
		Model: "gpt-4o",
		Messages: []Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
		},
		Temperature: &temp,
		MaxTokens:   &maxTok,
		Stream:      true,
		Stop:        []string{"END"},
		Project:     "test-project",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded AegisRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded.Model != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", decoded.Model)
	}
	if len(decoded.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(decoded.Messages))
	}
	if decoded.Messages[0].Role != "system" || decoded.Messages[0].Content != "You are helpful." {
		t.Errorf("unexpected first message: %+v", decoded.Messages[0])
	}
	if decoded.Temperature == nil || *decoded.Temperature != 0.7 {
		t.Errorf("expected temperature 0.7, got %v", decoded.Temperature)
	}
	if decoded.MaxTokens == nil || *decoded.MaxTokens != 1000 {
		t.Errorf("expected max_tokens 1000, got %v", decoded.MaxTokens)
	}
	if !decoded.Stream {
		t.Error("expected stream true")
	}
	if len(decoded.Stop) != 1 || decoded.Stop[0] != "END" {
		t.Errorf("unexpected stop: %v", decoded.Stop)
	}
	if decoded.Project != "test-project" {
		t.Errorf("expected project test-project, got %s", decoded.Project)
	}
}

func TestAegisRequest_OmitsOptionalFields(t *testing.T) {
	req := AegisRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var raw map[string]interface{}
	_ = json.Unmarshal(data, &raw)

	// temperature, max_tokens, top_p, stop should be absent
	for _, field := range []string{"temperature", "max_tokens", "top_p", "stop"} {
		if _, ok := raw[field]; ok {
			t.Errorf("expected %s to be omitted when nil, but it was present", field)
		}
	}
}

func TestAegisRequest_ReceivedAtNotSerialized(t *testing.T) {
	req := AegisRequest{Model: "gpt-4o"}
	data, _ := json.Marshal(req)
	if json.Valid(data) {
		var raw map[string]interface{}
		_ = json.Unmarshal(data, &raw)
		if _, ok := raw["received_at"]; ok {
			t.Error("ReceivedAt should not be serialized (json:\"-\")")
		}
	}
}

func TestAegisResponse_JSONRoundTrip(t *testing.T) {
	resp := AegisResponse{
		RequestID:        "req-123",
		Model:            "gpt-4o",
		Provider:         "openai",
		EstimatedCostUSD: 0.0075,
		Choices: []Choice{
			{
				Index:        0,
				Message:      Message{Role: "assistant", Content: "Hello!"},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded AegisResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded.RequestID != "req-123" {
		t.Errorf("expected request_id req-123, got %s", decoded.RequestID)
	}
	if decoded.Provider != "openai" {
		t.Errorf("expected provider openai, got %s", decoded.Provider)
	}
	if decoded.EstimatedCostUSD != 0.0075 {
		t.Errorf("expected cost 0.0075, got %f", decoded.EstimatedCostUSD)
	}
	if len(decoded.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(decoded.Choices))
	}
	if decoded.Choices[0].Message.Content != "Hello!" {
		t.Errorf("unexpected content: %s", decoded.Choices[0].Message.Content)
	}
	if decoded.Usage.TotalTokens != 150 {
		t.Errorf("expected total_tokens 150, got %d", decoded.Usage.TotalTokens)
	}
}

func TestAegisResponse_FilterSummary(t *testing.T) {
	resp := AegisResponse{
		FilterActions: FilterSummary{
			Secrets:   FilterAction{Action: "pass"},
			Injection: FilterAction{Action: "flag", Score: 0.75, Detections: 2},
		},
	}

	data, _ := json.Marshal(resp)
	var raw map[string]interface{}
	_ = json.Unmarshal(data, &raw)

	fa, ok := raw["filter_actions"].(map[string]interface{})
	if !ok {
		t.Fatal("expected filter_actions in JSON")
	}

	inj, ok := fa["injection"].(map[string]interface{})
	if !ok {
		t.Fatal("expected injection in filter_actions")
	}
	if inj["action"] != "flag" {
		t.Errorf("expected injection action flag, got %v", inj["action"])
	}
	if inj["score"].(float64) != 0.75 {
		t.Errorf("expected score 0.75, got %v", inj["score"])
	}
}

func TestUsage_ZeroValues(t *testing.T) {
	u := Usage{}
	data, _ := json.Marshal(u)
	var decoded Usage
	_ = json.Unmarshal(data, &decoded)

	if decoded.PromptTokens != 0 || decoded.CompletionTokens != 0 || decoded.TotalTokens != 0 {
		t.Errorf("expected all zeros, got %+v", decoded)
	}
}

func TestMessage_WithName(t *testing.T) {
	m := Message{Role: "user", Content: "Hi", Name: "alice"}
	data, _ := json.Marshal(m)

	var raw map[string]interface{}
	_ = json.Unmarshal(data, &raw)

	if raw["name"] != "alice" {
		t.Errorf("expected name alice, got %v", raw["name"])
	}
}

func TestMessage_OmitsEmptyName(t *testing.T) {
	m := Message{Role: "user", Content: "Hi"}
	data, _ := json.Marshal(m)

	var raw map[string]interface{}
	_ = json.Unmarshal(data, &raw)

	if _, ok := raw["name"]; ok {
		t.Error("expected name to be omitted when empty")
	}
}
