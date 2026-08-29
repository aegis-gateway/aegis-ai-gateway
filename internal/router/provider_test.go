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
	"context"
	"net/http"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router/adapters"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// fakeAdapter implements adapters.ProviderAdapter for testing.
type fakeAdapter struct {
	name string
}

func (f *fakeAdapter) Name() string { return f.name }
func (f *fakeAdapter) TransformRequest(_ context.Context, _ *types.AegisRequest) (*http.Request, error) {
	return nil, nil
}
func (f *fakeAdapter) TransformResponse(_ context.Context, _ *http.Response) (*types.AegisResponse, error) {
	return nil, nil
}
func (f *fakeAdapter) TransformStreamChunk(chunk []byte) ([]byte, error) { return chunk, nil }
func (f *fakeAdapter) SupportsStreaming() bool                           { return false }
func (f *fakeAdapter) SupportsTools() bool                               { return true }
func (f *fakeAdapter) SendRequest(_ *http.Request) (*http.Response, error) {
	return nil, nil
}

func newTestRegistry(names ...string) *Registry {
	r := NewRegistry()
	for _, n := range names {
		r.Register(n, &fakeAdapter{name: n})
	}
	return r
}

func modelsCfgWith(models map[string]config.ModelMapping) *config.ModelsConfig {
	return &config.ModelsConfig{Models: models}
}

func TestResolveRoute_UnknownModel(t *testing.T) {
	registry := newTestRegistry("openai")
	cfg := modelsCfgWith(map[string]config.ModelMapping{})

	_, err := ResolveRoute(cfg, registry, nil, "nonexistent", "INTERNAL")
	if err == nil {
		t.Fatal("expected error for unknown model")
	}
}

func TestResolveRoute_PrimaryProvider(t *testing.T) {
	registry := newTestRegistry("openai")
	cfg := modelsCfgWith(map[string]config.ModelMapping{
		"gpt-4o": {
			Primary: config.ProviderRoute{
				Provider:              "openai",
				Model:                 "gpt-4o",
				ClassificationCeiling: "CONFIDENTIAL",
			},
		},
	})

	route, err := ResolveRoute(cfg, registry, nil, "gpt-4o", "INTERNAL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.Model != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", route.Model)
	}
	if route.ProviderKey != "openai" {
		t.Errorf("expected adapter openai, got %s", route.ProviderKey)
	}
}

func TestResolveRoute_ClassificationGating_BlocksPrimary(t *testing.T) {
	registry := newTestRegistry("openai", "internal_vllm")
	cfg := modelsCfgWith(map[string]config.ModelMapping{
		"test-model": {
			Primary: config.ProviderRoute{
				Provider:              "openai",
				Model:                 "gpt-4o",
				ClassificationCeiling: "INTERNAL", // ceiling is INTERNAL
			},
			Fallback: []config.ProviderRoute{
				{
					Provider:              "internal_vllm",
					Model:                 "llama-70b",
					ClassificationCeiling: "RESTRICTED", // ceiling is RESTRICTED
				},
			},
		},
	})

	// RESTRICTED data exceeds INTERNAL ceiling → should skip openai, use vllm
	route, err := ResolveRoute(cfg, registry, nil, "test-model", "RESTRICTED")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.ProviderKey != "internal_vllm" {
		t.Errorf("expected internal_vllm (fallback), got %s", route.ProviderKey)
	}
	if route.Model != "llama-70b" {
		t.Errorf("expected llama-70b, got %s", route.Model)
	}
}

func TestResolveRoute_ClassificationGating_BlocksAll(t *testing.T) {
	registry := newTestRegistry("openai", "anthropic")
	cfg := modelsCfgWith(map[string]config.ModelMapping{
		"test-model": {
			Primary: config.ProviderRoute{
				Provider:              "openai",
				Model:                 "gpt-4o",
				ClassificationCeiling: "INTERNAL",
			},
			Fallback: []config.ProviderRoute{
				{
					Provider:              "anthropic",
					Model:                 "claude-sonnet",
					ClassificationCeiling: "CONFIDENTIAL",
				},
			},
		},
	})

	// RESTRICTED data exceeds all ceilings → should fail
	_, err := ResolveRoute(cfg, registry, nil, "test-model", "RESTRICTED")
	if err == nil {
		t.Fatal("expected error when all providers are below classification ceiling")
	}
}

func TestResolveRoute_ClassificationGating_AllowsEqualLevel(t *testing.T) {
	registry := newTestRegistry("openai")
	cfg := modelsCfgWith(map[string]config.ModelMapping{
		"test-model": {
			Primary: config.ProviderRoute{
				Provider:              "openai",
				Model:                 "gpt-4o",
				ClassificationCeiling: "CONFIDENTIAL",
			},
		},
	})

	// CONFIDENTIAL data with CONFIDENTIAL ceiling → should be allowed
	route, err := ResolveRoute(cfg, registry, nil, "test-model", "CONFIDENTIAL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.ProviderKey != "openai" {
		t.Errorf("expected openai, got %s", route.ProviderKey)
	}
}

