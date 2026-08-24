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

package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
)

// newAuditRequest builds a GET with an auth context, as the middleware would.
func newAuditRequest(t *testing.T, target string, info *auth.AuthInfo) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if info != nil {
		r = r.WithContext(auth.ContextWithAuth(r.Context(), info))
	}
	return r
}

func orgAuth() *auth.AuthInfo {
	return &auth.AuthInfo{KeyID: "key_1", OrganizationID: "org_1", TeamID: "team_1"}
}

// A nil reader means the gateway is running without a database. The endpoint
// must say so rather than panicking on a nil dereference.
func TestAuditHandler_NoDatabase(t *testing.T) {
	h := NewAuditHandler(nil)
	w := httptest.NewRecorder()
	h.Events(w, newAuditRequest(t, "/aegis/v1/audit/events", orgAuth()))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 with no database, got %d", w.Code)
	}
}

func TestAuditHandler_Unauthenticated(t *testing.T) {
	h := NewAuditHandler(nil)
	w := httptest.NewRecorder()
	h.Events(w, newAuditRequest(t, "/aegis/v1/audit/events", nil))

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without an auth context, got %d", w.Code)
	}
}

// A key with no organization cannot be scoped. Serving it an unscoped query
// would hand one tenant another tenant's audit trail, so it is refused.
func TestAuditHandler_RefusesUnscopedKey(t *testing.T) {
	h := NewAuditHandler(nil)
	w := httptest.NewRecorder()
	h.Events(w, newAuditRequest(t, "/aegis/v1/audit/events",
		&auth.AuthInfo{KeyID: "key_1", OrganizationID: ""}))

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for a key with no organization, got %d", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	errObj, _ := body["error"].(map[string]any)
	if msg, _ := errObj["message"].(string); msg == "" {
		t.Error("expected an explanatory message on the refusal")
	}
}

// Parameter validation runs before the database is touched, so these cases are
// exercised with a nil reader and must still return 400 rather than 503.
func TestAuditHandler_RejectsBadParameters(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{"unknown format", "/aegis/v1/audit/events?format=xml"},
		{"from not RFC3339", "/aegis/v1/audit/events?from=yesterday"},
		{"to not RFC3339", "/aegis/v1/audit/events?to=2026-13-45"},
		{"limit not a number", "/aegis/v1/audit/events?limit=lots"},
		{"limit zero", "/aegis/v1/audit/events?limit=0"},
		{"limit negative", "/aegis/v1/audit/events?limit=-5"},
		{"limit over maximum", "/aegis/v1/audit/events?limit=100000"},
		{"before_id not a number", "/aegis/v1/audit/events?before_id=abc"},
		{"before_id negative", "/aegis/v1/audit/events?before_id=-1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewAuditHandler(nil)
			w := httptest.NewRecorder()
			h.Events(w, newAuditRequest(t, tc.target, orgAuth()))

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 for %s, got %d (body: %s)",
					tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// Valid parameters must pass validation and reach the reader, which with a nil
// reader surfaces as 503. This is the negative control for the test above: it
// proves those 400s come from the parameter in question and not from the
// handler rejecting everything.
func TestAuditHandler_AcceptsValidParameters(t *testing.T) {
	targets := []string{
		"/aegis/v1/audit/events",
		"/aegis/v1/audit/events?format=csv",
		"/aegis/v1/audit/events?format=json&limit=50",
		"/aegis/v1/audit/events?from=2026-08-01T00:00:00Z&to=2026-08-23T00:00:00Z",
		"/aegis/v1/audit/events?event_type=filter_block&request_id=req_1",
		"/aegis/v1/audit/events?before_id=42&limit=1000",
	}

	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			h := NewAuditHandler(nil)
			w := httptest.NewRecorder()
			h.Events(w, newAuditRequest(t, target, orgAuth()))

			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("expected valid parameters to reach the reader (503 with no db), got %d: %s",
					w.Code, w.Body.String())
			}
		})
	}
}

func TestAuditHandler_LogsSharesParameterHandling(t *testing.T) {
	h := NewAuditHandler(nil)
	w := httptest.NewRecorder()
	h.Logs(w, newAuditRequest(t, "/aegis/v1/audit/logs?format=xml", orgAuth()))

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected /logs to validate format too, got %d", w.Code)
	}
}

// A key whose organization is the sentinel recorded for pre-authentication
// events must be refused, not scoped.
//
// audit.LogAuthFailure records authentication failures under
// audit.UnattributedOrg, because at that point there is no caller to attribute
// them to. Those rows carry the truncated key prefix, the IP and the user agent
// for every failed attempt in the deployment. Scoping by plain equality would
// hand all of them to anyone holding a key issued with that organization, and
// cmd/keygen takes -org as a free string, so that key is one typo away from
// existing.
func TestAuditHandler_RefusesSentinelOrg(t *testing.T) {
	for _, path := range []string{"/aegis/v1/audit/events", "/aegis/v1/audit/logs"} {
		t.Run(path, func(t *testing.T) {
			h := NewAuditHandler(nil)
			w := httptest.NewRecorder()
			req := newAuditRequest(t, path,
				&auth.AuthInfo{KeyID: "key_1", OrganizationID: audit.UnattributedOrg})

			if path == "/aegis/v1/audit/events" {
				h.Events(w, req)
			} else {
				h.Logs(w, req)
			}

			if w.Code != http.StatusUnauthorized {
				t.Errorf("a key scoped to the unattributed sentinel got %d, want 401; "+
					"it would otherwise read every tenant's authentication failures", w.Code)
			}
		})
	}
}
