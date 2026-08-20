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
	Models  map[string]ModelMapping          `yaml:"models"`
	Pricing map[string]map[string]PriceEntry `yaml:"pricing"`
}

type ModelMapping struct {
	DisplayName string           `yaml:"display_name"`
	Primary     ProviderRoute    `yaml:"primary"`
	Fallback    []ProviderRoute  `yaml:"fallback"`
}

type ProviderRoute struct {
	Provider              string `yaml:"provider"`
	Model                 string `yaml:"model"`
	Deployment            string `yaml:"deployment,omitempty"`
	Endpoint              string `yaml:"endpoint,omitempty"`
	ClassificationCeiling string `yaml:"classification_ceiling"`
}

type PriceEntry struct {
	Input  float64 `yaml:"input"`
	Output float64 `yaml:"output"`
}
