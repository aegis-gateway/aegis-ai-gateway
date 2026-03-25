package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/af-corp/aegis-gateway/internal/auth"
	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/cost"
	"github.com/af-corp/aegis-gateway/internal/filter"
	"github.com/af-corp/aegis-gateway/internal/httputil"
	"github.com/af-corp/aegis-gateway/internal/retry"
	"github.com/af-corp/aegis-gateway/internal/router"
	"github.com/af-corp/aegis-gateway/internal/storage"
	"github.com/af-corp/aegis-gateway/internal/telemetry"
	"github.com/af-corp/aegis-gateway/internal/types"
	"github.com/af-corp/aegis-gateway/internal/validation"
)

// AuditLogger defines the interface for audit logging (to avoid circular dependency).
type AuditLogger interface {
	LogFilterBlock(requestID, orgID, teamID, keyID, filterType, reason string, ip string)
}

// Handler holds dependencies for the gateway HTTP handlers.
type Handler struct {
	registry         *router.Registry
	healthTracker    *router.HealthTracker
	modelsCfg        func() *config.ModelsConfig
	cfg              func() *config.Config
	filterChain      *filter.Chain
	metrics          *telemetry.Metrics
	costCalc         *cost.Calculator
	usageRecorder    *storage.UsageRecorder
	auditLogger      AuditLogger
	retryExecutor    *retry.Executor
	contextMonitor   *retry.ContextMonitor
	validator        *validation.Validator
	streamingHandler *StreamingHandler
}

func NewHandler(registry *router.Registry, healthTracker *router.HealthTracker, modelsCfg func() *config.ModelsConfig, cfg func() *config.Config, filterChain *filter.Chain, metrics *telemetry.Metrics, costCalc *cost.Calculator, usageRecorder *storage.UsageRecorder, auditLogger AuditLogger, retryExecutor *retry.Executor, contextMonitor *retry.ContextMonitor, validator *validation.Validator) *Handler {
	h := &Handler{
		registry:       registry,
		healthTracker:  healthTracker,
		modelsCfg:      modelsCfg,
		cfg:            cfg,
		filterChain:    filterChain,
		metrics:        metrics,
		costCalc:       costCalc,
		usageRecorder:  usageRecorder,
		auditLogger:    auditLogger,
		retryExecutor:  retryExecutor,
		contextMonitor: contextMonitor,
		validator:      validator,
	}
	
	// Initialize streaming handler with configuration
	h.streamingHandler = NewStreamingHandler(h, DefaultStreamingConfig())
	
	return h
}

