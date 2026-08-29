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

// WriteMetrics is the optional recorder for audit writes that did not land.
//
// An interface rather than a dependency on internal/telemetry, matching
// policy.Evaluator.SetMetrics: internal/audit is imported by the packages that
// telemetry is also imported by, and a concrete dependency here would make the
// audit writer harder to exercise in a test than it needs to be.
type WriteMetrics interface {
	RecordAuditWriteFailure(eventType, reason string)
}

// Reasons an audit write did not land. A fixed set, because they are metric
// label values.
const (
	// WriteFailureNoDatabase is a gateway running without Postgres. Every
	// event is discarded, including this one.
	WriteFailureNoDatabase = "no_database"
	// WriteFailureInsert is an INSERT that returned an error.
	WriteFailureInsert = "insert_error"
)

// Logger writes audit events to the database.
type Logger struct {
	db      *pgxpool.Pool
	metrics WriteMetrics
}

// NewLogger creates a new audit logger.
func NewLogger(db *pgxpool.Pool) *Logger {
	return &Logger{db: db}
}

// SetMetrics attaches a recorder for audit writes that do not land.
//
// Without it a dropped write is invisible: no row is inserted, and because
// BIGSERIAL allocates only on a successful insert there is no id gap either, so
// the sealer seals a contiguous run and reports a healthy chain. The chain is
// intact and the record is incomplete, and nothing else in the system can tell.
func (l *Logger) SetMetrics(m WriteMetrics) {
	l.metrics = m
}

func (l *Logger) recordFailure(eventType EventType, reason string) {
	if l.metrics != nil {
		l.metrics.RecordAuditWriteFailure(string(eventType), reason)
	}
}

// Log records an audit event asynchronously.
func (l *Logger) Log(event Event) {
	go l.writeEvent(event)
}

