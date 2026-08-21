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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/cost"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router/adapters"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// stubAdapter satisfies adapters.ProviderAdapter for tests that just need routing
// to succeed so the pricing check can be exercised.
type stubAdapter struct{ name string }

func (s *stubAdapter) Name() string { return s.name }
func (s *stubAdapter) TransformRequest(_ context.Context, req *types.AegisRequest) (*http.Request, error) {
	return http.NewRequest(http.MethodPost, "http://stub.invalid/v1/chat/completions", nil)
}
func (s *stubAdapter) TransformResponse(_ context.Context, _ *http.Response) (*types.AegisResponse, error) {
	return &types.AegisResponse{}, nil
}
func (s *stubAdapter) TransformStreamChunk(chunk []byte) ([]byte, error) { return chunk, nil }
func (s *stubAdapter) SupportsStreaming() bool                           { return false }
func (s *stubAdapter) SendRequest(_ *http.Request) (*http.Response, error) {
	// Should never be called in deny-mode tests; fail loudly if it is.
	return nil, nil
}

// newTestRegistry builds a Registry with a single stubAdapter registered under
// the given provider name.
func newTestRegistry(provider string) *router.Registry {
	reg := router.NewRegistry()
	reg.Register(provider, &stubAdapter{name: provider})
	return reg
}

// TestChatCompletions_UnpricedModel_Deny asserts that when on_missing_pricing is
// "deny", a request routed to a model with no pricing entry is rejected with
// HTTP 402 and the response body contains an auditable error code.
func TestChatCompletions_UnpricedModel_Deny(t *testing.T) {
	const (
		providerName   = "stub-provider"
		routedModel    = "stub-model-v1" // no pricing entry for this model
		requestedAlias = "aegis-stub"
	)

	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{
			Models: map[string]config.ModelMapping{
				requestedAlias: {
					Primary: config.ProviderRoute{
						Provider:              providerName,
						Model:                 routedModel,
						ClassificationCeiling: "RESTRICTED",
					},
				},
			},
		}
	}

	// Pricing config deliberately omits stub-provider/stub-model-v1.
	pricingCfg := func() *config.PricingConfig {
		return &config.PricingConfig{
			Providers: map[string]config.ProviderPricing{
				"openai": {
					Models: map[string]config.PriceEntry{
						"gpt-5.6-sol": {Input: 5.00, Output: 30.00},
					},
				},
			},
		}
	}

	cfg := func() *config.Config {
		return &config.Config{
			Cost: config.CostConfig{
				OnMissingPricing: "deny",
			},
		}
	}

	registry := newTestRegistry(providerName)
	costCalc := cost.NewCalculator(pricingCfg)

	h := NewHandler(registry, nil, modelsCfg, cfg, nil, nil, nil, costCalc, nil, nil, nil, nil, nil)

	reqBody := `{"model": "aegis-stub", "messages": [{"role": "user", "content": "hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-test",
		TeamID:         "team-test",
		KeyID:          "key-test",
	}))

	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "req-deny-test-001")

	h.ChatCompletions(w, req)

	if w.Code != http.StatusPaymentRequired {
		t.Errorf("expected HTTP 402 Payment Required, got %d (body: %s)", w.Code, w.Body.String())
	}

	// Verify the response body carries the auditable error code.
	var errResp struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to parse error response body: %v", err)
	}
	if errResp.Error.Code != "pricing_unknown" {
		t.Errorf("expected error code 'pricing_unknown', got %q", errResp.Error.Code)
	}
	if errResp.Error.Type != "billing_error" {
		t.Errorf("expected error type 'billing_error', got %q", errResp.Error.Type)
	}
}

// TestChatCompletions_UnpricedModel_Flag asserts that when on_missing_pricing is
// "flag", a request with no pricing entry is NOT rejected — it proceeds past the
// pricing gate. In these unit tests the provider call itself will fail (stub
// returns nil response), but we can confirm the 402 is not returned.
func TestChatCompletions_UnpricedModel_Flag(t *testing.T) {
	const (
		providerName   = "stub-provider"
		routedModel    = "stub-model-v1"
		requestedAlias = "aegis-stub"
	)

	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{
			Models: map[string]config.ModelMapping{
				requestedAlias: {
					Primary: config.ProviderRoute{
						Provider:              providerName,
						Model:                 routedModel,
						ClassificationCeiling: "RESTRICTED",
					},
				},
			},
		}
	}

	pricingCfg := func() *config.PricingConfig {
		return &config.PricingConfig{Providers: map[string]config.ProviderPricing{}}
	}

	cfg := func() *config.Config {
		return &config.Config{
			Cost: config.CostConfig{
				OnMissingPricing: "flag",
			},
		}
	}

	registry := newTestRegistry(providerName)
	costCalc := cost.NewCalculator(pricingCfg)

	h := NewHandler(registry, nil, modelsCfg, cfg, nil, nil, nil, costCalc, nil, nil, nil, nil, nil)

	reqBody := `{"model": "aegis-stub", "messages": [{"role": "user", "content": "hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-test",
		TeamID:         "team-test",
		KeyID:          "key-test",
	}))

	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "req-flag-test-001")

	h.ChatCompletions(w, req)

	// Flag mode must NOT deny with 402.
	if w.Code == http.StatusPaymentRequired {
		t.Errorf("flag mode must not return HTTP 402, but got %d", w.Code)
	}
}

