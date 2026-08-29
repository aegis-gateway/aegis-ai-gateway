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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// TestNonStreamingAllowIsAttested is the regression test for the completeness
// hole: audit_events is the only table the sealer covers, and before this it
// held refusals only, so the chain attested nothing about permitted traffic.
func TestNonStreamingAllowIsAttested(t *testing.T) {
	t.Parallel()

	spy := &allowlistAudit{}
	w := postAs(t, newAllowlistHandler(spy), authWith(nil), "aegis-fast")

	if w.Code != http.StatusOK {
		t.Fatalf("expected the request to be served, got %d: %s", w.Code, w.Body.String())
	}
	if len(spy.completed) != 1 {
		t.Fatalf("a served request wrote %d completion event(s), want exactly 1", len(spy.completed))
	}
	if len(spy.failed) != 0 {
		t.Errorf("a served request also wrote %d provider-failure event(s)", len(spy.failed))
	}

	ev := spy.completed[0]
	if ev.RequestID != "req-allowlist-test" {
		t.Errorf("event carries request id %q", ev.RequestID)
	}
	if ev.OrgID != "org-test" || ev.TeamID != "team-test" || ev.KeyID != "key-test" {
		t.Errorf("event carries org=%q team=%q key=%q, want the authenticated identity",
			ev.OrgID, ev.TeamID, ev.KeyID)
	}
	// The provider key and the concrete model, not the adapter type and not
	// the alias: these must match what the pricing gate and the usage record
	// used, or the attested record and the billed record describe different
	// requests.
	if ev.Provider != "test-provider" {
		t.Errorf("event carries provider %q, want the configured key test-provider", ev.Provider)
	}
	if ev.Model != "test-model" {
		t.Errorf("event carries model %q, want the resolved concrete model test-model", ev.Model)
	}
	if ev.Streaming {
		t.Error("a non-streaming request was recorded as streaming")
	}
	if ev.StatusCode != http.StatusOK {
		t.Errorf("event carries status %d, want 200", ev.StatusCode)
	}
}

// failingAdapter fails at whichever stage is selected, so the handler's two
// provider-failure exits can be driven independently.
type failingAdapter struct {
	sendErr  error
	response *http.Response
}

func (f *failingAdapter) Name() string            { return "openai" }
func (f *failingAdapter) SupportsStreaming() bool { return true }
func (f *failingAdapter) SupportsTools() bool     { return true }
func (f *failingAdapter) TransformRequest(ctx context.Context, _ *types.AegisRequest) (*http.Request, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://provider.invalid/v1/chat", nil)
	return req, nil
}
func (f *failingAdapter) SendRequest(*http.Request) (*http.Response, error) {
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	return f.response, nil
}
func (f *failingAdapter) TransformResponse(context.Context, *http.Response) (*types.AegisResponse, error) {
	return nil, errors.New("provider returned status 400: [redacted excerpt]")
}
func (f *failingAdapter) TransformStreamChunk(c []byte) ([]byte, error) { return c, nil }