// writeEvent writes the audit event to the database.
func (l *Logger) writeEvent(event Event) {
	if l.db == nil {
		l.recordFailure(event.EventType, WriteFailureNoDatabase)
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
		l.recordFailure(event.EventType, WriteFailureInsert)
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

// ModelNotAllowedReason is the fixed reason string recorded when a key is
// refused a model alias its allowlist does not carry.
//
// A constant, and free of caller-supplied text. The alias itself is recorded in
// the model column, where it is an operator-configured identifier drawn from
// configs/models.yaml rather than an arbitrary string: DecodeChatCompletion
// accepts any model value, but a request whose alias is not a configured key
// never reaches this call site because it is refused as an unknown model first.
const ModelNotAllowedReason = "model alias not in the key's allowed_models"

// LogModelDenied records a request refused because the API key's allowed_models
// does not carry the requested alias.
//
// It reuses EventAuthFailure rather than introducing an event type: this is an
// authorization denial against an identified key, which is the category that
// constant names, and the audit read API and the sealed chain both key off the
// existing set.
//
// It does not reuse LogAuthFailure, which is the pre-identification path and
// deliberately records UnattributedOrg. This caller is authenticated and its
// organization is known, so recording the sentinel here would file a tenant's
// own authorization denials under the operator sentinel that Reader refuses to
// scope to, and the organization would never see them in its own audit export.
func (l *Logger) LogModelDenied(requestID, orgID, teamID, keyID, model string, statusCode int, ip string) {
	l.Log(Event{
		RequestID:      requestID,
		Timestamp:      time.Now(),
		EventType:      EventAuthFailure,
		OrganizationID: orgID,
		TeamID:         teamID,
		APIKeyID:       &keyID,
		IPAddress:      ip,
		Endpoint:       "/v1/chat/completions",
		Method:         "POST",
		StatusCode:     statusCode,
		ErrorMessage:   "Model not permitted for this API key",
		Reason:         strPtr(ModelNotAllowedReason),
		Model:          strPtr(model),
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

// CompletionEvent is the outcome of a request that passed every gate.
//
// It is a struct rather than a positional argument list because it carries ten
// fields and two of them are strings that look alike: Provider is the
// configured provider key and Model is the concrete model that provider served.
// Transposing those in a call is not a compile error and would misattribute
// every allowed request.
//
// Every field is an identifier, an enumerated value, or a status code. None is
// caller-supplied free text, and none is derived from message content. See
// LogRequestComplete for why that is a constraint rather than a coincidence.
type CompletionEvent struct {
	RequestID string
	OrgID     string
	TeamID    string
	UserID    string
	KeyID     string

	// Provider is the configured provider key from the resolved route, not
	// adapter.Name(). Model is the concrete model that key served, not the
	// alias the caller asked for. The pair matches what
	// LogPricingDenied writes and what configs/pricing.yaml is keyed by.
	Provider string
	Model    string

	// Streaming selects the value recorded in the operation column.
	Streaming bool

	StatusCode int
	IP         string
}

// Operations recorded in the operation column for a completion event.
const (
	OperationChatCompletion       = "chat_completion"
	OperationChatCompletionStream = "chat_completion_stream"
)

// Stages at which a request that passed every gate then failed. A fixed set:
// these are written to audit_events.reason, which is covered by the leaf hash
// and sealed, so the value has to be one this code chose rather than one a
// provider or a caller supplied.
const (
	// FailureProviderUnreachable is a send that never produced a response,
	// after any retries. Connection refused, DNS, timeout.
	FailureProviderUnreachable = "provider_unreachable"
	// FailureProviderHTTPError is a response with a non-success status.
	FailureProviderHTTPError = "provider_http_error"
	// FailureProviderResponseInvalid is a response the adapter could not
	// translate.
	FailureProviderResponseInvalid = "provider_response_invalid"
	// FailureStreamTimeout is a stream that stalled, either per chunk or in
	// total.
	FailureStreamTimeout = "stream_timeout"
	// FailureStreamRead is a stream that ended on a read or relay error.
	FailureStreamRead = "stream_read_error"
	// FailureStreamNotSupported is a response writer that cannot flush, so no
	// stream could be served.
	FailureStreamNotSupported = "stream_not_supported"
)

func (e CompletionEvent) operation() string {
	if e.Streaming {
		return OperationChatCompletionStream
	}
	return OperationChatCompletion
}

func (e CompletionEvent) base(eventType EventType) Event {
	var userID *string
	if e.UserID != "" {
		userID = &e.UserID
	}
	var keyID *string
	if e.KeyID != "" {
		keyID = &e.KeyID
	}
	return Event{
		RequestID:      e.RequestID,
		Timestamp:      time.Now(),
		EventType:      eventType,
		OrganizationID: e.OrgID,
		TeamID:         e.TeamID,
		UserID:         userID,
		APIKeyID:       keyID,
		IPAddress:      e.IP,
		Endpoint:       "/v1/chat/completions",
		Method:         "POST",
		StatusCode:     e.StatusCode,
		Provider:       strPtr(e.Provider),
		Model:          strPtr(e.Model),
		Operation:      strPtr(e.operation()),
	}
}

// LogRequestComplete records a request that was permitted and served.
//
// Until this existed, audit_events held only refusals, and audit_events is the
// only table the sealer covers. The chain therefore attested what the gateway
// turned away and said nothing about what it allowed through, which is the
// larger half of any question an auditor asks.
//
// What this event does NOT carry, and why it is not an oversight: the alias the
// caller requested, the classification tier, the latency, and the token counts.
// audit_events has no column for any of them, and adding one changes the field
// set the leaf hash covers, which requires hash_schema_version=3. ADR 0011
// records that decision and its cost. Until that bump is cut, those five facts
// live in usage_records, which is joinable on request_id and is not attested.
// docs/evidence/known-limitations.md section 2.13 states the gap.
func (l *Logger) LogRequestComplete(ev CompletionEvent) {
	l.Log(ev.base(EventRequestComplete))
}

// LogProviderFailure records a request that passed every gate and then failed
// at the provider.
//
// Without it, an allowed request that fails leaves no attested trace at all:
// LogRequestComplete never runs, and no denial event applies because nothing
// denied it. That is the same completeness hole this change exists to close,
// one step further along the request path.
//
// stage must be one of the Failure* constants. It is written to
// audit_events.reason, which the leaf hash covers and the sealer seals, so it
// is deliberately an enumerated value chosen here. A provider's error text and
// a Go error string are both excluded: the first is attacker-influenced content
// the gateway does not control, and putting either in a sealed, exported column
// is the mechanism section 2.12 warns operators against, arriving by a
// different door.
func (l *Logger) LogProviderFailure(ev CompletionEvent, stage string) {
	if !validFailureStage(stage) {
		// A caller passing free text here would put it in the sealed record.
		// Refusing the value rather than the event keeps the completeness
		// property: the event is still written, with the stage recorded as
		// unknown, and the bug is visible in the trail rather than silently
		// widening what the column can hold.
		slog.Error("audit: provider failure stage is not a known constant; recording it as unknown",
			"request_id", ev.RequestID)
		stage = "unknown"
	}
	e := ev.base(EventProviderFailure)
	e.ErrorMessage = "Provider call failed after the request was permitted"
	e.Reason = strPtr(stage)
	l.Log(e)
}

func validFailureStage(stage string) bool {
	switch stage {
	case FailureProviderUnreachable, FailureProviderHTTPError,
		FailureProviderResponseInvalid, FailureStreamTimeout,
		FailureStreamRead, FailureStreamNotSupported:
		return true
	}
	return false
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
