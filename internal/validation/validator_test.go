package validation

import (
	"strings"
	"testing"

	"github.com/af-corp/aegis-gateway/internal/types"
)

func TestValidator_ValidateModel(t *testing.T) {
	validator := NewValidator(DefaultLimits(), nil)

	tests := []struct {
		name    string
		model   string
		wantErr bool
	}{
		{"valid model", "gpt-4", false},
		{"valid model with version", "gpt-4-0125-preview", false},
		{"valid model with colon", "azure:gpt-4", false},
		{"empty model", "", true},
		{"too long model", strings.Repeat("a", 300), true},
		{"invalid characters", "model<script>", true},
		{"valid underscores", "my_model_v1", false},
		{"valid dots", "model.v1.2", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.validateModel(tt.model)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateModel() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidator_ValidateMessages(t *testing.T) {
	validator := NewValidator(DefaultLimits(), nil)

	tests := []struct {
		name     string
		messages []types.Message
		wantErr  bool
	}{
		{
			name: "valid messages",
			messages: []types.Message{
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi there!"},
			},
			wantErr: false,
		},
		{
			name:     "empty messages",
			messages: []types.Message{},
			wantErr:  true,
		},
		{
			name: "missing role",
			messages: []types.Message{
				{Content: "Hello"},
			},
			wantErr: true,
		},
		{
			name: "invalid role",
			messages: []types.Message{
				{Role: "admin", Content: "Hello"},
			},
			wantErr: true,
		},
		{
			name: "message too long",
			messages: []types.Message{
				{Role: "user", Content: strings.Repeat("a", 200000)},
			},
			wantErr: true,
		},
		{
			name: "null byte in content",
			messages: []types.Message{
				{Role: "user", Content: "Hello\x00World"},
			},
			wantErr: true,
		},
		{
			name: "valid system message",
			messages: []types.Message{
				{Role: "system", Content: "You are a helpful assistant."},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validator.validateMessages(tt.messages)
			if (len(errs) > 0) != tt.wantErr {
				t.Errorf("validateMessages() errors = %v, wantErr %v", errs, tt.wantErr)
			}
		})
	}
}

func TestValidator_ValidateTemperature(t *testing.T) {
	validator := NewValidator(DefaultLimits(), nil)

	tests := []struct {
		name        string
		temperature float64
		wantErr     bool
	}{
		{"valid temperature 0.7", 0.7, false},
		{"valid temperature 0.0", 0.0, false},
		{"valid temperature 2.0", 2.0, false},
		{"temperature too low", -0.1, true},
		{"temperature too high", 2.5, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.validateTemperature(tt.temperature)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTemperature() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidator_ValidateMaxTokens(t *testing.T) {
	validator := NewValidator(DefaultLimits(), nil)

	tests := []struct {
		name      string
		maxTokens int
		wantErr   bool
	}{
		{"valid max_tokens", 1000, false},
		{"max tokens at limit", 128000, false},
		{"zero tokens", 0, true},
		{"negative tokens", -100, true},
		{"too many tokens", 200000, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.validateMaxTokens(tt.maxTokens)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateMaxTokens() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidator_ValidateTopP(t *testing.T) {
	validator := NewValidator(DefaultLimits(), nil)

	tests := []struct {
		name    string
		topP    float64
		wantErr bool
	}{
		{"valid top_p 0.9", 0.9, false},
		{"valid top_p 0.0", 0.0, false},
		{"valid top_p 1.0", 1.0, false},
		{"top_p too low", -0.1, true},
		{"top_p too high", 1.5, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.validateTopP(tt.topP)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTopP() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidator_ValidateStopSequences(t *testing.T) {
	validator := NewValidator(DefaultLimits(), nil)

	tests := []struct {
		name    string
		stop    []string
		wantErr bool
	}{
		{"valid stop sequences", []string{"\n", "END"}, false},
		{"too many sequences", []string{"1", "2", "3", "4", "5"}, true},
		{"sequence too long", []string{strings.Repeat("a", 300)}, true},
		{"empty sequence allowed", []string{""}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.validateStopSequences(tt.stop)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateStopSequences() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidator_Validate_FullRequest(t *testing.T) {
	validator := NewValidator(DefaultLimits(), nil)

	temp := 0.7
	maxTokens := 1000
	topP := 0.9

	tests := []struct {
		name    string
		req     *types.AegisRequest
		wantErr bool
	}{
		{
			name: "valid request",
			req: &types.AegisRequest{
				Model: "gpt-4",
				Messages: []types.Message{
					{Role: "user", Content: "Hello"},
				},
				Temperature: &temp,
				MaxTokens:   &maxTokens,
				TopP:        &topP,
			},
			wantErr: false,
		},
		{
			name: "minimal valid request",
			req: &types.AegisRequest{
				Model: "gpt-3.5-turbo",
				Messages: []types.Message{
					{Role: "user", Content: "Test"},
				},
			},
			wantErr: false,
		},
		{
			name: "missing model",
			req: &types.AegisRequest{
				Messages: []types.Message{
					{Role: "user", Content: "Hello"},
				},
			},
			wantErr: true,
		},
		{
			name: "missing messages",
			req: &types.AegisRequest{
				Model:    "gpt-4",
				Messages: []types.Message{},
			},
			wantErr: true,
		},
		{
			name: "invalid temperature",
			req: &types.AegisRequest{
				Model: "gpt-4",
				Messages: []types.Message{
					{Role: "user", Content: "Hello"},
				},
				Temperature: floatPtr(3.0),
			},
			wantErr: true,
		},
		{
			name: "invalid max_tokens",
			req: &types.AegisRequest{
				Model: "gpt-4",
				Messages: []types.Message{
					{Role: "user", Content: "Hello"},
				},
				MaxTokens: intPtr(-100),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.Validate(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidator_ValidationErrors_Error(t *testing.T) {
	errs := ValidationErrors{
		{Field: "model", Message: "model is required"},
		{Field: "messages", Message: "messages is required"},
	}

	errStr := errs.Error()
	if !strings.Contains(errStr, "model is required") {
		t.Errorf("error string should contain 'model is required', got %s", errStr)
	}
	if !strings.Contains(errStr, "messages is required") {
		t.Errorf("error string should contain 'messages is required', got %s", errStr)
	}
}

func TestIsValidModelChar(t *testing.T) {
	tests := []struct {
		char rune
		want bool
	}{
		{'a', true},
		{'z', true},
		{'A', true},
		{'Z', true},
		{'0', true},
		{'9', true},
		{'-', true},
		{'_', true},
		{'.', true},
		{':', true},
		{'<', false},
		{'>', false},
		{'/', false},
		{'\\', false},
		{' ', false},
	}

	for _, tt := range tests {
		t.Run(string(tt.char), func(t *testing.T) {
			got := isValidModelChar(tt.char)
			if got != tt.want {
				t.Errorf("isValidModelChar(%c) = %v, want %v", tt.char, got, tt.want)
			}
		})
	}
}

func TestIsValidRole(t *testing.T) {
	tests := []struct {
		role string
		want bool
	}{
		{"system", true},
		{"user", true},
		{"assistant", true},
		{"function", true},
		{"admin", false},
		{"", false},
		{"SYSTEM", false}, // Case sensitive
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			got := isValidRole(tt.role)
			if got != tt.want {
				t.Errorf("isValidRole(%s) = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}

func TestContainsDangerousChars(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"normal text", "Hello, world!", false},
		{"with newline", "Hello\nWorld", false},
		{"with tab", "Hello\tWorld", false},
		{"with carriage return", "Hello\rWorld", false},
		{"with null byte", "Hello\x00World", true},
		{"with control char", "Hello\x01World", true},
		{"with backspace", "Hello\x08World", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsDangerousChars(tt.text)
			if got != tt.want {
				t.Errorf("containsDangerousChars() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Helper functions for test pointers
func floatPtr(f float64) *float64 {
	return &f
}

func intPtr(i int) *int {
	return &i
}
