//go:build live

// Live translation tests against the real Anthropic Messages API.
//
// Guarded by the `live` build tag and skipped without ANTHROPIC_API_KEY,
// because they cost money and need a credential. Run with:
//
//	go test ./internal/router/adapters/ -tags=live -run Live -v
//
// They exist because a translation validated only against fixtures is
// validated against the author's belief about the provider. Every mapping here
// is asserted against what the provider actually accepts.
package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

func liveAdapter(t *testing.T) *AnthropicAdapter {
	t.Helper()
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	return NewAnthropicAdapter(config.ProviderConfig{
		BaseURL: "https://api.anthropic.com/v1", APIKey: key,
	}, &http.Client{})
}

const liveModel = "claude-haiku-4-5"

var weatherTool = types.Tool{
	Type: types.ToolTypeFunction,
	Function: types.FunctionDef{
		Name:        "get_weather",
		Description: "Get the weather for a city",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
	},
}

// TestLive_ToolCallRoundTrip drives the whole loop through the adapter: offer a
// tool, get a call back, return the result, get an answer. If any part of the
// translation is wrong the provider says so rather than a fixture agreeing with
// the code that produced it.
func TestLive_ToolCallRoundTrip(t *testing.T) {
	a := liveAdapter(t)
	ctx := context.Background()

	req := &types.AegisRequest{
		Model:      liveModel,
		MaxTokens:  intPtr(512),
		Tools:      []types.Tool{weatherTool},
		ToolChoice: types.ToolChoice{Mode: types.ToolChoiceAuto},
		Messages: []types.Message{
			{Role: types.RoleUser, Content: types.TextContent("What is the weather in Paris?")},
		},
	}

	httpReq, err := a.TransformRequest(ctx, req)
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	resp, err := a.SendRequest(httpReq)
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}
	aegisResp, err := a.TransformResponse(ctx, resp)
	if err != nil {
		t.Fatalf("TransformResponse: %v", err)
	}

	choice := aegisResp.Choices[0]
	if choice.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", choice.FinishReason)
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %d, want 1", len(choice.Message.ToolCalls))
	}
	call := choice.Message.ToolCalls[0]
	if call.Function.Name != "get_weather" {
		t.Errorf("name = %q, want get_weather", call.Function.Name)
	}
	if !json.Valid([]byte(call.Function.Arguments)) {
		t.Errorf("arguments are not valid JSON: %q", call.Function.Arguments)
	}
	t.Logf("call: %s(%s) id=%s", call.Function.Name, call.Function.Arguments, call.ID)

	// Second turn: return the result the way an OpenAI client would, as a
	// message with role "tool".
	req.Messages = append(req.Messages,
		types.Message{Role: types.RoleAssistant, ToolCalls: choice.Message.ToolCalls},
		types.Message{Role: types.RoleTool, ToolCallID: call.ID, Content: types.TextContent("18C and clear")},
	)
	httpReq2, err := a.TransformRequest(ctx, req)
	if err != nil {
		t.Fatalf("TransformRequest turn two: %v", err)
	}
	resp2, err := a.SendRequest(httpReq2)
	if err != nil {
		t.Fatalf("SendRequest turn two: %v", err)
	}
	final, err := a.TransformResponse(ctx, resp2)
	if err != nil {
		t.Fatalf("TransformResponse turn two: %v", err)
	}
	answer := final.Choices[0].Message.Content.Flatten()
	if answer == "" {
		t.Fatal("the tool result turn produced no answer")
	}
	if final.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", final.Choices[0].FinishReason)
	}
	t.Logf("answer: %.90s", answer)
}

