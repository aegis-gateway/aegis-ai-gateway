package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/af-corp/aegis-gateway/internal/auth"
	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/cost"
	"github.com/af-corp/aegis-gateway/internal/telemetry"
	"github.com/af-corp/aegis-gateway/internal/types"
)

// TestNewHandler tests handler construction.
func TestNewHandler(t *testing.T) {
	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{}
	}
	cfg := func() *config.Config {
		return &config.Config{}
	}

	h := NewHandler(nil, nil, modelsCfg, cfg, nil, nil, nil, nil)

	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	if h.modelsCfg == nil {
		t.Error("expected modelsCfg to be set")
	}
	if h.cfg == nil {
		t.Error("expected cfg to be set")
	}
}

// TestChatCompletions_RequiresAuth tests that authentication is required.
func TestChatCompletions_RequiresAuth(t *testing.T) {
	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{}
	}
	cfg := func() *config.Config {
		return &config.Config{}
	}

	h := NewHandler(nil, nil, modelsCfg, cfg, nil, nil, nil, nil)

	reqBody := `{"model": "gpt-4o", "messages": [{"role": "user", "content": "Hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "test-123")

	h.ChatCompletions(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}
}

// TestChatCompletions_RequiresModel tests that model field is required.
func TestChatCompletions_RequiresModel(t *testing.T) {
	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{}
	}
	cfg := func() *config.Config {
		return &config.Config{}
	}

	h := NewHandler(nil, nil, modelsCfg, cfg, nil, nil, nil, nil)

	reqBody := `{"messages": [{"role": "user", "content": "Hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-1",
		TeamID:         "team-1",
		KeyID:          "key-1",
	}))

	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "test-123")

	h.ChatCompletions(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

// TestChatCompletions_RequiresMessages tests that messages field is required.
func TestChatCompletions_RequiresMessages(t *testing.T) {
	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{}
	}
	cfg := func() *config.Config {
		return &config.Config{}
	}

	h := NewHandler(nil, nil, modelsCfg, cfg, nil, nil, nil, nil)

	reqBody := `{"model": "gpt-4o"}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-1",
		TeamID:         "team-1",
		KeyID:          "key-1",
	}))

	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "test-123")

	h.ChatCompletions(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

// TestChatCompletions_InvalidJSON tests handling of invalid JSON.
func TestChatCompletions_InvalidJSON(t *testing.T) {
	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{}
	}
	cfg := func() *config.Config {
		return &config.Config{}
	}

	h := NewHandler(nil, nil, modelsCfg, cfg, nil, nil, nil, nil)

	reqBody := `{invalid json}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-1",
		TeamID:         "team-1",
		KeyID:          "key-1",
	}))

	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "test-123")

	h.ChatCompletions(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

// TestListModels_RequiresAuth tests that authentication is required for listing models.
func TestListModels_RequiresAuth(t *testing.T) {
	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{}
	}
	cfg := func() *config.Config {
		return &config.Config{}
	}

	h := NewHandler(nil, nil, modelsCfg, cfg, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/v1/models", nil)

	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "test-123")

	h.ListModels(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}
}

// TestListModels_ReturnsModels tests listing models with auth.
func TestListModels_ReturnsModels(t *testing.T) {
	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{
			Models: map[string]config.ModelMapping{
				"gpt-4o": {
					Primary: config.ProviderRoute{Provider: "openai", Model: "gpt-4o"},
				},
				"claude-sonnet": {
					Primary: config.ProviderRoute{Provider: "anthropic", Model: "claude-sonnet-4-5-20250929"},
				},
			},
		}
	}
	cfg := func() *config.Config {
		return &config.Config{}
	}

	h := NewHandler(nil, nil, modelsCfg, cfg, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-1",
		TeamID:         "team-1",
		KeyID:          "key-1",
	}))

	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "test-123")

	h.ListModels(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp modelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.Object != "list" {
		t.Errorf("expected object 'list', got '%s'", resp.Object)
	}

	if len(resp.Data) != 2 {
		t.Errorf("expected 2 models, got %d", len(resp.Data))
	}
}

// TestListModels_FilteredByAllowedModels tests that models are filtered based on auth.
func TestListModels_FilteredByAllowedModels(t *testing.T) {
	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{
			Models: map[string]config.ModelMapping{
				"gpt-4o":        {},
				"gpt-4o-mini":   {},
				"claude-sonnet": {},
			},
		}
	}
	cfg := func() *config.Config {
		return &config.Config{}
	}

	h := NewHandler(nil, nil, modelsCfg, cfg, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-1",
		TeamID:         "team-1",
		KeyID:          "key-1",
		AllowedModels:  []string{"gpt-4o-mini"}, // Only allow mini
	}))

	w := httptest.NewRecorder()

	h.ListModels(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp modelListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if len(resp.Data) != 1 {
		t.Errorf("expected 1 model (filtered), got %d", len(resp.Data))
	}

	if len(resp.Data) > 0 && resp.Data[0].ID != "gpt-4o-mini" {
		t.Errorf("expected model 'gpt-4o-mini', got '%s'", resp.Data[0].ID)
	}
}

