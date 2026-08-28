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

package adapters

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// Translation between the OpenAI tool surface AEGIS accepts and the Anthropic
// tool surface, in both directions.
//
// Every rule here was established by probing the live Messages API rather than
// from the schema as remembered. The probe is scripts/dev/probe_anthropic_tools.py
// and its output is recorded in docs/evidence/anthropic-tool-mapping.md; the
// error strings quoted in comments are the provider's own.
//
// Where a construct cannot be expressed on the far side it is refused, never
// approximated. An approximation is the same failure as a silent drop and
// harder to detect, which is the defect this whole line of work exists to
// remove.

// ErrUnmappable is returned when a request is valid OpenAI and cannot be
// expressed in the Anthropic Messages API.
var ErrUnmappable = errors.New("construct cannot be expressed for this provider")

// UnmappableError names the construct and says why, so the 400 a caller sees
// tells them what to change.
//
// Construct is always positional: a message index, a tool index, or the name of
// a field. It must never carry a scanned value. Both it and Detail are
// interpolated into the client response body and into a structured log line,
// and a tool call id is scanned text, so quoting one here would be the leak
// that "keep scanned values out of validation error labels" removed from the
// validator. TestUnmappableConstructsAreNeverScannedText enforces this.
type UnmappableError struct {
	Construct string
	Detail    string
}

func (e *UnmappableError) Error() string {
	return fmt.Sprintf("%s: %s", e.Construct, e.Detail)
}

func (e *UnmappableError) Unwrap() error { return ErrUnmappable }

// anthropicTool is a tool definition in Anthropic's shape: flat, with the JSON
// Schema under input_schema rather than nested in a function object under
// parameters.
//
// Sending OpenAI's spelling is refused by the provider:
//
//	tools.0.custom.input_schema: Field required
type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`

	// Strict is carried through rather than dropped. The provider accepts the
	// field alongside name/description/input_schema and enforces the schema
	// behind it; omitting it from a request whose caller asked for strict
	// would run the tool unenforced and report success, which is the silent
	// drop this translation exists to remove.
	Strict *bool `json:"strict,omitempty"`
}

// emptyObjectSchema is what a tool with no declared parameters gets.
// input_schema is required, so an absent schema cannot simply be omitted.
var emptyObjectSchema = json.RawMessage(`{"type":"object","properties":{}}`)

// toAnthropicTools converts tool definitions.
func toAnthropicTools(tools []types.Tool) ([]anthropicTool, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]anthropicTool, 0, len(tools))
	for i, t := range tools {
		if t.Type != "" && t.Type != types.ToolTypeFunction {
			return nil, &UnmappableError{
				Construct: fmt.Sprintf("tools[%d].type=%q", i, t.Type),
				Detail:    "the Anthropic Messages API has no equivalent of a non-function tool type",
			}
		}
		// strict is accepted by the provider, but it validates the schema
		// behind it and requires additionalProperties:false. AEGIS will not
		// rewrite a caller's schema to satisfy that, because the rewritten
		// request is not the one they sent.
		if t.Function.Strict != nil && *t.Function.Strict && !schemaDisallowsAdditionalProperties(t.Function.Parameters) {
			return nil, &UnmappableError{
				Construct: fmt.Sprintf("tools[%d].function.strict", i),
				Detail: "the Anthropic Messages API requires a strict tool's schema to set " +
					`"additionalProperties": false, and AEGIS will not rewrite your schema to add it`,
			}
		}
		schema := t.Function.Parameters
		if len(schema) == 0 {
			schema = emptyObjectSchema
		}
		out = append(out, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: schema,
			Strict:      t.Function.Strict,
		})
	}
	return out, nil
}

// schemaDisallowsAdditionalProperties reports whether the schema explicitly
// sets additionalProperties to false, which is what the provider demands of a
// strict tool.
func schemaDisallowsAdditionalProperties(schema json.RawMessage) bool {
	if len(schema) == 0 {
		return false
	}
	var probe struct {
		AdditionalProperties *bool `json:"additionalProperties"`
	}
	if err := json.Unmarshal(schema, &probe); err != nil {
		return false
	}
	return probe.AdditionalProperties != nil && !*probe.AdditionalProperties
}

// anthropicToolChoice is Anthropic's tool_choice. The provider accepts exactly
// four type values and rejects everything else:
//
//	tool_choice: Input tag 'required' found using 'type' does not match any of
//	the expected tags: 'auto', 'any', 'tool', 'none'
//
// disable_parallel_tool_use lives here rather than at the top level, which is
// where OpenAI puts its parallel_tool_calls.
type anthropicToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"`
}

