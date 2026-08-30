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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/retry"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router"
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
func (o *outcomeSpy) LogModelDenied(_, _, _, _, _, _ string, _ string)      {}
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
	// transformErr makes chunk transformation fail for a provider reason.
	transformErr error
	// onTransform runs during chunk transformation, which is how a test can
	// cancel at the exact moment the chunk is being processed. Cancelling
	// beforehand is useless: the loop's ctx.Done() case wins immediately and
	// the branches under test are never reached.
	onTransform func()
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
	if a.onTransform != nil {
		a.onTransform()
	}
	if a.transformErr != nil {
		return nil, a.transformErr
	}
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
		"anthropic", "aegis-fast", "claude-test", info, &types.AegisRequest{Model: "aegis-fast"})
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
				"anthropic", "aegis-fast", "claude-test", info, &types.AegisRequest{Model: "aegis-fast"})

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
	sh.HandleStream(w, req, "req-status-test", providerReq, adapter, "anthropic", "aegis-fast", "claude-test",
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

// failingWriter accepts headers and flushes but fails every body write, which
// is what a ResponseWriter does once the peer has gone away.
type failingWriter struct {
	hdr  http.Header
	code int
}

func (f *failingWriter) Header() http.Header {
	if f.hdr == nil {
		f.hdr = make(http.Header)
	}
	return f.hdr
}
func (f *failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (f *failingWriter) WriteHeader(code int)      { f.code = code }
func (f *failingWriter) Flush()                    {}

// A terminator the caller never received does not establish completion.
//
// processChunk used to discard the error from writing [DONE] and set the
// terminator flag regardless, so a stream whose final marker failed to reach
// the client was sealed as request_complete.
func TestStream_UndeliveredTerminatorIsNotACompletion(t *testing.T) {
	spy := &outcomeSpy{}
	h := newAllowlistTestHandler(spy)
	sh := NewStreamingHandler(h, StreamingConfig{
		TotalTimeout:    5 * time.Second,
		PerChunkTimeout: 2 * time.Second,
		BufferSize:      4096,
		MaxBufferSize:   1 << 20,
	})
	adapter := &scriptedStreamAdapter{body: "data: [DONE]\n\n"}

	info := &auth.AuthInfo{OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(
		auth.ContextWithAuth(context.Background(), info))
	providerReq, err := adapter.TransformRequest(req.Context(), &types.AegisRequest{})
	if err != nil {
		t.Fatalf("building provider request: %v", err)
	}

	sh.HandleStream(&failingWriter{}, req, "req-undelivered", providerReq, adapter,
		"anthropic", "aegis-fast", "claude-test", info, &types.AegisRequest{Model: "aegis-fast"})

	if len(spy.completes) != 0 {
		t.Errorf("a stream whose terminator never reached the client was attested as complete: %+v",
			spy.completes)
	}
	if len(spy.failures) != 1 {
		t.Fatalf("expected exactly 1 failure event, got %d", len(spy.failures))
	}
}

// Streaming refused before any header goes out means the caller received 500,
// not the 200 a stream would have sent. Both the audit event and the usage row
// have to say so.
func TestStream_NotStartedRecordsTheErrorStatus(t *testing.T) {
	spy := &outcomeSpy{}
	h := newAllowlistTestHandler(spy)
	sh := NewStreamingHandler(h, StreamingConfig{TotalTimeout: 5 * time.Second})
	adapter := &scriptedStreamAdapter{body: "data: [DONE]\n\n"}

	info := &auth.AuthInfo{OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(
		auth.ContextWithAuth(context.Background(), info))
	providerReq, err := adapter.TransformRequest(req.Context(), &types.AegisRequest{})
	if err != nil {
		t.Fatalf("building provider request: %v", err)
	}

	// nonFlusher deliberately does not implement http.Flusher, which is what a
	// middleware wrapper that drops the optional interface looks like.
	sh.HandleStream(&nonFlusher{}, req, "req-not-started", providerReq, adapter,
		"anthropic", "aegis-fast", "claude-test", info, &types.AegisRequest{Model: "aegis-fast"})

	if len(spy.completes) != 0 {
		t.Errorf("a request that never streamed was attested as complete: %+v", spy.completes)
	}
	if len(spy.failures) != 1 {
		t.Fatalf("expected exactly 1 failure event, got %d", len(spy.failures))
	}
	if got := spy.failures[0].Req.StatusCode; got != http.StatusInternalServerError {
		t.Errorf("audit event status = %d, want 500: no stream started, so the caller "+
			"never received the 200 a stream would have sent", got)
	}
	if got := spy.failures[0].Reason; got != audit.ReasonStreamNotStarted {
		t.Errorf("reason = %q, want %q", got, audit.ReasonStreamNotStarted)
	}
}

// nonFlusher is a ResponseWriter that does not implement http.Flusher.
type nonFlusher struct {
	hdr  http.Header
	code int
}

func (n *nonFlusher) Header() http.Header {
	if n.hdr == nil {
		n.hdr = make(http.Header)
	}
	return n.hdr
}
func (n *nonFlusher) Write(p []byte) (int, error) { return len(p), nil }
func (n *nonFlusher) WriteHeader(code int)        { n.code = code }

// The buffered path must not attest a completion it did not deliver either.
//
// It used to emit request_complete before json.NewEncoder(w).Encode ran, and
// discard that encoder's error, so a caller who went away after the provider
// answered still produced a sealed "completed 200" for a body that never
// arrived. This is the non-streaming mirror of the terminator-delivery case,
// and the two paths have to agree.
func TestChatCompletions_UndeliveredResponseIsNotACompletion(t *testing.T) {
	spy := &outcomeSpy{}
	h := newAllowlistTestHandler(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"aegis-fast","messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test",
	}))

	// A writer that fails every body write is what a ResponseWriter does once
	// the peer has gone.
	h.ChatCompletions(&failingWriter{}, req)

	if len(spy.completes) != 0 {
		t.Errorf("a response the caller never received was attested as complete: %+v",
			spy.completes)
	}
	if len(spy.failures) != 1 {
		t.Fatalf("expected exactly 1 failure event, got %d", len(spy.failures))
	}
	if got := spy.failures[0].Reason; got != audit.ReasonResponseNotDelivered {
		t.Errorf("reason = %q, want %q", got, audit.ReasonResponseNotDelivered)
	}
}

// The ordinary case must still be a completion, or the fix above would simply
// stop attesting successful buffered requests.
func TestChatCompletions_DeliveredResponseIsACompletion(t *testing.T) {
	spy := &outcomeSpy{}
	h := newAllowlistTestHandler(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"aegis-fast","messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test",
	}))

	w := httptest.NewRecorder()
	h.ChatCompletions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; this test's premise is wrong if the request did not succeed: %s",
			w.Code, w.Body.String())
	}
	if len(spy.failures) != 0 {
		t.Errorf("a delivered response was attested as a failure: %+v", spy.failures)
	}
	if len(spy.completes) != 1 {
		t.Fatalf("expected exactly 1 completion event, got %d", len(spy.completes))
	}
}

