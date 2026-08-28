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
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// These run without a credential. The live counterparts in
// anthropic_live_test.go prove the same mappings against the real API; these
// pin the wire shape so a regression is caught in CI, where no key exists.

func transform(t *testing.T, req *types.AegisRequest) map[string]any {
	t.Helper()
	a := NewAnthropicAdapter(config.ProviderConfig{BaseURL: "http://anthropic.invalid/v1"}, nil)
	httpReq, err := a.TransformRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	raw, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("body is not valid JSON: %s", raw)
	}
	return body
}

func userMsg(text string) types.Message {
	return types.Message{Role: types.RoleUser, Content: types.TextContent(text)}
}

var testTool = types.Tool{Type: types.ToolTypeFunction, Function: types.FunctionDef{
	Name: "get_weather", Description: "Get the weather",
	Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
}}

// TestAnthropicTools_DefinitionShape pins the field rename. Sending OpenAI's
// `parameters` is refused by the provider with
// "tools.0.custom.input_schema: Field required".
func TestAnthropicTools_DefinitionShape(t *testing.T) {
	t.Parallel()
	body := transform(t, &types.AegisRequest{
		Model: "m", Tools: []types.Tool{testTool}, Messages: []types.Message{userMsg("hi")},
	})
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v", body["tools"])
	}
	tool := tools[0].(map[string]any)
	if _, ok := tool["input_schema"]; !ok {
		t.Error("tool has no input_schema; the provider requires it")
	}
	if _, ok := tool["parameters"]; ok {
		t.Error("tool still carries the OpenAI `parameters` spelling")
	}
	if _, ok := tool["function"]; ok {
		t.Error("tool is still nested under `function`; Anthropic's shape is flat")
	}
	if tool["name"] != "get_weather" {
		t.Errorf("name = %v", tool["name"])
	}
}

// TestAnthropicTools_ToolChoiceMapping covers every value AEGIS accepts.
func TestAnthropicTools_ToolChoiceMapping(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in       types.ToolChoice
		wantType string
		wantName string
	}{
		{types.ToolChoice{Mode: types.ToolChoiceAuto}, "auto", ""},
		{types.ToolChoice{Mode: types.ToolChoiceNone}, "none", ""},
		// "required" means the model must call something. Anthropic spells
		// that "any" and refuses a type of "required" outright.
		{types.ToolChoice{Mode: types.ToolChoiceRequired}, "any", ""},
		{types.ToolChoice{Function: "get_weather"}, "tool", "get_weather"},
	} {
		body := transform(t, &types.AegisRequest{
			Model: "m", Tools: []types.Tool{testTool}, ToolChoice: tc.in,
			Messages: []types.Message{userMsg("hi")},
		})
		got, ok := body["tool_choice"].(map[string]any)
		if !ok {
			t.Errorf("%v: no tool_choice emitted", tc.in)
			continue
		}
		if got["type"] != tc.wantType {
			t.Errorf("%v -> type %v, want %v", tc.in, got["type"], tc.wantType)
		}
		if tc.wantName != "" && got["name"] != tc.wantName {
			t.Errorf("%v -> name %v, want %v", tc.in, got["name"], tc.wantName)
		}
	}
}

// TestAnthropicTools_ParallelIsNegated is the regression for a real bug.
//
// OpenAI's parallel_tool_calls and Anthropic's disable_parallel_tool_use are
// negations of each other, and the first implementation assigned the incoming
// pointer straight across. That emitted disable_parallel_tool_use:false, which
// means the opposite of what the caller asked for. Nothing about the types
// objected; a live call returning two tool calls is what caught it.
func TestAnthropicTools_ParallelIsNegated(t *testing.T) {
	t.Parallel()

	no := false
	body := transform(t, &types.AegisRequest{
		Model: "m", Tools: []types.Tool{testTool}, ParallelToolCalls: &no,
		Messages: []types.Message{userMsg("hi")},
	})
	tc, ok := body["tool_choice"].(map[string]any)
	if !ok {
		t.Fatal("parallel_tool_calls=false emitted no tool_choice; Anthropic carries the flag inside it")
	}
	if tc["disable_parallel_tool_use"] != true {
		t.Errorf("disable_parallel_tool_use = %v, want true. parallel_tool_calls=false means "+
			"parallel use is disabled; passing the value through unnegated tells the provider "+
			"the opposite", tc["disable_parallel_tool_use"])
	}
	// And it must not appear at the top level, where OpenAI puts it.
	if _, ok := body["parallel_tool_calls"]; ok {
		t.Error("parallel_tool_calls leaked to the top level; Anthropic does not accept it there")
	}

	yes := true
	body = transform(t, &types.AegisRequest{
		Model: "m", Tools: []types.Tool{testTool}, ParallelToolCalls: &yes,
		Messages: []types.Message{userMsg("hi")},
	})
	if tc, ok := body["tool_choice"].(map[string]any); ok {
		if _, present := tc["disable_parallel_tool_use"]; present {
			t.Error("parallel_tool_calls=true set disable_parallel_tool_use; it should be absent")
		}
	}
}

