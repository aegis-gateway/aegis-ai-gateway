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

package httputil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, "req_123", http.StatusBadRequest, "invalid_request_error", "bad_request", "test message")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	if rid := w.Header().Get("X-Request-ID"); rid != "req_123" {
		t.Errorf("expected X-Request-ID req_123, got %s", rid)
	}

	var resp APIError
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Error.Message != "test message" {
		t.Errorf("expected message 'test message', got %q", resp.Error.Message)
	}
	if resp.Error.Type != "invalid_request_error" {
		t.Errorf("expected type 'invalid_request_error', got %q", resp.Error.Type)
	}
	if resp.Error.AegisReqID != "req_123" {
		t.Errorf("expected aegis_request_id 'req_123', got %q", resp.Error.AegisReqID)
	}
}

func TestWriteAuthError(t *testing.T) {
	w := httptest.NewRecorder()
	WriteAuthError(w, "req_456", "Invalid key")

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}

	var resp APIError
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error.Code != "invalid_api_key" {
		t.Errorf("expected code 'invalid_api_key', got %q", resp.Error.Code)
	}
}

func TestWriteContentBlockedError(t *testing.T) {
	w := httptest.NewRecorder()
	WriteContentBlockedError(w, "req_789", "Secret detected")

	if w.Code != 451 {
		t.Errorf("expected status 451, got %d", w.Code)
	}
}
