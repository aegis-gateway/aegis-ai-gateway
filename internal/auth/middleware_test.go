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

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

const testPepper = "test-pepper-for-middleware-tests-abcdefghij"

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
	mw := Middleware(store, nil, testPepper)

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
	mw := Middleware(store, nil, testPepper)

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
	mw := Middleware(store, nil, testPepper)

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

// TestMiddleware_ValidKey_V1 verifies that legacy SHA-256 keys (hash_version=1)
// still authenticate via the v1 fallback path.
func TestMiddleware_ValidKey_V1(t *testing.T) {
	rawKey := "aegis-prod-testkey12345678901234567890ab"
	v1Hash := HashKey(rawKey)

	store := &mockKeyStore{
		keys: map[string]*KeyMetadata{
			v1Hash: {
				ID:                "key-uuid-123",
				OrganizationID:    "org-1",
				TeamID:            "team-1",
				UserID:            "user-1",
				MaxClassification: types.ClassInternal,
				ExpiresAt:         time.Now().Add(24 * time.Hour),
			},
		},
	}

	mw := Middleware(store, nil, testPepper)
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

// TestMiddleware_ValidKey_V2 verifies that HMAC-SHA256 keys (hash_version=2)
// authenticate via the primary v2 lookup path without hitting the v1 fallback.
func TestMiddleware_ValidKey_V2(t *testing.T) {
	rawKey := "aegis-prod-hmackey1234567890123456789012"
	v2Hash := HashKeyV2(rawKey, testPepper)

	store := &mockKeyStore{
		keys: map[string]*KeyMetadata{
			v2Hash: {
				ID:                "key-uuid-456",
				OrganizationID:    "org-2",
				TeamID:            "team-2",
				MaxClassification: types.ClassInternal,
				ExpiresAt:         time.Now().Add(24 * time.Hour),
			},
		},
	}

	mw := Middleware(store, nil, testPepper)
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
	if gotAuth.OrganizationID != "org-2" {
		t.Errorf("expected org-2, got %s", gotAuth.OrganizationID)
	}
}

// TestMiddleware_ValidKey is kept for backward compatibility with existing test names.
func TestMiddleware_ValidKey(t *testing.T) {
	TestMiddleware_ValidKey_V1(t)
}
