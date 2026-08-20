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

	// Resolved at routing time
	ProviderType string `json:"-"`

	// Internal tracking
	ReceivedAt      time.Time `json:"-"`
	EstimatedTokens int       `json:"-"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}
