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

package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/telemetry"
)

// EventType represents the type of audit event.
type EventType string

const (
	EventAuthFailure        EventType = "auth_failure"
	EventAuthSuccess        EventType = "auth_success"
	EventRateLimitViolation EventType = "rate_limit_violation"
	EventBudgetViolation    EventType = "budget_violation"
	EventFilterBlock        EventType = "filter_block"
	EventPricingDenied      EventType = "pricing_denied"
	EventRedisFailure       EventType = "redis_failure"
	EventProviderFailure    EventType = "provider_failure"
	EventRequestComplete    EventType = "request_complete"
)

// Event represents a security-relevant audit event.
type Event struct {
	RequestID      string
	Timestamp      time.Time
	EventType      EventType
	OrganizationID string
	TeamID         string
	UserID         *string
	APIKeyID       *string
	IPAddress      string
	UserAgent      string
	Endpoint       string
	Method         string
	StatusCode     int
	ErrorMessage   string

	// Detail columns, promoted out of the metadata JSONB by migration 013.
	// Pointers because absent and zero are different facts: an event that
	// carries no limit is not an event whose limit is zero.
	APIKeyPrefix   *string
	LimitDimension *string
	LimitValue     *int64
	SpentCents     *int64
	LimitCents     *int64
	FilterType     *string
	Reason         *string
	Provider       *string
	Model          *string
	Mode           *string
	Operation      *string
	ErrorDetail    *string
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func i64Ptr(i int64) *int64 { return &i }

// Logger writes audit events to the database.
type Logger struct {
	db *pgxpool.Pool

	// metrics is optional. When present, every dropped write increments
	// aegis_audit_write_failure_total, which is the only signal that the
	// decision record is incomplete: a failed insert leaves no row and no gap
	// in the id sequence, so the sealer cannot detect the loss.
	metrics *telemetry.Metrics
}

// WithMetrics returns a logger that reports dropped writes to the given
// registry. Separate from NewLogger so existing callers, including tests that
// have no registry, are unaffected.
func (l *Logger) WithMetrics(m *telemetry.Metrics) *Logger {
	l.metrics = m
	return l
}

// NewLogger creates a new audit logger.
func NewLogger(db *pgxpool.Pool) *Logger {
	return &Logger{db: db}
}

// Log records an audit event asynchronously.
func (l *Logger) Log(event Event) {
	go l.writeEvent(event)
}

// writeEvent writes the audit event to the database.
func (l *Logger) writeEvent(event Event) {
	if l.db == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Clip every value that lands in a bounded column before the insert. See
	// limits.go: PostgreSQL errors on varchar overflow rather than truncating, so
	// an unclipped value costs the whole audit row, and two of these columns are
	// written on the unauthenticated auth-failure path.
	event.IPAddress = clip(event.IPAddress, MaxIPAddress)
	event.ErrorMessage = clip(event.ErrorMessage, MaxErrorMessage)
	event.UserAgent = clip(event.UserAgent, MaxUserAgent)
	event.APIKeyPrefix = clipPtr(event.APIKeyPrefix, MaxAPIKeyPrefix)
	event.LimitDimension = clipPtr(event.LimitDimension, MaxLimitDimension)
	event.FilterType = clipPtr(event.FilterType, MaxFilterType)
	event.Reason = clipPtr(event.Reason, MaxReason)
	event.Provider = clipPtr(event.Provider, MaxProvider)
	event.Model = clipPtr(event.Model, MaxModel)
	event.Mode = clipPtr(event.Mode, MaxMode)
	event.Operation = clipPtr(event.Operation, MaxOperation)
	event.ErrorDetail = clipPtr(event.ErrorDetail, MaxErrorDetail)

	query := `
		INSERT INTO audit_events (
			request_id, timestamp, event_type, organization_id, team_id, user_id,
			api_key_id, ip_address, user_agent, endpoint, method, status_code,
			error_message,
			api_key_prefix, limit_dimension, limit_value, spent_cents, limit_cents,
			filter_type, reason, provider, model, mode, operation, error_detail
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
			$14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25
		)
	`

	_, err := l.db.Exec(ctx, query,
		event.RequestID,
		event.Timestamp,
		event.EventType,
		event.OrganizationID,
		event.TeamID,
		event.UserID,
		event.APIKeyID,
		event.IPAddress,
		event.UserAgent,
		event.Endpoint,
		event.Method,
		event.StatusCode,
		event.ErrorMessage,
		event.APIKeyPrefix,
		event.LimitDimension,
		event.LimitValue,
		event.SpentCents,
		event.LimitCents,
		event.FilterType,
		event.Reason,
		event.Provider,
		event.Model,
		event.Mode,
		event.Operation,
		event.ErrorDetail,
	)

	if err != nil {
		// The write is lost and unrecoverable: nothing retries it, and no gap
		// appears anywhere for the sealer or a reader to notice. The counter is
		// the only durable trace that the record is incomplete.
		l.metrics.RecordAuditWriteFailure(string(event.EventType))
		slog.Error("failed to write audit event",
			"error", err,
			"request_id", event.RequestID,
			"event_type", event.EventType,
		)
	}
}

// UnattributedOrg is the organization recorded for events that happen before a
// caller is identified, which in practice means authentication failures.
//
// It is a sentinel, not a tenant. Nothing may treat it as one: an audit query
// scoped to it would return every tenant's authentication failures, including
// the truncated key prefix, IP and user agent. [Reader] excludes it for exactly
// that reason, and refuses a query scoped to it.
const UnattributedOrg = "unknown"

// LogAuthFailure logs an authentication failure.
func (l *Logger) LogAuthFailure(requestID, ip, userAgent, apiKey, reason string) {
	l.Log(Event{
		RequestID:      requestID,
		Timestamp:      time.Now(),
		EventType:      EventAuthFailure,
		OrganizationID: UnattributedOrg,
		TeamID:         UnattributedOrg,
		IPAddress:      ip,
		UserAgent:      userAgent,
		Endpoint:       "/v1/*",
		Method:         "POST",
		StatusCode:     401,
		ErrorMessage:   reason,
		APIKeyPrefix:   strPtr(truncateAPIKey(apiKey)),
	})
}

// LogRateLimitViolation logs a rate limit violation.
func (l *Logger) LogRateLimitViolation(requestID, orgID, teamID, keyID, dimension string, limit int64, ip string) {
	l.Log(Event{
		RequestID:      requestID,
		Timestamp:      time.Now(),
		EventType:      EventRateLimitViolation,
		OrganizationID: orgID,
		TeamID:         teamID,
		APIKeyID:       &keyID,
		IPAddress:      ip,
		StatusCode:     429,
		ErrorMessage:   fmt.Sprintf("Rate limit exceeded: %s", dimension),
		LimitDimension: strPtr(dimension),
		LimitValue:     i64Ptr(limit),
	})
}

// LogBudgetViolation logs a budget limit violation.
func (l *Logger) LogBudgetViolation(requestID, orgID, teamID, keyID string, spentCents, limitCents int64, ip string) {
	l.Log(Event{
		RequestID:      requestID,
		Timestamp:      time.Now(),
		EventType:      EventBudgetViolation,
		OrganizationID: orgID,
		TeamID:         teamID,
		APIKeyID:       &keyID,
		IPAddress:      ip,
		StatusCode:     402,
		ErrorMessage:   "Daily budget exceeded",
		SpentCents:     i64Ptr(spentCents),
		LimitCents:     i64Ptr(limitCents),
	})
}

// LogFilterBlock logs a content filter block.
func (l *Logger) LogFilterBlock(requestID, orgID, teamID, keyID, filterType, reason string, ip string) {
	l.Log(Event{
		RequestID:      requestID,
		Timestamp:      time.Now(),
		EventType:      EventFilterBlock,
		OrganizationID: orgID,
		TeamID:         teamID,
		APIKeyID:       &keyID,
		IPAddress:      ip,
		StatusCode:     451,
		ErrorMessage:   fmt.Sprintf("Content blocked by %s filter", filterType),
		FilterType:     strPtr(filterType),
		Reason:         strPtr(reason),
	})
}

// LogModelDenied records a request refused because the presenting API key's
// model allowlist does not include the requested alias.
//
// This reuses EventAuthFailure rather than declaring a type of its own. The
// distinction it loses is real: authentication succeeded here and authorisation
// failed, whereas every other EventAuthFailure row is a credential that did not
// verify. Reason carries "model_not_allowed" so the two can be told apart in an
// export, and Model carries the alias that was refused. A dedicated event type
// would be the better long-term shape for CC6.1 evidence and is left as a
// follow-up rather than introduced here.
//
// Model is a configured alias from models.yaml, never caller-supplied free
// text: an unknown model is rejected by validation before this point.
func (l *Logger) LogModelDenied(requestID, orgID, teamID, keyID, model string, ip string) {
	l.Log(Event{
		RequestID:      requestID,
		Timestamp:      time.Now(),
		EventType:      EventAuthFailure,
		OrganizationID: orgID,
		TeamID:         teamID,
		APIKeyID:       &keyID,
		IPAddress:      ip,
		StatusCode:     403,
		ErrorMessage:   "Model not permitted for this API key",
		Model:          strPtr(model),
		Reason:         strPtr("model_not_allowed"),
	})
}

// Provider failure reasons. Enumerated because audit_events.reason is sealed
// and free text there is permanent: a provider error body is unbounded and
// echoes caller content, so it must never reach this column.
const (
	// ReasonProviderUnreachable covers a transport failure: the request never
	// produced a response, including after retries.
	ReasonProviderUnreachable = "provider_unreachable"
	// ReasonProviderError covers a response the provider returned and the
	// gateway could not use, which includes any non-200 status.
	ReasonProviderError = "provider_error"
	// ReasonStreamInterrupted covers a stream that began and did not finish
	// because the provider stopped, a deadline elapsed, or a chunk could not be
	// processed.
	ReasonStreamInterrupted = "stream_interrupted"
	// ReasonStreamTruncated covers a stream the provider closed cleanly without
	// sending its end-of-stream marker. The caller received a partial response
	// that looked complete, which is why it is not attested as one.
	ReasonStreamTruncated = "stream_truncated"
	// ReasonClientDisconnected covers a stream the caller abandoned. It is not
	// a provider fault, and is recorded separately so that an operator counting
	// provider failures is not misled by callers hanging up.
	ReasonClientDisconnected = "client_disconnected"
)

// CompletedRequest is what an allow event records.
//
// A struct rather than a long parameter list because the two call sites, the
// non-streaming handler and the streaming handler, are in different files and
// must not drift: a positional argument swapped between them would compile.
type CompletedRequest struct {
	RequestID      string
	OrganizationID string
	TeamID         string
	UserID         string
	KeyID          string
	KeyPrefix      string
	Endpoint       string
	// RequestedModel is the alias the caller asked for, from models.yaml.
	RequestedModel string
	// ProviderKey is the configured provider name that served the request, not
	// the adapter type. The two differ: azure_openai and internal_vllm are both
	// served by the OpenAI adapter and both report "openai".
	ProviderKey string
	StatusCode  int
	IPAddress   string
	Stream      bool
}

// LogRequestComplete records that a request passed every gate and completed.
//
// Until this existed, audit_events held only refusals. A permitted request
// produced a usage_records row, a log line and Prometheus counters, none of
// which the sealer covers, so the chain attested what was refused and nothing
// about what was allowed. A decision record that only records denials cannot
// answer "what did this key actually do", which is the question an assessor
// asks first.
//
// WHAT THIS EVENT CANNOT CARRY, and why. audit_events has twenty-six columns
// and all twenty-six are in the leaf hash at hash_schema_version=2, a 1:1
// correspondence verified against internal/audit/checkpoint/event.go. There is
// no column for latency, for prompt or completion tokens, for the resolved
// concrete model, or for classification. Adding one and putting it in the hash
// changes every leaf hash and requires hash_schema_version=3, which is deferred
// under ADR 0011 and tracked on issue #38. Adding one and leaving it OUT of the
// hash would be worse: it would be the first audit_events column not covered by
// the chain, so the fields carrying the evidence would be the fields nothing
// attests, while the record looked more complete than it is.
//
// So this event carries identity, outcome, and routing, all attested. The token
// counts and latency for the same request_id are in usage_records, which is not
// sealed. See docs/evidence/known-limitations.md section 2.14.
func (l *Logger) LogRequestComplete(req CompletedRequest) {
	l.Log(Event{
		RequestID:      req.RequestID,
		Timestamp:      time.Now(),
		EventType:      EventRequestComplete,
		OrganizationID: req.OrganizationID,
		TeamID:         req.TeamID,
		UserID:         strPtr(req.UserID),
		APIKeyID:       strPtr(req.KeyID),
		APIKeyPrefix:   strPtr(req.KeyPrefix),
		IPAddress:      req.IPAddress,
		Endpoint:       req.Endpoint,
		Method:         "POST",
		StatusCode:     req.StatusCode,
		Provider:       strPtr(req.ProviderKey),
		Model:          strPtr(req.RequestedModel),
		// Mode distinguishes a streamed completion from a buffered one. The
		// two take different code paths with different usage accounting, and
		// an assessor reading the trail cannot otherwise tell them apart.
		Mode: strPtr(streamMode(req.Stream)),
	})
}

// LogProviderFailure records a request that passed every gate and then failed at
// the provider.
//
// Without it an allowed request that fails produces no attested event at all,
// which is the same completeness hole as an unattested success in a different
// place: the trail would show requests that were permitted and completed, and
// requests that were refused, with the failures missing entirely.
//
// reason is a fixed classification string, never provider text. Provider error
// bodies are unbounded and echo caller content, and audit_events.reason is
// sealed, so anything put here is permanent. internal/redact exists for the log
// path; this column takes an enumerated value only.
func (l *Logger) LogProviderFailure(req CompletedRequest, reason string) {
	l.Log(Event{
		RequestID:      req.RequestID,
		Timestamp:      time.Now(),
		EventType:      EventProviderFailure,
		OrganizationID: req.OrganizationID,
		TeamID:         req.TeamID,
		UserID:         strPtr(req.UserID),
		APIKeyID:       strPtr(req.KeyID),
		APIKeyPrefix:   strPtr(req.KeyPrefix),
		IPAddress:      req.IPAddress,
		Endpoint:       req.Endpoint,
		Method:         "POST",
		StatusCode:     req.StatusCode,
		ErrorMessage:   "Provider request failed",
		Provider:       strPtr(req.ProviderKey),
		Model:          strPtr(req.RequestedModel),
		Mode:           strPtr(streamMode(req.Stream)),
		Reason:         strPtr(reason),
	})
}

// streamMode renders the transport as an enumerated value for the mode column.
func streamMode(stream bool) string {
	if stream {
		return "stream"
	}
	return "buffered"
}

// LogRedisFailure logs a Redis connectivity failure.
// LogPricingDenied records a request refused (or flagged) because the routed
// provider/model has no usable pricing entry. Without this, a pricing denial
// existed only as a process log line and was absent from audit exports — unlike
// every other governance denial.
func (l *Logger) LogPricingDenied(requestID, orgID, teamID, keyID, provider, model, mode string, ip string) {
	// Only deny mode refuses the request. Recording 402 for a flagged request
	// that actually reached the provider would misrepresent the outcome in
	// audit exports — the trail has to say what happened, not what the feature
	// is named after.
	status := 0
	if mode == "deny" {
		status = 402
	}
	l.Log(Event{
		RequestID:      requestID,
		Timestamp:      time.Now(),
		EventType:      EventPricingDenied,
		OrganizationID: orgID,
		TeamID:         teamID,
		APIKeyID:       &keyID,
		IPAddress:      ip,
		StatusCode:     status,
		ErrorMessage:   fmt.Sprintf("no pricing entry for %s/%s", provider, model),
		Provider:       strPtr(provider),
		Model:          strPtr(model),
		Mode:           strPtr(mode),
	})
}

func (l *Logger) LogRedisFailure(requestID, orgID, teamID, keyID, operation string, err error, ip string) {
	l.Log(Event{
		RequestID:      requestID,
		Timestamp:      time.Now(),
		EventType:      EventRedisFailure,
		OrganizationID: orgID,
		TeamID:         teamID,
		APIKeyID:       &keyID,
		IPAddress:      ip,
		StatusCode:     503,
		ErrorMessage:   "Redis unavailable - failed closed",
		Operation:      strPtr(operation),
		ErrorDetail:    strPtr(err.Error()),
	})
}

// truncateAPIKey returns the first 8 characters of an API key for logging.
func truncateAPIKey(apiKey string) string {
	// Count runes, not bytes. A key is normally ASCII, but this runs on the
	// unauthenticated auth-failure path where the "key" is whatever the caller
	// sent, and slicing a multibyte character in half produces invalid UTF-8 that
	// PostgreSQL refuses, costing the audit row that records the failure.
	if utf8.RuneCountInString(apiKey) > 8 {
		return string([]rune(apiKey)[:8]) + "..."
	}
	return apiKey
}
