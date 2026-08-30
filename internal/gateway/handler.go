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
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/cost"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/filter"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/filter/policy"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/httputil"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/retry"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router/adapters"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/storage"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/telemetry"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/validation"
)

// AuditLogger defines the interface for audit logging (to avoid circular dependency).
type AuditLogger interface {
	LogFilterBlock(requestID, orgID, teamID, keyID, filterType, reason string, ip string)
	LogPricingDenied(requestID, orgID, teamID, keyID, provider, model, mode string, ip string)
	LogModelDenied(requestID, orgID, teamID, keyID, model string, statusCode int, ip string)
	LogRequestComplete(ev audit.CompletionEvent)
	LogProviderFailure(ev audit.CompletionEvent, stage string)
}

// completionEvent builds the audit event for a request that reached the
// provider, from the identity and route that are settled by then.
//
// One constructor for both outcomes and both paths, so the streaming and
// non-streaming records cannot drift in what they attribute. providerKey and
// providerModel, never adapter.Name() and never the requested alias: the pair
// has to match what the pricing record and the pricing gate used, or the
// attested record and the billed record describe different requests.
func completionEvent(reqID string, authInfo *auth.AuthInfo, providerKey, providerModel string,
	streaming bool, statusCode int, ip string) audit.CompletionEvent {
	return audit.CompletionEvent{
		RequestID:  reqID,
		OrgID:      authInfo.OrganizationID,
		TeamID:     authInfo.TeamID,
		UserID:     authInfo.UserID,
		KeyID:      authInfo.KeyID,
		Provider:   providerKey,
		Model:      providerModel,
		Streaming:  streaming,
		StatusCode: statusCode,
		IP:         ip,
	}
}

// Handler holds dependencies for the gateway HTTP handlers.
type Handler struct {
	registry         *router.Registry
	healthTracker    *router.HealthTracker
	modelsCfg        func() *config.ModelsConfig
	cfg              func() *config.Config
	filterChain      *filter.Chain
	policyEvaluator  *policy.Evaluator
	metrics          *telemetry.Metrics
	costCalc         *cost.Calculator
	usageRecorder    *storage.UsageRecorder
	auditLogger      AuditLogger
	retryExecutor    *retry.Executor
	contextMonitor   *retry.ContextMonitor
	validator        *validation.Validator
	streamingHandler *StreamingHandler
}

