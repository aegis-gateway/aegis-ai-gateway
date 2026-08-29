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
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/cost"
)

// modelDenialSpy records LogModelDenied calls so a refusal can be checked for
// its audit row as well as its status.
type modelDenialSpy struct {
	denied []struct{ RequestID, Org, Team, Key, Model string }
}

func (s *modelDenialSpy) LogFilterBlock(_, _, _, _, _, _ string, _ string)      {}
func (s *modelDenialSpy) LogPricingDenied(_, _, _, _, _, _, _ string, _ string) {}
func (s *modelDenialSpy) LogRequestComplete(_ audit.CompletedRequest)           {}
func (s *modelDenialSpy) LogProviderFailure(_ audit.CompletedRequest, _ string) {}
func (s *modelDenialSpy) LogModelDenied(requestID, org, team, key, model string, _ string) {
	s.denied = append(s.denied, struct{ RequestID, Org, Team, Key, Model string }{
		requestID, org, team, key, model})
}

// newAllowlistTestHandler routes every alias in configs/models.yaml order to a
// single stub provider, with pricing present so the request would otherwise
// reach the provider and succeed.
func newAllowlistTestHandler(spy AuditLogger) *Handler {
	route := config.ProviderRoute{
		Provider: "stub-provider", Model: "stub-model", ClassificationCeiling: "RESTRICTED",
	}
	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{Models: map[string]config.ModelMapping{
			"aegis-fast":      {Primary: route},
			"aegis-balanced":  {Primary: route},
			"aegis-reasoning": {Primary: route},
			"aegis-gpt4":      {Primary: route, DeprecatedAlias: "aegis-balanced"},
		}}
	}
	cfg := func() *config.Config {
		return &config.Config{Cost: config.CostConfig{OnMissingPricing: "allow"}}
	}
	pricing := &config.PricingConfig{Providers: map[string]config.ProviderPricing{
		"stub-provider": {Models: map[string]config.PriceEntry{
			"stub-model": {Input: 1, Output: 1},
		}},
	}}
	return NewHandler(newTestRegistry("stub-provider"), nil, modelsCfg, cfg, nil, nil, nil,
		cost.NewCalculator(func() *config.PricingConfig { return pricing }), nil, spy, nil, nil, nil)
}

func doAllowlistRequest(t *testing.T, h *Handler, model string, allowed []string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test",
		AllowedModels: allowed,
	}))
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "req-allowlist-test")
	h.ChatCompletions(w, req)
	return w
}

