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
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

func toolMock() *MockAdapter {
	return NewMockAdapter("anthropic", config.ProviderConfig{})
}

// TestOpenAIAdapter_SendsToolFields asserts the outgoing body carries every
// tool field. This is the adapter half of the reported defect: the request type
// could carry tools and the outgoing body still would not.
func TestOpenAIAdapter_SendsToolFields(t *testing.T) {
	t.Parallel()

	a := NewOpenAIAdapter(config.ProviderConfig{BaseURL: "http://provider.invalid/v1"}, nil)
	parallel := false

	req := &types.AegisRequest{
		Model: "gpt-test",
		Tools: []types.Tool{{
			Type: types.ToolTypeFunction,
			Function: types.FunctionDef{
				Name:       "read_file",
				Parameters: json.RawMessage(`{"type":"object"}`),
			},
		}},
		ToolChoice:        types.ToolChoice{Function: "read_file"},
		ParallelToolCalls: &parallel,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: types.TextContent("read a.go")},
			{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{
				ID: "c1", Type: types.ToolTypeFunction,
				Function: types.FunctionCallSpec{Name: "read_file", Arguments: `{"path":"a.go"}`},
			}}},
			{Role: types.RoleTool, ToolCallID: "c1", Content: types.TextContent("package main")},
		},
	}

	httpReq, err := a.TransformRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	for _, want := range []string{
		`"tools":`, `"read_file"`, `"tool_choice":{"function":{"name":"read_file"},"type":"function"}`,
		`"parallel_tool_calls":false`, `"tool_calls":`, `"tool_call_id":"c1"`, `"role":"tool"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("outgoing body is missing %s\nbody: %s", want, body)
		}
	}
}

// TestOpenAIAdapter_OmitsAbsentToolChoice asserts an unset tool_choice is
// absent from the wire rather than sent as null. A null tool_choice is not the
// same request as an absent one, and some providers reject it.
func TestOpenAIAdapter_OmitsAbsentToolChoice(t *testing.T) {
	t.Parallel()

	a := NewOpenAIAdapter(config.ProviderConfig{BaseURL: "http://provider.invalid/v1"}, nil)
	httpReq, err := a.TransformRequest(context.Background(), &types.AegisRequest{
		Model:    "gpt-test",
		Messages: []types.Message{{Role: types.RoleUser, Content: types.TextContent("hi")}},
	})
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	body, _ := io.ReadAll(httpReq.Body)
	if strings.Contains(string(body), "tool_choice") {
		t.Errorf("an unset tool_choice was sent to the provider: %s", body)
	}
}

// TestAnthropicAdapter_ForwardsTools replaces an earlier test that asserted the
// opposite.
//
// That test existed to fire if anyone set SupportsTools to true without
// teaching the adapter to translate, because the gateway would then resume
// silently stripping tools from every request routed here. The translation now
// exists, so the guard inverts: tools must actually reach the wire.
func TestAnthropicAdapter_ForwardsTools(t *testing.T) {
	t.Parallel()

	a := NewAnthropicAdapter(config.ProviderConfig{}, nil)
	if !a.SupportsTools() {
		t.Fatal("the Anthropic adapter reports no tool support, but it translates tools; the " +
			"handler capability gate would refuse requests it can serve")
	}

	httpReq, err := a.TransformRequest(context.Background(), &types.AegisRequest{
		Model:    "claude-test",
		Tools:    []types.Tool{{Type: types.ToolTypeFunction, Function: types.FunctionDef{Name: "read_file"}}},
		Messages: []types.Message{{Role: types.RoleUser, Content: types.TextContent("hi")}},
	})
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	body, _ := io.ReadAll(httpReq.Body)
	if !strings.Contains(string(body), "read_file") {
		t.Errorf("the tool did not reach the outgoing body: %s", body)
	}
	if !strings.Contains(string(body), "input_schema") {
		t.Errorf("the tool has no input_schema, which the provider requires: %s", body)
	}
}

// TestAnthropicAdapter_FlattensStructuredContent covers the one place the
// adapter has to collapse the widened content shape: the Anthropic system
// parameter is a single string.
func TestAnthropicAdapter_FlattensStructuredContent(t *testing.T) {
	t.Parallel()

	a := NewAnthropicAdapter(config.ProviderConfig{}, nil)
	httpReq, err := a.TransformRequest(context.Background(), &types.AegisRequest{
		Model: "claude-test",
		Messages: []types.Message{
			{Role: types.RoleSystem, Content: types.PartsContent("be brief", "be kind")},
			{Role: types.RoleUser, Content: types.PartsContent("hello", "there")},
		},
	})
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	body, _ := io.ReadAll(httpReq.Body)
	for _, want := range []string{"be brief", "be kind", "hello", "there"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("content part %q was lost when flattening for Anthropic: %s", want, body)
		}
	}
}

// TestMockAdapter_AnswersToolRequestWithToolCall is what makes the
// agent-compatibility demo able to show the working path with no provider
// credential. A mock that answered a tool request with prose would reproduce
// the exact symptom of the bug and the demo could not tell a fixed gateway from
// a broken one.
func TestMockAdapter_AnswersToolRequestWithToolCall(t *testing.T) {
	t.Parallel()

	a := toolMock()
	req := &types.AegisRequest{
		Model:    "claude-test",
		Tools:    []types.Tool{{Type: types.ToolTypeFunction, Function: types.FunctionDef{Name: "read_file"}}},
		Messages: []types.Message{{Role: types.RoleUser, Content: types.TextContent("read a.go")}},
	}
	httpReq, err := a.TransformRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	resp, err := a.SendRequest(httpReq)
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}
	aegisResp, err := a.TransformResponse(context.Background(), resp)
	if err != nil {
		t.Fatalf("TransformResponse: %v", err)
	}

	if len(aegisResp.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(aegisResp.Choices))
	}
	choice := aegisResp.Choices[0]
	if choice.FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", choice.FinishReason)
	}
	if len(choice.Message.ToolCalls) != 1 || choice.Message.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("tool_calls = %+v, want one call to read_file", choice.Message.ToolCalls)
	}
}

// TestMockAdapter_TerminatesTheAgentLoop asserts the mock stops calling once a
// tool result comes back. Without this the demo loops forever.
func TestMockAdapter_TerminatesTheAgentLoop(t *testing.T) {
	t.Parallel()

	a := toolMock()
	req := &types.AegisRequest{
		Model: "claude-test",
		Tools: []types.Tool{{Type: types.ToolTypeFunction, Function: types.FunctionDef{Name: "read_file"}}},
		Messages: []types.Message{
			{Role: types.RoleUser, Content: types.TextContent("read a.go")},
			{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{
				ID: mockToolCallID, Type: types.ToolTypeFunction,
				Function: types.FunctionCallSpec{Name: "read_file", Arguments: "{}"},
			}}},
			{Role: types.RoleTool, ToolCallID: mockToolCallID, Content: types.TextContent("package main")},
		},
	}
	httpReq, _ := a.TransformRequest(context.Background(), req)
	resp, _ := a.SendRequest(httpReq)
	aegisResp, err := a.TransformResponse(context.Background(), resp)
	if err != nil {
		t.Fatalf("TransformResponse: %v", err)
	}
	if len(aegisResp.Choices[0].Message.ToolCalls) != 0 {
		t.Error("the mock called a tool again after receiving a tool result; the agent loop would not terminate")
	}
	if aegisResp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", aegisResp.Choices[0].FinishReason)
	}
}

// TestMockAdapter_ToolChoiceNoneIsHonoured covers the one tool_choice value
// that changes whether a call happens at all.
func TestMockAdapter_ToolChoiceNoneIsHonoured(t *testing.T) {
	t.Parallel()

	a := toolMock()
	req := &types.AegisRequest{
		Model:      "claude-test",
		Tools:      []types.Tool{{Type: types.ToolTypeFunction, Function: types.FunctionDef{Name: "read_file"}}},
		ToolChoice: types.ToolChoice{Mode: types.ToolChoiceNone},
		Messages:   []types.Message{{Role: types.RoleUser, Content: types.TextContent("read a.go")}},
	}
	httpReq, _ := a.TransformRequest(context.Background(), req)
	resp, _ := a.SendRequest(httpReq)
	aegisResp, _ := a.TransformResponse(context.Background(), resp)

	if len(aegisResp.Choices[0].Message.ToolCalls) != 0 {
		t.Error(`tool_choice "none" was ignored: the mock called a tool anyway`)
	}
}

// TestMockAdapter_StreamsToolCallAcrossDeltas asserts the mock emits a tool
// call the way a provider does, split across index-keyed deltas.
//
// A mock that emitted the whole call in one chunk would let a gateway that
// mishandles index-based accumulation pass the demo.
func TestMockAdapter_StreamsToolCallAcrossDeltas(t *testing.T) {
	t.Parallel()

	a := toolMock()
	req := &types.AegisRequest{
		Model:    "claude-test",
		Stream:   true,
		Tools:    []types.Tool{{Type: types.ToolTypeFunction, Function: types.FunctionDef{Name: "read_file"}}},
		Messages: []types.Message{{Role: types.RoleUser, Content: types.TextContent("read a.go")}},
	}
	httpReq, _ := a.TransformRequest(context.Background(), req)
	resp, err := a.SendRequest(httpReq)
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var argumentDeltas int
	var sawName bool
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimPrefix(scanner.Text(), "data: ")
		if line == "" || line == "[DONE]" || !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Index    *int `json:"index"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			t.Fatalf("mock emitted a chunk that is not valid JSON: %s", line)
		}
		for _, c := range chunk.Choices {
			for _, tc := range c.Delta.ToolCalls {
				if tc.Index == nil {
					t.Error("a tool call delta carried no index; a client cannot accumulate it")
				}
				if tc.Function.Name != "" {
					sawName = true
				}
				if tc.Function.Arguments != "" {
					argumentDeltas++
				}
			}
		}
	}

	if !sawName {
		t.Error("no delta carried the function name")
	}
	if argumentDeltas < 2 {
		t.Errorf("arguments arrived in %d delta(s); the mock must split them so the "+
			"accumulation path is actually exercised", argumentDeltas)
	}
}

