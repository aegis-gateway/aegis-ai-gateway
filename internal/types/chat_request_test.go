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
	"strings"
	"testing"
)

// TestDecode_ToolRoundTrip decodes the exact request shape an agent sends and
// asserts every tool field survives.
//
// This is the regression test for the reported defect. Before the change, this
// body decoded into a request with no tools, no tool calls and no tool call id,
// returned 200, and the agent loop stalled with nothing reporting a problem.
func TestDecode_ToolRoundTrip(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "aegis-fast",
		"tools": [{
			"type": "function",
			"function": {
				"name": "read_file",
				"description": "Read a file from disk",
				"parameters": {"type": "object", "properties": {"path": {"type": "string"}}}
			}
		}],
		"tool_choice": "auto",
		"parallel_tool_calls": false,
		"messages": [
			{"role": "user", "content": "Read src/main.go and summarise it"},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": "call_abc", "type": "function",
				 "function": {"name": "read_file", "arguments": "{\"path\":\"src/main.go\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_abc", "content": "package main"}
		]
	}`)

	req, err := DecodeChatCompletion(body)
	if err != nil {
		t.Fatalf("decoding a standard tool-calling request failed: %v", err)
	}

	if len(req.Tools) != 1 {
		t.Fatalf("tools: got %d, want 1 — the tool definitions were dropped", len(req.Tools))
	}
	if req.Tools[0].Function.Name != "read_file" {
		t.Errorf("tool name = %q, want read_file", req.Tools[0].Function.Name)
	}
	if !json.Valid(req.Tools[0].Function.Parameters) {
		t.Error("tool parameters did not survive as valid JSON")
	}
	if req.ToolChoice.Mode != ToolChoiceAuto {
		t.Errorf("tool_choice = %q, want auto", req.ToolChoice.Mode)
	}
	if req.ParallelToolCalls == nil || *req.ParallelToolCalls {
		t.Error("parallel_tool_calls=false did not survive")
	}

	assistant := req.Messages[1]
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant tool_calls: got %d, want 1 — the tool call was dropped", len(assistant.ToolCalls))
	}
	if assistant.ToolCalls[0].ID != "call_abc" {
		t.Errorf("tool call id = %q, want call_abc", assistant.ToolCalls[0].ID)
	}
	if got := assistant.ToolCalls[0].Function.Arguments; got != `{"path":"src/main.go"}` {
		t.Errorf("tool call arguments = %q, want the original JSON string", got)
	}
	if assistant.Content.Kind != ContentAbsent {
		t.Errorf("content kind = %v, want ContentAbsent — a null content is not an empty string, "+
			"and rewriting it changes the conversation the provider is asked to continue", assistant.Content.Kind)
	}

	result := req.Messages[2]
	if result.Role != RoleTool || result.ToolCallID != "call_abc" {
		t.Errorf("tool result message = role %q id %q, want role tool id call_abc", result.Role, result.ToolCallID)
	}
	if result.Content.Flatten() != "package main" {
		t.Errorf("tool result content = %q, want the file body", result.Content.Flatten())
	}
}

// TestDecode_StructuredContent covers the array form of content.
func TestDecode_StructuredContent(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"m","messages":[{"role":"user","content":[
		{"type":"text","text":"first"},
		{"type":"text","text":"second"}
	]}]}`)

	req, err := DecodeChatCompletion(body)
	if err != nil {
		t.Fatalf("decoding structured content failed: %v", err)
	}
	c := req.Messages[0].Content
	if c.Kind != ContentParts || len(c.Parts) != 2 {
		t.Fatalf("content = %+v, want two text parts", c)
	}
	if got := c.Texts(); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Errorf("Texts() = %v, want [first second]", got)
	}
}

