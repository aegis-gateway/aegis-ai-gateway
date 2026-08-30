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
	// Each component of the prompt total is tracked separately, because
	// Anthropic's usage arrives across several events and any given event may
	// carry only some of the fields. Reconstructing the total from whichever
	// fields the *current* event happens to hold, and overwriting with that,
	// loses the components the event omitted.
	uncachedInputTokens int
	cachedTokens        int
	cacheCreationTokens int
	// Cache writes are tracked per TTL tier, because they are priced
	// differently: 1.25x base input for the 5-minute tier and 2x for the
	// 1-hour. Keeping only the aggregate, as this transformer did, left the
	// calculator no way to tell them apart and every streamed cache-warming
	// request was billed at plain 1x input.
	cacheWrite5mTokens int
	cacheWrite1hTokens int
	outputTokens       int

	// usageSeen records that the provider sent a usage object at all, which is
	// a different fact from the counts in it being non-zero. An all-zero usage
	// block is a measurement; no usage block is not.
	usageSeen bool
	// usageInvalid records that some usage object carried a negative counter.
	// Sticky for the life of the stream: once a provider has reported nonsense
	// the reconstructed total is not trustworthy, and a later well-formed event
	// does not repair the components the bad one was meant to supply.
	usageInvalid bool

	// model is the model the provider actually served, taken from
	// message_start. The gateway reads it off a relayed chunk and uses it to
	// look up pricing, so a stream that never carries one is priced at zero
	// even when the token counts are right. Emitting usage without the model
	// fixed half the problem and left the cost at zero.
	model string
}

// anthropicUsage is the usage block, which appears in two different places.
// anthropicCacheCreation is the per-TTL breakdown of cache writes. Named rather
// than anonymous so the streaming tests can construct one.
type anthropicCacheCreation struct {
	Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// Cache counts are read but not yet used. CalculateSimple, which the
	// streaming path calls, has no cached-token parameter; cost.Calculator
	// does. Wiring that through would price cache reads correctly and is
	// deliberately not done here.
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`

	// The API splits cache creation by entry lifetime, and the two are priced
	// differently: a 5-minute write is 1.25x base input, a 1-hour write 2x.
	// cache_creation_input_tokens is their sum.
	CacheCreation anthropicCacheCreation `json:"cache_creation"`
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
		Model string `json:"model"`
		// Pointers so that an event carrying a usage object of zeros is
		// distinguishable from one carrying no usage object. audit_events seals
		// the counts and must not record a reported zero as an absence.
		Usage *anthropicUsage `json:"usage"`
	} `json:"message"`
	Usage *anthropicUsage `json:"usage"`
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
		if ev.Message.Usage != nil {
			t.absorbUsage(*ev.Message.Usage)
		}
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
		if ev.Usage != nil {
			t.absorbUsage(*ev.Usage)
		}
		finish := mapStopReason(ev.Delta.StopReason)
		chunk := openAIStreamChunkWithUsage{
			Model:   t.model,
			Choices: []openAIStreamChoice{{Index: 0, Delta: openAIDelta{}, FinishReason: &finish}},
		}
		// Gated on a usage object having been seen, not on a count being
		// positive. A message_delta reporting all zeros is a measurement, and
		// dropping it here made metrics.UsageReported false downstream, which
		// sealed three NULLs meaning "the provider reported nothing".
		if t.usageSeen && !t.usageInvalid {
			chunk.Usage = &openAIUsage{
				PromptTokens:     t.promptTokens(),
				CompletionTokens: t.outputTokens,
				TotalTokens:      t.promptTokens() + t.outputTokens,
			}
			write5m, write1h := t.cacheWriteTiers()
			if t.cachedTokens > 0 || write5m > 0 || write1h > 0 {
				chunk.Usage.PromptTokensDetails = &openAIPromptTokensDetails{
					CachedTokens:       t.cachedTokens,
					CacheWrite5mTokens: write5m,
					CacheWrite1hTokens: write1h,
				}
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
	CachedTokens       int `json:"cached_tokens"`
	CacheWrite5mTokens int `json:"cache_write_5m_tokens,omitempty"`
	CacheWrite1hTokens int `json:"cache_write_1h_tokens,omitempty"`
}

// absorbUsage folds an Anthropic usage block into the running totals,
// normalising to the canonical convention where the prompt count INCLUDES the
// cached portion. Anthropic's input_tokens excludes it. See
// anthropicUsageToCanonical.
// absorbUsage folds one usage object into the running totals.
//
// message_start carries the full prompt breakdown; message_delta carries the
// output count and, per Anthropic's schema, may repeat input_tokens while
// omitting the cache fields entirely. Rebuilding the prompt total from the
// current event and overwriting therefore replaced a correct total such as
// 1005 with the uncached 5 — and since cachedTokens survived, the calculator
// then computed uncached = 5 - 1000, clamped it to zero, and billed none of
// the uncached input.
//
// Updating each component independently means an event that omits a field
// leaves that component alone rather than erasing it.
func (t *anthropicStreamTransformer) absorbUsage(u anthropicUsage) {
	t.usageSeen = true

	// A negative counter is not a measurement, and it has to be caught HERE.
	// The assignments below take a value only when it is positive, so a
	// negative one is silently discarded and leaves the component at zero;
	// downstream the block would then look like a valid all-zero measurement
	// and the gateway's negative-count guard would never see it. Recording the
	// whole block as invalid keeps a malformed usage object out of the sealed
	// row rather than laundering it into an explicit zero.
	if anthropicUsageHasNegative(u) {
		t.usageInvalid = true
	}

	if u.InputTokens > 0 {
		t.uncachedInputTokens = u.InputTokens
	}
	if u.CacheReadInputTokens > 0 {
		t.cachedTokens = u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens > 0 {
		t.cacheCreationTokens = u.CacheCreationInputTokens
	}
	if u.CacheCreation.Ephemeral5m > 0 {
		t.cacheWrite5mTokens = u.CacheCreation.Ephemeral5m
	}
	if u.CacheCreation.Ephemeral1h > 0 {
		t.cacheWrite1hTokens = u.CacheCreation.Ephemeral1h
	}
	if u.OutputTokens > 0 {
		t.outputTokens = u.OutputTokens
	}
}

// cacheWriteTiers returns the per-TTL cache-write counts, applying the same
// fallback as the non-streaming path in anthropicUsageToCanonical: a response
// that reports only the aggregate is attributed to the 5-minute tier, the
// default TTL and the cheaper of the two, so an unattributable write is never
// over-charged.
func (t *anthropicStreamTransformer) cacheWriteTiers() (write5m, write1h int) {
	write5m, write1h = t.cacheWrite5mTokens, t.cacheWrite1hTokens
	if write5m == 0 && write1h == 0 && t.cacheCreationTokens > 0 {
		write5m = t.cacheCreationTokens
	}
	return write5m, write1h
}

// promptTokens is the total prompt size: uncached input plus everything that
// came from or went into the cache.
func (t *anthropicStreamTransformer) promptTokens() int {
	return t.uncachedInputTokens + t.cachedTokens + t.cacheCreationTokens
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
