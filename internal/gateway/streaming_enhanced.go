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
	"strings"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/cost"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/httputil"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/redact"
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

	// Outcome is how the stream ended. Every exit from the monitoring loop
	// returns the same metrics value, so without this the caller cannot tell a
	// stream that finished from one that timed out, and would attest a
	// truncated response as a completion.
	Outcome StreamOutcome

	// terminatorSeen records that the provider sent its end-of-stream marker:
	// [DONE] from an OpenAI-compatible provider, or message_stop from
	// Anthropic, which the transformer renders as [DONE].
	//
	// EOF alone does not mean the response finished. A provider that closes
	// the connection cleanly part way through produces a nil scanner error and
	// is indistinguishable from a complete stream without this flag, so a
	// truncated answer would be sealed as request_complete.
	terminatorSeen bool
}

// StreamOutcome enumerates how a stream ended. It decides which audit event the
// request produces, so it is a closed set rather than a free string.
type StreamOutcome string

const (
	// StreamOutcomeUnset is the zero value: no exit set an outcome.
	//
	// It exists so that "nobody said" is distinguishable from any real
	// outcome. Letting the zero value fall into a default branch is what
	// silently classified every completed stream as a failure when the [DONE]
	// return was missed, and a seventh exit added later would do it again.
	StreamOutcomeUnset StreamOutcome = ""
	// StreamCompleted means the provider closed the stream normally.
	StreamCompleted StreamOutcome = "completed"
	// StreamTotalTimeout means the whole-stream deadline elapsed.
	StreamTotalTimeout StreamOutcome = "total_timeout"
	// StreamChunkTimeout means the gap between chunks exceeded its deadline.
	StreamChunkTimeout StreamOutcome = "chunk_timeout"
	// StreamClientDisconnected means the caller went away mid-stream.
	StreamClientDisconnected StreamOutcome = "client_disconnect"
	// StreamReadError means reading the provider stream failed.
	StreamReadError StreamOutcome = "read_error"
	// StreamNotStarted means streaming was refused before any header went out,
	// so the gateway sent an error status rather than a stream.
	StreamNotStarted StreamOutcome = "not_started"
	// StreamHeaderUndelivered means the 200 header was written and its flush
	// failed, so nothing of the stream reached the caller.
	//
	// Distinct from StreamNotStarted because the status line differs, and the
	// record has to state what the gateway sent. WriteHeader has already
	// committed 200 by this point and no error status can follow it, so
	// recording 500 here would put a status the caller never received into the
	// sealed record: the same mismatch this PR corrected on every other path.
	StreamHeaderUndelivered StreamOutcome = "header_undelivered"
	// StreamTruncated means the provider closed the stream cleanly without
	// sending its end-of-stream marker, so the client received a partial
	// response that looked like a whole one.
	StreamTruncated StreamOutcome = "truncated"
	// StreamChunkError means a chunk could not be processed or forwarded.
	StreamChunkError StreamOutcome = "chunk_error"
)

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
	providerModel string,
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

		// Written before the event, so the recorded status is one the gateway
		// has sent rather than one it is about to attempt.
		httputil.WriteServiceUnavailableError(w, reqID, "Provider request failed")
		if sh.handler.auditLogger != nil {
			sh.handler.auditLogger.LogProviderFailure(
				completedRequest(reqID, authInfo, r, originalModel, providerKey, http.StatusServiceUnavailable, true),
				providerFailureReason(r, audit.ReasonProviderUnreachable))
		}
		return
	}

	if providerResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(providerResp.Body)
		_ = providerResp.Body.Close()
		// The body is excerpted, never logged whole. A provider error body is
		// unbounded text the gateway does not control and it routinely echoes
		// the caller's own content back, so logging it verbatim writes caller
		// text to the log store, which is a durable copy by any reading of the
		// zero-retention claim.
		//
		// provider is the configured provider key, not adapter.Name(): the
		// adapter type is shared, so azure_openai and internal_vllm both report
		// "openai" and an operator could not tell which one failed.
		slog.Error("streaming provider returned error",
			"request_id", reqID,
			"status", providerResp.StatusCode,
			"provider", providerKey,
			"adapter", adapter.Name(),
			"body_excerpt", redact.Excerpt(body),
		)

		if sh.handler.metrics != nil {
			sh.handler.metrics.RecordStreamingError(adapter.Name(), fmt.Sprintf("http_%d", providerResp.StatusCode))
		}

		// The gateway sends 500 from WriteInternalError, so that is what the
		// record says, and it is written first so the status is one already
		// sent. The upstream status is in the log line above.
		httputil.WriteInternalError(w, reqID, "Provider returned error")
		if sh.handler.auditLogger != nil {
			sh.handler.auditLogger.LogProviderFailure(
				completedRequest(reqID, authInfo, r, originalModel, providerKey, http.StatusInternalServerError, true),
				audit.ReasonProviderError)
		}
		return
	}

	slog.Info("streaming started",
		"request_id", reqID,
		"model_requested", originalModel,
		"provider", adapter.Name(),
		"org_id", authInfo.OrganizationID,
	)

	// Execute streaming with full monitoring
	metrics := sh.streamWithMonitoring(ctx, w, reqID, providerResp, adapter, providerKey, providerModel, authInfo)

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
		"time_to_first_token_ms", firstTokenMsForLog(metrics),
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
		// Only observed when a first chunk actually arrived; see
		// timeToFirstToken. The other streaming metrics are still recorded,
		// because a failed stream's chunk count and duration are real.
		ttft, sawFirstChunk := timeToFirstToken(metrics)
		if !sawFirstChunk {
			ttft = -1
		}
		sh.handler.metrics.RecordStreamingMetrics(telemetry.StreamingLabels{
			Provider:             metrics.Provider,
			Model:                originalModel,
			ChunkCount:           metrics.ChunkCount,
			TimeToFirstTokenMs:   float64(ttft.Milliseconds()),
			OmitTimeToFirstToken: !sawFirstChunk,
			TokensPerSecond:      sh.calculateTokensPerSecond(metrics.CompletionTokens, totalDuration),
			StreamDurationMs:     float64(totalDuration.Milliseconds()),
		})
	}

	// Attest the streamed request, exactly once, from one place.
	//
	// Every exit from streamWithMonitoring returns the same metrics value, so
	// this is the only point that can tell a completed stream from an
	// interrupted one, and the only point that runs for all of them. Emitting
	// inside the loop instead would mean six call sites and a real chance of
	// two events or none.
	//
	// The status code recorded is what the CLIENT actually received. A stream
	// sends its 200 header before the first chunk, so a stream that later times
	// out or is abandoned was still a 200 on the wire; recording anything else
	// would be inventing a response that was never sent. The event type and
	// reason carry the outcome instead.
	if sh.handler.auditLogger != nil {
		rec := completedRequest(reqID, authInfo, r, originalModel, providerKey,
			clientStatusFor(metrics.Outcome), true)
		switch metrics.Outcome {
		case StreamOutcomeUnset:
			// A path out of the monitoring loop set no outcome. The event is
			// still written, because dropping it would be a hole in the record,
			// but it is recorded as interrupted rather than guessed as a
			// success, and it says loudly that the gateway has a bug.
			slog.Error("stream ended with no outcome set; attesting it as interrupted",
				"request_id", reqID,
				"chunks", metrics.ChunkCount,
				"fix", "every return from streamWithMonitoring must set metrics.Outcome",
			)
			sh.handler.auditLogger.LogProviderFailure(rec, audit.ReasonStreamInterrupted)
		case StreamCompleted:
			sh.handler.auditLogger.LogRequestComplete(rec)
		case StreamNotStarted:
			sh.handler.auditLogger.LogProviderFailure(rec, audit.ReasonStreamNotStarted)
		case StreamHeaderUndelivered:
			// The header went out and the flush did not, so nothing of the
			// stream reached the caller. response_not_delivered is exactly
			// that, and clientStatusFor leaves the status at the committed 200.
			sh.handler.auditLogger.LogProviderFailure(rec, audit.ReasonResponseNotDelivered)
		case StreamTruncated:
			sh.handler.auditLogger.LogProviderFailure(rec, audit.ReasonStreamTruncated)
		case StreamClientDisconnected:
			// Not the provider's fault, and the reason says so. It is recorded
			// as a failure because the response was not delivered in full, and
			// a partial delivery attested as a completion would be a false
			// record of what the gateway delivered.
			sh.handler.auditLogger.LogProviderFailure(rec, audit.ReasonClientDisconnected)
		default:
			sh.handler.auditLogger.LogProviderFailure(rec, audit.ReasonStreamInterrupted)
		}
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
			StatusCode:         clientStatusFor(metrics.Outcome),
			Project:            aegisReq.Project,
			Stream:             true,
		})
	}
}