// ChatCompletions handles POST /v1/chat/completions
func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	reqID := w.Header().Get("X-Request-ID")
	receivedAt := time.Now()

	authInfo, ok := auth.AuthFromContext(r.Context())
	if !ok {
		httputil.WriteAuthError(w, reqID, "Not authenticated")
		return
	}

	// Parse request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httputil.WriteBadRequestError(w, reqID, "Failed to read request body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	var aegisReq types.AegisRequest
	if err := json.Unmarshal(body, &aegisReq); err != nil {
		httputil.WriteBadRequestError(w, reqID, "Invalid JSON: "+err.Error())
		return
	}

	// Enrich with auth context
	aegisReq.RequestID = reqID
	aegisReq.OrganizationID = authInfo.OrganizationID
	aegisReq.TeamID = authInfo.TeamID
	aegisReq.UserID = authInfo.UserID
	aegisReq.APIKeyID = authInfo.KeyID
	aegisReq.Classification = authInfo.MaxClassification
	aegisReq.ReceivedAt = receivedAt

	// Extract AEGIS headers
	aegisReq.Project = r.Header.Get("X-Aegis-Project")
	aegisReq.PreferProvider = r.Header.Get("X-Aegis-Prefer-Provider")
	aegisReq.TraceContext = r.Header.Get("X-Aegis-Trace-Context")

	// Validate request
	if h.validator != nil {
		if err := h.validator.Validate(&aegisReq); err != nil {
			slog.Warn("request validation failed",
				"request_id", reqID,
				"org_id", authInfo.OrganizationID,
				"error", err.Error(),
			)
			httputil.WriteBadRequestError(w, reqID, err.Error())
			return
		}
	} else {
		// Fallback to basic validation if validator not configured
		if aegisReq.Model == "" {
			httputil.WriteBadRequestError(w, reqID, "model is required")
			return
		}
		if len(aegisReq.Messages) == 0 {
			httputil.WriteBadRequestError(w, reqID, "messages is required")
			return
		}
	}

	// Run content filter chain (secrets, injection, PII, policy)
	if h.filterChain != nil {
		results, blocked := h.filterChain.Run(r.Context(), &aegisReq)
		if blocked != nil {
			slog.Warn("request blocked by filter",
				"request_id", reqID,
				"filter", blocked.FilterName,
				"detections", blocked.Detections,
				"score", blocked.Score,
				"org_id", authInfo.OrganizationID,
			)
			if h.auditLogger != nil {
				h.auditLogger.LogFilterBlock(reqID, authInfo.OrganizationID, authInfo.TeamID, authInfo.KeyID, blocked.FilterName, blocked.Message, r.RemoteAddr)
			}
			if h.metrics != nil {
				h.metrics.RecordFilterAction(blocked.FilterName, string(blocked.Action))
			}
			httputil.WriteContentBlockedError(w, reqID, blocked.Message)
			return
		}
		// Record flagged filters
		for _, fr := range results {
			if fr.Action == filter.ActionFlag && h.metrics != nil {
				h.metrics.RecordFilterAction(fr.FilterName, "flag")
			}
		}
	}

	// Route to provider
	modelsCfg := h.modelsCfg()
	adapter, providerModel, err := router.ResolveRoute(modelsCfg, h.registry, h.healthTracker, aegisReq.Model, string(aegisReq.Classification))
	if err != nil {
		httputil.WriteServiceUnavailableError(w, reqID, "No provider available: "+err.Error())
		return
	}

	// Override model with the provider-specific model name
	originalModel := aegisReq.Model
	aegisReq.Model = providerModel

	// Start monitoring context for cancellation
	var cleanupMonitor func()
	if h.contextMonitor != nil {
		cleanupMonitor = h.contextMonitor.Watch(r.Context(), reqID, adapter.Name())
		defer cleanupMonitor()
	}

	// Transform and send to provider
	providerReq, err := adapter.TransformRequest(r.Context(), &aegisReq)
	if err != nil {
		slog.Error("failed to transform request", "error", err, "provider", adapter.Name())
		httputil.WriteInternalError(w, reqID, "Failed to prepare provider request")
		return
	}

	// Streaming: forward SSE events from provider to client with full monitoring
	if aegisReq.Stream {
		h.streamingHandler.HandleStream(w, r, reqID, providerReq, adapter, originalModel, authInfo, &aegisReq)
		return
	}

	// Send request with retry logic
	var providerResp *http.Response
	if h.retryExecutor != nil {
		providerResp, err = h.retryExecutor.Execute(r.Context(), adapter.Name(), func(ctx context.Context, attempt int) (*http.Response, error) {
			// Re-create request for each attempt with fresh context
			retryReq, transformErr := adapter.TransformRequest(ctx, &aegisReq)
			if transformErr != nil {
				return nil, transformErr
			}
			return adapter.SendRequest(retryReq)
		})
	} else {
		// Fallback to direct send if no retry executor
		providerResp, err = adapter.SendRequest(providerReq)
	}

	if err != nil {
		slog.Error("provider request failed", "error", err, "provider", adapter.Name())
		if h.healthTracker != nil {
			h.healthTracker.RecordFailure(adapter.Name())
		}
		httputil.WriteServiceUnavailableError(w, reqID, "Provider request failed")
		return
	}

	aegisResp, err := adapter.TransformResponse(r.Context(), providerResp)
	if err != nil {
		slog.Error("failed to transform response", "error", err, "provider", adapter.Name())
		httputil.WriteInternalError(w, reqID, "Failed to process provider response")
		return
	}

	if h.healthTracker != nil {
		h.healthTracker.RecordSuccess(adapter.Name())
	}

	aegisResp.RequestID = reqID
	
	// Calculate cost using actual provider and model served
	if h.costCalc != nil {
		if cost, found := h.costCalc.Calculate(
			aegisResp.Provider,
			aegisResp.Model,
			aegisResp.Usage.PromptTokens,
			aegisResp.Usage.CompletionTokens,
		); found {
			aegisResp.EstimatedCostUSD = cost
		} else {
			slog.Warn("cost calculation failed - no pricing data",
				"provider", aegisResp.Provider,
				"model", aegisResp.Model,
				"request_id", reqID,
			)
		}
	}
	
	totalDuration := time.Since(receivedAt)

	slog.Info("request completed",
		"request_id", reqID,
		"model_requested", originalModel,
		"model_served", aegisResp.Model,
		"provider", aegisResp.Provider,
		"prompt_tokens", aegisResp.Usage.PromptTokens,
		"completion_tokens", aegisResp.Usage.CompletionTokens,
		"total_tokens", aegisResp.Usage.TotalTokens,
		"estimated_cost_usd", aegisResp.EstimatedCostUSD,
		"duration_ms", totalDuration.Milliseconds(),
		"status_code", http.StatusOK,
		"stream", false,
		"classification", string(authInfo.MaxClassification),
		"org_id", authInfo.OrganizationID,
		"team_id", authInfo.TeamID,
	)

	if h.metrics != nil {
		h.metrics.RecordRequest(telemetry.RequestLabels{
			Org:              authInfo.OrganizationID,
			Team:             authInfo.TeamID,
			Model:            originalModel,
			Provider:         aegisResp.Provider,
			Status:           "200",
			Classification:   string(authInfo.MaxClassification),
			DurationMs:       float64(totalDuration.Milliseconds()),
			OverheadMs:       float64(totalDuration.Milliseconds()), // approximation; provider latency subtracted in future
			PromptTokens:     aegisResp.Usage.PromptTokens,
			CompletionTokens: aegisResp.Usage.CompletionTokens,
			CostUSD:          aegisResp.EstimatedCostUSD,
		})
	}

	// Record usage asynchronously (non-blocking)
	if h.usageRecorder != nil {
		h.usageRecorder.RecordUsage(storage.UsageRecord{
			RequestID:        reqID,
			OrganizationID:   authInfo.OrganizationID,
			TeamID:           authInfo.TeamID,
			UserID:           authInfo.UserID,
			APIKeyID:         authInfo.KeyID,
			ModelRequested:   originalModel,
			ModelServed:      aegisResp.Model,
			Provider:         aegisResp.Provider,
			Classification:   string(authInfo.MaxClassification),
			PromptTokens:     aegisResp.Usage.PromptTokens,
			CompletionTokens: aegisResp.Usage.CompletionTokens,
			TotalTokens:      aegisResp.Usage.TotalTokens,
			EstimatedCostUSD: aegisResp.EstimatedCostUSD,
			DurationMs:       totalDuration.Milliseconds(),
			StatusCode:       http.StatusOK,
			Project:          aegisReq.Project,
			Stream:           false,
		})
	}

	// Return OpenAI-compatible response
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(aegisResp)
}

// ListModels handles GET /v1/models
func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	reqID := w.Header().Get("X-Request-ID")

	authInfo, ok := auth.AuthFromContext(r.Context())
	if !ok {
		httputil.WriteAuthError(w, reqID, "Not authenticated")
		return
	}

	modelsCfg := h.modelsCfg()
	var models []modelObject
	for name, mapping := range modelsCfg.Models {
		// Filter by allowed models if set
		if len(authInfo.AllowedModels) > 0 {
			allowed := false
			for _, m := range authInfo.AllowedModels {
				if m == name {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
		}

		_ = mapping
		models = append(models, modelObject{
			ID:      name,
			Object:  "model",
			Created: 0,
			OwnedBy: "aegis",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(modelListResponse{
		Object: "list",
		Data:   models,
	})
}

type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type modelListResponse struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}
