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
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/auth"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/httputil"
)

// AuditHandler serves the audit read API.
//
// The API exists so that the decision record is something an operator can
// actually get out of the system, in a form they can hand to an auditor,
// without reaching into Postgres. It is read-only by construction: the Reader
// exposes no write path.
type AuditHandler struct {
	reader *audit.Reader
}

// NewAuditHandler creates an AuditHandler. A nil reader disables the endpoints,
// which is what happens when the gateway runs without a database.
func NewAuditHandler(reader *audit.Reader) *AuditHandler {
	return &AuditHandler{reader: reader}
}

// auditListResponse is the JSON envelope. NextBefore is the cursor to pass back
// as ?before_id= to page further; it is absent on the last page.
type auditListResponse struct {
	Object     string `json:"object"`
	Data       any    `json:"data"`
	NextBefore *int64 `json:"next_before_id,omitempty"`
}

// Events serves GET /aegis/v1/audit/events.
func (h *AuditHandler) Events(w http.ResponseWriter, r *http.Request) {
	reqID := w.Header().Get("X-Request-ID")
	authInfo, filter, format, ok := h.parse(w, r, reqID)
	if !ok {
		return
	}

	rows, err := h.reader.QueryEvents(r.Context(), authInfo.OrganizationID, filter)
	if err != nil {
		httputil.WriteInternalError(w, reqID, "Failed to read audit events")
		return
	}

	if format == "csv" {
		writeCSV(w, reqID, "audit_events",
			[]string{"id", "request_id", "timestamp", "event_type", "organization_id",
				"team_id", "user_id", "api_key_id", "ip_address", "endpoint", "method",
				"status_code", "error_message", "metadata"},
			func(yield func([]string) error) error {
				for _, e := range rows {
					meta := ""
					if len(e.Metadata) > 0 {
						b, _ := json.Marshal(e.Metadata)
						meta = string(b)
					}
					if err := yield([]string{
						strconv.FormatInt(e.ID, 10), e.RequestID,
						e.Timestamp.UTC().Format(time.RFC3339Nano), e.EventType,
						deref(e.OrganizationID), deref(e.TeamID), deref(e.UserID),
						deref(e.APIKeyID), deref(e.IPAddress), deref(e.Endpoint),
						deref(e.Method), derefInt(e.StatusCode), deref(e.ErrorMessage), meta,
					}); err != nil {
						return err
					}
				}
				return nil
			})
		return
	}

	var next *int64
	if len(rows) == filter.Limit {
		next = &rows[len(rows)-1].ID
	}
	writeJSON(w, auditListResponse{Object: "list", Data: rows, NextBefore: next})
}

// Logs serves GET /aegis/v1/audit/logs.
func (h *AuditHandler) Logs(w http.ResponseWriter, r *http.Request) {
	reqID := w.Header().Get("X-Request-ID")
	authInfo, filter, format, ok := h.parse(w, r, reqID)
	if !ok {
		return
	}

	rows, err := h.reader.QueryLogs(r.Context(), authInfo.OrganizationID, filter)
	if err != nil {
		httputil.WriteInternalError(w, reqID, "Failed to read audit logs")
		return
	}

	if format == "csv" {
		writeCSV(w, reqID, "audit_logs",
			[]string{"id", "request_id", "timestamp", "duration_ms", "gateway_overhead_ms",
				"status_code", "organization_id", "team_id", "user_id", "model_requested",
				"model_served", "provider", "endpoint", "stream", "classification",
				"prompt_tokens", "completion_tokens", "total_tokens",
				"estimated_cost_cents", "routing_attempts", "failovers"},
			func(yield func([]string) error) error {
				for _, l := range rows {
					if err := yield([]string{
						strconv.FormatInt(l.ID, 10), l.RequestID,
						l.Timestamp.UTC().Format(time.RFC3339Nano),
						strconv.Itoa(l.DurationMs), strconv.Itoa(l.GatewayOverheadMs),
						strconv.Itoa(l.StatusCode), l.OrganizationID, l.TeamID, deref(l.UserID),
						l.ModelRequested, l.ModelServed, l.Provider, l.Endpoint,
						strconv.FormatBool(l.Stream), l.Classification,
						strconv.Itoa(l.PromptTokens), strconv.Itoa(l.CompletionTokens),
						strconv.Itoa(l.TotalTokens), strconv.Itoa(l.EstimatedCostCents),
						strconv.Itoa(l.RoutingAttempts), strconv.Itoa(l.Failovers),
					}); err != nil {
						return err
					}
				}
				return nil
			})
		return
	}

	var next *int64
	if len(rows) == filter.Limit {
		next = &rows[len(rows)-1].ID
	}
	writeJSON(w, auditListResponse{Object: "list", Data: rows, NextBefore: next})
}

