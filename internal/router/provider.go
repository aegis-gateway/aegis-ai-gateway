package router

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/router/adapters"
)

// Registry manages provider adapters.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]adapters.ProviderAdapter
}

func NewRegistry() *Registry {
	return &Registry{
		adapters: make(map[string]adapters.ProviderAdapter),
	}
}

func (r *Registry) Register(name string, adapter adapters.ProviderAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[name] = adapter
}

func (r *Registry) Get(name string) (adapters.ProviderAdapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[name]
	return a, ok
}

// BuildFromConfig builds provider adapters from the providers config.
func BuildFromConfig(provCfg *config.ProvidersConfig) *Registry {
	registry := NewRegistry()
	for name, cfg := range provCfg.Providers {
		client := &http.Client{
			Timeout: cfg.Timeout,
			Transport: &http.Transport{
				MaxIdleConns:        cfg.MaxConcurrent,
				MaxIdleConnsPerHost: cfg.MaxConcurrent,
				IdleConnTimeout:     90 * time.Second,
				ForceAttemptHTTP2:   true,
			},
		}

		var adapter adapters.ProviderAdapter
		switch cfg.Type {
		case "openai":
			adapter = adapters.NewOpenAIAdapter(cfg, client)
		case "anthropic":
			adapter = adapters.NewAnthropicAdapter(cfg, client)
		default:
			// Fall back to OpenAI-compatible for unknown types
			adapter = adapters.NewOpenAIAdapter(cfg, client)
		}
		registry.Register(name, adapter)
	}
	return registry
}

// ResolveRoute finds the right provider for a model request.
func ResolveRoute(modelsCfg *config.ModelsConfig, registry *Registry, modelName string, classification string) (adapters.ProviderAdapter, string, error) {
	mapping, ok := modelsCfg.Models[modelName]
	if !ok {
		return nil, "", fmt.Errorf("unknown model: %s", modelName)
	}

	// Try primary provider
	if adapter, ok := registry.Get(mapping.Primary.Provider); ok {
		return adapter, mapping.Primary.Model, nil
	}

	// Try fallbacks
	for _, fb := range mapping.Fallback {
		if adapter, ok := registry.Get(fb.Provider); ok {
			return adapter, fb.Model, nil
		}
	}

	return nil, "", fmt.Errorf("no available provider for model: %s", modelName)
}
