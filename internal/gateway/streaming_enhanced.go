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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/cost"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/httputil"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router/adapters"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/storage"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/telemetry"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// maxProviderErrorRead bounds how much of a failed provider response is read
// before it is redacted and excerpted. It is larger than
// adapters.MaxProviderErrorExcerpt so that a secret straddling the excerpt
// boundary is still inside the scanned text and gets replaced rather than
// clipped, and small enough that an oversized error body is not a way to spend
// the gateway's memory.
const maxProviderErrorRead = 8 << 10

// statusClientClosedRequest is nginx's 499, the conventional code for a caller
// that hung up before the response finished. Go's net/http does not define it.
// It is recorded in the audit row so a disconnect is distinguishable from a
// stream the caller actually read to the end; nothing is sent to the client,
// which has already gone.
const statusClientClosedRequest = 499

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
// StreamOutcome is how a stream ended. It exists so HandleStream can emit
// exactly one audit event for the stream, and the right one.
//
// streamWithMonitoring returns normally from six different places: a clean end,
// a client disconnect, two timeouts, a read error and a relay error. Before
// this they were indistinguishable to the caller, which recorded HTTP 200 and a
// usage row for every one of them. A completeness claim cannot be built on a
// function that reports a timeout as a success.
type StreamOutcome string

const (
	// StreamOutcomeUnset is the zero value and never a real outcome. A path
	// that returns without setting one is a bug, and it is recorded as a
	// provider failure rather than silently attested as a success.
	StreamOutcomeUnset StreamOutcome = ""
	// StreamOutcomeCompleted is a stream that ended at [DONE] or at the end of
	// the provider's response.
	StreamOutcomeCompleted StreamOutcome = "completed"
	// StreamOutcomeClientDisconnected is the caller going away mid-stream. The
	// gateway did its work and the provider was billed, so this is a
	// completion, not a failure.
	StreamOutcomeClientDisconnected StreamOutcome = "client_disconnected"
	// StreamOutcomeTimeout is a stream that stalled, per chunk or in total.
	StreamOutcomeTimeout StreamOutcome = "timeout"
	// StreamOutcomeReadError is a scanner or relay failure mid-stream.
	StreamOutcomeReadError StreamOutcome = "read_error"
	// StreamOutcomeNotSupported is a ResponseWriter that cannot flush.
	StreamOutcomeNotSupported StreamOutcome = "not_supported"
)

// HTTPStatus is the status a stream ending this way is recorded under.
//
// One mapping, because three sinks record the outcome of the same request: the
// audit event, the Prometheus request counter, and the usage record. They were
// allowed to disagree once, and the result was a stream sealed as a 504 timeout
// and billed as a 200 success under one request id, which is exactly the
// contradiction a reader joining audit_events to usage_records would hit.
//
// 200 for a completion. 499 for a client that hung up, which is not an error on
// anyone's part but is not a delivered response either. 504 for a stall, 502
// for a read failure, 500 for a writer that cannot flush.
func (o StreamOutcome) HTTPStatus() int {
	switch o {
	case StreamOutcomeCompleted:
		return http.StatusOK
	case StreamOutcomeClientDisconnected:
		return statusClientClosedRequest
	case StreamOutcomeTimeout:
		return http.StatusGatewayTimeout
	case StreamOutcomeReadError:
		return http.StatusBadGateway
	case StreamOutcomeNotSupported:
		return http.StatusInternalServerError
	default:
		// StreamOutcomeUnset. A path that recorded no outcome is a bug, and
		// the safe reading of an unknown ending is not "it succeeded".
		return http.StatusInternalServerError
	}
}

// Succeeded reports whether the stream delivered what it was asked for.
//
// A client disconnect counts: the gateway did its work and the provider was
// engaged and will bill for it. What the caller did with the bytes afterwards
// is not a gateway failure.
func (o StreamOutcome) Succeeded() bool {
	return o == StreamOutcomeCompleted || o == StreamOutcomeClientDisconnected
}