// parse pulls auth, filter and format off the request, writing the error
// response itself and reporting false when the request cannot be served.
func (h *AuditHandler) parse(w http.ResponseWriter, r *http.Request, reqID string) (*auth.AuthInfo, audit.ReadFilter, string, bool) {
	var zero audit.ReadFilter

	authInfo, ok := auth.AuthFromContext(r.Context())
	if !ok {
		httputil.WriteAuthError(w, reqID, "Not authenticated")
		return nil, zero, "", false
	}
	// An organization-less key cannot be scoped, so it is refused rather than
	// served an unscoped query.
	if authInfo.OrganizationID == "" {
		httputil.WriteAuthError(w, reqID, "API key carries no organization; cannot scope an audit query")
		return nil, zero, "", false
	}
	// audit.UnattributedOrg is the sentinel recorded for events that happen
	// before a caller is identified. It is not a tenant, and a key carrying it
	// would otherwise scope to every authentication failure in the deployment.
	// cmd/keygen takes -org as a free string, so such a key is one typo away.
	if authInfo.OrganizationID == audit.UnattributedOrg {
		httputil.WriteAuthError(w, reqID,
			"API key organization is the reserved sentinel for unattributed events; cannot scope an audit query")
		return nil, zero, "", false
	}

	q := r.URL.Query()

	format := strings.ToLower(q.Get("format"))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" {
		httputil.WriteBadRequestError(w, reqID, "format must be json or csv")
		return nil, zero, "", false
	}

	filter := audit.ReadFilter{
		EventType: q.Get("event_type"),
		RequestID: q.Get("request_id"),
	}

	for _, p := range []struct {
		name string
		dst  *time.Time
	}{{"from", &filter.From}, {"to", &filter.To}} {
		if v := q.Get(p.name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				httputil.WriteBadRequestError(w, reqID,
					fmt.Sprintf("%s must be an RFC3339 timestamp", p.name))
				return nil, zero, "", false
			}
			*p.dst = t
		}
	}

	if v := q.Get("before_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			httputil.WriteBadRequestError(w, reqID, "before_id must be a non-negative integer")
			return nil, zero, "", false
		}
		filter.BeforeID = n
	}

	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			httputil.WriteBadRequestError(w, reqID, "limit must be a positive integer")
			return nil, zero, "", false
		}
		if n > audit.MaxReadLimit {
			httputil.WriteBadRequestError(w, reqID,
				fmt.Sprintf("limit must not exceed %d", audit.MaxReadLimit))
			return nil, zero, "", false
		}
		filter.Limit = n
	}
	// Resolve the effective limit here so the caller can compare it against the
	// row count to decide whether another page exists.
	if filter.Limit == 0 {
		filter.Limit = audit.DefaultReadLimit
	}

	// Availability is checked last, once the request is known to be well formed.
	// A malformed query is a 400 whether or not the database happens to be up,
	// and reporting 503 for it would send the caller chasing an outage that is
	// not there.
	if h.reader == nil {
		httputil.WriteServiceUnavailableError(w, reqID,
			"Audit read API unavailable: the gateway is running without a database")
		return nil, zero, "", false
	}

	return authInfo, filter, format, true
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// writeCSV streams rows with a header. Headers are set before the first write,
// because once a row is flushed the status code can no longer change.
func writeCSV(w http.ResponseWriter, reqID, name string, header []string, rows func(func([]string) error) error) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", name+"-"+time.Now().UTC().Format("20060102")+".csv"))

	cw := csv.NewWriter(w)
	if err := cw.Write(header); err != nil {
		return
	}
	if err := rows(cw.Write); err != nil {
		return
	}
	cw.Flush()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefInt(i *int) string {
	if i == nil {
		return ""
	}
	return strconv.Itoa(*i)
}
