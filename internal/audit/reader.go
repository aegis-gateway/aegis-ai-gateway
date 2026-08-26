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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultReadLimit and MaxReadLimit bound a single page of audit records.
const (
	DefaultReadLimit = 100
	MaxReadLimit     = 1000
)

// Reader serves read-only queries over the audit tables.
//
// Every query is scoped to one organization, and the scope is a required
// argument rather than a filter option. A caller cannot construct a query that
// omits it, which is the point: the audit trail is the evidence one tenant
// shows an auditor, and a missing WHERE clause here would hand them another
// tenant's decision history.
type Reader struct {
	db *pgxpool.Pool
}

// errUnattributedScope is returned for a query scoped to the sentinel
// organization. Events recorded before a caller is identified carry
// [UnattributedOrg], and they belong to the operator rather than to any tenant:
// an authentication failure is by definition not attributable to the tenant
// whose key was not presented. Serving them through the tenant-scoped API would
// hand whoever held a key issued with that organization every other tenant's
// failed-auth history, with the truncated key prefix, IP and user agent
// attached. cmd/keygen takes -org as a free string, so that key is one typo away
// from existing.
var errUnattributedScope = fmt.Errorf(
	"audit read: %q is the sentinel organization for unattributed events, not a tenant", UnattributedOrg)

// NewReader creates a Reader over the given pool.
func NewReader(pool *pgxpool.Pool) *Reader { return &Reader{db: pool} }

// ReadFilter narrows a query. Zero values mean "no constraint" except for
// Limit, which is clamped into [1, MaxReadLimit].
type ReadFilter struct {
	From      time.Time
	To        time.Time
	EventType string
	RequestID string
	// AfterID pages forward. Rows are returned newest first, so the next page
	// is everything with an id below the smallest id already seen.
	BeforeID int64
	Limit    int
}

func (f ReadFilter) limit() int {
	switch {
	case f.Limit <= 0:
		return DefaultReadLimit
	case f.Limit > MaxReadLimit:
		return MaxReadLimit
	default:
		return f.Limit
	}
}

// EventRow is one row of audit_events as returned to a reader.
//
// It mirrors the table and deliberately carries no field that could hold
// request or response content, because no such column exists to populate one.
//
// Migration 013 replaced the single `metadata` object with the typed fields
// below, and this struct followed rather than synthesizing the old shape. A
// response that still presented a `metadata` object would describe storage that
// no longer exists, and this type's whole purpose is to mirror the table.
//
// Reason is the filter's or policy's own message. It names a pattern, a count or
// a rule, and never the matched text.
type EventRow struct {
	ID             int64     `json:"id"`
	RequestID      string    `json:"request_id"`
	Timestamp      time.Time `json:"timestamp"`
	EventType      string    `json:"event_type"`
	OrganizationID *string   `json:"organization_id"`
	TeamID         *string   `json:"team_id"`
	UserID         *string   `json:"user_id"`
	APIKeyID       *string   `json:"api_key_id"`
	IPAddress      *string   `json:"ip_address"`
	Endpoint       *string   `json:"endpoint"`
	Method         *string   `json:"method"`
	StatusCode     *int      `json:"status_code"`
	ErrorMessage   *string   `json:"error_message"`

	APIKeyPrefix   *string `json:"api_key_prefix"`
	LimitDimension *string `json:"limit_dimension"`
	LimitValue     *int64  `json:"limit_value"`
	SpentCents     *int64  `json:"spent_cents"`
	LimitCents     *int64  `json:"limit_cents"`
	FilterType     *string `json:"filter_type"`
	Reason         *string `json:"reason"`
	Provider       *string `json:"provider"`
	Model          *string `json:"model"`
	Mode           *string `json:"mode"`
	Operation      *string `json:"operation"`
	ErrorDetail    *string `json:"error_detail"`
}

// LogRow is one row of audit_logs as returned to a reader.
type LogRow struct {
	ID                 int64     `json:"id"`
	RequestID          string    `json:"request_id"`
	Timestamp          time.Time `json:"timestamp"`
	DurationMs         int       `json:"duration_ms"`
	GatewayOverheadMs  int       `json:"gateway_overhead_ms"`
	StatusCode         int       `json:"status_code"`
	OrganizationID     string    `json:"organization_id"`
	TeamID             string    `json:"team_id"`
	UserID             *string   `json:"user_id"`
	ModelRequested     string    `json:"model_requested"`
	ModelServed        string    `json:"model_served"`
	Provider           string    `json:"provider"`
	Endpoint           string    `json:"endpoint"`
	Stream             bool      `json:"stream"`
	Classification     string    `json:"classification"`
	PromptTokens       int       `json:"prompt_tokens"`
	CompletionTokens   int       `json:"completion_tokens"`
	TotalTokens        int       `json:"total_tokens"`
	EstimatedCostCents int       `json:"estimated_cost_cents"`
	RoutingAttempts    int       `json:"routing_attempts"`
	Failovers          int       `json:"failovers"`
}