// flushFailingWriter accepts every write and fails only on flush, which is the
// case that matters: net/http buffers a small response, so Write succeeds and
// the socket failure surfaces later. FlushError is the interface
// http.NewResponseController prefers, and it is the only way to see that error.
type flushFailingWriter struct {
	hdr  http.Header
	code int
	body int
}

func (f *flushFailingWriter) Header() http.Header {
	if f.hdr == nil {
		f.hdr = make(http.Header)
	}
	return f.hdr
}
func (f *flushFailingWriter) Write(p []byte) (int, error) { f.body += len(p); return len(p), nil }
func (f *flushFailingWriter) WriteHeader(code int)        { f.code = code }

// Flush satisfies http.Flusher, which the streaming path requires before it
// will start a stream at all. Without it this double exercised the
// "streaming not supported" path and the terminator test passed vacuously.
func (f *flushFailingWriter) Flush() {}

// FlushError is what http.NewResponseController prefers, and it is the only
// way the socket failure becomes visible.
func (f *flushFailingWriter) FlushError() error { return io.ErrClosedPipe }

// A buffered response whose bytes were accepted but never reached the socket is
// not delivered. Encode returns nil in that case, so the flush error is the
// only remaining signal.
func TestChatCompletions_FailedFlushIsNotACompletion(t *testing.T) {
	spy := &outcomeSpy{}
	h := newAllowlistTestHandler(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"aegis-fast","messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test",
	}))

	w := &flushFailingWriter{}
	h.ChatCompletions(w, req)

	if w.body == 0 {
		t.Fatal("the handler never wrote a body; this test's premise is wrong")
	}
	if len(spy.completes) != 0 {
		t.Errorf("a response that was buffered but never flushed was attested as complete: %+v",
			spy.completes)
	}
	if len(spy.failures) != 1 {
		t.Fatalf("expected exactly 1 failure event, got %d", len(spy.failures))
	}
	if got := spy.failures[0].Reason; got != audit.ReasonResponseNotDelivered {
		t.Errorf("reason = %q, want %q", got, audit.ReasonResponseNotDelivered)
	}
}

