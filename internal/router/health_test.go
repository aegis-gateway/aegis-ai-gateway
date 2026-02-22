package router

import (
	"testing"
	"time"

	"github.com/af-corp/aegis-gateway/internal/config"
)

func TestHealthTracker_LazyCreation(t *testing.T) {
	ht := NewHealthTracker(3, 5*time.Second)
	if !ht.IsAvailable("openai") {
		t.Error("expected new provider to be available")
	}
}

func TestHealthTracker_RecordFailureOpensCircuit(t *testing.T) {
	ht := NewHealthTracker(2, 5*time.Second)

	ht.RecordFailure("openai")
	ht.RecordFailure("openai")

	if ht.IsAvailable("openai") {
		t.Error("expected openai to be unavailable after 2 failures")
	}
}

func TestHealthTracker_RecordSuccessCloses(t *testing.T) {
	ht := NewHealthTracker(1, 10*time.Millisecond)

	ht.RecordFailure("openai")
	if ht.IsAvailable("openai") {
		t.Error("expected openai to be unavailable")
	}

	time.Sleep(15 * time.Millisecond)

	// After probe interval, should be half-open and allow one
	if !ht.IsAvailable("openai") {
		t.Error("expected openai to be available (half-open probe)")
	}

	ht.RecordSuccess("openai")
	if !ht.IsAvailable("openai") {
		t.Error("expected openai to be available after success")
	}
}

func TestHealthTracker_IndependentProviders(t *testing.T) {
	ht := NewHealthTracker(1, 5*time.Second)

	ht.RecordFailure("openai")

	if ht.IsAvailable("openai") {
		t.Error("expected openai to be unavailable")
	}
	if !ht.IsAvailable("anthropic") {
		t.Error("expected anthropic to be available (independent)")
	}
}

func TestResolveRoute_SkipsUnhealthyProvider(t *testing.T) {
	registry := newTestRegistry("openai", "anthropic")
	ht := NewHealthTracker(1, 5*time.Second)

	cfg := modelsCfgWith(map[string]config.ModelMapping{
		"test-model": {
			Primary: config.ProviderRoute{
				Provider:              "openai",
				Model:                 "gpt-4o",
				ClassificationCeiling: "CONFIDENTIAL",
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

	// Mark openai as unhealthy
	ht.RecordFailure("openai")

	adapter, model, err := ResolveRoute(cfg, registry, ht, "test-model", "INTERNAL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adapter.Name() != "anthropic" {
		t.Errorf("expected anthropic (fallback), got %s", adapter.Name())
	}
	if model != "claude-sonnet" {
		t.Errorf("expected claude-sonnet, got %s", model)
	}
}

func TestResolveRoute_AllUnhealthy_ReturnsError(t *testing.T) {
	registry := newTestRegistry("openai", "anthropic")
	ht := NewHealthTracker(1, 5*time.Second)

	cfg := modelsCfgWith(map[string]config.ModelMapping{
		"test-model": {
			Primary: config.ProviderRoute{
				Provider:              "openai",
				Model:                 "gpt-4o",
				ClassificationCeiling: "CONFIDENTIAL",
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

	ht.RecordFailure("openai")
	ht.RecordFailure("anthropic")

	_, _, err := ResolveRoute(cfg, registry, ht, "test-model", "INTERNAL")
	if err == nil {
		t.Fatal("expected error when all providers are unhealthy")
	}
}
