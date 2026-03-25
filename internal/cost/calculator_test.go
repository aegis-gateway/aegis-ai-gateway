package cost

import (
	"testing"

	"github.com/af-corp/aegis-gateway/internal/config"
)

func TestCalculator_Calculate(t *testing.T) {
	tests := []struct {
		name              string
		provider          string
		model             string
		promptTokens      int
		completionTokens  int
		expectedCost      float64
		expectedFound     bool
	}{
		{
			name:             "OpenAI GPT-4o standard request",
			provider:         "openai",
			model:            "gpt-4o",
			promptTokens:     1000,
			completionTokens: 500,
			expectedCost:     0.0025*1 + 0.01*0.5, // 1000/1000 * 0.0025 + 500/1000 * 0.01 = 0.0075
			expectedFound:    true,
		},
		{
			name:             "OpenAI GPT-4o-mini economical",
			provider:         "openai",
			model:            "gpt-4o-mini",
			promptTokens:     10000,
			completionTokens: 2000,
			expectedCost:     0.00015*10 + 0.0006*2, // 10000/1000 * 0.00015 + 2000/1000 * 0.0006 = 0.0027
			expectedFound:    true,
		},
		{
			name:             "Anthropic Claude Sonnet",
			provider:         "anthropic",
			model:            "claude-sonnet-4-5-20250929",
			promptTokens:     2500,
			completionTokens: 1500,
			expectedCost:     0.003*2.5 + 0.015*1.5, // 2500/1000 * 0.003 + 1500/1000 * 0.015 = 0.03
			expectedFound:    true,
		},
		{
			name:             "Anthropic Claude Haiku fast",
			provider:         "anthropic",
			model:            "claude-haiku-4-5-20251001",
			promptTokens:     5000,
			completionTokens: 1000,
			expectedCost:     0.0008*5 + 0.004*1, // 5000/1000 * 0.0008 + 1000/1000 * 0.004 = 0.008
			expectedFound:    true,
		},
		{
			name:             "Anthropic Claude Opus expensive",
			provider:         "anthropic",
			model:            "claude-opus-4-5-20250929",
			promptTokens:     1000,
			completionTokens: 1000,
			expectedCost:     0.015*1 + 0.075*1, // 1000/1000 * 0.015 + 1000/1000 * 0.075 = 0.09
			expectedFound:    true,
		},
		{
			name:             "Azure OpenAI GPT-4o",
			provider:         "azure_openai",
			model:            "gpt-4o",
			promptTokens:     3000,
			completionTokens: 1500,
			expectedCost:     0.0025*3 + 0.01*1.5, // 3000/1000 * 0.0025 + 1500/1000 * 0.01 = 0.0225
			expectedFound:    true,
		},
		{
			name:             "Zero tokens",
			provider:         "openai",
			model:            "gpt-4o",
			promptTokens:     0,
			completionTokens: 0,
			expectedCost:     0.0,
			expectedFound:    true,
		},
		{
			name:             "Unknown provider",
			provider:         "unknown",
			model:            "mystery-model",
			promptTokens:     1000,
			completionTokens: 500,
			expectedCost:     0.0,
			expectedFound:    false,
		},
		{
			name:             "Unknown model for known provider",
			provider:         "openai",
			model:            "gpt-5-turbo",
			promptTokens:     1000,
			completionTokens: 500,
			expectedCost:     0.0,
			expectedFound:    false,
		},
		{
			name:             "Only prompt tokens",
			provider:         "openai",
			model:            "gpt-4o-mini",
			promptTokens:     5000,
			completionTokens: 0,
			expectedCost:     0.00015*5, // 5000/1000 * 0.00015 = 0.00075
			expectedFound:    true,
		},
		{
			name:             "Only completion tokens",
			provider:         "openai",
			model:            "gpt-4o-mini",
			promptTokens:     0,
			completionTokens: 3000,
			expectedCost:     0.0006*3, // 3000/1000 * 0.0006 = 0.0018
			expectedFound:    true,
		},
	}

	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{
			Pricing: map[string]map[string]config.PriceEntry{
				"openai": {
					"gpt-4o": {
						Input:  0.0025,
						Output: 0.01,
					},
					"gpt-4o-mini": {
						Input:  0.00015,
						Output: 0.0006,
					},
				},
				"anthropic": {
					"claude-sonnet-4-5-20250929": {
						Input:  0.003,
						Output: 0.015,
					},
					"claude-haiku-4-5-20251001": {
						Input:  0.0008,
						Output: 0.004,
					},
					"claude-opus-4-5-20250929": {
						Input:  0.015,
						Output: 0.075,
					},
				},
				"azure_openai": {
					"gpt-4o": {
						Input:  0.0025,
						Output: 0.01,
					},
				},
			},
		}
	}

	calc := NewCalculator(modelsCfg)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost, found := calc.Calculate(tt.provider, tt.model, tt.promptTokens, tt.completionTokens)

			if found != tt.expectedFound {
				t.Errorf("Calculate() found = %v, want %v", found, tt.expectedFound)
			}

			// Use a small epsilon for float comparison
			const epsilon = 0.0001
			if diff := cost - tt.expectedCost; diff < -epsilon || diff > epsilon {
				t.Errorf("Calculate() cost = %v, want %v (diff: %v)", cost, tt.expectedCost, diff)
			}
		})
	}
}

