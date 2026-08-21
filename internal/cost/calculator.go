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
	"fmt"
	"log/slog"
	"sync"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
)

// RequestDetails carries the token breakdown and request flags needed to
// compute cost using the richer pricing schema in pricing.yaml.
type RequestDetails struct {
	Provider string
	Model    string

	// Token counts
	PromptTokens     int
	CachedTokens     int // subset of PromptTokens served from cache
	CompletionTokens int

	// Flags
	IsBatch bool // apply batch_multiplier when true
}

// Calculator provides cost estimation based on model pricing configuration.
type Calculator struct {
	mu         sync.RWMutex
	pricingCfg func() *config.PricingConfig
	priceCache map[string]config.PriceEntry
}

// ModelPrice represents the simplified per-token pricing for a model
// (returned for debugging/admin use via GetModelPrice).
type ModelPrice struct {
	InputPerToken  float64
	OutputPerToken float64
}

// NewCalculator creates a new cost calculator.
func NewCalculator(pricingCfg func() *config.PricingConfig) *Calculator {
	return &Calculator{
		pricingCfg: pricingCfg,
		priceCache: make(map[string]config.PriceEntry),
	}
}

// Calculate computes the estimated cost in USD for a request.
// It applies cached_input rates, long_context tiers, and batch_multiplier as appropriate.
// Returns the cost and a boolean indicating whether pricing was found.
func (c *Calculator) Calculate(details RequestDetails) (float64, bool) {
	if details.PromptTokens == 0 && details.CompletionTokens == 0 {
		return 0.0, true
	}

	entry, found := c.getPrice(details.Provider, details.Model)
	if !found {
		slog.Warn("no pricing found for model",
			"provider", details.Provider,
			"model", details.Model,
		)
		return 0.0, false
	}

	// Select rate tier: long_context when total input exceeds the threshold.
	inputRate := entry.Input
	cachedInputRate := entry.CachedInput
	outputRate := entry.Output

	// CachedTokens is a subset of PromptTokens (see RequestDetails), so the
	// prompt count alone is the true input size. Adding them double-counts the
	// cached portion and selects long-context rates below the threshold.
	if entry.LongContext != nil && details.PromptTokens >= entry.LongContext.ThresholdTokens {
		inputRate = entry.LongContext.Input
		cachedInputRate = entry.LongContext.CachedInput
		outputRate = entry.LongContext.Output
	}

	// Uncached prompt tokens = total prompt minus cached tokens.
	uncachedPrompt := details.PromptTokens - details.CachedTokens
	if uncachedPrompt < 0 {
		uncachedPrompt = 0
	}

	// Rates are USD per million tokens.
	const perMillion = 1_000_000.0
	inputCost := (float64(uncachedPrompt) / perMillion) * inputRate
	cachedCost := (float64(details.CachedTokens) / perMillion) * cachedInputRate
	outputCost := (float64(details.CompletionTokens) / perMillion) * outputRate
	totalCost := inputCost + cachedCost + outputCost

	if details.IsBatch && entry.BatchMultiplier > 0 {
		totalCost *= entry.BatchMultiplier
	}

	slog.Debug("cost calculated",
		"provider", details.Provider,
		"model", details.Model,
		"prompt_tokens", details.PromptTokens,
		"cached_tokens", details.CachedTokens,
		"completion_tokens", details.CompletionTokens,
		"is_batch", details.IsBatch,
		"input_cost", inputCost,
		"cached_cost", cachedCost,
		"output_cost", outputCost,
		"total_cost", totalCost,
	)

	return totalCost, true
}

// CalculateSimple is a convenience wrapper for callers that only have raw token
// counts (no cache breakdown, not a batch job).
func (c *Calculator) CalculateSimple(provider, model string, promptTokens, completionTokens int) (float64, bool) {
	return c.Calculate(RequestDetails{
		Provider:         provider,
		Model:            model,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
	})
}

