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
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
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

	route, err := ResolveRoute(cfg, registry, ht, "test-model", "INTERNAL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.ProviderKey != "anthropic" {
		t.Errorf("expected anthropic (fallback), got %s", route.ProviderKey)
	}
	if route.Model != "claude-sonnet" {
		t.Errorf("expected claude-sonnet, got %s", route.Model)
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

	_, err := ResolveRoute(cfg, registry, ht, "test-model", "INTERNAL")
	if err == nil {
		t.Fatal("expected error when all providers are unhealthy")
	}
}
