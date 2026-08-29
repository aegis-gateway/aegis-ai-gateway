package cost

import (
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
)

func writeTestCalc(entry config.PriceEntry) *Calculator {
	return NewCalculator(func() *config.PricingConfig {
		return &config.PricingConfig{Providers: map[string]config.ProviderPricing{
			"p": {Models: map[string]config.PriceEntry{"m": entry}},
		}}
	})
}

// TestCacheWriteIsNotBilledAtZero is the regression for a bypass this file
// already had once, reached through a different field.
//
// Write tokens are subtracted out of the prompt total, so a pricing entry with
// no write rate would multiply them by zero and a cache-warming request would
// record no input cost for the tokens it warmed with. The same shape as an
// absent cached_input rate, and it appears precisely when caching is doing its
// job.
func TestCacheWriteIsNotBilledAtZero(t *testing.T) {
	t.Parallel()

	// No cache_write rates configured at all.
	calc := writeTestCalc(config.PriceEntry{Input: 5.00, CachedInput: 0.50, Output: 25.00})

	got, ok := calc.Calculate(RequestDetails{
		Provider: "p", Model: "m",
		PromptTokens: 1_000_000, CacheWrite5mTokens: 1_000_000,
	})
	if !ok {
		t.Fatal("no pricing found")
	}
	if got == 0 {
		t.Fatal("a request that wrote a million tokens into cache was billed nothing. An absent " +
			"write rate must not zero out tokens that have been subtracted from the prompt total")
	}
	if got != 5.00 {
		t.Errorf("cost = %v, want 5.00 (the input rate as a floor)", got)
	}
}

// TestCacheWriteUsesTheConfiguredRate covers the ordinary path, and pins the
// direction: a write costs more than ordinary input, not less.
func TestCacheWriteUsesTheConfiguredRate(t *testing.T) {
	t.Parallel()

	calc := writeTestCalc(config.PriceEntry{
		Input: 5.00, CachedInput: 0.50, CacheWrite5m: 6.25, CacheWrite1h: 10.00, Output: 25.00,
	})

	plain, _ := calc.Calculate(RequestDetails{Provider: "p", Model: "m", PromptTokens: 1_000_000})
	w5, _ := calc.Calculate(RequestDetails{Provider: "p", Model: "m",
		PromptTokens: 1_000_000, CacheWrite5mTokens: 1_000_000})
	w1, _ := calc.Calculate(RequestDetails{Provider: "p", Model: "m",
		PromptTokens: 1_000_000, CacheWrite1hTokens: 1_000_000})

	if plain != 5.00 || w5 != 6.25 || w1 != 10.00 {
		t.Errorf("input=%v write5m=%v write1h=%v, want 5.00 / 6.25 / 10.00", plain, w5, w1)
	}
	if !(w5 > plain && w1 > w5) {
		t.Errorf("a cache write should cost more than ordinary input, and an hour more than five "+
			"minutes: got input=%v 5m=%v 1h=%v", plain, w5, w1)
	}
}

// TestCacheComponentsAreDisjoint checks the arithmetic when a request both
// reads from and writes to the cache, which is the ordinary case once a
// conversation grows past one cache breakpoint.
func TestCacheComponentsAreDisjoint(t *testing.T) {
	t.Parallel()

	calc := writeTestCalc(config.PriceEntry{
		Input: 5.00, CachedInput: 0.50, CacheWrite5m: 6.25, Output: 25.00,
	})

	// 1M prompt: 600k read from cache, 300k written, 100k plain.
	got, ok := calc.Calculate(RequestDetails{
		Provider: "p", Model: "m",
		PromptTokens: 1_000_000, CachedTokens: 600_000, CacheWrite5mTokens: 300_000,
	})
	if !ok {
		t.Fatal("no pricing found")
	}
	want := 0.6*0.50 + 0.3*6.25 + 0.1*5.00
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("cost = %v, want %v. Each component must be priced once: the uncached remainder "+
			"is the prompt minus reads minus writes", got, want)
	}
}
