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
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router/adapters"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/validation"
)

// modelDeniedRecord is one LogModelDenied call, captured.
type modelDeniedRecord struct {
	RequestID, OrgID, TeamID, KeyID, Model string
	StatusCode                             int
}

// providerFailureRecord is one LogProviderFailure call, captured.
type providerFailureRecord struct {
	Event audit.CompletionEvent
	Stage string
}

// allowlistAudit records every audit call the handler makes. It is shared with
// the completion tests, which need the same handler wiring.
type allowlistAudit struct {
	denied    []modelDeniedRecord
	completed []audit.CompletionEvent
	failed    []providerFailureRecord
}

func (a *allowlistAudit) LogFilterBlock(_, _, _, _, _, _ string, _ string)      {}
func (a *allowlistAudit) LogPricingDenied(_, _, _, _, _, _, _ string, _ string) {}
func (a *allowlistAudit) LogModelDenied(requestID, orgID, teamID, keyID, model string, statusCode int, _ string) {
	a.denied = append(a.denied, modelDeniedRecord{requestID, orgID, teamID, keyID, model, statusCode})
}
func (a *allowlistAudit) LogRequestComplete(ev audit.CompletionEvent) {
	a.completed = append(a.completed, ev)
}
func (a *allowlistAudit) LogProviderFailure(ev audit.CompletionEvent, stage string) {
	a.failed = append(a.failed, providerFailureRecord{ev, stage})
}

// allowlistAliases are the aliases the test handler routes. aegis-gpt4 mirrors
// the deprecated alias in configs/models.yaml: it carries its own route rather
// than redirecting, because config.ModelMapping.DeprecatedAlias is read by no
// code. A key allowlisted for aegis-balanced therefore may not use aegis-gpt4,
// and this test pins that.
var allowlistAliases = []string{"aegis-fast", "aegis-balanced", "aegis-reasoning", "aegis-gpt4"}

// newAllowlistHandler wires a handler with every alias in allowlistAliases
// routed to a mock adapter, no filters, and no pricing gate, so the only thing
// that can refuse a request is the allowlist check.
func newAllowlistHandler(auditLogger AuditLogger) *Handler {
	return newAllowlistHandlerWithAdapter(auditLogger,
		adapters.NewMockAdapter("test-provider", config.ProviderConfig{}))
}

// newAllowlistHandlerWithAdapter is newAllowlistHandler with the provider
// adapter chosen by the caller, so the provider-failure paths can be driven.
func newAllowlistHandlerWithAdapter(auditLogger AuditLogger, adapter adapters.ProviderAdapter) *Handler {
	reg := router.NewRegistry()
	reg.Register("test-provider", adapter)

	models := map[string]config.ModelMapping{}
	for _, alias := range allowlistAliases {
		models[alias] = config.ModelMapping{Primary: config.ProviderRoute{
			Provider: "test-provider", Model: "test-model", ClassificationCeiling: "RESTRICTED",
		}}
	}
	modelsCfg := func() *config.ModelsConfig { return &config.ModelsConfig{Models: models} }
	cfg := func() *config.Config {
		return &config.Config{Cost: config.CostConfig{OnMissingPricing: "allow"}}
	}
	return NewHandler(reg, nil, modelsCfg, cfg, nil, nil, nil, nil, nil, auditLogger,
		nil, nil, validation.NewValidator(validation.DefaultLimits(), nil))
}

func postAs(t *testing.T, h *Handler, info *auth.AuthInfo, model string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req = req.WithContext(auth.ContextWithAuth(req.Context(), info))
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "req-allowlist-test")
	h.ChatCompletions(w, req)
	return w
}

func authWith(allowed []string) *auth.AuthInfo {
	return &auth.AuthInfo{
		KeyID:             "key-test",
		OrganizationID:    "org-test",
		TeamID:            "team-test",
		MaxClassification: types.ClassRestricted,
		AllowedModels:     allowed,
	}
}

