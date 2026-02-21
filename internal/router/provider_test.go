package router

import (
	"context"
	"net/http"
	"testing"

	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/types"
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

	_, _, err := ResolveRoute(cfg, registry, "nonexistent", "INTERNAL")
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

	adapter, model, err := ResolveRoute(cfg, registry, "gpt-4o", "INTERNAL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", model)
	}
	if adapter.Name() != "openai" {
		t.Errorf("expected adapter openai, got %s", adapter.Name())
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
	adapter, model, err := ResolveRoute(cfg, registry, "test-model", "RESTRICTED")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adapter.Name() != "internal_vllm" {
		t.Errorf("expected internal_vllm (fallback), got %s", adapter.Name())
	}
	if model != "llama-70b" {
		t.Errorf("expected llama-70b, got %s", model)
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
	_, _, err := ResolveRoute(cfg, registry, "test-model", "RESTRICTED")
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
	adapter, _, err := ResolveRoute(cfg, registry, "test-model", "CONFIDENTIAL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adapter.Name() != "openai" {
		t.Errorf("expected openai, got %s", adapter.Name())
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
	_, _, err := ResolveRoute(cfg, registry, "test-model", "RESTRICTED")
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

	adapter, model, err := ResolveRoute(cfg, registry, "test-model", "INTERNAL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adapter.Name() != "provider-c" {
		t.Errorf("expected provider-c, got %s", adapter.Name())
	}
	if model != "model-c" {
		t.Errorf("expected model-c, got %s", model)
	}
}
