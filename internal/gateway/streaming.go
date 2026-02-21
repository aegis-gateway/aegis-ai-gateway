package gateway

import (
	"bufio"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/af-corp/aegis-gateway/internal/httputil"
	"github.com/af-corp/aegis-gateway/internal/router/adapters"
)

// streamSSE reads SSE events from the provider response and forwards them to the client,
// transforming each chunk through the adapter's TransformStreamChunk.
func streamSSE(w http.ResponseWriter, reqID string, providerResp *http.Response, adapter adapters.ProviderAdapter) {
	defer providerResp.Body.Close()

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
				fmt.Fprintf(w, "%s\n", line)
				flusher.Flush()
			}
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		// End of stream
		if data == "[DONE]" {
			fmt.Fprintf(w, "data: [DONE]\n\n")
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
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}

		fmt.Fprintf(w, "data: %s\n\n", transformed)
		flusher.Flush()
	}

	if err := scanner.Err(); err != nil {
		slog.Error("error reading stream", "error", err, "provider", adapter.Name())
	}
}
