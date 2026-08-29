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

package cost

import (
	"math"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
)

func makeTestPricingCfg() func() *config.PricingConfig {
	return func() *config.PricingConfig {
		return &config.PricingConfig{
			VerifiedAt: "2026-08-21",
			Providers: map[string]config.ProviderPricing{
				"anthropic": {
					Models: map[string]config.PriceEntry{
						"claude-opus-5": {
							Input: 5.00, CachedInput: 0.50, Output: 25.00,
							BatchMultiplier: 0.5,
						},
						"claude-sonnet-5": {
							Input: 2.00, CachedInput: 0.20, Output: 10.00,
							BatchMultiplier: 0.5,
						},
						"claude-haiku-4-5-20251001": {
							Input: 1.00, CachedInput: 0.10, Output: 5.00,
							BatchMultiplier: 0.5,
						},
					},
				},
				"openai": {
					Models: map[string]config.PriceEntry{
						"gpt-5.6-sol": {
							Input: 5.00, CachedInput: 0.50, Output: 30.00,
							BatchMultiplier: 0.5,
							LongContext: &config.LongContextPricing{
								ThresholdTokens: 272000,
								Input:           10.00,
								CachedInput:     1.00,
								Output:          45.00,
							},
						},
						"gpt-5.6-luna": {
							Input: 0.20, CachedInput: 0.02, Output: 1.20,
							BatchMultiplier: 0.5,
						},
					},
				},
			},
		}
	}
}

func TestCalculator_Calculate(t *testing.T) {
	calc := NewCalculator(makeTestPricingCfg())

	tests := []struct {
		name          string
		details       RequestDetails
		expectedCost  float64
		expectedFound bool
	}{
		{
			name: "standard anthropic request",
			details: RequestDetails{
				Provider: "anthropic", Model: "claude-opus-5",
				PromptTokens: 1_000_000, CompletionTokens: 1_000_000,
			},
			// 1M * $5/M + 1M * $25/M = $30
			expectedCost:  30.00,
			expectedFound: true,
		},
		{
			name: "cached tokens use cached_input rate",
			details: RequestDetails{
				Provider: "anthropic", Model: "claude-opus-5",
				PromptTokens: 1_000_000, CachedTokens: 500_000,
				CompletionTokens: 0,
			},
			// 500K uncached * $5/M + 500K cached * $0.50/M = $2.50 + $0.25 = $2.75
			expectedCost:  2.75,
			expectedFound: true,
		},
		{
			name: "batch job applies batch_multiplier",
			details: RequestDetails{
				Provider: "anthropic", Model: "claude-sonnet-5",
				PromptTokens: 1_000_000, CompletionTokens: 1_000_000,
				IsBatch: true,
			},
			// ($2 + $10) * 0.5 = $6
			expectedCost:  6.00,
			expectedFound: true,
		},
		{
			name: "openai long context tier",
			details: RequestDetails{
				Provider: "openai", Model: "gpt-5.6-sol",
				PromptTokens: 300_000, CompletionTokens: 50_000,
			},
			// total input 300K >= 272K → long_context: $10/M input, $45/M output
			// 300K * $10/M + 50K * $45/M = $3.00 + $2.25 = $5.25
			expectedCost:  5.25,
			expectedFound: true,
		},
		{
			name: "openai standard context below threshold",
			details: RequestDetails{
				Provider: "openai", Model: "gpt-5.6-sol",
				PromptTokens: 100_000, CompletionTokens: 10_000,
			},
			// 100K * $5/M + 10K * $30/M = $0.50 + $0.30 = $0.80
			expectedCost:  0.80,
			expectedFound: true,
		},
		{
			name: "zero tokens",
			details: RequestDetails{
				Provider: "anthropic", Model: "claude-haiku-4-5-20251001",
			},
			expectedCost:  0.0,
			expectedFound: true,
		},
		{
			name: "unknown provider",
			details: RequestDetails{
				Provider: "unknown", Model: "mystery-model",
				PromptTokens: 1000, CompletionTokens: 500,
			},
			expectedCost:  0.0,
			expectedFound: false,
		},
		{
			name: "unknown model for known provider",
			details: RequestDetails{
				Provider: "openai", Model: "gpt-4o",
				PromptTokens: 1000, CompletionTokens: 500,
			},
			expectedCost:  0.0,
			expectedFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost, found := calc.Calculate(tt.details)

			if found != tt.expectedFound {
				t.Errorf("Calculate() found = %v, want %v", found, tt.expectedFound)
			}

			const epsilon = 0.0001
			if diff := cost - tt.expectedCost; diff < -epsilon || diff > epsilon {
				t.Errorf("Calculate() cost = %v, want %v (diff: %v)", cost, tt.expectedCost, diff)
			}
		})
	}
}

func TestCalculator_CalculateSimple(t *testing.T) {
	calc := NewCalculator(makeTestPricingCfg())

	cost, found := calc.CalculateSimple("anthropic", "claude-haiku-4-5-20251001", 1_000_000, 1_000_000)
	if !found {
		t.Fatal("expected pricing to be found")
	}
	// $1/M input + $5/M output = $6
	const want = 6.00
	const epsilon = 0.0001
	if diff := cost - want; diff < -epsilon || diff > epsilon {
		t.Errorf("CalculateSimple() = %v, want %v", cost, want)
	}
}