// TestDecode_RejectsNonTextContentPart is the Part 5 guarantee.
//
// The gateway cannot inspect an image, so an image part is an egress path to a
// provider that the secrets, PII and injection filters do not cover. Admitting
// one as a side effect of widening content would be a hole in the one claim
// this product is built on, so it is refused by name.
func TestDecode_RejectsNonTextContentPart(t *testing.T) {
	t.Parallel()

	for _, partType := range []string{"image_url", "input_audio", "file", ""} {
		body := []byte(`{"model":"m","messages":[{"role":"user","content":[
			{"type":"text","text":"describe this"},
			{"type":"` + partType + `","image_url":{"url":"https://example.invalid/x.png"}}
		]}]}`)

		_, err := DecodeChatCompletion(body)
		if err == nil {
			t.Errorf("content part type %q was accepted — an unscannable part would reach "+
				"the provider unfiltered", partType)
			continue
		}
		if !errors.Is(err, ErrNonTextContentPart) {
			t.Errorf("content part type %q: error %v, want ErrNonTextContentPart", partType, err)
		}
		if !strings.Contains(err.Error(), "content[1]") {
			t.Errorf("error does not name the offending part index: %v", err)
		}
	}
}

// TestDecode_RefusesUnsupportedFields walks the fields a client can send that
// AEGIS does not implement, and asserts each is refused by name.
//
// The table is the point. Any one of these accepted and dropped is a request
// answered differently than it was asked, with a 200 and no log line, which is
// the failure mode that produced the tool-calling bug.
func TestDecode_RefusesUnsupportedFields(t *testing.T) {
	t.Parallel()

	fields := map[string]string{
		"n":                     `3`,
		"response_format":       `{"type":"json_object"}`,
		"seed":                  `42`,
		"stop_sequences":        `["x"]`,
		"logprobs":              `true`,
		"top_logprobs":          `5`,
		"logit_bias":            `{"1234":-100}`,
		"presence_penalty":      `0.5`,
		"frequency_penalty":     `0.5`,
		"max_completion_tokens": `100`,
		"store":                 `true`,
		"metadata":              `{"k":"v"}`,
		"user":                  `"end-user-42"`,
		"stream_options":        `{"include_usage":true}`,
		"functions":             `[{"name":"f"}]`,
		"function_call":         `"auto"`,
		"service_tier":          `"scale"`,
		"modalities":            `["text","audio"]`,
		"audio":                 `{"voice":"alloy"}`,
		"prediction":            `{"type":"content"}`,
		"reasoning_effort":      `"high"`,
		"verbosity":             `"low"`,
		"web_search_options":    `{}`,
		"prompt_cache_key":      `"k"`,
		"safety_identifier":     `"s"`,
		// AEGIS's own fields, which were part of the public request namespace
		// only because the wire type and the internal type used to be one type.
		"classification":  `"RESTRICTED"`,
		"organization_id": `"other-org"`,
		"team_id":         `"other-team"`,
		"user_id":         `"other-user"`,
		"api_key_id":      `"other-key"`,
		"request_id":      `"chosen-by-client"`,
		"project":         `"p"`,
		"prefer_provider": `"openai"`,
		"trace_context":   `"t"`,
		"skip_cache":      `true`,
		// Not an OpenAI field at all. A typo must be refused for the same
		// reason a real unsupported field is.
		"tolls": `[]`,
	}

	for field, value := range fields {
		body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"` + field + `":` + value + `}`)

		_, err := DecodeChatCompletion(body)
		if err == nil {
			t.Errorf("field %q was accepted and silently discarded", field)
			continue
		}
		var unsupported *UnsupportedFieldError
		if !errors.As(err, &unsupported) {
			t.Errorf("field %q: error %v, want UnsupportedFieldError", field, err)
			continue
		}
		if unsupported.Field != field {
			t.Errorf("error names field %q, want %q", unsupported.Field, field)
		}
		if !strings.Contains(err.Error(), field) {
			t.Errorf("field %q is not named in the message a caller sees: %v", field, err)
		}
	}
}

// TestDecode_RefusesUnsupportedMessageFields covers the same rule one level
// down. An unknown key inside a message object is exactly how tool_calls and
// tool_call_id were lost.
func TestDecode_RefusesUnsupportedMessageFields(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"function_call", "refusal", "audio", "tool_cals"} {
		body := []byte(`{"model":"m","messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":"ok","` + field + `":null}
		]}`)

		_, err := DecodeChatCompletion(body)
		if err == nil {
			t.Errorf("message field %q was accepted and silently discarded", field)
			continue
		}
		var unsupported *UnsupportedFieldError
		if !errors.As(err, &unsupported) {
			t.Errorf("message field %q: error %v, want UnsupportedFieldError", field, err)
			continue
		}
		if unsupported.Path != "messages[1]" {
			t.Errorf("error path = %q, want messages[1] — a caller needs to know which message", unsupported.Path)
		}
	}
}

// TestDecode_AcceptsSupportedFields is the negative control. Without it, every
// test above is satisfied by a decoder that refuses everything.
func TestDecode_AcceptsSupportedFields(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model":"aegis-fast",
		"messages":[{"role":"system","content":"be brief","name":"sys"},
		            {"role":"user","content":"hi"}],
		"temperature":0.5,"max_tokens":100,"top_p":0.9,"stop":["END"],"stream":true
	}`)

	req, err := DecodeChatCompletion(body)
	if err != nil {
		t.Fatalf("a request using only supported fields was refused: %v", err)
	}
	if req.Model != "aegis-fast" || len(req.Messages) != 2 || !req.Stream {
		t.Errorf("supported fields did not survive: %+v", req)
	}
	if len(req.Stop) != 1 || req.Stop[0] != "END" {
		t.Errorf("stop = %v, want [END]", req.Stop)
	}
	if req.Messages[0].Name != "sys" {
		t.Errorf("message name did not survive")
	}
}