// TestAnthropicTools_ToolResultBecomesUserBlock pins the reshaping that the
// provider enforces: "tool_result blocks can only be in user messages".
func TestAnthropicTools_ToolResultBecomesUserBlock(t *testing.T) {
	t.Parallel()
	body := transform(t, &types.AegisRequest{
		Model: "m", Tools: []types.Tool{testTool},
		Messages: []types.Message{
			userMsg("weather?"),
			{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{
				ID: "toolu_1", Type: types.ToolTypeFunction,
				Function: types.FunctionCallSpec{Name: "get_weather", Arguments: `{"city":"Paris"}`}}}},
			{Role: types.RoleTool, ToolCallID: "toolu_1", Content: types.TextContent("18C")},
		},
	})
	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}

	assistant := msgs[1].(map[string]any)
	blocks := assistant["content"].([]any)
	use := blocks[0].(map[string]any)
	if use["type"] != "tool_use" || use["id"] != "toolu_1" || use["name"] != "get_weather" {
		t.Errorf("assistant block = %v, want a tool_use naming the call", use)
	}
	if _, isString := use["input"].(string); isString {
		t.Error("tool_use input was sent as a JSON string; Anthropic expects an object")
	}

	result := msgs[2].(map[string]any)
	if result["role"] != types.RoleUser {
		t.Errorf("tool result landed on role %v, want user", result["role"])
	}
	rb := result["content"].([]any)[0].(map[string]any)
	if rb["type"] != "tool_result" || rb["tool_use_id"] != "toolu_1" {
		t.Errorf("result block = %v, want a tool_result carrying tool_use_id", rb)
	}
}

// TestAnthropicTools_ConsecutiveResultsMerge covers parallel calls answered
// together. Anthropic requires the results to arrive in the single user turn
// immediately after the call, so a run of OpenAI tool messages cannot become a
// run of user messages.
func TestAnthropicTools_ConsecutiveResultsMerge(t *testing.T) {
	t.Parallel()
	body := transform(t, &types.AegisRequest{
		Model: "m", Tools: []types.Tool{testTool},
		Messages: []types.Message{
			userMsg("weather and time?"),
			{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{
				{ID: "a", Type: types.ToolTypeFunction, Function: types.FunctionCallSpec{Name: "get_weather", Arguments: "{}"}},
				{ID: "b", Type: types.ToolTypeFunction, Function: types.FunctionCallSpec{Name: "get_time", Arguments: "{}"}},
			}},
			{Role: types.RoleTool, ToolCallID: "a", Content: types.TextContent("18C")},
			{Role: types.RoleTool, ToolCallID: "b", Content: types.TextContent("14:00")},
		},
	})
	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3: two tool results must merge into one user turn", len(msgs))
	}
	blocks := msgs[2].(map[string]any)["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("got %d result blocks in the merged turn, want 2", len(blocks))
	}
}