// The same for the stream terminator: fmt.Fprintf can put [DONE] in net/http's
// buffer and the socket failure only appear during the flush, whose error
// http.Flusher discards.
func TestStream_TerminatorThatFailedToFlushIsNotACompletion(t *testing.T) {
	spy := &outcomeSpy{}
	h := newAllowlistTestHandler(spy)
	sh := NewStreamingHandler(h, StreamingConfig{
		TotalTimeout:    5 * time.Second,
		PerChunkTimeout: 2 * time.Second,
		BufferSize:      4096,
		MaxBufferSize:   1 << 20,
	})
	adapter := &scriptedStreamAdapter{body: "data: [DONE]\n\n"}

	info := &auth.AuthInfo{OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(
		auth.ContextWithAuth(context.Background(), info))
	providerReq, err := adapter.TransformRequest(req.Context(), &types.AegisRequest{})
	if err != nil {
		t.Fatalf("building provider request: %v", err)
	}

	sh.HandleStream(&flushFailingWriter{}, req, "req-flush-fail", providerReq, adapter,
		"anthropic", "aegis-fast", "claude-test", info, &types.AegisRequest{Model: "aegis-fast"})

	if len(spy.completes) != 0 {
		t.Errorf("a terminator that never left the gateway was attested as complete: %+v",
			spy.completes)
	}
	if len(spy.failures) != 1 {
		t.Fatalf("expected exactly 1 failure event, got %d", len(spy.failures))
	}
}

// A writer that cannot be flushed on demand tells us nothing, and "cannot
// confirm" must not be recorded as "failed". Otherwise a wrapper that drops the
// optional interface would turn every successful request into a sealed failure,
// which is a worse error than the one the flush check exists to prevent.
//
// This is why the attested contract is "written in full, and flushed where the
// writer supports on-demand flushing" rather than a flat "flushed": this case
// records a completion without having confirmed a flush, and the contract has
// to say so rather than the test quietly widening it. Under net/http's own
// ResponseWriter a flush is always supported, so this arises only behind such a
// wrapper, where the response is still written and still flushed when the
// handler returns.
func TestChatCompletions_UnflushableWriterIsStillACompletion(t *testing.T) {
	spy := &outcomeSpy{}
	h := newAllowlistTestHandler(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"aegis-fast","messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test",
	}))

	// nonFlusher implements neither FlushError nor http.Flusher, so
	// ResponseController reports ErrNotSupported.
	h.ChatCompletions(&nonFlusher{}, req)

	if len(spy.failures) != 0 {
		t.Errorf("an unflushable writer produced a sealed failure: %+v", spy.failures)
	}
	if len(spy.completes) != 1 {
		t.Fatalf("expected exactly 1 completion event, got %d", len(spy.completes))
	}
}

// nthWriteFailingWriter fails one specific body write and accepts the rest,
// which is what a connection dropping midway through a response looks like.
type nthWriteFailingWriter struct {
	hdr      http.Header
	failOn   int
	writes   int
	code     int
	accepted int
}

func (n *nthWriteFailingWriter) Header() http.Header {
	if n.hdr == nil {
		n.hdr = make(http.Header)
	}
	return n.hdr
}
func (n *nthWriteFailingWriter) Write(p []byte) (int, error) {
	n.writes++
	if n.writes == n.failOn {
		return 0, io.ErrClosedPipe
	}
	n.accepted += len(p)
	return len(p), nil
}
func (n *nthWriteFailingWriter) WriteHeader(code int) { n.code = code }
func (n *nthWriteFailingWriter) Flush()               {}

