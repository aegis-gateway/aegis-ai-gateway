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
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/cost"
)

// The two providers disagree about what a prompt token count means, and the
// disagreement is silent: both report plausible integers, and using one
// convention where the other applies produces a wrong number rather than an
// error.
//
// Anthropic's input_tokens EXCLUDES anything served from or written to the
// cache. OpenAI's prompt_tokens INCLUDES the cached portion and reports it as a
// subset. The canonical shape follows OpenAI, because that is the convention
// cost.Calculator documents for RequestDetails.CachedTokens.
//
// Probed against the live API on 2026-08-29. Two identical calls with a
// cache_control marker:
//
//	call 1:  input_tokens 8,  cache_creation 4411,  cache_read 0
//	call 2:  input_tokens 8,  cache_creation 0,     cache_read 4411
//
// The prompt was 4419 tokens both times.

// TestAnthropicUsage_ReassemblesTheTotal pins the arithmetic.
//
// Carrying input_tokens through as the prompt count would have understated that
// request by 4411 tokens, and handing the same figure to the calculator as a
// subset would have made uncached input negative.
func TestAnthropicUsage_ReassemblesTheTotal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                            string
		in                              anthropicUsage
		wantPrompt, wantCached, wantTot int
	}{
		{
			"a cache read, as observed live",
			anthropicUsage{InputTokens: 8, CacheReadInputTokens: 4411, OutputTokens: 4},
			4419, 4411, 4423,
		},
		{
			"a cache write, as observed live",
			anthropicUsage{InputTokens: 8, CacheCreationInputTokens: 4411, OutputTokens: 4},
			// A written entry is charged, and is not a read, so it counts
			// toward the prompt but not toward the cached subset.
			4419, 0, 4423,
		},
		{
			"no caching, which is every request today",
			anthropicUsage{InputTokens: 4413, OutputTokens: 18},
			4413, 0, 4431,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := anthropicUsageToCanonical(tc.in)
			if got.PromptTokens != tc.wantPrompt {
				t.Errorf("prompt = %d, want %d. Anthropic reports the cached portion outside "+
					"input_tokens, so the total has to be reassembled", got.PromptTokens, tc.wantPrompt)
			}
			if got.CachedPromptTokens() != tc.wantCached {
				t.Errorf("cached = %d, want %d", got.CachedPromptTokens(), tc.wantCached)
			}
			if got.TotalTokens != tc.wantTot {
				t.Errorf("total = %d, want %d", got.TotalTokens, tc.wantTot)
			}
			// The invariant the calculator depends on.
			if got.CachedPromptTokens() > got.PromptTokens {
				t.Errorf("cached (%d) exceeds prompt (%d); the calculator computes uncached as "+
					"the difference and would go negative", got.CachedPromptTokens(), got.PromptTokens)
			}
		})
	}
}

// TestAnthropicStream_ReportsCachedTokens covers the same normalisation on the
// streaming path, which reassembles usage from two separate events.
func TestAnthropicStream_ReportsCachedTokens(t *testing.T) {
	t.Parallel()

	a := NewAnthropicAdapter(config.ProviderConfig{}, nil)
	tr := a.NewStreamTransformer()

	for _, ev := range []string{
		`{"type":"message_start","message":{"model":"claude-haiku-4-5","usage":{"input_tokens":8,"cache_read_input_tokens":4411,"output_tokens":1}}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":8,"cache_read_input_tokens":4411,"output_tokens":4}}`,
	} {
		out, err := tr.Transform([]byte(ev))
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		if out == nil {
			continue
		}
		var chunk struct {
			Usage *struct {
				PromptTokens        int `json:"prompt_tokens"`
				PromptTokensDetails *struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(out, &chunk); err != nil || chunk.Usage == nil {
			continue
		}
		if chunk.Usage.PromptTokens != 4419 {
			t.Errorf("streamed prompt = %d, want 4419 (8 uncached plus 4411 cached)", chunk.Usage.PromptTokens)
		}
		if chunk.Usage.PromptTokensDetails == nil || chunk.Usage.PromptTokensDetails.CachedTokens != 4411 {
			t.Errorf("streamed response reported no cached subset, so the cache read is priced "+
				"at the full input rate: %s", out)
		}
		return
	}
	t.Fatal("no chunk carried usage")
}

// TestCachedTokensChangeTheCost is the point of all of it.
//
// Without the subset, a cache read is billed at the full input rate. Several
// models set cached_input an order of magnitude below input, so the error is
// large and in the direction that erodes trust: AEGIS overstates spend, and a
// reconciliation against the provider's own bill does not match.
func TestCachedTokensChangeTheCost(t *testing.T) {
	t.Parallel()

	pricing := &config.PricingConfig{Providers: map[string]config.ProviderPricing{
		"anthropic": {Models: map[string]config.PriceEntry{
			"m": {Input: 5.00, CachedInput: 0.50, Output: 25.00},
		}},
	}}
	calc := cost.NewCalculator(func() *config.PricingConfig { return pricing })

	full, ok := calc.Calculate(cost.RequestDetails{
		Provider: "anthropic", Model: "m", PromptTokens: 1_000_000, CompletionTokens: 0,
	})
	if !ok {
		t.Fatal("no pricing found")
	}
	cached, ok := calc.Calculate(cost.RequestDetails{
		Provider: "anthropic", Model: "m", PromptTokens: 1_000_000, CachedTokens: 1_000_000, CompletionTokens: 0,
	})
	if !ok {
		t.Fatal("no pricing found")
	}

	if full != 5.00 {
		t.Errorf("uncached million tokens = %v, want 5.00", full)
	}
	if cached != 0.50 {
		t.Errorf("fully cached million tokens = %v, want 0.50", cached)
	}
	if full == cached {
		t.Fatal("cached and uncached price identically, so the cached_input rate in " +
			"configs/pricing.yaml is configured and unused")
	}
	t.Logf("a fully cached million-token prompt: %v uncached vs %v cached, a %.0fx difference",
		full, cached, full/cached)
}