// TestMockAdapter_CountsToolDefinitionTokens asserts tool definitions move the
// prompt token count.
//
// Tool definitions are billed as input. On a real provider the count comes from
// the provider's own usage block, so it is correct by construction; the mock is
// the one path where AEGIS produces the number itself, and a mock that ignored
// tools would report a cost that does not move when a request carries fifty of
// them.
func TestMockAdapter_CountsToolDefinitionTokens(t *testing.T) {
	t.Parallel()

	messages := []types.Message{{Role: types.RoleUser, Content: types.TextContent("hi")}}

	usage := func(req *types.AegisRequest) int {
		a := toolMock()
		httpReq, err := a.TransformRequest(context.Background(), req)
		if err != nil {
			t.Fatalf("TransformRequest: %v", err)
		}
		resp, err := a.SendRequest(httpReq)
		if err != nil {
			t.Fatalf("SendRequest: %v", err)
		}
		aegisResp, err := a.TransformResponse(context.Background(), resp)
		if err != nil {
			t.Fatalf("TransformResponse: %v", err)
		}
		return aegisResp.Usage.PromptTokens
	}

	bare := usage(&types.AegisRequest{Model: "m", Messages: messages})

	withTools := usage(&types.AegisRequest{
		Model:    "m",
		Messages: messages,
		Tools: []types.Tool{{
			Type: types.ToolTypeFunction,
			Function: types.FunctionDef{
				Name:        "read_file",
				Description: strings.Repeat("a long tool description. ", 20),
				Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			},
		}},
	})

	if withTools <= bare {
		t.Errorf("prompt tokens with tools (%d) did not exceed prompt tokens without (%d) — "+
			"tool definitions are billed as input and are not being accounted for", withTools, bare)
	}
}
