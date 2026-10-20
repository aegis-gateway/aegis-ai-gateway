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
	"reflect"
	"sort"
	"strings"
	"testing"
)

// /v1/messages forwards the client's own body, so the question scan_coverage
// asks of AegisRequest has to be asked of the body itself: does every string
// that will leave for the provider reach a filter? Two tests answer it, in the
// same pair shape as scan_coverage_test.go.
//
//   - TestMessagesScan_EveryStringFieldIsClassified walks MessagesRequest and
//     fails on any string-bearing field not classified below.
//   - TestMessagesScan_EveryBodyStringIsScannedOrVocabulary decodes a body with
//     a sentinel in every free-text position Claude Code uses, walks every
//     string in the RAW body, and fails if one is neither a fixed vocabulary
//     value the decoder enforces nor present in Projection().TextSegments().
//     It reads the raw body rather than the struct, because the raw body is
//     what is forwarded.

var messagesSurface = map[string]fieldRule{
	"MessagesRequest.Model": {excluded, "an alias validated against configs/models.yaml, replaced with the routed provider model before forwarding", ""},
	"MessagesRequest.Raw":   {excluded, "the forwarded body itself; every string in it is checked by TestMessagesScan_EveryBodyStringIsScannedOrVocabulary", ""},

	"MessagesRequest.Messages[].Role":                  {excluded, `"user" or "assistant"; the decoder refuses any other value`, ""},
	"MessagesRequest.System[].Type":                    {excluded, "a block type from the decoder's allowlist", ""},
	"MessagesRequest.Messages[].Blocks[].Type":         {excluded, "a block type from the decoder's allowlist", ""},
	"MessagesRequest.ToolChoice.Type":                  {excluded, "one of auto, any, tool, none; the decoder refuses any other value", ""},
	"MessagesRequest.System[].Image.Detail":            {excluded, "never set: the Messages API has no detail field and the decoder does not populate it", ""},
	"MessagesRequest.Messages[].Blocks[].Image.Detail": {excluded, "never set: the Messages API has no detail field and the decoder does not populate it", ""},
	"MessagesRequest.System[].Image.URL":               {excluded, "never set: system accepts only text blocks, and the decoder refuses an image there", ""},
	"MessagesRequest.Messages[].Blocks[].Image.URL":    {gatedUnscanned, "no image scanner exists; admitted only on routes opted in via unscanned_parts and sealed in the audit trail as unscanned", ""},

	"MessagesRequest.StopSequences":                 {scanned, "", ""},
	"MessagesRequest.MetadataUserID":                {scanned, "", ""},
	"MessagesRequest.ConfigStrings":                 {scanned, "", ""},
	"MessagesRequest.System[].Text":                 {scanned, "", ""},
	"MessagesRequest.Messages[].Blocks[].Text":      {scanned, "", ""},
	"MessagesRequest.Messages[].Blocks[].ID":        {scanned, "", ""},
	"MessagesRequest.Messages[].Blocks[].Name":      {scanned, "", ""},
	"MessagesRequest.Messages[].Blocks[].Input":     {scanned, "", ""},
	"MessagesRequest.Messages[].Blocks[].ToolUseID": {scanned, "", ""},
	"MessagesRequest.Messages[].Blocks[].Signature": {scanned, "", ""},
	"MessagesRequest.Messages[].Blocks[].Data":      {scanned, "", ""},
	"MessagesRequest.Tools[].Name":                  {scanned, "", ""},
	"MessagesRequest.Tools[].Description":           {scanned, "", ""},
	"MessagesRequest.Tools[].InputSchema":           {scanned, "", ""},
	"MessagesRequest.ToolChoice.Name":               {scanned, "", ""},
	// System blocks share MessagesBlock; only Text can be set there, and the
	// remaining fields are listed so the walk is fully accounted for.
	"MessagesRequest.System[].ID":        {excluded, "never set: system accepts only text blocks", ""},
	"MessagesRequest.System[].Name":      {excluded, "never set: system accepts only text blocks", ""},
	"MessagesRequest.System[].Input":     {excluded, "never set: system accepts only text blocks", ""},
	"MessagesRequest.System[].ToolUseID": {excluded, "never set: system accepts only text blocks", ""},
	"MessagesRequest.System[].Signature": {excluded, "never set: system accepts only text blocks", ""},
	"MessagesRequest.System[].Data":      {excluded, "never set: system accepts only text blocks", ""},
}

func TestMessagesScan_EveryStringFieldIsClassified(t *testing.T) {
	t.Parallel()

	found := walkStringFields(reflect.TypeOf(MessagesRequest{}), "MessagesRequest", map[reflect.Type]bool{})
	inFound := map[string]bool{}
	for _, path := range found {
		inFound[path] = true
		rule, ok := messagesSurface[path]
		if !ok {
			t.Errorf("%s is a string-bearing field of MessagesRequest and is not classified in messagesSurface. "+
				"Decide whether it is scanned or why it cannot carry client text to the provider", path)
			continue
		}
		if rule.disposition != scanned && strings.TrimSpace(rule.reason) == "" {
			t.Errorf("%s is not scanned and gives no reason", path)
		}
	}
	for path := range messagesSurface {
		if !inFound[path] {
			t.Errorf("messagesSurface names %q, which no longer exists", path)
		}
	}
}

// messagesFixturePNG is a 1x1 PNG, the one value the body test lets through
// unscanned, because it is gated.
const messagesFixturePNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

