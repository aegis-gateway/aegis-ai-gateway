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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/filter"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/filter/injection"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/filter/secrets"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router/adapters"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/validation"
)

const providerSecretKey = "sk-ant-provider-credential"

// fakeAnthropic stands in for api.anthropic.com and records what it was sent.
type fakeAnthropic struct {
	mu       sync.Mutex
	paths    []string
	headers  []http.Header
	bodies   [][]byte
	status   int
	body     string
	sse      bool
	srv      *httptest.Server
	extraHdr map[string]string
	// eventGap, when set, sends an SSE body one event at a time with this
	// pause between events, as a slow model turn would.
	eventGap time.Duration
}

func newFakeAnthropic(t *testing.T) *fakeAnthropic {
	f := &fakeAnthropic{status: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.headers = append(f.headers, r.Header.Clone())
		f.bodies = append(f.bodies, b)
		status, body, sse, gap := f.status, f.body, f.sse, f.eventGap
		f.mu.Unlock()
		for k, v := range f.extraHdr {
			w.Header().Set(k, v)
		}
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		if sse && gap > 0 {
			for _, ev := range strings.SplitAfter(body, "\n\n") {
				_, _ = io.WriteString(w, ev)
				w.(http.Flusher).Flush()
				time.Sleep(gap)
			}
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAnthropic) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

type messagesAuditSpy struct {
	mu        sync.Mutex
	blocks    []string
	completes []audit.CompletedRequest
	failures  []string
	failRecs  []audit.CompletedRequest
}

func (s *messagesAuditSpy) LogFilterBlock(_, _, _, _, filterType, _ string, _ audit.Source) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocks = append(s.blocks, filterType)
}
func (s *messagesAuditSpy) LogPricingDenied(_, _, _, _, _, _, _ string, _ audit.Source) {}
func (s *messagesAuditSpy) LogRequestComplete(c audit.CompletedRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completes = append(s.completes, c)
}
func (s *messagesAuditSpy) LogProviderFailure(c audit.CompletedRequest, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, reason)
	s.failRecs = append(s.failRecs, c)
}
func (s *messagesAuditSpy) LogModelDenied(_, _, _, _, _, _ string, _ audit.Source)   {}
func (s *messagesAuditSpy) LogFeatureDenied(_, _, _, _, _, _ string, _ audit.Source) {}

func newMessagesHandler(t *testing.T, f *fakeAnthropic, spy AuditLogger) *Handler {
	t.Helper()
	return newMessagesHandlerWithClient(t, f, spy, f.srv.Client())
}

func newMessagesHandlerWithClient(t *testing.T, f *fakeAnthropic, spy AuditLogger, client *http.Client) *Handler {
	t.Helper()
	reg := router.NewRegistry()
	reg.Register("anthropic", adapters.NewAnthropicAdapter(config.ProviderConfig{
		BaseURL: f.srv.URL + "/v1", APIKey: providerSecretKey,
	}, client))
	reg.Register("openai", adapters.NewOpenAIAdapter(config.ProviderConfig{BaseURL: "http://openai.invalid/v1"}, nil))

	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{Models: map[string]config.ModelMapping{
			"aegis-claude": {Primary: config.ProviderRoute{
				Provider: "anthropic", Model: "claude-haiku-4-5-20251001", ClassificationCeiling: "RESTRICTED",
			}},
			"aegis-gpt": {Primary: config.ProviderRoute{
				Provider: "openai", Model: "gpt-x", ClassificationCeiling: "RESTRICTED",
			}},
			// The primary is OpenAI and the fallback Anthropic: /v1/messages
			// must skip the primary rather than translate for it.
			"aegis-mixed": {
				Primary:  config.ProviderRoute{Provider: "openai", Model: "gpt-x"},
				Fallback: []config.ProviderRoute{{Provider: "anthropic", Model: "claude-haiku-4-5-20251001"}},
			},
			// Routed to an Anthropic provider that providers.yaml lists but
			// that is not registered, as when its API key is unset.
			"aegis-claude-nokey": {Primary: config.ProviderRoute{
				Provider: "anthropic_nokey", Model: "claude-haiku-4-5-20251001",
			}},
			"aegis-claude-nokey-image": {Primary: config.ProviderRoute{
				Provider: "anthropic_nokey", Model: "claude-haiku-4-5-20251001", UnscannedParts: []string{"image"},
			}},
			// Cleared for images only on the OpenAI route, which /v1/messages
			// can never use.
			"aegis-mixed-image": {
				Primary: config.ProviderRoute{Provider: "openai", Model: "gpt-x", UnscannedParts: []string{"image"}},
				Fallback: []config.ProviderRoute{{
					Provider: "anthropic", Model: "claude-haiku-4-5-20251001", ClassificationCeiling: "RESTRICTED",
				}},
			},
		}}
	}
	cfg := func() *config.Config {
		c := config.DefaultConfig()
		c.Cost.OnMissingPricing = "allow"
		return c
	}
	chain := filter.NewChain(
		secrets.NewFilter(func() bool { return true }),
		injection.NewScanner(func() config.InjectionFilterConfig {
			return config.InjectionFilterConfig{Enabled: true, BlockThreshold: 0.8, FlagThreshold: 0.5}
		}),
	)
	h := NewHandler(reg, nil, modelsCfg, cfg, chain, nil, nil, nil, nil, spy,
		nil, nil, validation.NewValidator(validation.DefaultLimits(), nil))
	h.SetProvidersConfig(func() *config.ProvidersConfig { return messagesProviders })
	return h
}

