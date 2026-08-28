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
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/telemetry"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// ValidationError represents a validation error with a field and message
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// ValidationErrors holds multiple validation errors
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	var msgs []string
	for _, err := range e {
		msgs = append(msgs, err.Error())
	}
	return strings.Join(msgs, "; ")
}

// Limits holds validation limits for request fields
type Limits struct {
	MaxModelNameLength    int
	MaxMessagesCount      int
	MaxMessageLength      int
	MaxTotalContentLength int
	MaxTokens             int
	MinTemperature        float64
	MaxTemperature        float64
	MinTopP               float64
	MaxTopP               float64
	MaxStopSequences      int
	MaxStopSequenceLength int
	MaxTools              int
	MaxToolNameLength     int
	MaxToolCallsPerMsg    int
	MaxToolCallIDLength   int
}

// DefaultLimits returns sensible default validation limits
func DefaultLimits() Limits {
	return Limits{
		MaxModelNameLength:    256,
		MaxMessagesCount:      1000,
		MaxMessageLength:      100000,  // 100K chars per message
		MaxTotalContentLength: 1000000, // 1M chars total
		MaxTokens:             128000,  // Maximum tokens (adjust per model)
		MinTemperature:        0.0,
		MaxTemperature:        2.0,
		MinTopP:               0.0,
		MaxTopP:               1.0,
		MaxStopSequences:      4,
		MaxStopSequenceLength: 256,
		MaxTools:              128,
		MaxToolNameLength:     64,
		MaxToolCallsPerMsg:    128,
		// A provider-issued correlator is around 30 characters: OpenAI sends
		// call_ plus 24, Anthropic toolu_ plus 24. 128 is four times that.
		//
		// It is bounded at all because nothing bounded it before: a tool call
		// id is client-supplied on every turn of an agent loop, and a
		// hundred-kilobyte id validated cleanly and was forwarded to the
		// provider. Every other client-controlled string on the request has a
		// limit; this one was missed because it reads like a value the gateway
		// issued.
		MaxToolCallIDLength: 128,
	}
}

// Validator validates incoming requests
type Validator struct {
	limits  Limits
	metrics *telemetry.Metrics
}

// NewValidator creates a new request validator
func NewValidator(limits Limits, metrics *telemetry.Metrics) *Validator {
	return &Validator{
		limits:  limits,
		metrics: metrics,
	}
}

// Validate validates an AegisRequest and returns validation errors if any
func (v *Validator) Validate(req *types.AegisRequest) error {
	var errs ValidationErrors

	// Validate model
	if err := v.validateModel(req.Model); err != nil {
		errs = append(errs, *err)
		v.recordInvalidField("model")
	}

	// Validate messages
	if messageErrs := v.validateMessages(req.Messages); len(messageErrs) > 0 {
		errs = append(errs, messageErrs...)
		v.recordInvalidField("messages")
	}

	// Validate temperature
	if req.Temperature != nil {
		if err := v.validateTemperature(*req.Temperature); err != nil {
			errs = append(errs, *err)
			v.recordInvalidField("temperature")
		}
	}

	// Validate max_tokens
	if req.MaxTokens != nil {
		if err := v.validateMaxTokens(*req.MaxTokens); err != nil {
			errs = append(errs, *err)
			v.recordInvalidField("max_tokens")
		}
	}

	// Validate top_p
	if req.TopP != nil {
		if err := v.validateTopP(*req.TopP); err != nil {
			errs = append(errs, *err)
			v.recordInvalidField("top_p")
		}
	}

	// Validate tools
	if toolErrs := v.validateTools(req.Tools); len(toolErrs) > 0 {
		errs = append(errs, toolErrs...)
		v.recordInvalidField("tools")
	}

	// Validate stop sequences
	if len(req.Stop) > 0 {
		if err := v.validateStopSequences(req.Stop); err != nil {
			errs = append(errs, *err)
			v.recordInvalidField("stop")
		}
	}

	if len(errs) > 0 {
		return errs
	}

	return nil
}