// TestChatCompletions_PricedModel_NoDeny asserts that a model with a valid
// pricing entry is not rejected by the pricing gate.
func TestChatCompletions_PricedModel_NoDeny(t *testing.T) {
	const (
		providerName   = "openai"
		routedModel    = "gpt-5.6-sol"
		requestedAlias = "aegis-gpt"
	)

	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{
			Models: map[string]config.ModelMapping{
				requestedAlias: {
					Primary: config.ProviderRoute{
						Provider:              providerName,
						Model:                 routedModel,
						ClassificationCeiling: "RESTRICTED",
					},
				},
			},
		}
	}

	pricingCfg := func() *config.PricingConfig {
		return &config.PricingConfig{
			Providers: map[string]config.ProviderPricing{
				"openai": {
					Models: map[string]config.PriceEntry{
						"gpt-5.6-sol": {Input: 5.00, Output: 30.00},
					},
				},
			},
		}
	}

	cfg := func() *config.Config {
		return &config.Config{
			Cost: config.CostConfig{
				OnMissingPricing: "deny",
			},
		}
	}

	registry := newTestRegistry(providerName)
	costCalc := cost.NewCalculator(pricingCfg)

	h := NewHandler(registry, nil, modelsCfg, cfg, nil, nil, nil, costCalc, nil, nil, nil, nil, nil)

	reqBody := `{"model": "aegis-gpt", "messages": [{"role": "user", "content": "hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-test",
		TeamID:         "team-test",
		KeyID:          "key-test",
	}))

	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "req-priced-test-001")

	h.ChatCompletions(w, req)

	// A priced model must not be rejected with 402 by the pricing gate.
	// (The request will fail later because the stub returns nil, but that's expected.)
	if w.Code == http.StatusPaymentRequired {
		t.Errorf("priced model must not return HTTP 402, got %d (body: %s)", w.Code, w.Body.String())
	}
}

// Compile-time check: stubAdapter must satisfy the ProviderAdapter interface.
var _ adapters.ProviderAdapter = (*stubAdapter)(nil)

// spyAuditLogger records pricing-denial audit calls so tests can assert the
// decision reached the audit trail, not just the process log.
type spyAuditLogger struct {
	pricingDenied []struct{ Provider, Model, Mode string }
}

func (s *spyAuditLogger) LogFilterBlock(_, _, _, _, _, _ string, _ string) {}
func (s *spyAuditLogger) LogPricingDenied(_, _, _, _, provider, model, mode string, _ string) {
	s.pricingDenied = append(s.pricingDenied, struct{ Provider, Model, Mode string }{provider, model, mode})
}

// newPricingTestHandler wires a handler whose only routed alias points at
// providerKey/routedModel, with pricing supplied by pricingCfg.
func newPricingTestHandler(providerKey, routedModel, mode string,
	pricing *config.PricingConfig, spy AuditLogger) *Handler {

	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{Models: map[string]config.ModelMapping{
			"aegis-stub": {Primary: config.ProviderRoute{
				Provider: providerKey, Model: routedModel, ClassificationCeiling: "RESTRICTED",
			}},
		}}
	}
	cfg := func() *config.Config {
		return &config.Config{Cost: config.CostConfig{OnMissingPricing: mode}}
	}
	return NewHandler(newTestRegistry(providerKey), nil, modelsCfg, cfg, nil, nil, nil,
		cost.NewCalculator(func() *config.PricingConfig { return pricing }), nil, spy, nil, nil, nil)
}

func doPricingRequest(t *testing.T, h *Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"aegis-stub","messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test",
	}))
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "req-pricing-test")
	h.ChatCompletions(w, req)
	return w
}

// TestPricingGate_UnknownModeFailsClosed asserts that a typo'd or unsupported
// on_missing_pricing value is treated as deny. Previously anything that was not
// exactly "deny" behaved like "flag", so a single misconfigured character
// silently reopened the unpriced-traffic bypass.
func TestPricingGate_UnknownModeFailsClosed(t *testing.T) {
	empty := &config.PricingConfig{Providers: map[string]config.ProviderPricing{}}
	for _, mode := range []string{"Deny", "DENY", "warn", "off", "flagg", "  deny"} {
		t.Run(mode, func(t *testing.T) {
			w := doPricingRequest(t, newPricingTestHandler("stub-provider", "stub-model", mode, empty, nil))
			if w.Code != http.StatusPaymentRequired {
				t.Errorf("mode %q: expected 402 (fail closed), got %d", mode, w.Code)
			}
		})
	}
}

// TestPricingGate_LooksUpByConfiguredProviderKey asserts pricing is resolved by
// the provider key from models.yaml, not by the adapter type. azure_openai and
// internal_vllm are both served by the OpenAI adapter, whose Name() returns
// "openai" — so keying off the adapter looked up the wrong pricing row and
// denied a correctly priced route.
func TestPricingGate_LooksUpByConfiguredProviderKey(t *testing.T) {
	// Priced under the configured key, absent under the adapter type.
	pricing := &config.PricingConfig{Providers: map[string]config.ProviderPricing{
		"internal_vllm": {Models: map[string]config.PriceEntry{
			"llama-70b": {Input: 1.0, Output: 2.0},
		}},
	}}
	// The stub must mirror reality: registered under the configured key, but
	// reporting the *adapter type* from Name(), exactly as OpenAIAdapter does
	// when it serves internal_vllm. A stub whose Name() echoes the registered
	// key cannot reproduce this bug at all.
	reg := router.NewRegistry()
	reg.Register("internal_vllm", &stubAdapter{name: "openai"})

	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{Models: map[string]config.ModelMapping{
			"aegis-stub": {Primary: config.ProviderRoute{
				Provider: "internal_vllm", Model: "llama-70b", ClassificationCeiling: "RESTRICTED",
			}},
		}}
	}
	cfg := func() *config.Config {
		return &config.Config{Cost: config.CostConfig{OnMissingPricing: "deny"}}
	}
	spy := &spyAuditLogger{}
	h := NewHandler(reg, nil, modelsCfg, cfg, nil, nil, nil,
		cost.NewCalculator(func() *config.PricingConfig { return pricing }), nil, spy, nil, nil, nil)
	w := doPricingRequest(t, h)
	if w.Code == http.StatusPaymentRequired {
		t.Errorf("priced route denied with 402: pricing was looked up by adapter type instead of the configured provider key")
	}
	if len(spy.pricingDenied) != 0 {
		t.Errorf("priced route produced a pricing-denial audit event: %+v", spy.pricingDenied)
	}
}

// TestPricingGate_DenialIsAudited asserts the decision is persisted, not merely
// logged. Both deny and flag are governance events and must survive log
// rotation in the audit trail.
func TestPricingGate_DenialIsAudited(t *testing.T) {
	empty := &config.PricingConfig{Providers: map[string]config.ProviderPricing{}}
	for _, mode := range []string{"deny", "flag"} {
		t.Run(mode, func(t *testing.T) {
			spy := &spyAuditLogger{}
			doPricingRequest(t, newPricingTestHandler("stub-provider", "stub-model", mode, empty, spy))
			if len(spy.pricingDenied) != 1 {
				t.Fatalf("mode %q: expected 1 audited pricing decision, got %d", mode, len(spy.pricingDenied))
			}
			got := spy.pricingDenied[0]
			if got.Provider != "stub-provider" || got.Model != "stub-model" || got.Mode != mode {
				t.Errorf("mode %q: audited %+v, want provider=stub-provider model=stub-model mode=%s", mode, got, mode)
			}
		})
	}
}

// TestPricingGate_ZeroRatedEntryIsNotPricing asserts that a pricing row which
// exists but carries no usable rates — a misspelled YAML field unmarshals to
// zero — is treated as missing. A presence-only check passed such a row while
// recording zero spend for every request.
func TestPricingGate_ZeroRatedEntryIsNotPricing(t *testing.T) {
	pricing := &config.PricingConfig{Providers: map[string]config.ProviderPricing{
		"stub-provider": {Models: map[string]config.PriceEntry{
			"stub-model": {}, // key present, every rate zero
		}},
	}}
	w := doPricingRequest(t, newPricingTestHandler("stub-provider", "stub-model", "deny", pricing, nil))
	if w.Code != http.StatusPaymentRequired {
		t.Errorf("expected 402 for a zero-rated pricing entry, got %d", w.Code)
	}
}