func TestCalculator_CacheInvalidation(t *testing.T) {
	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{
			Pricing: map[string]map[string]config.PriceEntry{
				"openai": {
					"gpt-4o": {
						Input:  0.0025,
						Output: 0.01,
					},
				},
			},
		}
	}

	calc := NewCalculator(modelsCfg)

	// First call should populate cache
	cost1, found1 := calc.Calculate("openai", "gpt-4o", 1000, 500)
	if !found1 {
		t.Fatal("Expected pricing to be found")
	}

	// Verify cache has entry
	if len(calc.priceCache) != 1 {
		t.Errorf("Expected cache to have 1 entry, got %d", len(calc.priceCache))
	}

	// Second call should use cache (same result)
	cost2, found2 := calc.Calculate("openai", "gpt-4o", 1000, 500)
	if cost1 != cost2 || found1 != found2 {
		t.Error("Cached result differs from original")
	}

	// Invalidate cache
	calc.InvalidateCache()
	if len(calc.priceCache) != 0 {
		t.Errorf("Expected cache to be empty after invalidation, got %d entries", len(calc.priceCache))
	}

	// Third call should repopulate cache
	cost3, found3 := calc.Calculate("openai", "gpt-4o", 1000, 500)
	if cost1 != cost3 || found1 != found3 {
		t.Error("Result after cache invalidation differs")
	}

	if len(calc.priceCache) != 1 {
		t.Errorf("Expected cache to have 1 entry after repopulation, got %d", len(calc.priceCache))
	}
}

func TestCalculator_GetModelPrice(t *testing.T) {
	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{
			Pricing: map[string]map[string]config.PriceEntry{
				"openai": {
					"gpt-4o": {
						Input:  0.0025,
						Output: 0.01,
					},
				},
			},
		}
	}

	calc := NewCalculator(modelsCfg)

	price, found := calc.GetModelPrice("openai", "gpt-4o")
	if !found {
		t.Fatal("Expected pricing to be found")
	}

	if price.InputPerToken != 0.0025 {
		t.Errorf("Expected input price 0.0025, got %v", price.InputPerToken)
	}

	if price.OutputPerToken != 0.01 {
		t.Errorf("Expected output price 0.01, got %v", price.OutputPerToken)
	}

	// Test unknown model
	_, found = calc.GetModelPrice("openai", "unknown")
	if found {
		t.Error("Expected pricing not to be found for unknown model")
	}
}

func BenchmarkCalculator_Calculate(b *testing.B) {
	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{
			Pricing: map[string]map[string]config.PriceEntry{
				"openai": {
					"gpt-4o": {
						Input:  0.0025,
						Output: 0.01,
					},
				},
			},
		}
	}

	calc := NewCalculator(modelsCfg)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calc.Calculate("openai", "gpt-4o", 1000, 500)
	}
}
