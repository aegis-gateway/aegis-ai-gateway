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

import (
	"strconv"
	"time"
)

// AegisRequest is the canonical internal representation of an incoming AI request.
// All provider-specific formats are converted to/from this type.
//
// It is not the wire type. Client JSON is decoded by DecodeChatCompletion into
// ChatCompletionRequest, which is an explicit allowlist, and only then mapped
// here. The two were the same type once, which is how the identity fields below
// came to be settable from a request body and how every unsupported field came
// to be discarded in silence.
type AegisRequest struct {
	// Identity (set by auth middleware, never from the request body)
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

	// Tool calling
	Tools             []Tool     `json:"tools,omitempty"`
	ToolChoice        ToolChoice `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool      `json:"parallel_tool_calls,omitempty"`

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

// Message is one turn of the conversation.
//
// Content carries either a string or an array of text parts. ToolCalls is set
// on an assistant turn that asked for a tool; ToolCallID is set on a tool turn
// returning that call's result.
type Message struct {
	Role       string     `json:"role"`
	Content    Content    `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// Message roles.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
	RoleFunction  = "function"
)

// SegmentKind labels where a scannable text segment came from. It is metadata,
// never persisted with its text, and exists so a filter can say which surface
// it found something on without quoting the finding.
type SegmentKind string

const (
	// SegmentMessageContent is a message whose content was a plain string.
	SegmentMessageContent SegmentKind = "message_content"
	// SegmentContentPart is one text part of a structured content array.
	SegmentContentPart SegmentKind = "content_part"
	// SegmentToolCallArguments is the arguments string of one tool call.
	SegmentToolCallArguments SegmentKind = "tool_call_arguments"
	// SegmentToolResult is the content of a tool-role message. This is
	// content returned from outside the model and is the primary vector for
	// indirect prompt injection.
	SegmentToolResult SegmentKind = "tool_result"
	// SegmentToolDefinition is a tool's name, description or parameter schema.
	SegmentToolDefinition SegmentKind = "tool_definition"
)

// TextSegment is one text-bearing element of a request, with enough context to
// report where it was found.
type TextSegment struct {
	Kind SegmentKind
	// MessageIndex is the index into Messages, or -1 for a request-level
	// segment such as a tool definition.
	MessageIndex int
	// Ref names the element within the message: the tool call ID, the tool
	// name, or the part index. Never the text itself.
	Ref  string
	Text string
}

// IsUntrusted reports whether the segment carries content that originated
// outside the client's own prompt. Tool results are fetched from elsewhere and
// handed back to the model, so a segment marked untrusted is where indirect
// prompt injection actually arrives.
func (s TextSegment) IsUntrusted() bool { return s.Kind == SegmentToolResult }

// TextSegments returns every text-bearing element of the message.
//
// This is the single definition of "what a filter must scan" for a message.
// Filters call it rather than reading Content directly, so that widening the
// message shape again cannot quietly add a surface no filter looks at.
func (m Message) TextSegments(msgIndex int) []TextSegment {
	var segs []TextSegment

	kind := SegmentMessageContent
	if m.Role == RoleTool {
		kind = SegmentToolResult
	}

	switch m.Content.Kind {
	case ContentString:
		if m.Content.Str != "" {
			segs = append(segs, TextSegment{
				Kind: kind, MessageIndex: msgIndex, Ref: m.ToolCallID, Text: m.Content.Str,
			})
		}
	case ContentParts:
		partKind := kind
		if m.Role != RoleTool {
			partKind = SegmentContentPart
		}
		for i, p := range m.Content.Parts {
			if p.Text == "" {
				continue
			}
			segs = append(segs, TextSegment{
				Kind: partKind, MessageIndex: msgIndex, Ref: strconv.Itoa(i), Text: p.Text,
			})
		}
	}

	for _, tc := range m.ToolCalls {
		if tc.Function.Arguments == "" {
			continue
		}
		segs = append(segs, TextSegment{
			Kind: SegmentToolCallArguments, MessageIndex: msgIndex, Ref: tc.ID, Text: tc.Function.Arguments,
		})
	}

	return segs
}

// TextSegments returns every text-bearing element of the whole request: message
// content in either shape, tool call arguments, tool result content, and the
// tool definitions themselves.
//
// Tool definitions are included because they are client-supplied text that
// reaches the provider. A credential pasted into a tool description egresses
// exactly as readily as one pasted into a prompt.
func (r *AegisRequest) TextSegments() []TextSegment {
	var segs []TextSegment
	for i, m := range r.Messages {
		segs = append(segs, m.TextSegments(i)...)
	}
	for _, t := range r.Tools {
		name := t.Function.Name
		if t.Function.Description != "" {
			segs = append(segs, TextSegment{
				Kind: SegmentToolDefinition, MessageIndex: -1, Ref: name, Text: t.Function.Description,
			})
		}
		if len(t.Function.Parameters) > 0 {
			segs = append(segs, TextSegment{
				Kind: SegmentToolDefinition, MessageIndex: -1, Ref: name, Text: string(t.Function.Parameters),
			})
		}
	}
	return segs
}

// ToolNames returns the names of the tools offered on the request, in order.
// Names only: a tool name is metadata, its arguments are payload.
func (r *AegisRequest) ToolNames() []string {
	if len(r.Tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(r.Tools))
	for _, t := range r.Tools {
		names = append(names, t.Function.Name)
	}
	return names
}

// CalledToolNames returns the names of tools the conversation has already
// called, deduplicated and in first-seen order. Names only, never arguments.
func (r *AegisRequest) CalledToolNames() []string {
	var names []string
	seen := make(map[string]bool)
	for _, m := range r.Messages {
		for _, tc := range m.ToolCalls {
			n := tc.Function.Name
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			names = append(names, n)
		}
	}
	return names
}

// HasTools reports whether the request carries tool definitions or any tool
// call or tool result in its history. A provider that cannot express tools
// cannot serve any of these without changing what the client asked for.
func (r *AegisRequest) HasTools() bool {
	if len(r.Tools) > 0 || r.ToolChoice.IsSet() {
		return true
	}
	for _, m := range r.Messages {
		if len(m.ToolCalls) > 0 || m.Role == RoleTool {
			return true
		}
	}
	return false
}
