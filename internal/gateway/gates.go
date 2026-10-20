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

// The request gates shared by every ingress. /v1/chat/completions and
// /v1/messages run the same checks in the same order, so each lives here once:
// two copies of a governance check drift, and the drift is invisible until one
// ingress enforces something the other does not.

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/filter"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/httputil"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// readBoundedBody reads the request body under server.max_request_bytes,
// answering 413 or 400 itself when it cannot. ok is false when it answered.
func (h *Handler) readBoundedBody(w http.ResponseWriter, r *http.Request, reqID string) (body []byte, ok bool) {
	// Parse request body.
	//
	// Bounded, because nothing bounded it before: io.ReadAll on the raw body
	// would buffer whatever a key holder sent. Image parts made the gap matter,
	// since they arrive as base64 inside the JSON body.
	bodyLimit := config.DefaultMaxRequestBytes
	if h.cfg != nil {
		if c := h.cfg(); c != nil {
			bodyLimit = c.Server.RequestBodyLimit()
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, bodyLimit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httputil.WriteError(w, reqID, http.StatusRequestEntityTooLarge,
				"invalid_request_error", "request_too_large",
				fmt.Sprintf("request body exceeds %d bytes (server.max_request_bytes)", bodyLimit))
			return nil, false
		}
		httputil.WriteBadRequestError(w, reqID, "Failed to read request body")
		return nil, false
	}
	return body, true
}

// runFilterChain runs the content filters over req and answers a block
// itself, sealing it. It reports whether the request may continue.
func (h *Handler) runFilterChain(w http.ResponseWriter, r *http.Request, reqID string, authInfo *auth.AuthInfo, aegisReq *types.AegisRequest) bool {
	// Run content filter chain (secrets, injection, PII, policy)
	if h.filterChain != nil {
		results, blocked := h.filterChain.Run(r.Context(), aegisReq)
		if blocked != nil {
			slog.Warn("request blocked by filter",
				"request_id", reqID,
				"filter", blocked.FilterName,
				"detections", blocked.Detections,
				"score", blocked.Score,
				"org_id", authInfo.OrganizationID,
			)
			if h.auditLogger != nil {
				h.auditLogger.LogFilterBlock(reqID, authInfo.OrganizationID, authInfo.TeamID, authInfo.KeyID, blocked.FilterName, blocked.Message, audit.SourceOf(r))
			}
			if h.metrics != nil {
				h.metrics.RecordFilterAction(blocked.FilterName, string(blocked.Action))
			}
			httputil.WriteContentBlockedError(w, reqID, blocked.Message)
			return false
		}
		// Record flagged filters
		for _, fr := range results {
			if fr.Action == filter.ActionFlag && h.metrics != nil {
				h.metrics.RecordFilterAction(fr.FilterName, "flag")
			}
		}
	}
	return true
}

// enforceModelAllowlist refuses, and seals, a model outside the key's
// allowlist. It reports whether the request may continue.
func (h *Handler) enforceModelAllowlist(w http.ResponseWriter, r *http.Request, reqID string, authInfo *auth.AuthInfo, aegisReq *types.AegisRequest) bool {
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
			h.auditLogger.LogModelDenied(reqID, authInfo.OrganizationID, authInfo.TeamID, authInfo.KeyID, authInfo.KeyPrefix, deniedModel, audit.SourceOf(r))
		}
		httputil.WriteModelNotAllowedError(w, reqID,
			"model "+aegisReq.Model+" is not permitted for this API key")
		return false
	}
	return true
}