// messagesFixture is shaped on what Claude Code 2.1.150 sends, with every
// free-text position holding a sentinel.
const messagesFixture = `{
  "model": "aegis-claude",
  "max_tokens": 64000,
  "stream": true,
  "stop_sequences": ["S_stop"],
  "metadata": {"user_id": "S_metadata_user_id"},
  "thinking": {"type": "adaptive"},
  "context_management": {"edits": [{"type": "clear_thinking_20251015", "keep": "all", "exclude_tools": ["S_ctx_exclude_tool"]}]},
  "output_config": {"effort": "xhigh", "format": {"type": "json_schema", "schema": {"properties": {"S_schema_prop": {"description": "S_schema_desc"}}}}},
  "system": [
    {"type": "text", "text": "S_system_0"},
    {"type": "text", "text": "S_system_1", "cache_control": {"type": "ephemeral", "ttl": "1h"}}
  ],
  "tools": [
    {"name": "S_tool_name", "description": "S_tool_desc", "input_schema": {"type": "object", "properties": {"S_param": {"type": "string", "description": "S_param_desc"}}}, "cache_control": {"type": "ephemeral"}}
  ],
  "tool_choice": {"type": "tool", "name": "S_tool_choice_name", "disable_parallel_tool_use": true},
  "messages": [
    {"role": "user", "content": "S_user_string"},
    {"role": "assistant", "content": [
      {"type": "thinking", "thinking": "S_thinking_text", "signature": "S_thinking_signature"},
      {"type": "redacted_thinking", "data": "S_redacted_data"},
      {"type": "text", "text": "S_assistant_text"},
      {"type": "tool_use", "id": "S_tool_use_id", "name": "S_tool_use_name", "input": {"S_input_key": "S_input_value"}}
    ]},
    {"role": "user", "content": [
      {"type": "tool_result", "tool_use_id": "S_tool_result_id", "content": [
        {"type": "text", "text": "S_tool_result_text"},
        {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "` + messagesFixturePNG + `"}}
      ]},
      {"type": "tool_result", "tool_use_id": "S_tool_result_id2", "content": "S_tool_result_string", "is_error": true},
      {"type": "text", "text": "S_user_text", "cache_control": {"type": "ephemeral"}},
      {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "` + messagesFixturePNG + `"}}
    ]}
  ]
}`

// messagesVocabulary are the strings the decoder constrains to a fixed set, or
// which are configuration literals of the provider's own vocabulary. Each is
// forwarded unscanned, which is acceptable only because none can be chosen
// freely: anything else in the same position is refused at decode.
var messagesVocabulary = map[string]string{
	"aegis-claude": "model alias, replaced before forwarding",
	"user":         "role", "assistant": "role",
	"text": "block type", "image": "block type", "tool_use": "block type", "tool_result": "block type",
	"thinking": "block type", "redacted_thinking": "block type",
	"base64": "image source type", "image/png": "media type checked against the bytes",
	"ephemeral": "cache_control type", "1h": "cache_control ttl", "5m": "cache_control ttl",
	"tool": "tool_choice type",
}

func TestMessagesScan_EveryBodyStringIsScannedOrVocabulary(t *testing.T) {
	t.Parallel()

	req, err := DecodeMessages([]byte(messagesFixture))
	if err != nil {
		t.Fatalf("fixture refused: %v", err)
	}
	var seen []string
	for _, s := range req.Projection().TextSegments() {
		seen = append(seen, s.Text)
	}
	haystack := strings.Join(seen, "\x00")

	leaves, err := stringLeaves(json.RawMessage(messagesFixture))
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	sentinels := 0
	for _, leaf := range leaves {
		if strings.HasPrefix(leaf, "S_") {
			sentinels++
		}
		switch {
		case leaf == messagesFixturePNG:
			// Gated, and asserted below.
		case messagesVocabulary[leaf] != "":
		case strings.Contains(haystack, leaf):
		default:
			missing = append(missing, leaf)
		}
	}
	// Object keys from the allowlisted structure itself (role, content, type,
	// ...) are not client text and do not reach the haystack. stringLeaves
	// returns keys too, so drop the ones the decoder's own allowlists name.
	missing = withoutStructuralKeys(missing)
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("these strings are forwarded to the provider and reach no filter:\n  %s",
			strings.Join(missing, "\n  "))
	}
	if sentinels < 25 {
		t.Errorf("the fixture plants only %d sentinels; it has lost coverage", sentinels)
	}

	if s := req.Projection().UnscannedParts(); s.Parts != 2 {
		t.Errorf("UnscannedParts = %+v, want both images, including the one inside a tool result", s)
	}
}

// withoutStructuralKeys removes object keys that belong to the request's own
// allowlisted structure. A key under a client-defined object (a schema
// property, a tool input key) is client text and is not in this set; those
// sentinels are all prefixed S_ and must have been found.
func withoutStructuralKeys(in []string) []string {
	structural := map[string]bool{}
	for _, set := range []map[string]bool{messagesTopFields, toolDefFields, toolChoiceFields,
		cacheControlFields, imageSourceFields, {"role": true, "content": true, "user_id": true}} {
		for k := range set {
			structural[k] = true
		}
	}
	for _, set := range blockFields {
		for k := range set {
			structural[k] = true
		}
	}
	var out []string
	for _, s := range in {
		if !structural[s] {
			out = append(out, s)
		}
	}
	return out
}