// validateModel validates the model field
func (v *Validator) validateModel(model string) *ValidationError {
	if model == "" {
		return &ValidationError{
			Field:   "model",
			Message: "model is required",
		}
	}

	if len(model) > v.limits.MaxModelNameLength {
		return &ValidationError{
			Field:   "model",
			Message: fmt.Sprintf("model name too long (max %d characters)", v.limits.MaxModelNameLength),
		}
	}

	// Check for valid format (alphanumeric, hyphens, underscores, dots, colons)
	for _, r := range model {
		if !isValidModelChar(r) {
			return &ValidationError{
				Field:   "model",
				Message: "model name contains invalid characters (allowed: a-z, A-Z, 0-9, -, _, ., :)",
			}
		}
	}

	return nil
}

// validateMessages validates the messages array
func (v *Validator) validateMessages(messages []types.Message) ValidationErrors {
	var errs ValidationErrors

	if len(messages) == 0 {
		errs = append(errs, ValidationError{
			Field:   "messages",
			Message: "messages array is required and must not be empty",
		})
		return errs
	}

	if len(messages) > v.limits.MaxMessagesCount {
		errs = append(errs, ValidationError{
			Field:   "messages",
			Message: fmt.Sprintf("too many messages (max %d)", v.limits.MaxMessagesCount),
		})
		return errs
	}

	totalContentLength := 0
	for i, msg := range messages {
		// Validate role
		if msg.Role == "" {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("messages[%d].role", i),
				Message: "role is required",
			})
		} else if !isValidRole(msg.Role) {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("messages[%d].role", i),
				Message: fmt.Sprintf("invalid role '%s' (allowed: system, user, assistant, tool, function)", msg.Role),
			})
		}

		// Validate content. The length and control-character checks run over
		// every text-bearing element of the message, not only Content: a
		// structured content array and a tool call's arguments are both message
		// content as far as size limits and control characters are concerned.
		contentLength := 0
		for _, seg := range msg.TextSegments(i) {
			contentLength += utf8.RuneCountInString(seg.Text)
			if containsDangerousChars(seg.Text) {
				errs = append(errs, ValidationError{
					Field:   segmentField(i, seg),
					Message: "message content contains invalid control characters",
				})
			}
		}
		if contentLength > v.limits.MaxMessageLength {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("messages[%d].content", i),
				Message: fmt.Sprintf("message content too long (max %d characters)", v.limits.MaxMessageLength),
			})
		}

		if msgErrs := v.validateToolFields(i, msg); len(msgErrs) > 0 {
			errs = append(errs, msgErrs...)
		}

		totalContentLength += contentLength
	}

	// Check total content length
	if totalContentLength > v.limits.MaxTotalContentLength {
		errs = append(errs, ValidationError{
			Field:   "messages",
			Message: fmt.Sprintf("total message content too long (max %d characters)", v.limits.MaxTotalContentLength),
		})
	}

	return errs
}

// validateTools validates the tool definitions on a request.
func (v *Validator) validateTools(tools []types.Tool) ValidationErrors {
	var errs ValidationErrors
	if len(tools) == 0 {
		return nil
	}
	if len(tools) > v.limits.MaxTools {
		return ValidationErrors{{
			Field:   "tools",
			Message: fmt.Sprintf("too many tools (max %d)", v.limits.MaxTools),
		}}
	}
	seen := make(map[string]bool, len(tools))
	for i, t := range tools {
		if t.Type != types.ToolTypeFunction {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("tools[%d].type", i),
				Message: fmt.Sprintf("unsupported tool type %q (only %q is supported)", t.Type, types.ToolTypeFunction),
			})
		}
		name := t.Function.Name
		if name == "" {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("tools[%d].function.name", i),
				Message: "tool name is required",
			})
			continue
		}
		if len(name) > v.limits.MaxToolNameLength {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("tools[%d].function.name", i),
				Message: fmt.Sprintf("tool name too long (max %d characters)", v.limits.MaxToolNameLength),
			})
		}
		// A duplicate name makes the model's choice ambiguous and makes the
		// tool-name metadata exposed to policy and logs ambiguous with it.
		if seen[name] {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("tools[%d].function.name", i),
				Message: fmt.Sprintf("duplicate tool name %q", name),
			})
		}
		seen[name] = true
	}
	return errs
}