// A chunk that failed to reach the client means the response was not written in
// full, even if the terminator afterwards writes and flushes cleanly.
//
// processChunk used to discard write and flush errors for every ordinary chunk,
// so only the terminator was checked. A stream that lost its middle and then
// sent [DONE] was sealed as request_complete, contradicting the contract that
// the FULL response was written.
func TestStream_FailedChunkIsNotACompletion(t *testing.T) {
	spy := &outcomeSpy{}
	h := newAllowlistTestHandler(spy)
	sh := NewStreamingHandler(h, StreamingConfig{
		TotalTimeout:    5 * time.Second,
		PerChunkTimeout: 2 * time.Second,
		BufferSize:      4096,
		MaxBufferSize:   1 << 20,
	})
	adapter := &scriptedStreamAdapter{
		body: "data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"two\"}}]}\n\n" +
			"data: [DONE]\n\n",
	}

	info := &auth.AuthInfo{OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(
		auth.ContextWithAuth(context.Background(), info))
	providerReq, err := adapter.TransformRequest(req.Context(), &types.AegisRequest{})
	if err != nil {
		t.Fatalf("building provider request: %v", err)
	}

	// Write 1 is the streaming header; fail the first content chunk after it.
	w := &nthWriteFailingWriter{failOn: 2}
	sh.HandleStream(w, req, "req-chunk-fail", providerReq, adapter,
		"anthropic", "aegis-fast", "claude-test", info, &types.AegisRequest{Model: "aegis-fast"})

	if w.writes < 2 {
		t.Fatalf("the handler made %d writes; this test's premise needs at least 2", w.writes)
	}
	if len(spy.completes) != 0 {
		t.Errorf("a stream that lost a chunk was attested as complete: %+v", spy.completes)
	}
	if len(spy.failures) != 1 {
		t.Fatalf("expected exactly 1 failure event, got %d", len(spy.failures))
	}
}

// sendFailingAdapter is registered under the name the test route uses, so
// routing succeeds and the request reaches the send, which then fails. Pointing
// the registry at a different name instead makes ResolveRoute fail first and no
// audit event is produced at all, which is how the first version of these tests
// managed to skip while proving nothing.
type sendFailingAdapter struct{ err error }

func (sendFailingAdapter) Name() string { return "stub-provider" }
func (sendFailingAdapter) TransformRequest(ctx context.Context, req *types.AegisRequest) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodPost, "http://provider.invalid/v1/chat/completions", nil)
}
func (a sendFailingAdapter) SendRequest(*http.Request) (*http.Response, error) {
	if a.err != nil {
		return nil, a.err
	}
	return nil, io.ErrUnexpectedEOF
}
func (sendFailingAdapter) TransformResponse(context.Context, *http.Response) (*types.AegisResponse, error) {
	return nil, nil
}
func (sendFailingAdapter) TransformStreamChunk(c []byte) ([]byte, error) { return c, nil }
func (sendFailingAdapter) SupportsStreaming() bool                       { return true }
func (sendFailingAdapter) SupportsTools() bool                           { return false }

func newSendFailingHandler(spy AuditLogger, sendErr error) *Handler {
	h := newAllowlistTestHandler(spy)
	reg := router.NewRegistry()
	reg.Register("stub-provider", sendFailingAdapter{err: sendErr})
	h.registry = reg
	return h
}

