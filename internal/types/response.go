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

type AegisResponse struct {
	RequestID        string        `json:"request_id"`
	Model            string        `json:"model"`
	Provider         string        `json:"provider"`
	Choices          []Choice      `json:"choices"`
	Usage            Usage         `json:"usage"`
	EstimatedCostUSD float64       `json:"estimated_cost_usd"`
	FilterActions    FilterSummary `json:"filter_actions"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	// PromptTokensDetails carries the cached subset of PromptTokens, in the
	// shape an OpenAI client already expects.
	//
	// Cached input is billed at a different rate: configs/pricing.yaml sets
	// cached_input an order of magnitude below input on several models, and
	// cost.Calculator has always known how to apply it. Nothing populated the
	// count, so every cache read was priced at the full input rate.
	PromptTokensDetails *PromptTokensDetails `json:"prompt_tokens_details,omitempty"`
}

// PromptTokensDetails breaks down PromptTokens.
//
// CachedTokens is a SUBSET of PromptTokens, which is OpenAI's convention and
// the one cost.Calculator documents. Anthropic reports the opposite: its
// input_tokens excludes the cached portion and reports it alongside. Verified
// against the live API: a cached call returned input_tokens 8 with
// cache_read_input_tokens 4411 for the same 4419-token prompt. The Anthropic
// adapter normalises to this convention, so a caller and the calculator see one
// meaning rather than two.
type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`

	// Cache writes, split by entry lifetime because they are priced
	// differently: a 5-minute write is 1.25x base input and a 1-hour write 2x.
	// Both are subsets of PromptTokens and disjoint from CachedTokens.
	//
	// These have no OpenAI counterpart, which does not charge for cache writes,
	// so they are omitted when zero and an OpenAI-shaped client never sees them.
	CacheWrite5mTokens int `json:"cache_write_5m_tokens,omitempty"`
	CacheWrite1hTokens int `json:"cache_write_1h_tokens,omitempty"`
}

// CachedPromptTokens returns the cached subset, or zero when the provider
// reported none.
func (u Usage) CachedPromptTokens() int {
	if u.PromptTokensDetails == nil {
		return 0
	}
	return u.PromptTokensDetails.CachedTokens
}

// CacheWrite5mTokens returns the tokens written to a five-minute cache entry.
func (u Usage) CacheWrite5mTokens() int {
	if u.PromptTokensDetails == nil {
		return 0
	}
	return u.PromptTokensDetails.CacheWrite5mTokens
}

// CacheWrite1hTokens returns the tokens written to a one-hour cache entry.
func (u Usage) CacheWrite1hTokens() int {
	if u.PromptTokensDetails == nil {
		return 0
	}
	return u.PromptTokensDetails.CacheWrite1hTokens
}

type FilterSummary struct {
	PIIInbound  FilterAction `json:"pii_inbound"`
	PIIOutbound FilterAction `json:"pii_outbound"`
	Secrets     FilterAction `json:"secrets"`
	Injection   FilterAction `json:"injection"`
	Policy      FilterAction `json:"policy"`
}

type FilterAction struct {
	Action     string  `json:"action"`
	Detections int     `json:"detections,omitempty"`
	Score      float64 `json:"score,omitempty"`
}
