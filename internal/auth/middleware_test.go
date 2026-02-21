package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/af-corp/aegis-gateway/internal/types"
)

// mockKeyStore implements KeyStore for testing.
type mockKeyStore struct {
	keys map[string]*KeyMetadata
}

func (m *mockKeyStore) Lookup(ctx context.Context, keyHash string) (*KeyMetadata, error) {
	meta, ok := m.keys[keyHash]
	if !ok {
		return nil, nil
	}
	return meta, nil
}

func TestMiddleware_MissingAuthHeader(t *testing.T) {
	store := &mockKeyStore{keys: make(map[string]*KeyMetadata)}
	mw := Middleware(store)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "test-req")
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestMiddleware_InvalidFormat(t *testing.T) {
	store := &mockKeyStore{keys: make(map[string]*KeyMetadata)}
	mw := Middleware(store)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "test-req")
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestMiddleware_InvalidKey(t *testing.T) {
	store := &mockKeyStore{keys: make(map[string]*KeyMetadata)}
	mw := Middleware(store)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer aegis-prod-invalidkey123")
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "test-req")
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestMiddleware_ValidKey(t *testing.T) {
	rawKey := "aegis-prod-testkey12345678901234567890ab"
	keyHash := HashKey(rawKey)

	store := &mockKeyStore{
		keys: map[string]*KeyMetadata{
			keyHash: {
				ID:                "key-uuid-123",
				OrganizationID:    "org-1",
				TeamID:            "team-1",
				UserID:            "user-1",
				MaxClassification: types.ClassInternal,
				ExpiresAt:         time.Now().Add(24 * time.Hour),
			},
		},
	}

	mw := Middleware(store)
	var gotAuth *AuthInfo

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info, ok := AuthFromContext(r.Context())
		if !ok {
			t.Error("expected auth info in context")
			return
		}
		gotAuth = info
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "test-req")
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	if gotAuth == nil {
		t.Fatal("auth info should be set")
	}
	if gotAuth.OrganizationID != "org-1" {
		t.Errorf("expected org-1, got %s", gotAuth.OrganizationID)
	}
	if gotAuth.TeamID != "team-1" {
		t.Errorf("expected team-1, got %s", gotAuth.TeamID)
	}
}