// toAnthropicToolChoice maps tool_choice and parallel_tool_calls together,
// because Anthropic expresses the second inside the first.
//
// A request carrying parallel_tool_calls:false and no tool_choice still needs a
// tool_choice object to say so, so one is synthesised as "auto", which is the
// behaviour the caller already had.
func toAnthropicToolChoice(tc types.ToolChoice, parallel *bool) (*anthropicToolChoice, error) {
	var out *anthropicToolChoice

	switch {
	case tc.Function != "":
		out = &anthropicToolChoice{Type: "tool", Name: tc.Function}
	case tc.Mode == types.ToolChoiceAuto:
		out = &anthropicToolChoice{Type: "auto"}
	case tc.Mode == types.ToolChoiceNone:
		out = &anthropicToolChoice{Type: "none"}
	case tc.Mode == types.ToolChoiceRequired:
		// "required" means the model must call some tool. Anthropic spells
		// that "any"; it has no value named "required" and refuses one.
		out = &anthropicToolChoice{Type: "any"}
	case tc.Mode != "":
		return nil, &UnmappableError{
			Construct: fmt.Sprintf("tool_choice=%q", tc.Mode),
			Detail:    "no equivalent in the Anthropic Messages API",
		}
	}

	if parallel != nil && !*parallel {
		if out == nil {
			out = &anthropicToolChoice{Type: "auto"}
		}
		// The two fields are negations of each other: OpenAI says "may call in
		// parallel", Anthropic says "disable parallel". Assigning the incoming
		// pointer here sends disable_parallel_tool_use:false, which means the
		// opposite of what the caller asked for, and the provider cheerfully
		// returns two tool calls. A live test caught it; nothing about the
		// types would have.
		disable := true
		out.DisableParallelToolUse = &disable
	}
	return out, nil
}