// TestLive_ToolChoiceValues asserts every tool_choice AEGIS accepts is accepted
// by the provider after translation. A mapping that produced an invalid value
// would fail here rather than in production.
func TestLive_ToolChoiceValues(t *testing.T) {
	a := liveAdapter(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		choice types.ToolChoice
		want   string
	}{
		{"auto", types.ToolChoice{Mode: types.ToolChoiceAuto}, "tool_calls"},
		{"required maps to any", types.ToolChoice{Mode: types.ToolChoiceRequired}, "tool_calls"},
		{"none", types.ToolChoice{Mode: types.ToolChoiceNone}, "stop"},
		{"named function maps to tool", types.ToolChoice{Function: "get_weather"}, "tool_calls"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &types.AegisRequest{
				Model: liveModel, MaxTokens: intPtr(256),
				Tools: []types.Tool{weatherTool}, ToolChoice: tc.choice,
				Messages: []types.Message{{Role: types.RoleUser, Content: types.TextContent("What is the weather in Paris?")}},
			}
			httpReq, err := a.TransformRequest(ctx, req)
			if err != nil {
				t.Fatalf("TransformRequest: %v", err)
			}
			resp, err := a.SendRequest(httpReq)
			if err != nil {
				t.Fatalf("SendRequest: %v", err)
			}
			aegisResp, err := a.TransformResponse(ctx, resp)
			if err != nil {
				t.Fatalf("TransformResponse: %v (the provider refused the translated tool_choice)", err)
			}
			if got := aegisResp.Choices[0].FinishReason; got != tc.want {
				t.Errorf("finish_reason = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLive_ParallelToolCalls covers both the default and the disable path,
// which Anthropic expresses inside tool_choice rather than at the top level.
func TestLive_ParallelToolCalls(t *testing.T) {
	a := liveAdapter(t)
	ctx := context.Background()
	timeTool := types.Tool{Type: types.ToolTypeFunction, Function: types.FunctionDef{
		Name: "get_time", Description: "Get the current time in a city",
		Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
	}}
	prompt := "What is the weather AND the time in Paris? Call both tools."

	run := func(parallel *bool) int {
		req := &types.AegisRequest{
			Model: liveModel, MaxTokens: intPtr(512),
			Tools: []types.Tool{weatherTool, timeTool}, ParallelToolCalls: parallel,
			Messages: []types.Message{{Role: types.RoleUser, Content: types.TextContent(prompt)}},
		}
		httpReq, err := a.TransformRequest(ctx, req)
		if err != nil {
			t.Fatalf("TransformRequest: %v", err)
		}
		resp, err := a.SendRequest(httpReq)
		if err != nil {
			t.Fatalf("SendRequest: %v", err)
		}
		aegisResp, err := a.TransformResponse(ctx, resp)
		if err != nil {
			t.Fatalf("TransformResponse: %v", err)
		}
		return len(aegisResp.Choices[0].Message.ToolCalls)
	}

	if n := run(nil); n < 2 {
		t.Errorf("default produced %d tool call(s), want at least 2", n)
	}
	no := false
	if n := run(&no); n != 1 {
		t.Errorf("parallel_tool_calls=false produced %d tool call(s), want 1", n)
	}
}

// TestLive_StreamingIndexRemap is the one that matters most.
//
// Anthropic numbers every content block in one sequence; OpenAI numbers tool
// calls in their own. This asks the model for prose before the call, which is
// what separates the two index spaces, and asserts the client-facing ordinals
// start at zero and reassemble.
func TestLive_StreamingIndexRemap(t *testing.T) {
	a := liveAdapter(t)
	ctx := context.Background()

	req := &types.AegisRequest{
		Model: liveModel, MaxTokens: intPtr(512), Stream: true,
		Tools: []types.Tool{weatherTool},
		Messages: []types.Message{{Role: types.RoleUser, Content: types.TextContent(
			"Say one short sentence about what you are about to do, then call the weather tool for Paris.")}},
	}
	httpReq, err := a.TransformRequest(ctx, req)
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	resp, err := a.SendRequest(httpReq)
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	transformer := a.NewStreamTransformer()
	acc := map[int]*struct{ id, name, args string }{}
	var sawText bool

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		out, err := transformer.Transform([]byte(strings.TrimPrefix(line, "data: ")))
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		if out == nil || string(out) == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    *int `json:"index"`
						ID       string
						Function struct{ Name, Arguments string }
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(out, &chunk); err != nil {
			t.Fatalf("emitted chunk is not valid JSON: %s", out)
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				sawText = true
			}
			for _, tc := range c.Delta.ToolCalls {
				if tc.Index == nil {
					t.Fatal("emitted a tool call delta with no index; a client cannot accumulate it")
				}
				s, ok := acc[*tc.Index]
				if !ok {
					s = &struct{ id, name, args string }{}
					acc[*tc.Index] = s
				}
				if tc.ID != "" {
					s.id = tc.ID
				}
				s.name += tc.Function.Name
				s.args += tc.Function.Arguments
			}
		}
	}

	if !sawText {
		t.Log("note: the model produced no prose, so the two index spaces may not have diverged this run")
	}
	if len(acc) != 1 {
		t.Fatalf("reassembled %d tool call(s), want 1", len(acc))
	}
	s, ok := acc[0]
	if !ok {
		var got []int
		for k := range acc {
			got = append(got, k)
		}
		t.Fatalf("the tool call arrived at ordinal(s) %v, want 0. Anthropic's content block index "+
			"was relayed without being remapped onto the OpenAI tool call ordinal, so a client "+
			"accumulating by index sees a gap", got)
	}
	if s.name != "get_weather" {
		t.Errorf("name = %q, want get_weather", s.name)
	}
	if !json.Valid([]byte(s.args)) {
		t.Errorf("reassembled arguments are not valid JSON: %q", s.args)
	}
	t.Logf("ordinal 0: %s(%s) id=%s  prose_before_call=%v", s.name, s.args, s.id, sawText)
}

// TestLive_UnmappableConstructsAreRefused asserts AEGIS refuses before dispatch
// rather than letting the provider produce an opaque 400.
func TestLive_UnmappableConstructsAreRefused(t *testing.T) {
	a := liveAdapter(t)
	ctx := context.Background()

	cases := []struct {
		name string
		msgs []types.Message
	}{
		{
			"a tool call the conversation never answers",
			[]types.Message{
				{Role: types.RoleUser, Content: types.TextContent("weather?")},
				{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{
					ID: "toolu_x", Type: types.ToolTypeFunction,
					Function: types.FunctionCallSpec{Name: "get_weather", Arguments: `{"city":"Paris"}`}}}},
			},
		},
		{
			"a message between the call and its result",
			[]types.Message{
				{Role: types.RoleUser, Content: types.TextContent("weather?")},
				{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{
					ID: "toolu_y", Type: types.ToolTypeFunction,
					Function: types.FunctionCallSpec{Name: "get_weather", Arguments: `{"city":"Paris"}`}}}},
				{Role: types.RoleUser, Content: types.TextContent("actually never mind")},
				{Role: types.RoleTool, ToolCallID: "toolu_y", Content: types.TextContent("18C")},
			},
		},
		{
			"a tool result answering no call",
			[]types.Message{
				{Role: types.RoleUser, Content: types.TextContent("hi")},
				{Role: types.RoleTool, ToolCallID: "toolu_nope", Content: types.TextContent("x")},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &types.AegisRequest{
				Model: liveModel, MaxTokens: intPtr(256),
				Tools: []types.Tool{weatherTool}, Messages: tc.msgs,
			}
			_, err := a.TransformRequest(ctx, req)
			if err == nil {
				t.Fatal("AEGIS accepted a construct the provider cannot express; it would have " +
					"reached the provider and returned an opaque 400")
			}
			var unmappable *UnmappableError
			if !errorsAs(err, &unmappable) {
				t.Fatalf("error %v is not an UnmappableError, so the caller gets no named construct", err)
			}
			t.Logf("refused: %v", err)
		})
	}
}

func intPtr(i int) *int { return &i }

func errorsAs(err error, target **UnmappableError) bool {
	for err != nil {
		if u, ok := err.(*UnmappableError); ok {
			*target = u
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

var _ = io.Discard
