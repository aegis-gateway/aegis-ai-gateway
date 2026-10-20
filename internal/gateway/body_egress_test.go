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
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/cost"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/retry"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router/adapters"
)

// A provider_failure record carries the unscanned-part metadata only when
// the request body was actually written to the provider. These tests drive
// the real OpenAI adapter over a real transport, because what is under test
// is what the transport reports, not what a stub claims.

// newEgressTestHandler routes aegis-local, cleared for images, to an OpenAI
// adapter at baseURL over a standard http.Transport.
func newEgressTestHandler(t *testing.T, spy AuditLogger, baseURL string, exec *retry.Executor) *Handler {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{}}
	reg := router.NewRegistry()
	reg.Register("vlm", adapters.NewOpenAIAdapter(config.ProviderConfig{
		Type: "openai", BaseURL: baseURL, APIKey: "test-key",
	}, client))
	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{Models: map[string]config.ModelMapping{
			"aegis-local": {Primary: config.ProviderRoute{
				Provider: "vlm", Model: "qwen3-vl", ClassificationCeiling: "RESTRICTED",
				UnscannedParts: []string{"image"},
			}},
		}}
	}
	cfg := func() *config.Config {
		return &config.Config{Cost: config.CostConfig{OnMissingPricing: "allow"}}
	}
	pricing := &config.PricingConfig{}
	return NewHandler(reg, nil, modelsCfg, cfg, nil, nil, nil,
		cost.NewCalculator(func() *config.PricingConfig { return pricing }), nil, spy, exec, nil, nil)
}

// refusedURL returns a base URL nothing listens on: the connect fails before
// any byte of the request is written.
func refusedURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return "http://" + addr + "/v1"
}

// readsBodyThen500 is a provider that receives the whole body, then fails.
func readsBodyThen500(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Error(w, `{"error":{"message":"boom"}}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// readsBodyThenResets is a provider that receives the whole body, then drops
// the connection without answering.
func readsBodyThenResets(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func streamingImageBody() string {
	return strings.Replace(imageChatBody("aegis-local"), `{"model":"aegis-local",`, `{"model":"aegis-local","stream":true,`, 1)
}

func onlyFailure(t *testing.T, spy *imageAuditSpy) audit.CompletedRequest {
	t.Helper()
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.failures) != 1 || len(spy.completes) != 0 {
		t.Fatalf("got %d provider_failure and %d request_complete records, want exactly one failure",
			len(spy.failures), len(spy.completes))
	}
	return spy.failures[0]
}

func assertUnscannedNull(t *testing.T, rec audit.CompletedRequest) {
	t.Helper()
	if rec.UnscannedParts != nil || rec.UnscannedBytes != nil || rec.UnscannedPartTypes != "" {
		t.Errorf("provider_failure sealed unscanned metadata although no body was written: parts=%v bytes=%v types=%q",
			deref64(rec.UnscannedParts), deref64(rec.UnscannedBytes), rec.UnscannedPartTypes)
	}
}

func assertUnscannedSealed(t *testing.T, rec audit.CompletedRequest) {
	t.Helper()
	if rec.UnscannedParts == nil || *rec.UnscannedParts != 1 ||
		rec.UnscannedBytes == nil || *rec.UnscannedBytes != 70 || rec.UnscannedPartTypes != "image/png" {
		t.Errorf("provider_failure after the body was written: parts=%v bytes=%v types=%q, want 1, 70, image/png",
			deref64(rec.UnscannedParts), deref64(rec.UnscannedBytes), rec.UnscannedPartTypes)
	}
}

func TestBodyEgress_ConnectRefusedSealsNull(t *testing.T) {
	spy := &imageAuditSpy{}
	h := newEgressTestHandler(t, spy, refusedURL(t), nil)
	if w := doImageRequest(t, h, imageChatBody("aegis-local")); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	assertUnscannedNull(t, onlyFailure(t, spy))
}

// Retries do not change it: no attempt connected, so nothing left.
func TestBodyEgress_ConnectRefusedAcrossRetriesSealsNull(t *testing.T) {
	spy := &imageAuditSpy{}
	exec := retry.NewExecutor(retry.Config{MaxRetries: 2, InitialBackoff: time.Millisecond,
		MaxBackoff: time.Millisecond, BackoffMultiplier: 1}, nil)
	h := newEgressTestHandler(t, spy, refusedURL(t), exec)
	if w := doImageRequest(t, h, imageChatBody("aegis-local")); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	assertUnscannedNull(t, onlyFailure(t, spy))
}

func TestBodyEgress_StreamConnectRefusedSealsNull(t *testing.T) {
	spy := &imageAuditSpy{}
	h := newEgressTestHandler(t, spy, refusedURL(t), nil)
	if w := doImageRequest(t, h, streamingImageBody()); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	assertUnscannedNull(t, onlyFailure(t, spy))
}

func TestBodyEgress_ProviderErrorAfterBodySealsMetadata(t *testing.T) {
	spy := &imageAuditSpy{}
	h := newEgressTestHandler(t, spy, readsBodyThen500(t).URL+"/v1", nil)
	if w := doImageRequest(t, h, imageChatBody("aegis-local")); w.Code < 500 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	assertUnscannedSealed(t, onlyFailure(t, spy))
}

// Through the retry executor, which rebuilds the request for every attempt
// from the traced context.
func TestBodyEgress_ProviderErrorAfterBodyAcrossRetriesSealsMetadata(t *testing.T) {
	spy := &imageAuditSpy{}
	exec := retry.NewExecutor(retry.Config{MaxRetries: 1, InitialBackoff: time.Millisecond,
		MaxBackoff: time.Millisecond, BackoffMultiplier: 1}, nil)
	h := newEgressTestHandler(t, spy, readsBodyThen500(t).URL+"/v1", exec)
	if w := doImageRequest(t, h, imageChatBody("aegis-local")); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	assertUnscannedSealed(t, onlyFailure(t, spy))
}

func TestBodyEgress_ResetAfterBodySealsMetadata(t *testing.T) {
	spy := &imageAuditSpy{}
	h := newEgressTestHandler(t, spy, readsBodyThenResets(t).URL+"/v1", nil)
	if w := doImageRequest(t, h, imageChatBody("aegis-local")); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	assertUnscannedSealed(t, onlyFailure(t, spy))
}

func TestBodyEgress_StreamProviderErrorAfterBodySealsMetadata(t *testing.T) {
	spy := &imageAuditSpy{}
	h := newEgressTestHandler(t, spy, readsBodyThen500(t).URL+"/v1", nil)
	if w := doImageRequest(t, h, streamingImageBody()); w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	assertUnscannedSealed(t, onlyFailure(t, spy))
}

func TestBodyEgress_StreamResetAfterBodySealsMetadata(t *testing.T) {
	spy := &imageAuditSpy{}
	h := newEgressTestHandler(t, spy, readsBodyThenResets(t).URL+"/v1", nil)
	if w := doImageRequest(t, h, streamingImageBody()); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	assertUnscannedSealed(t, onlyFailure(t, spy))
}

func deref64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
