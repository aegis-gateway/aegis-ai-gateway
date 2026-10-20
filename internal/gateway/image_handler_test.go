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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/cost"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

const handlerPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

// imageAdapter records the request it was asked to send and answers 200.
type imageAdapter struct {
	name string
	mu   sync.Mutex
	sent []*types.AegisRequest
}

func (a *imageAdapter) Name() string { return a.name }
func (a *imageAdapter) TransformRequest(_ context.Context, req *types.AegisRequest) (*http.Request, error) {
	a.mu.Lock()
	a.sent = append(a.sent, req)
	a.mu.Unlock()
	return http.NewRequest(http.MethodPost, "http://stub.invalid/v1/chat/completions", nil)
}
func (a *imageAdapter) TransformResponse(_ context.Context, _ *http.Response) (*types.AegisResponse, error) {
	return &types.AegisResponse{Model: "served"}, nil
}
func (a *imageAdapter) TransformStreamChunk(chunk []byte) ([]byte, error) { return chunk, nil }
func (a *imageAdapter) SupportsStreaming() bool                           { return false }
func (a *imageAdapter) SupportsTools() bool                               { return true }
func (a *imageAdapter) SendRequest(_ *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
}

// imageAuditSpy records the audit calls the image paths make.
type imageAuditSpy struct {
	mu        sync.Mutex
	blocks    []struct{ Filter, Reason string }
	completes []audit.CompletedRequest
}

func (s *imageAuditSpy) LogFilterBlock(_, _, _, _, filterType, reason string, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocks = append(s.blocks, struct{ Filter, Reason string }{filterType, reason})
}
func (s *imageAuditSpy) LogPricingDenied(_, _, _, _, _, _, _ string, _ string) {}
func (s *imageAuditSpy) LogRequestComplete(c audit.CompletedRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completes = append(s.completes, c)
}
func (s *imageAuditSpy) LogProviderFailure(_ audit.CompletedRequest, _ string) {}
func (s *imageAuditSpy) LogModelDenied(_, _, _, _, _, _ string, _ string)      {}

// newImageTestHandler serves two aliases: aegis-local, whose only route opts
// into images, and aegis-cloud, whose route does not.
func newImageTestHandler(t *testing.T, spy AuditLogger, maxBody int64) (*Handler, *imageAdapter, *imageAdapter) {
	t.Helper()
	local := &imageAdapter{name: "jetson_llama"}
	cloud := &imageAdapter{name: "cloud"}
	reg := router.NewRegistry()
	reg.Register("jetson_llama", local)
	reg.Register("cloud", cloud)

	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{Models: map[string]config.ModelMapping{
			"aegis-local": {Primary: config.ProviderRoute{
				Provider: "jetson_llama", Model: "qwen3-vl", ClassificationCeiling: "RESTRICTED",
				UnscannedParts: []string{"image"},
			}},
			"aegis-cloud": {Primary: config.ProviderRoute{
				Provider: "cloud", Model: "big", ClassificationCeiling: "RESTRICTED",
			}},
		}}
	}
	cfg := func() *config.Config {
		return &config.Config{
			Cost:   config.CostConfig{OnMissingPricing: "allow"},
			Server: config.ServerConfig{MaxRequestBytes: maxBody},
		}
	}
	pricing := &config.PricingConfig{}
	h := NewHandler(reg, nil, modelsCfg, cfg, nil, nil, nil,
		cost.NewCalculator(func() *config.PricingConfig { return pricing }), nil, spy, nil, nil, nil)
	return h, local, cloud
}

func imageChatBody(model string) string {
	return `{"model":"` + model + `","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"what is in this picture"},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + handlerPNG + `"}}]}]}`
}

func doImageRequest(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test",
		MaxClassification: types.ClassRestricted,
	}))
	w := httptest.NewRecorder()
	h.ChatCompletions(w, req)
	return w
}

func TestChatCompletions_ImageOnClearedAliasIsForwarded(t *testing.T) {
	spy := &imageAuditSpy{}
	h, local, _ := newImageTestHandler(t, spy, 0)

	w := doImageRequest(t, h, imageChatBody("aegis-local"))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if len(local.sent) != 1 {
		t.Fatalf("provider saw %d requests", len(local.sent))
	}
	if imgs := local.sent[0].Messages[0].Content.ImageParts(); len(imgs) != 1 || imgs[0].Base64Data() != handlerPNG {
		t.Errorf("the image did not reach the provider intact: %v", imgs)
	}
}

