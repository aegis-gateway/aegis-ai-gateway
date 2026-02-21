package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/af-corp/aegis-gateway/internal/auth"
	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/httputil"
	"github.com/af-corp/aegis-gateway/internal/router"
	"github.com/af-corp/aegis-gateway/internal/types"
)

// Handler holds dependencies for the gateway HTTP handlers.
type Handler struct {
	registry  *router.Registry
	modelsCfg func() *config.ModelsConfig
}

func NewHandler(registry *router.Registry, modelsCfg func() *config.ModelsConfig) *Handler {
	return &Handler{
		registry:  registry,
		modelsCfg: modelsCfg,
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

	// TODO: streaming support will be added in step 8
	if aegisReq.Stream {
		h.handleStream(w, r, reqID, providerReq, adapter, originalModel)
		return
	}

	client := &http.Client{Timeout: 30 * time.Second}
	providerResp, err := client.Do(providerReq)
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
	gatewayOverhead := time.Since(receivedAt)

	slog.Info("request completed",
		"request_id", reqID,
		"model_requested", originalModel,
		"model_served", aegisResp.Model,
		"provider", aegisResp.Provider,
		"prompt_tokens", aegisResp.Usage.PromptTokens,
		"completion_tokens", aegisResp.Usage.CompletionTokens,
		"gateway_overhead_ms", gatewayOverhead.Milliseconds(),
		"org_id", authInfo.OrganizationID,
	)

	// Return OpenAI-compatible response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(aegisResp)
}

// handleStream forwards SSE streaming responses (stub for now, full impl in step 8)
func (h *Handler) handleStream(w http.ResponseWriter, r *http.Request, reqID string, providerReq *http.Request, adapter interface{}, originalModel string) {
	httputil.WriteBadRequestError(w, reqID, "Streaming not yet implemented")
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
