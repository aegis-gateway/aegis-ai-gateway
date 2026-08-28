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

package validation

import (
	"strings"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
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
				{Role: "user", Content: types.TextContent("Hello")},
				{Role: "assistant", Content: types.TextContent("Hi there!")},
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
				{Content: types.TextContent("Hello")},
			},
			wantErr: true,
		},
		{
			name: "invalid role",
			messages: []types.Message{
				{Role: "admin", Content: types.TextContent("Hello")},
			},
			wantErr: true,
		},
		{
			name: "message too long",
			messages: []types.Message{
				{Role: "user", Content: types.TextContent(strings.Repeat("a", 200000))},
			},
			wantErr: true,
		},
		{
			name: "null byte in content",
			messages: []types.Message{
				{Role: "user", Content: types.TextContent("Hello\x00World")},
			},
			wantErr: true,
		},
		{
			name: "valid system message",
			messages: []types.Message{
				{Role: "system", Content: types.TextContent("You are a helpful assistant.")},
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
					{Role: "user", Content: types.TextContent("Hello")},
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
					{Role: "user", Content: types.TextContent("Test")},
				},
			},
			wantErr: false,
		},
		{
			name: "missing model",
			req: &types.AegisRequest{
				Messages: []types.Message{
					{Role: "user", Content: types.TextContent("Hello")},
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
					{Role: "user", Content: types.TextContent("Hello")},
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
					{Role: "user", Content: types.TextContent("Hello")},
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

// TestControlCharInToolCallIDNamesTheRightField pins the field label on a
// control-character finding in either correlator.
//
// Both correlators are scanned text now, so they reach the control-character
// check as segments. segmentField had no case for their segment kind, and the
// default arm reported them as messages[N].content, which names a field the
// caller did not send and sends them looking in the wrong place.
func TestControlCharInToolCallIDNamesTheRightField(t *testing.T) {
	validator := NewValidator(DefaultLimits(), nil)

	tests := []struct {
		name      string
		messages  []types.Message
		wantField string
	}{
		{
			name: "null byte in tool_call_id",
			messages: []types.Message{
				{Role: types.RoleTool, ToolCallID: "call_\x00abc", Content: types.TextContent("ok")},
			},
			wantField: "messages[0].tool_call_id",
		},
		{
			// A second call, to prove the label carries the position rather
			// than a fixed string. With an empty bracket both calls produced
			// the same label and a caller could not tell which one to fix.
			name: "null byte in the second tool call",
			messages: []types.Message{
				{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{
					{ID: "call_ok", Type: types.ToolTypeFunction, Function: types.FunctionCallSpec{Name: "f"}},
					{ID: "call_\x00bad", Type: types.ToolTypeFunction, Function: types.FunctionCallSpec{Name: "g"}},
				}},
			},
			wantField: "messages[0].tool_calls[1].id",
		},
		{
			name: "null byte in tool_calls[0].id",
			messages: []types.Message{
				{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{
					{ID: "call_\x00abc", Type: types.ToolTypeFunction, Function: types.FunctionCallSpec{Name: "f"}},
				}},
			},
			wantField: "messages[0].tool_calls[0].id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validator.validateMessages(tt.messages)

			var found bool
			for _, e := range errs {
				if e.Field == tt.wantField {
					found = true
				}
			}
			if !found {
				t.Errorf("no validation error named %q; got %v", tt.wantField, errs)
			}
		})
	}
}

// TestValidationErrorsDoNotEchoScannedValues is the reason the labels above are
// positional.
//
// A validation error's full text goes to the client response body and to the
// structured log line, and validation runs before the filter chain. So a label
// built by interpolating a correlator or a tool name would copy a credential
// into both, before anything had looked for one. The labels used to do exactly
// that, which was safe only while those fields were not scanned text.
func TestValidationErrorsDoNotEchoScannedValues(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"

	validator := NewValidator(DefaultLimits(), nil)

	// Each of these carries the secret in a field whose value used to become
	// part of an error label, plus a control character elsewhere in the same
	// element so that validation actually produces an error.
	cases := []struct {
		name string
		req  *types.AegisRequest
	}{
		{
			"secret in a tool call id",
			&types.AegisRequest{
				Model: "m",
				Messages: []types.Message{
					{Role: types.RoleUser, Content: types.TextContent("hi")},
					{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{
						ID:       "call_" + secret,
						Type:     types.ToolTypeFunction,
						Function: types.FunctionCallSpec{Name: "f", Arguments: "{\x00}"},
					}}},
				},
			},
		},
		{
			"secret in a tool name",
			&types.AegisRequest{
				Model: "m",
				Tools: []types.Tool{{
					Type:     types.ToolTypeFunction,
					Function: types.FunctionDef{Name: secret},
				}},
				Messages: []types.Message{
					{Role: types.RoleUser, Content: types.TextContent("hi\x00")},
				},
			},
		},
		{
			// The arm the two cases above miss. Case 1's control character is
			// in the arguments, so the error lands on the arguments segment;
			// case 2's tool is a top-level definition, and those never reach
			// segmentField at all. Neither exercises a message-level tool
			// call name, which is its own arm of the switch with its own Ref.
			"secret in a tool call id, control character in the call's name",
			&types.AegisRequest{
				Model: "m",
				Messages: []types.Message{
					{Role: types.RoleUser, Content: types.TextContent("hi")},
					{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{
						ID:       "call_" + secret,
						Type:     types.ToolTypeFunction,
						Function: types.FunctionCallSpec{Name: "f\x00"},
					}}},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validator.Validate(tc.req)
			if err == nil {
				t.Fatal("expected a validation error; without one this proves nothing")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the validation error echoes the credential back to the caller and into "+
					"the log: %s", err.Error())
			}
		})
	}
}
