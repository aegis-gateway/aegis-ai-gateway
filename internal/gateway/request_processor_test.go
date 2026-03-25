package gateway

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/af-corp/aegis-gateway/internal/auth"
	"github.com/af-corp/aegis-gateway/internal/types"
)

// mockValidator implements request validation for testing.
type mockValidator struct {
	shouldFail bool
	errorMsg   string
}

func (m *mockValidator) Validate(req *types.AegisRequest) error {
	if m.shouldFail {
		return &mockError{message: m.errorMsg}
	}
	return nil
}

type mockError struct {
	message string
}

func (e *mockError) Error() string {
	return e.message
}

func TestParseAndValidateRequest(t *testing.T) {
	tests := []struct {
		name        string
		requestBody string
		validator   *mockValidator
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid request",
			requestBody: `{
				"model": "gpt-4",
				"messages": [{"role": "user", "content": "Hello"}]
			}`,
			validator:   &mockValidator{shouldFail: false},
			expectError: false,
		},
		{
			name:        "invalid JSON",
			requestBody: `{invalid json}`,
			expectError: true,
			errorMsg:    "Invalid JSON",
		},
		{
			name: "validation failure",
			requestBody: `{
				"model": "gpt-4",
				"messages": [{"role": "user", "content": "Hello"}]
			}`,
			validator:   &mockValidator{shouldFail: true, errorMsg: "model not allowed"},
			expectError: true,
			errorMsg:    "model not allowed",
		},
		{
			name: "missing model - no validator",
			requestBody: `{
				"messages": [{"role": "user", "content": "Hello"}]
			}`,
			validator:   nil,
			expectError: true,
			errorMsg:    "model is required",
		},
		{
			name: "missing messages - no validator",
			requestBody: `{
				"model": "gpt-4",
				"messages": []
			}`,
			validator:   nil,
			expectError: true,
			errorMsg:    "messages is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create request
			req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(tt.requestBody))
			req.Header.Set("X-Aegis-Project", "test-project")
			
			authInfo := &auth.AuthInfo{
				OrganizationID:    "test-org",
				TeamID:            "test-team",
				UserID:            "test-user",
				KeyID:             "test-key",
				MaxClassification: types.ClassPublic,
			}

			var validator interface{ Validate(*types.AegisRequest) error }
			if tt.validator != nil {
				validator = tt.validator
			}
			processor := &RequestProcessor{
				validator: validator,
			}

			// Execute
			result, err := processor.ParseAndValidateRequest(req, "test-req-id", authInfo)

			// Verify
			if tt.expectError {
				if err == nil {
					t.Fatal("Expected error but got nil")
				}
				if tt.errorMsg != "" && !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error to contain '%s', got '%s'", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("Unexpected error: %v", err)
				}
				if result == nil {
					t.Fatal("Expected result but got nil")
				}
				if result.AegisRequest.RequestID != "test-req-id" {
					t.Errorf("Expected request ID 'test-req-id', got '%s'", result.AegisRequest.RequestID)
				}
				if result.AegisRequest.OrganizationID != "test-org" {
					t.Errorf("Expected org 'test-org', got '%s'", result.AegisRequest.OrganizationID)
				}
				if result.AegisRequest.Project != "test-project" {
					t.Errorf("Expected project 'test-project', got '%s'", result.AegisRequest.Project)
				}
			}
		})
	}
}

func TestRequestEnrichment(t *testing.T) {
	requestBody := `{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": "Hello"}]
	}`
	
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(requestBody))
	req.Header.Set("X-Aegis-Project", "my-project")
	req.Header.Set("X-Aegis-Prefer-Provider", "openai")
	req.Header.Set("X-Aegis-Trace-Context", "trace-123")
	
	authInfo := &auth.AuthInfo{
		OrganizationID:    "org-123",
		TeamID:            "team-456",
		UserID:            "user-789",
		KeyID:             "key-abc",
		MaxClassification: types.ClassRestricted,
	}

	processor := &RequestProcessor{}
	result, err := processor.ParseAndValidateRequest(req, "req-xyz", authInfo)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify all enrichments
	aegisReq := result.AegisRequest
	
	if aegisReq.RequestID != "req-xyz" {
		t.Errorf("Expected RequestID 'req-xyz', got '%s'", aegisReq.RequestID)
	}
	if aegisReq.OrganizationID != "org-123" {
		t.Errorf("Expected OrganizationID 'org-123', got '%s'", aegisReq.OrganizationID)
	}
	if aegisReq.TeamID != "team-456" {
		t.Errorf("Expected TeamID 'team-456', got '%s'", aegisReq.TeamID)
	}
	if aegisReq.UserID != "user-789" {
		t.Errorf("Expected UserID 'user-789', got '%s'", aegisReq.UserID)
	}
	if aegisReq.APIKeyID != "key-abc" {
		t.Errorf("Expected APIKeyID 'key-abc', got '%s'", aegisReq.APIKeyID)
	}
	if aegisReq.Classification != types.ClassRestricted {
		t.Errorf("Expected Classification 'RESTRICTED', got '%s'", aegisReq.Classification)
	}
	if aegisReq.Project != "my-project" {
		t.Errorf("Expected Project 'my-project', got '%s'", aegisReq.Project)
	}
	if aegisReq.PreferProvider != "openai" {
		t.Errorf("Expected PreferProvider 'openai', got '%s'", aegisReq.PreferProvider)
	}
	if aegisReq.TraceContext != "trace-123" {
		t.Errorf("Expected TraceContext 'trace-123', got '%s'", aegisReq.TraceContext)
	}
	if aegisReq.ReceivedAt.IsZero() {
		t.Error("Expected ReceivedAt to be set")
	}
}

// mockCostCalculator implements cost calculation for testing.
type mockCostCalculator struct {
	cost  float64
	found bool
}

func (m *mockCostCalculator) Calculate(provider, model string, promptTokens, completionTokens int) (float64, bool) {
	return m.cost, m.found
}

func TestResponseBuilder(t *testing.T) {
	calc := &mockCostCalculator{
		cost:  0.123,
		found: true,
	}

	builder := &ResponseBuilder{
		costCalc: calc,
	}

	aegisResp := &types.AegisResponse{
		Provider: "openai",
		Model:    "gpt-4",
		Usage: types.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}

	builder.BuildResponse(aegisResp, "req-123")

	// Verify enrichment
	if aegisResp.RequestID != "req-123" {
		t.Errorf("Expected RequestID 'req-123', got '%s'", aegisResp.RequestID)
	}
	if aegisResp.EstimatedCostUSD != 0.123 {
		t.Errorf("Expected cost 0.123, got %f", aegisResp.EstimatedCostUSD)
	}
}

