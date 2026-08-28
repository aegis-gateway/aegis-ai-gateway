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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ToolTypeFunction is the only tool type OpenAI defines for chat completions.
const ToolTypeFunction = "function"

// Tool is one entry in the request's tools array.
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef is a tool's callable signature.
//
// Parameters is kept as raw JSON: it is a JSON Schema document the gateway has
// no reason to interpret, and re-encoding it through a Go map would reorder
// keys and drop the distinction between an absent and an empty schema.
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ToolCall is one call the model asked for, carried on an assistant message.
type ToolCall struct {
	// Index is present only on streaming deltas, where it is the accumulator
	// key. It is omitted from a complete tool call.
	Index    *int             `json:"index,omitempty"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function FunctionCallSpec `json:"function"`
}

// FunctionCallSpec is the called function's name and its serialised arguments.
//
// Arguments is a string containing JSON, not JSON: that is what the OpenAI wire
// format specifies, and it is what makes the field a text channel the filter
// chain has to scan. See AegisRequest.TextSegments.
type FunctionCallSpec struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ToolChoice is the request's tool_choice field: either one of the strings
// "none", "auto" or "required", or an object naming a specific function.
type ToolChoice struct {
	// Mode is set when tool_choice was a string.
	Mode string
	// Function is set when tool_choice was an object naming a function.
	Function string
}

// Valid tool_choice string modes.
const (
	ToolChoiceNone     = "none"
	ToolChoiceAuto     = "auto"
	ToolChoiceRequired = "required"
)

// ErrInvalidToolChoice is returned for a tool_choice the gateway does not
// recognise. Passing an unrecognised value through would be the same silent
// acceptance this work exists to remove.
var ErrInvalidToolChoice = errors.New("invalid tool_choice")

func (t *ToolChoice) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" || trimmed == "" {
		*t = ToolChoice{}
		return nil
	}

	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		switch s {
		case ToolChoiceNone, ToolChoiceAuto, ToolChoiceRequired:
			*t = ToolChoice{Mode: s}
			return nil
		default:
			return fmt.Errorf("%w: %q is not one of %q, %q, %q or a {\"type\":\"function\"} object",
				ErrInvalidToolChoice, s, ToolChoiceNone, ToolChoiceAuto, ToolChoiceRequired)
		}
	}

	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&obj); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidToolChoice, err)
	}
	if obj.Type != ToolTypeFunction {
		return fmt.Errorf("%w: object form requires type %q, got %q", ErrInvalidToolChoice, ToolTypeFunction, obj.Type)
	}
	if obj.Function.Name == "" {
		return fmt.Errorf("%w: object form requires function.name", ErrInvalidToolChoice)
	}
	*t = ToolChoice{Function: obj.Function.Name}
	return nil
}

func (t ToolChoice) MarshalJSON() ([]byte, error) {
	if t.Function != "" {
		return json.Marshal(map[string]any{
			"type":     ToolTypeFunction,
			"function": map[string]string{"name": t.Function},
		})
	}
	if t.Mode != "" {
		return json.Marshal(t.Mode)
	}
	return []byte("null"), nil
}

// IsSet reports whether tool_choice was supplied.
func (t ToolChoice) IsSet() bool { return t.Mode != "" || t.Function != "" }

// String renders tool_choice for logging and policy input. It carries no
// argument values, only the mode or the function name.
func (t ToolChoice) String() string {
	if t.Function != "" {
		return "function:" + t.Function
	}
	return t.Mode
}