// Anthropic content blocks. Only the shapes this translation produces or reads
// are modelled; anything else is passed over rather than guessed at.
type anthropicContentBlock struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   any    `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// toAnthropicMessages converts the canonical message list.
//
// Three shapes change:
//
//   - a system message is hoisted to the top-level system parameter, because
//     role "system" inside messages is refused
//   - an assistant message carrying tool_calls becomes an assistant message
//     whose content is tool_use blocks
//   - a message with role "tool" becomes a tool_result block inside a USER
//     message, because "tool_result blocks can only be in user messages"
//
// Consecutive tool results are merged into one user message. Anthropic requires
// every tool_use to be followed immediately by its result, so a run of OpenAI
// tool messages has to arrive as a single user turn rather than several.
func toAnthropicMessages(msgs []types.Message) (string, []anthropicMessage, error) {
	var system string
	var out []anthropicMessage

	// pendingCalls tracks the tool_use ids from the assistant turn immediately
	// preceding, so the adjacency rule can be checked here with a message the
	// caller can act on rather than as an opaque provider 400.
	var pendingCalls []string

	for i := 0; i < len(msgs); i++ {
		m := msgs[i]

		switch m.Role {
		case types.RoleSystem:
			if system != "" {
				system += "\n\n"
			}
			system += m.Content.Flatten()
			continue

		case types.RoleTool:
			if len(pendingCalls) == 0 {
				return "", nil, &UnmappableError{
					Construct: fmt.Sprintf("messages[%d] (role tool)", i),
					Detail: "the Anthropic Messages API requires every tool result to answer a tool " +
						"call in the message immediately before it, and this result follows none",
				}
			}
			// Gather this and any following tool results into one user turn.
			var results []anthropicContentBlock
			answered := map[string]bool{}
			expected := make(map[string]bool, len(pendingCalls))
			for _, id := range pendingCalls {
				expected[id] = true
			}
			for i < len(msgs) && msgs[i].Role == types.RoleTool {
				r := msgs[i]
				// A result answering no call in the preceding assistant turn is
				// refused here. The provider refuses an unexpected tool_use_id
				// too, but as an opaque 400 that does not say which message to
				// fix, and only once the orphan has already been forwarded.
				if !expected[r.ToolCallID] {
					return "", nil, &UnmappableError{
						Construct: fmt.Sprintf("messages[%d] (role tool)", i),
						Detail: "the Anthropic Messages API requires every tool result to answer a tool " +
							"call in the message immediately before it, and this result's tool_call_id " +
							"answers none of that message's calls",
					}
				}
				results = append(results, anthropicContentBlock{
					Type:      "tool_result",
					ToolUseID: r.ToolCallID,
					Content:   r.Content.Flatten(),
				})
				answered[r.ToolCallID] = true
				i++
			}
			i-- // the outer loop increments

			for n, id := range pendingCalls {
				if !answered[id] {
					return "", nil, &UnmappableError{
						Construct: fmt.Sprintf("tool_calls[%d] of the preceding assistant message", n),
						Detail: "the Anthropic Messages API requires every tool call to be answered " +
							"by a tool result in the message immediately after it, and this call is unanswered",
					}
				}
			}
			out = append(out, anthropicMessage{Role: types.RoleUser, Blocks: results})
			pendingCalls = nil
			continue

		case types.RoleAssistant:
			if len(m.ToolCalls) == 0 {
				// An assistant turn between a tool call and its result breaks
				// the same adjacency rule the default arm refuses below.
				// Clearing pendingCalls here instead would let the interleaved
				// conversation through to the provider as an opaque 400, or
				// end the conversation with the call silently unanswered.
				if len(pendingCalls) > 0 {
					return "", nil, &UnmappableError{
						Construct: fmt.Sprintf("messages[%d] (role %q)", i, m.Role),
						Detail: "the Anthropic Messages API requires a tool result immediately after a " +
							"tool call, and this message comes between them",
					}
				}
				out = append(out, anthropicMessage{Role: m.Role, Content: m.Content.Flatten()})
				continue
			}
			var blocks []anthropicContentBlock
			if text := m.Content.Flatten(); text != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: text})
			}
			pendingCalls = pendingCalls[:0]
			for n, tc := range m.ToolCalls {
				input := json.RawMessage(tc.Function.Arguments)
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				if !json.Valid(input) {
					return "", nil, &UnmappableError{
						Construct: fmt.Sprintf("messages[%d].tool_calls[%d].function.arguments", i, n),
						Detail: "the Anthropic Messages API carries tool call arguments as a JSON " +
							"object, and these arguments are not valid JSON",
					}
				}
				blocks = append(blocks, anthropicContentBlock{
					Type: "tool_use", ID: tc.ID, Name: tc.Function.Name, Input: input,
				})
				pendingCalls = append(pendingCalls, tc.ID)
			}
			out = append(out, anthropicMessage{Role: m.Role, Blocks: blocks})
			continue

		default:
			if len(pendingCalls) > 0 {
				return "", nil, &UnmappableError{
					Construct: fmt.Sprintf("messages[%d] (role %q)", i, m.Role),
					Detail: "the Anthropic Messages API requires a tool result immediately after a " +
						"tool call, and this message comes between them",
				}
			}
			out = append(out, anthropicMessage{Role: m.Role, Content: m.Content.Flatten()})
		}
	}

	if len(pendingCalls) > 0 {
		return "", nil, &UnmappableError{
			Construct: "tool_calls[0] of the final assistant message",
			Detail: "the Anthropic Messages API requires every tool call to be answered by a tool " +
				"result in the message immediately after it, and the conversation ends with it unanswered",
		}
	}

	return system, out, nil
}

// fromAnthropicToolUse converts a completed response's tool_use blocks into the
// canonical tool call shape. Arguments travel as a JSON string on the OpenAI
// side and as a JSON object on Anthropic's, so the object is re-serialised.
func fromAnthropicToolUse(blocks []anthropicContentBlock) []types.ToolCall {
	var calls []types.ToolCall
	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		args := string(b.Input)
		if args == "" {
			args = "{}"
		}
		calls = append(calls, types.ToolCall{
			ID:       b.ID,
			Type:     types.ToolTypeFunction,
			Function: types.FunctionCallSpec{Name: b.Name, Arguments: args},
		})
	}
	return calls
}
