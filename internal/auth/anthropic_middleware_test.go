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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

const anthropicTestKey = "aegis-dev-anthropicheader1234567890123"

func anthropicTestStore() *mockKeyStore {
	return &mockKeyStore{keys: map[string]*KeyMetadata{
		HashKeyV2(anthropicTestKey, testPepper): {
			ID: "key-a", OrganizationID: "org-a", TeamID: "team-a",
			MaxClassification: types.ClassInternal, ExpiresAt: time.Now().Add(time.Hour),
		},
	}}
}

func serveAuth(t *testing.T, mw func(http.Handler) http.Handler, headers map[string]string) (int, *AuthInfo) {
	t.Helper()
	var got *AuthInfo
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = AuthFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code, got
}

func TestAnthropicMiddleware_AcceptsAPIKeyHeader(t *testing.T) {
	code, info := serveAuth(t, AnthropicMiddleware(anthropicTestStore(), nil, testPepper),
		map[string]string{"x-api-key": anthropicTestKey})
	if code != http.StatusOK || info == nil || info.OrganizationID != "org-a" {
		t.Fatalf("code %d, auth %+v", code, info)
	}
}

func TestAnthropicMiddleware_AcceptsBearer(t *testing.T) {
	code, _ := serveAuth(t, AnthropicMiddleware(anthropicTestStore(), nil, testPepper),
		map[string]string{"Authorization": "Bearer " + anthropicTestKey})
	if code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
}

func TestAnthropicMiddleware_AgreeingHeadersAccepted(t *testing.T) {
	code, _ := serveAuth(t, AnthropicMiddleware(anthropicTestStore(), nil, testPepper),
		map[string]string{"x-api-key": anthropicTestKey, "Authorization": "Bearer " + anthropicTestKey})
	if code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
}

// Two different keys on one request would leave the acting identity
// ambiguous. Refused even when one of them is valid.
func TestAnthropicMiddleware_ConflictingHeadersRefused(t *testing.T) {
	code, info := serveAuth(t, AnthropicMiddleware(anthropicTestStore(), nil, testPepper),
		map[string]string{"x-api-key": anthropicTestKey, "Authorization": "Bearer aegis-dev-someotherkey"})
	if code != http.StatusUnauthorized || info != nil {
		t.Fatalf("code %d, auth %+v", code, info)
	}
}

// The OpenAI routes keep their contract: x-api-key is not a credential there.
func TestMiddleware_IgnoresAPIKeyHeader(t *testing.T) {
	code, _ := serveAuth(t, Middleware(anthropicTestStore(), nil, testPepper),
		map[string]string{"x-api-key": anthropicTestKey})
	if code != http.StatusUnauthorized {
		t.Fatalf("code %d, want 401", code)
	}
}
