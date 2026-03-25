package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/af-corp/aegis-gateway/internal/auth"
	"github.com/af-corp/aegis-gateway/internal/filter"
	"github.com/af-corp/aegis-gateway/internal/httputil"
	"github.com/af-corp/aegis-gateway/internal/types"
)

// RequestProcessor handles request parsing, validation, and enrichment.
type RequestProcessor struct {
	validator   interface{ Validate(*types.AegisRequest) error }
	auditLogger AuditLogger
	metrics     interface{ RecordFilterAction(string, string) }
}

// ParsedRequest contains a parsed and validated request.
type ParsedRequest struct {
	AegisRequest *types.AegisRequest
	AuthInfo     *auth.AuthInfo
	RequestID    string
	ReceivedAt   time.Time
}

// ParseAndValidateRequest reads, parses, and validates a request.
func (rp *RequestProcessor) ParseAndValidateRequest(
	r *http.Request,
	reqID string,
	authInfo *auth.AuthInfo,
) (*ParsedRequest, error) {
	receivedAt := time.Now()

	// Parse request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, httputil.NewHTTPError(http.StatusBadRequest, "Failed to read request body")
	}
	defer func() { _ = r.Body.Close() }()

	var aegisReq types.AegisRequest
	if err := json.Unmarshal(body, &aegisReq); err != nil {
		return nil, httputil.NewHTTPError(http.StatusBadRequest, "Invalid JSON: "+err.Error())
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
	if err := rp.validateRequest(&aegisReq); err != nil {
		return nil, err
	}

	return &ParsedRequest{
		AegisRequest: &aegisReq,
		AuthInfo:     authInfo,
		RequestID:    reqID,
		ReceivedAt:   receivedAt,
	}, nil
}

// validateRequest performs comprehensive request validation.
func (rp *RequestProcessor) validateRequest(req *types.AegisRequest) error {
	if rp.validator != nil {
		if err := rp.validator.Validate(req); err != nil {
			slog.Warn("request validation failed",
				"request_id", req.RequestID,
				"org_id", req.OrganizationID,
				"error", err.Error(),
			)
			return httputil.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	} else {
		// Fallback to basic validation if validator not configured
		if req.Model == "" {
			return httputil.NewHTTPError(http.StatusBadRequest, "model is required")
		}
		if len(req.Messages) == 0 {
			return httputil.NewHTTPError(http.StatusBadRequest, "messages is required")
		}
	}
	return nil
}

// FilterProcessor handles content filtering.
type FilterProcessor struct {
	filterChain *filter.Chain
	auditLogger AuditLogger
	metrics     interface{ RecordFilterAction(string, string) }
}

// FilterResult contains the result of content filtering.
type FilterResult struct {
	Blocked    *filter.Result
	AllResults []filter.Result
}

// RunFilters executes the content filter chain.
func (fp *FilterProcessor) RunFilters(
	r *http.Request,
	aegisReq *types.AegisRequest,
	authInfo *auth.AuthInfo,
) (*FilterResult, error) {
	if fp.filterChain == nil {
		return &FilterResult{}, nil
	}

	results, blocked := fp.filterChain.Run(r.Context(), aegisReq)
	
	if blocked != nil {
		slog.Warn("request blocked by filter",
			"request_id", aegisReq.RequestID,
			"filter", blocked.FilterName,
			"detections", blocked.Detections,
			"score", blocked.Score,
			"org_id", authInfo.OrganizationID,
		)
		
		if fp.auditLogger != nil {
			fp.auditLogger.LogFilterBlock(
				aegisReq.RequestID,
				authInfo.OrganizationID,
				authInfo.TeamID,
				authInfo.KeyID,
				blocked.FilterName,
				blocked.Message,
				r.RemoteAddr,
			)
		}
		
		if fp.metrics != nil {
			fp.metrics.RecordFilterAction(blocked.FilterName, string(blocked.Action))
		}
		
		return nil, httputil.NewHTTPError(http.StatusForbidden, blocked.Message)
	}

	// Record flagged filters
	for _, fr := range results {
		if fr.Action == filter.ActionFlag && fp.metrics != nil {
			fp.metrics.RecordFilterAction(fr.FilterName, "flag")
		}
	}

	return &FilterResult{
		AllResults: results,
	}, nil
}

// ResponseBuilder handles response construction and enrichment.
type ResponseBuilder struct {
	costCalc interface {
		Calculate(provider, model string, promptTokens, completionTokens int) (float64, bool)
	}
}

// BuildResponse enriches a provider response with cost and metadata.
func (rb *ResponseBuilder) BuildResponse(
	aegisResp *types.AegisResponse,
	reqID string,
) {
	aegisResp.RequestID = reqID
	
	// Calculate cost using actual provider and model served
	if rb.costCalc != nil && (aegisResp.Usage.PromptTokens > 0 || aegisResp.Usage.CompletionTokens > 0) {
		if cost, found := rb.costCalc.Calculate(
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
}