// messagesProviders is providers.yaml for the handler above. anthropic_nokey
// is configured but never registered.
var messagesProviders = &config.ProvidersConfig{Providers: map[string]config.ProviderConfig{
	"anthropic":       {Type: "anthropic"},
	"openai":          {Type: "openai"},
	"anthropic_nokey": {Type: "anthropic"},
}}

func postMessages(t *testing.T, h *Handler, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("anthropic-version", "2023-06-01")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test",
		MaxClassification: types.ClassInternal,
	}))
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "req-messages-test")
	if strings.HasPrefix(path, "/v1/messages/count_tokens") {
		h.CountTokens(w, req)
	} else {
		h.Messages(w, req)
	}
	return w
}

const claudeCodeTurn = `{"model":"aegis-claude","max_tokens":1024,"stream":true,
 "thinking":{"type":"adaptive"},"output_config":{"effort":"high"},
 "system":[{"type":"text","text":"You are a coding agent.","cache_control":{"type":"ephemeral"}}],
 "tools":[{"name":"Bash","description":"run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],
 "messages":[
  {"role":"user","content":"list the files"},
  {"role":"assistant","content":[
    {"type":"thinking","thinking":"I should run ls & check < output >","signature":"EqQBCkYIBxgCKkA="},
    {"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"ls --color"}}]},
  {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"\u001b[34msrc\u001b[0m  README.md"}]}
 ]}`

const anthropicSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[],"usage":{"input_tokens":100,"cache_read_input_tokens":2000,"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_5m_input_tokens":300,"ephemeral_1h_input_tokens":0},"output_tokens":1}}}` + "\n\n" +
	"event: ping\ndata: {\"type\":\"ping\"}\n\n" +
	"event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"src and README.md"}}` + "\n\n" +
	"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}` + "\n\n" +
	"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"

// The main path: a Claude Code turn, streamed, forwarded natively.
func TestMessages_ClaudeCodeTurnForwardedNativelyAndStreamRelayedVerbatim(t *testing.T) {
	f := newFakeAnthropic(t)
	f.sse, f.body = true, anthropicSSE
	spy := &messagesAuditSpy{}
	h := newMessagesHandler(t, f, spy)

	w := postMessages(t, h, "/v1/messages?beta=true", claudeCodeTurn, map[string]string{
		"anthropic-beta": "claude-code-20250219,interleaved-thinking-2025-05-14",
		"x-api-key":      "aegis-dev-client-key",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if w.Body.String() != anthropicSSE {
		t.Errorf("the stream was not relayed byte for byte:\n got %q\nwant %q", w.Body.String(), anthropicSSE)
	}
	if f.calls() != 1 {
		t.Fatalf("provider calls = %d", f.calls())
	}

	// What reached the provider.
	if f.paths[0] != "/v1/messages" {
		t.Errorf("provider path %q", f.paths[0])
	}
	hdr := f.headers[0]
	if hdr.Get("x-api-key") != providerSecretKey {
		t.Errorf("the provider received x-api-key %q; the client's AEGIS key must never be forwarded", hdr.Get("x-api-key"))
	}
	if hdr.Get("anthropic-beta") != "claude-code-20250219,interleaved-thinking-2025-05-14" ||
		hdr.Get("anthropic-version") != "2023-06-01" {
		t.Errorf("headers: beta %q version %q", hdr.Get("anthropic-beta"), hdr.Get("anthropic-version"))
	}
	var sent, orig map[string]any
	_ = json.Unmarshal(f.bodies[0], &sent)
	_ = json.Unmarshal([]byte(claudeCodeTurn), &orig)
	if sent["model"] != "claude-haiku-4-5-20251001" {
		t.Errorf("model forwarded as %v", sent["model"])
	}
	sent["model"], orig["model"] = nil, nil
	a, _ := json.Marshal(orig)
	b, _ := json.Marshal(sent)
	if string(a) != string(b) {
		t.Errorf("the forwarded body differs beyond model:\n got %s\nwant %s", b, a)
	}

	// What was sealed: one completion, with the cache-inclusive prompt count.
	if len(spy.completes) != 1 || len(spy.failures) != 0 {
		t.Fatalf("completes %d failures %v", len(spy.completes), spy.failures)
	}
	rec := spy.completes[0]
	if rec.PromptTokens == nil || *rec.PromptTokens != 2400 || rec.CompletionTokens == nil || *rec.CompletionTokens != 12 {
		t.Errorf("sealed tokens prompt=%v completion=%v, want 2400 (100+2000+300) and 12", rec.PromptTokens, rec.CompletionTokens)
	}
	if rec.ModelServed != "claude-haiku-4-5-20251001" || rec.RequestedModel != "aegis-claude" || !rec.Stream {
		t.Errorf("sealed record %+v", rec)
	}
}

func TestMessages_BufferedResponseRelayed(t *testing.T) {
	f := newFakeAnthropic(t)
	f.body = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2}}`
	spy := &messagesAuditSpy{}
	h := newMessagesHandler(t, f, spy)

	w := postMessages(t, h, "/v1/messages", `{"model":"aegis-claude","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if w.Code != http.StatusOK || w.Body.String() != f.body {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if len(spy.completes) != 1 || *spy.completes[0].TotalTokens != 7 {
		t.Errorf("completes %+v", spy.completes)
	}
}

