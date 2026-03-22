package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EventType represents the type of audit event.
type EventType string

const (
	EventAuthFailure         EventType = "auth_failure"
	EventAuthSuccess         EventType = "auth_success"
	EventRateLimitViolation  EventType = "rate_limit_violation"
	EventBudgetViolation     EventType = "budget_violation"
	EventFilterBlock         EventType = "filter_block"
	EventRedisFailure        EventType = "redis_failure"
	EventProviderFailure     EventType = "provider_failure"
	EventRequestComplete     EventType = "request_complete"
)

// Event represents a security-relevant audit event.
type Event struct {
	RequestID       string
	Timestamp       time.Time
	EventType       EventType
	OrganizationID  string
	TeamID          string
	UserID          *string
	APIKeyID        *string
	IPAddress       string
	UserAgent       string
	Endpoint        string
	Method          string
	StatusCode      int
	ErrorMessage    string
	Metadata        map[string]interface{}
}

// Logger writes audit events to the database.
type Logger struct {
	db *pgxpool.Pool
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

	// Serialize metadata to JSONB
	metadataJSON, err := json.Marshal(event.Metadata)
	if err != nil {
		slog.Error("failed to marshal audit metadata", "error", err, "request_id", event.RequestID)
		metadataJSON = []byte("{}")
	}

	query := `
		INSERT INTO audit_events (
			request_id, timestamp, event_type, organization_id, team_id, user_id,
			api_key_id, ip_address, user_agent, endpoint, method, status_code,
			error_message, metadata
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
		)
	`

	_, err = l.db.Exec(ctx, query,
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
		metadataJSON,
	)

	if err != nil {
		slog.Error("failed to write audit event",
			"error", err,
			"request_id", event.RequestID,
			"event_type", event.EventType,
		)
	}
}

// LogAuthFailure logs an authentication failure.
func (l *Logger) LogAuthFailure(requestID, ip, userAgent, apiKey, reason string) {
	l.Log(Event{
		RequestID:      requestID,
		Timestamp:      time.Now(),
		EventType:      EventAuthFailure,
		OrganizationID: "unknown",
		TeamID:         "unknown",
		IPAddress:      ip,
		UserAgent:      userAgent,
		Endpoint:       "/v1/*",
		Method:         "POST",
		StatusCode:     401,
		ErrorMessage:   reason,
		Metadata: map[string]interface{}{
			"api_key_prefix": truncateAPIKey(apiKey),
		},
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
		Metadata: map[string]interface{}{
			"dimension": dimension,
			"limit":     limit,
		},
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
		Metadata: map[string]interface{}{
			"spent_cents": spentCents,
			"limit_cents": limitCents,
		},
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
		Metadata: map[string]interface{}{
			"filter_type": filterType,
			"reason":      reason,
		},
	})
}

// LogRedisFailure logs a Redis connectivity failure.
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
		Metadata: map[string]interface{}{
			"operation": operation,
			"error":     err.Error(),
		},
	})
}

// truncateAPIKey returns the first 8 characters of an API key for logging.
func truncateAPIKey(apiKey string) string {
	if len(apiKey) > 8 {
		return apiKey[:8] + "..."
	}
	return apiKey
}
