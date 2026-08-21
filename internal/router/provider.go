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

package router

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router/adapters"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
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

// ListProviders returns a list of all registered provider names.
func (r *Registry) ListProviders() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	return names
}

// ReplaceFrom replaces this registry's adapters with those from another registry.
func (r *Registry) ReplaceFrom(other *Registry) {
	other.mu.RLock()
	newAdapters := make(map[string]adapters.ProviderAdapter, len(other.adapters))
	for k, v := range other.adapters {
		newAdapters[k] = v
	}
	other.mu.RUnlock()

	r.mu.Lock()
	r.adapters = newAdapters
	r.mu.Unlock()
}

// GetProvider returns a provider adapter by name (for health checks).
func (r *Registry) GetProvider(name string) adapters.ProviderAdapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.adapters[name]
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

// Route is the outcome of resolving a model alias to a concrete provider.
//
// ProviderKey is the name under which the provider is configured in
// providers.yaml and referenced from models.yaml (e.g. "azure_openai",
// "internal_vllm"). It is deliberately distinct from Adapter.Name(), which
// reports the adapter *type* ("openai", "anthropic") and is shared by every
// provider served by that adapter. Pricing, metrics and usage attribution must
// key off ProviderKey; using the adapter name conflates distinct providers and
// looks up the wrong pricing row.
type Route struct {
	Adapter     adapters.ProviderAdapter
	ProviderKey string
	Model       string
}

// ResolveRoute finds the right provider for a model request.
// It checks classification ceilings to ensure the request's data classification
// does not exceed what the provider route is allowed to handle.
// If healthTracker is non-nil, providers with open circuit breakers are skipped.
func ResolveRoute(modelsCfg *config.ModelsConfig, registry *Registry, healthTracker *HealthTracker, modelName string, classification string) (Route, error) {
	mapping, ok := modelsCfg.Models[modelName]
	if !ok {
		return Route{}, fmt.Errorf("unknown model: %s", modelName)
	}

	// Try primary provider (must be registered, classification-eligible, and healthy)
	if routeEligible(mapping.Primary, classification) && providerHealthy(healthTracker, mapping.Primary.Provider) {
		if adapter, ok := registry.Get(mapping.Primary.Provider); ok {
			return Route{Adapter: adapter, ProviderKey: mapping.Primary.Provider, Model: mapping.Primary.Model}, nil
		}
	}

	// Try fallbacks in order
	for _, fb := range mapping.Fallback {
		if routeEligible(fb, classification) && providerHealthy(healthTracker, fb.Provider) {
			if adapter, ok := registry.Get(fb.Provider); ok {
				return Route{Adapter: adapter, ProviderKey: fb.Provider, Model: fb.Model}, nil
			}
		}
	}

	return Route{}, fmt.Errorf("no eligible provider for model %s at classification %s", modelName, classification)
}

// providerHealthy returns true if the provider is healthy or if no health tracker is configured.
func providerHealthy(ht *HealthTracker, provider string) bool {
	if ht == nil {
		return true
	}
	return ht.IsAvailable(provider)
}