// A caller cancelling mid-request reaches the same error path as a provider
// that could not be reached: both surface as an error from the send. Sealing
// provider_unreachable for both attributes every routine client disconnect to a
// provider outage, in the record an operator uses to judge provider reliability.
func TestChatCompletions_CallerCancellationIsNotAProviderOutage(t *testing.T) {
	spy := &outcomeSpy{}
	// A real transport reports a cancelled request as an error wrapping
	// context.Canceled. Returning an unrelated error while cancelling the
	// context does not model a disconnect, it models a provider failure that
	// coincided with one, which is a different thing and is classified as a
	// provider fault on purpose.
	h := newSendFailingHandler(spy, fmt.Errorf("Post \"http://provider.invalid\": %w", context.Canceled))

	ctx, cancel := context.WithCancel(context.Background())
	info := &auth.AuthInfo{OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"aegis-fast","messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(auth.ContextWithAuth(ctx, info))
	cancel() // the caller has gone before the send completes

	h.ChatCompletions(httptest.NewRecorder(), req)

	if len(spy.failures) != 1 {
		t.Fatalf("expected exactly 1 failure event, got %d; the request must reach the "+
			"send path or this test proves nothing", len(spy.failures))
	}
	if got := spy.failures[0].Reason; got != audit.ReasonClientDisconnected {
		t.Errorf("reason = %q, want %q: a cancelled caller is not a provider outage",
			got, audit.ReasonClientDisconnected)
	}
}

// The provider genuinely being unreachable must still say so, or the fix above
// would relabel real outages as client disconnects.
func TestChatCompletions_RealProviderFailureIsStillAProviderOutage(t *testing.T) {
	spy := &outcomeSpy{}
	h := newSendFailingHandler(spy, nil) // an ordinary transport failure

	info := &auth.AuthInfo{OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"aegis-fast","messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(auth.ContextWithAuth(context.Background(), info))

	h.ChatCompletions(httptest.NewRecorder(), req)

	if len(spy.failures) != 1 {
		t.Fatalf("expected exactly 1 failure event, got %d; the request must reach the "+
			"send path or this test proves nothing", len(spy.failures))
	}
	if got := spy.failures[0].Reason; got == audit.ReasonClientDisconnected {
		t.Errorf("a live caller's request was recorded as %q; only a cancelled context "+
			"may produce that reason", got)
	}
}

// headerFlushFailingWriter fails only the first flush, which is the streaming
// header, and succeeds afterwards. That is the shape the reviewer described: a
// one-shot failure whose later writes all succeed.
type headerFlushFailingWriter struct {
	hdr     http.Header
	code    int
	flushes int
}

func (h *headerFlushFailingWriter) Header() http.Header {
	if h.hdr == nil {
		h.hdr = make(http.Header)
	}
	return h.hdr
}
func (h *headerFlushFailingWriter) Write(p []byte) (int, error) { return len(p), nil }
func (h *headerFlushFailingWriter) WriteHeader(code int)        { h.code = code }
func (h *headerFlushFailingWriter) Flush()                      {}
func (h *headerFlushFailingWriter) FlushError() error {
	h.flushes++
	if h.flushes == 1 {
		return io.ErrClosedPipe
	}
	return nil
}

// A stream whose 200 header never reached the client did not start, however
// well the later writes went.
func TestStream_FailedHeaderFlushIsNotACompletion(t *testing.T) {
	spy := &outcomeSpy{}
	h := newAllowlistTestHandler(spy)
	sh := NewStreamingHandler(h, StreamingConfig{
		TotalTimeout:    5 * time.Second,
		PerChunkTimeout: 2 * time.Second,
		BufferSize:      4096,
		MaxBufferSize:   1 << 20,
	})
	adapter := &scriptedStreamAdapter{body: "data: [DONE]\n\n"}

	info := &auth.AuthInfo{OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(
		auth.ContextWithAuth(context.Background(), info))
	providerReq, err := adapter.TransformRequest(req.Context(), &types.AegisRequest{})
	if err != nil {
		t.Fatalf("building provider request: %v", err)
	}

	w := &headerFlushFailingWriter{}
	sh.HandleStream(w, req, "req-header-flush", providerReq, adapter,
		"anthropic", "aegis-fast", "claude-test", info, &types.AegisRequest{Model: "aegis-fast"})

	if w.flushes == 0 {
		t.Fatal("no flush was attempted; this test's premise is wrong")
	}
	if len(spy.completes) != 0 {
		t.Errorf("a stream whose header never reached the client was attested as "+
			"complete: %+v", spy.completes)
	}
	if len(spy.failures) != 1 {
		t.Fatalf("expected exactly 1 failure event, got %d", len(spy.failures))
	}
	if got := spy.failures[0].Reason; got != audit.ReasonResponseNotDelivered {
		t.Errorf("reason = %q, want %q", got, audit.ReasonResponseNotDelivered)
	}
	// 200, not 500. WriteHeader has already committed the status line by the
	// time the flush is attempted, and no error status can follow it, so
	// recording 500 would put a status the caller never received into the
	// sealed record. The first version of this fix did exactly that.
	if got := spy.failures[0].Req.StatusCode; got != http.StatusOK {
		t.Errorf("status = %d, want 200: WriteHeader already committed it", got)
	}
	// The audit event takes providerKey directly, so it was never the surface
	// this could damage. The usage row reads metrics.Provider, which is what
	// TestStreamWithMonitoring_EarlyReturnsKeepTheProvider covers.
}

// The usage row is built from the StreamMetrics that streamWithMonitoring
// returns, so an early return that yields a zero value persists provider = ”
// even though providerKey was known before the request was sent. That loses the
// attribution for precisely the requests an operator would investigate.
//
// Asserted on the returned metrics rather than through the handler, because the
// usage recorder is a concrete type with no seam to observe. The audit event is
// not affected: it takes providerKey directly.
func TestStreamWithMonitoring_EarlyReturnsKeepTheProvider(t *testing.T) {
	tests := map[string]struct {
		writer http.ResponseWriter
		want   StreamOutcome
	}{
		"writer cannot flush": {&nonFlusher{}, StreamNotStarted},
		"header flush fails":  {&flushFailingWriter{}, StreamHeaderUndelivered},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			h := newAllowlistTestHandler(&outcomeSpy{})
			sh := NewStreamingHandler(h, StreamingConfig{TotalTimeout: 5 * time.Second})
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
				Header:     make(http.Header),
			}

			got := sh.streamWithMonitoring(context.Background(), tt.writer, "req-attr",
				resp, &scriptedStreamAdapter{}, "anthropic", "claude-test",
				&auth.AuthInfo{OrganizationID: "org-test"})

			if got.Outcome != tt.want {
				t.Errorf("outcome = %q, want %q", got.Outcome, tt.want)
			}
			if got.Provider != "anthropic" {
				t.Errorf("provider = %q, want anthropic; the usage row is built from "+
					"this value and would record an empty provider", got.Provider)
			}
		})
	}
}

