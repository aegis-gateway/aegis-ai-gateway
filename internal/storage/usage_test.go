package storage

import (
	"context"
	"testing"
	"time"
)

// TestUsageRecord_Fields ensures the UsageRecord struct has all necessary fields.
func TestUsageRecord_Fields(t *testing.T) {
	record := UsageRecord{
		RequestID:        "req-123",
		OrganizationID:   "org-456",
		TeamID:           "team-789",
		UserID:           "user-abc",
		APIKeyID:         "key-def",
		ModelRequested:   "gpt-4",
		ModelServed:      "gpt-4o",
		Provider:         "openai",
		Classification:   "INTERNAL",
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		EstimatedCostUSD: 0.0075,
		DurationMs:       250,
		StatusCode:       200,
		Project:          "my-project",
		Stream:           false,
	}

	// Verify all fields are correctly set
	if record.RequestID != "req-123" {
		t.Errorf("expected RequestID 'req-123', got '%s'", record.RequestID)
	}
	if record.OrganizationID != "org-456" {
		t.Errorf("expected OrganizationID 'org-456', got '%s'", record.OrganizationID)
	}
	if record.TeamID != "team-789" {
		t.Errorf("expected TeamID 'team-789', got '%s'", record.TeamID)
	}
	if record.UserID != "user-abc" {
		t.Errorf("expected UserID 'user-abc', got '%s'", record.UserID)
	}
	if record.APIKeyID != "key-def" {
		t.Errorf("expected APIKeyID 'key-def', got '%s'", record.APIKeyID)
	}
	if record.ModelRequested != "gpt-4" {
		t.Errorf("expected ModelRequested 'gpt-4', got '%s'", record.ModelRequested)
	}
	if record.ModelServed != "gpt-4o" {
		t.Errorf("expected ModelServed 'gpt-4o', got '%s'", record.ModelServed)
	}
	if record.Provider != "openai" {
		t.Errorf("expected Provider 'openai', got '%s'", record.Provider)
	}
	if record.Classification != "INTERNAL" {
		t.Errorf("expected Classification 'INTERNAL', got '%s'", record.Classification)
	}
	if record.PromptTokens != 100 {
		t.Errorf("expected PromptTokens 100, got %d", record.PromptTokens)
	}
	if record.CompletionTokens != 50 {
		t.Errorf("expected CompletionTokens 50, got %d", record.CompletionTokens)
	}
	if record.TotalTokens != 150 {
		t.Errorf("expected TotalTokens 150, got %d", record.TotalTokens)
	}
	if record.EstimatedCostUSD != 0.0075 {
		t.Errorf("expected EstimatedCostUSD 0.0075, got %f", record.EstimatedCostUSD)
	}
	if record.DurationMs != 250 {
		t.Errorf("expected DurationMs 250, got %d", record.DurationMs)
	}
	if record.StatusCode != 200 {
		t.Errorf("expected StatusCode 200, got %d", record.StatusCode)
	}
	if record.Project != "my-project" {
		t.Errorf("expected Project 'my-project', got '%s'", record.Project)
	}
	if record.Stream != false {
		t.Errorf("expected Stream false, got %v", record.Stream)
	}
}

// TestUsageRecord_StreamingRequest tests the streaming flag.
func TestUsageRecord_StreamingRequest(t *testing.T) {
	record := UsageRecord{
		RequestID: "stream-req-1",
		Stream:    true,
	}

	if !record.Stream {
		t.Error("expected Stream to be true for streaming request")
	}
}

// TestUsageRecord_ZeroTokens tests handling of zero token counts.
func TestUsageRecord_ZeroTokens(t *testing.T) {
	record := UsageRecord{
		RequestID:        "zero-tokens",
		PromptTokens:     0,
		CompletionTokens: 0,
		TotalTokens:      0,
		EstimatedCostUSD: 0.0,
	}

	if record.PromptTokens != 0 {
		t.Errorf("expected PromptTokens 0, got %d", record.PromptTokens)
	}
	if record.EstimatedCostUSD != 0.0 {
		t.Errorf("expected EstimatedCostUSD 0.0, got %f", record.EstimatedCostUSD)
	}
}

