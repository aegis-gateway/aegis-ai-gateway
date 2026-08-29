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
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router/adapters"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// captureLogs redirects the default slog logger to a buffer for the duration of
// the test and returns the decoded records.
//
// A JSON handler, because that is what cmd/gateway installs, and because the
// point of collapsing control characters is that an attacker-influenced value
// cannot end one JSON record and begin another. Decoding line by line is what
// makes that assertion meaningful.
func captureLogs(t *testing.T, fn func()) []map[string]any {
	t.Helper()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	fn()

	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log output is not one JSON object per line, which means a value "+
				"forged a record boundary: %v\nline: %s", err, line)
		}
		records = append(records, rec)
	}
	return records
}

func findRecord(records []map[string]any, msg string) map[string]any {
	for _, r := range records {
		if r["msg"] == msg {
			return r
		}
	}
	return nil
}

// oversizedProviderError is a provider error body far past the excerpt bound,
// carrying a canary and AWS's documentation example key. Both must be absent
// from the emitted record: the canary because it is past the bound, the key
// because it is redacted.
func oversizedProviderError() []byte {
	return []byte(`{"error":{"type":"invalid_request_error","message":"rejected ` +
		fakeAWSKeyForLogTest + ` ` + strings.Repeat("PADDING", 4000) +
		` CANARY_TAIL_2f9c1a"}}`)
}

const fakeAWSKeyForLogTest = "AKIAIOSFODNN7EXAMPLE"

// TestStreamingProviderErrorIsNotLoggedVerbatim is the regression test for
// streaming_enhanced.go logging the whole provider error body.
//
// The bound matters because the body is unbounded text the gateway does not
// control, and a provider that rejects a request commonly quotes the request
// back.
func TestStreamingProviderErrorIsNotLoggedVerbatim(t *testing.T) {
	body := oversizedProviderError()

	adapter := &mockStreamAdapter{
		name: "openai",
		response: &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
		},
	}

	handler := &Handler{metrics: getTestMetrics()}
	sh := NewStreamingHandler(handler, StreamingConfig{
		PerChunkTimeout: time.Second,
		TotalTimeout:    5 * time.Second,
		BufferSize:      64 * 1024,
		MaxBufferSize:   1024 * 1024,
	})

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	providerReq, _ := http.NewRequest("POST", "http://mock-provider.com", nil)

	records := captureLogs(t, func() {
		sh.HandleStream(w, req, "req-stream-err", providerReq, adapter, "azure_openai",
			"aegis-fast", &auth.AuthInfo{OrganizationID: "org", TeamID: "team", KeyID: "key"},
			&types.AegisRequest{Model: "aegis-fast", Stream: true})
	})

	rec := findRecord(records, "streaming provider returned error")
	if rec == nil {
		t.Fatalf("no provider-error record was emitted; got %d record(s)", len(records))
	}

	// Positive control. Without asserting the excerpt is present, "the canary
	// is absent" is satisfied by a record that logs nothing at all.
	excerpt, ok := rec["body_excerpt"].(string)
	if !ok {
		t.Fatalf("the record carries no body_excerpt field: %v", rec)
	}
	if excerpt == "" {
		t.Fatal("body_excerpt is empty; the test cannot distinguish truncation from omission")
	}

	// The record must never carry the body under its old key.
	if _, present := rec["body"]; present {
		t.Error(`the record still carries a "body" field; the whole body is what this test forbids`)
	}

	if len(excerpt) > adapters.MaxProviderErrorExcerpt+64 {
		t.Errorf("body_excerpt is %d bytes for a %d byte body; the bound is %d plus a short notice",
			len(excerpt), len(body), adapters.MaxProviderErrorExcerpt)
	}
	if strings.Contains(excerpt, "CANARY_TAIL_2f9c1a") {
		t.Error("the tail of the provider body reached the log record; it was not truncated")
	}
	if strings.Contains(excerpt, fakeAWSKeyForLogTest) {
		t.Error("a credential quoted back by the provider reached the log record")
	}
	if !strings.Contains(excerpt, "invalid_request_error") {
		t.Errorf("the excerpt lost the part an operator needs: %q", excerpt)
	}

	// The correlating fields the excerpt replaces the body with.
	for _, field := range []string{"request_id", "status", "provider"} {
		if _, present := rec[field]; !present {
			t.Errorf("the record carries no %q field", field)
		}
	}
	if rec["provider"] != "azure_openai" {
		t.Errorf("the record names provider %v, want the configured key azure_openai; "+
			"adapter.Name() is shared across providers", rec["provider"])
	}
}

// TestNonStreamingProviderErrorIsNotLoggedVerbatim covers the other path. There
// the body is wrapped into an error by the adapter and logged by the handler,
// so the redaction has to happen where the body is read.
func TestNonStreamingProviderErrorIsNotLoggedVerbatim(t *testing.T) {
	body := oversizedProviderError()

	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
	}

	a := adapters.NewOpenAIAdapter(config.ProviderConfig{}, nil)
	_, err := a.TransformResponse(t.Context(), resp)
	if err == nil {
		t.Fatal("a non-200 provider response produced no error")
	}

	records := captureLogs(t, func() {
		slog.Error("failed to transform response",
			"request_id", "req-nonstream-err",
			"error", err,
			"provider", "azure_openai",
			"adapter", a.Name(),
		)
	})

	rec := findRecord(records, "failed to transform response")
	if rec == nil {
		t.Fatal("no record was emitted")
	}
	msg, _ := rec["error"].(string)
	if msg == "" {
		t.Fatal("the record carries no error text; the test cannot distinguish truncation from omission")
	}
	if strings.Contains(msg, "CANARY_TAIL_2f9c1a") {
		t.Error("the tail of the provider body reached the log record through the wrapped error")
	}
	if strings.Contains(msg, fakeAWSKeyForLogTest) {
		t.Error("a credential quoted back by the provider reached the log record")
	}
	if len(msg) > adapters.MaxProviderErrorExcerpt+128 {
		t.Errorf("the wrapped error is %d bytes for a %d byte body", len(msg), len(body))
	}
	if !strings.Contains(msg, "invalid_request_error") {
		t.Errorf("the excerpt lost the part an operator needs: %q", msg)
	}
}
