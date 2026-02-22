package ratelimit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/af-corp/aegis-gateway/internal/auth"
	"github.com/af-corp/aegis-gateway/internal/httputil"
)

func intPtr(v int) *int { return &v }

func TestMiddleware_AllowsRequest(t *testing.T) {
	limiter := NewLimiter(nil)
	budget := NewBudgetTracker(nil)
	mw := Middleware(limiter, budget, nil)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	authInfo := &auth.AuthInfo{
		KeyID:          "key-1",
		OrganizationID: "org-1",
		TeamID:         "team-1",
		RPMLimit:       intPtr(100),
	}
	req = req.WithContext(auth.ContextWithAuth(req.Context(), authInfo))
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", "req-1")

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// Check rate limit headers
	if h := rec.Header().Get(headerRateLimitRequests); h != "100" {
		t.Errorf("expected X-RateLimit-Limit-Requests=100, got %s", h)
	}
	if h := rec.Header().Get(headerRateLimitRemainingRequests); h == "" {
		t.Error("expected X-RateLimit-Remaining-Requests header")
	}
	if h := rec.Header().Get(headerRateLimitReset); h == "" {
		t.Error("expected X-RateLimit-Reset-Requests header")
	}
}

func TestMiddleware_DefaultRPM(t *testing.T) {
	limiter := NewLimiter(nil)
	budget := NewBudgetTracker(nil)
	mw := Middleware(limiter, budget, nil)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	authInfo := &auth.AuthInfo{
		KeyID:          "key-2",
		OrganizationID: "org-1",
		TeamID:         "team-1",
		// RPMLimit is nil — should use default (60)
	}
	req = req.WithContext(auth.ContextWithAuth(req.Context(), authInfo))
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", "req-2")

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if h := rec.Header().Get(headerRateLimitRequests); h != "60" {
		t.Errorf("expected default RPM=60, got %s", h)
	}
}

func TestMiddleware_NoAuth_PassThrough(t *testing.T) {
	limiter := NewLimiter(nil)
	budget := NewBudgetTracker(nil)
	mw := Middleware(limiter, budget, nil)

	called := false
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !called {
		t.Error("expected handler to be called when no auth context")
	}
}

func TestMiddleware_BudgetExceeded(t *testing.T) {
	// With nil Redis, budget always passes. Test that the error format is correct
	// by directly testing WriteBudgetExceededError.
	rec := httptest.NewRecorder()
	httputil.WriteBudgetExceededError(rec, "req-3", "Daily budget exceeded")

	if rec.Code != http.StatusPaymentRequired {
		t.Errorf("expected 402, got %d", rec.Code)
	}

	var apiErr httputil.APIError
	if err := json.NewDecoder(rec.Body).Decode(&apiErr); err != nil {
		t.Fatalf("failed to decode error: %v", err)
	}
	if apiErr.Error.Code != "budget_exceeded" {
		t.Errorf("expected code 'budget_exceeded', got %s", apiErr.Error.Code)
	}
}

func TestMiddleware_RateLimitHeaders_Present(t *testing.T) {
	limiter := NewLimiter(nil)
	budget := NewBudgetTracker(nil)
	mw := Middleware(limiter, budget, nil)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	authInfo := &auth.AuthInfo{
		KeyID:             "key-3",
		OrganizationID:    "org-1",
		TeamID:            "team-1",
		MaxClassification: "PUBLIC",
	}
	req = req.WithContext(auth.ContextWithAuth(req.Context(), authInfo))
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", "req-4")

	handler.ServeHTTP(rec, req)

	headers := []string{headerRateLimitRequests, headerRateLimitRemainingRequests, headerRateLimitReset}
	for _, h := range headers {
		if rec.Header().Get(h) == "" {
			t.Errorf("missing header: %s", h)
		}
	}
}
