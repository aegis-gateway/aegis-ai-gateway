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
	"encoding/json"
	"fmt"
)

// StreamTransformer converts one provider stream into canonical OpenAI chunks.
//
// It exists because TransformStreamChunk is stateless and one Anthropic stream
// cannot be translated a chunk at a time. Anthropic numbers every content block
// in one sequence; OpenAI numbers tool calls in their own. Producing the right
// OpenAI ordinal for a delta means remembering which blocks earlier in *this*
// stream were tool calls, and an adapter is shared across every concurrent
// request, so that memory cannot live on the adapter.
type StreamTransformer interface {
	// Transform converts one SSE data payload. A nil result means the chunk
	// carries nothing the client needs.
	Transform(chunk []byte) ([]byte, error)
}

// StreamTransformerFactory is implemented by adapters whose stream translation
// needs per-stream state. The streaming handler asks for one transformer per
// request; adapters that do not implement it are relayed chunk by chunk through
// TransformStreamChunk as before.
type StreamTransformerFactory interface {
	NewStreamTransformer() StreamTransformer
}

// statelessStreamTransformer adapts the existing per-chunk method so the
// streaming handler has one code path.
type statelessStreamTransformer struct{ adapter ProviderAdapter }

func (s statelessStreamTransformer) Transform(chunk []byte) ([]byte, error) {
	return s.adapter.TransformStreamChunk(chunk)
}

// NewStreamTransformerFor returns the right transformer for an adapter.
func NewStreamTransformerFor(a ProviderAdapter) StreamTransformer {
	if f, ok := a.(StreamTransformerFactory); ok {
		return f.NewStreamTransformer()
	}
	return statelessStreamTransformer{adapter: a}
}

// NewStreamTransformer gives each Anthropic stream its own translation state.
func (a *AnthropicAdapter) NewStreamTransformer() StreamTransformer {
	return &anthropicStreamTransformer{toolOrdinal: map[int]int{}}
}

// anthropicStreamTransformer translates one Anthropic SSE stream.
//
// The load-bearing part is toolOrdinal. Anthropic's content_block index counts
// every block, so a response that says a sentence before calling a tool puts
// that call at index 1. An OpenAI client accumulating tool_calls by index would
// then see a call at ordinal 1 with nothing at 0, and reconstruct a gap. The map
// records, per Anthropic block index, which OpenAI tool_calls ordinal it is.
//
// Verified against the live API: with two tool calls and no prose the two index
// spaces agree by coincidence, and one sentence of prose is enough to separate
// them. See docs/evidence/anthropic-tool-mapping.md.
type anthropicStreamTransformer struct {
	// toolOrdinal maps an Anthropic content block index to its position among
	// the tool calls of this response.
	toolOrdinal map[int]int
	// nextOrdinal is the ordinal the next tool_use block will take.
	nextOrdinal int
}

// anthropicStreamEvent is the subset of the event shape this translation reads.
type anthropicStreamEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Text  string          `json:"text"`
		Input json.RawMessage `json:"input"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
}

func (t *anthropicStreamTransformer) Transform(chunk []byte) ([]byte, error) {
	var ev anthropicStreamEvent
	if err := json.Unmarshal(chunk, &ev); err != nil {
		// An unparseable chunk is skipped rather than failing the stream. The
		// relay must not die because a provider sent something unmodelled.
		return nil, nil
	}

	switch ev.Type {
	case "content_block_start":
		if ev.ContentBlock.Type != "tool_use" {
			// A text block opening carries no delta a client needs.
			return nil, nil
		}
		ordinal := t.nextOrdinal
		t.nextOrdinal++
		t.toolOrdinal[ev.Index] = ordinal

		// The opening delta carries the id, the type and the name, with the
		// arguments empty. That is the shape an OpenAI client expects first.
		return marshalToolCallChunk(ordinal, ev.ContentBlock.ID, ev.ContentBlock.Name, "")

	case "content_block_delta":
		switch ev.Delta.Type {
		case "text_delta":
			return json.Marshal(openAIStreamChunk{Choices: []openAIStreamChoice{{
				Index: 0, Delta: openAIDelta{Content: ev.Delta.Text},
			}}})
		case "input_json_delta":
			ordinal, ok := t.toolOrdinal[ev.Index]
			if !ok {
				// A delta for a block whose opening was never seen. Dropping it
				// silently would corrupt the call it belongs to, so say so and
				// skip rather than attach it to the wrong ordinal.
				return nil, fmt.Errorf("input_json_delta for unknown content block index %d", ev.Index)
			}
			if ev.Delta.PartialJSON == "" {
				return nil, nil
			}
			return marshalToolCallArgsChunk(ordinal, ev.Delta.PartialJSON)
		}
		return nil, nil

	case "message_delta":
		finish := mapStopReason(ev.Delta.StopReason)
		return json.Marshal(openAIStreamChunk{Choices: []openAIStreamChoice{{
			Index: 0, Delta: openAIDelta{}, FinishReason: &finish,
		}}})

	case "message_stop":
		return []byte("[DONE]"), nil

	default:
		// message_start, content_block_stop, ping
		return nil, nil
	}
}

// openAIToolCallDelta is a tool call fragment in OpenAI streaming shape. Index
// is a pointer so an omitted index is distinguishable from index 0, which is
// the distinction the gateway's own accumulator depends on.
type openAIToolCallDelta struct {
	Index    *int                        `json:"index"`
	ID       string                      `json:"id,omitempty"`
	Type     string                      `json:"type,omitempty"`
	Function openAIToolCallFunctionDelta `json:"function"`
}

type openAIToolCallFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type openAIToolDelta struct {
	Role      string                `json:"role,omitempty"`
	Content   string                `json:"content,omitempty"`
	ToolCalls []openAIToolCallDelta `json:"tool_calls,omitempty"`
}

type openAIToolStreamChoice struct {
	Index        int             `json:"index"`
	Delta        openAIToolDelta `json:"delta"`
	FinishReason *string         `json:"finish_reason"`
}

type openAIToolStreamChunk struct {
	Choices []openAIToolStreamChoice `json:"choices"`
}

func marshalToolCallChunk(ordinal int, id, name, args string) ([]byte, error) {
	idx := ordinal
	return json.Marshal(openAIToolStreamChunk{Choices: []openAIToolStreamChoice{{
		Index: 0,
		Delta: openAIToolDelta{ToolCalls: []openAIToolCallDelta{{
			Index:    &idx,
			ID:       id,
			Type:     "function",
			Function: openAIToolCallFunctionDelta{Name: name, Arguments: args},
		}}},
	}}})
}

func marshalToolCallArgsChunk(ordinal int, partial string) ([]byte, error) {
	idx := ordinal
	return json.Marshal(openAIToolStreamChunk{Choices: []openAIToolStreamChoice{{
		Index: 0,
		Delta: openAIToolDelta{ToolCalls: []openAIToolCallDelta{{
			Index:    &idx,
			Function: openAIToolCallFunctionDelta{Arguments: partial},
		}}},
	}}})
}
