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
	"sort"
	"strings"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/cost"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/httputil"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/redact"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/router/adapters"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/storage"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/telemetry"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// anthropicAPIVersion is the only anthropic-version the Messages API defines.
const anthropicAPIVersion = "2023-06-01"

// maxProviderErrorBody bounds a provider error body relayed to the caller.
const maxProviderErrorBody = 1 << 20

// Messages handles POST /v1/messages, the Anthropic Messages API.
//
// The request is governed exactly as a chat completion is: the same body cap,
// filter chain, key allowlist, image gate, policy and pricing gate, over a
// projection of the body (types.MessagesRequest.Projection). What differs is
// what leaves. The client's own body is forwarded, with only the model
// replaced, to a route whose adapter speaks the Messages API natively, and the
// provider's response is relayed as it came, event stream included. Nothing is
// translated, so nothing Claude Code depends on (thinking signatures, cache
// markers, effort, context management) is approximated.
func (h *Handler) Messages(w http.ResponseWriter, r *http.Request) {
	h.serveMessages(w, r, adapters.NativeMessages)
}

// CountTokens handles POST /v1/messages/count_tokens. It sends the whole
// prompt to the provider, so it passes every gate a message does; it is
// attested like one, and is free, so it is not priced.
func (h *Handler) CountTokens(w http.ResponseWriter, r *http.Request) {
	h.serveMessages(w, r, adapters.NativeCountTokens)
}