// TestAnthropicTools_UnmappableRefusals asserts each construct is refused with
// a named error rather than approximated or forwarded.
func TestAnthropicTools_UnmappableRefusals(t *testing.T) {
	t.Parallel()
	a := NewAnthropicAdapter(config.ProviderConfig{BaseURL: "http://x.invalid/v1"}, nil)

	strictTool := types.Tool{Type: types.ToolTypeFunction, Function: types.FunctionDef{
		Name: "f", Strict: boolPtr(true),
		Parameters: json.RawMessage(`{"type":"object","properties":{}}`), // no additionalProperties:false
	}}

	for _, tc := range []struct {
		name string
		req  *types.AegisRequest
		want string
	}{
		{"unanswered tool call", &types.AegisRequest{Model: "m", Messages: []types.Message{
			userMsg("hi"),
			{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{ID: "x", Function: types.FunctionCallSpec{Name: "f", Arguments: "{}"}}}},
		}}, "unanswered"},
		{"message between call and result", &types.AegisRequest{Model: "m", Messages: []types.Message{
			userMsg("hi"),
			{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{ID: "x", Function: types.FunctionCallSpec{Name: "f", Arguments: "{}"}}}},
			userMsg("never mind"),
			{Role: types.RoleTool, ToolCallID: "x", Content: types.TextContent("r")},
		}}, "comes between"},
		{"orphan tool result", &types.AegisRequest{Model: "m", Messages: []types.Message{
			userMsg("hi"),
			{Role: types.RoleTool, ToolCallID: "nope", Content: types.TextContent("r")},
		}}, "follows none"},
		{"strict without additionalProperties", &types.AegisRequest{Model: "m",
			Tools: []types.Tool{strictTool}, Messages: []types.Message{userMsg("hi")}}, "additionalProperties"},
		{"tool call arguments that are not JSON", &types.AegisRequest{Model: "m", Messages: []types.Message{
			userMsg("hi"),
			{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{ID: "x", Function: types.FunctionCallSpec{Name: "f", Arguments: "not json"}}}},
			{Role: types.RoleTool, ToolCallID: "x", Content: types.TextContent("r")},
		}}, "not valid JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := a.TransformRequest(context.Background(), tc.req)
			if err == nil {
				t.Fatal("accepted a construct the provider cannot express; it would reach the " +
					"provider and come back as an opaque 400")
			}
			var u *UnmappableError
			if !errors.As(err, &u) {
				t.Fatalf("error %v is not an UnmappableError", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not explain the problem (looking for %q)", err, tc.want)
			}
		})
	}
}

// TestAnthropicTools_StrictWithAdditionalPropertiesIsAccepted is the negative
// control for the strict refusal.
func TestAnthropicTools_StrictWithAdditionalPropertiesIsAccepted(t *testing.T) {
	t.Parallel()
	body := transform(t, &types.AegisRequest{
		Model: "m", Messages: []types.Message{userMsg("hi")},
		Tools: []types.Tool{{Type: types.ToolTypeFunction, Function: types.FunctionDef{
			Name: "f", Strict: boolPtr(true),
			Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		}}},
	})
	if _, ok := body["tools"]; !ok {
		t.Error("a strict tool with additionalProperties:false was not forwarded")
	}
}

// TestAnthropicAdapter_SetsVersionHeader covers a requirement that used to be
// met only by configuration. Without anthropic-version every request is a 400,
// and the adapter was content to send one.
func TestAnthropicAdapter_SetsVersionHeader(t *testing.T) {
	t.Parallel()
	a := NewAnthropicAdapter(config.ProviderConfig{BaseURL: "http://x.invalid/v1"}, nil)
	req, err := a.TransformRequest(context.Background(), &types.AegisRequest{
		Model: "m", Messages: []types.Message{userMsg("hi")}})
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	if got := req.Header.Get("anthropic-version"); got == "" {
		t.Error("no anthropic-version header; the API refuses every request without it, and " +
			"relying on providers.yaml to supply it means one deleted line breaks all traffic")
	}

	// An operator pinning a different version must still win.
	a2 := NewAnthropicAdapter(config.ProviderConfig{
		BaseURL: "http://x.invalid/v1", Headers: map[string]string{"anthropic-version": "2099-01-01"},
	}, nil)
	req2, _ := a2.TransformRequest(context.Background(), &types.AegisRequest{
		Model: "m", Messages: []types.Message{userMsg("hi")}})
	if got := req2.Header.Get("anthropic-version"); got != "2099-01-01" {
		t.Errorf("configured version = %q, want the operator's value to override the default", got)
	}
}

func boolPtr(b bool) *bool { return &b }

var _ = http.MethodPost

