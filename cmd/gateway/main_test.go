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

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router"
)

func TestGenerateRequestID_Format(t *testing.T) {
	id := generateRequestID()
	if !strings.HasPrefix(id, "req_") {
		t.Errorf("expected req_ prefix, got %s", id)
	}
	// Format: req_{unix_millis}_{hex}
	parts := strings.SplitN(id, "_", 3)
	if len(parts) != 3 {
		t.Errorf("expected 3 parts (req, timestamp, hex), got %d in %s", len(parts), id)
	}
	// Hex part should be 16 chars (8 bytes)
	if len(parts[2]) != 16 {
		t.Errorf("expected 16 hex chars, got %d in %s", len(parts[2]), id)
	}
}

func TestGenerateRequestID_Uniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := generateRequestID()
		if seen[id] {
			t.Fatalf("duplicate request ID: %s", id)
		}
		seen[id] = true
	}
}

func TestRequestIDMiddleware_GeneratesID(t *testing.T) {
	var capturedID string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = w.Header().Get("X-Request-ID")
		w.WriteHeader(http.StatusOK)
	})

	handler := requestIDMiddleware(inner)
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if capturedID == "" {
		t.Error("expected X-Request-ID to be generated")
	}
	if !strings.HasPrefix(capturedID, "req_") {
		t.Errorf("expected req_ prefix, got %s", capturedID)
	}
}

func TestRequestIDMiddleware_PreservesExisting(t *testing.T) {
	var capturedID string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = w.Header().Get("X-Request-ID")
		w.WriteHeader(http.StatusOK)
	})

	handler := requestIDMiddleware(inner)
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Request-ID", "custom-id-123")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if capturedID != "custom-id-123" {
		t.Errorf("expected preserved ID custom-id-123, got %s", capturedID)
	}
}

func TestRequestIDMiddleware_SetsContext(t *testing.T) {
	var ctxVal interface{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxVal = r.Context().Value(requestIDKey)
		w.WriteHeader(http.StatusOK)
	})

	handler := requestIDMiddleware(inner)
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Request-ID", "ctx-test-id")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if ctxVal == nil || ctxVal.(string) != "ctx-test-id" {
		t.Errorf("expected context value ctx-test-id, got %v", ctxVal)
	}
}

func TestMakeHealthHandler_NilDependencies(t *testing.T) {
	handler := makeHealthHandler(nil, nil, nil, nil, nil)
	req := httptest.NewRequest("GET", "/aegis/v1/health", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp healthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "healthy" {
		t.Errorf("expected status healthy, got %s", resp.Status)
	}
	if resp.Version != version {
		t.Errorf("expected version %s, got %s", version, resp.Version)
	}
	if resp.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
	if resp.Database != nil {
		t.Error("expected nil database when pool is nil")
	}
	if resp.Redis != nil {
		t.Error("expected nil redis when client is nil")
	}
}

func TestMakeHealthHandler_ContentType(t *testing.T) {
	handler := makeHealthHandler(nil, nil, nil, nil, nil)
	req := httptest.NewRequest("GET", "/aegis/v1/health", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}
}

// TestMakeHealthHandler_ReportsMockProvider covers the requirement that a
// gateway answering from the mock says so on the health endpoint. Somebody
// evaluating this project needs to be able to tell a real provider call from a
// canned one without reading the process environment, and "did that response
// come from a model?" is exactly the question a mock makes hard to answer.
func TestMakeHealthHandler_ReportsMockProvider(t *testing.T) {
	provCfg := &config.ProvidersConfig{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {Type: "anthropic", BaseURL: "https://api.anthropic.com/v1"},
			"openai":    {Type: "openai", BaseURL: "https://api.openai.com/v1"},
		},
	}

	t.Run("mock active", func(t *testing.T) {
		t.Setenv(router.MockProviderEnvVar, "true")
		resp := healthWithRegistry(t, router.BuildFromConfig(provCfg))

		if !resp.MockProvider {
			t.Error("mock_provider is false while every provider is served by the mock")
		}
		// Per provider as well as in aggregate, so a partially mocked gateway
		// could not report itself as fully real.
		for name, status := range resp.Providers.Details {
			if status.Adapter != "mock" {
				t.Errorf("provider %q reports adapter %q, want mock", name, status.Adapter)
			}
		}
	})

	t.Run("real providers", func(t *testing.T) {
		t.Setenv(router.MockProviderEnvVar, "")
		resp := healthWithRegistry(t, router.BuildFromConfig(provCfg))

		if resp.MockProvider {
			t.Error("mock_provider is true without the opt-in set")
		}
		if got := resp.Providers.Details["anthropic"].Adapter; got != "anthropic" {
			t.Errorf("anthropic reports adapter %q, want anthropic", got)
		}
	})
}

func healthWithRegistry(t *testing.T, registry *router.Registry) healthResponse {
	t.Helper()

	tracker := router.NewHealthTracker(5, time.Second)
	handler := makeHealthHandler(nil, nil, nil, registry, tracker)
	req := httptest.NewRequest("GET", "/aegis/v1/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var resp healthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if resp.Providers == nil {
		t.Fatal("health response carries no providers block")
	}
	return resp
}