func (h *Handler) serveMessages(w http.ResponseWriter, r *http.Request, endpoint string) {
	reqID := w.Header().Get("X-Request-ID")
	receivedAt := time.Now()
	countOnly := endpoint == adapters.NativeCountTokens

	authInfo, ok := auth.AuthFromContext(r.Context())
	if !ok {
		httputil.WriteAuthError(w, reqID, "Not authenticated")
		return
	}

	// The SDKs append ?beta=true when a beta header is set. Anything else in
	// the query is refused rather than dropped.
	for k, vs := range r.URL.Query() {
		if k != "beta" || len(vs) != 1 || vs[0] != "true" {
			httputil.WriteError(w, reqID, http.StatusBadRequest, "invalid_request_error", "unsupported_query_parameter",
				"query parameter "+k+" is not supported on this endpoint")
			return
		}
	}
	if v := r.Header.Get("anthropic-version"); v != "" && v != anthropicAPIVersion {
		httputil.WriteError(w, reqID, http.StatusBadRequest, "invalid_request_error", "unsupported_anthropic_version",
			"anthropic-version "+v+" is not supported; AEGIS forwards "+anthropicAPIVersion)
		return
	}
	betas, refused := h.allowedBetas(r)
	if refused != "" {
		// The value is caller-controlled header text that no filter has seen,
		// so only its length is logged. The caller gets it back in the error.
		slog.Warn("request refused: anthropic-beta not in the allowlist",
			"request_id", reqID, "beta_length", len(refused), "org_id", authInfo.OrganizationID)
		httputil.WriteError(w, reqID, http.StatusBadRequest, "invalid_request_error", "unsupported_beta",
			"anthropic-beta "+refused+" is not in anthropic_messages.allowed_betas. A beta switches on provider "+
				"behaviour, and some change pricing, so AEGIS forwards only flags an operator has listed")
		return
	}

	body, ok := h.readBoundedBody(w, r, reqID)
	if !ok {
		return
	}
	defer func() { _ = r.Body.Close() }()

	msgReq, err := types.DecodeMessages(body)
	if err != nil {
		h.writeDecodeError(w, reqID, authInfo.OrganizationID, err)
		return
	}
	aegisReq := *msgReq.Projection()
	aegisReq.RequestID = reqID
	aegisReq.OrganizationID = authInfo.OrganizationID
	aegisReq.TeamID = authInfo.TeamID
	aegisReq.UserID = authInfo.UserID
	aegisReq.APIKeyID = authInfo.KeyID
	aegisReq.Classification = authInfo.MaxClassification
	aegisReq.ReceivedAt = receivedAt
	aegisReq.Project = r.Header.Get("X-Aegis-Project")
	aegisReq.TraceContext = r.Header.Get("X-Aegis-Trace-Context")
	if countOnly {
		aegisReq.Stream = false
	}

	if err := h.agentValidator.Validate(&aegisReq); err != nil {
		slog.Warn("request validation failed",
			"request_id", reqID, "org_id", authInfo.OrganizationID, "error", err.Error())
		httputil.WriteBadRequestError(w, reqID, err.Error())
		return
	}

	if !h.runFilterChain(w, r, reqID, authInfo, &aegisReq) {
		return
	}
	if !h.enforceModelAllowlist(w, r, reqID, authInfo, &aegisReq) {
		return
	}
	unscanned := aegisReq.UnscannedParts()
	modelsCfg := h.modelsCfg()
	if !h.enforceUnscannedGate(w, r, reqID, authInfo, &aegisReq, modelsCfg, unscanned, h.isAnthropicRoute) {
		return
	}

	route, err := router.ResolveRouteWhere(modelsCfg, h.registry, h.healthTracker, aegisReq.Model,
		string(aegisReq.Classification), unscanned.Kinds, adapters.SupportsAnthropicNative)
	if err != nil {
		// A configured alias with no Anthropic provider anywhere in its routes
		// can never serve this endpoint: say so, as a 400, rather than a 503
		// that invites a retry. Provider type is read from providers.yaml, so
		// an Anthropic route that is only unregistered (no API key) is an
		// outage, not this refusal. Anything else is the same no-provider 503
		// the chat path returns.
		if _, configured := modelsCfg.Models[aegisReq.Model]; configured &&
			!router.AnyRouteMatches(modelsCfg, aegisReq.Model, h.isAnthropicRoute) {
			httputil.WriteError(w, reqID, http.StatusBadRequest, "invalid_request_error", "model_not_anthropic",
				"model "+aegisReq.Model+" routes to no Anthropic provider. /v1/messages forwards the request "+
					"unchanged and so serves only aliases routed to an Anthropic-type provider; use "+
					"/v1/chat/completions for this model")
			return
		}
		httputil.WriteServiceUnavailableError(w, reqID, "No provider available: "+err.Error())
		return
	}
	native := route.Adapter.(adapters.AnthropicNative)
	providerKey, providerModel := route.ProviderKey, route.Model
	aegisReq.ProviderType = native.Name()

	if !h.evaluatePolicy(w, r, reqID, authInfo, &aegisReq) {
		return
	}
	if !countOnly && !h.enforcePricing(w, r, reqID, authInfo, providerKey, providerModel) {
		return
	}

	forward, err := msgReq.ForwardBody(providerModel)
	if err != nil {
		slog.Error("failed to build forward body", "request_id", reqID, "error", err)
		httputil.WriteInternalError(w, reqID, "Failed to prepare provider request")
		return
	}

	if h.contextMonitor != nil {
		defer h.contextMonitor.Watch(r.Context(), reqID, native.Name())()
	}

	call := nativeCall{
		h: h, w: w, r: r, reqID: reqID, authInfo: authInfo, req: &aegisReq,
		adapter: native, providerKey: providerKey, providerModel: providerModel,
		originalModel: aegisReq.Model, endpoint: endpoint, body: forward, betas: betas,
		unscanned: unscanned, receivedAt: receivedAt, stream: msgReq.Stream && !countOnly,
	}
	call.run()
}

// allowedBetas returns the request's anthropic-beta flags, or the first one
// outside the configured allowlist.
func (h *Handler) allowedBetas(r *http.Request) (betas []string, refused string) {
	allowed := config.DefaultAnthropicBetas
	if h.cfg != nil {
		if c := h.cfg(); c != nil {
			allowed = c.AnthropicMessages.AllowedBetas
		}
	}
	set := make(map[string]bool, len(allowed))
	for _, b := range allowed {
		set[b] = true
	}
	for _, header := range r.Header.Values("anthropic-beta") {
		for _, b := range strings.Split(header, ",") {
			b = strings.TrimSpace(b)
			if b == "" {
				continue
			}
			if !set[b] {
				return nil, b
			}
			betas = append(betas, b)
		}
	}
	sort.Strings(betas)
	return betas, ""
}

