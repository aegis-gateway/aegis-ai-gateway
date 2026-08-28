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

package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestToolCallAccumulator_ReassemblesSplitArguments feeds the accumulator the
// delta sequence a provider actually emits: one chunk carrying the index, id,
// type and name, then many carrying only the index and one fragment of the
// arguments.
//
// This is the property a streaming client depends on. If the gateway relayed
// chunks in a way that lost the index, or reordered them, a client could not
// reconstruct the call at all.
func TestToolCallAccumulator_ReassemblesSplitArguments(t *testing.T) {
	t.Parallel()

	const wantArgs = `{"path":"src/main.go","limit":100}`

	chunks := []string{
		`{"choices":[{"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
	}
	for _, frag := range splitForTest(wantArgs, 5) {
		chunk, err := json.Marshal(map[string]any{
			"choices": []map[string]any{{
				"delta": map[string]any{
					"tool_calls": []map[string]any{{
						"index":    0,
						"function": map[string]string{"arguments": frag},
					}},
				},
			}},
		})
		if err != nil {
			t.Fatalf("building chunk: %v", err)
		}
		chunks = append(chunks, string(chunk))
	}

	acc := newToolCallAccumulator()
	for _, c := range chunks {
		acc.Observe([]byte(c))
	}

	calls := acc.Calls()
	if len(calls) != 1 {
		t.Fatalf("reconstructed %d calls, want 1", len(calls))
	}
	if calls[0].ID != "call_abc" {
		t.Errorf("id = %q, want call_abc", calls[0].ID)
	}
	if calls[0].Function.Name != "read_file" {
		t.Errorf("name = %q, want read_file", calls[0].Function.Name)
	}
	if calls[0].Function.Arguments != wantArgs {
		t.Errorf("arguments = %q, want %q — the fragments did not reassemble", calls[0].Function.Arguments, wantArgs)
	}
	if !json.Valid([]byte(calls[0].Function.Arguments)) {
		t.Error("reassembled arguments are not valid JSON")
	}
}

// TestToolCallAccumulator_ParallelCallsStaySeparate is why the index exists.
//
// Two tools called in the same turn interleave their argument fragments in the
// stream. Accumulating without the index concatenates one call's arguments onto
// the other's, producing two calls that are each unparseable.
func TestToolCallAccumulator_ParallelCallsStaySeparate(t *testing.T) {
	t.Parallel()

	acc := newToolCallAccumulator()
	for _, c := range []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c1","type":"function","function":{"name":"list_dir","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"dir\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\".\"}"}}]}}]}`,
	} {
		acc.Observe([]byte(c))
	}

	calls := acc.Calls()
	if len(calls) != 2 {
		t.Fatalf("reconstructed %d calls, want 2", len(calls))
	}
	if calls[0].Function.Name != "read_file" || calls[0].Function.Arguments != `{"path":"a.go"}` {
		t.Errorf("call 0 = %s(%s), want read_file({\"path\":\"a.go\"})",
			calls[0].Function.Name, calls[0].Function.Arguments)
	}
	if calls[1].Function.Name != "list_dir" || calls[1].Function.Arguments != `{"dir":"."}` {
		t.Errorf("call 1 = %s(%s), want list_dir({\"dir\":\".\"})",
			calls[1].Function.Name, calls[1].Function.Arguments)
	}
}

// TestToolCallAccumulator_MissingIndexDoesNotMerge covers the pointer on Index.
//
// A delta genuinely carrying index 0 and a delta carrying no index at all are
// different things. Treating both as 0 is the obvious implementation and it
// merges unrelated calls.
func TestToolCallAccumulator_MissingIndexDoesNotMerge(t *testing.T) {
	t.Parallel()

	acc := newToolCallAccumulator()
	acc.Observe([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"alpha","arguments":"{}"}}]}}]}`))
	acc.Observe([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c1","function":{"name":"beta","arguments":"{}"}}]}}]}`))

	if got := acc.Count(); got != 2 {
		t.Fatalf("Count() = %d, want 2", got)
	}
	names := strings.Join(acc.ToolNames(), ",")
	if names != "alpha,beta" {
		t.Errorf("ToolNames() = %q, want alpha,beta in index order", names)
	}
}

// TestToolCallAccumulator_IgnoresNonToolChunks asserts the accumulator cannot
// fail the relay. It runs on every chunk of every stream, including plain text
// deltas, keep-alives and anything a provider sends that is not JSON.
func TestToolCallAccumulator_IgnoresNonToolChunks(t *testing.T) {
	t.Parallel()

	acc := newToolCallAccumulator()
	for _, c := range []string{
		`{"choices":[{"delta":{"content":"hello"}}]}`,
		`{"choices":[]}`,
		`{}`,
		`not json at all`,
		``,
	} {
		acc.Observe([]byte(c))
	}
	if acc.Count() != 0 {
		t.Errorf("Count() = %d after chunks carrying no tool call, want 0", acc.Count())
	}
	if acc.ToolNames() != nil {
		t.Errorf("ToolNames() = %v, want nil", acc.ToolNames())
	}
}

// TestToolCallAccumulator_ExposesNamesNotArguments guards the no-payload
// boundary on the one accessor that leaves the accumulator for a log line.
func TestToolCallAccumulator_ExposesNamesNotArguments(t *testing.T) {
	t.Parallel()

	const payload = "SECRET_ARGUMENT_VALUE"
	acc := newToolCallAccumulator()
	acc.Observe([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"read_file","arguments":"{\"p\":\"` + payload + `\"}"}}]}}]}`))

	for _, name := range acc.ToolNames() {
		if strings.Contains(name, payload) {
			t.Errorf("ToolNames() leaked argument payload: %q", name)
		}
	}
	if len(acc.ToolNames()) != 1 || acc.ToolNames()[0] != "read_file" {
		t.Errorf("ToolNames() = %v, want [read_file]", acc.ToolNames())
	}
}

// splitForTest cuts s into fragments of at most n bytes.
func splitForTest(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// TestToolCallAccumulator_MissingIndexDoesNotMergeIntoFirstCall covers the case
// the Index pointer exists for. Defaulting an absent index to 0 folded the
// delta into the first tool call, corrupting a call that was otherwise
// correct — and the existing test for this never actually omitted the field.
func TestToolCallAccumulator_MissingIndexDoesNotMergeIntoFirstCall(t *testing.T) {
	acc := newToolCallAccumulator()
	for _, chunk := range []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","type":"function","function":{"name":"read_file","arguments":"{\"p\":"}}]}}]}`,
		// No index: malformed, and must not land on call 0.
		`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"\"/etc/shadow\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
	} {
		acc.Observe([]byte(chunk))
	}

	calls := acc.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 accumulated call, got %d", len(calls))
	}
	if got := calls[0].Function.Arguments; got != `{"p":"a.go"}` {
		t.Errorf("arguments = %q, want %q — the indexless fragment was merged into the first call",
			got, `{"p":"a.go"}`)
	}
}
