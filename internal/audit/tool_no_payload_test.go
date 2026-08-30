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

// This file extends the zero-retention conformance coverage to the request
// surface that tool calling introduced.
//
// no_payload_test.go established that the audit trail carries no prompt or
// completion text. Tool calling added three more things a request can carry, and
// all three are payload by the same reasoning: a tool definition is text the
// client wrote, a tool call's arguments are values the model chose from the
// conversation, and a tool result is a document fetched from somewhere. None of
// them may reach an audit row, a usage record, or a filter result.
//
// The tests here are structural, in the same spirit as the existing ones: they
// assert over types rather than over a database, so they run on a clean machine
// and fail at review time rather than at audit time.
//
// no_payload_test.go and no_payload_integration_test.go are deliberately not
// modified; this is additive.
package audit_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/storage"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// toolPayloadFieldNames are field names that would indicate a persisted type
// carries tool payload rather than tool metadata.
//
// "ToolName" and "ToolsOffered" are deliberately absent: a tool name says which
// capability was exercised and is metadata by the same rule that admits
// "model". Arguments and results are the payload half.
var toolPayloadFieldNames = map[string]bool{
	"ToolCalls":     true,
	"ToolCall":      true,
	"Arguments":     true,
	"ToolArguments": true,
	"ToolResult":    true,
	"ToolResults":   true,
	"ToolOutput":    true,
	"Tools":         true,
	"ToolDefs":      true,
	"Parameters":    true,
	"FunctionCall":  true,
	"Segments":      true,
	"TextSegments":  true,
}

// TestNoPayload_AuditTypesCarryNoToolPayload walks the types the audit trail
// writes and reads, and fails if any of them gains a field that would hold a
// tool definition, a call's arguments, or a tool result.
//
// audit.Event is the write side and is the type that matters most: everything
// that reaches the audit tables passes through it.
func TestNoPayload_AuditTypesCarryNoToolPayload(t *testing.T) {
	t.Parallel()

	for _, target := range []struct {
		name string
		typ  reflect.Type
	}{
		{"audit.Event", reflect.TypeOf(audit.Event{})},
		{"audit.EventRow", reflect.TypeOf(audit.EventRow{})},
		{"storage.UsageRecord", reflect.TypeOf(storage.UsageRecord{})},
	} {
		assertNoToolPayloadFields(t, target.name, target.typ, map[reflect.Type]bool{})
	}
}

func assertNoToolPayloadFields(t *testing.T, label string, typ reflect.Type, visited map[reflect.Type]bool) {
	t.Helper()

	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || visited[typ] {
		return
	}
	visited[typ] = true

	for i := range typ.NumField() {
		field := typ.Field(i)
		if toolPayloadFieldNames[field.Name] {
			t.Errorf("%s: field %q may hold tool payload. A tool definition, a tool call's "+
				"arguments and a tool result are all request content, and the audit trail "+
				"stores metadata only. If the intent is to record which capability was "+
				"exercised, store the tool name",
				label, field.Name)
		}

		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		switch strings.ToLower(tag) {
		case "tools", "tool_calls", "arguments", "tool_result", "parameters", "function_call":
			t.Errorf("%s: field %q has json tag %q, which would expose tool payload through the API",
				label, field.Name, tag)
		}

		ft := field.Type
		if ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			assertNoToolPayloadFields(t, label+"."+field.Name, ft, visited)
		}
	}
}

// TestNoPayload_ToolMetadataAccessorsReturnNamesOnly pins down the boundary
// this change actually walks up to.
//
// Tool names are now exposed to Rego policy input and to the completion log
// line, which makes types.AegisRequest.ToolNames and CalledToolNames the two
// functions standing between a tool call and the record of it. If either ever
// returned a name concatenated with its arguments, payload would enter both the
// policy input and the process log without any of the tests above noticing,
// because no struct field would have changed.
func TestNoPayload_ToolMetadataAccessorsReturnNamesOnly(t *testing.T) {
	t.Parallel()

	const (
		argumentPayload = "PAYLOAD_IN_ARGUMENTS_a91f7c"
		schemaPayload   = "PAYLOAD_IN_SCHEMA_4d02be"
		descPayload     = "PAYLOAD_IN_DESCRIPTION_77af31"
		resultPayload   = "PAYLOAD_IN_TOOL_RESULT_e5c8d0"
	)

	req := &types.AegisRequest{
		Tools: []types.Tool{{
			Type: types.ToolTypeFunction,
			Function: types.FunctionDef{
				Name:        "read_file",
				Description: descPayload,
				Parameters:  []byte(`{"x":"` + schemaPayload + `"}`),
			},
		}},
		Messages: []types.Message{
			{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{
				ID:       "c1",
				Function: types.FunctionCallSpec{Name: "read_file", Arguments: `{"p":"` + argumentPayload + `"}`},
			}}},
			{Role: types.RoleTool, ToolCallID: "c1", Content: types.TextContent(resultPayload)},
		},
	}

	payloads := []string{argumentPayload, schemaPayload, descPayload, resultPayload}

	for _, got := range append(req.ToolNames(), req.CalledToolNames()...) {
		for _, p := range payloads {
			if strings.Contains(got, p) {
				t.Errorf("tool name metadata %q carries request payload %q — this value reaches "+
					"the Rego policy input and the completion log line", got, p)
			}
		}
		if got != "read_file" {
			t.Errorf("tool name metadata = %q, want the bare tool name", got)
		}
	}
}
