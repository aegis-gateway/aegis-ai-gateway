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
	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
			// Keys are required: BuildFromConfig leaves an uncredentialed
			// provider unregistered, since it cannot serve a request and being
			// registered only made it eligible for routing.
			"anthropic": {Type: "anthropic", BaseURL: "https://api.anthropic.com/v1", APIKey: "test-key"},
			"openai":    {Type: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "test-key"},
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

// requestIDMiddleware must replace an id the downstream sinks cannot store, and
// the two failure modes are different.
//
// Too long: audit_events.request_id is VARCHAR(50) and PostgreSQL rejects an
// over-long value rather than truncating, so the permitted request leaves no
// attested record.
//
// Not valid UTF-8: Go accepts an obs-text byte such as 0xff in an HTTP/1 header.
// The audit path clips, and clip replaces invalid bytes with U+FFFD, so the
// sealed row would carry a different id from the one returned to the caller.
// The usage path does not clip, and PostgreSQL refuses the byte sequence, so the
// spend record is lost outright.
func TestRequestIDMiddleware_ReplacesUnusableIDs(t *testing.T) {
	tests := map[string]struct {
		given   string
		replace bool
	}{
		"ordinary id":       {"req-abc-123", false},
		"exactly the limit": {strings.Repeat("a", 50), false},
		"one over":          {strings.Repeat("a", 51), true},
		"invalid utf-8":     {"req-\xff-123", true},
		"short invalid":     {"\xff", true},
		"absent":            {"", true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var seen string
			h := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = w.Header().Get("X-Request-ID")
			}))

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if tt.given != "" {
				req.Header.Set("X-Request-ID", tt.given)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)

			if seen == "" {
				t.Fatal("no request id was set")
			}
			if !utf8.ValidString(seen) {
				t.Errorf("request id %q is not valid UTF-8; PostgreSQL will refuse it", seen)
			}
			if len(seen) > audit.MaxRequestID {
				t.Errorf("request id is %d bytes, over the %d the column holds", len(seen), audit.MaxRequestID)
			}
			if tt.replace && seen == tt.given {
				t.Errorf("unusable id %q was passed through unchanged", tt.given)
			}
			if !tt.replace && seen != tt.given {
				t.Errorf("usable id %q was replaced with %q; the caller's correlation id must survive",
					tt.given, seen)
			}
		})
	}
}