// TestAnthropicTools_EverythingOnTheWireWasScanned is the filtering guarantee
// for this translation, asserted rather than argued.
//
// The argument runs: the canonical request is OpenAI-shaped, the filter chain
// runs on it before any adapter sees it, and the Anthropic shape exists only
// outbound, so translation cannot introduce an inbound channel. That reasoning
// is correct and it is not a check. If the translation ever synthesised text,
// or reached a field the scan surface does not cover, the argument would still
// read true while the property had gone.
//
// So: plant a unique sentinel in every text field, translate, and require that
// every sentinel appearing in the outgoing body also appears in the request's
// TextSegments. Anything on the wire that no filter read fails this.
func TestAnthropicTools_EverythingOnTheWireWasScanned(t *testing.T) {
	t.Parallel()

	req := &types.AegisRequest{
		Model: "SENTINELMODEL",
		Stop:  []string{"SENTINELSTOP"},
		Tools: []types.Tool{{
			Type: types.ToolTypeFunction,
			Function: types.FunctionDef{
				Name:        "SENTINELTOOLNAME",
				Description: "SENTINELTOOLDESC",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"p":{"description":"SENTINELSCHEMA"}}}`),
			},
		}},
		ToolChoice: types.ToolChoice{Function: "SENTINELTOOLNAME"},
		Messages: []types.Message{
			{Role: types.RoleSystem, Content: types.TextContent("SENTINELSYSTEM")},
			{Role: types.RoleUser, Name: "SENTINELPARTICIPANT", Content: types.PartsContent("SENTINELPARTA", "SENTINELPARTB")},
			{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{
				ID: "SENTINELCALLID", Type: types.ToolTypeFunction,
				Function: types.FunctionCallSpec{Name: "SENTINELCALLEDTOOL", Arguments: `{"a":"SENTINELARGS"}`},
			}}},
			{Role: types.RoleTool, ToolCallID: "SENTINELCALLID", Content: types.TextContent("SENTINELRESULT")},
		},
	}

	// What the filter chain saw.
	scanned := map[string]bool{}
	for _, seg := range req.TextSegments() {
		for _, s := range allSentinels {
			if strings.Contains(seg.Text, s) {
				scanned[s] = true
			}
		}
	}

	// What actually leaves for the provider.
	a := NewAnthropicAdapter(config.ProviderConfig{BaseURL: "http://x.invalid/v1"}, nil)
	httpReq, err := a.TransformRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	raw, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	wire := string(raw)

	for _, s := range allSentinels {
		onWire := strings.Contains(wire, s)
		if onWire && !scanned[s] {
			t.Errorf("%s reaches the provider but never appeared in TextSegments, so no filter "+
				"read it. Either add the field to the scan surface or stop sending it", s)
		}
	}

	// Negative control. If the fixture stopped putting anything on the wire the
	// loop above would pass vacuously.
	var found int
	for _, s := range allSentinels {
		if strings.Contains(wire, s) {
			found++
		}
	}
	if found < 6 {
		t.Fatalf("only %d sentinel(s) reached the wire; the fixture is not exercising the "+
			"translation and this test proves nothing", found)
	}
	t.Logf("%d sentinel(s) on the wire, all of them scanned", found)
}

// allSentinels is every marker planted by the test above.
//
// Model is deliberately absent. It is the one field that reaches the provider
// without being scanned, and the reason is upstream of the adapter: the handler
// replaces the client's alias with the provider-specific model name from
// configs/models.yaml before dispatch ("aegisReq.Model = providerModel"), so
// what an adapter sends is an operator-configured value, not client text. The
// alias the client did send is validated against that config and fails routing
// if it matches nothing. TestModelIsReplacedBeforeDispatch below keeps that
// justification honest.
var allSentinels = []string{
	"SENTINELSTOP", "SENTINELTOOLNAME", "SENTINELTOOLDESC",
	"SENTINELSCHEMA", "SENTINELSYSTEM", "SENTINELPARTICIPANT", "SENTINELPARTA",
	"SENTINELPARTB", "SENTINELCALLID", "SENTINELCALLEDTOOL", "SENTINELARGS",
	"SENTINELRESULT",
}

// TestModelIsReplacedBeforeDispatch grounds the one exemption in the test
// above.
//
// AegisRequest.Model is classified excluded from the scan surface, and it does
// reach the provider. That is only sound because the handler overwrites it with
// the provider-specific model name from configs/models.yaml before any adapter
// runs, so the string on the wire is operator configuration rather than
// anything a client typed.
//
// This asserts the substitution exists. If it were ever removed, the client's
// own model string would start reaching providers unscanned and the exemption
// would be wrong, with nothing else in the suite noticing.
func TestModelIsReplacedBeforeDispatch(t *testing.T) {
	t.Parallel()

	handler, err := os.ReadFile("../../gateway/handler.go")
	if err != nil {
		t.Fatalf("reading the handler: %v", err)
	}
	if !strings.Contains(string(handler), "aegisReq.Model = providerModel") {
		t.Error("the handler no longer replaces the requested model with the routed provider model " +
			"before dispatch. AegisRequest.Model is excluded from the scan surface on the grounds " +
			"that an adapter only ever sends an operator-configured value; without that " +
			"substitution the client's own string reaches the provider unscanned")
	}
}

// TestAnthropicTools_StrictReachesTheWire pins the flag itself rather than the
// presence of the tools array. Validating strict against the schema and then
// dropping it forwards a request the provider runs unenforced, and reports
// success for a tool the caller asked to have enforced.
func TestAnthropicTools_StrictReachesTheWire(t *testing.T) {
	t.Parallel()
	body := transform(t, &types.AegisRequest{
		Model: "m", Messages: []types.Message{userMsg("hi")},
		Tools: []types.Tool{{Type: types.ToolTypeFunction, Function: types.FunctionDef{
			Name: "f", Strict: boolPtr(true),
			Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		}}},
	})
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools not forwarded: %v", body["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	strict, present := tool["strict"]
	if !present {
		t.Fatal("strict was validated and then dropped, so the provider runs the tool " +
			"unenforced while the caller believes it is enforced")
	}
	if strict != true {
		t.Errorf("strict reached the wire as %v, want true", strict)
	}
}

// TestAnthropicTools_StrictAbsentWhenNotAsked is the negative control: a tool
// that never set strict must not gain it.
func TestAnthropicTools_StrictAbsentWhenNotAsked(t *testing.T) {
	t.Parallel()
	body := transform(t, &types.AegisRequest{
		Model: "m", Messages: []types.Message{userMsg("hi")}, Tools: []types.Tool{testTool},
	})
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools not forwarded: %v", body["tools"])
	}
	if tool, _ := tools[0].(map[string]any); tool["strict"] != nil {
		t.Errorf("strict appeared on a tool that did not ask for it: %v", tool["strict"])
	}
}

// TestAnthropicTools_AdjacencyGapsAreRefused covers the two interleavings the
// first version of the adjacency check let through. Both are valid OpenAI and
// both are refused by the provider, so letting them past produces an opaque
// provider 400 instead of the named refusal.
func TestAnthropicTools_AdjacencyGapsAreRefused(t *testing.T) {
	t.Parallel()
	a := NewAnthropicAdapter(config.ProviderConfig{BaseURL: "http://anthropic.invalid/v1"}, nil)

	callTurn := types.Message{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{
		{ID: "call_a", Function: types.FunctionCallSpec{Name: "f", Arguments: `{}`}},
	}}

	for _, tc := range []struct {
		name string
		msgs []types.Message
		want string
	}{
		{
			// An assistant turn with no tool_calls used to clear the pending
			// set instead of refusing, so this reached the provider.
			name: "assistant turn between a call and its result",
			msgs: []types.Message{
				userMsg("hi"), callTurn,
				{Role: types.RoleAssistant, Content: types.TextContent("thinking out loud")},
				{Role: types.RoleTool, ToolCallID: "call_a", Content: types.TextContent("r")},
			},
			want: "comes between",
		},
		{
			// A conversation that simply ends after an assistant turn that
			// followed the call. The call is never answered.
			name: "assistant turn ends the conversation with the call unanswered",
			msgs: []types.Message{
				userMsg("hi"), callTurn,
				{Role: types.RoleAssistant, Content: types.TextContent("never answered it")},
			},
			want: "comes between",
		},
		{
			// An extra result alongside a valid one. The old check only asked
			// whether every call was answered, never whether every result
			// answered a call.
			name: "extra tool result mixed in with a valid one",
			msgs: []types.Message{
				userMsg("hi"), callTurn,
				{Role: types.RoleTool, ToolCallID: "call_a", Content: types.TextContent("r")},
				{Role: types.RoleTool, ToolCallID: "call_ghost", Content: types.TextContent("r")},
			},
			want: "answers none",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := a.TransformRequest(context.Background(),
				&types.AegisRequest{Model: "m", Messages: tc.msgs})
			if err == nil {
				t.Fatal("accepted an interleaving the provider refuses; it would reach the " +
					"provider and come back as an opaque 400")
			}
			var u *UnmappableError
			if !errors.As(err, &u) {
				t.Fatalf("error %v is not an UnmappableError", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not explain the problem (looking for %q)", err, tc.want)
			}
		})
	}
}

// TestUnmappableConstructsAreNeverScannedText is the counterpart of
// TestSegmentRefsAreNeverScannedText in internal/types.
//
// An UnmappableError's Construct and Detail are interpolated into the client
// response body and into a structured log line. A tool call id and a tool name
// are scanned text, so quoting either in a refusal reproduces the leak that
// "keep scanned values out of validation error labels" removed from the
// validator: a correlator holding a credential would come back in a 400 and be
// logged, and the refusal happens before the filter chain ever sees it.
//
// Every refusal this translation can produce is driven with sentinel values in
// every scanned tool field, and the resulting message must contain none of them.
func TestUnmappableConstructsAreNeverScannedText(t *testing.T) {
	t.Parallel()
	a := NewAnthropicAdapter(config.ProviderConfig{BaseURL: "http://anthropic.invalid/v1"}, nil)

	// Each sentinel stands in for a credential a caller could have put in that
	// field. None may appear in a refusal.
	const (
		callID   = "SENTINEL_CALL_ID"
		callName = "SENTINEL_CALL_NAME"
		toolName = "SENTINEL_TOOL_NAME"
		toolDesc = "SENTINEL_TOOL_DESCRIPTION"
		toolPar  = "SENTINEL_TOOL_PARAMS"
		resultID = "SENTINEL_RESULT_ID"
	)
	sentinels := []string{callID, callName, toolName, toolDesc, toolPar, resultID}

	sentinelTool := types.Tool{Type: types.ToolTypeFunction, Function: types.FunctionDef{
		Name: toolName, Description: toolDesc, Strict: boolPtr(true),
		Parameters: json.RawMessage(`{"type":"object","properties":{"` + toolPar + `":{"type":"string"}}}`),
	}}
	callTurn := types.Message{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{
		{ID: callID, Function: types.FunctionCallSpec{Name: callName, Arguments: `{}`}},
	}}

	for _, tc := range []struct {
		name string
		req  *types.AegisRequest
	}{
		{"strict without additionalProperties", &types.AegisRequest{Model: "m",
			Tools: []types.Tool{sentinelTool}, Messages: []types.Message{userMsg("hi")}}},
		{"result answering no call", &types.AegisRequest{Model: "m", Messages: []types.Message{
			userMsg("hi"),
			{Role: types.RoleTool, ToolCallID: resultID, Content: types.TextContent("r")},
		}}},
		{"extra result alongside a valid one", &types.AegisRequest{Model: "m", Messages: []types.Message{
			userMsg("hi"), callTurn,
			{Role: types.RoleTool, ToolCallID: callID, Content: types.TextContent("r")},
			{Role: types.RoleTool, ToolCallID: resultID, Content: types.TextContent("r")},
		}}},
		{"call never answered", &types.AegisRequest{Model: "m", Messages: []types.Message{
			userMsg("hi"), callTurn,
		}}},
		{"user turn between call and result", &types.AegisRequest{Model: "m", Messages: []types.Message{
			userMsg("hi"), callTurn, userMsg("interrupting"),
			{Role: types.RoleTool, ToolCallID: callID, Content: types.TextContent("r")},
		}}},
		{"assistant turn between call and result", &types.AegisRequest{Model: "m", Messages: []types.Message{
			userMsg("hi"), callTurn,
			{Role: types.RoleAssistant, Content: types.TextContent("interrupting")},
			{Role: types.RoleTool, ToolCallID: callID, Content: types.TextContent("r")},
		}}},
		{"arguments that are not JSON", &types.AegisRequest{Model: "m", Messages: []types.Message{
			userMsg("hi"),
			{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{
				{ID: callID, Function: types.FunctionCallSpec{Name: callName, Arguments: "not json"}},
			}},
			{Role: types.RoleTool, ToolCallID: callID, Content: types.TextContent("r")},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := a.TransformRequest(context.Background(), tc.req)
			if err == nil {
				t.Fatal("expected a refusal; this case no longer exercises the invariant")
			}
			var u *UnmappableError
			if !errors.As(err, &u) {
				t.Fatalf("error %v is not an UnmappableError", err)
			}
			for _, s := range sentinels {
				if strings.Contains(err.Error(), s) {
					t.Errorf("refusal %q quotes scanned text %q. The message reaches the client "+
						"response body and the log line, and the refusal happens before the "+
						"filter chain, so a credential in that field would leak. Construct must "+
						"be positional: an index, or the name of the field", err, s)
				}
			}
		})
	}
}

// TestStrictSchemaWalk covers the nested additionalProperties requirement.
//
// The provider requires every object in a strict tool's schema to set
// additionalProperties:false, not only the root. The first implementation
// checked the root alone, so a strict tool with a nested object passed AEGIS
// and failed at the provider.
//
// Each expectation here was probed against the live API:
//
//	root false, nested object without it        400
//	root false, nested object with it           200
//	root without it                             400
//	object inside array items, without it       400
//
// The undecidable cases are equally deliberate. A schema using $ref or a
// composition keyword may be perfectly valid, and refusing a shape AEGIS does
// not understand rejects requests the provider would accept, which is worse for
// a caller than the provider's own error. That error names the tool and the
// requirement, so it is a reasonable thing to fall back to.
func TestStrictSchemaWalk(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		schema  string
		wantBad bool
	}{
		{"root sets it, no nesting", `{"type":"object","additionalProperties":false,"properties":{"x":{"type":"string"}}}`, false},
		{"root omits it", `{"type":"object","properties":{"x":{"type":"string"}}}`, true},
		{"nested object omits it", `{"type":"object","additionalProperties":false,"properties":{"n":{"type":"object","properties":{"x":{"type":"string"}}}}}`, true},
		{"nested object sets it", `{"type":"object","additionalProperties":false,"properties":{"n":{"type":"object","additionalProperties":false,"properties":{"x":{"type":"string"}}}}}`, false},
		{"object inside array items omits it", `{"type":"object","additionalProperties":false,"properties":{"l":{"type":"array","items":{"type":"object","properties":{"y":{"type":"string"}}}}}}`, true},
		{"object inside array items sets it", `{"type":"object","additionalProperties":false,"properties":{"l":{"type":"array","items":{"type":"object","additionalProperties":false,"properties":{"y":{"type":"string"}}}}}}`, false},
		{"non-object nodes are not objects", `{"type":"object","additionalProperties":false,"properties":{"s":{"type":"string"},"n":{"type":"number"}}}`, false},
		{"type given as a list including object", `{"type":["object","null"],"properties":{"x":{"type":"string"}}}`, true},

		// Undecidable: not refused, by design.
		{"$ref is left to the provider", `{"type":"object","additionalProperties":false,"properties":{"n":{"$ref":"#/$defs/thing"}}}`, false},
		{"allOf is left to the provider", `{"allOf":[{"type":"object"}]}`, false},
		{"anyOf is left to the provider", `{"type":"object","additionalProperties":false,"properties":{"n":{"anyOf":[{"type":"object"}]}}}`, false},
		{"a schema-valued additionalProperties is left alone", `{"type":"object","additionalProperties":{"type":"string"}}`, false},
		{"unparseable schema is left to the provider", `not json`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := firstObjectAllowingAdditionalProperties(json.RawMessage(tc.schema), "parameters")
			if tc.wantBad && got == "" {
				t.Errorf("expected a refusal path; the provider returns 400 for this schema and "+
					"AEGIS would forward it: %s", tc.schema)
			}
			if !tc.wantBad && got != "" {
				t.Errorf("refused at %q, but the provider accepts this schema. Refusing what the "+
					"provider would accept is worse for a caller than its own error: %s", got, tc.schema)
			}
		})
	}
}

// TestStrictSchemaWalk_NamesTheOffendingPath checks the refusal is actionable.
func TestStrictSchemaWalk_NamesTheOffendingPath(t *testing.T) {
	t.Parallel()

	got := firstObjectAllowingAdditionalProperties(json.RawMessage(
		`{"type":"object","additionalProperties":false,"properties":{"outer":{"type":"object","additionalProperties":false,"properties":{"inner":{"type":"object","properties":{}}}}}}`),
		"parameters")
	want := "parameters.properties.outer.properties.inner"
	if got != want {
		t.Errorf("path = %q, want %q; a caller needs to know which object to fix", got, want)
	}
}
