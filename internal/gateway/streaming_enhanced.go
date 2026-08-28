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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/httputil"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router/adapters"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/storage"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/telemetry"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// StreamingConfig holds configuration for streaming behavior.
type StreamingConfig struct {
	PerChunkTimeout time.Duration // Timeout for each individual chunk
	TotalTimeout    time.Duration // Total stream timeout
	BufferSize      int           // Scanner buffer size
	MaxBufferSize   int           // Maximum scanner buffer size
}

// DefaultStreamingConfig returns sensible defaults for streaming.
func DefaultStreamingConfig() StreamingConfig {
	return StreamingConfig{
		PerChunkTimeout: 30 * time.Second, // 30s per chunk
		TotalTimeout:    5 * time.Minute,  // 5 min total
		BufferSize:      64 * 1024,        // 64KB initial
		MaxBufferSize:   1024 * 1024,      // 1MB max
	}
}

// StreamMetrics tracks metrics during a streaming session.
type StreamMetrics struct {
	StartTime        time.Time
	FirstChunkTime   time.Time
	ChunkCount       int
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	EstimatedCostUSD float64
	Provider         string
	Model            string
	// ToolCallNames are the tool names reconstructed from the stream's tool
	// call deltas, in index order. Names only, never arguments.
	ToolCallNames []string
}

// StreamingHandler manages enhanced streaming with metrics, timeouts, and cost tracking.
type StreamingHandler struct {
	handler *Handler
	config  StreamingConfig
}

// NewStreamingHandler creates a new streaming handler with configuration.
func NewStreamingHandler(handler *Handler, config StreamingConfig) *StreamingHandler {
	return &StreamingHandler{
		handler: handler,
		config:  config,
	}
}