func TestResolveRoute_NoCeiling_AllowsAll(t *testing.T) {
	registry := newTestRegistry("openai")
	cfg := modelsCfgWith(map[string]config.ModelMapping{
		"test-model": {
			Primary: config.ProviderRoute{
				Provider: "openai",
				Model:    "gpt-4o",
				// No ClassificationCeiling set
			},
		},
	})

	// RESTRICTED data with no ceiling → should be allowed
	_, err := ResolveRoute(cfg, registry, nil, "test-model", "RESTRICTED")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveRoute_FallbackOrder(t *testing.T) {
	// Only register the second fallback, not the primary or first fallback
	registry := newTestRegistry("provider-c")
	cfg := modelsCfgWith(map[string]config.ModelMapping{
		"test-model": {
			Primary: config.ProviderRoute{
				Provider:              "provider-a",
				Model:                 "model-a",
				ClassificationCeiling: "CONFIDENTIAL",
			},
			Fallback: []config.ProviderRoute{
				{
					Provider:              "provider-b",
					Model:                 "model-b",
					ClassificationCeiling: "CONFIDENTIAL",
				},
				{
					Provider:              "provider-c",
					Model:                 "model-c",
					ClassificationCeiling: "CONFIDENTIAL",
				},
			},
		},
	})

	route, err := ResolveRoute(cfg, registry, nil, "test-model", "INTERNAL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.ProviderKey != "provider-c" {
		t.Errorf("expected provider-c, got %s", route.ProviderKey)
	}
	if route.Model != "model-c" {
		t.Errorf("expected model-c, got %s", route.Model)
	}
}

// ── Mock provider opt-in ──────────────────────────────────────────

// TestBuildFromConfig_MockRequiresExplicitOptIn is the guarantee that matters
// most about the mock: a gateway must not answer from it unless somebody said
// so on the process. Everything else about the mock is a convenience; this is
// the part that keeps it out of production.
func TestBuildFromConfig_MockRequiresExplicitOptIn(t *testing.T) {
	provCfg := &config.ProvidersConfig{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {Type: "anthropic", BaseURL: "https://api.anthropic.com/v1", APIKey: "test-key"},
			"openai":    {Type: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "test-key"},
		},
	}

	// Every spelling that is not exactly "true" leaves real adapters in place.
	// A flag that accepts "1", "yes", or "TRUE" is a flag somebody sets by
	// accident, so each near-miss is asserted rather than assumed.
	for _, value := range []string{"", "1", "yes", "TRUE", "True", "false", " true"} {
		t.Run("value="+value, func(t *testing.T) {
			t.Setenv(MockProviderEnvVar, value)

			registry := BuildFromConfig(provCfg)
			if registry.UsesMockProvider() {
				t.Errorf("%s=%q enabled the mock provider; only exactly \"true\" may",
					MockProviderEnvVar, value)
			}
			if got := registry.AdapterType("anthropic"); got != "anthropic" {
				t.Errorf("anthropic adapter type = %q, want %q", got, "anthropic")
			}
		})
	}
}

func TestBuildFromConfig_MockReplacesEveryProviderWhenOptedIn(t *testing.T) {
	t.Setenv(MockProviderEnvVar, "true")

	provCfg := &config.ProvidersConfig{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {Type: "anthropic", BaseURL: "https://api.anthropic.com/v1", APIKey: "test-key"},
			"openai":    {Type: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "test-key"},
		},
	}
	registry := BuildFromConfig(provCfg)

	if !registry.UsesMockProvider() {
		t.Error("UsesMockProvider() = false with the opt-in set")
	}
	// Registered under the real provider names, not under a new "mock" name.
	// That is what keeps models.yaml routing, classification ceilings, and the
	// pricing rows for the real model names in play.
	for _, name := range []string{"anthropic", "openai"} {
		if got := registry.AdapterType(name); got != adapters.MockAdapterName {
			t.Errorf("adapter type for %q = %q, want %q", name, got, adapters.MockAdapterName)
		}
	}
	if registry.AdapterType("mock") != "" {
		t.Error("the mock registered itself as a provider named \"mock\"; it must stand in for configured providers, not add one")
	}
}

// TestBuildFromConfig_MockTypeWithoutOptInFailsClosed covers the other
// direction: a providers.yaml entry typed "mock" on a gateway that has not
// opted in. The default branch of the type switch would otherwise hand it to
// the OpenAI adapter, which would send real traffic and real credentials to
// whatever base_url that entry carried.
func TestBuildFromConfig_MockTypeWithoutOptInFailsClosed(t *testing.T) {
	t.Setenv(MockProviderEnvVar, "")

	provCfg := &config.ProvidersConfig{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {Type: "anthropic", BaseURL: "https://api.anthropic.com/v1", APIKey: "test-key"},
			"sneaky":    {Type: adapters.MockAdapterName, BaseURL: "https://example.invalid/v1", APIKey: "test-key"},
		},
	}
	registry := BuildFromConfig(provCfg)

	if _, ok := registry.Get("sneaky"); ok {
		t.Error("a provider typed \"mock\" was registered without the opt-in; it must be left unregistered so routing fails closed")
	}

	modelsCfg := &config.ModelsConfig{
		Models: map[string]config.ModelMapping{
			"aegis-sneaky": {Primary: config.ProviderRoute{Provider: "sneaky", Model: "whatever"}},
		},
	}
	if _, err := ResolveRoute(modelsCfg, registry, nil, "aegis-sneaky", "INTERNAL"); err == nil {
		t.Error("ResolveRoute resolved an alias pointing at an unregistered mock provider; it must fail closed")
	}
}