// TestUsageRecord_HighTokenCounts tests handling of large token counts.
func TestUsageRecord_HighTokenCounts(t *testing.T) {
	record := UsageRecord{
		RequestID:        "high-tokens",
		PromptTokens:     100000,
		CompletionTokens: 50000,
		TotalTokens:      150000,
		EstimatedCostUSD: 15.50,
	}

	if record.TotalTokens != 150000 {
		t.Errorf("expected TotalTokens 150000, got %d", record.TotalTokens)
	}
}

// TestUsageRecord_DifferentProviders tests records for different providers.
func TestUsageRecord_DifferentProviders(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
	}{
		{"OpenAI", "openai", "gpt-4o"},
		{"Anthropic", "anthropic", "claude-sonnet-4-5-20250929"},
		{"Azure OpenAI", "azure_openai", "gpt-4o"},
		{"Google", "google", "gemini-1.5-pro"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := UsageRecord{
				RequestID:    "req-" + tt.provider,
				Provider:     tt.provider,
				ModelServed:  tt.model,
			}

			if record.Provider != tt.provider {
				t.Errorf("expected Provider '%s', got '%s'", tt.provider, record.Provider)
			}
			if record.ModelServed != tt.model {
				t.Errorf("expected ModelServed '%s', got '%s'", tt.model, record.ModelServed)
			}
		})
	}
}

// TestUsageRecord_ClassificationLevels tests different classification levels.
func TestUsageRecord_ClassificationLevels(t *testing.T) {
	classifications := []string{"PUBLIC", "INTERNAL", "CONFIDENTIAL", "RESTRICTED"}

	for _, class := range classifications {
		t.Run(class, func(t *testing.T) {
			record := UsageRecord{
				RequestID:      "req-" + class,
				Classification: class,
			}

			if record.Classification != class {
				t.Errorf("expected Classification '%s', got '%s'", class, record.Classification)
			}
		})
	}
}

// TestUsageRecord_StatusCodes tests different HTTP status codes.
func TestUsageRecord_StatusCodes(t *testing.T) {
	statusCodes := []int{200, 400, 401, 403, 429, 500, 503}

	for _, code := range statusCodes {
		t.Run(string(rune(code)), func(t *testing.T) {
			record := UsageRecord{
				RequestID:  "req-status",
				StatusCode: code,
			}

			if record.StatusCode != code {
				t.Errorf("expected StatusCode %d, got %d", code, record.StatusCode)
			}
		})
	}
}

// TestUsageSummary_Fields tests the UsageSummary struct.
func TestUsageSummary_Fields(t *testing.T) {
	summary := UsageSummary{
		TotalRequests:     1000,
		TotalCostUSD:      150.50,
		TotalTokens:       5000000,
		PromptTokens:      3000000,
		CompletionTokens:  2000000,
		AverageDurationMs: 245.5,
	}

	if summary.TotalRequests != 1000 {
		t.Errorf("expected TotalRequests 1000, got %d", summary.TotalRequests)
	}
	if summary.TotalCostUSD != 150.50 {
		t.Errorf("expected TotalCostUSD 150.50, got %f", summary.TotalCostUSD)
	}
	if summary.TotalTokens != 5000000 {
		t.Errorf("expected TotalTokens 5000000, got %d", summary.TotalTokens)
	}
	if summary.PromptTokens != 3000000 {
		t.Errorf("expected PromptTokens 3000000, got %d", summary.PromptTokens)
	}
	if summary.CompletionTokens != 2000000 {
		t.Errorf("expected CompletionTokens 2000000, got %d", summary.CompletionTokens)
	}
	if summary.AverageDurationMs != 245.5 {
		t.Errorf("expected AverageDurationMs 245.5, got %f", summary.AverageDurationMs)
	}
}

// TestUsageSummary_ZeroValues tests summary with zero values.
func TestUsageSummary_ZeroValues(t *testing.T) {
	summary := UsageSummary{}

	if summary.TotalRequests != 0 {
		t.Errorf("expected TotalRequests 0, got %d", summary.TotalRequests)
	}
	if summary.TotalCostUSD != 0 {
		t.Errorf("expected TotalCostUSD 0, got %f", summary.TotalCostUSD)
	}
}

