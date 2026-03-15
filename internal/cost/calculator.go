package cost

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/af-corp/aegis-gateway/internal/config"
)

// Calculator provides cost estimation based on model pricing configuration.
type Calculator struct {
	mu         sync.RWMutex
	modelsCfg  func() *config.ModelsConfig
	priceCache map[string]ModelPrice // cache for faster lookups
}

// ModelPrice represents the pricing for a specific provider/model combination.
type ModelPrice struct {
	InputPerToken  float64
	OutputPerToken float64
}

// NewCalculator creates a new cost calculator.
func NewCalculator(modelsCfg func() *config.ModelsConfig) *Calculator {
	return &Calculator{
		modelsCfg:  modelsCfg,
		priceCache: make(map[string]ModelPrice),
	}
}

// Calculate computes the estimated cost in USD for a request.
// Returns the cost and a boolean indicating if pricing was found.
func (c *Calculator) Calculate(provider, model string, promptTokens, completionTokens int) (float64, bool) {
	if promptTokens == 0 && completionTokens == 0 {
		return 0.0, true // valid but free
	}

	price, found := c.getPrice(provider, model)
	if !found {
		slog.Warn("no pricing found for model",
			"provider", provider,
			"model", model,
		)
		return 0.0, false
	}

	// Cost = (input_tokens / 1000) * input_price + (output_tokens / 1000) * output_price
	// Pricing in config is per 1000 tokens
	inputCost := (float64(promptTokens) / 1000.0) * price.InputPerToken
	outputCost := (float64(completionTokens) / 1000.0) * price.OutputPerToken
	totalCost := inputCost + outputCost

	slog.Debug("cost calculated",
		"provider", provider,
		"model", model,
		"prompt_tokens", promptTokens,
		"completion_tokens", completionTokens,
		"input_cost", inputCost,
		"output_cost", outputCost,
		"total_cost", totalCost,
	)

	return totalCost, true
}

// getPrice retrieves pricing from cache or config.
func (c *Calculator) getPrice(provider, model string) (ModelPrice, bool) {
	cacheKey := fmt.Sprintf("%s:%s", provider, model)

	// Check cache first
	c.mu.RLock()
	if price, found := c.priceCache[cacheKey]; found {
		c.mu.RUnlock()
		return price, true
	}
	c.mu.RUnlock()

	// Load from config
	cfg := c.modelsCfg()
	if cfg == nil || cfg.Pricing == nil {
		return ModelPrice{}, false
	}

	providerPricing, ok := cfg.Pricing[provider]
	if !ok {
		return ModelPrice{}, false
	}

	priceEntry, ok := providerPricing[model]
	if !ok {
		return ModelPrice{}, false
	}

	price := ModelPrice{
		InputPerToken:  priceEntry.Input,
		OutputPerToken: priceEntry.Output,
	}

	// Cache it
	c.mu.Lock()
	c.priceCache[cacheKey] = price
	c.mu.Unlock()

	return price, true
}

// InvalidateCache clears the pricing cache. Useful when config is reloaded.
func (c *Calculator) InvalidateCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.priceCache = make(map[string]ModelPrice)
	slog.Info("cost calculator cache invalidated")
}

// GetModelPrice returns the pricing for a specific provider/model (for debugging/admin).
func (c *Calculator) GetModelPrice(provider, model string) (ModelPrice, bool) {
	return c.getPrice(provider, model)
}