// nativeCall is one dispatch of a governed /v1/messages request.
type nativeCall struct {
	h             *Handler
	w             http.ResponseWriter
	r             *http.Request
	reqID         string
	authInfo      *auth.AuthInfo
	req           *types.AegisRequest
	adapter       adapters.AnthropicNative
	providerKey   string
	providerModel string
	originalModel string
	endpoint      string
	body          []byte
	betas         []string
	unscanned     types.UnscannedSummary
	receivedAt    time.Time
	stream        bool
}

func (c *nativeCall) run() {
	h := c.h
	ctx := c.r.Context()
	var cancel context.CancelFunc
	if c.stream {
		total := c.streamConfig().TotalTimeout
		ctx, cancel = context.WithTimeout(ctx, total)
		defer cancel()
		// server.write_timeout bounds a whole response, and would cut a long
		// agent turn off mid-stream with nothing recorded. Extend it for this
		// stream only, to the stream's own deadline plus room for the final
		// error event.
		_ = http.NewResponseController(c.w).SetWriteDeadline(time.Now().Add(total + 10*time.Second))
	}
	// Every attempt is built from egressCtx, so a provider failure seals the
	// unscanned-part metadata only if some attempt wrote the body.
	egress := &bodyEgress{}
	egressCtx := withBodyEgress(ctx, egress)

	send := func(ctx context.Context, _ int) (*http.Response, error) {
		req, err := c.adapter.NativeRequest(ctx, c.endpoint, c.body, c.betas)
		if err != nil {
			return nil, err
		}
		if c.stream {
			return c.adapter.SendNativeStream(req)
		}
		return c.adapter.SendRequest(req)
	}
	var resp *http.Response
	var err error
	if h.retryExecutor != nil {
		resp, err = h.retryExecutor.Execute(egressCtx, c.adapter.Name(), send)
	} else {
		resp, err = send(egressCtx, 0)
	}

	if resp == nil {
		// No answer at all. Same refusal and record as the chat path.
		slog.Error("provider request failed", "request_id", c.reqID, "error", err, "provider", c.providerKey)
		if h.healthTracker != nil {
			h.healthTracker.RecordFailure(c.adapter.Name())
		}
		httputil.WriteServiceUnavailableError(c.w, c.reqID, "Provider request failed")
		c.attestFailure(http.StatusServiceUnavailable, egress.Written(),
			providerFailureReason(c.r, err, 0, audit.ReasonProviderUnreachable))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		c.relayProviderError(resp)
		return
	}
	if h.healthTracker != nil {
		h.healthTracker.RecordSuccess(c.adapter.Name())
	}
	if c.stream {
		c.relayStream(ctx, resp)
		return
	}
	c.relayBuffered(resp)
}

// relayProviderError passes a provider's error response to the caller as it
// came: the status (so the SDK's own retry on 429 and 529 works) and the
// Anthropic-shaped body. It is the caller's own request echoed back to them,
// so relaying it discloses nothing; it is excerpted, not quoted, in the log.
func (c *nativeCall) relayProviderError(resp *http.Response) {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProviderErrorBody))
	slog.Warn("provider returned an error status",
		"request_id", c.reqID,
		"status", resp.StatusCode,
		"provider", c.providerKey,
		"body_excerpt", redact.Excerpt(body),
	)
	if resp.StatusCode >= 500 && c.h.healthTracker != nil {
		c.h.healthTracker.RecordFailure(c.adapter.Name())
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	c.w.Header().Set("Content-Type", ct)
	c.w.Header().Set("X-Request-ID", c.reqID)
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		c.w.Header().Set("Retry-After", ra)
	}
	c.w.WriteHeader(resp.StatusCode)
	_, _ = c.w.Write(body)
	// The provider answered, which proves the body was sent.
	c.attestFailure(resp.StatusCode, true, audit.ReasonProviderError)
}