// validateToolFields validates the tool call and tool result fields on one
// message, and the role pairing between them.
func (v *Validator) validateToolFields(i int, msg types.Message) ValidationErrors {
	var errs ValidationErrors

	if len(msg.ToolCalls) > v.limits.MaxToolCallsPerMsg {
		errs = append(errs, ValidationError{
			Field:   fmt.Sprintf("messages[%d].tool_calls", i),
			Message: fmt.Sprintf("too many tool calls (max %d)", v.limits.MaxToolCallsPerMsg),
		})
		return errs
	}

	if len(msg.ToolCalls) > 0 && msg.Role != types.RoleAssistant {
		errs = append(errs, ValidationError{
			Field:   fmt.Sprintf("messages[%d].tool_calls", i),
			Message: fmt.Sprintf("tool_calls is only valid on an assistant message, got role %q", msg.Role),
		})
	}

	if len(msg.ToolCallID) > v.limits.MaxToolCallIDLength {
		errs = append(errs, ValidationError{
			Field:   fmt.Sprintf("messages[%d].tool_call_id", i),
			Message: fmt.Sprintf("tool_call_id too long (max %d characters)", v.limits.MaxToolCallIDLength),
		})
	}

	for j, tc := range msg.ToolCalls {
		if len(tc.ID) > v.limits.MaxToolCallIDLength {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("messages[%d].tool_calls[%d].id", i, j),
				Message: fmt.Sprintf("tool call id too long (max %d characters)", v.limits.MaxToolCallIDLength),
			})
		}
		if tc.ID == "" {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("messages[%d].tool_calls[%d].id", i, j),
				Message: "tool call id is required so the matching tool result can be paired with it",
			})
		}
		if tc.Type != "" && tc.Type != types.ToolTypeFunction {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("messages[%d].tool_calls[%d].type", i, j),
				Message: fmt.Sprintf("unsupported tool call type %q (only %q is supported)", tc.Type, types.ToolTypeFunction),
			})
		}
		if tc.Function.Name == "" {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("messages[%d].tool_calls[%d].function.name", i, j),
				Message: "tool call function name is required",
			})
		}
	}

	// A tool result with no tool_call_id cannot be attributed to a call, and
	// the provider will reject it. Catching it here keeps the failure at the
	// gateway with a field name rather than as an opaque provider 400.
	if msg.Role == types.RoleTool && msg.ToolCallID == "" {
		errs = append(errs, ValidationError{
			Field:   fmt.Sprintf("messages[%d].tool_call_id", i),
			Message: "tool_call_id is required on a message with role \"tool\"",
		})
	}
	if msg.ToolCallID != "" && msg.Role != types.RoleTool {
		errs = append(errs, ValidationError{
			Field:   fmt.Sprintf("messages[%d].tool_call_id", i),
			Message: fmt.Sprintf("tool_call_id is only valid on a tool message, got role %q", msg.Role),
		})
	}

	return errs
}

// validateTemperature validates the temperature parameter
func (v *Validator) validateTemperature(temp float64) *ValidationError {
	if temp < v.limits.MinTemperature || temp > v.limits.MaxTemperature {
		return &ValidationError{
			Field:   "temperature",
			Message: fmt.Sprintf("temperature must be between %.1f and %.1f", v.limits.MinTemperature, v.limits.MaxTemperature),
		}
	}
	return nil
}