// HandleStream sends the request to the provider and forwards SSE chunks with full monitoring.
// providerKey is the configured provider name from the resolved route.
// adapter.Name() is only the adapter type: it is shared across providers
// (azure_openai and internal_vllm both report "openai") and it is "mock" for
// every provider when AEGIS_MOCK_PROVIDER is set. Pricing rows in
// configs/pricing.yaml and the persisted usage record are keyed by the
// configured name, so both must use providerKey. The non-streaming path
// already does this; this path used to attribute to the adapter type.
func (sh *StreamingHandler) HandleStream(
	w http.ResponseWriter,
	r *http.Request,
	reqID string,
	providerReq *http.Request,
	adapter adapters.ProviderAdapter,
	providerKey string,
	originalModel string,
	authInfo *auth.AuthInfo,
	aegisReq *types.AegisRequest,
) {
	receivedAt := time.Now()

	// Create context with total timeout
	ctx, cancel := context.WithTimeout(r.Context(), sh.config.TotalTimeout)
	defer cancel()

	// Update provider request with timeout context
	providerReq = providerReq.WithContext(ctx)

	// Send request to provider
	providerResp, err := adapter.SendRequest(providerReq)
	if err != nil {
		slog.Error("streaming provider request failed", "error", err, "provider", adapter.Name())

		// Record failure metrics
		if sh.handler.healthTracker != nil {
			sh.handler.healthTracker.RecordFailure(adapter.Name())
		}
		if sh.handler.metrics != nil {
			sh.handler.metrics.RecordStreamingError(adapter.Name(), "request_failed")
		}

		httputil.WriteServiceUnavailableError(w, reqID, "Provider request failed")
		return
	}

	if providerResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(providerResp.Body)
		_ = providerResp.Body.Close()
		slog.Error("streaming provider returned error",
			"status", providerResp.StatusCode,
			"provider", adapter.Name(),
			"body", string(body),
		)

		if sh.handler.metrics != nil {
			sh.handler.metrics.RecordStreamingError(adapter.Name(), fmt.Sprintf("http_%d", providerResp.StatusCode))
		}

		httputil.WriteInternalError(w, reqID, "Provider returned error")
		return
	}

	slog.Info("streaming started",
		"request_id", reqID,
		"model_requested", originalModel,
		"provider", adapter.Name(),
		"org_id", authInfo.OrganizationID,
	)

	// Execute streaming with full monitoring
	metrics := sh.streamWithMonitoring(ctx, w, reqID, providerResp, adapter, providerKey, authInfo)

	totalDuration := time.Since(receivedAt)

	// Calculate final cost if we have token counts.
	if sh.handler.costCalc != nil && metrics.TotalTokens > 0 {
		if cost, found := sh.handler.costCalc.CalculateSimple(
			metrics.Provider,
			metrics.Model,
			metrics.PromptTokens,
			metrics.CompletionTokens,
		); found {
			metrics.EstimatedCostUSD = cost
		} else {
			slog.Warn("pricing_unknown: no pricing data for served model",
				"event_type", "pricing_unknown",
				"provider", metrics.Provider,
				"model", metrics.Model,
				"request_id", reqID,
			)
		}
	}

	slog.Info("streaming completed",
		"request_id", reqID,
		"model_requested", originalModel,
		"model_served", metrics.Model,
		"provider", metrics.Provider,
		"chunks", metrics.ChunkCount,
		"prompt_tokens", metrics.PromptTokens,
		"completion_tokens", metrics.CompletionTokens,
		"total_tokens", metrics.TotalTokens,
		"estimated_cost_usd", metrics.EstimatedCostUSD,
		"duration_ms", totalDuration.Milliseconds(),
		"time_to_first_token_ms", metrics.FirstChunkTime.Sub(metrics.StartTime).Milliseconds(),
		// The same pair the non-streaming path logs, so the two are comparable.
		// tools_returned is reconstructed from the stream's index-keyed deltas,
		// which is the only place a streamed response records it.
		"tools_offered", len(aegisReq.Tools),
		"tools_called", len(aegisReq.CalledToolNames()),
		"tools_returned", len(metrics.ToolCallNames),
		"org_id", authInfo.OrganizationID,
	)

	// Record Prometheus metrics
	if sh.handler.metrics != nil {
		sh.handler.metrics.RecordRequest(telemetry.RequestLabels{
			Org:              authInfo.OrganizationID,
			Team:             authInfo.TeamID,
			Model:            originalModel,
			Provider:         metrics.Provider,
			Status:           "200",
			Classification:   string(authInfo.MaxClassification),
			DurationMs:       float64(totalDuration.Milliseconds()),
			OverheadMs:       float64(totalDuration.Milliseconds()),
			PromptTokens:     metrics.PromptTokens,
			CompletionTokens: metrics.CompletionTokens,
			CostUSD:          metrics.EstimatedCostUSD,
		})

		// Record streaming-specific metrics
		timeToFirstToken := metrics.FirstChunkTime.Sub(metrics.StartTime)
		sh.handler.metrics.RecordStreamingMetrics(telemetry.StreamingLabels{
			Provider:           metrics.Provider,
			Model:              originalModel,
			ChunkCount:         metrics.ChunkCount,
			TimeToFirstTokenMs: float64(timeToFirstToken.Milliseconds()),
			TokensPerSecond:    sh.calculateTokensPerSecond(metrics.CompletionTokens, totalDuration),
			StreamDurationMs:   float64(totalDuration.Milliseconds()),
		})
	}

	// Record usage asynchronously
	if sh.handler.usageRecorder != nil {
		sh.handler.usageRecorder.RecordUsage(storage.UsageRecord{
			RequestID:        reqID,
			OrganizationID:   authInfo.OrganizationID,
			TeamID:           authInfo.TeamID,
			UserID:           authInfo.UserID,
			APIKeyID:         authInfo.KeyID,
			ModelRequested:   originalModel,
			ModelServed:      metrics.Model,
			Provider:         metrics.Provider,
			Classification:   string(authInfo.MaxClassification),
			PromptTokens:     metrics.PromptTokens,
			CompletionTokens: metrics.CompletionTokens,
			TotalTokens:      metrics.TotalTokens,
			EstimatedCostUSD: metrics.EstimatedCostUSD,
			DurationMs:       totalDuration.Milliseconds(),
			StatusCode:       http.StatusOK,
			Project:          aegisReq.Project,
			Stream:           true,
		})
	}
}

