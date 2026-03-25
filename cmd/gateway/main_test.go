package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