// outcomeForContext maps a cancelled stream context onto an outcome.
//
// ctx is context.WithTimeout(r.Context(), TotalTimeout), so the deadline
// elapsing yields DeadlineExceeded while the caller hanging up cancels the
// parent and yields Canceled. Both callers use this so the two endings cannot
// be told apart differently in two places.
func outcomeForContext(err error) StreamOutcome {
	if errors.Is(err, context.DeadlineExceeded) {
		return StreamTotalTimeout
	}
	return StreamClientDisconnected
}

// firstTokenMsForLog renders the first-token latency for the completion log
// line, using -1 to mean "no chunk ever arrived" rather than printing a
// nonsensical negative age.
func firstTokenMsForLog(m StreamMetrics) int64 {
	d, ok := timeToFirstToken(m)
	if !ok {
		return -1
	}
	return d.Milliseconds()
}

// timeToFirstToken reports how long the first content chunk took, and whether
// there was one.
//
// FirstChunkTime is the zero time until a chunk arrives, so a stream that failed
// before then yields FirstChunkTime.Sub(StartTime) as a multi-billion-year
// negative duration. Logging that is merely wrong; observing it on the
// aegis_streaming_time_to_first_token_ms histogram corrupts the _sum that every
// average over that metric is computed from, and it does so for precisely the
// failures an operator would be looking at.
func timeToFirstToken(m StreamMetrics) (time.Duration, bool) {
	if m.FirstChunkTime.IsZero() {
		return 0, false
	}
	return m.FirstChunkTime.Sub(m.StartTime), true
}