// validateMaxTokens validates the max_tokens parameter
func (v *Validator) validateMaxTokens(maxTokens int) *ValidationError {
	if maxTokens <= 0 {
		return &ValidationError{
			Field:   "max_tokens",
			Message: "max_tokens must be positive",
		}
	}

	if maxTokens > v.limits.MaxTokens {
		return &ValidationError{
			Field:   "max_tokens",
			Message: fmt.Sprintf("max_tokens too large (max %d)", v.limits.MaxTokens),
		}
	}

	return nil
}

// validateTopP validates the top_p parameter
func (v *Validator) validateTopP(topP float64) *ValidationError {
	if topP < v.limits.MinTopP || topP > v.limits.MaxTopP {
		return &ValidationError{
			Field:   "top_p",
			Message: fmt.Sprintf("top_p must be between %.1f and %.1f", v.limits.MinTopP, v.limits.MaxTopP),
		}
	}
	return nil
}

// validateStopSequences validates stop sequences
func (v *Validator) validateStopSequences(stop []string) *ValidationError {
	if len(stop) > v.limits.MaxStopSequences {
		return &ValidationError{
			Field:   "stop",
			Message: fmt.Sprintf("too many stop sequences (max %d)", v.limits.MaxStopSequences),
		}
	}

	for i, seq := range stop {
		if len(seq) > v.limits.MaxStopSequenceLength {
			return &ValidationError{
				Field:   fmt.Sprintf("stop[%d]", i),
				Message: fmt.Sprintf("stop sequence too long (max %d characters)", v.limits.MaxStopSequenceLength),
			}
		}
	}

	return nil
}

// recordInvalidField records a validation failure metric
func (v *Validator) recordInvalidField(field string) {
	if v.metrics != nil {
		v.metrics.RecordValidationFailure(field)
	}
}

// isValidModelChar checks if a character is valid in a model name
func isValidModelChar(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r == '-' || r == '_' || r == '.' || r == ':'
}

// isValidRole checks if a role is valid.
//
// "tool" is here because a tool result message is how an agent returns a call's
// output. It was previously rejected, which meant the one part of tool calling
// that did fail loudly failed for the wrong reason.
func isValidRole(role string) bool {
	switch role {
	case types.RoleSystem, types.RoleUser, types.RoleAssistant, types.RoleTool, types.RoleFunction:
		return true
	default:
		return false
	}
}

// segmentField names the field a segment came from, for a validation error.
func segmentField(msgIndex int, seg types.TextSegment) string {
	switch seg.Kind {
	case types.SegmentContentPart:
		return fmt.Sprintf("messages[%d].content[%s].text", msgIndex, seg.Ref)
	case types.SegmentToolCallArguments:
		return fmt.Sprintf("messages[%d].tool_calls[%s].function.arguments", msgIndex, seg.Ref)
	case types.SegmentParticipantName:
		return fmt.Sprintf("messages[%d].name", msgIndex)
	case types.SegmentToolName:
		// Tool names come from three places; MessageIndex distinguishes them.
		if msgIndex < 0 {
			if seg.Ref == "tool_choice" {
				return "tool_choice.function.name"
			}
			return fmt.Sprintf("tools[%s].function.name", seg.Ref)
		}
		return fmt.Sprintf("messages[%d].tool_calls[%s].function.name", msgIndex, seg.Ref)
	case types.SegmentToolDefinition:
		return fmt.Sprintf("tools[%s].function", seg.Ref)
	default:
		return fmt.Sprintf("messages[%d].content", msgIndex)
	}
}

// containsDangerousChars checks for dangerous control characters
func containsDangerousChars(s string) bool {
	for _, r := range s {
		// Null byte
		if r == 0 {
			return true
		}
		// Other control characters (except newline, tab, carriage return)
		if r < 32 && r != '\n' && r != '\t' && r != '\r' {
			return true
		}
	}
	return false
}