// The permanent refusal: no route of the alias is cleared. 451, sealed as a
// block with a fixed reason, and the provider is never called.
func TestChatCompletions_ImageOnUnclearedAliasIsRefusedAndSealed(t *testing.T) {
	spy := &imageAuditSpy{}
	h, _, cloud := newImageTestHandler(t, spy, 0)

	w := doImageRequest(t, h, imageChatBody("aegis-cloud"))
	if w.Code != 451 || !strings.Contains(w.Body.String(), "unscanned_parts_not_cleared") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if len(cloud.sent) != 0 {
		t.Error("an image reached a provider that was never cleared for one")
	}
	if len(spy.blocks) != 1 || spy.blocks[0].Filter != UnscannedPartsFilter || spy.blocks[0].Reason != UnscannedPartsNotClearedReason {
		t.Errorf("audit blocks = %+v, want one sealed unscanned_parts refusal", spy.blocks)
	}
	if strings.Contains(spy.blocks[0].Reason, "aegis-cloud") || strings.Contains(spy.blocks[0].Reason, "picture") {
		t.Error("the sealed reason quotes the request")
	}
}

// A text request to the same uncleared alias is unaffected.
func TestChatCompletions_TextOnUnclearedAliasUnaffected(t *testing.T) {
	h, _, cloud := newImageTestHandler(t, &imageAuditSpy{}, 0)
	w := doImageRequest(t, h, `{"model":"aegis-cloud","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusOK || len(cloud.sent) != 1 {
		t.Fatalf("status %d, provider calls %d: %s", w.Code, len(cloud.sent), w.Body)
	}
}

func TestChatCompletions_RemoteImageURLRefusedAtDecode(t *testing.T) {
	h, local, _ := newImageTestHandler(t, &imageAuditSpy{}, 0)
	body := `{"model":"aegis-local","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"https://example.invalid/a.png?sig=secret"}}]}]}`
	w := doImageRequest(t, h, body)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "unsupported_image_part") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "sig=secret") {
		t.Error("the refusal echoes the URL")
	}
	if len(local.sent) != 0 {
		t.Error("provider was called")
	}
}

func TestChatCompletions_BodyOverCapIs413(t *testing.T) {
	h, local, _ := newImageTestHandler(t, &imageAuditSpy{}, 256)
	w := doImageRequest(t, h, imageChatBody("aegis-local"))
	if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "max_request_bytes") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if len(local.sent) != 0 {
		t.Error("provider was called")
	}
}

// A forwarded image is sealed as metadata: one part, its decoded size, its
// media type. Never the content.
func TestChatCompletions_ImageForwardSealsUnscannedMetadata(t *testing.T) {
	spy := &imageAuditSpy{}
	h, _, _ := newImageTestHandler(t, spy, 0)

	if w := doImageRequest(t, h, imageChatBody("aegis-local")); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if len(spy.completes) != 1 {
		t.Fatalf("got %d completion records", len(spy.completes))
	}
	rec := spy.completes[0]
	if rec.UnscannedParts == nil || *rec.UnscannedParts != 1 {
		t.Errorf("UnscannedParts = %v, want 1", rec.UnscannedParts)
	}
	if rec.UnscannedBytes == nil || *rec.UnscannedBytes != 70 {
		t.Errorf("UnscannedBytes = %v, want 70 (the decoded PNG)", rec.UnscannedBytes)
	}
	if rec.UnscannedPartTypes != "image/png" {
		t.Errorf("UnscannedPartTypes = %q", rec.UnscannedPartTypes)
	}

	// A text request on the same route records nothing, which seals NULL.
	spy.completes = nil
	doImageRequest(t, h, `{"model":"aegis-local","messages":[{"role":"user","content":"hi"}]}`)
	if len(spy.completes) != 1 || spy.completes[0].UnscannedParts != nil || spy.completes[0].UnscannedPartTypes != "" {
		t.Errorf("text request recorded unscanned metadata: %+v", spy.completes)
	}
}

// The decoder admits a media type only if the audit trail can seal it by name.
// Were the two lists to drift, an admitted image would be sealed as
// "(unlisted)", and the record would stop saying what was sent.
func TestImageMediaTypes_MatchAuditAllowlist(t *testing.T) {
	for _, mt := range types.ImageMediaTypes() {
		if !audit.IsUnscannedMediaType(mt) {
			t.Errorf("the decoder admits %s but audit.IsUnscannedMediaType refuses it", mt)
		}
	}
	for _, mt := range []string{"image/svg+xml", "image/bmp", "text/plain"} {
		if audit.IsUnscannedMediaType(mt) {
			t.Errorf("the audit allowlist admits %s, which the decoder refuses", mt)
		}
	}
}