// A provider error is relayed with its status, so the SDK's own retry on 429
// and 529 works, and it is sealed as a provider failure.
func TestMessages_ProviderErrorRelayedWithStatus(t *testing.T) {
	f := newFakeAnthropic(t)
	f.status = http.StatusTooManyRequests
	f.body = `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`
	f.extraHdr = map[string]string{"Retry-After": "7"}
	spy := &messagesAuditSpy{}
	h := newMessagesHandler(t, f, spy)

	w := postMessages(t, h, "/v1/messages", `{"model":"aegis-claude","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if w.Code != http.StatusTooManyRequests || w.Body.String() != f.body || w.Header().Get("Retry-After") != "7" {
		t.Fatalf("status %d retry-after %q body %s", w.Code, w.Header().Get("Retry-After"), w.Body)
	}
	if len(spy.failures) != 1 || spy.failures[0] != audit.ReasonProviderError || spy.failRecs[0].StatusCode != 429 {
		t.Errorf("failures %v", spy.failures)
	}
}

// A stream the provider ends without message_stop is not a completion.
// The provider client's Timeout (providers.yaml timeout, 30s as shipped)
// covers the whole exchange. A stream that outlives it must still complete:
// for a stream it bounds only the wait for headers.
func TestMessages_StreamOutlivesProviderClientTimeout(t *testing.T) {
	f := newFakeAnthropic(t)
	f.sse, f.body = true, anthropicSSE
	f.eventGap = 40 * time.Millisecond // seven events, about 280ms in all
	spy := &messagesAuditSpy{}
	client := f.srv.Client()
	client.Timeout = 100 * time.Millisecond
	h := newMessagesHandlerWithClient(t, f, spy, client)

	w := postMessages(t, h, "/v1/messages", claudeCodeTurn, nil)
	if w.Code != http.StatusOK || w.Body.String() != anthropicSSE {
		t.Fatalf("status %d, stream cut short:\n%s", w.Code, w.Body)
	}
	if len(spy.completes) != 1 || len(spy.failures) != 0 {
		t.Errorf("completes %d failures %v", len(spy.completes), spy.failures)
	}
}

// A refused anthropic-beta value is caller text no filter has seen, so it is
// returned to the caller but kept out of the log.
func TestMessages_RefusedBetaValueNotLogged(t *testing.T) {
	logs := captureLogs(t)
	f := newFakeAnthropic(t)
	h := newMessagesHandler(t, f, &messagesAuditSpy{})
	const marker = "sk-ant-api03-LEAKED-IN-A-HEADER"

	w := postMessages(t, h, "/v1/messages", `{"model":"aegis-claude","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"anthropic-beta": marker})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "unsupported_beta") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(logs.String(), "anthropic-beta not in the allowlist") {
		t.Fatalf("refusal not logged: %s", logs)
	}
	if strings.Contains(logs.String(), marker) {
		t.Errorf("the refused beta value reached the log: %s", logs)
	}
}

func TestMessages_TruncatedStreamSealedAsFailure(t *testing.T) {
	f := newFakeAnthropic(t)
	f.sse = true
	f.body = anthropicSSE[:strings.Index(anthropicSSE, "event: message_delta")]
	spy := &messagesAuditSpy{}
	h := newMessagesHandler(t, f, spy)

	postMessages(t, h, "/v1/messages", claudeCodeTurn, nil)
	if len(spy.completes) != 0 || len(spy.failures) != 1 || spy.failures[0] != audit.ReasonStreamTruncated {
		t.Errorf("completes %d failures %v", len(spy.completes), spy.failures)
	}
}

// The filter chain reads the projection, so a credential in a file the agent
// read is blocked before anything is forwarded.
func TestMessages_SecretInToolResultBlockedAndNotForwarded(t *testing.T) {
	f := newFakeAnthropic(t)
	spy := &messagesAuditSpy{}
	h := newMessagesHandler(t, f, spy)

	body := strings.Replace(claudeCodeTurn, `README.md"`, `aws_key=AKIAIOSFODNN7EXAMPLE"`, 1)
	w := postMessages(t, h, "/v1/messages", body, nil)
	if w.Code != 451 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if f.calls() != 0 {
		t.Error("a request carrying a credential reached the provider")
	}
	if len(spy.blocks) != 1 || spy.blocks[0] != "secrets" {
		t.Errorf("sealed blocks %v", spy.blocks)
	}
	// The refusal parses as an Anthropic error.
	var env struct {
		Type  string `json:"type"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || env.Type != "error" || env.Error.Message == "" {
		t.Errorf("refusal is not Anthropic-shaped: %s", w.Body)
	}
}

func TestMessages_RefusalsBeforeDispatch(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		body    string
		headers map[string]string
		status  int
		code    string
	}{
		{"unlisted beta", "/v1/messages", `{"model":"aegis-claude","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`,
			map[string]string{"anthropic-beta": "claude-code-20250219,files-api-2025-04-14"}, 400, "unsupported_beta"},
		{"unknown version", "/v1/messages", `{"model":"aegis-claude","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`,
			map[string]string{"anthropic-version": "2099-01-01"}, 400, "unsupported_anthropic_version"},
		{"unknown query", "/v1/messages?debug=1", `{"model":"aegis-claude","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`,
			nil, 400, "unsupported_query_parameter"},
		{"OpenAI-only alias", "/v1/messages", `{"model":"aegis-gpt","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`,
			nil, 400, "model_not_anthropic"},
		{"document block", "/v1/messages", `{"model":"aegis-claude","max_tokens":1,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"text","media_type":"text/plain","data":"x"}}]}]}`,
			nil, 400, "unsupported_field"},
		{"url image", "/v1/messages", `{"model":"aegis-claude","max_tokens":1,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://x"}}]}]}`,
			nil, 400, "unsupported_image_part"},
		{"image on uncleared alias", "/v1/messages", `{"model":"aegis-claude","max_tokens":1,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + handlerPNG + `"}}]}]}`,
			nil, 451, "unscanned_parts_not_cleared"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeAnthropic(t)
			h := newMessagesHandler(t, f, &messagesAuditSpy{})
			w := postMessages(t, h, c.path, c.body, c.headers)
			if w.Code != c.status || !strings.Contains(w.Body.String(), c.code) {
				t.Fatalf("status %d body %s", w.Code, w.Body)
			}
			if f.calls() != 0 {
				t.Error("provider was called")
			}
		})
	}
}

