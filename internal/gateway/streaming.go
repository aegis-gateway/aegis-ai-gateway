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
	"bufio"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/httputil"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router/adapters"
)

// DEPRECATED and unreachable. Nothing outside streaming_test.go calls this.
//
// The live streaming path is StreamingHandler.HandleStream in
// streaming_enhanced.go, which cmd/gateway routes to. This function predates it
// and has none of what that path acquired: it discards every write and flush
// error, so it cannot tell a delivered response from an abandoned one, and it
// emits no audit event at all, so a request served through it would leave no
// attested record.
//
// Wiring it up would silently undo the delivery checking and the allow-path
// attestation added across #66. If a second streaming implementation is ever
// wanted, start from HandleStream rather than from here, and delete this.
//
// streamSSE reads SSE events from the provider response and forwards them to the client,
// transforming each chunk through the adapter's TransformStreamChunk.
func streamSSE(w http.ResponseWriter, reqID string, providerResp *http.Response, adapter adapters.ProviderAdapter) {
	defer func() { _ = providerResp.Body.Close() }()

	flusher, ok := w.(http.Flusher)
	if !ok {
		httputil.WriteInternalError(w, reqID, "Streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Request-ID", reqID)
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	scanner := bufio.NewScanner(providerResp.Body)
	// Increase scanner buffer for large chunks
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// SSE format: lines starting with "data: "
		if !strings.HasPrefix(line, "data: ") {
			// Forward event: lines or empty lines as-is for keep-alive
			if strings.HasPrefix(line, "event: ") || line == "" {
				_, _ = fmt.Fprintf(w, "%s\n", line)
				flusher.Flush()
			}
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		// End of stream
		if data == "[DONE]" {
			_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}

		// Transform chunk through the adapter
		transformed, err := adapter.TransformStreamChunk([]byte(data))
		if err != nil {
			slog.Error("failed to transform stream chunk", "error", err, "provider", adapter.Name())
			continue
		}

		// nil means skip this chunk (e.g., Anthropic non-content events)
		if transformed == nil {
			continue
		}

		// Check if the adapter signaled end of stream (Anthropic message_stop → [DONE])
		if string(transformed) == "[DONE]" {
			_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}

		_, _ = fmt.Fprintf(w, "data: %s\n\n", transformed)
		flusher.Flush()
	}

	if err := scanner.Err(); err != nil {
		slog.Error("error reading stream", "error", err, "provider", adapter.Name())
	}
}
