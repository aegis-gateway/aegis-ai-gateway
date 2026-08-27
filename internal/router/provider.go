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
	"log/slog"
	"net/http"
	"os"
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

// MockProviderEnvVar names the environment variable that opts a gateway into
// answering from the mock provider instead of calling out.
//
// It is an environment variable rather than a field in providers.yaml because
// the threat is a configuration file travelling somewhere it was not meant to
// go. A gateway that picked up a mock from a copied config would silently stop
// calling providers while continuing to permit, price, and audit traffic, and
// nothing on the request path would look wrong. Requiring a variable set on the
// process means the mock cannot arrive by copying a file.
const MockProviderEnvVar = "AEGIS_MOCK_PROVIDER"

// MockProviderEnabled reports whether the mock opt-in is set to exactly "true".
//
// Exactly "true" and nothing else: no "1", no "yes", no case folding. A
// half-recognised spelling is how a flag meant to be deliberate becomes one
// somebody sets by accident.
func MockProviderEnabled() bool {
	return os.Getenv(MockProviderEnvVar) == "true"
}

// BuildFromConfig builds provider adapters from the providers config.
//
// When MockProviderEnabled() is true, every configured provider is served by a
// MockAdapter instead of a real one. The mock stands in for the providers
// already in providers.yaml rather than adding a provider of its own, so
// models.yaml keeps routing to "anthropic" and "openai", classification
// ceilings and fallback chains are unchanged, and pricing lookups still resolve
// against the real pricing rows for the real model names. Only the upstream
// HTTP call is replaced; the rest of the request pipeline is untouched.
func BuildFromConfig(provCfg *config.ProvidersConfig) *Registry {
	mockAll := MockProviderEnabled()
	registry := NewRegistry()
	for name, cfg := range provCfg.Providers {
		if mockAll {
			registry.Register(name, adapters.NewMockAdapter(name, cfg))
			continue
		}

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
		case adapters.MockAdapterName:
			// A provider typed "mock" without the opt-in is not quietly
			// downgraded to an OpenAI-compatible adapter by the default branch
			// below: that would send real traffic, with real credentials, to
			// whatever base_url the mock entry happened to carry. It is left
			// unregistered instead, so any alias routing to it resolves to "no
			// eligible provider" and fails closed.
			slog.Error("provider is typed mock but "+MockProviderEnvVar+" is not \"true\"; leaving it unregistered",
				"provider", name)
			continue
		default:
			// Fall back to OpenAI-compatible for unknown types
			adapter = adapters.NewOpenAIAdapter(cfg, client)
		}
		registry.Register(name, adapter)
	}

	if mockAll {
		slog.Warn("mock provider is active: no request will reach a real provider",
			"opt_in", MockProviderEnvVar+"=true",
			"providers", registry.ListProviders(),
		)
	}
	return registry
}

// UsesMockProvider reports whether every registered adapter is a mock. It backs
// the mock_provider field on the health endpoint, and reads the registry rather
// than the environment so that what is reported is what is actually loaded.
func (r *Registry) UsesMockProvider() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.adapters) == 0 {
		return false
	}
	for _, a := range r.adapters {
		if a.Name() != adapters.MockAdapterName {
			return false
		}
	}
	return true
}

// AdapterType reports the adapter type serving a provider name, or "" if the
// provider is not registered. The health endpoint uses it so an operator can
// see which provider is mocked rather than only that some are.
func (r *Registry) AdapterType(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[name]
	if !ok {
		return ""
	}
	return a.Name()
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