// streamWithMonitoring handles the actual streaming with timeouts and monitoring.
func (sh *StreamingHandler) streamWithMonitoring(
	ctx context.Context,
	w http.ResponseWriter,
	reqID string,
	providerResp *http.Response,
	adapter adapters.ProviderAdapter,
	providerKey string,
	authInfo *auth.AuthInfo,
) (result StreamMetrics) {
	defer func() { _ = providerResp.Body.Close() }()

	flusher, ok := w.(http.Flusher)
	if !ok {
		httputil.WriteInternalError(w, reqID, "Streaming not supported")
		return StreamMetrics{}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Request-ID", reqID)
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	metrics := StreamMetrics{
		StartTime: time.Now(),
		// The configured provider name, not adapter.Name(). See HandleStream.
		Provider: providerKey,
	}

	// Tool calls arrive as index-keyed fragments across many chunks. The relay
	// below forwards every chunk byte for byte, so the client reconstructs them
	// itself; this accumulator exists so the gateway's own record of the
	// request can say which tools were called, which on a streamed response is
	// a fact that exists nowhere else.
	toolCalls := newToolCallAccumulator()

	// One transformer per stream. Anthropic's translation carries state for the
	// life of a response (it maps content block indices onto tool call
	// ordinals), and the adapter is shared across every concurrent request, so
	// that state cannot live on the adapter. Adapters that need none get a
	// passthrough wrapper around TransformStreamChunk.
	transformer := adapters.NewStreamTransformerFor(adapter)

	// A stream can end at any of six points below, including a timeout and a
	// client disconnect. Attaching the reconstructed names on the way out
	// rather than at each return means a path added later cannot forget to.
	defer func() { result.ToolCallNames = toolCalls.ToolNames() }()

	scanner := bufio.NewScanner(providerResp.Body)
	scanner.Buffer(make([]byte, 0, sh.config.BufferSize), sh.config.MaxBufferSize)

	// Channel to detect client disconnect via context cancellation
	clientDisconnected := make(chan bool, 1)
	go func() {
		<-ctx.Done()
		clientDisconnected <- true
	}()

	// Channel for per-chunk timeout
	chunkTimer := time.NewTimer(sh.config.PerChunkTimeout)
	defer chunkTimer.Stop()

	scanChan := make(chan bool)
	lineChan := make(chan string)

	// Scanner goroutine
	go func() {
		for scanner.Scan() {
			select {
			case lineChan <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		close(scanChan)
	}()

	for {
		// Reset chunk timer for each iteration
		chunkTimer.Reset(sh.config.PerChunkTimeout)

		select {
		case <-ctx.Done():
			slog.Warn("stream total timeout exceeded",
				"request_id", reqID,
				"chunks_sent", metrics.ChunkCount,
			)
			if sh.handler.metrics != nil {
				sh.handler.metrics.RecordStreamingError(adapter.Name(), "total_timeout")
			}
			_, _ = fmt.Fprintf(w, "data: {\"error\": \"timeout\"}\n\n")
			flusher.Flush()
			return metrics

		case <-chunkTimer.C:
			slog.Warn("stream chunk timeout",
				"request_id", reqID,
				"chunks_sent", metrics.ChunkCount,
			)
			if sh.handler.metrics != nil {
				sh.handler.metrics.RecordStreamingError(adapter.Name(), "chunk_timeout")
			}
			_, _ = fmt.Fprintf(w, "data: {\"error\": \"chunk timeout\"}\n\n")
			flusher.Flush()
			return metrics

		case <-clientDisconnected:
			slog.Info("client disconnected during streaming",
				"request_id", reqID,
				"chunks_sent", metrics.ChunkCount,
			)
			if sh.handler.metrics != nil {
				sh.handler.metrics.RecordStreamingError(adapter.Name(), "client_disconnect")
			}
			return metrics

		case <-scanChan:
			// Scanner finished
			if err := scanner.Err(); err != nil {
				slog.Error("error reading stream", "error", err, "provider", adapter.Name())
				if sh.handler.metrics != nil {
					sh.handler.metrics.RecordStreamingError(adapter.Name(), "scanner_error")
				}
			}
			return metrics

		case line := <-lineChan:
			// Process chunk
			if err := sh.processChunk(w, flusher, line, adapter, transformer, &metrics, toolCalls); err != nil {
				slog.Error("error processing chunk", "error", err)
				if sh.handler.metrics != nil {
					sh.handler.metrics.RecordStreamingError(adapter.Name(), "chunk_processing_error")
				}
				return metrics
			}

			// Check if stream ended
			if strings.Contains(line, "[DONE]") {
				return metrics
			}
		}
	}
}

// processChunk handles a single SSE chunk with token counting.
func (sh *StreamingHandler) processChunk(
	w http.ResponseWriter,
	flusher http.Flusher,
	line string,
	adapter adapters.ProviderAdapter,
	transformer adapters.StreamTransformer,
	metrics *StreamMetrics,
	toolCalls *toolCallAccumulator,
) error {
	// SSE format: lines starting with "data: "
	if !strings.HasPrefix(line, "data: ") {
		// Forward event lines or empty lines as-is for keep-alive
		if strings.HasPrefix(line, "event: ") || line == "" {
			_, _ = fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()
		}
		return nil
	}

	data := strings.TrimPrefix(line, "data: ")

	// End of stream
	if data == "[DONE]" {
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
		return nil
	}

	// Transform chunk through this stream's transformer
	transformed, err := transformer.Transform([]byte(data))
	if err != nil {
		return fmt.Errorf("transform chunk failed: %w", err)
	}

	// nil means skip this chunk (e.g., Anthropic non-content events)
	if transformed == nil {
		return nil
	}

	// Check if the adapter signaled end of stream
	if string(transformed) == "[DONE]" {
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
		return nil
	}

	// Track time to first chunk
	if metrics.ChunkCount == 0 {
		metrics.FirstChunkTime = time.Now()
	}

	metrics.ChunkCount++

	// Extract token counts and model info from chunk (OpenAI format)
	if err := sh.extractTokensFromChunk(transformed, metrics); err != nil {
		// Non-fatal - just log
		slog.Debug("failed to extract tokens from chunk", "error", err)
	}

	// Fold any tool call delta into the accumulator. This observes the bytes
	// that are about to be forwarded and never alters them: the client gets
	// the provider's chunk exactly as it arrived, which is what lets a client
	// doing its own index-based accumulation reconstruct the call.
	if toolCalls != nil {
		toolCalls.Observe(transformed)
	}

	// Forward to client
	_, _ = fmt.Fprintf(w, "data: %s\n\n", transformed)
	flusher.Flush()

	return nil
}

// extractTokensFromChunk attempts to parse token usage from a streaming chunk.
func (sh *StreamingHandler) extractTokensFromChunk(chunk []byte, metrics *StreamMetrics) error {
	var chunkData struct {
		Model string `json:"model"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(chunk, &chunkData); err != nil {
		return err
	}

	// Update model if present
	if chunkData.Model != "" && metrics.Model == "" {
		metrics.Model = chunkData.Model
	}

	// Update token counts if present
	if chunkData.Usage != nil {
		metrics.PromptTokens = chunkData.Usage.PromptTokens
		metrics.CompletionTokens = chunkData.Usage.CompletionTokens
		metrics.TotalTokens = chunkData.Usage.TotalTokens
	}

	return nil
}

// calculateTokensPerSecond calculates the tokens per second rate.
func (sh *StreamingHandler) calculateTokensPerSecond(tokens int, duration time.Duration) float64 {
	if duration.Seconds() == 0 {
		return 0
	}
	return float64(tokens) / duration.Seconds()
}
