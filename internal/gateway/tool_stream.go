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
	"sort"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// toolCallAccumulator reconstructs whole tool calls from streaming deltas.
//
// A provider does not send a tool call in one piece. It sends a first delta
// carrying the index, the id, the type and the function name, then further
// deltas that carry only the index and the next fragment of the arguments
// string. The index is the join key: without accumulating on it, a client sees
// a call whose name arrived in one chunk and whose arguments arrived in ten
// others, and parallel calls interleave beyond reconstruction.
//
// The gateway relays chunks byte for byte, so the client does its own
// accumulation and this type does not stand between them. AEGIS accumulates in
// parallel for its own record: the streaming completion log line and the policy
// input need to know which tools were called, and on a streamed response that
// fact exists only in the deltas.
//
// Arguments are accumulated because a name cannot be read without consuming the
// deltas that carry it. They are used for nothing else: nothing here reaches a
// log line, a metric or an audit row. See ToolNames, which is the only accessor
// that leaves this type, and which returns names alone.
type toolCallAccumulator struct {
	byIndex map[int]*accumulatedCall
	// order preserves first-seen index order so two streams that produced the
	// same calls produce the same name list.
	order []int
}

type accumulatedCall struct {
	id        string
	name      string
	arguments string
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{byIndex: make(map[int]*accumulatedCall)}
}

// streamChunkToolCalls is the delta shape a tool call arrives in. Index is a
// pointer so that a delta genuinely carrying index 0 is distinguishable from
// one carrying no index at all: the first is the common case for a single tool
// call, and treating a missing index as 0 would merge unrelated calls.
type streamChunkToolCalls struct {
	Choices []struct {
		Delta struct {
			ToolCalls []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
}

// Observe folds one relayed SSE chunk into the accumulator. A chunk that is not
// JSON, or that carries no tool call delta, is ignored: this runs on every
// chunk of every stream and must never be able to fail the relay.
func (a *toolCallAccumulator) Observe(chunk []byte) {
	var parsed streamChunkToolCalls
	if err := json.Unmarshal(chunk, &parsed); err != nil {
		return
	}
	for _, choice := range parsed.Choices {
		for _, tc := range choice.Delta.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			call, ok := a.byIndex[idx]
			if !ok {
				call = &accumulatedCall{}
				a.byIndex[idx] = call
				a.order = append(a.order, idx)
			}
			if tc.ID != "" {
				call.id = tc.ID
			}
			if tc.Function.Name != "" {
				call.name += tc.Function.Name
			}
			call.arguments += tc.Function.Arguments
		}
	}
}

// Count reports how many distinct tool calls the stream carried.
func (a *toolCallAccumulator) Count() int { return len(a.byIndex) }

// ToolNames returns the reconstructed tool names, in index order.
//
// Names only. This is the whole external surface of the accumulator, and it is
// deliberately narrow: a tool name is metadata that says which capability was
// exercised, and the arguments that say what was done with it stay inside.
func (a *toolCallAccumulator) ToolNames() []string {
	if len(a.byIndex) == 0 {
		return nil
	}
	idxs := append([]int(nil), a.order...)
	sort.Ints(idxs)
	names := make([]string, 0, len(idxs))
	for _, i := range idxs {
		if n := a.byIndex[i].name; n != "" {
			names = append(names, n)
		}
	}
	return names
}

// Calls returns the reconstructed calls in index order.
//
// It exists so a test can assert that a call split across many deltas
// reassembles exactly, which is the property a streaming client depends on. No
// production path calls it.
func (a *toolCallAccumulator) Calls() []types.ToolCall {
	idxs := append([]int(nil), a.order...)
	sort.Ints(idxs)
	out := make([]types.ToolCall, 0, len(idxs))
	for _, i := range idxs {
		c := a.byIndex[i]
		idx := i
		out = append(out, types.ToolCall{
			Index:    &idx,
			ID:       c.id,
			Type:     types.ToolTypeFunction,
			Function: types.FunctionCallSpec{Name: c.name, Arguments: c.arguments},
		})
	}
	return out
}

// countReturnedToolCalls counts the tool calls a completed response asked for.
//
// Distinct from the count of tools already called in the request history: one
// says what the conversation had done before it arrived, the other says what
// the model wants done next. Logging both under one name made a streamed
// request and a non-streamed one report different things under the same key.
func countReturnedToolCalls(resp *types.AegisResponse) int {
	if resp == nil {
		return 0
	}
	n := 0
	for _, c := range resp.Choices {
		n += len(c.Message.ToolCalls)
	}
	return n
}
