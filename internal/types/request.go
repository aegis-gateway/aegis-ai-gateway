package types

import "time"

// AegisRequest is the canonical internal representation of an incoming AI request.
// All provider-specific formats are converted to/from this type.
type AegisRequest struct {
	// Identity (set by auth middleware)
	RequestID      string         `json:"request_id"`
	OrganizationID string         `json:"organization_id"`
	TeamID         string         `json:"team_id"`
	UserID         string         `json:"user_id"`
	APIKeyID       string         `json:"api_key_id"`
	Classification Classification `json:"classification"`

	// Request content
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream"`
	TopP        *float64  `json:"top_p,omitempty"`
	Stop        []string  `json:"stop,omitempty"`

	// Metadata
	Project        string `json:"project,omitempty"`
	PreferProvider string `json:"prefer_provider,omitempty"`
	TraceContext   string `json:"trace_context,omitempty"`
	SkipCache      bool   `json:"skip_cache,omitempty"`

	// Internal tracking
	ReceivedAt      time.Time `json:"-"`
	EstimatedTokens int       `json:"-"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}
