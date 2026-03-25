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

	"github.com/af-corp/aegis-gateway/internal/auth"
	"github.com/af-corp/aegis-gateway/internal/httputil"
	"github.com/af-corp/aegis-gateway/internal/router/adapters"
	"github.com/af-corp/aegis-gateway/internal/storage"
	"github.com/af-corp/aegis-gateway/internal/telemetry"
	"github.com/af-corp/aegis-gateway/internal/types"
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
		PerChunkTimeout: 30 * time.Second,  // 30s per chunk
		TotalTimeout:    5 * time.Minute,    // 5 min total
		BufferSize:      64 * 1024,          // 64KB initial
		MaxBufferSize:   1024 * 1024,        // 1MB max
	}
}

// StreamMetrics tracks metrics during a streaming session.
type StreamMetrics struct {
	StartTime         time.Time
	FirstChunkTime    time.Time
	ChunkCount        int
	PromptTokens      int
	CompletionTokens  int
	TotalTokens       int
	EstimatedCostUSD  float64
	Provider          string
	Model             string
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
func (sh *StreamingHandler) HandleStream(
	w http.ResponseWriter,
	r *http.Request,
	reqID string,
	providerReq *http.Request,
	adapter adapters.ProviderAdapter,
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
		providerResp.Body.Close()
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
	metrics := sh.streamWithMonitoring(ctx, w, reqID, providerResp, adapter, authInfo)
	
	totalDuration := time.Since(receivedAt)
	
	// Calculate final cost if we have token counts
	if sh.handler.costCalc != nil && metrics.TotalTokens > 0 {
		if cost, found := sh.handler.costCalc.Calculate(
			metrics.Provider,
			metrics.Model,
			metrics.PromptTokens,
			metrics.CompletionTokens,
		); found {
			metrics.EstimatedCostUSD = cost
		} else {
			slog.Warn("cost calculation failed - no pricing data",
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
			Provider:             metrics.Provider,
			Model:                originalModel,
			ChunkCount:           metrics.ChunkCount,
			TimeToFirstTokenMs:   float64(timeToFirstToken.Milliseconds()),
			TokensPerSecond:      sh.calculateTokensPerSecond(metrics.CompletionTokens, totalDuration),
			StreamDurationMs:     float64(totalDuration.Milliseconds()),
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
	authInfo *auth.AuthInfo,
) StreamMetrics {
	defer providerResp.Body.Close()

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
		Provider:  adapter.Name(),
	}

	scanner := bufio.NewScanner(providerResp.Body)
	scanner.Buffer(make([]byte, 0, sh.config.BufferSize), sh.config.MaxBufferSize)

	// Channel to detect client disconnect
	var clientDisconnected <-chan bool
	if cn, ok := w.(http.CloseNotifier); ok {
		clientDisconnected = cn.CloseNotify()
	} else {
		// Fallback: use context cancellation for disconnect detection
		ch := make(chan bool)
		go func() {
			<-ctx.Done()
			ch <- true
		}()
		clientDisconnected = ch
	}
	
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
			fmt.Fprintf(w, "data: {\"error\": \"timeout\"}\n\n")
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
			fmt.Fprintf(w, "data: {\"error\": \"chunk timeout\"}\n\n")
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
			if err := sh.processChunk(w, flusher, line, adapter, &metrics); err != nil {
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
	metrics *StreamMetrics,
) error {
	// SSE format: lines starting with "data: "
	if !strings.HasPrefix(line, "data: ") {
		// Forward event lines or empty lines as-is for keep-alive
		if strings.HasPrefix(line, "event: ") || line == "" {
			fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()
		}
		return nil
	}

	data := strings.TrimPrefix(line, "data: ")

	// End of stream
	if data == "[DONE]" {
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
		return nil
	}

	// Transform chunk through the adapter
	transformed, err := adapter.TransformStreamChunk([]byte(data))
	if err != nil {
		return fmt.Errorf("transform chunk failed: %w", err)
	}

	// nil means skip this chunk (e.g., Anthropic non-content events)
	if transformed == nil {
		return nil
	}

	// Check if the adapter signaled end of stream
	if string(transformed) == "[DONE]" {
		fmt.Fprintf(w, "data: [DONE]\n\n")
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

	// Forward to client
	fmt.Fprintf(w, "data: %s\n\n", transformed)
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