// TestChatCompletions_ModelAllowlist is the regression test for a key being
// served a model its allowlist does not carry.
//
// Before this, AllowedModels was consulted only by ListModels, so a key
// restricted to aegis-fast was listed one alias and served any of them.
func TestChatCompletions_ModelAllowlist(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		allowed   []string
		requested string
		wantAllow bool
	}{
		{
			// Empty means unrestricted. cmd/keygen writes an empty JSON array
			// for every key it issues, so reading this as deny-all would
			// revoke every key in existence.
			name: "empty allowlist permits every alias", allowed: nil,
			requested: "aegis-reasoning", wantAllow: true,
		},
		{
			name: "empty non-nil allowlist permits every alias", allowed: []string{},
			requested: "aegis-reasoning", wantAllow: true,
		},
		{
			name: "allowlist containing the requested alias", allowed: []string{"aegis-fast"},
			requested: "aegis-fast", wantAllow: true,
		},
		{
			name: "allowlist excluding the requested alias", allowed: []string{"aegis-fast"},
			requested: "aegis-reasoning", wantAllow: false,
		},
		{
			name: "one of several permitted aliases", allowed: []string{"aegis-fast", "aegis-balanced"},
			requested: "aegis-balanced", wantAllow: true,
		},
		{
			// The deprecated alias is a distinct name, not a synonym for the
			// alias it names. Permitting aegis-balanced does not permit it.
			name: "deprecated alias is not implied by its replacement", allowed: []string{"aegis-balanced"},
			requested: "aegis-gpt4", wantAllow: false,
		},
		{
			name: "deprecated alias permitted explicitly", allowed: []string{"aegis-gpt4"},
			requested: "aegis-gpt4", wantAllow: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spy := &allowlistAudit{}
			w := postAs(t, newAllowlistHandler(spy), authWith(tc.allowed), tc.requested)

			if tc.wantAllow {
				if w.Code != http.StatusOK {
					t.Fatalf("expected the request to be served, got HTTP %d: %s", w.Code, w.Body.String())
				}
				if len(spy.denied) != 0 {
					t.Errorf("a permitted request wrote %d model-denied audit event(s)", len(spy.denied))
				}
				return
			}

			// The refusal shape is the classification-ceiling refusal's:
			// 503, server_error, service_unavailable.
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected HTTP %d for a model outside the allowlist, got %d: %s",
					http.StatusServiceUnavailable, w.Code, w.Body.String())
			}
			var body struct {
				Error struct {
					Type    string `json:"type"`
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("refusal body is not the OpenAI error envelope: %v", err)
			}
			if body.Error.Type != "server_error" || body.Error.Code != "service_unavailable" {
				t.Errorf("refusal envelope is %q/%q, want server_error/service_unavailable; "+
					"it must not be distinguishable from the classification-ceiling refusal",
					body.Error.Type, body.Error.Code)
			}

			// The denial is attested, with the caller's real organization
			// rather than the unattributed sentinel.
			if len(spy.denied) != 1 {
				t.Fatalf("expected exactly 1 model-denied audit event, got %d", len(spy.denied))
			}
			got := spy.denied[0]
			if got.OrgID != "org-test" || got.TeamID != "team-test" || got.KeyID != "key-test" {
				t.Errorf("audit event carries org=%q team=%q key=%q, want the authenticated identity",
					got.OrgID, got.TeamID, got.KeyID)
			}
			if got.OrgID == audit.UnattributedOrg {
				t.Errorf("audit event recorded the unattributed sentinel; the organization would " +
					"never see this denial in its own audit export")
			}
			if got.Model != tc.requested {
				t.Errorf("audit event records model %q, want the requested alias %q", got.Model, tc.requested)
			}
			if got.StatusCode != http.StatusServiceUnavailable {
				t.Errorf("audit event records status %d, want %d to match what the caller was sent",
					got.StatusCode, http.StatusServiceUnavailable)
			}
		})
	}
}

