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

func toolResultReq(result string) *types.AegisRequest {
	return &types.AegisRequest{Model: "m", Messages: []types.Message{
		{Role: types.RoleUser, Content: types.TextContent("run ls")},
		{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{ID: "t1", Type: types.ToolTypeFunction,
			Function: types.FunctionCallSpec{Name: "Bash", Arguments: `{}`}}}},
		{Role: types.RoleTool, ToolCallID: "t1", Content: types.TextContent(result)},
	}}
}

// Terminal output carries ANSI escapes. The agent profile admits them in a
// tool result; the default profile, used by /v1/chat/completions, still
// refuses, and NUL is refused by both.
func TestAgentLimits_ControlCharactersInToolResults(t *testing.T) {
	ansi := "\x1b[34msrc\x1b[0m"
	if err := NewValidator(AgentLimits(), nil).Validate(toolResultReq(ansi)); err != nil {
		t.Errorf("agent profile refused ANSI output: %v", err)
	}
	if err := NewValidator(DefaultLimits(), nil).Validate(toolResultReq(ansi)); err == nil {
		t.Error("default profile changed: it now admits control characters")
	}
	if err := NewValidator(AgentLimits(), nil).Validate(toolResultReq("a\x00b")); err == nil {
		t.Error("agent profile admitted NUL")
	}
	// Only tool results: user text with an escape is still refused.
	req := &types.AegisRequest{Model: "m", Messages: []types.Message{{Role: types.RoleUser, Content: types.TextContent(ansi)}}}
	if err := NewValidator(AgentLimits(), nil).Validate(req); err == nil || !strings.Contains(err.Error(), "control") {
		t.Errorf("agent profile admitted a control character outside a tool result: %v", err)
	}
}

// MaxToolNameLength applies to a tool call's name in message history, not
// only to tool definitions: 64 on the default profile (/v1/chat/completions),
// 128 on the agent profile (/v1/messages). The error never quotes the name.
func TestToolCallNameLengthEnforcedInHistory(t *testing.T) {
	withName := func(name string) *types.AegisRequest {
		req := toolResultReq("ok")
		req.Messages[1].ToolCalls[0].Function.Name = name
		return req
	}
	cases := []struct {
		profile string
		limits  Limits
		max     int
	}{
		{"default", DefaultLimits(), 64},
		{"agent", AgentLimits(), 128},
	}
	for _, c := range cases {
		t.Run(c.profile, func(t *testing.T) {
			v := NewValidator(c.limits, nil)
			if err := v.Validate(withName(strings.Repeat("a", c.max))); err != nil {
				t.Errorf("a %d-character name was refused: %v", c.max, err)
			}
			marker := "MARKER" + strings.Repeat("b", c.max)
			err := v.Validate(withName(marker))
			if err == nil {
				t.Fatalf("a %d-character name was admitted", len(marker))
			}
			if !strings.Contains(err.Error(), "messages[1].tool_calls[0].function.name") ||
				!strings.Contains(err.Error(), "too long") {
				t.Errorf("error does not name the field and limit: %v", err)
			}
			if strings.Contains(err.Error(), "MARKER") {
				t.Errorf("error echoes the tool name: %v", err)
			}
		})
	}
}

func TestAgentLimits_LargeToolResult(t *testing.T) {
	big := strings.Repeat("x", 500_000)
	if err := NewValidator(AgentLimits(), nil).Validate(toolResultReq(big)); err != nil {
		t.Errorf("a 500K-character file read was refused: %v", err)
	}
}