func (c *nativeCall) attestFailure(status int, bodySent bool, reason string) {
	if c.h.auditLogger == nil {
		return
	}
	c.h.auditLogger.LogProviderFailure(
		withDuration(
			completedRequest(c.reqID, c.authInfo, c.r, c.originalModel, c.providerKey, status, c.stream, c.unscanned, bodySent),
			time.Since(c.receivedAt)),
		reason)
}

func (c *nativeCall) relayBuffered(resp *http.Response) {
	h := c.h
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Error("failed to read provider response", "request_id", c.reqID, "error", err, "provider", c.providerKey)
		httputil.WriteInternalError(c.w, c.reqID, "Failed to process provider response")
		c.attestFailure(http.StatusInternalServerError, true,
			providerFailureReason(c.r, err, resp.StatusCode, audit.ReasonProviderError))
		return
	}

	// count_tokens answers {"input_tokens": n}: nothing to price, no model.
	var usage adapters.NativeUsage
	if c.endpoint == adapters.NativeMessages {
		usage, err = adapters.ParseNativeResponse(body)
		if err != nil {
			slog.Error("failed to read usage from provider response",
				"request_id", c.reqID, "error", err, "provider", c.providerKey)
			httputil.WriteInternalError(c.w, c.reqID, "Failed to process provider response")
			c.attestFailure(http.StatusInternalServerError, true, audit.ReasonProviderError)
			return
		}
	}
	costUSD := c.price(usage)

	c.w.Header().Set("Content-Type", "application/json")
	c.w.Header().Set("X-Request-ID", c.reqID)
	c.w.WriteHeader(http.StatusOK)
	_, deliveryErr := c.w.Write(body)
	if deliveryErr == nil {
		deliveryErr = flushToClient(c.w)
	}
	if deliveryErr != nil {
		slog.Warn("failed to deliver the response to the caller",
			"request_id", c.reqID, "error", deliveryErr, "provider", c.providerKey)
	}
	duration := time.Since(c.receivedAt)
	c.record(usage, costUSD, http.StatusOK, duration)

	if h.auditLogger != nil {
		rec := withOutcome(
			completedRequest(c.reqID, c.authInfo, c.r, c.originalModel, c.providerKey, http.StatusOK, false, c.unscanned, true),
			usage.Model, usage.Reported,
			usage.Usage.PromptTokens, usage.Usage.CompletionTokens, usage.Usage.TotalTokens, duration)
		if deliveryErr != nil {
			h.auditLogger.LogProviderFailure(rec, audit.ReasonResponseNotDelivered)
		} else {
			h.auditLogger.LogRequestComplete(rec)
		}
	}
}

// relayStream relays the provider's event stream byte for byte, line by line,
// flushing at each event boundary, and reads usage from it on the way past.
// Event lines are kept: the Anthropic SDKs dispatch on them.
//
// The outcome logic is the chat relay's (streamWithMonitoring): only the
// provider's terminator, message_stop, establishes completion, and the same
// timeouts and disconnect handling decide every other ending.
func (c *nativeCall) relayStream(ctx context.Context, resp *http.Response) {
	cfg := c.streamConfig()
	var usage adapters.NativeStreamUsage
	chunks := 0
	outcome := c.streamLoop(ctx, resp, cfg, &usage, &chunks)

	result, err := usage.Result()
	if err != nil {
		slog.Error("invalid usage in provider stream", "request_id", c.reqID, "error", err, "provider", c.providerKey)
		result = adapters.NativeUsage{Model: usage.Model}
	}
	costUSD := c.price(result)
	duration := time.Since(c.receivedAt)
	status := clientStatusFor(outcome)
	c.record(result, costUSD, status, duration)

	slog.Info("streaming completed",
		"request_id", c.reqID, "endpoint", c.endpoint, "model_requested", c.originalModel,
		"model_served", result.Model, "provider", c.providerKey, "chunks", chunks,
		"prompt_tokens", result.Usage.PromptTokens, "completion_tokens", result.Usage.CompletionTokens,
		"estimated_cost_usd", costUSD, "outcome", string(outcome), "duration_ms", duration.Milliseconds(),
		"org_id", c.authInfo.OrganizationID,
	)
	if c.h.auditLogger != nil {
		rec := withOutcome(
			completedRequest(c.reqID, c.authInfo, c.r, c.originalModel, c.providerKey, status, true, c.unscanned, true),
			result.Model, result.Reported,
			result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens, duration)
		attestStreamOutcome(c.h.auditLogger, rec, outcome, c.reqID, chunks)
	}
}

