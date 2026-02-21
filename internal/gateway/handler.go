package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/af-corp/aegis-gateway/internal/auth"
	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/filter/secrets"
	"github.com/af-corp/aegis-gateway/internal/httputil"
	"github.com/af-corp/aegis-gateway/internal/router"
	"github.com/af-corp/aegis-gateway/internal/router/adapters"
	"github.com/af-corp/aegis-gateway/internal/telemetry"
	"github.com/af-corp/aegis-gateway/internal/types"
)

// Handler holds dependencies for the gateway HTTP handlers.
type Handler struct {
	registry       *router.Registry
	modelsCfg      func() *config.ModelsConfig
	cfg            func() *config.Config
	secretsScanner *secrets.Scanner
	metrics        *telemetry.Metrics
}

func NewHandler(registry *router.Registry, modelsCfg func() *config.ModelsConfig, cfg func() *config.Config, metrics *telemetry.Metrics) *Handler {
	return &Handler{
		registry:       registry,
		modelsCfg:      modelsCfg,
		cfg:            cfg,
		secretsScanner: secrets.NewScanner(),
		metrics:        metrics,
	}
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
	defer r.Body.Close()

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

	if aegisReq.Model == "" {
		httputil.WriteBadRequestError(w, reqID, "model is required")
		return
	}
	if len(aegisReq.Messages) == 0 {
		httputil.WriteBadRequestError(w, reqID, "messages is required")
		return
	}

	// Secrets scanning (before routing — block early)
	if h.cfg().Filter.Secrets.Enabled {
		detections := h.secretsScanner.ScanMessages(aegisReq.Messages)
		if len(detections) > 0 {
			// Collect unique secret types for the error message (never include the secret itself)
			seen := map[string]bool{}
			for _, d := range detections {
				seen[d.PatternName] = true
			}
			secretTypes := ""
			for name := range seen {
				if secretTypes != "" {
					secretTypes += ", "
				}
				secretTypes += name
			}
			slog.Warn("secrets detected in request",
				"request_id", reqID,
				"detection_count", len(detections),
				"secret_types", secretTypes,
				"org_id", authInfo.OrganizationID,
			)
			if h.metrics != nil {
				h.metrics.RecordFilterAction("secrets", "block")
			}
			httputil.WriteContentBlockedError(w, reqID,
				fmt.Sprintf("Request blocked: detected %d secret(s) of type: %s", len(detections), secretTypes))
			return
		}
	}

	// Route to provider
	modelsCfg := h.modelsCfg()
	adapter, providerModel, err := router.ResolveRoute(modelsCfg, h.registry, aegisReq.Model, string(aegisReq.Classification))
	if err != nil {
		httputil.WriteServiceUnavailableError(w, reqID, "No provider available: "+err.Error())
		return
	}

	// Override model with the provider-specific model name
	originalModel := aegisReq.Model
	aegisReq.Model = providerModel

	// Transform and send to provider
	providerReq, err := adapter.TransformRequest(r.Context(), &aegisReq)
	if err != nil {
		slog.Error("failed to transform request", "error", err, "provider", adapter.Name())
		httputil.WriteInternalError(w, reqID, "Failed to prepare provider request")
		return
	}

	// Streaming: forward SSE events from provider to client
	if aegisReq.Stream {
		h.handleStream(w, reqID, providerReq, adapter, originalModel, authInfo)
		return
	}

	providerResp, err := adapter.SendRequest(providerReq)
	if err != nil {
		slog.Error("provider request failed", "error", err, "provider", adapter.Name())
		httputil.WriteServiceUnavailableError(w, reqID, "Provider request failed")
		return
	}

	aegisResp, err := adapter.TransformResponse(r.Context(), providerResp)
	if err != nil {
		slog.Error("failed to transform response", "error", err, "provider", adapter.Name())
		httputil.WriteInternalError(w, reqID, "Failed to process provider response")
		return
	}

	aegisResp.RequestID = reqID
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

	// Return OpenAI-compatible response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(aegisResp)
}

// handleStream sends the request to the provider and forwards SSE chunks to the client.
func (h *Handler) handleStream(w http.ResponseWriter, reqID string, providerReq *http.Request, adapter adapters.ProviderAdapter, originalModel string, authInfo *auth.AuthInfo) {
	providerResp, err := adapter.SendRequest(providerReq)
	if err != nil {
		slog.Error("streaming provider request failed", "error", err, "provider", adapter.Name())
		httputil.WriteServiceUnavailableError(w, reqID, "Provider request failed")
		return
	}

	if providerResp.StatusCode != http.StatusOK {
		// Forward provider error as JSON
		body, _ := io.ReadAll(providerResp.Body)
		providerResp.Body.Close()
		slog.Error("streaming provider returned error",
			"status", providerResp.StatusCode,
			"provider", adapter.Name(),
			"body", string(body),
		)
		httputil.WriteInternalError(w, reqID, "Provider returned error")
		return
	}

	slog.Info("streaming started",
		"request_id", reqID,
		"model_requested", originalModel,
		"provider", adapter.Name(),
		"org_id", authInfo.OrganizationID,
	)

	streamSSE(w, reqID, providerResp, adapter)
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
	json.NewEncoder(w).Encode(modelListResponse{
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
