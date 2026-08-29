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

	// Token counts, carried so the final chunk can report them.
	//
	// Anthropic reports usage natively: input_tokens on message_start, and both
	// counts on message_delta. The gateway reads usage from a relayed chunk in
	// the OpenAI shape, so an Anthropic stream that never emits one records
	// zero tokens and therefore zero cost. That is not a reporting nicety: the
	// daily spend budget is computed from these records, so a streamed request
	// costing real money moved no budget at all.
	inputTokens  int
	cachedTokens int
	outputTokens int

	// model is the model the provider actually served, taken from
	// message_start. The gateway reads it off a relayed chunk and uses it to
	// look up pricing, so a stream that never carries one is priced at zero
	// even when the token counts are right. Emitting usage without the model
	// fixed half the problem and left the cost at zero.
	model string
}

// anthropicUsage is the usage block, which appears in two different places.
type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// Cache counts are read but not yet used. CalculateSimple, which the
	// streaming path calls, has no cached-token parameter; cost.Calculator
	// does. Wiring that through would price cache reads correctly and is
	// deliberately not done here.
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
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

	// message_start nests usage under message; message_delta carries it at the
	// top level.
	Message struct {
		Model string         `json:"model"`
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
	Usage anthropicUsage `json:"usage"`
}

func (t *anthropicStreamTransformer) Transform(chunk []byte) ([]byte, error) {
	var ev anthropicStreamEvent
	if err := json.Unmarshal(chunk, &ev); err != nil {
		// An unparseable chunk is skipped rather than failing the stream. The
		// relay must not die because a provider sent something unmodelled.
		return nil, nil
	}

	switch ev.Type {
	case "message_start":
		// Carries the served model and the input token count. Both are needed
		// downstream: the counts to record spend, the model to price it.
		if ev.Message.Model != "" {
			t.model = ev.Message.Model
		}
		t.absorbUsage(ev.Message.Usage)
		return nil, nil

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
			return json.Marshal(openAIStreamChunkWithUsage{
				Model: t.model,
				Choices: []openAIStreamChoice{{
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
		// The final event: stop reason plus the settled token counts. Both are
		// carried on one chunk, in the OpenAI shape, because that is what the
		// gateway's usage extraction and every OpenAI client read.
		t.absorbUsage(ev.Usage)
		finish := mapStopReason(ev.Delta.StopReason)
		chunk := openAIStreamChunkWithUsage{
			Model:   t.model,
			Choices: []openAIStreamChoice{{Index: 0, Delta: openAIDelta{}, FinishReason: &finish}},
		}
		if t.inputTokens > 0 || t.outputTokens > 0 {
			chunk.Usage = &openAIUsage{
				PromptTokens:     t.inputTokens,
				CompletionTokens: t.outputTokens,
				TotalTokens:      t.inputTokens + t.outputTokens,
			}
			if t.cachedTokens > 0 {
				chunk.Usage.PromptTokensDetails = &openAIPromptTokensDetails{CachedTokens: t.cachedTokens}
			}
		}
		return json.Marshal(chunk)

	case "message_stop":
		return []byte("[DONE]"), nil

	default:
		// content_block_stop, ping
		return nil, nil
	}
}

// openAIStreamChunkWithUsage is the final chunk shape. Usage is a pointer so it
// is absent rather than zero when the provider reported nothing, which keeps a
// missing count distinguishable from a genuine zero.
type openAIStreamChunkWithUsage struct {
	Model   string               `json:"model,omitempty"`
	Choices []openAIStreamChoice `json:"choices"`
	Usage   *openAIUsage         `json:"usage,omitempty"`
}

type openAIUsage struct {
	PromptTokens        int                        `json:"prompt_tokens"`
	CompletionTokens    int                        `json:"completion_tokens"`
	TotalTokens         int                        `json:"total_tokens"`
	PromptTokensDetails *openAIPromptTokensDetails `json:"prompt_tokens_details,omitempty"`
}

type openAIPromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// absorbUsage folds an Anthropic usage block into the running totals,
// normalising to the canonical convention where the prompt count INCLUDES the
// cached portion. Anthropic's input_tokens excludes it. See
// anthropicUsageToCanonical.
func (t *anthropicStreamTransformer) absorbUsage(u anthropicUsage) {
	if total := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens; total > 0 {
		t.inputTokens = total
	}
	if u.CacheReadInputTokens > 0 {
		t.cachedTokens = u.CacheReadInputTokens
	}
	if u.OutputTokens > 0 {
		t.outputTokens = u.OutputTokens
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