// clientStatusFor reports the HTTP status the gateway sent.
//
// A stream sends its 200 header before the first chunk, so any ending after
// that point was still a 200 on the wire however badly it went, including a
// header whose flush failed: WriteHeader has already committed the status and
// no error can follow it. The single exception is a stream that never started,
// where an error status went out and nothing else. Both the audit event and the
// usage row have to agree with the status line the gateway wrote, or the record
// contradicts the response.
func clientStatusFor(outcome StreamOutcome) int {
	if outcome == StreamNotStarted {
		return http.StatusInternalServerError
	}
	return http.StatusOK
}

// streamWithMonitoring handles the actual streaming with timeouts and monitoring.
func (sh *StreamingHandler) streamWithMonitoring(
	ctx context.Context,
	w http.ResponseWriter,
	reqID string,
	providerResp *http.Response,
	adapter adapters.ProviderAdapter,
	providerKey string,
	providerModel string,
	authInfo *auth.AuthInfo,
) (result StreamMetrics) {
	defer func() { _ = providerResp.Body.Close() }()

	// Initialised before the first return, so every outcome carries the
	// provider. The early returns below used to yield a zero StreamMetrics, and
	// HandleStream records a usage row from whatever comes back: a stream that
	// failed at the header therefore persisted provider = '' even though
	// providerKey was known all along, losing the attribution for exactly the
	// requests an operator would be investigating.
	metrics := StreamMetrics{
		StartTime: time.Now(),
		// The configured provider name, not adapter.Name(). See HandleStream.
		Provider: providerKey,
		// The routed model, known before the request was sent. A stream that
		// carries usage will overwrite this with the model the provider reports
		// serving; an early return keeps it, so the usage row says which
		// provider request failed instead of recording an empty model_served.
		Model: providerModel,
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		// No stream is sent and the caller receives 500. Say so in the result:
		// the zero value would leave the outcome unset, and the attested event
		// and the usage row would both record the 200 that was never written.
		httputil.WriteInternalError(w, reqID, "Streaming not supported")
		metrics.Outcome = StreamNotStarted
		return metrics
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Request-ID", reqID)
	w.WriteHeader(http.StatusOK)

	// The header flush is checked like every other write on this path. Leaving
	// it unchecked meant a stream whose 200 never reached the client could
	// still be attested as complete if the later chunk and terminator writes
	// happened to succeed, which contradicts the claim that the full response
	// was written and flushed.
	if err := flushToClient(w); err != nil {
		slog.Error("failed to flush the streaming response header",
			"request_id", reqID,
			"error", err,
			"provider", providerKey,
		)
		metrics.Outcome = StreamHeaderUndelivered
		return metrics
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
			// One case, and the cause decides which ending it was.
			//
			// There used to be a second case fed by a goroutine waiting on this
			// same ctx.Done(), meaning both became ready at once and select
			// chose between them at random: a total timeout could be recorded
			// as a client disconnect and a disconnect as a timeout. That was
			// survivable while it only mislabelled a log line. It is not
			// survivable now that the outcome is sealed into the audit record.
			//
			// ctx is context.WithTimeout(r.Context(), TotalTimeout), so the two
			// causes are already distinguishable: the deadline elapsing yields
			// DeadlineExceeded, and the caller hanging up cancels the parent
			// and yields Canceled.
			if outcomeForContext(ctx.Err()) == StreamTotalTimeout {
				slog.Warn("stream total timeout exceeded",
					"request_id", reqID,
					"chunks_sent", metrics.ChunkCount,
				)
				if sh.handler.metrics != nil {
					sh.handler.metrics.RecordStreamingError(adapter.Name(), "total_timeout")
				}
				// Only the timeout gets an error frame. A disconnected client
				// is not there to read one.
				_, _ = fmt.Fprintf(w, "data: {\"error\": \"timeout\"}\n\n")
				flusher.Flush()
				metrics.Outcome = StreamTotalTimeout
				return metrics
			}

			slog.Info("client disconnected during streaming",
				"request_id", reqID,
				"chunks_sent", metrics.ChunkCount,
			)
			if sh.handler.metrics != nil {
				sh.handler.metrics.RecordStreamingError(adapter.Name(), "client_disconnect")
			}
			metrics.Outcome = StreamClientDisconnected
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
			metrics.Outcome = StreamChunkTimeout
			return metrics

		case <-scanChan:
			// The scanner reached EOF. That is not the same as the response
			// being finished: a provider that closes the connection cleanly
			// part way through gives a nil error here and would otherwise be
			// attested as a completion, sealing a truncated answer as a whole
			// one. Only the terminator establishes completion.
			switch {
			case scanner.Err() != nil:
				slog.Error("error reading stream", "error", scanner.Err(), "provider", adapter.Name())
				if sh.handler.metrics != nil {
					sh.handler.metrics.RecordStreamingError(adapter.Name(), "scanner_error")
				}
				metrics.Outcome = StreamReadError
			case !metrics.terminatorSeen:
				slog.Warn("provider closed the stream without an end-of-stream marker",
					"request_id", reqID,
					"provider", providerKey,
					"chunks_sent", metrics.ChunkCount,
				)
				if sh.handler.metrics != nil {
					sh.handler.metrics.RecordStreamingError(adapter.Name(), "truncated")
				}
				metrics.Outcome = StreamTruncated
			default:
				metrics.Outcome = StreamCompleted
			}
			return metrics

		case line := <-lineChan:
			// Process chunk
			if err := sh.processChunk(w, line, adapter, transformer, &metrics, toolCalls); err != nil {
				slog.Error("error processing chunk", "error", err)
				if sh.handler.metrics != nil {
					sh.handler.metrics.RecordStreamingError(adapter.Name(), "chunk_processing_error")
				}
				// A chunk failing to write is the usual way a caller hanging up
				// first becomes visible, so the cause is consulted here for the
				// same reason the terminator path consults it: otherwise a
				// disconnect is sealed as a provider interruption, and the two
				// are different facts about who caused the request to end.
				if ctxErr := ctx.Err(); ctxErr != nil {
					metrics.Outcome = outcomeForContext(ctxErr)
				} else {
					metrics.Outcome = StreamChunkError
				}
				return metrics
			}

			// Check if the stream ended.
			//
			// Keyed off the flag processChunk sets, not off the raw line: the
			// Anthropic terminator is message_stop, which only becomes [DONE]
			// after transformation, so a raw-line test silently covered
			// OpenAI alone and let every Anthropic stream end at EOF instead.
			//
			// This is the NORMAL completion path and it is a separate return
			// from the scanner-finished case above. Missing it is what once
			// left a completed stream carrying the zero-value outcome.
			if metrics.terminatorSeen {
				// select chooses at random between ready cases, so the
				// terminator can be processed on the very tick the caller goes
				// away or the deadline lands. Delivering the marker into a dead
				// connection is not a completed response, so the context is
				// consulted before claiming one.
				if err := ctx.Err(); err != nil {
					metrics.Outcome = outcomeForContext(err)
					slog.Info("stream ended as its terminator arrived; not attesting completion",
						"request_id", reqID,
						"outcome", string(metrics.Outcome),
						"chunks_sent", metrics.ChunkCount,
					)
					return metrics
				}
				metrics.Outcome = StreamCompleted
				return metrics
			}
		}
	}
}

