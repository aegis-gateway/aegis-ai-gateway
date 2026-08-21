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
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
)

// TestValidatePricingCoverage_AllPresent verifies that a models config whose
// every route (primary + all fallbacks) has a pricing entry produces no missing
// pairs — meaning the startup validation passes.
func TestValidatePricingCoverage_AllPresent(t *testing.T) {
	modelsCfg := &config.ModelsConfig{
		Models: map[string]config.ModelMapping{
			"aegis-fast": {
				Primary: config.ProviderRoute{
					Provider: "anthropic",
					Model:    "claude-haiku-4-5-20251001",
				},
				Fallback: []config.ProviderRoute{
					{Provider: "openai", Model: "gpt-5.6-luna"},
				},
			},
		},
	}
	pricingCfg := &config.PricingConfig{
		Providers: map[string]config.ProviderPricing{
			"anthropic": {
				Models: map[string]config.PriceEntry{
					"claude-haiku-4-5-20251001": {Input: 1.00, Output: 5.00},
				},
			},
			"openai": {
				Models: map[string]config.PriceEntry{
					"gpt-5.6-luna": {Input: 0.20, Output: 1.20},
				},
			},
		},
	}

	missing := ValidatePricingCoverage(modelsCfg, pricingCfg)
	if len(missing) != 0 {
		t.Errorf("expected no missing pairs, got %v", missing)
	}
}

// TestValidatePricingCoverage_MissingPrimary verifies that a primary route
// without a pricing entry is reported as missing, so the gateway fails at
// startup instead of silently charging zero for traffic.
func TestValidatePricingCoverage_MissingPrimary(t *testing.T) {
	modelsCfg := &config.ModelsConfig{
		Models: map[string]config.ModelMapping{
			"aegis-new": {
				Primary: config.ProviderRoute{
					Provider: "openai",
					Model:    "gpt-5.7-unreleased", // not in pricing
				},
			},
		},
	}
	pricingCfg := &config.PricingConfig{
		Providers: map[string]config.ProviderPricing{
			"openai": {
				Models: map[string]config.PriceEntry{
					"gpt-5.6-sol": {Input: 5.00, Output: 30.00},
				},
			},
		},
	}

	missing := ValidatePricingCoverage(modelsCfg, pricingCfg)
	if len(missing) != 1 {
		t.Fatalf("expected 1 missing pair, got %d: %v", len(missing), missing)
	}
	if missing[0].Provider != "openai" || missing[0].Model != "gpt-5.7-unreleased" {
		t.Errorf("unexpected missing pair: %v", missing[0])
	}
}

// TestValidatePricingCoverage_MissingFallback verifies that a fallback route
// without a pricing entry is reported. All fallbacks must have pricing because
// any one of them may serve traffic after a provider failure.
func TestValidatePricingCoverage_MissingFallback(t *testing.T) {
	modelsCfg := &config.ModelsConfig{
		Models: map[string]config.ModelMapping{
			"aegis-gpt4": {
				Primary: config.ProviderRoute{
					Provider: "openai",
					Model:    "gpt-5.6-sol",
				},
				Fallback: []config.ProviderRoute{
					{Provider: "anthropic", Model: "claude-opus-5"},
					{Provider: "azure_openai", Model: "gpt-5.6-sol"}, // no pricing
				},
			},
		},
	}
	pricingCfg := &config.PricingConfig{
		Providers: map[string]config.ProviderPricing{
			"openai": {
				Models: map[string]config.PriceEntry{
					"gpt-5.6-sol": {Input: 5.00, Output: 30.00},
				},
			},
			"anthropic": {
				Models: map[string]config.PriceEntry{
					"claude-opus-5": {Input: 5.00, Output: 25.00},
				},
			},
			// azure_openai intentionally absent
		},
	}

	missing := ValidatePricingCoverage(modelsCfg, pricingCfg)
	if len(missing) != 1 {
		t.Fatalf("expected 1 missing pair, got %d: %v", len(missing), missing)
	}
	if missing[0].Provider != "azure_openai" || missing[0].Model != "gpt-5.6-sol" {
		t.Errorf("unexpected missing pair: %v", missing[0])
	}
}

// TestValidatePricingCoverage_NilPricing verifies that a nil pricingCfg causes
// all routed models to be reported as missing — failing fast and loudly rather
// than silently passing with zero cost.
func TestValidatePricingCoverage_NilPricing(t *testing.T) {
	modelsCfg := &config.ModelsConfig{
		Models: map[string]config.ModelMapping{
			"aegis-fast": {
				Primary: config.ProviderRoute{
					Provider: "anthropic",
					Model:    "claude-haiku-4-5-20251001",
				},
			},
		},
	}

	missing := ValidatePricingCoverage(modelsCfg, nil)
	if len(missing) != 1 {
		t.Fatalf("expected 1 missing pair with nil pricing, got %d: %v", len(missing), missing)
	}
}

// TestValidatePricingCoverage_NilModels verifies that a nil modelsCfg is safe
// (no models to validate → no missing pairs).
func TestValidatePricingCoverage_NilModels(t *testing.T) {
	missing := ValidatePricingCoverage(nil, nil)
	if len(missing) != 0 {
		t.Errorf("expected no missing pairs for nil modelsCfg, got %v", missing)
	}
}

// TestValidatePricingCoverage_DuplicateRoutes verifies that the same
// provider/model pair shared across multiple model entries is only reported
// once, avoiding duplicate noise in the startup error message.
func TestValidatePricingCoverage_DuplicateRoutes(t *testing.T) {
	modelsCfg := &config.ModelsConfig{
		Models: map[string]config.ModelMapping{
			"model-a": {
				Primary: config.ProviderRoute{Provider: "openai", Model: "gpt-5.7-unreleased"},
			},
			"model-b": {
				Primary: config.ProviderRoute{Provider: "openai", Model: "gpt-5.7-unreleased"},
			},
		},
	}
	pricingCfg := &config.PricingConfig{
		Providers: map[string]config.ProviderPricing{
			"openai": {
				Models: map[string]config.PriceEntry{},
			},
		},
	}

	missing := ValidatePricingCoverage(modelsCfg, pricingCfg)
	if len(missing) != 1 {
		t.Errorf("expected 1 de-duplicated missing pair, got %d: %v", len(missing), missing)
	}
}
