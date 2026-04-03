package gateway

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/af-corp/aegis-gateway/internal/auth"
	"github.com/af-corp/aegis-gateway/internal/filter"
	"github.com/af-corp/aegis-gateway/internal/httputil"
	"github.com/af-corp/aegis-gateway/internal/types"
)

// ChatCompletionsRefactored handles POST /v1/chat/completions with refactored logic.
// This is the clean, modular version under 50 lines per function.
func (h *Handler) ChatCompletionsRefactored(w http.ResponseWriter, r *http.Request) {
	reqID := w.Header().Get("X-Request-ID")
	receivedAt := time.Now()

	// Get auth from context
	authInfo, ok := auth.AuthFromContext(r.Context())
	if !ok {
		httputil.WriteAuthError(w, reqID, "Not authenticated")
		return
	}

	// Parse and validate request
	parsedReq, err := h.parseAndValidate(r, reqID, authInfo)
	if err != nil {
		h.writeHTTPError(w, reqID, err)
		return
	}

	// Run content filters
	if err := h.runContentFilters(r, parsedReq, authInfo); err != nil {
		h.writeHTTPError(w, reqID, err)
		return
	}

	// Route to provider
	routeResult, err := h.routeRequest(r.Context(), parsedReq)
	if err != nil {
		h.writeHTTPError(w, reqID, err)
		return
	}

	// Run OPA policy evaluation after routing (needs provider type)
	if err := h.runPolicyCheck(r, reqID, parsedReq, routeResult, authInfo); err != nil {
		h.writeHTTPError(w, reqID, err)
		return
	}

	// Monitor context cancellation
	cleanupMonitor := h.monitorContext(r.Context(), reqID, routeResult.Adapter.Name())
	defer cleanupMonitor()

	// Handle streaming separately
	if parsedReq.AegisRequest.Stream {
		h.handleStreamingRequest(w, r, reqID, routeResult, parsedReq, authInfo)
		return
	}

	// Execute provider request
	aegisResp, err := h.executeNonStreamingRequest(r.Context(), routeResult, parsedReq)
	if err != nil {
		h.writeHTTPError(w, reqID, err)
		return
	}

	// Build and enrich response
	h.buildResponse(aegisResp, reqID)

	// Log and record metrics
	h.logAndRecordMetrics(reqID, parsedReq.OriginalModel, aegisResp, authInfo, parsedReq.AegisRequest.Project, false, time.Since(receivedAt))

	// Return OpenAI-compatible response
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(aegisResp)
}

// parseAndValidate handles request parsing and validation.
func (h *Handler) parseAndValidate(r *http.Request, reqID string, authInfo *auth.AuthInfo) (*ParsedRequestWithModel, error) {
	processor := &RequestProcessor{
		validator:   h.validator,
		auditLogger: h.auditLogger,
		metrics:     h.metrics,
	}
	
	parsed, err := processor.ParseAndValidateRequest(r, reqID, authInfo)
	if err != nil {
		return nil, err
	}
	
	return &ParsedRequestWithModel{
		ParsedRequest: parsed,
		OriginalModel: parsed.AegisRequest.Model,
	}, nil
}

// runContentFilters executes content filtering.
func (h *Handler) runContentFilters(r *http.Request, parsedReq *ParsedRequestWithModel, authInfo *auth.AuthInfo) error {
	processor := &FilterProcessor{
		filterChain: h.filterChain,
		auditLogger: h.auditLogger,
		metrics:     h.metrics,
	}
	
	_, err := processor.RunFilters(r, parsedReq.AegisRequest, authInfo)
	return err
}

// runPolicyCheck runs OPA policy evaluation after routing has resolved the provider type.
// It temporarily restores the original model name so policies see the user-requested model,
// not the provider-specific one set by RouteToProvider.
func (h *Handler) runPolicyCheck(r *http.Request, reqID string, parsedReq *ParsedRequestWithModel, routeResult *RouteResultWithModel, authInfo *auth.AuthInfo) error {
	if h.policyEvaluator == nil || !h.policyEvaluator.Enabled() {
		return nil
	}

	// RouteToProvider already mutated AegisRequest.Model to the provider model.
	// Restore the original for policy evaluation, then put the provider model back.
	providerModel := parsedReq.AegisRequest.Model
	parsedReq.AegisRequest.Model = parsedReq.OriginalModel
	parsedReq.AegisRequest.ProviderType = routeResult.Adapter.Name()

	result := h.policyEvaluator.ScanRequest(r.Context(), parsedReq.AegisRequest)

	parsedReq.AegisRequest.Model = providerModel

	if result.Action == filter.ActionBlock {
		if h.auditLogger != nil {
			h.auditLogger.LogFilterBlock(reqID, authInfo.OrganizationID, authInfo.TeamID, authInfo.KeyID, result.FilterName, result.Message, r.RemoteAddr)
		}
		if h.metrics != nil {
			h.metrics.RecordFilterAction(result.FilterName, string(result.Action))
		}
		return &httputil.HTTPError{StatusCode: http.StatusForbidden, Message: result.Message}
	}
	return nil
}

