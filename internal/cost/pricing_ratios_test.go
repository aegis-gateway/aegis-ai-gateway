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
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
)

// Pricing validation checked coverage: that every routed model has a price at
// all. Nothing checked whether the numbers relate to each other correctly, and
// that is how `cache_write_5m` sat at a tenth of its real value across all four
// Anthropic models without anything objecting. Each row was internally
// plausible; only the ratio gave it away.
//
// Anthropic publishes prompt caching as multipliers on the base input rate, so
// the correct values are derivable rather than a matter of taste:
//
//	5-minute cache write   1.25x input
//	1-hour cache write     2x    input
//	cache read             0.1x  input
//
// Source: https://platform.claude.com/docs/en/build-with-claude/prompt-caching.md
// ("5-minute cache write tokens are 1.25 times the base input tokens price;
// 1-hour cache write tokens are 2 times; cache read tokens are 0.1 times").

// anthropicCacheMultipliers are the published ratios, keyed by the field they
// govern.
var anthropicCacheMultipliers = []struct {
	name       string
	multiplier float64
	rate       func(config.PriceEntry) float64
}{
	{"cached_input", 0.10, func(e config.PriceEntry) float64 { return e.CachedInput }},
	{"cache_write_5m", 1.25, func(e config.PriceEntry) float64 { return e.CacheWrite5m }},
	{"cache_write_1h", 2.00, func(e config.PriceEntry) float64 { return e.CacheWrite1h }},
}

func loadShippedPricing(t *testing.T) *config.PricingConfig {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "configs", "pricing.yaml"))
	if err != nil {
		t.Fatalf("reading the shipped pricing file: %v", err)
	}
	var cfg config.PricingConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parsing the shipped pricing file: %v", err)
	}
	return &cfg
}

// TestAnthropicCacheRatesMatchPublishedMultipliers checks every Anthropic row
// against the published ratios.
//
// Its scope is derived from the file rather than a list written here, so a model
// added to the anthropic block is covered without anyone remembering to add it.
// A check whose scope is a fixed list reports success over whatever it happens
// to cover, which is the failure this repository has now fixed more than once.
func TestAnthropicCacheRatesMatchPublishedMultipliers(t *testing.T) {
	t.Parallel()

	cfg := loadShippedPricing(t)
	anthropic, ok := cfg.Providers["anthropic"]
	if !ok {
		t.Fatal("no anthropic provider in configs/pricing.yaml; this test is not checking anything")
	}
	if len(anthropic.Models) == 0 {
		t.Fatal("the anthropic provider has no models; this test is not checking anything")
	}

	for name, entry := range anthropic.Models {
		if entry.Input <= 0 {
			t.Errorf("%s: no input rate, so no cache rate can be checked against it", name)
			continue
		}
		for _, m := range anthropicCacheMultipliers {
			got := m.rate(entry)
			if got == 0 {
				t.Errorf("%s: %s is not set. Anthropic charges for this, and an absent rate is "+
					"billed at the input rate as a floor, which under-charges a write and "+
					"over-charges a read", name, m.name)
				continue
			}
			want := entry.Input * m.multiplier
			// Exact, not approximate. These are published multipliers on a
			// published base, so a mismatch is a data error rather than
			// rounding.
			if diff := got - want; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("%s: %s is %.4f, want %.4f (%.2fx the input rate of %.2f). Ratio is %.4fx",
					name, m.name, got, want, m.multiplier, entry.Input, got/entry.Input)
			}
		}
	}
}

// TestCacheReadIsCheaperThanInputAndWriteIsDearer is the provider-agnostic
// sanity check, and it covers the rows the test above deliberately does not.
//
// The multipliers differ between providers and AEGIS cannot verify a rate it
// has no published source for. But the ordering is not a pricing decision, it
// is what makes caching a tradeoff at all: a read must be cheaper than sending
// the tokens fresh, or nobody would cache, and a write must not be cheaper, or
// caching would be free money.
func TestCacheReadIsCheaperThanInputAndWriteIsDearer(t *testing.T) {
	t.Parallel()

	cfg := loadShippedPricing(t)
	for provider, p := range cfg.Providers {
		for name, entry := range p.Models {
			if entry.Input <= 0 {
				continue
			}
			if entry.CachedInput > 0 && entry.CachedInput >= entry.Input {
				t.Errorf("%s/%s: cached_input %.4f is not below input %.4f. A cache read that "+
					"costs as much as fresh input makes caching pointless; check for a "+
					"transcription error", provider, name, entry.CachedInput, entry.Input)
			}
			if entry.CacheWrite5m > 0 && entry.CacheWrite5m < entry.Input {
				t.Errorf("%s/%s: cache_write_5m %.4f is below input %.4f. A write that costs less "+
					"than fresh input makes caching free money, which no provider offers; this "+
					"is the shape the decimal error had", provider, name, entry.CacheWrite5m, entry.Input)
			}
			if entry.CacheWrite1h > 0 && entry.CacheWrite5m > 0 && entry.CacheWrite1h < entry.CacheWrite5m {
				t.Errorf("%s/%s: a one-hour write (%.4f) costs less than a five-minute one (%.4f)",
					provider, name, entry.CacheWrite1h, entry.CacheWrite5m)
			}
		}
	}
}