// TestDecode_StopAcceptsBothForms covers the OpenAI stop parameter, which is a
// string or an array of strings.
func TestDecode_StopAcceptsBothForms(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		json string
		want []string
	}{
		{`"END"`, []string{"END"}},
		{`["A","B"]`, []string{"A", "B"}},
		{`null`, nil},
	} {
		body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stop":` + tc.json + `}`)
		req, err := DecodeChatCompletion(body)
		if err != nil {
			t.Fatalf("stop %s was refused: %v", tc.json, err)
		}
		if len(req.Stop) != len(tc.want) {
			t.Errorf("stop %s decoded to %v, want %v", tc.json, req.Stop, tc.want)
		}
	}
}

// TestToolChoice_Forms covers every accepted tool_choice shape and asserts an
// unrecognised one is refused rather than passed through.
func TestToolChoice_Forms(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in       string
		mode     string
		function string
		wantErr  bool
	}{
		{`"auto"`, ToolChoiceAuto, "", false},
		{`"none"`, ToolChoiceNone, "", false},
		{`"required"`, ToolChoiceRequired, "", false},
		{`{"type":"function","function":{"name":"read_file"}}`, "", "read_file", false},
		{`"sometimes"`, "", "", true},
		{`{"type":"retrieval"}`, "", "", true},
		{`{"type":"function","function":{}}`, "", "", true},
	} {
		var got ToolChoice
		err := json.Unmarshal([]byte(tc.in), &got)
		if tc.wantErr {
			if err == nil {
				t.Errorf("tool_choice %s was accepted; an unrecognised value must not pass through", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("tool_choice %s was refused: %v", tc.in, err)
			continue
		}
		if got.Mode != tc.mode || got.Function != tc.function {
			t.Errorf("tool_choice %s = %+v, want mode %q function %q", tc.in, got, tc.mode, tc.function)
		}
	}
}

// TestContent_MarshalRoundTrip asserts content leaves in the shape it arrived
// in. A string rewritten as a one-element array, or a null rewritten as an
// empty string, is a different request than the client sent.
func TestContent_MarshalRoundTrip(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		`"plain string"`,
		`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`,
		`null`,
	} {
		var c Content
		if err := json.Unmarshal([]byte(in), &c); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
		out, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal %s: %v", in, err)
		}
		if string(out) != in {
			t.Errorf("round trip changed the wire shape: %s became %s", in, out)
		}
	}
}

