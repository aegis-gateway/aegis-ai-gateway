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

package audit

import (
	"testing"
	"time"
)

func TestEventSerialization(t *testing.T) {
	event := Event{
		RequestID:      "req_123",
		Timestamp:      time.Now(),
		EventType:      EventAuthFailure,
		OrganizationID: "org_test",
		TeamID:         "team_test",
		IPAddress:      "192.168.1.1",
		UserAgent:      "test-agent/1.0",
		Endpoint:       "/v1/chat/completions",
		Method:         "POST",
		StatusCode:     401,
		ErrorMessage:   "invalid api key",
		APIKeyPrefix:   strPtr("sk-test..."),
		Reason:         strPtr("key not found"),
	}

	if event.EventType != EventAuthFailure {
		t.Errorf("expected EventAuthFailure, got %s", event.EventType)
	}
	if event.StatusCode != 401 {
		t.Errorf("expected status 401, got %d", event.StatusCode)
	}
}

func TestTruncateAPIKey(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"sk-test1234567890abcdef", "sk-test1..."},
		{"short", "short"},
		{"exactly8", "exactly8"},
		{"", ""},
	}

	for _, tt := range tests {
		result := truncateAPIKey(tt.input)
		if result != tt.expected {
			t.Errorf("truncateAPIKey(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestLoggerCreation(t *testing.T) {
	// Nil pool should not panic
	logger := NewLogger(nil)
	if logger == nil {
		t.Error("NewLogger returned nil")
	}

	// Logging with nil pool should not panic
	logger.Log(Event{
		RequestID: "test",
		EventType: EventAuthFailure,
	})
}

func TestAuditEventTypes(t *testing.T) {
	eventTypes := []EventType{
		EventAuthFailure,
		EventAuthSuccess,
		EventRateLimitViolation,
		EventBudgetViolation,
		EventFilterBlock,
		EventRedisFailure,
		EventProviderFailure,
		EventRequestComplete,
	}

	for _, et := range eventTypes {
		if string(et) == "" {
			t.Errorf("event type should not be empty")
		}
	}
}