// A stream that never delivered a chunk has no first-token latency, and a
// histogram cannot express "not applicable": any value observed lands in _sum
// and skews every average computed from it. FirstChunkTime is the zero time on
// these paths, so the naive subtraction is a multi-billion-year negative.
func TestTimeToFirstToken_AbsentWhenNoChunkArrived(t *testing.T) {
	start := time.Now()

	if _, ok := timeToFirstToken(StreamMetrics{StartTime: start}); ok {
		t.Error("reported a first-token latency for a stream that delivered no chunk")
	}
	if got := firstTokenMsForLog(StreamMetrics{StartTime: start}); got != -1 {
		t.Errorf("log value = %d, want -1; a negative age is worse than an explicit absence", got)
	}

	withChunk := StreamMetrics{StartTime: start, FirstChunkTime: start.Add(150 * time.Millisecond)}
	d, ok := timeToFirstToken(withChunk)
	if !ok {
		t.Fatal("a stream that delivered a chunk must report its latency")
	}
	if d != 150*time.Millisecond {
		t.Errorf("latency = %v, want 150ms", d)
	}
	if got := firstTokenMsForLog(withChunk); got != 150 {
		t.Errorf("log value = %d, want 150", got)
	}
}

// The early returns must carry the routed model as well as the provider, or the
// usage row cannot say which provider request failed.
func TestStreamWithMonitoring_EarlyReturnsKeepTheModel(t *testing.T) {
	h := newAllowlistTestHandler(&outcomeSpy{})
	sh := NewStreamingHandler(h, StreamingConfig{TotalTimeout: 5 * time.Second})
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}

	got := sh.streamWithMonitoring(context.Background(), &nonFlusher{}, "req-model",
		resp, &scriptedStreamAdapter{}, "anthropic", "claude-haiku-4-5",
		&auth.AuthInfo{OrganizationID: "org-test"})

	if got.Model != "claude-haiku-4-5" {
		t.Errorf("model = %q, want the routed model; usage_records.model_served would "+
			"otherwise be empty for the request being investigated", got.Model)
	}
}

// The model the provider reports must overwrite the routed one.
//
// Model is pre-populated with the routed model so an early return can attribute
// the request, and the "only if empty" guard in extractTokensFromChunk then made
// that pre-population permanent: a completed stream kept the routed name, so
// model_served and the streaming cost were wrong whenever the provider served
// something else. The routed value is a fallback, not a floor.
func TestStream_ProviderReportedModelOverridesTheRoutedOne(t *testing.T) {
	spy := &outcomeSpy{}
	h := newAllowlistTestHandler(spy)
	sh := NewStreamingHandler(h, StreamingConfig{
		TotalTimeout:    5 * time.Second,
		PerChunkTimeout: 2 * time.Second,
		BufferSize:      4096,
		MaxBufferSize:   1 << 20,
	})
	adapter := &scriptedStreamAdapter{
		body: `data: {"model":"claude-haiku-4-5-20251001","choices":[{"delta":{"content":"hi"}}]}` +
			"\n\ndata: [DONE]\n\n",
	}

	info := &auth.AuthInfo{OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(
		auth.ContextWithAuth(context.Background(), info))
	providerReq, err := adapter.TransformRequest(req.Context(), &types.AegisRequest{})
	if err != nil {
		t.Fatalf("building provider request: %v", err)
	}

	resp, err := adapter.SendRequest(providerReq)
	if err != nil {
		t.Fatalf("sending: %v", err)
	}

	got := sh.streamWithMonitoring(context.Background(), httptest.NewRecorder(), "req-model-win",
		resp, adapter, "anthropic", "routed-model-name", info)

	if got.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("model = %q, want the model the provider reported; the routed name "+
			"is a fallback for early returns, not a floor", got.Model)
	}
}

