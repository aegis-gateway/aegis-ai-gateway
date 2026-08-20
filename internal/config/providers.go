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

import "time"

type ProvidersConfig struct {
	Providers map[string]ProviderConfig `yaml:"providers"`
}

type ProviderConfig struct {
	Type          string            `yaml:"type"`
	BaseURL       string            `yaml:"base_url"`
	APIKey        string            `yaml:"api_key"`
	APIVersion    string            `yaml:"api_version,omitempty"`
	MaxConcurrent int               `yaml:"max_concurrent"`
	Timeout       time.Duration     `yaml:"timeout"`
	Headers       map[string]string `yaml:"headers,omitempty"`
}