// TestChatCompletions_EnforcesModelAllowlist covers the gap that made
// AllowedModels advisory: it was read only by ListModels, so a key restricted
// to one alias was served any other and billed for it.
func TestChatCompletions_EnforcesModelAllowlist(t *testing.T) {
	tests := []struct {
		name       string
		allowed    []string
		model      string
		wantDenied bool
	}{
		{
			name:       "empty allowlist permits everything",
			allowed:    nil,
			model:      "aegis-reasoning",
			wantDenied: false,
		},
		{
			name:       "empty non-nil allowlist permits everything",
			allowed:    []string{},
			model:      "aegis-reasoning",
			wantDenied: false,
		},
		{
			name:       "allowlist containing the requested alias permits it",
			allowed:    []string{"aegis-fast", "aegis-balanced"},
			model:      "aegis-fast",
			wantDenied: false,
		},
		{
			name:       "allowlist excluding the requested alias refuses it",
			allowed:    []string{"aegis-fast"},
			model:      "aegis-reasoning",
			wantDenied: true,
		},
		{
			// The defect in the brief, exactly: restricted to aegis-fast,
			// served aegis-reasoning, billed for it.
			name:       "single-model key cannot reach a costlier model",
			allowed:    []string{"aegis-fast"},
			model:      "aegis-balanced",
			wantDenied: true,
		},
		{
			// DeprecatedAlias is declared in config.ModelMapping and read
			// nowhere, so nothing rewrites aegis-gpt4 to aegis-balanced at any
			// point. Allowing the target does not allow the alias, and
			// ListModels shows them as two separate models, so the two views
			// agree. Pinned because a future alias-resolution change would
			// otherwise silently widen every allowlist that names the target.
			name:       "deprecated alias is not covered by allowing its target",
			allowed:    []string{"aegis-balanced"},
			model:      "aegis-gpt4",
			wantDenied: true,
		},
		{
			name:       "deprecated alias is permitted when named explicitly",
			allowed:    []string{"aegis-gpt4"},
			model:      "aegis-gpt4",
			wantDenied: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &modelDenialSpy{}
			w := doAllowlistRequest(t, newAllowlistTestHandler(spy), tt.model, tt.allowed)

			if !tt.wantDenied {
				if w.Code == http.StatusForbidden {
					t.Fatalf("model %q was refused for allowlist %v; body: %s",
						tt.model, tt.allowed, w.Body.String())
				}
				if len(spy.denied) != 0 {
					t.Errorf("a permitted request wrote %d denial audit event(s)", len(spy.denied))
				}
				return
			}

			if w.Code != http.StatusForbidden {
				t.Fatalf("expected 403 for model %q against allowlist %v, got %d: %s",
					tt.model, tt.allowed, w.Code, w.Body.String())
			}

			// Body shape must match every other refusal in the gateway.
			var body struct {
				Error struct {
					Message    string `json:"message"`
					Type       string `json:"type"`
					Code       string `json:"code"`
					AegisReqID string `json:"aegis_request_id"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("refusal body is not the standard error envelope: %v", err)
			}
			if body.Error.Type != "permission_error" || body.Error.Code != "model_not_allowed" {
				t.Errorf("got type=%q code=%q, want permission_error/model_not_allowed",
					body.Error.Type, body.Error.Code)
			}

			// The refusal must be attested, not just returned.
			if len(spy.denied) != 1 {
				t.Fatalf("expected exactly 1 audit event for a refusal, got %d", len(spy.denied))
			}
			got := spy.denied[0]
			if got.Model != tt.model {
				t.Errorf("audit event recorded model %q, want %q", got.Model, tt.model)
			}
			if got.Org != "org-test" || got.Team != "team-test" || got.Key != "key-test" {
				t.Errorf("audit event lost the request's identity: %+v", got)
			}
		})
	}
}

// TestChatCompletions_RefusalHappensBeforeRouting asserts the check runs before
// a provider is selected. A denied request that still resolved a route would
// influence health tracking and could reach a provider.
func TestChatCompletions_RefusalHappensBeforeRouting(t *testing.T) {
	spy := &modelDenialSpy{}
	// No provider is registered under this name, so if the allowlist check did
	// not short-circuit, routing would fail with 503 rather than 403.
	h := newAllowlistTestHandler(spy)
	h.registry = newTestRegistry("some-other-provider")

	w := doAllowlistRequest(t, h, "aegis-reasoning", []string{"aegis-fast"})
	if w.Code != http.StatusForbidden {
		t.Errorf("got %d, want 403: the allowlist must be enforced before routing", w.Code)
	}
}

// TestListModelsAndChatCompletionsAgree pins the property that made one shared
// predicate worth having: anything ListModels advertises to a key must be
// usable by that key, and anything it hides must be refused.
func TestListModelsAndChatCompletionsAgree(t *testing.T) {
	allowed := []string{"aegis-fast", "aegis-gpt4"}
	h := newAllowlistTestHandler(&modelDenialSpy{})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-test", AllowedModels: allowed,
	}))
	lw := httptest.NewRecorder()
	h.ListModels(lw, req)

	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lw.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decoding /v1/models: %v", err)
	}
	visible := map[string]bool{}
	for _, m := range listed.Data {
		visible[m.ID] = true
	}
	if len(visible) != len(allowed) {
		t.Fatalf("ListModels showed %d models, want %d: %v", len(visible), len(allowed), visible)
	}

	for _, model := range []string{"aegis-fast", "aegis-balanced", "aegis-reasoning", "aegis-gpt4"} {
		w := doAllowlistRequest(t, h, model, allowed)
		refused := w.Code == http.StatusForbidden
		if visible[model] && refused {
			t.Errorf("%s is advertised by ListModels but refused by ChatCompletions", model)
		}
		if !visible[model] && !refused {
			t.Errorf("%s is hidden by ListModels but served by ChatCompletions", model)
		}
	}
}

// TestChatCompletions_DeniedUnknownModelIsNotSealedVerbatim is a zero-retention
// test, not an allowlist one.
//
// The model field on an allowlist denial is the one place a caller chooses what
// reaches the sealed audit trail. This check runs before ResolveRoute, and
// validation checks a model name's length and character set rather than whether
// it exists, so any syntactically valid string arrives here. Writing it to
// audit_events.model let any caller holding a key with a non-empty allowlist
// put up to 128 characters of their own text into an immutable, exported
// record: the no-payload contract broken through a field nobody thinks of as
// payload.
//
// Confirmed as a real violation before the fix, with the attacker string
// appearing verbatim in the event.
func TestChatCompletions_DeniedUnknownModelIsNotSealedVerbatim(t *testing.T) {
	const attacker = "CALLER_CONTROLLED_TEXT_THAT_MUST_NEVER_BE_SEALED_0123456789"

	spy := &modelDenialSpy{}
	w := doAllowlistRequest(t, newAllowlistTestHandler(spy), attacker, []string{"aegis-fast"})

	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403: an unconfigured model outside the allowlist is still a denial", w.Code)
	}
	if len(spy.denied) != 1 {
		t.Fatalf("expected exactly 1 denial event, got %d", len(spy.denied))
	}
	if got := spy.denied[0].Model; got != audit.UnconfiguredModel {
		t.Errorf("sealed model field = %q, want %q; caller-controlled text must not "+
			"reach the audit trail", got, audit.UnconfiguredModel)
	}
}

// The sentinel must not swallow real aliases: a denial for a configured model
// still has to name it, or the trail cannot say which model was refused.
func TestChatCompletions_DeniedConfiguredModelIsNamed(t *testing.T) {
	spy := &modelDenialSpy{}
	doAllowlistRequest(t, newAllowlistTestHandler(spy), "aegis-reasoning", []string{"aegis-fast"})

	if len(spy.denied) != 1 {
		t.Fatalf("expected exactly 1 denial event, got %d", len(spy.denied))
	}
	if got := spy.denied[0].Model; got != "aegis-reasoning" {
		t.Errorf("sealed model field = %q, want the configured alias; the sentinel "+
			"must not hide which model was refused", got)
	}
}
