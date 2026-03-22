package validation

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/af-corp/aegis-gateway/internal/telemetry"
	"github.com/af-corp/aegis-gateway/internal/types"
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
				Message: fmt.Sprintf("invalid role '%s' (allowed: system, user, assistant, function)", msg.Role),
			})
		}

		// Validate content
		contentLength := utf8.RuneCountInString(msg.Content)
		if contentLength > v.limits.MaxMessageLength {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("messages[%d].content", i),
				Message: fmt.Sprintf("message content too long (max %d characters)", v.limits.MaxMessageLength),
			})
		}

		// Check for potential injection attacks (null bytes, control characters)
		if containsDangerousChars(msg.Content) {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("messages[%d].content", i),
				Message: "message content contains invalid control characters",
			})
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

// isValidRole checks if a role is valid
func isValidRole(role string) bool {
	switch role {
	case "system", "user", "assistant", "function":
		return true
	default:
		return false
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