// QueryEvents returns audit_events for one organization, newest first.
func (r *Reader) QueryEvents(ctx context.Context, orgID string, f ReadFilter) ([]EventRow, error) {
	if orgID == "" {
		return nil, fmt.Errorf("audit read: organization scope is required")
	}
	if orgID == UnattributedOrg {
		return nil, errUnattributedScope
	}

	// The SQL repeats the exclusion the guard above already enforces. That is
	// deliberate: the guard is one edit away from being refactored out, and this
	// predicate means the sentinel's rows still cannot be selected if it is.
	q := `
		SELECT id, request_id, timestamp, event_type,
		       organization_id, team_id, user_id, api_key_id,
		       ip_address, endpoint, method, status_code,
		       error_message,
		       api_key_prefix, limit_dimension, limit_value,
		       spent_cents, limit_cents, filter_type, reason,
		       provider, model, mode, operation, error_detail
		FROM audit_events
		WHERE organization_id = $1
		  AND organization_id <> $8
		  AND ($2::timestamptz IS NULL OR timestamp >= $2)
		  AND ($3::timestamptz IS NULL OR timestamp < $3)
		  AND ($4::text IS NULL OR event_type = $4)
		  AND ($5::text IS NULL OR request_id = $5)
		  AND ($6::bigint IS NULL OR id < $6)
		ORDER BY id DESC
		LIMIT $7
	`
	rows, err := r.db.Query(ctx, q, orgID,
		nullTime(f.From), nullTime(f.To), nullString(f.EventType),
		nullString(f.RequestID), nullInt64(f.BeforeID), f.limit(),
		UnattributedOrg)
	if err != nil {
		return nil, fmt.Errorf("audit read: querying events: %w", err)
	}
	defer rows.Close()

	out := make([]EventRow, 0, f.limit())
	for rows.Next() {
		var e EventRow
		if err := rows.Scan(&e.ID, &e.RequestID, &e.Timestamp, &e.EventType,
			&e.OrganizationID, &e.TeamID, &e.UserID, &e.APIKeyID,
			&e.IPAddress, &e.Endpoint, &e.Method, &e.StatusCode,
			&e.ErrorMessage,
			&e.APIKeyPrefix, &e.LimitDimension, &e.LimitValue,
			&e.SpentCents, &e.LimitCents, &e.FilterType, &e.Reason,
			&e.Provider, &e.Model, &e.Mode, &e.Operation, &e.ErrorDetail); err != nil {
			return nil, fmt.Errorf("audit read: scanning event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// QueryLogs returns audit_logs for one organization, newest first.
func (r *Reader) QueryLogs(ctx context.Context, orgID string, f ReadFilter) ([]LogRow, error) {
	if orgID == "" {
		return nil, fmt.Errorf("audit read: organization scope is required")
	}
	if orgID == UnattributedOrg {
		return nil, errUnattributedScope
	}

	q := `
		SELECT id, request_id, timestamp, duration_ms, gateway_overhead_ms, status_code,
		       organization_id, team_id, user_id,
		       model_requested, model_served, provider, endpoint, stream, classification,
		       prompt_tokens, completion_tokens, total_tokens, estimated_cost_cents,
		       routing_attempts, failovers
		FROM audit_logs
		WHERE organization_id = $1
		  AND ($2::timestamptz IS NULL OR timestamp >= $2)
		  AND ($3::timestamptz IS NULL OR timestamp < $3)
		  AND ($4::text IS NULL OR request_id = $4)
		  AND ($5::bigint IS NULL OR id < $5)
		ORDER BY id DESC
		LIMIT $6
	`
	rows, err := r.db.Query(ctx, q, orgID,
		nullTime(f.From), nullTime(f.To), nullString(f.RequestID),
		nullInt64(f.BeforeID), f.limit())
	if err != nil {
		return nil, fmt.Errorf("audit read: querying logs: %w", err)
	}
	defer rows.Close()

	out := make([]LogRow, 0, f.limit())
	for rows.Next() {
		var l LogRow
		if err := rows.Scan(&l.ID, &l.RequestID, &l.Timestamp, &l.DurationMs,
			&l.GatewayOverheadMs, &l.StatusCode,
			&l.OrganizationID, &l.TeamID, &l.UserID,
			&l.ModelRequested, &l.ModelServed, &l.Provider, &l.Endpoint, &l.Stream,
			&l.Classification, &l.PromptTokens, &l.CompletionTokens, &l.TotalTokens,
			&l.EstimatedCostCents, &l.RoutingAttempts, &l.Failovers); err != nil {
			return nil, fmt.Errorf("audit read: scanning log: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt64(i int64) any {
	if i == 0 {
		return nil
	}
	return i
}
