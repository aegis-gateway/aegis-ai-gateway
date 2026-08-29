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
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/redact"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// captureLogs redirects the default slog logger into a buffer for the duration
// of a test. Both paths under test log through slog's package-level default, so
// the assertion has to be made on what that actually emits rather than on a
// value the test computes for itself.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// erroringStreamAdapter returns a provider error whose body is far longer than
// redact.ExcerptLimit and contains a marker at the end.
type erroringStreamAdapter struct {
	body string
}

func (e *erroringStreamAdapter) Name() string { return "openai" }
func (e *erroringStreamAdapter) TransformRequest(ctx context.Context, req *types.AegisRequest) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodPost, "http://provider.invalid/v1/chat/completions", nil)
}
func (e *erroringStreamAdapter) SendRequest(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(e.body)),
		Header:     make(http.Header),
	}, nil
}
func (e *erroringStreamAdapter) TransformResponse(ctx context.Context, resp *http.Response) (*types.AegisResponse, error) {
	return nil, nil
}
func (e *erroringStreamAdapter) TransformStreamChunk(chunk []byte) ([]byte, error) { return chunk, nil }
func (e *erroringStreamAdapter) SupportsStreaming() bool                           { return true }
func (e *erroringStreamAdapter) SupportsTools() bool                               { return false }

// TestStreamingProviderError_BodyIsTruncatedInTheLogRecord is the assertion the
// work order asks for: a provider error body longer than the bound must be
// truncated in what is actually emitted.
//
// The tail marker is the load-bearing part. Asserting only on length would pass
// against a bound applied to the wrong end of the body, and the risk here is
// caller content echoed back by the provider, which lands anywhere in it.
func TestStreamingProviderError_BodyIsTruncatedInTheLogRecord(t *testing.T) {
	const tailMarker = "CALLER_CONTENT_ECHOED_AT_THE_END"
	body := `{"error":{"message":"` + strings.Repeat("x", 4000) + tailMarker + `"}}`

	buf := captureLogs(t)

	h := newAllowlistTestHandler(&modelDenialSpy{})
	sh := NewStreamingHandler(h, StreamingConfig{})
	adapter := &erroringStreamAdapter{body: body}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(auth.ContextWithAuth(req.Context(), &auth.AuthInfo{
		OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test",
	}))
	w := httptest.NewRecorder()
	providerReq, err := adapter.TransformRequest(req.Context(), &types.AegisRequest{})
	if err != nil {
		t.Fatalf("building the provider request: %v", err)
	}

	sh.HandleStream(w, req, "req-trunc-test", providerReq, adapter, "azure_openai", "aegis-fast", "gpt-test",
		&auth.AuthInfo{OrganizationID: "org-test", TeamID: "team-test", KeyID: "key-test"},
		&types.AegisRequest{Model: "aegis-fast"})

	logged := buf.String()
	if logged == "" {
		t.Fatal("no log record was emitted; the test asserts on the emitted record")
	}

	if strings.Contains(logged, tailMarker) {
		t.Errorf("the end of a %d-byte provider body reached the log record", len(body))
	}
	if strings.Contains(logged, strings.Repeat("x", redact.ExcerptLimit+1)) {
		t.Errorf("more than ExcerptLimit=%d characters of the body reached the log",
			redact.ExcerptLimit)
	}
	if !strings.Contains(logged, "truncated") {
		t.Errorf("the record does not mark the body as truncated: %s", logged)
	}

	// The record must still be useful: status, provider key and request ID.
	var rec map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logged), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err == nil {
			if m["msg"] == "streaming provider returned error" {
				rec = m
			}
		}
	}
	if rec == nil {
		t.Fatalf("no streaming provider error record found in: %s", logged)
	}
	if rec["status"] != float64(http.StatusBadRequest) {
		t.Errorf("record status = %v, want 400", rec["status"])
	}
	if rec["provider"] != "azure_openai" {
		t.Errorf("record provider = %v, want the configured key azure_openai, not the adapter type",
			rec["provider"])
	}
	if rec["request_id"] != "req-trunc-test" {
		t.Errorf("record request_id = %v", rec["request_id"])
	}
}