// Clearance for an unscanned kind counts only on a route /v1/messages can
// use. An alias cleared for images on its OpenAI route alone is refused with
// the same sealed 451 as an alias cleared nowhere, not a retryable 503.
func TestMessages_ImageClearedOnlyOnNonNativeRouteIsSealed451(t *testing.T) {
	f := newFakeAnthropic(t)
	spy := &messagesAuditSpy{}
	h := newMessagesHandler(t, f, spy)
	body := `{"model":"aegis-mixed-image","max_tokens":1,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + handlerPNG + `"}}]}]}`

	w := postMessages(t, h, "/v1/messages", body, nil)
	if w.Code != 451 || !strings.Contains(w.Body.String(), "unscanned_parts_not_cleared") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if f.calls() != 0 {
		t.Error("provider was called")
	}
	if len(spy.blocks) != 1 || spy.blocks[0] != UnscannedPartsFilter {
		t.Errorf("sealed blocks %v, want one %s block", spy.blocks, UnscannedPartsFilter)
	}
}

// Whether a route is Anthropic is read from providers.yaml, not the live
// registry. An Anthropic route left unregistered (no API key) is an outage:
// the retryable 503 from routing, with nothing sealed as uncleared and no
// model_not_anthropic.
func TestMessages_UnregisteredAnthropicRouteIsOutageNotRefusal(t *testing.T) {
	cases := []struct {
		name, body string
	}{
		{"text, not model_not_anthropic", `{"model":"aegis-claude-nokey","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`},
		{"cleared image, not unscanned_parts_not_cleared", `{"model":"aegis-claude-nokey-image","max_tokens":1,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + handlerPNG + `"}}]}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeAnthropic(t)
			spy := &messagesAuditSpy{}
			h := newMessagesHandler(t, f, spy)
			w := postMessages(t, h, "/v1/messages", c.body, nil)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status %d, want the routing 503: %s", w.Code, w.Body)
			}
			if len(spy.blocks) != 0 {
				t.Errorf("sealed blocks %v, want none", spy.blocks)
			}
			if f.calls() != 0 {
				t.Error("provider was called")
			}
		})
	}
}