// TestModelAllowlist_ListAndCompletionAgree asserts the two call sites cannot
// disagree: every alias ListModels advertises must be one ChatCompletions
// serves, and every alias it withholds must be one ChatCompletions refuses.
func TestModelAllowlist_ListAndCompletionAgree(t *testing.T) {
	t.Parallel()

	for _, allowed := range [][]string{
		nil,
		{},
		{"aegis-fast"},
		{"aegis-fast", "aegis-gpt4"},
		{"not-a-configured-alias"},
	} {
		info := authWith(allowed)

		// What the listing advertises.
		h := newAllowlistHandler(&allowlistAudit{})
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req = req.WithContext(auth.ContextWithAuth(req.Context(), info))
		lw := httptest.NewRecorder()
		h.ListModels(lw, req)

		var listed struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(lw.Body.Bytes(), &listed); err != nil {
			t.Fatalf("decode /v1/models: %v", err)
		}
		advertised := map[string]bool{}
		for _, m := range listed.Data {
			advertised[m.ID] = true
		}

		// What the completion path actually serves.
		for _, alias := range allowlistAliases {
			w := postAs(t, newAllowlistHandler(&allowlistAudit{}), info, alias)
			served := w.Code == http.StatusOK
			if served != advertised[alias] {
				t.Errorf("allowlist %v: /v1/models advertises %s=%v but /v1/chat/completions serves it=%v",
					allowed, alias, advertised[alias], served)
			}
		}
	}
}

// TestChatCompletions_UnconfiguredAliasIsNotAttested is the regression test for
// a payload channel found in review on PR #67.
//
// DecodeChatCompletion accepts any string as a model, and the allowlist check
// deliberately runs before ResolveRoute, so nothing upstream had rejected the
// value before it was written to audit_events.model. That column is covered by
// the leaf hash and sealed into the Merkle chain, so a caller holding a
// restricted key could put arbitrary text into the attested record permanently.
//
// The fix is that the allowlist is enforced only on an alias that is a key of
// modelsCfg.Models. An unconfigured alias falls through to ResolveRoute and is
// refused as an unknown model, writing no audit row, which is what happens for
// every key today.
func TestChatCompletions_UnconfiguredAliasIsNotAttested(t *testing.T) {
	t.Parallel()

	// Charset-legal, because the validator restricts a model name to
	// [a-zA-Z0-9._:-] and 256 characters before this code runs. That bounds the
	// exposure and does not remove it: audit_events.model is VARCHAR(128) and
	// is clipped to it, so the channel was 128 characters of that alphabet into
	// a column the leaf hash covers. A canary containing spaces would be
	// refused at validation and would prove nothing about this path.
	const canary = "CANARY_MODEL_PAYLOAD_9e1f4c.and-a-good-deal-of-caller-text:not-a-model-name"

	for _, tc := range []struct {
		name    string
		allowed []string
	}{
		{"restricted key", []string{"aegis-fast"}},
		{"unrestricted key", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spy := &allowlistAudit{}
			w := postAs(t, newAllowlistHandler(spy), authWith(tc.allowed), canary)

			// Refused either way: an unconfigured alias has no route.
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("got HTTP %d for an unconfigured alias, want 503: %s", w.Code, w.Body.String())
			}

			// Nothing caller-controlled may have been attested. Checking every
			// audit call rather than only the denial list, so a future event
			// added on this path is covered too.
			for _, d := range spy.denied {
				if strings.Contains(d.Model, "CANARY_MODEL_PAYLOAD") {
					t.Errorf("caller-controlled text reached the sealed model column: %q", d.Model)
				}
			}
			for _, ev := range spy.completed {
				if strings.Contains(ev.Model, "CANARY_MODEL_PAYLOAD") {
					t.Errorf("caller-controlled text reached a completion event: %q", ev.Model)
				}
			}
			for _, f := range spy.failed {
				if strings.Contains(f.Event.Model, "CANARY_MODEL_PAYLOAD") {
					t.Errorf("caller-controlled text reached a failure event: %q", f.Event.Model)
				}
			}

			// An unconfigured alias is refused by routing, not by the
			// allowlist, so it writes no denial row at all. Asserting the count
			// keeps the test honest: without it, a fix that recorded the
			// denial with an empty model would also pass the checks above,
			// and that is a different design than the one in the handler.
			if len(spy.denied) != 0 {
				t.Errorf("an unconfigured alias wrote %d model-denied event(s); it should be "+
					"refused by ResolveRoute as an unknown model, writing none", len(spy.denied))
			}
		})
	}
}
