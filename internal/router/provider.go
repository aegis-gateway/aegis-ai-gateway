package router

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/router/adapters"
	"github.com/af-corp/aegis-gateway/internal/types"
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

// routeEligible checks whether a provider route's classification ceiling
// permits the request's classification level.
func routeEligible(route config.ProviderRoute, classification string) bool {
	if route.ClassificationCeiling == "" {
		return true // no ceiling configured = allow all
	}
	ceiling, ok := types.ParseClassification(route.ClassificationCeiling)
	if !ok {
		return false // unparseable ceiling = deny
	}
	reqClass, ok := types.ParseClassification(classification)
	if !ok {
		return true // unparseable request classification = allow (fail open for routing)
	}
	return ceiling.Allows(reqClass)
}

// ResolveRoute finds the right provider for a model request.
// It checks classification ceilings to ensure the request's data classification
// does not exceed what the provider route is allowed to handle.
func ResolveRoute(modelsCfg *config.ModelsConfig, registry *Registry, modelName string, classification string) (adapters.ProviderAdapter, string, error) {
	mapping, ok := modelsCfg.Models[modelName]
	if !ok {
		return nil, "", fmt.Errorf("unknown model: %s", modelName)
	}

	// Try primary provider (must be registered and classification-eligible)
	if routeEligible(mapping.Primary, classification) {
		if adapter, ok := registry.Get(mapping.Primary.Provider); ok {
			return adapter, mapping.Primary.Model, nil
		}
	}

	// Try fallbacks in order
	for _, fb := range mapping.Fallback {
		if routeEligible(fb, classification) {
			if adapter, ok := registry.Get(fb.Provider); ok {
				return adapter, fb.Model, nil
			}
		}
	}

	return nil, "", fmt.Errorf("no eligible provider for model %s at classification %s", modelName, classification)
}