// decodeFailingAdapter answers with 200 and then fails while the body is being
// read, which is what a caller disconnecting mid-decode looks like from here.
type decodeFailingAdapter struct {
	status int
	err    error
}

func (decodeFailingAdapter) Name() string { return "stub-provider" }
func (decodeFailingAdapter) TransformRequest(ctx context.Context, req *types.AegisRequest) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodPost, "http://provider.invalid/v1/chat/completions", nil)
}
func (d decodeFailingAdapter) SendRequest(*http.Request) (*http.Response, error) {
	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
}
func (d decodeFailingAdapter) TransformResponse(context.Context, *http.Response) (*types.AegisResponse, error) {
	if d.err != nil {
		return nil, d.err
	}
	return nil, errors.New("malformed provider response")
}
func (decodeFailingAdapter) TransformStreamChunk(c []byte) ([]byte, error) { return c, nil }
func (decodeFailingAdapter) SupportsStreaming() bool                       { return true }
func (decodeFailingAdapter) SupportsTools() bool                           { return false }

// A caller who goes away while the response body is being decoded reaches the
// transform-failure branch, not the send-failure branch. Sealing provider_error
// there attributes a routine disconnect to the provider, in the record an
// operator uses to judge provider reliability.
func TestChatCompletions_CancellationDuringDecodeIsNotAProviderError(t *testing.T) {
	// The classification turns on WHAT WENT WRONG, not on the state of the
	// request context when the event happens to be written. The last two cases
	// are the decisive ones: a genuine provider failure whose caller also went
	// away is still a provider failure.
	tests := map[string]struct {
		status int
		err    error
		cancel bool
		want   string
	}{
		"cancelled mid-decode":            {http.StatusOK, context.Canceled, true, audit.ReasonClientDisconnected},
		"undecodable 200, live caller":    {http.StatusOK, nil, false, audit.ReasonProviderError},
		"non-200, live caller":            {http.StatusBadGateway, nil, false, audit.ReasonProviderError},
		"malformed 200, caller also gone": {http.StatusOK, nil, true, audit.ReasonProviderError},

		// The adapters read the body before inspecting the status, so a caller
		// leaving during a non-200 body read produces a cancellation and the
		// status error is never built. Classifying on the error alone erased
		// the provider fault here, which is why the status is consulted first.
		"non-200, caller also gone":           {http.StatusBadGateway, nil, true, audit.ReasonProviderError},
		"non-200, cancelled during body read": {http.StatusBadGateway, context.Canceled, true, audit.ReasonProviderError},

		// The buffered path runs through retry.Executor, which wraps its own
		// sentinel and formats ctx.Err() with %v, so context.Canceled is not in
		// the chain. Testing for it alone sealed real disconnects as outages.
		"retry-wrapped cancellation": {http.StatusOK,
			fmt.Errorf("%w: %v", retry.ErrContextCancelled, context.Canceled), true,
			audit.ReasonClientDisconnected},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			spy := &outcomeSpy{}
			h := newAllowlistTestHandler(spy)
			reg := router.NewRegistry()
			reg.Register("stub-provider", decodeFailingAdapter{status: tt.status, err: tt.err})
			h.registry = reg

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			info := &auth.AuthInfo{OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test"}
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"aegis-fast","messages":[{"role":"user","content":"hi"}]}`))
			req = req.WithContext(auth.ContextWithAuth(ctx, info))
			if tt.cancel {
				cancel()
			}

			h.ChatCompletions(httptest.NewRecorder(), req)

			if len(spy.failures) != 1 {
				t.Fatalf("expected exactly 1 failure event, got %d", len(spy.failures))
			}
			if got := spy.failures[0].Reason; got != tt.want {
				t.Errorf("reason = %q, want %q", got, tt.want)
			}
		})
	}
}

// A caller closing promptly after receiving [DONE] is the normal end of a
// streamed request. terminatorSeen is set only after the marker is written AND
// flushed without error, so the full response demonstrably went out; cancelling
// the context afterwards does not undo that. Sealing it as a disconnect would
// deny a completion that happened.
func TestStream_CloseAfterTerminatorIsStillACompletion(t *testing.T) {
	spy := &outcomeSpy{}
	h := newAllowlistTestHandler(spy)
	sh := NewStreamingHandler(h, StreamingConfig{
		TotalTimeout:    5 * time.Second,
		PerChunkTimeout: 2 * time.Second,
		BufferSize:      4096,
		MaxBufferSize:   1 << 20,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The Anthropic form routes the marker through Transform, so cancelling
	// there lands the disconnect exactly as the terminator is processed, which
	// is what a client closing on receipt of [DONE] looks like.
	adapter := &scriptedStreamAdapter{
		anthropicStyle: true,
		body:           "data: {\"type\":\"message_stop\"}\n\n",
		onTransform:    cancel,
	}

	info := &auth.AuthInfo{OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test"}
	resp, err := adapter.SendRequest(nil)
	if err != nil {
		t.Fatalf("sending: %v", err)
	}

	got := sh.streamWithMonitoring(ctx, httptest.NewRecorder(), "req-close-after-done",
		resp, adapter, "anthropic", "claude-test", info)

	if got.Outcome != StreamCompleted {
		t.Errorf("outcome = %q, want %q: the terminator was written and flushed, so the "+
			"response went out and a prompt client close does not undo it",
			got.Outcome, StreamCompleted)
	}
	_ = spy
}

// A provider sending an untransformable chunk fails on the same branch a
// disconnect does. A cancellation arriving alongside it must not relabel a
// provider fault as a client disconnect.
func TestStream_ChunkErrorIsNotRelabelledByACoincidentCancellation(t *testing.T) {
	spy := &outcomeSpy{}
	h := newAllowlistTestHandler(spy)
	sh := NewStreamingHandler(h, StreamingConfig{
		TotalTimeout:    5 * time.Second,
		PerChunkTimeout: 2 * time.Second,
		BufferSize:      4096,
		MaxBufferSize:   1 << 20,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The provider fails the chunk AND the caller leaves at the same moment.
	// Cancelling before the run instead would be useless: the loop's ctx.Done()
	// case wins immediately and this branch is never reached, which is how an
	// earlier version of this test passed against correct code.
	adapter := &scriptedStreamAdapter{
		body:         "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n",
		transformErr: errors.New("untransformable chunk from provider"),
		onTransform:  cancel,
	}

	info := &auth.AuthInfo{OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test"}
	resp, err := adapter.SendRequest(nil)
	if err != nil {
		t.Fatalf("sending: %v", err)
	}

	got := sh.streamWithMonitoring(ctx, httptest.NewRecorder(), "req-chunk-err",
		resp, adapter, "anthropic", "claude-test", info)

	if got.Outcome == StreamClientDisconnected {
		t.Error("a provider chunk failure was relabelled as a client disconnect because " +
			"the context happened to be cancelled")
	}
	_ = spy
}

// A disconnect during a chunk write surfaces as EPIPE or ECONNRESET, not as
// anything wrapping context.Canceled. Checking only for cancellation missed the
// commonest form of the very thing it was looking for.
func TestStream_SocketWriteFailureIsADisconnect(t *testing.T) {
	spy := &outcomeSpy{}
	h := newAllowlistTestHandler(spy)
	sh := NewStreamingHandler(h, StreamingConfig{
		TotalTimeout:    5 * time.Second,
		PerChunkTimeout: 2 * time.Second,
		BufferSize:      4096,
		MaxBufferSize:   1 << 20,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The write fails the way a closed peer makes it fail, and the caller is
	// gone by then, which is what a real disconnect looks like from here.
	adapter := &scriptedStreamAdapter{
		body:         "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n",
		transformErr: fmt.Errorf("write chunk: %w", syscall.EPIPE),
		onTransform:  cancel,
	}
	resp, err := adapter.SendRequest(nil)
	if err != nil {
		t.Fatalf("sending: %v", err)
	}

	got := sh.streamWithMonitoring(ctx, httptest.NewRecorder(), "req-epipe",
		resp, adapter, "anthropic", "claude-test",
		&auth.AuthInfo{OrganizationID: "org-test"})

	if got.Outcome != StreamClientDisconnected {
		t.Errorf("outcome = %q, want %q: a broken pipe is the caller going away",
			got.Outcome, StreamClientDisconnected)
	}
	_ = spy
}