// TestNonStreamingProviderFailureIsAttested covers the other half of the
// completeness argument. A request that passed every gate and then failed at
// the provider leaves no denial event, so without this it leaves no attested
// trace at all.
func TestNonStreamingProviderFailureIsAttested(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		adapter    *failingAdapter
		wantStage  string
		wantStatus int
	}{
		{
			name:       "provider unreachable",
			adapter:    &failingAdapter{sendErr: errors.New("connection refused")},
			wantStage:  audit.FailureProviderUnreachable,
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "response could not be translated",
			adapter: &failingAdapter{response: &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{}}`))),
				Header:     make(http.Header),
			}},
			wantStage:  audit.FailureProviderResponseInvalid,
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spy := &allowlistAudit{}
			h := newAllowlistHandlerWithAdapter(spy, tc.adapter)
			w := postAs(t, h, authWith(nil), "aegis-fast")

			if w.Code != tc.wantStatus {
				t.Fatalf("got HTTP %d, want %d: %s", w.Code, tc.wantStatus, w.Body.String())
			}
			if len(spy.failed) != 1 {
				t.Fatalf("got %d provider-failure event(s), want exactly 1", len(spy.failed))
			}
			if len(spy.completed) != 0 {
				t.Errorf("a failed request also wrote %d completion event(s); the two are "+
					"mutually exclusive", len(spy.completed))
			}
			got := spy.failed[0]
			if got.Stage != tc.wantStage {
				t.Errorf("recorded stage %q, want %q", got.Stage, tc.wantStage)
			}
			if got.Event.StatusCode != tc.wantStatus {
				t.Errorf("event records status %d, want %d to match what the caller was sent",
					got.Event.StatusCode, tc.wantStatus)
			}
			if got.Event.Provider != "test-provider" {
				t.Errorf("event carries provider %q, want the configured key", got.Event.Provider)
			}
		})
	}
}

// sseBody returns a provider stream body.
func sseBody(lines ...string) io.ReadCloser {
	var buf bytes.Buffer
	for _, l := range lines {
		buf.WriteString("data: " + l + "\n\n")
	}
	return io.NopCloser(&buf)
}

// runStream drives HandleStream against a provider response and returns the spy.
func runStream(t *testing.T, resp *http.Response, sendErr error, ctxFn func() (context.Context, context.CancelFunc)) *allowlistAudit {
	t.Helper()

	spy := &allowlistAudit{}
	handler := &Handler{metrics: getTestMetrics(), auditLogger: spy}
	sh := NewStreamingHandler(handler, StreamingConfig{
		PerChunkTimeout: 150 * time.Millisecond,
		TotalTimeout:    5 * time.Second,
		BufferSize:      64 * 1024,
		MaxBufferSize:   1024 * 1024,
	})

	ctx, cancel := context.Background(), context.CancelFunc(func() {})
	if ctxFn != nil {
		ctx, cancel = ctxFn()
	}
	defer cancel()

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	providerReq, _ := http.NewRequest("POST", "http://mock-provider.com", nil)

	sh.HandleStream(w, req, "req-stream-audit", providerReq,
		&mockStreamAdapter{name: "openai", response: resp, sendError: sendErr},
		"test-provider", "aegis-fast",
		&auth.AuthInfo{OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test"},
		&types.AegisRequest{Model: "test-model", Stream: true})
	return spy
}

// TestStreamingEmitsExactlyOneEvent is the test the phase's warning is about.
// The streaming path has six exits and its own completion accounting, so both
// double-writing and silent omission live here.
func TestStreamingEmitsExactlyOneEvent(t *testing.T) {
	cases := []struct {
		name          string
		resp          *http.Response
		sendErr       error
		ctxFn         func() (context.Context, context.CancelFunc)
		wantCompleted int
		wantFailed    int
		wantStage     string
		wantStatus    int
	}{
		{
			name: "clean completion",
			resp: &http.Response{StatusCode: 200, Header: make(http.Header),
				Body: sseBody(`{"choices":[{"delta":{"content":"hi"}}]}`, "[DONE]")},
			wantCompleted: 1, wantStatus: http.StatusOK,
		},
		{
			name: "provider end of stream without DONE",
			resp: &http.Response{StatusCode: 200, Header: make(http.Header),
				Body: sseBody(`{"choices":[{"delta":{"content":"hi"}}]}`)},
			wantCompleted: 1, wantStatus: http.StatusOK,
		},
		{
			name:       "provider unreachable",
			sendErr:    errors.New("connection refused"),
			wantFailed: 1, wantStage: audit.FailureProviderUnreachable,
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "provider returns non-200",
			resp: &http.Response{StatusCode: 429, Header: make(http.Header),
				Body: io.NopCloser(bytes.NewReader([]byte(`{"error":{"code":"rate_limited"}}`)))},
			wantFailed: 1, wantStage: audit.FailureProviderHTTPError,
			wantStatus: http.StatusInternalServerError,
		},
		{
			// A caller hanging up is not the provider's fault. It is a
			// completion with a status that says what happened.
			name: "client disconnects mid-stream",
			resp: &http.Response{StatusCode: 200, Header: make(http.Header),
				Body: io.NopCloser(blockingReader{})},
			ctxFn: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				go func() { time.Sleep(50 * time.Millisecond); cancel() }()
				return ctx, cancel
			},
			wantCompleted: 1, wantStatus: statusClientClosedRequest,
		},
		{
			name: "stream stalls past the per-chunk timeout",
			resp: &http.Response{StatusCode: 200, Header: make(http.Header),
				Body: io.NopCloser(blockingReader{})},
			wantFailed: 1, wantStage: audit.FailureStreamTimeout,
			wantStatus: http.StatusGatewayTimeout,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := runStream(t, tc.resp, tc.sendErr, tc.ctxFn)

			total := len(spy.completed) + len(spy.failed)
			if total != 1 {
				t.Fatalf("the stream produced %d audit event(s) (%d completion, %d failure), "+
					"want exactly 1", total, len(spy.completed), len(spy.failed))
			}
			if len(spy.completed) != tc.wantCompleted || len(spy.failed) != tc.wantFailed {
				t.Fatalf("got %d completion and %d failure event(s), want %d and %d",
					len(spy.completed), len(spy.failed), tc.wantCompleted, tc.wantFailed)
			}

			var ev audit.CompletionEvent
			if tc.wantCompleted == 1 {
				ev = spy.completed[0]
			} else {
				ev = spy.failed[0].Event
				if spy.failed[0].Stage != tc.wantStage {
					t.Errorf("recorded stage %q, want %q", spy.failed[0].Stage, tc.wantStage)
				}
			}
			if !ev.Streaming {
				t.Error("a streamed request was not recorded as streaming")
			}
			if ev.StatusCode != tc.wantStatus {
				t.Errorf("event records status %d, want %d", ev.StatusCode, tc.wantStatus)
			}
			if ev.Provider != "test-provider" {
				t.Errorf("event carries provider %q, want the configured key test-provider; "+
					"adapter.Name() is shared across providers", ev.Provider)
			}
		})
	}
}

// blockingReader never returns data, so a stream reading from it stalls until a
// timeout or a disconnect ends it.
type blockingReader struct{}

func (blockingReader) Read([]byte) (int, error) {
	select {}
}
