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

package config

type ModelsConfig struct {
	Models map[string]ModelMapping `yaml:"models"`
}

type ModelMapping struct {
	DisplayName     string          `yaml:"display_name"`
	DeprecatedAlias string          `yaml:"deprecated_alias,omitempty"`
	Primary         ProviderRoute   `yaml:"primary"`
	Fallback        []ProviderRoute `yaml:"fallback"`
}

type ProviderRoute struct {
	Provider              string `yaml:"provider"`
	Model                 string `yaml:"model"`
	Deployment            string `yaml:"deployment,omitempty"`
	Endpoint              string `yaml:"endpoint,omitempty"`
	ClassificationCeiling string `yaml:"classification_ceiling"`
	// UnscannedParts names the kinds of content part this route may carry
	// although no filter reads them. The only kind is "image". Empty, the
	// default, means the route carries scanned text only, and a request with
	// an image part is never routed here. Each route opts in on its own: a
	// primary that admits images does not make its fallbacks admit them.
	//
	// Opting a route in is a statement that its provider may see content the
	// gateway did not inspect, so it belongs on routes whose provider the
	// operator trusts with that, such as an on-device model.
	UnscannedParts []string `yaml:"unscanned_parts,omitempty"`
}

// AdmitsUnscanned reports whether the route lists every kind in kinds. An
// unknown kind in the route's list admits nothing extra: it can only fail to
// match, so a typo fails closed.
func (r ProviderRoute) AdmitsUnscanned(kinds []string) bool {
	for _, k := range kinds {
		found := false
		for _, allowed := range r.UnscannedParts {
			if allowed == k {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// PricingConfig is loaded from configs/pricing.yaml.
type PricingConfig struct {
	VerifiedAt string                     `yaml:"verified_at"`
	Providers  map[string]ProviderPricing `yaml:"providers"`
}

type ProviderPricing struct {
	SourceURL string                `yaml:"source_url"`
	Models    map[string]PriceEntry `yaml:"models"`
}

type PriceEntry struct {
	// Rates are USD per million tokens.
	Input           float64             `yaml:"input"`
	CachedInput     float64             `yaml:"cached_input"`
	CacheWrite5m    float64             `yaml:"cache_write_5m"`
	CacheWrite1h    float64             `yaml:"cache_write_1h"`
	Output          float64             `yaml:"output"`
	BatchMultiplier float64             `yaml:"batch_multiplier"`
	RegionalUplift  float64             `yaml:"regional_uplift,omitempty"`
	LongContext     *LongContextPricing `yaml:"long_context,omitempty"`
}

type LongContextPricing struct {
	ThresholdTokens int     `yaml:"threshold_tokens"`
	Input           float64 `yaml:"input"`
	CachedInput     float64 `yaml:"cached_input"`
	CacheWrite5m    float64 `yaml:"cache_write_5m"`
	Output          float64 `yaml:"output"`
}