// Refuse unscanned content parts on an alias no route of which is cleared
// for them.
//
// No filter reads an image, so one may leave only for a route whose
// operator listed its kind in unscanned_parts. This is the permanent half
// of that gate: the alias is never cleared, so the refusal is a governance
// decision, sealed and answered like a filter block, and not the retryable 503 a cleared
// route being down produces from ResolveRouteFor below. The sealed reason
// is a fixed rule identifier and never quotes the request.
//
// The gate applies only to a configured alias. An unknown alias is not
// "uncleared for images", it does not exist: it falls through to
// ResolveRouteFor and gets the same unknown-model 503 a text request
// gets, and no filter block is sealed for an alias nobody configured.
// The request is refused either way.
//
// accept, when not nil, narrows the routes that can clear the alias to those
// whose provider the ingress can actually use: /v1/messages passes
// h.isAnthropicRoute, judged from providers.yaml, so a cleared OpenAI-type
// route does not clear the alias there while a cleared Anthropic route that
// is merely unregistered still does. Chat completions passes nil and counts
// every route.
func (h *Handler) enforceUnscannedGate(w http.ResponseWriter, r *http.Request, reqID string, authInfo *auth.AuthInfo, aegisReq *types.AegisRequest, modelsCfg *config.ModelsConfig, unscanned types.UnscannedSummary, accept func(provider string) bool) bool {
	_, aliasConfigured := modelsCfg.Models[aegisReq.Model]
	if !unscanned.Empty() && aliasConfigured &&
		!router.AnyRouteAdmitsUnscannedWhere(modelsCfg, aegisReq.Model, unscanned.Kinds, accept) {
		slog.Warn("request refused: unscanned content parts on an uncleared alias",
			"request_id", reqID,
			"model", aegisReq.Model,
			"unscanned_parts", unscanned.Parts,
			"unscanned_kinds", unscanned.Kinds,
			"org_id", authInfo.OrganizationID,
			"team_id", authInfo.TeamID,
		)
		if h.auditLogger != nil {
			h.auditLogger.LogFilterBlock(reqID, authInfo.OrganizationID, authInfo.TeamID, authInfo.KeyID,
				UnscannedPartsFilter, UnscannedPartsNotClearedReason, audit.SourceOf(r))
		}
		// 451 like every other sealed content block: LogFilterBlock seals 451,
		// and the record and the response must state the same status.
		httputil.WriteError(w, reqID, 451, "content_filter_error", "unscanned_parts_not_cleared",
			"model "+aegisReq.Model+" is not cleared to carry image content parts. AEGIS cannot scan an "+
				"image for secrets, PII or prompt injection, so it forwards one only to a route whose "+
				"operator has listed \"image\" in unscanned_parts in configs/models.yaml. "+
				"Use a model cleared for images, or remove the image parts")
		return false
	}
	return true
}

// evaluatePolicy runs the OPA policy and answers a denial itself, sealing it.
// It reports whether the request may continue.
func (h *Handler) evaluatePolicy(w http.ResponseWriter, r *http.Request, reqID string, authInfo *auth.AuthInfo, aegisReq *types.AegisRequest) bool {
	// Run OPA policy evaluation after routing (needs provider type)
	if h.policyEvaluator != nil && h.policyEvaluator.Enabled() {
		result := h.policyEvaluator.ScanRequest(r.Context(), aegisReq)
		if result.Action == filter.ActionBlock {
			slog.Warn("request blocked by policy",
				"request_id", reqID,
				"filter", result.FilterName,
				"org_id", authInfo.OrganizationID,
			)
			if h.auditLogger != nil {
				h.auditLogger.LogFilterBlock(reqID, authInfo.OrganizationID, authInfo.TeamID, authInfo.KeyID, result.FilterName, result.Message, audit.SourceOf(r))
			}
			if h.metrics != nil {
				h.metrics.RecordFilterAction(result.FilterName, string(result.Action))
			}
			httputil.WriteContentBlockedError(w, reqID, result.Message)
			return false
		}
	}
	return true
}

// enforcePricing applies cost.on_missing_pricing to the routed model before
// dispatch, answering and sealing a denial itself. It reports whether the
// request may continue.
func (h *Handler) enforcePricing(w http.ResponseWriter, r *http.Request, reqID string, authInfo *auth.AuthInfo, providerKey, providerModel string) bool {
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
					authInfo.KeyID, providerKey, providerModel, mode, audit.SourceOf(r))
			}
			if mode == "deny" {
				httputil.WriteError(w, reqID, http.StatusPaymentRequired,
					"billing_error", "pricing_unknown",
					"no pricing configuration for "+providerKey+"/"+providerModel+"; contact your AEGIS administrator",
				)
				return false
			}
			// mode == "flag": logged, counted and audited above; request proceeds.
		}
	}
	return true
}