// An alias whose primary is OpenAI is served by its Anthropic fallback, and
// never translated for the primary.
func TestMessages_SkipsNonNativePrimary(t *testing.T) {
	f := newFakeAnthropic(t)
	f.body = `{"type":"message","model":"claude-haiku-4-5-20251001","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`
	h := newMessagesHandler(t, f, &messagesAuditSpy{})
	w := postMessages(t, h, "/v1/messages", `{"model":"aegis-mixed","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if w.Code != http.StatusOK || f.calls() != 1 {
		t.Fatalf("status %d calls %d: %s", w.Code, f.calls(), w.Body)
	}
}

func TestCountTokens_GovernedForwardedAndNotPriced(t *testing.T) {
	f := newFakeAnthropic(t)
	f.body = `{"input_tokens":42}`
	spy := &messagesAuditSpy{}
	h := newMessagesHandler(t, f, spy)

	w := postMessages(t, h, "/v1/messages/count_tokens", `{"model":"aegis-claude","messages":[{"role":"user","content":"hi"}]}`, nil)
	if w.Code != http.StatusOK || w.Body.String() != `{"input_tokens":42}` {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if f.paths[0] != "/v1/messages/count_tokens" {
		t.Errorf("path %q", f.paths[0])
	}
	if len(spy.completes) != 1 || spy.completes[0].PromptTokens != nil {
		t.Errorf("count_tokens record %+v: attested, with no token outcome", spy.completes)
	}

	// And it is filtered: the whole prompt goes to the provider.
	w = postMessages(t, h, "/v1/messages/count_tokens", `{"model":"aegis-claude","messages":[{"role":"user","content":"key AKIAIOSFODNN7EXAMPLE"}]}`, nil)
	if w.Code != 451 || f.calls() != 1 {
		t.Errorf("count_tokens with a credential: status %d, provider calls %d", w.Code, f.calls())
	}
}

// AEGIS_MOCK_PROVIDER replaces every provider with the mock; /v1/messages has
// to work there too, so the demo stack and the integration tests can drive it
// with no credential.
func TestMessages_MockProviderAnswersNatively(t *testing.T) {
	reg := router.NewRegistry()
	reg.Register("anthropic", adapters.NewMockAdapter("anthropic", config.ProviderConfig{}))
	modelsCfg := func() *config.ModelsConfig {
		return &config.ModelsConfig{Models: map[string]config.ModelMapping{
			"aegis-claude": {Primary: config.ProviderRoute{Provider: "anthropic", Model: "claude-haiku-4-5-20251001"}},
		}}
	}
	cfg := func() *config.Config { c := config.DefaultConfig(); c.Cost.OnMissingPricing = "allow"; return c }
	spy := &messagesAuditSpy{}
	h := NewHandler(reg, nil, modelsCfg, cfg, nil, nil, nil, nil, nil, spy, nil, nil, nil)
	// As under AEGIS_MOCK_PROVIDER=true: providers.yaml keeps its real types
	// and every provider is the mock, which is native.
	t.Setenv(router.MockProviderEnvVar, "true")
	h.SetProvidersConfig(func() *config.ProvidersConfig {
		return &config.ProvidersConfig{Providers: map[string]config.ProviderConfig{"anthropic": {Type: "anthropic"}}}
	})

	w := postMessages(t, h, "/v1/messages", `{"model":"aegis-claude","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "event: message_stop") {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if len(spy.completes) != 1 {
		t.Errorf("completes %d failures %v", len(spy.completes), spy.failures)
	}
}
