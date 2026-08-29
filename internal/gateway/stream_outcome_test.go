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
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// outcomeSpy records which audit event a streamed request produced.
type outcomeSpy struct {
	completes []audit.CompletedRequest
	failures  []struct {
		Req    audit.CompletedRequest
		Reason string
	}
}

func (o *outcomeSpy) LogFilterBlock(_, _, _, _, _, _ string, _ string)      {}
func (o *outcomeSpy) LogPricingDenied(_, _, _, _, _, _, _ string, _ string) {}
func (o *outcomeSpy) LogModelDenied(_, _, _, _, _ string, _ string)         {}
func (o *outcomeSpy) LogRequestComplete(req audit.CompletedRequest) {
	o.completes = append(o.completes, req)
}
func (o *outcomeSpy) LogProviderFailure(req audit.CompletedRequest, reason string) {
	o.failures = append(o.failures, struct {
		Req    audit.CompletedRequest
		Reason string
	}{req, reason})
}

// scriptedStreamAdapter replays a fixed SSE body with HTTP 200.
type scriptedStreamAdapter struct {
	body string
	// anthropicStyle makes TransformStreamChunk render the provider's
	// message_stop as [DONE], which is what the real Anthropic transformer
	// does. The raw line never contains [DONE] in that case.
	anthropicStyle bool
}

func (a *scriptedStreamAdapter) Name() string { return "openai" }
func (a *scriptedStreamAdapter) TransformRequest(ctx context.Context, req *types.AegisRequest) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodPost, "http://provider.invalid/v1/chat/completions", nil)
}
func (a *scriptedStreamAdapter) SendRequest(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(a.body)),
		Header:     make(http.Header),
	}, nil
}
func (a *scriptedStreamAdapter) TransformResponse(ctx context.Context, resp *http.Response) (*types.AegisResponse, error) {
	return nil, nil
}
func (a *scriptedStreamAdapter) TransformStreamChunk(chunk []byte) ([]byte, error) {
	if a.anthropicStyle && strings.Contains(string(chunk), "message_stop") {
		return []byte("[DONE]"), nil
	}
	return chunk, nil
}
func (a *scriptedStreamAdapter) SupportsStreaming() bool { return true }
func (a *scriptedStreamAdapter) SupportsTools() bool     { return false }

