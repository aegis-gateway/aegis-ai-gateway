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