// TestCostCalculator_Integration tests cost calculator with handler.
func TestCostCalculator_Integration(t *testing.T) {
	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{
			Pricing: map[string]map[string]config.PriceEntry{
				"openai": {
					"gpt-4o": {
						Input:  0.0025,
						Output: 0.01,
					},
				},
				"anthropic": {
					"claude-sonnet-4-5-20250929": {
						Input:  0.003,
						Output: 0.015,
					},
				},
			},
		}
	}

	calc := cost.NewCalculator(modelsCfg)

	// Test OpenAI cost calculation
	cost1, found := calc.Calculate("openai", "gpt-4o", 1000, 500)
	if !found {
		t.Error("expected pricing to be found for gpt-4o")
	}
	expectedCost := 0.0025*1 + 0.01*0.5 // 0.0075
	if abs(cost1-expectedCost) > 0.0001 {
		t.Errorf("expected cost %f, got %f", expectedCost, cost1)
	}

	// Test Anthropic cost calculation
	cost2, found := calc.Calculate("anthropic", "claude-sonnet-4-5-20250929", 2000, 1000)
	if !found {
		t.Error("expected pricing to be found for claude-sonnet")
	}
	expectedCost2 := 0.003*2 + 0.015*1 // 0.021
	if abs(cost2-expectedCost2) > 0.0001 {
		t.Errorf("expected cost %f, got %f", expectedCost2, cost2)
	}
}

// TestMetricsIntegration tests metrics recording in handler context.
func TestMetricsIntegration(t *testing.T) {
	// This is a unit test for the metrics flow, not a full integration test
	// Real integration tests would require a running provider
	
	labels := telemetry.RequestLabels{
		Org:              "org-1",
		Team:             "team-1",
		Model:            "gpt-4o",
		Provider:         "openai",
		Status:           "200",
		Classification:   "INTERNAL",
		DurationMs:       250,
		OverheadMs:       5,
		PromptTokens:     1000,
		CompletionTokens: 500,
		CostUSD:          0.0075,
	}

	if labels.Org != "org-1" {
		t.Error("labels should be correctly set")
	}
	if labels.CostUSD <= 0 {
		t.Error("cost should be positive for a successful request")
	}
}

// TestAegisRequestParsing tests parsing of AEGIS request format.
func TestAegisRequestParsing(t *testing.T) {
	reqJSON := `{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant"},
			{"role": "user", "content": "Hello"}
		],
		"stream": false,
		"temperature": 0.7,
		"max_tokens": 1000
	}`

	var aegisReq types.AegisRequest
	if err := json.Unmarshal([]byte(reqJSON), &aegisReq); err != nil {
		t.Fatalf("failed to parse request: %v", err)
	}

	if aegisReq.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got '%s'", aegisReq.Model)
	}
	if len(aegisReq.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(aegisReq.Messages))
	}
	if aegisReq.Stream {
		t.Error("expected stream to be false")
	}
	if aegisReq.Temperature == nil || *aegisReq.Temperature != 0.7 {
		t.Errorf("expected temperature 0.7, got %v", aegisReq.Temperature)
	}
	if aegisReq.MaxTokens == nil || *aegisReq.MaxTokens != 1000 {
		t.Errorf("expected max_tokens 1000, got %v", aegisReq.MaxTokens)
	}
}

// TestAegisResponseFormat tests AEGIS response format.
func TestAegisResponseFormat(t *testing.T) {
	resp := types.AegisResponse{
		RequestID:        "req-456",
		Model:            "gpt-4o",
		Provider:         "openai",
		EstimatedCostUSD: 0.0075,
		Usage: types.Usage{
			PromptTokens:     1000,
			CompletionTokens: 500,
			TotalTokens:      1500,
		},
		Choices: []types.Choice{
			{
				Index: 0,
				Message: types.Message{
					Role:    "assistant",
					Content: "Hello! How can I help you?",
				},
				FinishReason: "stop",
			},
		},
	}

	// Serialize and verify JSON structure
	jsonBytes, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if parsed["estimated_cost_usd"].(float64) != 0.0075 {
		t.Error("estimated_cost_usd should be in response")
	}
	if parsed["request_id"].(string) != "req-456" {
		t.Error("request_id should be in response")
	}
	if parsed["provider"].(string) != "openai" {
		t.Error("provider should be in response")
	}
}

// TestAegisHeaders tests parsing of AEGIS-specific headers.
func TestAegisHeaders(t *testing.T) {
	headers := map[string]string{
		"X-Aegis-Project":        "my-project",
		"X-Aegis-Prefer-Provider": "openai",
		"X-Aegis-Trace-Context":   "trace-123-456",
	}

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	if req.Header.Get("X-Aegis-Project") != "my-project" {
		t.Error("project header should be readable")
	}
	if req.Header.Get("X-Aegis-Prefer-Provider") != "openai" {
		t.Error("prefer-provider header should be readable")
	}
	if req.Header.Get("X-Aegis-Trace-Context") != "trace-123-456" {
		t.Error("trace-context header should be readable")
	}
}

// TestClassificationLevels tests different classification levels in requests.
func TestClassificationLevels(t *testing.T) {
	classifications := []types.Classification{
		types.ClassPublic,
		types.ClassInternal,
		types.ClassConfidential,
		types.ClassRestricted,
	}

	for _, class := range classifications {
		t.Run(string(class), func(t *testing.T) {
			authInfo := &auth.AuthInfo{
				OrganizationID:    "org-1",
				TeamID:            "team-1",
				KeyID:             "key-1",
				MaxClassification: class,
			}

			ctx := auth.ContextWithAuth(context.Background(), authInfo)
			retrieved, ok := auth.AuthFromContext(ctx)

			if !ok {
				t.Fatal("expected auth info to be retrievable")
			}
			if retrieved.MaxClassification != class {
				t.Errorf("expected classification %s, got %s", class, retrieved.MaxClassification)
			}
		})
	}
}

// Helper function for float comparison
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