func runStream(t *testing.T, adapter *scriptedStreamAdapter) *outcomeSpy {
	t.Helper()

	spy := &outcomeSpy{}
	h := newAllowlistTestHandler(spy)
	sh := NewStreamingHandler(h, StreamingConfig{
		TotalTimeout:    5 * time.Second,
		PerChunkTimeout: 2 * time.Second,
		BufferSize:      4096,
		MaxBufferSize:   1 << 20,
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &auth.AuthInfo{OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test"}
	req = req.WithContext(auth.ContextWithAuth(req.Context(), info))
	providerReq, err := adapter.TransformRequest(req.Context(), &types.AegisRequest{})
	if err != nil {
		t.Fatalf("building provider request: %v", err)
	}

	sh.HandleStream(httptest.NewRecorder(), req, "req-outcome-test", providerReq, adapter,
		"anthropic", "aegis-fast", info, &types.AegisRequest{Model: "aegis-fast"})
	return spy
}

// A provider that closes cleanly WITHOUT its end-of-stream marker has delivered
// a truncated answer. bufio.Scanner reports EOF with a nil error, so nothing
// distinguishes it from a finished stream except the marker itself, and the
// request would otherwise be sealed as request_complete.
func TestStream_TruncatedWithoutTerminatorIsNotACompletion(t *testing.T) {
	spy := runStream(t, &scriptedStreamAdapter{
		body: "data: {\"choices\":[{\"delta\":{\"content\":\"par\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"tial\"}}]}\n\n",
	})

	if len(spy.completes) != 0 {
		t.Errorf("a truncated stream was attested as complete: %+v", spy.completes)
	}
	if len(spy.failures) != 1 {
		t.Fatalf("expected exactly 1 failure event, got %d", len(spy.failures))
	}
	if got := spy.failures[0].Reason; got != audit.ReasonStreamTruncated {
		t.Errorf("reason = %q, want %q", got, audit.ReasonStreamTruncated)
	}
}

func TestStream_OpenAITerminatorIsACompletion(t *testing.T) {
	spy := runStream(t, &scriptedStreamAdapter{
		body: "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
	})

	if len(spy.failures) != 0 {
		t.Errorf("a completed stream was attested as a failure: %+v", spy.failures)
	}
	if len(spy.completes) != 1 {
		t.Fatalf("expected exactly 1 completion event, got %d", len(spy.completes))
	}
}

// The Anthropic terminator is message_stop, which only becomes [DONE] after
// transformation. The loop used to test the RAW line for [DONE], so this case
// never matched and every Anthropic stream ended at EOF instead, which now
// means "truncated". Without this test the terminator fix would silently
// reclassify every Anthropic stream as a failure.
func TestStream_AnthropicTerminatorIsACompletion(t *testing.T) {
	spy := runStream(t, &scriptedStreamAdapter{
		anthropicStyle: true,
		body: "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hi\"}}\n\n" +
			"data: {\"type\":\"message_stop\"}\n\n",
	})

	if len(spy.failures) != 0 {
		t.Errorf("a completed Anthropic stream was attested as a failure: %+v", spy.failures)
	}
	if len(spy.completes) != 1 {
		t.Fatalf("expected exactly 1 completion event, got %d", len(spy.completes))
	}
}

// Every streamed request must produce exactly one event, whatever the ending.
func TestStream_ProducesExactlyOneEvent(t *testing.T) {
	cases := map[string]*scriptedStreamAdapter{
		"completed": {body: "data: [DONE]\n\n"},
		"truncated": {body: "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"},
		"anthropic": {anthropicStyle: true, body: "data: {\"type\":\"message_stop\"}\n\n"},
	}
	for name, adapter := range cases {
		t.Run(name, func(t *testing.T) {
			spy := runStream(t, adapter)
			if total := len(spy.completes) + len(spy.failures); total != 1 {
				t.Errorf("got %d events, want exactly 1 (%d complete, %d failure)",
					total, len(spy.completes), len(spy.failures))
			}
		})
	}
}

// blockingBody never yields a line and never ends, so the stream can only be
// ended by the context: either the total deadline or the caller hanging up.
type blockingBody struct{ done chan struct{} }

func (b *blockingBody) Read(p []byte) (int, error) {
	<-b.done
	return 0, io.EOF
}
func (b *blockingBody) Close() error { return nil }

type hangingStreamAdapter struct{ body *blockingBody }

func (a *hangingStreamAdapter) Name() string { return "openai" }
func (a *hangingStreamAdapter) TransformRequest(ctx context.Context, req *types.AegisRequest) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodPost, "http://provider.invalid/v1/chat/completions", nil)
}
func (a *hangingStreamAdapter) SendRequest(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: a.body, Header: make(http.Header)}, nil
}
func (a *hangingStreamAdapter) TransformResponse(ctx context.Context, resp *http.Response) (*types.AegisResponse, error) {
	return nil, nil
}
func (a *hangingStreamAdapter) TransformStreamChunk(chunk []byte) ([]byte, error) { return chunk, nil }
func (a *hangingStreamAdapter) SupportsStreaming() bool                           { return true }
func (a *hangingStreamAdapter) SupportsTools() bool                               { return false }