// processChunk handles a single SSE chunk with token counting.
func (sh *StreamingHandler) processChunk(
	w http.ResponseWriter,
	line string,
	adapter adapters.ProviderAdapter,
	transformer adapters.StreamTransformer,
	metrics *StreamMetrics,
	toolCalls *toolCallAccumulator,
) error {
	// SSE format: lines starting with "data: "
	if !strings.HasPrefix(line, "data: ") {
		// Forward event lines or empty lines as-is for keep-alive. Checked for
		// the same reason as content chunks: these bytes are part of the
		// response, so a failure here means it was not written in full.
		if strings.HasPrefix(line, "event: ") || line == "" {
			if _, err := fmt.Fprintf(w, "%s\n", line); err != nil {
				return fmt.Errorf("writing stream keep-alive line: %w", err)
			}
			if err := flushToClient(w); err != nil {
				return fmt.Errorf("flushing stream keep-alive line: %w", err)
			}
		}
		return nil
	}

	data := strings.TrimPrefix(line, "data: ")

	// End of stream, OpenAI-compatible form.
	//
	// The write error is checked rather than discarded, and terminatorSeen is
	// set only after it succeeds. Completion is a claim about what the CALLER
	// received, so a terminator the gateway failed to deliver does not
	// establish one.
	if data == "[DONE]" {
		if _, err := fmt.Fprintf(w, "data: [DONE]\n\n"); err != nil {
			return fmt.Errorf("writing end-of-stream marker: %w", err)
		}
		// http.Flusher.Flush discards its error, so a marker that reached
		// net/http's buffer and then failed on the socket looked delivered.
		// The flag is set only once the flush has actually succeeded.
		if err := flushToClient(w); err != nil {
			return fmt.Errorf("flushing end-of-stream marker: %w", err)
		}
		metrics.terminatorSeen = true
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

	// Check if the adapter signaled end of stream. This is the Anthropic path:
	// message_stop arrives as an ordinary data line and the transformer renders
	// it as [DONE], so the raw line never contains the marker. The loop's own
	// terminator check used to test the RAW line, which meant it only ever
	// matched OpenAI and every Anthropic stream fell through to EOF.
	if string(transformed) == "[DONE]" {
		if _, err := fmt.Fprintf(w, "data: [DONE]\n\n"); err != nil {
			return fmt.Errorf("writing end-of-stream marker: %w", err)
		}
		// Same as the OpenAI branch above: the flush error is what says the
		// marker actually left the gateway.
		if err := flushToClient(w); err != nil {
			return fmt.Errorf("flushing end-of-stream marker: %w", err)
		}
		metrics.terminatorSeen = true
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

	// Forward to client.
	//
	// These errors used to be discarded, so a chunk that failed to reach the
	// client midway through a response was invisible: if a later terminator
	// wrote and flushed cleanly the stream was attested as complete, which
	// contradicts the claim that the FULL response was written and flushed.
	// Returning the error makes the loop record StreamChunkError instead.
	if _, err := fmt.Fprintf(w, "data: %s\n\n", transformed); err != nil {
		return fmt.Errorf("writing stream chunk: %w", err)
	}
	if err := flushToClient(w); err != nil {
		return fmt.Errorf("flushing stream chunk: %w", err)
	}

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