// TestTextSegments_CoversEveryTextBearingElement is the structural counterpart
// to the filter conformance tests: it pins down what "everything a filter must
// scan" means, so that adding a text-bearing field without extending this
// function fails here rather than silently in production.
func TestTextSegments_CoversEveryTextBearingElement(t *testing.T) {
	t.Parallel()

	req := &AegisRequest{
		Messages: []Message{
			{Role: RoleUser, Content: PartsContent("part-a", "part-b")},
			{Role: RoleAssistant, ToolCalls: []ToolCall{
				{ID: "c1", Function: FunctionCallSpec{Name: "f", Arguments: "args-here"}},
			}},
			{Role: RoleTool, ToolCallID: "c1", Content: TextContent("result-here")},
		},
		Tools: []Tool{{
			Type: ToolTypeFunction,
			Function: FunctionDef{
				Name:        "f",
				Description: "description-here",
				Parameters:  json.RawMessage(`{"schema":"params-here"}`),
			},
		}},
	}

	byText := map[string]SegmentKind{}
	for _, seg := range req.TextSegments() {
		byText[seg.Text] = seg.Kind
	}

	want := map[string]SegmentKind{
		"part-a":                   SegmentContentPart,
		"part-b":                   SegmentContentPart,
		"args-here":                SegmentToolCallArguments,
		"result-here":              SegmentToolResult,
		"description-here":         SegmentToolDefinition,
		`{"schema":"params-here"}`: SegmentToolDefinition,
	}
	for text, kind := range want {
		got, ok := byText[text]
		if !ok {
			t.Errorf("%q is not in TextSegments — no filter will ever see it", text)
			continue
		}
		if got != kind {
			t.Errorf("%q has kind %q, want %q", text, got, kind)
		}
	}

	// A tool result must be marked untrusted: it is content from outside the
	// model, and that classification is what says where indirect prompt
	// injection arrives.
	for _, seg := range req.TextSegments() {
		if seg.Text == "result-here" && !seg.IsUntrusted() {
			t.Error("a tool result segment is not marked untrusted")
		}
		if seg.Text == "part-a" && seg.IsUntrusted() {
			t.Error("a user content part is marked untrusted")
		}
	}
}

// TestHasTools drives the capability gate that refuses a tool-bearing request
// routed to a provider that cannot express tools.
func TestHasTools(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		req  AegisRequest
		want bool
	}{
		{"no tools", AegisRequest{Messages: []Message{{Role: RoleUser, Content: TextContent("hi")}}}, false},
		{"tool definitions", AegisRequest{Tools: []Tool{{Function: FunctionDef{Name: "f"}}}}, true},
		{"tool_choice only", AegisRequest{ToolChoice: ToolChoice{Mode: ToolChoiceAuto}}, true},
		{"history carries a call", AegisRequest{Messages: []Message{
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c"}}},
		}}, true},
		{"history carries a result", AegisRequest{Messages: []Message{
			{Role: RoleTool, ToolCallID: "c", Content: TextContent("r")},
		}}, true},
	}
	for _, tc := range cases {
		if got := tc.req.HasTools(); got != tc.want {
			t.Errorf("%s: HasTools() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestToolNames_AreNamesOnly guards the no-payload boundary on the metadata
// this change exposes to policy and to log lines.
func TestToolNames_AreNamesOnly(t *testing.T) {
	t.Parallel()

	const argumentPayload = "SECRET_ARGUMENT_VALUE"
	req := &AegisRequest{
		Tools: []Tool{{Function: FunctionDef{Name: "read_file", Description: "desc"}}},
		Messages: []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "c1", Function: FunctionCallSpec{Name: "read_file", Arguments: `{"path":"` + argumentPayload + `"}`}},
			{ID: "c2", Function: FunctionCallSpec{Name: "read_file", Arguments: `{}`}},
		}}},
	}

	offered := strings.Join(req.ToolNames(), " ")
	called := strings.Join(req.CalledToolNames(), " ")

	if offered != "read_file" {
		t.Errorf("ToolNames() = %q, want read_file", offered)
	}
	// Deduplicated: two calls to the same tool are one capability exercised.
	if called != "read_file" {
		t.Errorf("CalledToolNames() = %q, want a single deduplicated read_file", called)
	}
	for _, got := range []string{offered, called} {
		if strings.Contains(got, argumentPayload) {
			t.Errorf("tool name metadata carries argument payload: %q", got)
		}
	}
}