// A client hanging up and the total deadline elapsing are different facts and
// must be recorded as different reasons.
//
// They used to be indistinguishable. A goroutine waited on the same ctx.Done()
// the total-timeout case already selected on, so both select cases became ready
// together and the choice between them was random. That was tolerable while it
// only mislabelled a log line; it is not, now that the outcome is sealed.
func TestStream_DisconnectAndTimeoutAreDistinguished(t *testing.T) {
	tests := []struct {
		name       string
		total      time.Duration
		cancel     bool
		wantReason string
	}{
		{
			name:       "caller hangs up",
			total:      10 * time.Second, // deadline must not be what fires
			cancel:     true,
			wantReason: audit.ReasonClientDisconnected,
		},
		{
			name:       "total deadline elapses",
			total:      150 * time.Millisecond,
			cancel:     false,
			wantReason: audit.ReasonStreamInterrupted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := &blockingBody{done: make(chan struct{})}
			defer close(body.done)
			adapter := &hangingStreamAdapter{body: body}

			spy := &outcomeSpy{}
			h := newAllowlistTestHandler(spy)
			sh := NewStreamingHandler(h, StreamingConfig{
				TotalTimeout:    tt.total,
				PerChunkTimeout: 10 * time.Second, // must not be what fires
				BufferSize:      4096,
				MaxBufferSize:   1 << 20,
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			info := &auth.AuthInfo{OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test"}
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(
				auth.ContextWithAuth(ctx, info))
			providerReq, err := adapter.TransformRequest(context.Background(), &types.AegisRequest{})
			if err != nil {
				t.Fatalf("building provider request: %v", err)
			}

			if tt.cancel {
				go func() {
					time.Sleep(50 * time.Millisecond)
					cancel()
				}()
			}

			sh.HandleStream(httptest.NewRecorder(), req, "req-cause-test", providerReq, adapter,
				"anthropic", "aegis-fast", info, &types.AegisRequest{Model: "aegis-fast"})

			if len(spy.completes) != 0 {
				t.Errorf("an unfinished stream was attested as complete: %+v", spy.completes)
			}
			if len(spy.failures) != 1 {
				t.Fatalf("expected exactly 1 failure event, got %d", len(spy.failures))
			}
			if got := spy.failures[0].Reason; got != tt.wantReason {
				t.Errorf("reason = %q, want %q", got, tt.wantReason)
			}
		})
	}
}

// The audit record must state the outcome the CALLER observed.
//
// When a provider returns a non-200, the gateway does not pass that status on:
// it sends 500 via WriteInternalError. Sealing the upstream status would put a
// row in the attested record saying the client received a 401 when it received
// a 500, which is a false statement about the request in the one place that
// cannot be corrected afterwards. The upstream status stays in the logs.
func TestStream_ProviderErrorRecordsTheStatusTheCallerReceived(t *testing.T) {
	spy := &outcomeSpy{}
	h := newAllowlistTestHandler(spy)
	sh := NewStreamingHandler(h, StreamingConfig{})
	adapter := &erroringStreamAdapter{body: `{"error":{"code":"invalid_api_key"}}`}

	info := &auth.AuthInfo{OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(
		auth.ContextWithAuth(context.Background(), info))
	providerReq, err := adapter.TransformRequest(req.Context(), &types.AegisRequest{})
	if err != nil {
		t.Fatalf("building provider request: %v", err)
	}

	w := httptest.NewRecorder()
	sh.HandleStream(w, req, "req-status-test", providerReq, adapter, "anthropic", "aegis-fast",
		info, &types.AegisRequest{Model: "aegis-fast"})

	if len(spy.failures) != 1 {
		t.Fatalf("expected exactly 1 failure event, got %d", len(spy.failures))
	}
	// The adapter answered 400; the caller got 500. The record says 500.
	if got := spy.failures[0].Req.StatusCode; got != http.StatusInternalServerError {
		t.Errorf("audit event status = %d, want %d (what the caller received); "+
			"the provider's own status was 400", got, http.StatusInternalServerError)
	}
	if w.Code != http.StatusInternalServerError {
		t.Errorf("caller received %d, want 500 — this test's premise is wrong if not", w.Code)
	}
	if got := spy.failures[0].Reason; got != audit.ReasonProviderError {
		t.Errorf("reason = %q, want %q", got, audit.ReasonProviderError)
	}
}
