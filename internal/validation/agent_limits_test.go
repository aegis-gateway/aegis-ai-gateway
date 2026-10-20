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

func TestAgentLimits_LargeToolResult(t *testing.T) {
	big := strings.Repeat("x", 500_000)
	if err := NewValidator(AgentLimits(), nil).Validate(toolResultReq(big)); err != nil {
		t.Errorf("a 500K-character file read was refused: %v", err)
	}
}