func NewHandler(registry *router.Registry, healthTracker *router.HealthTracker, modelsCfg func() *config.ModelsConfig, cfg func() *config.Config, filterChain *filter.Chain, policyEvaluator *policy.Evaluator, metrics *telemetry.Metrics, costCalc *cost.Calculator, usageRecorder *storage.UsageRecorder, auditLogger AuditLogger, retryExecutor *retry.Executor, contextMonitor *retry.ContextMonitor, validator *validation.Validator) *Handler {
	h := &Handler{
		registry:        registry,
		healthTracker:   healthTracker,
		modelsCfg:       modelsCfg,
		cfg:             cfg,
		filterChain:     filterChain,
		policyEvaluator: policyEvaluator,
		metrics:         metrics,
		costCalc:        costCalc,
		usageRecorder:   usageRecorder,
		auditLogger:     auditLogger,
		retryExecutor:   retryExecutor,
		contextMonitor:  contextMonitor,
		validator:       validator,
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

	// Decode against an explicit allowlist. A field AEGIS does not knowingly
	// support is a 400 naming the field, not a discarded key: silently
	// accepting input the gateway cannot honour is how tool calling came to be
	// stripped from every agent request without anything reporting a problem.
	parsed, err := types.DecodeChatCompletion(body)
	if err != nil {
		h.writeDecodeError(w, reqID, authInfo.OrganizationID, err)
		return
	}
	aegisReq := *parsed

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

	// Enforce the API key's model allowlist before routing.
	//
	// This is an authorization check on the alias the caller asked for, not on
	// the concrete model it resolves to: allowed_models holds alias names, and
	// GET /v1/models advertises alias names, so enforcing on anything else
	// would let the listing and the enforcement disagree. AuthInfo.ModelAllowed
	// is the single definition both use.
	//
	// Deliberately after the filter chain and before ResolveRoute. Before
	// routing, because a request the key may not make must not reach a
	// provider, be priced, or open a circuit. After the filters, because a
	// request carrying a secret is already recorded as a filter_block today,
	// and moving this check above them would replace that attested event with
	// this one rather than adding to it. The narrower governance record wins,
	// which is the same ordering rule the tool-capability refusal below states.
	//
	// The membership check comes first and is load-bearing, not defensive.
	// DecodeChatCompletion accepts any string as a model, and this refusal
	// writes that string to audit_events.model, which the leaf hash covers and
	// the sealer seals. Enforcing on an unconfigured alias would therefore let
	// a caller holding a restricted key put up to 128 characters of arbitrary
	// text into the attested record, permanently, which is the exact violation
	// this pull request exists to close. An alias that is a key of
	// modelsCfg.Models is operator-configured by construction.
	//
	// An unconfigured alias falls through to ResolveRoute, which refuses it as
	// an unknown model and writes no audit row. That is what happens today for
	// every key, and it is unchanged.
	modelsCfg := h.modelsCfg()
	_, aliasConfigured := modelsCfg.Models[aegisReq.Model]
	if aliasConfigured && !authInfo.ModelAllowed(aegisReq.Model) {
		slog.Warn("request refused: model not in the key's allowlist",
			"request_id", reqID,
			"model", aegisReq.Model,
			"org_id", authInfo.OrganizationID,
			"team_id", authInfo.TeamID,
		)
		if h.auditLogger != nil {
			h.auditLogger.LogModelDenied(reqID, authInfo.OrganizationID, authInfo.TeamID,
				authInfo.KeyID, aegisReq.Model, http.StatusServiceUnavailable, r.RemoteAddr)
		}
		// The same status and envelope as the classification-ceiling refusal
		// below, which is the other way a key can be denied a route it named.
		// Two refusals that mean "this key may not reach that provider" should
		// not be distinguishable by shape, and the specific cause is in the
		// audit row rather than in a response to the caller who tripped it.
		httputil.WriteServiceUnavailableError(w, reqID,
			"No provider available: no eligible provider for model "+aegisReq.Model+
				" at classification "+string(aegisReq.Classification))
		return
	}

	// Route to provider
	route, err := router.ResolveRoute(modelsCfg, h.registry, h.healthTracker, aegisReq.Model, string(aegisReq.Classification))
	if err != nil {
		httputil.WriteServiceUnavailableError(w, reqID, "No provider available: "+err.Error())
		return
	}
	// providerKey is the configured provider name; adapter.Name() is only the
	// adapter type and is shared across providers (azure_openai and
	// internal_vllm both report "openai"). Pricing, metrics and usage must use
	// providerKey or they attribute to the wrong provider.
	adapter, providerKey, providerModel := route.Adapter, route.ProviderKey, route.Model

	// Set provider type for policy evaluation (adapter.Name() returns "openai", "anthropic", etc.)
	aegisReq.ProviderType = adapter.Name()

	// Run OPA policy evaluation after routing (needs provider type)
	if h.policyEvaluator != nil && h.policyEvaluator.Enabled() {
		result := h.policyEvaluator.ScanRequest(r.Context(), &aegisReq)
		if result.Action == filter.ActionBlock {
			slog.Warn("request blocked by policy",
				"request_id", reqID,
				"filter", result.FilterName,
				"org_id", authInfo.OrganizationID,
			)
			if h.auditLogger != nil {
				h.auditLogger.LogFilterBlock(reqID, authInfo.OrganizationID, authInfo.TeamID, authInfo.KeyID, result.FilterName, result.Message, r.RemoteAddr)
			}
			if h.metrics != nil {
				h.metrics.RecordFilterAction(result.FilterName, string(result.Action))
			}
			httputil.WriteContentBlockedError(w, reqID, result.Message)
			return
		}
	}

	// Refuse a tool-bearing request routed to an adapter that cannot express
	// tools, rather than dispatching it without them. Dropping the tools here
	// would be the original defect wearing a different hat: the provider would
	// answer in prose and the agent loop would stall with nothing reporting a
	// problem.
	//
	// Deliberately after policy evaluation. A policy denial is a governance
	// decision and is written to the audit trail; this refusal is a
	// compatibility error and is not. Checking capability first would let a
	// 400 preempt a 451 that should have been recorded, which matters now that
	// tool names are exposed to Rego and a rule can deny on them.
	if aegisReq.HasTools() && !adapter.SupportsTools() {
		slog.Warn("tool request refused: provider adapter cannot carry tools",
			"request_id", reqID,
			"provider", providerKey,
			"adapter", adapter.Name(),
			"model", aegisReq.Model,
			"tools_offered", len(aegisReq.Tools),
			"org_id", authInfo.OrganizationID,
		)
		if h.metrics != nil {
			h.metrics.RecordToolRequestRefused(providerKey, adapter.Name())
		}
		httputil.WriteError(w, reqID, http.StatusBadRequest,
			"invalid_request_error", "tools_unsupported_by_provider",
			"model "+aegisReq.Model+" routes to provider "+providerKey+
				", whose adapter ("+adapter.Name()+") does not carry tool definitions, tool calls or tool results. "+
				"AEGIS refuses the request rather than forwarding it without its tools. "+
				"Route this request to an OpenAI-compatible provider, or remove the tool fields",
		)
		return
	}

	// Override model with the provider-specific model name
	originalModel := aegisReq.Model
	aegisReq.Model = providerModel

	// Enforce pricing policy before dispatching to provider.
	// This prevents unpriced traffic from silently bypassing spend controls.
	if h.costCalc != nil && h.cfg != nil {
		cfg := h.cfg()
		// Anything that is not explicitly a documented pass-through mode is
		// treated as deny. A typo or an unsupported value must not silently
		// reopen the bypass this gate exists to close.
		mode := cfg.Cost.OnMissingPricing
		switch mode {
		case "flag", "allow":
			// documented non-deny modes
		case "", "deny":
			mode = "deny"
		default:
			slog.Error("unrecognised on_missing_pricing value; treating as deny",
				"configured", cfg.Cost.OnMissingPricing, "request_id", reqID)
			mode = "deny"
		}

		if mode != "allow" && !h.costCalc.HasPricing(providerKey, providerModel) {
			if h.metrics != nil {
				h.metrics.UnpricedRequestsTotal.WithLabelValues(providerKey, providerModel, mode).Inc()
			}
			slog.Warn("pricing_unknown: no pricing entry for routed model",
				"event_type", "pricing_unknown",
				"provider", providerKey,
				"model", providerModel,
				"mode", mode,
				"request_id", reqID,
				"org_id", authInfo.OrganizationID,
			)
			// Persist the decision. Both deny and flag are governance events and
			// belong in the audit trail, not only in process logs that rotate.
			if h.auditLogger != nil {
				h.auditLogger.LogPricingDenied(reqID, authInfo.OrganizationID, authInfo.TeamID,
					authInfo.KeyID, providerKey, providerModel, mode, r.RemoteAddr)
			}
			if mode == "deny" {
				httputil.WriteError(w, reqID, http.StatusPaymentRequired,
					"billing_error", "pricing_unknown",
					"no pricing configuration for "+providerKey+"/"+providerModel+"; contact your AEGIS administrator",
				)
				return
			}
			// mode == "flag": logged, counted and audited above; request proceeds.
		}
	}

	// Start monitoring context for cancellation
	var cleanupMonitor func()
	if h.contextMonitor != nil {
		cleanupMonitor = h.contextMonitor.Watch(r.Context(), reqID, adapter.Name())
		defer cleanupMonitor()
	}

	// Transform and send to provider
	providerReq, err := adapter.TransformRequest(r.Context(), &aegisReq)
	if err != nil {
		// A construct the provider cannot express is the caller's input, not a
		// gateway failure. Reporting it as a 500 tells an agent to retry a
		// request that can never succeed, and hides the named refusal this
		// translation exists to produce. The construct is positional by
		// invariant, so neither the response nor this log line carries a
		// scanned value.
		var unmappable *adapters.UnmappableError
		if errors.As(err, &unmappable) {
			slog.Warn("request refused: construct cannot be expressed for this provider",
				"request_id", reqID,
				"provider", providerKey,
				"adapter", adapter.Name(),
				"construct", unmappable.Construct,
			)
			httputil.WriteError(w, reqID, http.StatusBadRequest,
				"invalid_request_error", "unmappable_for_provider",
				unmappable.Construct+": "+unmappable.Detail,
			)
			return
		}
		slog.Error("failed to transform request", "error", err, "provider", adapter.Name())
		httputil.WriteInternalError(w, reqID, "Failed to prepare provider request")
		return
	}

	// Streaming: forward SSE events from provider to client with full monitoring
	if aegisReq.Stream {
		h.streamingHandler.HandleStream(w, r, reqID, providerReq, adapter, providerKey, originalModel, authInfo, &aegisReq)
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
		// retry.Executor returns the last response alongside
		// ErrMaxRetriesExceeded when it gave up on a retryable status, so a
		// non-nil response here means the provider answered and kept
		// answering badly. That is provider_http_error, the same stage the
		// streaming path seals for the same rejection; provider_unreachable
		// is for a send that produced no response at all.
		//
		// Getting this wrong is not a cosmetic mislabel. reason is one of the
		// twenty-six fields the leaf hash covers, so once a checkpoint covers
		// the row the stage cannot be corrected without verify-chain
		// reporting the row as tampered.
		stage := audit.FailureProviderUnreachable
		status := 0
		if providerResp != nil {
			// Nothing reads this body: the retry executor has already decided,
			// and the caller gets a fixed message. Closing it returns the
			// connection to the pool instead of leaking one per exhausted
			// retry.
			_ = providerResp.Body.Close()
			status = providerResp.StatusCode
			if status < 200 || status > 299 {
				stage = audit.FailureProviderHTTPError
			}
		}

		slog.Error("provider request failed",
			"request_id", reqID,
			"error", err,
			"provider", providerKey,
			"adapter", adapter.Name(),
			"provider_status", status,
			"stage", stage,
		)
		if h.healthTracker != nil {
			h.healthTracker.RecordFailure(adapter.Name())
		}
		// This request passed every gate and then failed. Without an event
		// here it leaves no attested trace: no denial applies, and the
		// completion event below never runs.
		if h.auditLogger != nil {
			h.auditLogger.LogProviderFailure(
				completionEvent(reqID, authInfo, providerKey, providerModel, false,
					http.StatusServiceUnavailable, r.RemoteAddr),
				stage)
		}
		httputil.WriteServiceUnavailableError(w, reqID, "Provider request failed")
		return
	}

	aegisResp, err := adapter.TransformResponse(r.Context(), providerResp)
	if err != nil {
		// On a non-2xx the adapters put the provider's response body in this
		// error. It reaches the log already bounded and redacted, by
		// adapters.RedactProviderError at the point the body is read: a
		// provider error body can quote the request back, and it must not be
		// repeated whole. providerKey rather than adapter.Name() so the line
		// names the configured provider rather than the adapter type it shares
		// with others.
		slog.Error("failed to transform response",
			"request_id", reqID,
			"error", err,
			"provider", providerKey,
			"adapter", adapter.Name(),
		)
		if h.auditLogger != nil {
			// TransformResponse returns an error for two unrelated things: a
			// non-success status from the provider, and a success it could not
			// read or decode. They are different failures and the streaming
			// path already seals them apart, so attesting both as
			// provider_response_invalid would make the recorded stage for one
			// provider rejection depend on whether the caller asked to stream.
			stage := audit.FailureProviderResponseInvalid
			if providerResp.StatusCode < 200 || providerResp.StatusCode > 299 {
				stage = audit.FailureProviderHTTPError
			}
			h.auditLogger.LogProviderFailure(
				completionEvent(reqID, authInfo, providerKey, providerModel, false,
					http.StatusInternalServerError, r.RemoteAddr),
				stage)
		}
		httputil.WriteInternalError(w, reqID, "Failed to process provider response")
		return
	}

	if h.healthTracker != nil {
		h.healthTracker.RecordSuccess(adapter.Name())
	}

	aegisResp.RequestID = reqID

	// Adapters set Provider to their own type ("openai", "anthropic") because
	// that is all they know. Everything downstream — cost lookup, metrics
	// labels, the usage record — needs the configured provider key, or an
	// azure_openai / internal_vllm route passes the pre-dispatch gate and then
	// fails to price, recording zero spend under the wrong provider. Normalise
	// once, here, so the two identities cannot diverge again below.
	aegisResp.Provider = providerKey

	// Calculate cost using actual provider and model served.
	if h.costCalc != nil {
		// Calculate rather than CalculateSimple: the cached subset is priced at
		// the cached_input rate, which several models set an order of magnitude
		// below input. CalculateSimple leaves CachedTokens zero, so every cache
		// read was billed at the full rate.
		if cost, found := h.costCalc.Calculate(cost.RequestDetails{
			Provider:           aegisResp.Provider,
			Model:              aegisResp.Model,
			PromptTokens:       aegisResp.Usage.PromptTokens,
			CachedTokens:       aegisResp.Usage.CachedPromptTokens(),
			CacheWrite5mTokens: aegisResp.Usage.CacheWrite5mTokens(),
			CacheWrite1hTokens: aegisResp.Usage.CacheWrite1hTokens(),
			CompletionTokens:   aegisResp.Usage.CompletionTokens,
		}); found {
			aegisResp.EstimatedCostUSD = cost
		} else {
			slog.Warn("pricing_unknown: no pricing data for served model",
				"event_type", "pricing_unknown",
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
		// Two counts, because they are two different facts. tools_called is
		// what the conversation had already invoked when it arrived;
		// tools_returned is what the model asked for in this response. The
		// streaming path logs the same pair, reconstructed from the deltas.
		"tools_offered", len(aegisReq.Tools),
		"tools_called", len(aegisReq.CalledToolNames()),
		"tools_returned", countReturnedToolCalls(aegisResp),
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

	// Attest the allow, beside the usage record rather than anywhere else in
	// this function, so the two cannot come to describe different requests.
	// audit_events is the only table the sealer covers, so this is what puts a
	// permitted request into the sealed chain at all; usage_records is
	// operational data and is not attested.
	if h.auditLogger != nil {
		// providerModel, the value from configs/models.yaml, rather than
		// aegisResp.Model, which is the provider's echo of it. The echo is
		// text an upstream controls, and this column is sealed into the chain
		// and exported by the read API, so it takes the operator-configured
		// value. usage_records.model_served keeps the echo for reconciliation
		// against a provider bill.
		h.auditLogger.LogRequestComplete(completionEvent(reqID, authInfo,
			providerKey, providerModel, false, http.StatusOK, r.RemoteAddr))
	}

	// Record usage asynchronously (non-blocking)
	if h.usageRecorder != nil {
		h.usageRecorder.RecordUsage(storage.UsageRecord{
			RequestID:          reqID,
			OrganizationID:     authInfo.OrganizationID,
			TeamID:             authInfo.TeamID,
			UserID:             authInfo.UserID,
			APIKeyID:           authInfo.KeyID,
			ModelRequested:     originalModel,
			ModelServed:        aegisResp.Model,
			Provider:           aegisResp.Provider,
			Classification:     string(authInfo.MaxClassification),
			PromptTokens:       aegisResp.Usage.PromptTokens,
			CompletionTokens:   aegisResp.Usage.CompletionTokens,
			TotalTokens:        aegisResp.Usage.TotalTokens,
			CachedTokens:       aegisResp.Usage.CachedPromptTokens(),
			CacheWrite5mTokens: aegisResp.Usage.CacheWrite5mTokens(),
			CacheWrite1hTokens: aegisResp.Usage.CacheWrite1hTokens(),
			EstimatedCostUSD:   aegisResp.EstimatedCostUSD,
			DurationMs:         totalDuration.Milliseconds(),
			StatusCode:         http.StatusOK,
			Project:            aegisReq.Project,
			Stream:             false,
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
		// The same predicate ChatCompletions enforces. Listing an alias here
		// that the completion path would refuse is the defect this shared
		// method exists to make impossible.
		if !authInfo.ModelAllowed(name) {
			continue
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