func TestUsesMockProvider_EmptyRegistryIsNotMock(t *testing.T) {
	// An empty registry has no real adapters, but reporting "mock_provider:
	// true" for it would tell an operator the gateway is answering locally when
	// in fact it is answering nothing at all.
	if NewRegistry().UsesMockProvider() {
		t.Error("an empty registry reported itself as running on the mock provider")
	}
}

// TestBuildFromConfig_SkipsUncredentialedProviders covers the behaviour that
// produced review findings on four separate PRs. A provider with no api_key
// cannot serve a request, but registering it made it *eligible*: ResolveRoute
// selects a primary route on registration, classification and health alone, so
// the request reached the provider, came back 401, and surfaced as a 500 while
// the configured fallback was never tried.
func TestBuildFromConfig_SkipsUncredentialedProviders(t *testing.T) {
	provCfg := &config.ProvidersConfig{
		Providers: map[string]config.ProviderConfig{
			"anthropic":     {Type: "anthropic", BaseURL: "https://api.anthropic.com/v1"}, // no key
			"openai":        {Type: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"},
			"internal_vllm": {Type: "openai", BaseURL: "http://vllm.internal/v1", APIKey: "not-needed"},
		},
	}
	reg := BuildFromConfig(provCfg)

	if _, ok := reg.Get("anthropic"); ok {
		t.Error("a provider with no api_key stayed registered; it is eligible for routing " +
			"and every request through it dies on a 401 with the fallback unused")
	}
	if _, ok := reg.Get("openai"); !ok {
		t.Error("a credentialed provider was not registered")
	}
	// A provider that genuinely needs no credential says so with a placeholder.
	if _, ok := reg.Get("internal_vllm"); !ok {
		t.Error(`api_key: "not-needed" must keep a keyless provider registered`)
	}
}

// TestResolveRoute_FallsBackWhenPrimaryHasNoKey is the outcome that matters:
// with only an OpenAI key set, an Anthropic-primary alias must reach its OpenAI
// fallback instead of failing upstream. Every alias in configs/models.yaml is
// Anthropic-primary, so before this change OPENAI_API_KEY alone drove nothing.
func TestResolveRoute_FallsBackWhenPrimaryHasNoKey(t *testing.T) {
	reg := BuildFromConfig(&config.ProvidersConfig{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {Type: "anthropic", BaseURL: "https://api.anthropic.com/v1"}, // no key
			"openai":    {Type: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"},
		},
	})

	models := &config.ModelsConfig{Models: map[string]config.ModelMapping{
		"aegis-fast": {
			Primary: config.ProviderRoute{Provider: "anthropic", Model: "claude-haiku", ClassificationCeiling: "INTERNAL"},
			Fallback: []config.ProviderRoute{
				{Provider: "openai", Model: "gpt-4o-mini", ClassificationCeiling: "INTERNAL"},
			},
		},
	}}

	route, err := ResolveRoute(models, reg, nil, "aegis-fast", "INTERNAL")
	if err != nil {
		t.Fatalf("no route resolved: %v — the OpenAI fallback should be reachable", err)
	}
	if route.ProviderKey != "openai" {
		t.Errorf("routed to %q, want openai — the uncredentialed primary was selected", route.ProviderKey)
	}
	if route.Model != "gpt-4o-mini" {
		t.Errorf("model = %q, want gpt-4o-mini", route.Model)
	}
}

// TestResolveRoute_FailsClosedWhenNoProviderHasAKey asserts the other
// direction: with nothing credentialed, an alias resolves to no provider rather
// than to one that will 401.
func TestResolveRoute_FailsClosedWhenNoProviderHasAKey(t *testing.T) {
	reg := BuildFromConfig(&config.ProvidersConfig{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {Type: "anthropic", BaseURL: "https://api.anthropic.com/v1"},
			"openai":    {Type: "openai", BaseURL: "https://api.openai.com/v1"},
		},
	})
	models := &config.ModelsConfig{Models: map[string]config.ModelMapping{
		"aegis-fast": {
			Primary:  config.ProviderRoute{Provider: "anthropic", Model: "claude-haiku", ClassificationCeiling: "INTERNAL"},
			Fallback: []config.ProviderRoute{{Provider: "openai", Model: "gpt-4o-mini", ClassificationCeiling: "INTERNAL"}},
		},
	}}
	if _, err := ResolveRoute(models, reg, nil, "aegis-fast", "INTERNAL"); err == nil {
		t.Error("resolved a route with no credentialed provider; the request would 401 upstream")
	}
}