// TestNewUsageRecorder tests creating a UsageRecorder with nil pool.
// In production, pool would be non-nil, but this tests struct initialization.
func TestNewUsageRecorder(t *testing.T) {
	recorder := NewUsageRecorder(nil)

	if recorder == nil {
		t.Error("expected non-nil UsageRecorder")
	}
}

// TestUsageRecord_ModelMapping tests when requested model differs from served model.
func TestUsageRecord_ModelMapping(t *testing.T) {
	tests := []struct {
		name           string
		modelRequested string
		modelServed    string
	}{
		{"GPT-4 to GPT-4o", "gpt-4", "gpt-4o"},
		{"Claude to Claude Sonnet", "claude", "claude-sonnet-4-5-20250929"},
		{"Same model", "gpt-4o-mini", "gpt-4o-mini"},
		{"Alias resolution", "claude-3-sonnet", "claude-sonnet-4-5-20250929"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := UsageRecord{
				RequestID:      "req-mapping",
				ModelRequested: tt.modelRequested,
				ModelServed:    tt.modelServed,
			}

			if record.ModelRequested != tt.modelRequested {
				t.Errorf("expected ModelRequested '%s', got '%s'", tt.modelRequested, record.ModelRequested)
			}
			if record.ModelServed != tt.modelServed {
				t.Errorf("expected ModelServed '%s', got '%s'", tt.modelServed, record.ModelServed)
			}
		})
	}
}

// TestUsageRecord_LongDuration tests handling of long request durations.
func TestUsageRecord_LongDuration(t *testing.T) {
	// 5 minute duration in milliseconds
	longDuration := int64(5 * 60 * 1000)

	record := UsageRecord{
		RequestID:  "long-req",
		DurationMs: longDuration,
	}

	if record.DurationMs != longDuration {
		t.Errorf("expected DurationMs %d, got %d", longDuration, record.DurationMs)
	}
}

// TestUsageRecord_EmptyProject tests handling of empty project field.
func TestUsageRecord_EmptyProject(t *testing.T) {
	record := UsageRecord{
		RequestID: "no-project",
		Project:   "",
	}

	if record.Project != "" {
		t.Errorf("expected empty Project, got '%s'", record.Project)
	}
}

// TestUsageRecord_SpecialCharactersInProject tests project names with special characters.
func TestUsageRecord_SpecialCharactersInProject(t *testing.T) {
	projects := []string{
		"my-project",
		"my_project",
		"my.project",
		"project/subproject",
		"Project With Spaces",
		"项目名称",
		"🚀-project",
	}

	for _, project := range projects {
		t.Run(project, func(t *testing.T) {
			record := UsageRecord{
				RequestID: "req-special",
				Project:   project,
			}

			if record.Project != project {
				t.Errorf("expected Project '%s', got '%s'", project, record.Project)
			}
		})
	}
}

// mockQueryTime simulates query time ranges for testing.
func TestQueryTimeRanges(t *testing.T) {
	now := time.Now()
	
	tests := []struct {
		name      string
		startTime time.Time
		endTime   time.Time
		valid     bool
	}{
		{
			name:      "Last hour",
			startTime: now.Add(-1 * time.Hour),
			endTime:   now,
			valid:     true,
		},
		{
			name:      "Last 24 hours",
			startTime: now.Add(-24 * time.Hour),
			endTime:   now,
			valid:     true,
		},
		{
			name:      "Last 7 days",
			startTime: now.Add(-7 * 24 * time.Hour),
			endTime:   now,
			valid:     true,
		},
		{
			name:      "Last 30 days",
			startTime: now.Add(-30 * 24 * time.Hour),
			endTime:   now,
			valid:     true,
		},
		{
			name:      "Inverted range (should still work as params)",
			startTime: now,
			endTime:   now.Add(-1 * time.Hour),
			valid:     true, // SQL would return empty, but struct accepts it
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just verify time range is valid for context usage
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if ctx.Err() != nil {
				t.Errorf("context should not be cancelled yet")
			}

			if tt.startTime.After(tt.endTime) && tt.valid {
				// This is an edge case - inverted ranges are technically valid parameters
				t.Logf("Note: inverted time range %s", tt.name)
			}
		})
	}
}