func (c *nativeCall) streamLoop(ctx context.Context, resp *http.Response, cfg StreamingConfig,
	usage *adapters.NativeStreamUsage, chunks *int) StreamOutcome {
	c.w.Header().Set("Content-Type", "text/event-stream")
	c.w.Header().Set("Cache-Control", "no-cache")
	c.w.Header().Set("Connection", "keep-alive")
	c.w.Header().Set("X-Request-ID", c.reqID)
	c.w.WriteHeader(http.StatusOK)
	if err := flushToClient(c.w); err != nil {
		slog.Error("failed to flush the streaming response header", "request_id", c.reqID, "error", err)
		return StreamHeaderUndelivered
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, cfg.BufferSize), cfg.MaxBufferSize)
	lines := make(chan []byte)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- line:
			case <-ctx.Done():
				return
			}
		}
	}()

	timer := time.NewTimer(cfg.PerChunkTimeout)
	defer timer.Stop()
	terminator := false
	writeErrorEvent := func(msg string) {
		_, _ = fmt.Fprintf(c.w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":%q}}\n\n", msg)
		_ = flushToClient(c.w)
	}

	for {
		timer.Reset(cfg.PerChunkTimeout)
		select {
		case <-ctx.Done():
			if outcomeForContext(ctx.Err()) == StreamTotalTimeout {
				writeErrorEvent("AEGIS stream timeout")
				return StreamTotalTimeout
			}
			return StreamClientDisconnected

		case <-timer.C:
			writeErrorEvent("AEGIS stream chunk timeout")
			return StreamChunkTimeout

		case <-done:
			switch {
			case scanner.Err() != nil && ctx.Err() != nil && errors.Is(scanner.Err(), ctx.Err()):
				outcome := outcomeForContext(ctx.Err())
				if outcome == StreamTotalTimeout {
					writeErrorEvent("AEGIS stream timeout")
				}
				return outcome
			case scanner.Err() != nil:
				slog.Error("error reading stream", "request_id", c.reqID, "error", scanner.Err(), "provider", c.providerKey)
				return StreamReadError
			case !terminator:
				slog.Warn("provider closed the stream without message_stop",
					"request_id", c.reqID, "provider", c.providerKey, "chunks_sent", *chunks)
				return StreamTruncated
			default:
				return StreamCompleted
			}

		case line := <-lines:
			if _, err := c.w.Write(append(line, '\n')); err != nil {
				return writeFailureOutcome(ctx, err)
			}
			if data, ok := strings.CutPrefix(string(line), "data:"); ok {
				*chunks++
				data = strings.TrimSpace(data)
				usage.Observe([]byte(data))
				if strings.Contains(data, `"message_stop"`) && isEventType(data, "message_stop") {
					terminator = true
				}
			}
			if len(line) == 0 {
				// Event boundary. Flushing here, not per line, keeps an event
				// whole on the wire.
				if err := flushToClient(c.w); err != nil {
					return writeFailureOutcome(ctx, err)
				}
				if terminator {
					if errors.Is(ctx.Err(), context.DeadlineExceeded) {
						return StreamTotalTimeout
					}
					return StreamCompleted
				}
			}
		}
	}
}

// streamConfig is the chat streaming configuration with the total and idle
// timeouts taken from anthropic_messages.
func (c *nativeCall) streamConfig() StreamingConfig {
	cfg := c.h.streamingHandler.config
	if c.h.cfg != nil {
		if gc := c.h.cfg(); gc != nil {
			if t := gc.AnthropicMessages.StreamTotalTimeout; t > 0 {
				cfg.TotalTimeout = t
			}
			if t := gc.AnthropicMessages.StreamIdleTimeout; t > 0 {
				cfg.PerChunkTimeout = t
			}
		}
	}
	return cfg
}