// getPrice retrieves a PriceEntry from cache or from the live pricing config.
func (c *Calculator) getPrice(provider, model string) (config.PriceEntry, bool) {
	cacheKey := fmt.Sprintf("%s:%s", provider, model)

	c.mu.RLock()
	if entry, found := c.priceCache[cacheKey]; found {
		c.mu.RUnlock()
		return entry, true
	}
	c.mu.RUnlock()

	cfg := c.pricingCfg()
	if cfg == nil || cfg.Providers == nil {
		return config.PriceEntry{}, false
	}

	providerPricing, ok := cfg.Providers[provider]
	if !ok {
		return config.PriceEntry{}, false
	}

	entry, ok := providerPricing.Models[model]
	if !ok {
		return config.PriceEntry{}, false
	}

	c.mu.Lock()
	c.priceCache[cacheKey] = entry
	c.mu.Unlock()

	return entry, true
}

// InvalidateCache clears the pricing cache. Call after config reload.
func (c *Calculator) InvalidateCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.priceCache = make(map[string]config.PriceEntry)
	slog.Info("cost calculator cache invalidated")
}

// GetModelPrice returns the simplified per-token pricing for a provider/model
// (for debugging/admin endpoints). For accurate cost estimation, use Calculate.
func (c *Calculator) GetModelPrice(provider, model string) (ModelPrice, bool) {
	entry, found := c.getPrice(provider, model)
	if !found {
		return ModelPrice{}, false
	}
	// Per-token from per-million rates.
	return ModelPrice{
		InputPerToken:  entry.Input / 1_000_000.0,
		OutputPerToken: entry.Output / 1_000_000.0,
	}, true
}

// HasPricing reports whether the given provider/model has a pricing entry.
// Use this for pre-dispatch checks rather than GetModelPrice when only
// presence matters.
func (c *Calculator) HasPricing(provider, model string) bool {
	entry, found := c.getPrice(provider, model)
	return found && IsUsablePriceEntry(entry)
}

// IsUsablePriceEntry reports whether a PriceEntry carries rates that can
// actually price a request. A key that exists but unmarshalled to zeroes —
// because the YAML fields were misspelled, or the stanza is empty — would
// otherwise satisfy a presence check while recording zero spend for every
// request, which is the governance bypass fail-closed pricing exists to stop.
//
// Output may legitimately be zero only if input is too (a free model is
// expressed by omitting the model, not by zero rates), so both are required.
func IsUsablePriceEntry(entry config.PriceEntry) bool {
	if entry.Input <= 0 || entry.Output <= 0 {
		return false
	}
	if entry.LongContext != nil {
		lc := entry.LongContext
		if lc.ThresholdTokens <= 0 || lc.Input <= 0 || lc.Output <= 0 {
			return false
		}
	}
	return true
}

// MissingPricingPair describes a provider/model combination that lacks a pricing entry.
type MissingPricingPair struct {
	Provider string
	Model    string
}

func (m MissingPricingPair) String() string {
	return m.Provider + "/" + m.Model
}

// ValidatePricingCoverage checks that every provider/model reachable through
// modelsCfg routing (primary + all fallbacks) has a corresponding pricing entry
// in pricingCfg. Returns all missing pairs so the caller can fail startup with
// a clear message.
func ValidatePricingCoverage(modelsCfg *config.ModelsConfig, pricingCfg *config.PricingConfig) []MissingPricingPair {
	if modelsCfg == nil {
		return nil
	}

	seen := make(map[string]struct{})
	var missing []MissingPricingPair

	checkRoute := func(provider, model string) {
		key := fmt.Sprintf("%s:%s", provider, model)
		if _, already := seen[key]; already {
			return
		}
		seen[key] = struct{}{}

		var hasPricing bool
		if pricingCfg != nil && pricingCfg.Providers != nil {
			if provPricing, ok := pricingCfg.Providers[provider]; ok {
				entry, ok := provPricing.Models[model]
				hasPricing = ok && IsUsablePriceEntry(entry)
			}
		}
		if !hasPricing {
			missing = append(missing, MissingPricingPair{Provider: provider, Model: model})
		}
	}

	for _, mapping := range modelsCfg.Models {
		checkRoute(mapping.Primary.Provider, mapping.Primary.Model)
		for _, fb := range mapping.Fallback {
			checkRoute(fb.Provider, fb.Model)
		}
	}

	return missing
}
