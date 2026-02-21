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