// routeRequest handles provider routing.
func (h *Handler) routeRequest(ctx interface{ Done() <-chan struct{} }, parsedReq *ParsedRequestWithModel) (*RouteResultWithModel, error) {
	processor := &RouterProcessor{
		registry:      h.registry,
		healthTracker: h.healthTracker,
		modelsCfg:     h.modelsCfg,
	}
	
	// Convert ctx to context.Context using type assertion
	routeResult, err := processor.RouteToProvider(ctx.(interface {
		Done() <-chan struct{}
		Deadline() (deadline time.Time, ok bool)
		Err() error
		Value(key interface{}) interface{}
	}), parsedReq.AegisRequest)
	if err != nil {
		return nil, err
	}
	
	return &RouteResultWithModel{
		RouteResult:   routeResult,
		OriginalModel: parsedReq.OriginalModel,
	}, nil
}

// monitorContext sets up context cancellation monitoring.
func (h *Handler) monitorContext(ctx interface{ Done() <-chan struct{} }, reqID, provider string) func() {
	if h.contextMonitor != nil {
		return h.contextMonitor.Watch(ctx.(interface {
			Done() <-chan struct{}
			Deadline() (deadline time.Time, ok bool)
			Err() error
			Value(key interface{}) interface{}
		}), reqID, provider)
	}
	return func() {}
}

// handleStreamingRequest delegates to streaming handler.
func (h *Handler) handleStreamingRequest(
	w http.ResponseWriter,
	r *http.Request,
	reqID string,
	routeResult *RouteResultWithModel,
	parsedReq *ParsedRequestWithModel,
	authInfo *auth.AuthInfo,
) {
	h.streamingHandler.HandleStream(
		w,
		r,
		reqID,
		routeResult.ProviderReq,
		routeResult.Adapter,
		routeResult.OriginalModel,
		authInfo,
		parsedReq.AegisRequest,
	)
}

// executeNonStreamingRequest executes provider request with retry logic.
func (h *Handler) executeNonStreamingRequest(
	ctx interface{ Done() <-chan struct{} },
	routeResult *RouteResultWithModel,
	parsedReq *ParsedRequestWithModel,
) (*types.AegisResponse, error) {
	executor := &ProviderExecutor{
		healthTracker: h.healthTracker,
		retryExecutor: h.retryExecutor,
	}
	
	// Execute provider request
	providerResp, err := executor.ExecuteProviderRequest(
		ctx.(interface {
			Done() <-chan struct{}
			Deadline() (deadline time.Time, ok bool)
			Err() error
			Value(key interface{}) interface{}
		}),
		routeResult.Adapter,
		parsedReq.AegisRequest,
		routeResult.ProviderReq,
	)
	if err != nil {
		return nil, err
	}
	
	// Transform response
	return executor.TransformProviderResponse(
		ctx.(interface {
			Done() <-chan struct{}
			Deadline() (deadline time.Time, ok bool)
			Err() error
			Value(key interface{}) interface{}
		}),
		routeResult.Adapter,
		providerResp,
	)
}

// buildResponse enriches response with cost and metadata.
func (h *Handler) buildResponse(aegisResp *types.AegisResponse, reqID string) {
	builder := &ResponseBuilder{
		costCalc: h.costCalc,
	}
	builder.BuildResponse(aegisResp, reqID)
}

// logAndRecordMetrics logs and records telemetry.
func (h *Handler) logAndRecordMetrics(
	reqID string,
	originalModel string,
	aegisResp *types.AegisResponse,
	authInfo *auth.AuthInfo,
	project string,
	stream bool,
	duration time.Duration,
) {
	logger := &TelemetryLogger{
		metrics:       h.metrics,
		usageRecorder: h.usageRecorder,
	}
	logger.LogCompletedRequest(reqID, originalModel, aegisResp, authInfo, project, stream, duration)
}

// writeHTTPError writes an HTTP error response.
func (h *Handler) writeHTTPError(w http.ResponseWriter, reqID string, err error) {
	if httpErr, ok := err.(*httputil.HTTPError); ok {
		switch httpErr.StatusCode {
		case http.StatusBadRequest:
			httputil.WriteBadRequestError(w, reqID, httpErr.Message)
		case http.StatusUnauthorized:
			httputil.WriteAuthError(w, reqID, httpErr.Message)
		case http.StatusForbidden:
			httputil.WriteContentBlockedError(w, reqID, httpErr.Message)
		case http.StatusServiceUnavailable:
			httputil.WriteServiceUnavailableError(w, reqID, httpErr.Message)
		default:
			httputil.WriteInternalError(w, reqID, httpErr.Message)
		}
	} else {
		httputil.WriteInternalError(w, reqID, err.Error())
	}
}

// ParsedRequestWithModel extends ParsedRequest with original model tracking.
type ParsedRequestWithModel struct {
	*ParsedRequest
	OriginalModel string
}

// RouteResultWithModel extends RouteResult with original model tracking.
type RouteResultWithModel struct {
	*RouteResult
	OriginalModel string
}