// writeFailureOutcome classifies a failed write to the caller the way the
// chat relay does: the gateway's own deadline first, then a caller who left.
func writeFailureOutcome(ctx context.Context, err error) StreamOutcome {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return StreamTotalTimeout
	case errors.Is(err, context.Canceled) || clientGone(err) || errors.Is(ctx.Err(), context.Canceled):
		return StreamClientDisconnected
	default:
		return StreamChunkError
	}
}

func isEventType(data, typ string) bool {
	var ev struct {
		Type string `json:"type"`
	}
	return json.Unmarshal([]byte(data), &ev) == nil && ev.Type == typ
}

// price returns the cost of a messages response, or 0 for count_tokens and for
// a response with no usage.
func (c *nativeCall) price(u adapters.NativeUsage) float64 {
	if c.h.costCalc == nil || c.endpoint != adapters.NativeMessages || !u.Reported {
		return 0
	}
	model := u.Model
	if model == "" {
		model = c.providerModel
	}
	costUSD, found := c.h.costCalc.Calculate(cost.RequestDetails{
		Provider:           c.providerKey,
		Model:              model,
		PromptTokens:       u.Usage.PromptTokens,
		CachedTokens:       u.Usage.CachedPromptTokens(),
		CacheWrite5mTokens: u.Usage.CacheWrite5mTokens(),
		CacheWrite1hTokens: u.Usage.CacheWrite1hTokens(),
		CompletionTokens:   u.Usage.CompletionTokens,
	})
	if !found {
		slog.Warn("pricing_unknown: no pricing data for served model",
			"event_type", "pricing_unknown", "provider", c.providerKey, "model", model, "request_id", c.reqID)
	}
	return costUSD
}

// record writes the usage row and the request metric. count_tokens does no
// billable work and writes neither; it is attested like any other request.
func (c *nativeCall) record(u adapters.NativeUsage, costUSD float64, status int, duration time.Duration) {
	if c.endpoint != adapters.NativeMessages {
		return
	}
	served := u.Model
	if served == "" {
		served = c.providerModel
	}
	if c.h.metrics != nil {
		c.h.metrics.RecordRequest(telemetry.RequestLabels{
			Org: c.authInfo.OrganizationID, Team: c.authInfo.TeamID,
			Model: c.originalModel, Provider: c.providerKey,
			Status:           fmt.Sprint(status),
			Classification:   string(c.authInfo.MaxClassification),
			DurationMs:       float64(duration.Milliseconds()),
			OverheadMs:       float64(duration.Milliseconds()),
			PromptTokens:     u.Usage.PromptTokens,
			CompletionTokens: u.Usage.CompletionTokens,
			CostUSD:          costUSD,
		})
	}
	if c.h.usageRecorder != nil {
		c.h.usageRecorder.RecordUsage(storage.UsageRecord{
			RequestID:          c.reqID,
			OrganizationID:     c.authInfo.OrganizationID,
			TeamID:             c.authInfo.TeamID,
			UserID:             c.authInfo.UserID,
			APIKeyID:           c.authInfo.KeyID,
			ModelRequested:     c.originalModel,
			ModelServed:        served,
			Provider:           c.providerKey,
			Classification:     string(c.authInfo.MaxClassification),
			PromptTokens:       u.Usage.PromptTokens,
			CompletionTokens:   u.Usage.CompletionTokens,
			TotalTokens:        u.Usage.TotalTokens,
			CachedTokens:       u.Usage.CachedPromptTokens(),
			CacheWrite5mTokens: u.Usage.CacheWrite5mTokens(),
			CacheWrite1hTokens: u.Usage.CacheWrite1hTokens(),
			EstimatedCostUSD:   costUSD,
			DurationMs:         duration.Milliseconds(),
			StatusCode:         status,
			Project:            c.req.Project,
			Stream:             c.stream,
		})
	}
}