type StreamMetrics struct {
	// Outcome is how the stream ended. Always set by streamWithMonitoring
	// before it returns.
	Outcome StreamOutcome

	StartTime          time.Time
	FirstChunkTime     time.Time
	ChunkCount         int
	PromptTokens       int
	CachedTokens       int
	CacheWrite5mTokens int
	CacheWrite1hTokens int
	CompletionTokens   int
	TotalTokens        int
	EstimatedCostUSD   float64
	Provider           string
	Model              string
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
		if sh.handler.auditLogger != nil {
			sh.handler.auditLogger.LogProviderFailure(
				completionEvent(reqID, authInfo, providerKey, aegisReq.Model, true,
					http.StatusServiceUnavailable, r.RemoteAddr),
				audit.FailureProviderUnreachable)
		}

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
		// Bounded read. The body is only used to build a redacted excerpt, so
		// reading an unbounded one costs memory for nothing, and a
		// misconfigured proxy in front of a provider can return a very large
		// one. The extra byte distinguishes "exactly at the cap" from "longer
		// than the cap" for the truncation notice.
		body, _ := io.ReadAll(io.LimitReader(providerResp.Body, maxProviderErrorRead+1))
		_ = providerResp.Body.Close()
		// The provider's error body is never logged verbatim. It is text the
		// gateway does not control, it can quote the request back, and it
		// arrives at a JSON log handler where an embedded newline forges a
		// record. What goes in the line is the status, the identifiers needed
		// to correlate it, and a bounded redacted excerpt.
		slog.Error("streaming provider returned error",
			"request_id", reqID,
			"status", providerResp.StatusCode,
			"provider", providerKey,
			"adapter", adapter.Name(),
			"body_excerpt", adapters.RedactProviderError(body),
		)

		if sh.handler.metrics != nil {
			sh.handler.metrics.RecordStreamingError(adapter.Name(), fmt.Sprintf("http_%d", providerResp.StatusCode))
		}

		if sh.handler.auditLogger != nil {
			sh.handler.auditLogger.LogProviderFailure(
				completionEvent(reqID, authInfo, providerKey, aegisReq.Model, true,
					http.StatusInternalServerError, r.RemoteAddr),
				audit.FailureProviderHTTPError)
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
		// See the non-streaming path: Calculate, not CalculateSimple, so the
		// cached subset is priced at the cached_input rate.
		if cost, found := sh.handler.costCalc.Calculate(cost.RequestDetails{
			Provider:           metrics.Provider,
			Model:              metrics.Model,
			PromptTokens:       metrics.PromptTokens,
			CachedTokens:       metrics.CachedTokens,
			CacheWrite5mTokens: metrics.CacheWrite5mTokens,
			CacheWrite1hTokens: metrics.CacheWrite1hTokens,
			CompletionTokens:   metrics.CompletionTokens,
		}); found {
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

	// The one status this stream is recorded under, everywhere. Resolved before
	// the three sinks below so none of them can carry a different answer.
	streamStatus := metrics.Outcome.HTTPStatus()

	// Exactly one audit event for the stream, chosen by how it ended.
	//
	// This is the only emit on the post-stream path, and every return above it
	// emits its own and stops, so a stream produces one event on every route
	// through this function. A completion and a failure are mutually exclusive
	// here by construction rather than by inspection.
	//
	// A client disconnect is a completion, not a failure: the gateway did its
	// work and the provider was engaged and will bill for it, and recording it
	// as a provider failure would blame an upstream for a caller hanging up.
	// The status code says 499 so the record still distinguishes it.
	if sh.handler.auditLogger != nil {
		// aegisReq.Model is the concrete provider model: handler.go overwrites
		// it with providerModel before dispatch. Config-derived, so no
		// provider-supplied text enters the sealed record.
		ev := completionEvent(reqID, authInfo, providerKey, aegisReq.Model, true,
			streamStatus, r.RemoteAddr)
		switch metrics.Outcome {
		case StreamOutcomeCompleted, StreamOutcomeClientDisconnected:
			sh.handler.auditLogger.LogRequestComplete(ev)
		case StreamOutcomeTimeout:
			sh.handler.auditLogger.LogProviderFailure(ev, audit.FailureStreamTimeout)
		case StreamOutcomeReadError:
			sh.handler.auditLogger.LogProviderFailure(ev, audit.FailureStreamRead)
		case StreamOutcomeNotSupported:
			sh.handler.auditLogger.LogProviderFailure(ev, audit.FailureStreamNotSupported)
		default:
			slog.Error("stream ended with no recorded outcome; recording a provider failure",
				"request_id", reqID)
			sh.handler.auditLogger.LogProviderFailure(ev, audit.FailureStreamRead)
		}
	}

	slog.Info("streaming completed",
		"request_id", reqID,
		"outcome", string(metrics.Outcome),
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
			Status:           strconv.Itoa(streamStatus),
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
			RequestID:          reqID,
			OrganizationID:     authInfo.OrganizationID,
			TeamID:             authInfo.TeamID,
			UserID:             authInfo.UserID,
			APIKeyID:           authInfo.KeyID,
			ModelRequested:     originalModel,
			ModelServed:        metrics.Model,
			Provider:           metrics.Provider,
			Classification:     string(authInfo.MaxClassification),
			PromptTokens:       metrics.PromptTokens,
			CompletionTokens:   metrics.CompletionTokens,
			TotalTokens:        metrics.TotalTokens,
			CachedTokens:       metrics.CachedTokens,
			CacheWrite5mTokens: metrics.CacheWrite5mTokens,
			CacheWrite1hTokens: metrics.CacheWrite1hTokens,
			EstimatedCostUSD:   metrics.EstimatedCostUSD,
			DurationMs:         totalDuration.Milliseconds(),
			// Not http.StatusOK. A stream that stalled, hit a read error, or
			// lost its client is not a success, and recording one here while
			// the audit event records 504, 502 or 499 makes the two tables
			// disagree about the same request_id. streamStatus is the single
			// mapping all three sinks read; see StreamOutcome.HTTPStatus.
			StatusCode: streamStatus,
			Project:    aegisReq.Project,
			Stream:     true,
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
		return StreamMetrics{Outcome: StreamOutcomeNotSupported}
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
			// ctx is r.Context() wrapped in a total-stream deadline, so it
			// ends for two unrelated reasons and the audit record must not
			// confuse them: the deadline passing is the provider stalling,
			// while a plain cancellation is the caller hanging up. Reading
			// ctx.Err() is what tells them apart.
			//
			// A separate goroutine used to watch the same channel and feed a
			// clientDisconnected case in this select. Two ready cases on one
			// signal meant select chose between them at random, so a
			// disconnect was reported as a timeout about half the time.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				slog.Warn("stream total timeout exceeded",
					"request_id", reqID,
					"chunks_sent", metrics.ChunkCount,
				)
				if sh.handler.metrics != nil {
					sh.handler.metrics.RecordStreamingError(adapter.Name(), "total_timeout")
				}
				_, _ = fmt.Fprintf(w, "data: {\"error\": \"timeout\"}\n\n")
				flusher.Flush()
				metrics.Outcome = StreamOutcomeTimeout
				return metrics
			}
			slog.Info("client disconnected during streaming",
				"request_id", reqID,
				"chunks_sent", metrics.ChunkCount,
			)
			if sh.handler.metrics != nil {
				sh.handler.metrics.RecordStreamingError(adapter.Name(), "client_disconnect")
			}
			// Nothing is written back: the client has gone.
			metrics.Outcome = StreamOutcomeClientDisconnected
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
			metrics.Outcome = StreamOutcomeTimeout
			return metrics

		case <-scanChan:
			// Scanner finished
			metrics.Outcome = StreamOutcomeCompleted
			if err := scanner.Err(); err != nil {
				slog.Error("error reading stream", "error", err, "provider", adapter.Name())
				if sh.handler.metrics != nil {
					sh.handler.metrics.RecordStreamingError(adapter.Name(), "scanner_error")
				}
				metrics.Outcome = StreamOutcomeReadError
			}
			return metrics

		case line := <-lineChan:
			// Process chunk
			if err := sh.processChunk(w, flusher, line, adapter, transformer, &metrics, toolCalls); err != nil {
				slog.Error("error processing chunk", "error", err)
				if sh.handler.metrics != nil {
					sh.handler.metrics.RecordStreamingError(adapter.Name(), "chunk_processing_error")
				}
				metrics.Outcome = StreamOutcomeReadError
				return metrics
			}

			// Check if stream ended
			if strings.Contains(line, "[DONE]") {
				metrics.Outcome = StreamOutcomeCompleted
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
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			TotalTokens         int `json:"total_tokens"`
			PromptTokensDetails *struct {
				CachedTokens       int `json:"cached_tokens"`
				CacheWrite5mTokens int `json:"cache_write_5m_tokens"`
				CacheWrite1hTokens int `json:"cache_write_1h_tokens"`
			} `json:"prompt_tokens_details"`
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
		if d := chunkData.Usage.PromptTokensDetails; d != nil {
			metrics.CachedTokens = d.CachedTokens
			metrics.CacheWrite5mTokens = d.CacheWrite5mTokens
			metrics.CacheWrite1hTokens = d.CacheWrite1hTokens
		}
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