func TestCalculator_CacheInvalidation(t *testing.T) {
	calc := NewCalculator(makeTestPricingCfg())

	cost1, found1 := calc.CalculateSimple("openai", "gpt-5.6-luna", 1_000_000, 1_000_000)
	if !found1 {
		t.Fatal("expected pricing to be found")
	}
	if len(calc.priceCache) != 1 {
		t.Errorf("expected 1 cache entry, got %d", len(calc.priceCache))
	}

	cost2, found2 := calc.CalculateSimple("openai", "gpt-5.6-luna", 1_000_000, 1_000_000)
	if cost1 != cost2 || found1 != found2 {
		t.Error("cached result differs from original")
	}

	calc.InvalidateCache()
	if len(calc.priceCache) != 0 {
		t.Errorf("expected empty cache after invalidation, got %d entries", len(calc.priceCache))
	}

	cost3, found3 := calc.CalculateSimple("openai", "gpt-5.6-luna", 1_000_000, 1_000_000)
	if cost1 != cost3 || found1 != found3 {
		t.Error("result after invalidation differs from original")
	}
	if len(calc.priceCache) != 1 {
		t.Errorf("expected 1 cache entry after repopulation, got %d", len(calc.priceCache))
	}
}

func TestCalculator_GetModelPrice(t *testing.T) {
	calc := NewCalculator(makeTestPricingCfg())

	price, found := calc.GetModelPrice("anthropic", "claude-opus-5")
	if !found {
		t.Fatal("expected pricing to be found")
	}
	// $5/M = $0.000005 per token
	wantInput := 5.00 / 1_000_000.0
	const epsilon = 1e-10
	if diff := price.InputPerToken - wantInput; diff < -epsilon || diff > epsilon {
		t.Errorf("InputPerToken = %v, want %v", price.InputPerToken, wantInput)
	}

	_, found = calc.GetModelPrice("openai", "does-not-exist")
	if found {
		t.Error("expected not found for unknown model")
	}
}

func BenchmarkCalculator_Calculate(b *testing.B) {
	calc := NewCalculator(makeTestPricingCfg())
	details := RequestDetails{
		Provider: "openai", Model: "gpt-5.6-sol",
		PromptTokens: 1000, CompletionTokens: 500,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calc.Calculate(details)
	}
}

// TestCalculate_LongContextIgnoresCachedSubset asserts the long-context tier is
// selected from the true input size. CachedTokens is documented as a subset of
// PromptTokens, so adding the two double-counted the cached portion and applied
// long-context rates to requests below the threshold — a 200k prompt with 100k
// cache hits was billed at exactly 2x.
func TestCalculate_LongContextIgnoresCachedSubset(t *testing.T) {
	cfg := &config.PricingConfig{Providers: map[string]config.ProviderPricing{
		"anthropic": {Models: map[string]config.PriceEntry{
			"m": {
				Input: 5, CachedInput: 0.5, Output: 25,
				LongContext: &config.LongContextPricing{
					ThresholdTokens: 272000, Input: 10, CachedInput: 1, Output: 50,
				},
			},
		}},
	}}
	c := NewCalculator(func() *config.PricingConfig { return cfg })

	t.Run("below threshold despite cached tokens", func(t *testing.T) {
		got, ok := c.Calculate(RequestDetails{
			Provider: "anthropic", Model: "m",
			PromptTokens: 200_000, CachedTokens: 100_000, CompletionTokens: 1_000,
		})
		if !ok {
			t.Fatal("expected pricing to be found")
		}
		want := (100_000.0/1e6)*5 + (100_000.0/1e6)*0.5 + (1_000.0/1e6)*25
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("got %.6f, want %.6f — 200k real input is below the 272k threshold "+
				"and must use standard rates", got, want)
		}
	})

	t.Run("above threshold uses long-context rates", func(t *testing.T) {
		got, ok := c.Calculate(RequestDetails{
			Provider: "anthropic", Model: "m",
			PromptTokens: 300_000, CachedTokens: 0, CompletionTokens: 1_000,
		})
		if !ok {
			t.Fatal("expected pricing to be found")
		}
		want := (300_000.0/1e6)*10 + (1_000.0/1e6)*50
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("got %.6f, want %.6f — 300k input must use long-context rates", got, want)
		}
	})
}

// TestCalculate_AbsentCachedRateFallsBackToInput covers a pricing entry with no
// cached_input. Once cached tokens are split out of the prompt total, a missing
// rate is zero — so a fully cached request would have recorded no input cost,
// a spend-accounting bypass that shows up exactly when caching works best.
func TestCalculate_AbsentCachedRateFallsBackToInput(t *testing.T) {
	cfg := &config.PricingConfig{Providers: map[string]config.ProviderPricing{
		"p": {Models: map[string]config.PriceEntry{
			// No cached_input, and a long-context tier that also omits it.
			"m": {
				Input: 10, Output: 20,
				LongContext: &config.LongContextPricing{
					ThresholdTokens: 100_000, Input: 20, Output: 40,
				},
			},
		}},
	}}
	c := NewCalculator(func() *config.PricingConfig { return cfg })

	t.Run("standard tier", func(t *testing.T) {
		got, ok := c.Calculate(RequestDetails{
			Provider: "p", Model: "m",
			PromptTokens: 1000, CachedTokens: 1000, CompletionTokens: 0,
		})
		if !ok {
			t.Fatal("no price found")
		}
		want := (1000.0 / 1e6) * 10 // every token cached, charged at the input rate
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("fully cached request cost %.9f, want %.9f — an absent cached "+
				"rate must not price cached input at zero", got, want)
		}
	})

	t.Run("long-context tier", func(t *testing.T) {
		got, ok := c.Calculate(RequestDetails{
			Provider: "p", Model: "m",
			PromptTokens: 200_000, CachedTokens: 200_000, CompletionTokens: 0,
		})
		if !ok {
			t.Fatal("no price found")
		}
		want := (200_000.0 / 1e6) * 20 // long-context input rate
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("fully cached long-context request cost %.9f, want %.9f", got, want)
		}
	})
}
