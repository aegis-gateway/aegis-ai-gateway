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
	LogModelDenied(requestID, orgID, teamID, keyID, keyPrefix, model string, ip string)
	LogRequestComplete(req audit.CompletedRequest)
	LogProviderFailure(req audit.CompletedRequest, reason string)
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

	// Enforce the API key's model allowlist.
	//
	// AuthInfo.AllowedModels comes from the api_keys row and was read in
	// exactly one place, ListModels, which decides what a key may SEE. Nothing
	// checked it on the path that decides what a key may USE, so a key
	// restricted to aegis-fast was served aegis-reasoning and billed for it.
	// docs/COMPLIANCE-MAPPING.md cites this field as the CC6.1 logical access
	// control, so the gap was between the claim and the code.
	//
	// Placed after the filter chain and before routing. Before routing because
	// a refused key must not reach a provider or influence provider selection;
	// after the filters for the reason the tool-capability refusal below gives
	// at length, which is that a content block is the more security-relevant
	// record and must not be preempted by a cheaper check.
	if !modelAllowed(authInfo.AllowedModels, aegisReq.Model) {
		slog.Warn("request refused: model not in the key's allowlist",
			"request_id", reqID,
			"model", aegisReq.Model,
			"org_id", authInfo.OrganizationID,
			"team_id", authInfo.TeamID,
		)
		// Only a CONFIGURED alias may be sealed.
		//
		// This check runs before ResolveRoute, and validation checks a model
		// name's length and character set rather than its existence, so
		// aegisReq.Model here is whatever the caller sent. Writing it to
		// audit_events.model would let any caller holding a key with a
		// non-empty allowlist put up to 128 characters of their own text into
		// the sealed, exported trail: the no-payload contract broken through a
		// field nobody thinks of as payload.
		//
		// The unknown name stays in the process log, which is bounded and not
		// the attested record, so an operator can still see what was asked for.
		deniedModel := aegisReq.Model
		if _, configured := h.modelsCfg().Models[deniedModel]; !configured {
			deniedModel = audit.UnconfiguredModel
		}
		if h.auditLogger != nil {
			h.auditLogger.LogModelDenied(reqID, authInfo.OrganizationID, authInfo.TeamID, authInfo.KeyID, authInfo.KeyPrefix, deniedModel, r.RemoteAddr)
		}
		httputil.WriteModelNotAllowedError(w, reqID,
			"model "+aegisReq.Model+" is not permitted for this API key")
		return
	}

	// Route to provider
	modelsCfg := h.modelsCfg()
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
		h.streamingHandler.HandleStream(w, r, reqID, providerReq, adapter, providerKey, originalModel, providerModel, authInfo, &aegisReq)
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
		// This request passed every gate and then failed at the provider.
		// Without an event here it would produce no attested record at all,
		// which is the completeness hole an allow event exists to close,
		// reached from the other side.
		//
		// The refusal is written BEFORE the event, so the recorded status is
		// one the gateway has actually sent rather than one it is about to
		// attempt. The success path was already ordered this way.
		httputil.WriteServiceUnavailableError(w, reqID, "Provider request failed")
		if h.auditLogger != nil {
			h.auditLogger.LogProviderFailure(
				completedRequest(reqID, authInfo, r, originalModel, providerKey, http.StatusServiceUnavailable, false),
				providerFailureReason(r, err, 0, audit.ReasonProviderUnreachable))
		}
		return
	}

	aegisResp, err := adapter.TransformResponse(r.Context(), providerResp)
	if err != nil {
		// err carries the provider status and a bounded excerpt of its body,
		// never the body itself: the adapters build it with redact.Excerpt for
		// the reason given at that call site. providerKey rather than
		// adapter.Name() so the log names the configured provider and not the
		// adapter type, which is shared across providers.
		slog.Error("failed to transform response",
			"request_id", reqID,
			"error", err,
			"status", providerResp.StatusCode,
			"provider", providerKey,
			"adapter", adapter.Name(),
		)
		// http.StatusInternalServerError, not providerResp.StatusCode: the
		// audit record states the status the GATEWAY SENT, and it sends 500
		// from WriteInternalError below. Recording an upstream 401
		// here would seal a row saying the client got 401 when it did not. The
		// upstream status is in the log line above, where it belongs.
		httputil.WriteInternalError(w, reqID, "Failed to process provider response")
		// The status is passed because the adapters read the body before they
		// inspect it: a caller leaving during a non-200 body read yields a
		// cancellation and no status error, and the provider fault would
		// otherwise be erased.
		if h.auditLogger != nil {
			h.auditLogger.LogProviderFailure(
				completedRequest(reqID, authInfo, r, originalModel, providerKey, http.StatusInternalServerError, false),
				providerFailureReason(r, err, providerResp.StatusCode, audit.ReasonProviderError))
		}
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

	// Send the response BEFORE recording anything about it.
	//
	// Completion is a claim about what the gateway managed to WRITE AND FLUSH,
	// which is the strongest thing an HTTP handler can establish. Encoding
	// straight into the ResponseWriter can fail, most obviously when the caller
	// has gone away, and the error used to be discarded, so a request whose
	// body was never written was still attested as a completed 200. The
	// streaming path treats delivery failure explicitly and this path agrees
	// with it. Neither path can prove the peer received the bytes; see
	// flushToClient in deliver.go.
	w.Header().Set("Content-Type", "application/json")
	deliveryErr := json.NewEncoder(w).Encode(aegisResp)
	if deliveryErr == nil {
		// Encode reporting no error only means net/http accepted the bytes into
		// its buffer. A small response can sit there until the handler returns,
		// so the socket write, and its failure, can happen after this point.
		// Flushing here is the last moment the gateway can still tell whether
		// the caller got the response it is about to attest.
		deliveryErr = flushToClient(w)
	}
	if deliveryErr != nil {
		slog.Warn("failed to deliver the response to the caller",
			"request_id", reqID,
			"error", deliveryErr,
			"provider", providerKey,
			"org_id", authInfo.OrganizationID,
		)
	}

	// Record usage asynchronously (non-blocking).
	//
	// Unconditional: the provider did the work and the spend happened whether
	// or not the bytes reached the caller, so omitting it on a delivery failure
	// would under-report cost. Only the audit outcome depends on delivery.
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

	// Attest the outcome, next to the usage write so the two cannot drift.
	//
	// audit_events held only refusals until this existed, so the sealed chain
	// recorded what was denied and nothing about what was permitted. The usage
	// row, the log line above and the Prometheus counters are all unsealed, so
	// none of them is evidence.
	//
	// Asynchronous, like every other audit write: Logger.Log spawns a goroutine
	// and this must never add latency to or fail the caller's request.
	//
	// The status is 200 either way, because that is the status line the gateway
	// sent: the first write into the ResponseWriter commits it, so a failure
	// part way through the body still went out as a 200. What changes is the
	// event, not the status.
	if h.auditLogger != nil {
		rec := completedRequest(reqID, authInfo, r, originalModel, providerKey, http.StatusOK, false)
		if deliveryErr != nil {
			h.auditLogger.LogProviderFailure(rec, audit.ReasonResponseNotDelivered)
		} else {
			h.auditLogger.LogRequestComplete(rec)
		}
	}
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
		// Same predicate ChatCompletions enforces, so what a key is shown and
		// what it may use cannot diverge.
		if !modelAllowed(authInfo.AllowedModels, name) {
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
